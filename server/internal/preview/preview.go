// SPDX-License-Identifier: Apache-2.0

// Package preview runs the preview environments of pull requests (F03, REQ-PREV-001 to REQ-PREV-014).
// A PR event creates the environment pr-<n> from the base environment, builds the PR commit, posts the URL on the PR,
// and the environment is deleted when the PR closes (after the grace time) or after the TTL.
package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
)

// Names that Git users see.
const (
	// LabelName is the label a maintainer adds to a PR from a fork before it is built (REQ-PREV-009).
	LabelName = "polipan-preview"
	// StatusContext is the context of the commit status.
	StatusContext = "polipan/preview"
	// CommentMarker is the first line of the comment, so a human sees which comment is the one of Polipan.
	CommentMarker = "<!-- polipan-preview -->"
)

// States of a preview (F03 States). They are stored in settings.preview_state of the environment.
const (
	StateCreating = "creating"
	StateActive   = "active"
	StateFailed   = "failed"
	StateClosing  = "closing"
	StateDeleted  = "deleted"
)

// Defaults of the project settings (glossary).
const (
	DefaultMaxActive    = 10
	DefaultTTLDays      = 7
	DefaultGraceMinutes = 60
)

// Config is the previews block of the project settings.
type Config struct {
	Enabled             bool              `json:"enabled"`
	BaseEnvironmentID   string            `json:"base_environment_id"`
	MaxActive           *int              `json:"max_active"`
	TTLDays             *int              `json:"ttl_days"`
	CleanupGraceMinutes *int              `json:"cleanup_grace_minutes"`
	FromForks           bool              `json:"from_forks"`
	NodeSelector        map[string]string `json:"node_selector"`
	SeedCommand         []string          `json:"seed_command"`
	Strategy            string            `json:"strategy"`
}

func intOr(p *int, def int) int {
	if p == nil || *p < 0 {
		return def
	}
	return *p
}

// Max is preview_max_active.
func (c Config) Max() int { return intOr(c.MaxActive, DefaultMaxActive) }

// TTL is preview_ttl: the life of a preview since the last push. 0 means no limit.
func (c Config) TTL() time.Duration {
	return time.Duration(intOr(c.TTLDays, DefaultTTLDays)) * 24 * time.Hour
}

// Grace is preview_cleanup_grace: the time between the close of the PR and the delete of the environment.
func (c Config) Grace() time.Duration {
	return time.Duration(intOr(c.CleanupGraceMinutes, DefaultGraceMinutes)) * time.Minute
}

// DeployStrategy is the strategy of a preview deployment: recreate by default (REQ-PREV-004).
func (c Config) DeployStrategy() string {
	if c.Strategy == "blue_green" || c.Strategy == "recreate" {
		return c.Strategy
	}
	return "recreate"
}

// ProviderFunc makes the Git provider of a connection. It is used to post statuses and comments.
type ProviderFunc func(ctx context.Context, orgID, connectionID string) (git.Provider, error)

// Deps are the parts of the preview service.
type Deps struct {
	Pool    *pgxpool.Pool
	Store   *store.Store
	Orch    orchestrator.Orchestrator
	Secrets *secrets.Manager
	Clock   clock.Clock
	Log     *slog.Logger
	// Provider posts the commit status and the comment. Nil: nothing is posted.
	Provider ProviderFunc
	// ExternalURL is the address of the panel. It is the target of the commit status when the preview has no URL.
	ExternalURL string
	// Interval is how often the service looks at the previews (default 5 s).
	Interval time.Duration
}

// Service is the preview service.
type Service struct {
	Deps
	mu sync.Mutex
}

// New makes the service. Call Run in a goroutine.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Interval == 0 {
		d.Interval = 5 * time.Second
	}
	return &Service{Deps: d}
}

// Run looks at the previews until ctx ends: it reports finished deployments, deletes expired and closed previews.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// ---- names ----

// Slug is the name of the environment of a PR (REQ-PREV-003).
func Slug(number int) string { return "pr-" + strconv.Itoa(number) }

// BranchSlug is the name of the environment of a preview that was made by hand for a branch.
func BranchSlug(branch string) string {
	sum := sha256.Sum256([]byte(branch))
	name := sanitize(branch)
	if len(name) > 20 {
		name = strings.Trim(name[:20], "-")
	}
	return "br-" + name + "-" + hex.EncodeToString(sum[:])[:4]
}

