// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/obs"
)

const day = 24 * time.Hour

// retentionOf gives the retention of the org for metrics and logs. An org setting wins, else the storage limit.
func (h *Handlers) retentionOf(r *http.Request) (metrics, logs time.Duration) {
	metrics = time.Duration(h.Monitoring.MetricsRetentionDays) * day
	logs = time.Duration(h.Monitoring.LogsRetentionDays) * day
	o, err := h.Store.GetOrg(r.Context(), h.orgID(r))
	if err != nil {
		return metrics, logs
	}
	var s gen.OrgSettings
	if json.Unmarshal(o.Settings, &s) != nil {
		return metrics, logs
	}
	if s.MetricsRetentionDays != nil && *s.MetricsRetentionDays > 0 {
		metrics = time.Duration(*s.MetricsRetentionDays) * day
	}
	if s.LogsRetentionDays != nil && *s.LogsRetentionDays > 0 {
		logs = time.Duration(*s.LogsRetentionDays) * day
	}
	return metrics, logs
}

// obsFailed maps an error of the metric or log store to an answer. It returns true when it wrote one.
func obsFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, obs.ErrBadQuery):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
	case errors.Is(err, obs.ErrUnavailable):
		logging.From(r.Context()).Warn("monitoring store", "error", err)
		WriteError(w, r, http.StatusServiceUnavailable, CodeInternal, "the metric and log store is not available", map[string]any{"reason": "monitoring_unavailable"})
	default:
		internalError(w, r, err)
	}
	return true
}

func logJSON(l obs.LogLine) gen.LogLine {
	out := gen.LogLine{Message: &l.Message, Match: &l.Match}
	t := l.Time
	out.Time = &t
	set := func(dst **string, v string) {
		if v != "" {
			*dst = ptr(v)
		}
	}
	set(&out.Level, l.Level)
	set(&out.ProjectId, l.ProjectID)
	set(&out.EnvironmentId, l.EnvironmentID)
	set(&out.ServiceId, l.ServiceID)
	set(&out.ReleaseId, l.ReleaseID)
	set(&out.NodeId, l.NodeID)
	set(&out.Container, l.Container)
	if l.Stream != "" {
		st := gen.LogLineStream(l.Stream)
		out.Stream = &st
	}
	out.Replica = l.Replica
	return out
}

// GetServiceLogs returns the last lines of a service from the log store (REQ-OBS-010). When the store is off or down,
// it falls back to `docker logs` of the live container (12 section 3).
func (h *Handlers) GetServiceLogs(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.GetServiceLogsParams) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	masker, err := h.maskerFor(r, serviceID)
	if fail(w, r, err) {
		return
	}
	limit := pageLimitLogs(params.Limit)
	if h.Obs.LogsEnabled() {
		_, logsRet := h.retentionOf(r)
		f := obs.LogFilter{OrgID: s.OrgID, ServiceID: serviceID, Limit: min(limit, 200), Retention: logsRet}
		if params.From != nil {
			f.From = *params.From
		}
		if params.To != nil {
			f.To = *params.To
		}
		page, err := h.Obs.Search(r.Context(), h.Clock.Now(), f)
		if err == nil {
			lines := make([]gen.LogLine, 0, len(page.Items))
			for i := len(page.Items) - 1; i >= 0; i-- { // oldest first
				l := page.Items[i]
				l.Message = masker.Mask(l.Message) // REQ-SEC-007
				lines = append(lines, logJSON(l))
			}
			WriteJSON(w, http.StatusOK, lines)
			return
		}
		logging.From(r.Context()).Warn("service logs: store failed, using docker logs", "error", err)
	}
	text, err := h.Orch.Logs(r.Context(), serviceID, min(limit, 1000))
	if h.orchestratorFailed(w, r, err) {
		return
	}
	lines := []gen.LogLine{}
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if l == "" {
			continue
		}
		l = masker.Mask(l)
		lines = append(lines, gen.LogLine{Message: ptr(l), ServiceId: &s.ID, EnvironmentId: &s.EnvironmentID, ProjectId: &s.ProjectID})
	}
	WriteJSON(w, http.StatusOK, lines)
}

