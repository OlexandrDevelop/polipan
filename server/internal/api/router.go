// SPDX-License-Identifier: Apache-2.0

// Package api is the HTTP layer. Handlers only validate and call services (REQ-CORE-030).
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/api"
	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
)

// Deps are the parts the router needs.
type Deps struct {
	Log   *slog.Logger
	DB    Pinger
	Clock clock.Clock
	// RateLimit is the number of requests per minute per client IP (api_rate_limit).
	RateLimit int
	// Web serves the web app for every path outside /api. Optional.
	Web http.Handler
	// Spec is the OpenAPI file. Empty means the embedded file. Tests set it.
	Spec []byte
	// TrustedProxies are CIDRs of reverse proxies. From them X-Forwarded-For gives the client address.
	TrustedProxies []string

	// Pool is the database. Without it only the operations that need no data work (health, version).
	Pool *pgxpool.Pool
	// Orchestrator is the deploy engine (the control package).
	Orchestrator orchestrator.Orchestrator
	// ExternalURL is config external_url. https sets HSTS, an empty or http value clears the Secure cookie flag.
	ExternalURL string
	// AgentListen is config agent_listen, for the install command of a node.
	AgentListen string
	// SetupToken is the one-time token for the first user. Empty means no token is checked.
	SetupToken string
	// SessionTTL and SessionAbsoluteTTL are config session_ttl and session_absolute_ttl.
	SessionTTL         time.Duration
	SessionAbsoluteTTL time.Duration
	// Secrets is the secrets store (M2).
	Secrets *secrets.Manager
	// Git is the Git side (connections, webhooks, deploy keys). Optional.
	Git *GitDeps
	// Exec opens terminals on the nodes (the gateway, WP2.6).
	Exec ExecGateway
	// Backup is the backup service (M4).
	Backup *backup.Service
	// KEK is the master key. It encrypts the TOTP secrets (M5).
	KEK []byte
	// MaxBodyBytes is the size limit of a request body. Default 1 MiB.
	MaxBodyBytes int64
}

// secureCookies tells if the session cookie gets the Secure flag: always, unless
// external_url is empty or plain http (local tests).
func secureCookies(externalURL string) bool {
	return externalURL != "" && !strings.HasPrefix(strings.ToLower(externalURL), "http://")
}

// NewRouter builds the router. It returns an error, and so the server does not
// start, when an operation has no x-permission, when a route is not in the
// OpenAPI file, or when an operation has no route (REQ-API-101).
func NewRouter(d Deps) (http.Handler, error) {
	spec := d.Spec
	if len(spec) == 0 {
		spec = api.Spec
	}
	ops, err := ParseOperations(spec)
	if err != nil {
		return nil, err
	}
	if err := CheckPermissions(ops, nil); err != nil {
		return nil, err
	}
	perms := make(map[Route]string, len(ops))
	opIDs := make(map[Route]string, len(ops))
	for _, o := range ops {
		opIDs[Route{Method: o.Method, Pattern: normalize(o.FullPath())}] = o.OperationID
		perms[Route{Method: o.Method, Pattern: normalize(o.FullPath())}] = o.Permission
	}
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if d.SessionTTL <= 0 {
		d.SessionTTL = 7 * 24 * time.Hour
	}
	if d.SessionAbsoluteTTL <= 0 {
		d.SessionAbsoluteTTL = 30 * 24 * time.Hour
	}

	r := chi.NewRouter()
	hsts := strings.HasPrefix(strings.ToLower(d.ExternalURL), "https://")
	r.Use(TrustedProxies(d.TrustedProxies), RequestID(d.Log), AccessLog, Recover, SecurityHeaders(hsts), BodyLimit(d.MaxBodyBytes))

	h := &Handlers{
		DB:       d.DB,
		Orch:     d.Orchestrator,
		Clock:    d.Clock,
		Throttle: auth.NewThrottle(d.Clock),
		Limiter:  NewRateLimiter(d.Clock, d.RateLimit),
		Settings: Settings{ExternalURL: d.ExternalURL, AgentListen: d.AgentListen, SetupToken: d.SetupToken,
			SessionTTL: d.SessionTTL, SessionAbsoluteTTL: d.SessionAbsoluteTTL},
		cookieSecure: secureCookies(d.ExternalURL),
		perms:        perms,
		opIDs:        opIDs,
		KEK:          d.KEK,
		Secrets:      d.Secrets,
		Git:          d.Git,
		Exec:         d.Exec,
		Backup:       d.Backup,
	}
	if d.Pool != nil {
		h.Store = store.New(d.Pool)
		h.initConfig(d.Pool)
	}
	gen.HandlerWithOptions(h, gen.ChiServerOptions{
		BaseURL:     BasePath,
		BaseRouter:  r,
		Middlewares: []gen.MiddlewareFunc{h.guard()},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		},
	})
	// Same handler without the base path, for load balancers and container health checks.
	r.Get("/healthz", h.GetHealth)
	r.Get(BasePath+"/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(spec)
	})
	r.Handle("/api/docs", docsHandler(ops))
	r.Get("/install-agent.sh", installAgentScript)

	notFound := func(w http.ResponseWriter, req *http.Request) {
		if d.Web != nil && !strings.HasPrefix(req.URL.Path, "/api/") && req.Method == http.MethodGet {
			d.Web.ServeHTTP(w, req)
			return
		}
		WriteError(w, req, http.StatusNotFound, CodeNotFound, "not found", nil)
	}
	r.NotFound(notFound)
	// The error codes are a closed list, so a wrong method is also "not found".
	r.MethodNotAllowed(notFound)

	routes, err := ListRoutes(r)
	if err != nil {
		return nil, err
	}
	if err := CheckRoutes(ops, routes); err != nil {
		return nil, err
	}
	return r, nil
}

// ListRoutes returns every method and pattern of the router.
func ListRoutes(r chi.Routes) ([]Route, error) {
	var out []Route
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, Route{Method: method, Pattern: route})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk routes: %w", err)
	}
	return out, nil
}
