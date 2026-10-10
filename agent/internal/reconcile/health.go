// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// The health check runner (F05). It runs in the agent, not in Docker, so the deploy engine gets
// the result at once and a node that lost the server keeps checking and restarting.

const (
	maxHealthBody = 1024 // REQ-HC-006, REQ-HC-012
	healthTick    = time.Second
)

// restartBackoff is the wait before the n-th restart of an unhealthy container (REQ-HC-007).
var restartBackoff = []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second, 2 * time.Minute, 5 * time.Minute}

// hstate is the result of the checks of one placement.
type hstate struct {
	health      string // unknown | starting | healthy | unhealthy
	passes      int
	fails       int
	lastErr     string
	lastStatus  int
	lastBody    string
	checked     time.Time
	next        time.Time
	containerID string
	startedAt   time.Time
	seen        time.Time // when the runner first saw this container
	busy        bool
	restartIdx  int
	restartAt   time.Time
	reported    string
}

// probeResult is the outcome of one check.
type probeResult struct {
	ok     bool
	status int
	body   string
	err    string
}

func (e *Engine) healthOf(placement string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s := e.hs[placement]; s != nil && s.health != "" {
		return s.health
	}
	return "unknown"
}

// healthLoop runs the checks until ctx ends.
func (e *Engine) healthLoop(ctx context.Context) {
	t := time.NewTicker(healthTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.healthTick(ctx)
		}
	}
}

// healthTick starts the checks that are due.
func (e *Engine) healthTick(ctx context.Context) {
	e.mu.Lock()
	ds, hold := e.desired, e.hold
	e.mu.Unlock()
	if ds == nil || hold {
		return
	}
	managed, err := e.Docker.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return
	}
	byPlacement := map[string]docker.Container{}
	for _, c := range managed {
		byPlacement[c.Labels[LabelPlacement]] = c
	}
	now := time.Now()
	wanted := map[string]bool{}
	for _, w := range ds.Workloads {
		wanted[w.PlacementId] = true
		c, ok := byPlacement[w.PlacementId]
		e.mu.Lock()
		s := e.hs[w.PlacementId]
		if s == nil {
			s = &hstate{health: "unknown"}
			e.hs[w.PlacementId] = s
		}
		if w.Role == "stopped" || !ok || c.State != "running" {
			if s.health != "unknown" {
				e.resetHealth(s)
			}
			e.mu.Unlock()
			continue
		}
		if s.containerID != c.ID {
			e.resetHealth(s)
			s.containerID, s.health, s.seen = c.ID, "starting", now
		}
		due := !s.busy && !now.Before(s.next)
		restart := s.health == "unhealthy" && !s.restartAt.IsZero() && !now.Before(s.restartAt)
		if restart {
			s.restartAt = time.Time{}
			s.restartIdx++
			e.resetHealth(s)
			s.containerID, s.health, s.seen = c.ID, "starting", now
			due = false
		}
		if due {
			s.busy = true
		}
		e.mu.Unlock()
		if restart {
			e.Log.Warn("restarting an unhealthy container", "placement", w.PlacementId)
			grace := int(w.StopGraceSeconds)
			if err := e.Docker.Restart(ctx, c.ID, grace); err != nil {
				e.Log.Warn("restart failed", "placement", w.PlacementId, "error", err)
			}
			e.kick()
			continue
		}
		if due {
			go e.runProbe(ctx, w, c)
		}
	}
	e.mu.Lock()
	for id := range e.hs {
		if !wanted[id] {
			delete(e.hs, id)
		}
	}
	e.mu.Unlock()
}

// resetHealth clears the counters. The caller holds e.mu.
func (e *Engine) resetHealth(s *hstate) {
	s.health, s.passes, s.fails = "unknown", 0, 0
	s.lastErr, s.lastStatus, s.lastBody = "", 0, ""
	s.startedAt, s.next, s.restartAt = time.Time{}, time.Time{}, time.Time{}
}

func secondsOr(v uint32, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return time.Duration(v) * time.Second
}

