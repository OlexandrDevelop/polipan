// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/secrets"
)

// ---- Git secret values ----

// GitSecrets keeps Git secret values (tokens, app keys, webhook secrets, deploy keys) in
// git_secrets, AES-256-GCM with a key made from the KEK. It implements git.KeyStore.
// These values are never injected into containers, so they are not rows of "secrets".
type GitSecrets struct {
	Pool *pgxpool.Pool
	key  []byte
}

// NewGitSecrets makes the store. kek is the master key.
func NewGitSecrets(pool *pgxpool.Pool, kek []byte) *GitSecrets {
	sum := sha256.Sum256(append([]byte("polipan-git-secrets:"), kek...))
	return &GitSecrets{Pool: pool, key: sum[:]}
}

var _ git.KeyStore = (*GitSecrets)(nil)

// Put implements git.KeyStore.
func (g *GitSecrets) Put(ctx context.Context, scope git.Scope, _ string, value string) (string, error) {
	id := NewID("gsc")
	nonce, ct, err := secrets.Seal(g.key, id, 1, []byte(value))
	if err != nil {
		return "", err
	}
	_, err = g.Pool.Exec(ctx, `INSERT INTO git_secrets (id, org_id, nonce, ciphertext) VALUES ($1,$2,$3,$4)`, id, scope.OrgID, nonce, ct)
	return id, err
}

