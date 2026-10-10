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

// sseKeepalive is how often an idle stream sends a comment (sse_keepalive, 15 s).
var sseKeepalive = 15 * time.Second

// openSSE counts the stream of the user, checks the limit and writes the headers. release must be called at the end.
func openSSE(w http.ResponseWriter, r *http.Request) (flusher http.Flusher, release func(), ok bool) {
	user := PrincipalFrom(r.Context()).User.ID
	sseOpen.Lock()
	if sseOpen.n[user] >= sseLimit {
		sseOpen.Unlock()
		WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, "too many open event streams", nil)
		return nil, nil, false
	}
	sseOpen.n[user]++
	sseOpen.Unlock()
	release = func() {
		sseOpen.Lock()
		sseOpen.n[user]--
		sseOpen.Unlock()
	}
	f, isFlusher := w.(http.Flusher)
	if !isFlusher {
		release()
		internalError(w, r, fmt.Errorf("streaming is not supported"))
		return nil, nil, false
	}
	_ = ClearWriteDeadline(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return f, release, true
}

// StreamDeploymentEvents is the SSE stream of a deployment: state changes, health results and log lines
// (REQ-DEPLOY-111). Each message has an id "<event>.<log line>", so a client can resume with Last-Event-ID
// (REQ-API-103). The stream ends after a terminal state.
func (h *Handlers) StreamDeploymentEvents(w http.ResponseWriter, r *http.Request, deploymentID gen.DeploymentId) {
	ctx := r.Context()
	if _, err := h.Store.GetDeployment(ctx, h.orgID(r), deploymentID); fail(w, r, err) {
		return
	}
	flusher, release, ok := openSSE(w, r)
	if !ok {
		return
	}
	defer release()

	var evCur, seqCur int64
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		_, _ = fmt.Sscanf(id, "%d.%d", &evCur, &seqCur)
	}
	send := func(event string, v any) {
		raw, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "id: %d.%d\nevent: %s\ndata: %s\n\n", evCur, seqCur, event, raw)
	}
	lastWrite := time.Now()
	for {
		events, err := h.Store.EventsAfter(ctx, h.orgID(r), evCur, "deployment", deploymentID, 500)
		if err != nil {
			return
		}
		lines, err := h.Store.DeploymentLogs(ctx, deploymentID, seqCur, 1000)
		if err != nil {
			return
		}
		// State events and log lines are merged by time.
		ei, li := 0, 0
		for ei < len(events) || li < len(lines) {
			if li >= len(lines) || (ei < len(events) && !events[ei].CreatedAt.After(lines[li].CreatedAt)) {
				ev := events[ei]
				ei++
				evCur = ev.ID
				var p map[string]any
				_ = json.Unmarshal(ev.Payload, &p)
				typ, _ := p["type"].(string)
				if typ == "" {
					typ = "state"
				}
				data, _ := p["data"].(map[string]any)
				if data == nil {
					data = map[string]any{}
				}
				if m, ok := p["message"]; ok {
					data["message"] = m
				}
				msg := map[string]any{"type": typ, "data": data, "time": ev.CreatedAt}
				if st, ok := p["state"]; ok {
					msg["state"] = st
				}
				send(typ, msg)
				continue
			}
			l := lines[li]
			li++
			seqCur = l.Seq
			if l.Stream == "deploy" {
				continue // the state events already carry these lines
			}
			send("log", map[string]any{"type": "log", "time": l.CreatedAt, "data": map[string]any{"seq": l.Seq, "stream": l.Stream, "line": l.Line}})
		}
		if len(events)+len(lines) > 0 {
			lastWrite = time.Now()
		}
		d, err := h.Store.GetDeployment(ctx, h.orgID(r), deploymentID)
		if err != nil {
			return
		}
		if terminal(d) && len(events) == 0 && len(lines) == 0 {
			flusher.Flush()
			return
		}
		if time.Since(lastWrite) >= sseKeepalive {
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			lastWrite = time.Now()
		}
		flusher.Flush()
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollEvery):
		}
	}
}

// StreamEvents is the SSE stream of the platform events of the org: deployments, nodes, services, alerts.
// A client that sends Last-Event-ID gets what it missed. Without it, only new events come.
func (h *Handlers) StreamEvents(w http.ResponseWriter, r *http.Request, params gen.StreamEventsParams) {
	ctx := r.Context()
	org := h.orgID(r)
	var cur int64
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		_, _ = fmt.Sscanf(id, "%d", &cur)
	} else {
		last, err := h.Store.LastEventID(ctx, org)
		if fail(w, r, err) {
			return
		}
		cur = last
	}
	topics := map[string]bool{}
	if params.Topics != nil {
		for _, t := range *params.Topics {
			topics[t] = true
		}
	}
	flusher, release, ok := openSSE(w, r)
	if !ok {
		return
	}
	defer release()
	lastWrite := time.Now()
	for {
		events, err := h.Store.EventsAfter(ctx, org, cur, "", "", 500)
		if err != nil {
			return
		}
		for _, ev := range events {
			cur = ev.ID
			if len(topics) > 0 && !topics[ev.Topic] {
				continue
			}
			var payload any
			_ = json.Unmarshal(ev.Payload, &payload)
			raw, _ := json.Marshal(map[string]any{"topic": ev.Topic, "object_id": ev.ObjectID, "payload": payload})
			_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, raw)
			lastWrite = time.Now()
		}
		if time.Since(lastWrite) >= sseKeepalive {
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			lastWrite = time.Now()
		}
		flusher.Flush()
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