// runProbe does one check and updates the counters (REQ-HC-004, REQ-HC-005).
func (e *Engine) runProbe(ctx context.Context, w *agentv1.WorkloadSpec, c docker.Container) {
	hc := w.Health
	if hc == nil {
		hc = &agentv1.HealthCheckSpec{Type: "running"}
	}
	started := time.Time{}
	if ic, err := e.Docker.Inspect(ctx, c.ID); err == nil {
		started = ic.StartedAt
	}
	e.mu.Lock()
	if started.IsZero() {
		if s := e.hs[w.PlacementId]; s != nil {
			started = s.seen // Docker gave no start time: the first time we saw the container
		}
	}
	e.mu.Unlock()
	res := e.probeOnce(ctx, w, c, started)

	healthy, unhealthy := int(hc.HealthyThreshold), int(hc.UnhealthyThreshold)
	if healthy <= 0 {
		healthy = 3
	}
	if unhealthy <= 0 {
		unhealthy = 3
	}
	if hc.Type == "running" {
		healthy = 1
	}
	start := time.Duration(hc.StartPeriodSeconds) * time.Second
	now := time.Now()

	e.mu.Lock()
	s := e.hs[w.PlacementId]
	if s == nil || s.containerID != c.ID {
		e.mu.Unlock()
		return
	}
	s.busy = false
	if !started.IsZero() && !started.Equal(s.startedAt) {
		// The container started again (Docker restart policy): the start period begins again.
		s.passes, s.fails, s.health = 0, 0, "starting"
		s.startedAt = started
	}
	old := s.health
	inStart := !started.IsZero() && now.Sub(started) < start
	if res.ok {
		s.passes++
		s.fails = 0
		if s.passes >= healthy {
			s.health = "healthy"
		}
	} else {
		s.passes = 0
		if !inStart { // failures in the start period do not count (REQ-HC-004)
			s.fails++
			if s.fails >= unhealthy {
				s.health = "unhealthy"
			}
		}
	}
	if s.health == "unknown" {
		s.health = "starting"
	}
	s.lastErr, s.lastStatus, s.lastBody, s.checked = res.err, res.status, res.body, now
	// Until the container is healthy, it is checked every second at most, so a service that is ready early
	// switches early (the fast rollback needs this). After that the interval of the config applies.
	iv := secondsOr(hc.IntervalSeconds, 5*time.Second)
	if s.health != "healthy" && s.health != "unhealthy" && iv > time.Second {
		iv = time.Second
	}
	s.next = now.Add(iv)
	switch {
	case s.health == "unhealthy" && old != "unhealthy" && hc.RestartOnUnhealthy && w.Role == "live": // only a live container is restarted (REQ-HC-007)
		idx := min(s.restartIdx, len(restartBackoff)-1)
		s.restartAt = now.Add(restartBackoff[idx])
	case s.health == "healthy":
		s.restartIdx, s.restartAt = 0, time.Time{}
	}
	report := &agentv1.HealthReport{PlacementId: w.PlacementId, Health: s.health, ConsecutivePasses: int32(s.passes),
		ConsecutiveFails: int32(s.fails), LastError: s.lastErr, LastStatus: int32(s.lastStatus), LastBody: s.lastBody, CheckedUnixMs: now.UnixMilli()}
	// A report goes out when the state or the last error changes, not at every check (REQ-HC-009).
	key := s.health + "|" + s.lastErr
	send := key != s.reported
	if send {
		s.reported = key
	}
	e.mu.Unlock()
	if send {
		report.LastError = e.scrub(report.LastError)
		report.LastBody = e.scrub(report.LastBody)
		e.send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Health{Health: report}})
	}
}

// probeOnce runs the check of the spec once against the container.
func (e *Engine) probeOnce(ctx context.Context, w *agentv1.WorkloadSpec, c docker.Container, started time.Time) probeResult {
	hc := w.Health
	if hc == nil {
		return probeResult{ok: true}
	}
	timeout := secondsOr(hc.TimeoutSeconds, 3*time.Second)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	port := hc.Port
	if port == 0 && len(w.Ports) > 0 {
		port = w.Ports[0].ContainerPort
	}
	switch hc.Type {
	case "running":
		start := time.Duration(hc.StartPeriodSeconds) * time.Second
		if started.IsZero() || time.Since(started) < start {
			return probeResult{err: "the container has not run for the start period yet"}
		}
		return probeResult{ok: true}
	case "command":
		return e.probeCommand(pctx, c, hc.Command)
	case "http":
		if c.IP == "" {
			return probeResult{err: "the container has no address"}
		}
		return probeHTTP(pctx, net.JoinHostPort(c.IP, strconv.Itoa(int(port))), hc)
	default: // tcp
		if c.IP == "" {
			return probeResult{err: "the container has no address"}
		}
		d := net.Dialer{}
		conn, err := d.DialContext(pctx, "tcp", net.JoinHostPort(c.IP, strconv.Itoa(int(port))))
		if err != nil {
			return probeResult{err: err.Error() + " (the app must listen on 0.0.0.0, not on localhost)"}
		}
		_ = conn.Close()
		return probeResult{ok: true}
	}
}

