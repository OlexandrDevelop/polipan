// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/version"
)

// Pinger checks that the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Settings are the config values the handlers need.
type Settings struct {
	// ExternalURL is the public address of the panel. It sets the Secure flag of the cookie,
	// HSTS and the install command of a node.
	ExternalURL string
	// AgentListen is the address of the agent gateway, for the install command. Default port 9443.
	AgentListen string
	// SetupToken is the one-time token for the first user. Empty means no token is checked.
	SetupToken string
	// SessionTTL is the sliding life of a session, SessionAbsoluteTTL the hard limit.
	SessionTTL         time.Duration
	SessionAbsoluteTTL time.Duration
}

// Handlers implements the operations that exist in this version.
// Every other operation answers 501 through gen.Unimplemented.
type Handlers struct {
	gen.Unimplemented
	DB       Pinger
	Store    *store.Store
	Orch     orchestrator.Orchestrator
	Clock    clock.Clock
	Throttle *auth.Throttle
	Limiter  *RateLimiter
	Settings Settings
	// Secrets is the secrets store (M2). Without it secret writes answer 500.
	Secrets *secrets.Manager
	// Git is the Git side: key store, SSRF policy, providers (handlers_git.go). Nil: git operations answer 500.
	Git *GitDeps
	// Exec opens terminals on the nodes (the gateway). Without it exec answers 500.
	Exec ExecGateway
	// Backup is the backup service (M4). Without it the backup operations answer 500.
	Backup *backup.Service

	// KEK encrypts the TOTP secrets of users. Without it the 2FA operations answer 500.
	KEK []byte

	cookieSecure bool
	perms        map[Route]string
	opIDs        map[Route]string
}

// healthTimeout is the longest wait for the database in /healthz.
const healthTimeout = 2 * time.Second

// GetHealth answers 200 with status ok when the database is up, and 503 with status degraded when it is not.
func (h *Handlers) GetHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	dbUp := h.DB.Ping(ctx) == nil
	body := gen.Health{Status: gen.HealthStatus("ok"), Database: &dbUp}
	status := http.StatusOK
	if !dbUp {
		body.Status = gen.HealthStatus("degraded")
		status = http.StatusServiceUnavailable
		logging.From(r.Context()).Warn("health check: database is down")
	}
	WriteJSON(w, status, body)
}

// GetVersion returns the server version.
func (h *Handlers) GetVersion(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"api":     version.APIVersion,
	})
}

// page is a list answer with a cursor.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// pageLimit reads the limit parameter (1 to 200, default 50).
func pageLimit(l *gen.Limit) int {
	if l == nil {
		return 50
	}
	return min(max(*l, 1), 200)
}

// parseCursor reads the cursor parameter. It writes 422 and returns false when it is bad.
func parseCursor(w http.ResponseWriter, r *http.Request, c *gen.Cursor) (*store.Cursor, bool) {
	if c == nil {
		return nil, true
	}
	cur, err := store.DecodeCursor(*c)
	if err != nil {
		invalid(w, r, "cursor", "cursor is not valid")
		return nil, false
	}
	return cur, true
}

// orchestratorFailed maps an error of the orchestrator to an answer. It returns true when it wrote one.
func (h *Handlers) orchestratorFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, orchestrator.ErrNotFound):
		notFoundError(w, r)
	case errors.Is(err, orchestrator.ErrNoNode):
		WriteError(w, r, http.StatusConflict, CodeConflict, "no node is ready to run the service", map[string]any{"reason": "node_unavailable"})
	case errors.Is(err, orchestrator.ErrAlreadyLive):
		WriteError(w, r, http.StatusConflict, CodeAlreadyLive, "this release is the live release", nil)
	case errors.Is(err, orchestrator.ErrImagePruned):
		WriteError(w, r, http.StatusConflict, CodeReleaseImagePruned, "the image of this release was pruned. Start a normal deploy from its commit", nil)
	case errors.Is(err, orchestrator.ErrSecretDestroyed):
		WriteError(w, r, http.StatusConflict, CodeSecretVersionDestroyed, "a secret version of this release was destroyed. Nothing was started", nil)
	case errors.Is(err, orchestrator.ErrNotCancellable):
		WriteError(w, r, http.StatusConflict, CodeNotCancellable, "the deployment cannot be cancelled now. Let it finish, then roll back", nil)
	case errors.Is(err, orchestrator.ErrDomainPortMismatch):
		WriteError(w, r, http.StatusConflict, CodeDomainPortMismatch, "a domain points to a port this release does not expose. Send force to roll back anyway",
			map[string]any{"warning": "domain_port_mismatch"})
	case errors.Is(err, orchestrator.ErrNoLiveRelease):
		WriteError(w, r, http.StatusConflict, CodeConflict, "the service has no live container", map[string]any{"reason": "no_live_release"})
	case errors.Is(err, orchestrator.ErrStrategyUnavailable):
		invalid(w, r, "strategy", "this strategy is not available in this version (use recreate or blue_green)")
	case errors.Is(err, orchestrator.ErrNodeNotEmpty):
		WriteError(w, r, http.StatusConflict, CodeNodeNotEmpty, "the node has workloads. Drain it first, or delete it with force=true", nil)
	case errors.Is(err, orchestrator.ErrNodeState):
		WriteError(w, r, http.StatusConflict, CodeConflict, strings.TrimPrefix(err.Error(), "orchestrator: the node state does not allow this: "), map[string]any{"reason": "node_state"})
	case errors.Is(err, orchestrator.ErrNodeOffline):
		WriteError(w, r, http.StatusConflict, CodeConflict, "the node is not connected", map[string]any{"reason": "node_offline"})
	case errors.Is(err, orchestrator.ErrDeployRunning):
		WriteError(w, r, http.StatusConflict, CodeConflict, "a deployment of this service is running", map[string]any{"reason": "deploy_running"})
	default:
		internalError(w, r, err)
	}
	return true
}