// Get implements git.KeyStore.
func (g *GitSecrets) Get(ctx context.Context, id string) (string, error) {
	var nonce, ct []byte
	if err := g.Pool.QueryRow(ctx, `SELECT nonce, ciphertext FROM git_secrets WHERE id = $1`, id).Scan(&nonce, &ct); err != nil {
		if errors.Is(notFound(err), ErrNotFound) {
			return "", git.ErrNoSecret
		}
		return "", err
	}
	plain, err := secrets.Open(g.key, id, 1, nonce, ct)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Delete implements git.KeyStore.
func (g *GitSecrets) Delete(ctx context.Context, id string) error {
	_, err := g.Pool.Exec(ctx, `DELETE FROM git_secrets WHERE id = $1`, id)
	return err
}

// ---- connections ----

// GitAuth is git_connections.auth: public ids and the ids of the secret values.
type GitAuth struct {
	AppID            string `json:"app_id,omitempty"`
	InstallationID   string `json:"installation_id,omitempty"`
	SecretRef        string `json:"secret_ref,omitempty"`         // token or private key (git_secrets)
	WebhookSecretRef string `json:"webhook_secret_ref,omitempty"` // git_secrets
}

// GitConnection is a row of git_connections.
type GitConnection struct {
	ID       string
	OrgID    string
	Provider string
	Name     string
	BaseURL  *string
	Auth     GitAuth
}

const gitConnSelect = `SELECT id, org_id, provider, name, base_url, auth FROM git_connections`

func scanGitConn(row interface{ Scan(...any) error }) (*GitConnection, error) {
	var c GitConnection
	var raw []byte
	if err := row.Scan(&c.ID, &c.OrgID, &c.Provider, &c.Name, &c.BaseURL, &raw); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(raw, &c.Auth)
	return &c, nil
}

// CreateGitConnection stores a connection.
func (s *Store) CreateGitConnection(ctx context.Context, c GitConnection) (*GitConnection, error) {
	c.ID = NewID("git")
	auth, _ := json.Marshal(c.Auth)
	_, err := s.Pool.Exec(ctx, `INSERT INTO git_connections (id, org_id, provider, name, base_url, auth) VALUES ($1,$2,$3,$4,$5,$6)`,
		c.ID, c.OrgID, c.Provider, c.Name, c.BaseURL, auth)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListGitConnections lists the connections of the org.
func (s *Store) ListGitConnections(ctx context.Context, orgID string) ([]GitConnection, error) {
	rows, err := s.Pool.Query(ctx, gitConnSelect+` WHERE org_id = $1 ORDER BY name, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitConnection{}
	for rows.Next() {
		c, err := scanGitConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetGitConnection reads one connection of the org. An empty orgID reads any org (webhooks).
func (s *Store) GetGitConnection(ctx context.Context, orgID, id string) (*GitConnection, error) {
	return scanGitConn(s.Pool.QueryRow(ctx, gitConnSelect+` WHERE id = $1 AND ($2 = '' OR org_id = $2)`, id, orgID))
}

// DeleteGitConnection removes a connection and returns it (the caller deletes its secrets).
// A connection that services still use cannot be deleted (ErrInUse).
func (s *Store) DeleteGitConnection(ctx context.Context, orgID, id string) (*GitConnection, error) {
	c, err := s.GetGitConnection(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	var used bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM services WHERE deleted_at IS NULL AND source_type = 'git'
		AND config->'git'->>'connection_id' = $1)`, id).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, ErrInUse
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM git_connections WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return c, nil
}

// ErrInUse means an object is still used by services.
var ErrInUse = errors.New("store: in use")

// ---- webhooks ----

// Webhook is the target of /webhooks/git/{id}: a connection (git_) or a service webhook (whk_).
type Webhook struct {
	ID           string
	OrgID        string
	Provider     string
	SecretRef    string
	ConnectionID string // set for a connection webhook
	ServiceID    string // set for a service webhook
	Kind         string // git or deploy_hook
}

// GetWebhook finds a webhook by its id.
func (s *Store) GetWebhook(ctx context.Context, id string) (*Webhook, error) {
	if c, err := s.GetGitConnection(ctx, "", id); err == nil {
		return &Webhook{ID: c.ID, OrgID: c.OrgID, Provider: c.Provider, SecretRef: c.Auth.WebhookSecretRef, ConnectionID: c.ID, Kind: "git"}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var w Webhook
	err := s.Pool.QueryRow(ctx, `SELECT w.id, p.org_id, w.provider, w.secret_ref, w.service_id, w.kind
		FROM git_webhooks w JOIN services sv ON sv.id = w.service_id AND sv.deleted_at IS NULL
		JOIN environments e ON e.id = sv.environment_id JOIN projects p ON p.id = e.project_id
		WHERE w.id = $1`, id).Scan(&w.ID, &w.OrgID, &w.Provider, &w.SecretRef, &w.ServiceID, &w.Kind)
	if err != nil {
		return nil, notFound(err)
	}
	return &w, nil
}

// PutServiceWebhook makes (or replaces) the webhook of a service. It returns the id and the old secret ref.
func (s *Store) PutServiceWebhook(ctx context.Context, serviceID, kind, provider, secretRef string) (id, oldRef string, err error) {
	_ = s.Pool.QueryRow(ctx, `SELECT id, secret_ref FROM git_webhooks WHERE service_id = $1 AND kind = $2`, serviceID, kind).Scan(&id, &oldRef)
	if id == "" {
		id = NewID("whk")
		_, err = s.Pool.Exec(ctx, `INSERT INTO git_webhooks (id, service_id, kind, provider, secret_ref) VALUES ($1,$2,$3,$4,$5)`,
			id, serviceID, kind, provider, secretRef)
		return id, "", err
	}
	_, err = s.Pool.Exec(ctx, `UPDATE git_webhooks SET provider = $2, secret_ref = $3 WHERE id = $1`, id, provider, secretRef)
	return id, oldRef, err
}

// SeenDelivery records a delivery id and tells if it was seen in the last 24 h (a replay).
// Older records are removed on the way.
func (s *Store) SeenDelivery(ctx context.Context, webhookID, deliveryID string, now time.Time) (bool, error) {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM git_webhook_deliveries WHERE received_at < $1`, now.Add(-24*time.Hour)); err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO git_webhook_deliveries (webhook_id, delivery_id, received_at) VALUES ($1,$2,$3)
		ON CONFLICT (webhook_id, delivery_id) DO NOTHING`, webhookID, deliveryID, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		if _, err := s.Pool.Exec(ctx, `UPDATE git_webhooks SET last_delivery_at = $2 WHERE id = $1`, webhookID, now); err != nil {
			return false, err
		}
	}
	return tag.RowsAffected() == 0, nil
}

// GitService is a git service a webhook may deploy.
type GitService struct {
	ID           string
	EnvProtected bool
	Config       json.RawMessage
}

// GitServicesOf lists the live git services linked to a webhook: the services that use the
// connection, or the one service of a service webhook.
func (s *Store) GitServicesOf(ctx context.Context, w *Webhook) ([]GitService, error) {
	rows, err := s.Pool.Query(ctx, `SELECT sv.id, e.protected, sv.config FROM services sv
		JOIN environments e ON e.id = sv.environment_id AND e.deleted_at IS NULL
		JOIN projects p ON p.id = e.project_id AND p.deleted_at IS NULL
		WHERE sv.deleted_at IS NULL AND sv.source_type = 'git' AND p.org_id = $1
		  AND (($2 <> '' AND sv.config->'git'->>'connection_id' = $2) OR sv.id = $3)
		ORDER BY sv.created_at, sv.id`, w.OrgID, w.ConnectionID, w.ServiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitService
	for rows.Next() {
		var g GitService
		if err := rows.Scan(&g.ID, &g.EnvProtected, &g.Config); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// LogLine is a row of deployment_logs.
type LogLine struct {
	Seq       int64     `json:"seq"`
	Stream    string    `json:"stream"`
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"time"`
}

// DeploymentLogs returns up to limit log lines of a deployment after seq afterSeq.
// The caller checks the org first (GetDeployment).
func (s *Store) DeploymentLogs(ctx context.Context, depID string, afterSeq int64, limit int) ([]LogLine, error) {
	rows, err := s.Pool.Query(ctx, `SELECT seq, stream, line, created_at FROM deployment_logs
		WHERE deployment_id = $1 AND seq > $2 ORDER BY seq LIMIT $3`, depID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.Stream, &l.Line, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
