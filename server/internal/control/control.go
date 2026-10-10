// SPDX-License-Identifier: Apache-2.0

// Package control is the control side of the server: it implements orchestrator.Orchestrator.
// It builds the desired state of the nodes, runs the recreate deploy engine,
// watches the node states and makes join tokens.
package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// Sender is the part of the gateway that control uses. *gateway.Gateway implements it.
type Sender interface {
	Send(ctx context.Context, nodeID string, cmd *pb.Command) (*pb.CommandAck, error)
	SendCollect(ctx context.Context, nodeID string, cmd *pb.Command) ([]byte, *pb.CommandAck, error)
	Push(nodeID string, ds *pb.DesiredState) bool
	Connected(nodeID string) bool
}

// Deps are the parts control needs. Sender may be nil (the CLI commands do not need it).
type Deps struct {
	DB     *pgxpool.Pool
	Clock  clock.Clock // default clock.Real
	Log    *slog.Logger
	Config config.Config
	CA     *pki.CA
	Sender Sender
	// PanelUpstream is the address of the panel inside the docker network of the node.
	// Default "polipan-server:8080". The panel route uses the host of Config.ExternalURL.
	PanelUpstream string
	// MonitorInterval is how often Run checks the node states (default 1 s).
	MonitorInterval time.Duration
}

// Control implements orchestrator.Orchestrator.
type Control struct {
	Deps

	// startedAt gives every node a fresh start after a server restart: agents need a moment
	// to reconnect, so an old heartbeat must not make a node suspect at once.
	startedAt time.Time

	runCtx context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	active map[string]bool // deployments run by this process
}

var _ orchestrator.Orchestrator = (*Control)(nil)
var _ gateway.StateProvider = (*Control)(nil)

// New creates the Control. It does not touch the database. Call Run in a goroutine.
func New(d Deps) *Control {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.PanelUpstream == "" {
		d.PanelUpstream = "polipan-server:8080"
	}
	if d.MonitorInterval == 0 {
		d.MonitorInterval = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Control{Deps: d, runCtx: ctx, stop: cancel, active: map[string]bool{}}
}

// Run marks the deployments that a former server left running as failed (server_restarted),
// then watches the node states until ctx ends. On exit it stops the deployments and waits for them.
func (c *Control) Run(ctx context.Context) error {
	defer c.Close()
	c.startedAt = c.Clock.Now()
	if err := c.recoverDeployments(ctx); err != nil {
		return err
	}
	t := time.NewTicker(c.MonitorInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := c.CheckNodes(ctx); err != nil && ctx.Err() == nil {
				c.Log.Error("node monitor", "err", err)
			}
		}
	}
}

// Close stops the running deployments and waits for them.
func (c *Control) Close() {
	c.stop()
	c.wg.Wait()
}

type dbq interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ---- desired state changes ----

// change runs fn in a transaction. fn returns the nodes whose desired state changed.
// For each node the version is bumped (in the same transaction), the new document is built,
// and after the commit it is pushed to the stream of the node.
func (c *Control) change(ctx context.Context, fn func(tx pgx.Tx) ([]string, error)) error {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids, err := fn(tx)
	if err != nil {
		return err
	}
	docs, err := c.bumpNodes(ctx, tx, ids)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for id, ds := range docs {
		if c.Sender != nil {
			c.Sender.Push(id, ds)
		}
	}
	return nil
}

func (c *Control) bumpNodes(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*pb.DesiredState, error) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	sorted := make([]string, 0, len(set))
	for id := range set {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted) // fixed lock order
	docs := map[string]*pb.DesiredState{}
	for _, id := range sorted {
		var v int64
		err := tx.QueryRow(ctx,
			`UPDATE nodes SET desired_state_version = desired_state_version + 1, updated_at = now()
			  WHERE id = $1 RETURNING desired_state_version`, id).Scan(&v)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // node was deleted
		}
		if err != nil {
			return nil, err
		}
		ds, err := c.buildDesiredState(ctx, tx, id, v)
		if err != nil {
			return nil, err
		}
		docs[id] = ds
	}
	return docs, nil
}

func (c *Control) allNodeIDs(ctx context.Context, q dbq) ([]string, error) {
	return queryStrings(ctx, q, `SELECT id FROM nodes ORDER BY id`)
}

