// SPDX-License-Identifier: Apache-2.0

// Package backup is the server side of F10: backup targets and policies, the schedule, the runs,
// the restore checks and the restores. The server only schedules and records. The agent on the
// node that holds the data does the work (REQ-BKP-009).
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
)

// Sender sends commands to the agents. *gateway.Gateway implements it.
type Sender interface {
	Send(ctx context.Context, nodeID string, cmd *pb.Command) (*pb.CommandAck, error)
	Connected(nodeID string) bool
}

// Deps are the parts the service needs.
type Deps struct {
	DB      *pgxpool.Pool
	Secrets *secrets.Manager
	Sender  Sender
	Orch    orchestrator.Orchestrator
	Clock   clock.Clock
	Log     *slog.Logger
	// Tick is how often the schedule is checked. Default 15 s.
	Tick time.Duration
	// RunTimeout and CheckTimeout limit one run and one restore check (default 2 h, REQ-BKP edge cases).
	RunTimeout   time.Duration
	CheckTimeout time.Duration
	// DatabaseVersion tells the major version of the control-plane database (for the temporary container of its restore check).
	// Nil means it is read from the database.
	DatabaseVersion func(ctx context.Context) (int, error)
	// LocalRoot is the only folder tree where "local" targets may live. Default /var/lib/polipan/backups.
	LocalRoot string
	// SSRF gives the deny list of an org. The host of an S3 endpoint and of an SFTP target is checked against it
	// (REQ-SEC-104, R12). Nil means no check (tests).
	SSRF func(ctx context.Context, orgID string) ssrf.Policy
	// StableWait is how long a run waits for a deployment of the service to end (REQ edge case, default 10 min).
	StableWait time.Duration
}

// Service is the backup service.
type Service struct {
	Deps
	store *store.Store

	mu      sync.Mutex
	working map[string]bool // runs and checks that this process executes
	wg      sync.WaitGroup
}

// New makes the service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Tick == 0 {
		d.Tick = 15 * time.Second
	}
	if d.RunTimeout == 0 {
		d.RunTimeout = 2 * time.Hour
	}
	if d.CheckTimeout == 0 {
		d.CheckTimeout = 2 * time.Hour
	}
	if d.LocalRoot == "" {
		d.LocalRoot = DefaultLocalRoot
	}
	if d.StableWait == 0 {
		d.StableWait = 10 * time.Minute
	}
	return &Service{Deps: d, store: store.New(d.DB), working: map[string]bool{}}
}

// DefaultLocalRoot is the folder for local targets.
const DefaultLocalRoot = "/var/lib/polipan/backups"

// Errors the API maps to answers.
var (
	ErrNotFound       = errors.New("backup: not found")
	ErrInvalid        = errors.New("backup: invalid")
	ErrTargetInUse    = errors.New("backup: a policy uses the target")
	ErrConflict       = errors.New("backup: conflict")
	ErrNoNode         = errors.New("backup: no node can run this")
	ErrNotRestorable  = errors.New("backup: the snapshot cannot be restored")
	ErrControlPlane   = errors.New("backup: the control-plane policy cannot be changed this way")
	ErrDeployRunning  = errors.New("backup: a deployment is running")
	ErrAlreadyRunning = errors.New("backup: a restore is running")
)

// invalidf is a 422.
type invalidError struct{ Field, Msg string }

func (e *invalidError) Error() string { return e.Msg }
func (e *invalidError) Unwrap() error { return ErrInvalid }

// InvalidField returns the field of an invalid-input error, or "".
func InvalidField(err error) string {
	var ie *invalidError
	if errors.As(err, &ie) {
		return ie.Field
	}
	return ""
}