// StreamServiceLogs is the SSE live tail: the last 200 lines, then the new ones (REQ-OBS-010).
func (h *Handlers) StreamServiceLogs(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	masker, err := h.maskerFor(r, serviceID)
	if fail(w, r, err) {
		return
	}
	if !h.Obs.LogsEnabled() {
		WriteError(w, r, http.StatusServiceUnavailable, CodeInternal, "the log store is not set up", map[string]any{"reason": "monitoring_unavailable"})
		return
	}
	ctx := r.Context()
	_, logsRet := h.retentionOf(r)
	f := obs.LogFilter{OrgID: s.OrgID, ServiceID: serviceID, Retention: logsRet}
	first, err := h.Obs.Recent(ctx, h.Clock.Now(), f, 200)
	if obsFailed(w, r, err) {
		return
	}
	flusher, release, ok := openSSE(w, r)
	if !ok {
		return
	}
	defer release()
	seen := map[string]bool{} // lines of the newest timestamp that were sent already
	var last time.Time
	send := func(lines []obs.LogLine) {
		for _, l := range lines {
			key := fmt.Sprintf("%s|%s|%s", l.Container, l.Stream, l.Message)
			if l.Time.Before(last) || (l.Time.Equal(last) && seen[key]) {
				continue
			}
			if l.Time.After(last) {
				last, seen = l.Time, map[string]bool{}
			}
			seen[key] = true
			l.Message = masker.Mask(l.Message)
			raw, _ := json.Marshal(logJSON(l))
			_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}
	send(first)
	if last.IsZero() {
		last = h.Clock.Now().Add(-time.Second)
	}
	lastWrite := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		flusher.Flush()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		nf := f
		nf.From = last
		lines, err := h.Obs.Recent(ctx, h.Clock.Now(), nf, 200)
		if err != nil {
			if ctx.Err() == nil {
				_, _ = fmt.Fprint(w, ": the log store is not available\n\n")
			}
			continue
		}
		before := last
		send(lines)
		if !last.Equal(before) || len(lines) > 0 {
			lastWrite = time.Now()
		}
		if time.Since(lastWrite) >= sseKeepalive {
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			lastWrite = time.Now()
		}
	}
}