func queryStrings(ctx context.Context, q dbq, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DesiredState returns the current document of a node (gateway.StateProvider).
func (c *Control) DesiredState(ctx context.Context, nodeID string) (*pb.DesiredState, error) {
	tx, err := c.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var v int64
	if err := tx.QueryRow(ctx, `SELECT desired_state_version FROM nodes WHERE id = $1`, nodeID).Scan(&v); err != nil {
		return nil, err
	}
	return c.buildDesiredState(ctx, tx, nodeID, v)
}

// ---- Orchestrator: stop, start, domains, remove, logs ----

// SetRunning stops or starts the service. No release is made.
func (c *Control) SetRunning(ctx context.Context, serviceID string, running bool) error {
	desired, from, to := "running", "stopped", "live"
	if !running {
		desired, from, to = "stopped", "live", "stopped"
	}
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		tag, err := tx.Exec(ctx,
			`UPDATE services SET desired_state = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, serviceID, desired)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, orchestrator.ErrNotFound
		}
		if running {
			_, err = tx.Exec(ctx, `UPDATE placements SET role = $2, updated_at = now() WHERE service_id = $1 AND role = $3`, serviceID, to, from)
		} else {
			_, err = tx.Exec(ctx, `UPDATE placements SET role = 'stopped', updated_at = now() WHERE service_id = $1 AND role <> 'stopped'`, serviceID)
		}
		if err != nil {
			return nil, err
		}
		return queryStrings(ctx, tx, `SELECT DISTINCT node_id FROM placements WHERE service_id = $1`, serviceID)
	})
}

// DomainsChanged makes a new desired state (ingress) for all nodes. Workloads do not change.
func (c *Control) DomainsChanged(ctx context.Context, serviceID string) error {
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) { return c.allNodeIDs(ctx, tx) })
}

// ServiceRemoved removes the containers of the service and its placements.
func (c *Control) ServiceRemoved(ctx context.Context, serviceID string) error {
	type pl struct{ id, node string }
	rows, err := c.DB.Query(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}
	var pls []pl
	for rows.Next() {
		var p pl
		if err := rows.Scan(&p.id, &p.node); err != nil {
			rows.Close()
			return err
		}
		pls = append(pls, p)
	}
	rows.Close()
	for _, p := range pls {
		c.removeContainer(ctx, p.node, p.id)
	}
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE service_id = $1`, serviceID); err != nil {
			return nil, err
		}
		// The service row may be gone already, so all nodes get a new state (cheap, M1 has few nodes).
		return c.allNodeIDs(ctx, tx)
	})
}

// removeContainer stops and removes a container. Errors are logged only:
// the new desired state removes the container anyway.
func (c *Control) removeContainer(ctx context.Context, nodeID, placementID string) {
	if c.Sender == nil || !c.Sender.Connected(nodeID) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmds := []*pb.Command{
		{CommandId: gateway.NewID("cmd"), Body: &pb.Command_StopWorkload{StopWorkload: &pb.StopWorkload{PlacementId: placementID, GraceSeconds: 10}}},
		{CommandId: gateway.NewID("cmd"), Body: &pb.Command_RemoveWorkload{RemoveWorkload: &pb.RemoveWorkload{PlacementId: placementID}}},
	}
	for _, cmd := range cmds {
		if ack, err := c.Sender.Send(cctx, nodeID, cmd); err != nil || !ack.GetOk() {
			c.Log.Warn("remove container", "placement", placementID, "err", err, "ack", ack.GetErrorMessage())
		}
	}
}

// Logs returns the last lines of the container. The agent returns its default tail (200 lines),
// the server cuts it to tail lines.
func (c *Control) Logs(ctx context.Context, serviceID string, tail int) (string, error) {
	var placementID, nodeID string
	err := c.DB.QueryRow(ctx,
		`SELECT p.id, p.node_id FROM placements p JOIN services s ON s.id = p.service_id
		  WHERE p.service_id = $1 AND s.deleted_at IS NULL
		  ORDER BY (p.role = 'live') DESC, p.created_at DESC LIMIT 1`, serviceID).Scan(&placementID, &nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", orchestrator.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if c.Sender == nil || !c.Sender.Connected(nodeID) {
		return "", orchestrator.ErrNoNode
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, ack, err := c.Sender.SendCollect(cctx, nodeID, &pb.Command{
		Body: &pb.Command_StreamLogs{StreamLogs: &pb.StreamLogs{PlacementId: placementID, Follow: false}},
	})
	if err != nil {
		return "", fmt.Errorf("logs: %w", err)
	}
	if !ack.GetOk() {
		return "", fmt.Errorf("logs: %s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
	}
	return lastLines(string(data), tail), nil
}

func lastLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	trimmed := strings.TrimRight(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}

// ---- join tokens ----

// CreateJoinToken makes a single-use token. Only its hash is stored.
func (c *Control) CreateJoinToken(ctx context.Context, orgID, nodeName string, labels map[string]string, actorID string) (orchestrator.JoinToken, error) {
	if c.CA == nil {
		return orchestrator.JoinToken{}, errors.New("control: no CA")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return orchestrator.JoinToken{}, err
	}
	token := "pj_" + base64.RawURLEncoding.EncodeToString(b)
	if labels == nil {
		labels = map[string]string{}
	}
	lb, _ := json.Marshal(labels)
	expires := c.Clock.Now().Add(c.Config.JoinTokenTTL)
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	_, err := c.DB.Exec(ctx,
		`INSERT INTO node_join_tokens (id, org_id, token_hash, node_name, labels, expires_at, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		gateway.NewID("jtk"), orgID, gateway.HashToken(token), nodeName, lb, expires, actor)
	if err != nil {
		return orchestrator.JoinToken{}, err
	}
	return orchestrator.JoinToken{Token: token, ExpiresAt: expires, CAFingerprint: c.CA.Fingerprint()}, nil
}
