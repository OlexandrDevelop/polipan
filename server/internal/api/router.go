// SPDX-License-Identifier: Apache-2.0

// Package api is the HTTP layer. Handlers only validate and call services (REQ-CORE-030).
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/polipan/polipan/api"
	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// Deps are the parts the router needs.
type Deps struct {
	Log   *slog.Logger
	DB    Pinger
	Clock clock.Clock
	// RateLimit is the number of requests per minute per client (api_rate_limit).
	RateLimit int
	// Web serves the web app for every path outside /api. Optional.
	Web http.Handler
	// Spec is the OpenAPI file. Empty means the embedded file. Tests set it.
	Spec []byte
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

	r := chi.NewRouter()
	r.Use(RequestID(d.Log), AccessLog, Recover)

	limiter := NewRateLimiter(d.Clock, d.RateLimit)
	h := &Handlers{DB: d.DB}
	gen.HandlerWithOptions(h, gen.ChiServerOptions{
		BaseURL:     BasePath,
		BaseRouter:  r,
		Middlewares: []gen.MiddlewareFunc{notImplementedAsJSON, limiter.Middleware},
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