// SearchLogs searches the logs of the org. Only text and fields are allowed, never raw LogsQL (REQ-OBS-101).
func (h *Handlers) SearchLogs(w http.ResponseWriter, r *http.Request, params gen.SearchLogsParams) {
	if !h.Obs.LogsEnabled() {
		WriteError(w, r, http.StatusServiceUnavailable, CodeInternal, "the log store is not set up", map[string]any{"reason": "monitoring_unavailable"})
		return
	}
	org := h.orgID(r)
	_, logsRet := h.retentionOf(r)
	f := obs.LogFilter{OrgID: org, Retention: logsRet, Limit: pageLimit(params.Limit)}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	f.Text, f.NodeID = str(params.Q), str(params.NodeId)
	if len(f.Text) > 1000 {
		invalid(w, r, "q", "the search text has at most 1000 characters")
		return
	}
	// The ids must belong to the org, and the user needs logs:read in the project of the id.
	project := str(params.ProjectId)
	if id := str(params.ServiceId); id != "" {
		s, err := h.Store.GetService(r.Context(), org, id)
		if fail(w, r, err) {
			return
		}
		f.ServiceID, project = id, s.ProjectID
	}
	if id := str(params.ReleaseId); id != "" {
		rel, err := h.Store.GetRelease(r.Context(), org, id)
		if fail(w, r, err) {
			return
		}
		s, err := h.Store.GetService(r.Context(), org, rel.ServiceID)
		if fail(w, r, err) {
			return
		}
		f.ReleaseID, project = id, s.ProjectID
	}
	if id := str(params.EnvironmentId); id != "" {
		e, err := h.Store.GetEnvironment(r.Context(), org, id)
		if fail(w, r, err) {
			return
		}
		f.EnvironmentID, project = id, e.ProjectID
	}
	if project != "" {
		if _, err := h.Store.GetProject(r.Context(), org, project); fail(w, r, err) {
			return
		}
		if h.denyInProject(w, r, project, "logs:read", false) {
			return
		}
		f.ProjectID = project
	} else if t := PrincipalFrom(r.Context()).Token; t != nil && t.ProjectID != nil {
		invalid(w, r, "project_id", "a token limited to a project must search with project_id")
		return
	}
	if params.Level != nil {
		f.Level = string(*params.Level)
	}
	if params.Stream != nil {
		f.Stream = string(*params.Stream)
	}
	if params.Context != nil {
		f.Context = *params.Context
	}
	if params.From != nil {
		f.From = *params.From
	}
	if params.To != nil {
		f.To = *params.To
	}
	if params.Cursor != nil {
		f.Cursor = *params.Cursor
	}
	page, err := h.Obs.Search(r.Context(), h.Clock.Now(), f)
	if obsFailed(w, r, err) {
		return
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	masker, err := h.maskerForOrg(r, page.Items)
	if fail(w, r, err) {
		return
	}
	items := make([]gen.LogLine, 0, len(page.Items))
	for _, l := range page.Items {
		l.Message = masker(l)
		items = append(items, logJSON(l))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// maskerForOrg masks the lines of a page with the secrets of the service of each line.
func (h *Handlers) maskerForOrg(r *http.Request, lines []obs.LogLine) (func(obs.LogLine) string, error) {
	maskers := map[string]interface{ Mask(string) string }{}
	for _, l := range lines {
		if l.ServiceID == "" || maskers[l.ServiceID] != nil {
			continue
		}
		m, err := h.maskerFor(r, l.ServiceID)
		if err != nil {
			return nil, err
		}
		maskers[l.ServiceID] = m
	}
	return func(l obs.LogLine) string {
		if m := maskers[l.ServiceID]; m != nil {
			return m.Mask(l.Message)
		}
		return l.Message
	}, nil
}

func (h *Handlers) metricsRequest(w http.ResponseWriter, r *http.Request, scope obs.Scope, names []string, from, to *time.Time, step *string) {
	if !h.Obs.MetricsEnabled() {
		WriteError(w, r, http.StatusServiceUnavailable, CodeInternal, "the metric store is not set up", map[string]any{"reason": "monitoring_unavailable"})
		return
	}
	known := map[string]bool{}
	for _, n := range obs.MetricNames {
		known[n] = true
	}
	for _, n := range names {
		if !known[n] {
			invalid(w, r, "metrics", "unknown metric "+n)
			return
		}
	}
	q := obs.MetricsQuery{Scope: scope, Metrics: names}
	if from != nil {
		q.From = *from
	}
	if to != nil {
		q.To = *to
	}
	if step != nil && *step != "" {
		d, err := time.ParseDuration(*step)
		if err != nil || d < time.Second {
			invalid(w, r, "step", "step is a duration like 60s")
			return
		}
		q.Step = d
	}
	q.Retention, _ = h.retentionOf(r)
	res, err := h.Obs.Metrics(r.Context(), h.Clock.Now(), q)
	if obsFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// GetServiceMetrics gives the time series of a service. The org label is always in the query (REQ-OBS-100).
func (h *Handlers) GetServiceMetrics(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.GetServiceMetricsParams) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	h.metricsRequest(w, r, obs.Scope{OrgID: s.OrgID, ServiceID: serviceID}, params.Metrics, params.From, params.To, params.Step)
}

// GetNodeMetrics gives the time series of a node.
func (h *Handlers) GetNodeMetrics(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId, params gen.GetNodeMetricsParams) {
	if _, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID); fail(w, r, err) {
		return
	}
	h.metricsRequest(w, r, obs.Scope{NodeID: nodeID}, params.Metrics, params.From, params.To, params.Step)
}