func invalid(field, format string, a ...any) error {
	return &invalidError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

type dbq interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- secrets of targets and policies: they live in a hidden system project of the org ----

const systemSlug = "polipan-system"

// systemProject returns the id of the hidden project that holds the secrets of backups. It makes the project when it is missing.
func (s *Service) systemProject(ctx context.Context, q dbq, orgID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM projects WHERE org_id = $1 AND slug = $2`, orgID, systemSlug).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = store.NewID("prj")
	if _, err := q.Exec(ctx, `INSERT INTO projects (id, org_id, name, slug, settings) VALUES ($1,$2,'Polipan system',$3,'{"system": true}')
		ON CONFLICT (org_id, slug) DO NOTHING`, id, orgID, systemSlug); err != nil {
		return "", err
	}
	err = q.QueryRow(ctx, `SELECT id FROM projects WHERE org_id = $1 AND slug = $2`, orgID, systemSlug).Scan(&id)
	return id, err
}

// putSecret stores a value as a new secret of the system project and returns its id.
func (s *Service) putSecret(ctx context.Context, tx pgx.Tx, orgID, name, value string) (string, error) {
	if s.Secrets == nil {
		return "", errors.New("the secrets store is not set up")
	}
	project, err := s.systemProject(ctx, tx, orgID)
	if err != nil {
		return "", err
	}
	id := store.NewID("sec")
	if _, err := tx.Exec(ctx, `INSERT INTO secrets (id, project_id, name, current_version) VALUES ($1,$2,$3,1)`, id, project, name); err != nil {
		return "", err
	}
	if err := s.Secrets.Provider.Put(ctx, tx, project, id, 1, []byte(value), ""); err != nil {
		return "", err
	}
	return id, nil
}

// newSecretVersion writes a new value of a secret.
func (s *Service) newSecretVersion(ctx context.Context, tx pgx.Tx, secretID, value string) error {
	var project string
	var version int
	if err := tx.QueryRow(ctx, `SELECT project_id, current_version + 1 FROM secrets WHERE id = $1 FOR UPDATE`, secretID).Scan(&project, &version); err != nil {
		return notFound(err)
	}
	if err := s.Secrets.Provider.Put(ctx, tx, project, secretID, version, []byte(value), ""); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE secrets SET current_version = $2 WHERE id = $1`, secretID, version)
	return err
}

// secretValue reads the current value of a secret.
func (s *Service) secretValue(ctx context.Context, secretID string) (string, error) {
	if s.Secrets == nil {
		return "", errors.New("the secrets store is not set up")
	}
	var project string
	var version int
	if err := s.DB.QueryRow(ctx, `SELECT project_id, current_version FROM secrets WHERE id = $1 AND deleted_at IS NULL`, secretID).Scan(&project, &version); err != nil {
		return "", notFound(err)
	}
	v, err := s.Secrets.Provider.Get(ctx, s.DB, project, secretID, version)
	return string(v), err
}

// deleteSecret removes a secret (soft delete, the history stays).
func (s *Service) deleteSecret(ctx context.Context, q dbq, secretID string) error {
	_, err := q.Exec(ctx, `UPDATE secrets SET deleted_at = now() WHERE id = $1`, secretID)
	return err
}

// ---- events: SSE for the UI and alerts ----

// event writes an event of topic "backup" for the stream of the UI.
func (s *Service) event(ctx context.Context, orgID, objectID string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	if _, err := s.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'backup',$2,$3)`, orgID, objectID, b); err != nil {
		s.Log.Warn("backup event", "error", err)
	}
}

// alert writes an alert event. Delivery to channels is M8; the event is the record (the UI reads it).
func (s *Service) alert(ctx context.Context, orgID, rule, severity, policyID string, details map[string]any) {
	p := map[string]any{"rule": rule, "severity": severity, "state": "firing", "policy_id": policyID}
	for k, v := range details {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	if _, err := s.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'alert',$2,$3)`, orgID, policyID, b); err != nil {
		s.Log.Warn("backup alert", "error", err)
	}
}

func (s *Service) now() time.Time { return s.Clock.Now() }
