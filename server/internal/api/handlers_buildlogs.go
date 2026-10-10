// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/store"
)

// GetDeploymentLogs returns the build and deploy log lines of a deployment (already masked when stored).
func (h *Handlers) GetDeploymentLogs(w http.ResponseWriter, r *http.Request, deploymentID gen.DeploymentId, params gen.GetDeploymentLogsParams) {
	if _, err := h.Store.GetDeployment(r.Context(), h.orgID(r), deploymentID); fail(w, r, err) {
		return
	}
	var after int64
	if params.AfterSeq != nil {
		after = int64(*params.AfterSeq)
	}
	lines, err := h.Store.DeploymentLogs(r.Context(), deploymentID, after, 5000)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, lines)
}

// sseLimit is the number of open SSE streams per user (10-security row 21).
const sseLimit = 10

var sseOpen = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

// pollEvery is how often an open stream looks for new lines and states.
var pollEvery = 500 * time.Millisecond

// StreamDeploymentEvents is the SSE stream of a deployment: state changes and log lines,
// live while the build runs (REQ-APP-007, REQ-DEPLOY-111). It ends after a terminal state.
func (h *Handlers) StreamDeploymentEvents(w http.ResponseWriter, r *http.Request, deploymentID gen.DeploymentId) {
	ctx := r.Context()
	if _, err := h.Store.GetDeployment(ctx, h.orgID(r), deploymentID); fail(w, r, err) {
		return
	}
	user := PrincipalFrom(ctx).User.ID
	sseOpen.Lock()
	if sseOpen.n[user] >= sseLimit {
		sseOpen.Unlock()
		WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, "too many open event streams", nil)
		return
	}
	sseOpen.n[user]++
	sseOpen.Unlock()
	defer func() {
		sseOpen.Lock()
		sseOpen.n[user]--
		sseOpen.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		internalError(w, r, fmt.Errorf("streaming is not supported"))
		return
	}
	_ = ClearWriteDeadline(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, v any) {
		raw, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	}
	var seq int64
	lastState := ""
	for {
		lines, err := h.Store.DeploymentLogs(ctx, deploymentID, seq, 1000)
		if err != nil {
			return
		}
		for _, l := range lines {
			seq = l.Seq
			send("log", map[string]any{"type": "log", "data": l, "time": l.CreatedAt})
		}
		d, err := h.Store.GetDeployment(ctx, h.orgID(r), deploymentID)
		if err != nil {
			return
		}
		if d.State != lastState {
			lastState = d.State
			send("state", map[string]any{"type": "state", "state": d.State, "time": time.Now().UTC(),
				"data": map[string]any{"failure_reason": d.FailureReason, "release_id": d.ReleaseID}})
		}
		flusher.Flush()
		if terminal(d) && len(lines) < 1000 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}

func terminal(d *store.Deployment) bool {
	switch d.State {
	case "live", "failed", "rolled_back", "cancelled", "superseded":
		return true
	}
	return false
}