// probeHTTP calls the container directly (not through Caddy) and does not follow redirects (REQ-HC-006).
func probeHTTP(ctx context.Context, addr string, hc *agentv1.HealthCheckSpec) probeResult {
	path := hc.Path
	if path == "" {
		path = "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return probeResult{err: err.Error()}
	}
	if hc.Host != "" {
		req.Host = hc.Host
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{err: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody))
	lo, hi := hc.ExpectedStatusMin, hc.ExpectedStatusMax
	if lo == 0 {
		lo, hi = 200, 399
	}
	if hi == 0 {
		hi = lo
	}
	res := probeResult{status: resp.StatusCode, body: string(body)}
	if uint32(resp.StatusCode) < lo || uint32(resp.StatusCode) > hi {
		res.err = fmt.Sprintf("status %d (expected %d-%d)", resp.StatusCode, lo, hi)
		if len(body) > 0 {
			res.err += ": " + strings.TrimSpace(string(body))
		}
		return res
	}
	res.ok = true
	return res
}

// probeCommand runs the command in the container without a TTY. Success is exit code 0 (REQ-HC-012).
func (e *Engine) probeCommand(ctx context.Context, c docker.Container, cmd []string) probeResult {
	if len(cmd) == 0 {
		return probeResult{err: "the health check has no command"}
	}
	conn, err := e.Docker.Exec(ctx, c.ID, docker.ExecOptions{Cmd: cmd})
	if err != nil {
		return probeResult{err: err.Error()}
	}
	defer func() { _ = conn.Close() }()
	got := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(conn, maxHealthBody))
		got <- b
	}()
	var out []byte
	select {
	case out = <-got:
	case <-ctx.Done():
		return probeResult{err: "the command did not end in time"}
	}
	_ = conn.CloseWrite()
	code, err := conn.Wait(ctx)
	res := probeResult{body: string(out)}
	if err != nil {
		res.err = err.Error()
		return res
	}
	if code != 0 {
		res.err = fmt.Sprintf("exit code %d", code)
		if len(out) > 0 {
			res.err += ": " + strings.TrimSpace(string(out))
		}
		return res
	}
	res.ok = true
	return res
}

// checkHealth waits until the placement is healthy: healthy_threshold passes in a row (REQ-HC-005).
func (e *Engine) checkHealth(ctx context.Context, ch *agentv1.CheckHealth) ([]byte, string, string) {
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wasRunning := false
	for {
		e.mu.Lock()
		s := e.hs[ch.PlacementId]
		var health, lastErr string
		var status int
		var body string
		if s != nil {
			health, lastErr, status, body = s.health, s.lastErr, s.lastStatus, s.lastBody
		}
		e.mu.Unlock()
		if w := e.workload(ch.PlacementId); w != nil && w.Health == nil {
			if cs, err := e.byPlacement(ctx, ch.PlacementId); err == nil && len(cs) > 0 && cs[0].State == "running" {
				return []byte("{}"), "", ""
			}
		}
		if health == "healthy" {
			return []byte("{}"), "", ""
		}
		if health == "starting" || health == "unhealthy" {
			wasRunning = true
		}
		select {
		case <-ctx.Done():
			if !wasRunning {
				return nil, "container_start_failed", "the container did not start in time"
			}
			msg := "the health check did not pass in time"
			if lastErr != "" {
				msg += ": last result: " + lastErr
			} else if status != 0 {
				msg += fmt.Sprintf(": last status %d", status)
			}
			if body != "" && !strings.Contains(msg, body) {
				msg += " (body: " + strings.TrimSpace(body) + ")"
			}
			return nil, "health_timeout", msg
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// probeHealthNow runs the check of a placement once and returns the result (POST health-check:test).
func (e *Engine) probeHealthNow(ctx context.Context, placement string) ([]byte, string, string) {
	w := e.workload(placement)
	if w == nil {
		return nil, "internal_error", "workload is not in the desired state"
	}
	cs, err := e.byPlacement(ctx, placement)
	if err != nil || len(cs) == 0 || cs[0].State != "running" {
		return nil, "container_start_failed", "the container is not running"
	}
	started := time.Time{}
	if ic, err := e.Docker.Inspect(ctx, cs[0].ID); err == nil {
		started = ic.StartedAt
	}
	t0 := time.Now()
	res := e.probeOnce(ctx, w, cs[0], started)
	return jsonBytes(map[string]any{"healthy": res.ok, "status": res.status, "body_excerpt": e.scrub(res.body),
		"duration_ms": time.Since(t0).Milliseconds(), "error": e.scrub(res.err)}), "", ""
}