// BranchNumber gives a negative number to a preview of a branch, so it cannot clash with a PR number.
func BranchNumber(branch string) int {
	sum := sha256.Sum256([]byte(branch))
	n := int(uint32(sum[0])<<24|uint32(sum[1])<<16|uint32(sum[2])<<8|uint32(sum[3])) & 0x3fffffff
	return -(n + 1)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Label is the one DNS label of the host of a service in a preview: <slug>-<service> (REQ-PREV-005).
// It has at most 63 characters. A longer service name is cut and gets a 4 character hash.
func Label(slug, service string) string {
	prefix := slug + "-"
	name := sanitize(service)
	if name == "" {
		name = "app"
	}
	if len(prefix)+len(name) <= 63 {
		return prefix + name
	}
	sum := sha256.Sum256([]byte(service))
	keep := 63 - len(prefix) - 5
	cut := strings.Trim(name[:min(len(name), max(keep, 1))], "-")
	return prefix + cut + "-" + hex.EncodeToString(sum[:])[:4]
}

// Host is the host of a service in a preview, or "" when the org has no preview domain.
func Host(slug, service, domain string) string {
	domain = strings.Trim(strings.ToLower(domain), ".")
	if domain == "" {
		return ""
	}
	return Label(slug, service) + "." + domain
}

// ---- settings ----

// envState is the part of a preview environment that the service reads and writes.
type envState struct {
	ID, ProjectID, OrgID string
	Slug, Repo           string
	Number               int
	BaseEnvID            string
	State                string
	Settings             map[string]any
	LastPush, Expires    *time.Time
}

func (e *envState) str(k string) string { v, _ := e.Settings[k].(string); return v }
func (e *envState) boolean(k string) bool {
	v, _ := e.Settings[k].(bool)
	return v
}

// pending is settings.preview_pending: service id to the id of its newest deployment.
func (e *envState) pending() map[string]string {
	out := map[string]string{}
	if m, ok := e.Settings["preview_pending"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

const envCols = `e.id, e.project_id, p.org_id, e.slug, COALESCE(e.preview_repo,''), COALESCE(e.preview_pr_number,0), COALESCE(e.base_env_id,''),
	e.settings, e.preview_last_push_at, e.preview_expires_at`

func scanEnv(row interface{ Scan(...any) error }) (*envState, error) {
	var e envState
	var raw []byte
	if err := row.Scan(&e.ID, &e.ProjectID, &e.OrgID, &e.Slug, &e.Repo, &e.Number, &e.BaseEnvID, &raw, &e.LastPush, &e.Expires); err != nil {
		return nil, err
	}
	e.Settings = map[string]any{}
	_ = json.Unmarshal(raw, &e.Settings)
	e.State, _ = e.Settings["preview_state"].(string)
	return &e, nil
}

// setSettings merges keys into the settings of the environment.
func (s *Service) setSettings(ctx context.Context, envID string, kv map[string]any) error {
	raw, _ := json.Marshal(kv)
	_, err := s.Pool.Exec(ctx, `UPDATE environments SET settings = settings || $2::jsonb, updated_at = now() WHERE id = $1`, envID, raw)
	return err
}

// projectConfig reads the previews block of the settings of a project.
func (s *Service) projectConfig(ctx context.Context, projectID string) (Config, error) {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT settings FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).Scan(&raw); err != nil {
		return Config{}, err
	}
	var st struct {
		Previews Config `json:"previews"`
	}
	_ = json.Unmarshal(raw, &st)
	return st.Previews, nil
}

// previewDomain is the org setting preview_domain.
func (s *Service) previewDomain(ctx context.Context, orgID string) string {
	var d string
	_ = s.Pool.QueryRow(ctx, `SELECT COALESCE(settings->>'preview_domain','') FROM orgs WHERE id = $1`, orgID).Scan(&d)
	return d
}

// baseEnvironment finds the base environment of a project (REQ-PREV-002): the one of the settings, else the staging
// environment, else production.
func (s *Service) baseEnvironment(ctx context.Context, projectID string, cfg Config) (string, error) {
	if cfg.BaseEnvironmentID != "" {
		var id string
		err := s.Pool.QueryRow(ctx, `SELECT id FROM environments WHERE id = $1 AND project_id = $2 AND kind <> 'preview' AND deleted_at IS NULL`,
			cfg.BaseEnvironmentID, projectID).Scan(&id)
		return id, err
	}
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM environments WHERE project_id = $1 AND kind IN ('staging','production') AND deleted_at IS NULL
		ORDER BY (kind = 'staging') DESC, created_at LIMIT 1`, projectID).Scan(&id)
	return id, err
}

var errNoBase = errors.New("preview: the project has no base environment")
