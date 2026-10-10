// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/version"
)

// Pinger checks that the database answers.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Handlers implements the operations that exist in this version.
// Every other operation answers 501 through gen.Unimplemented.
type Handlers struct {
	gen.Unimplemented
	DB Pinger
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
