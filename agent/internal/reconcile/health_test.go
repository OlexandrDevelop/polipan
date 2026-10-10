// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// healthEnv runs an engine with one workload whose container answers on a local test server.
func healthEnv(t *testing.T, hc *agentv1.HealthCheckSpec, h http.HandlerFunc) (*Engine, *sink, *fakeDocker, context.Context) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	var p uint32
	for _, c := range port {
		p = p*10 + uint32(c-'0')
	}
	hc.Port = p
	f := newFake()
	e, sk := newEngine(t, t.TempDir(), f)
	w := wl("p1", "live")
	w.Health = hc
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(ctx)
	f.mu.Lock()
	f.cs["pp-p1"].IP = "127.0.0.1" // the container is the test server
	f.mu.Unlock()
	go e.healthLoop(ctx)
	return e, sk, f, ctx
}

func healthReports(s *sink) (out []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if h := m.GetHealth(); h != nil {
			out = append(out, h.Health)
		}
	}
	return
}

// AC-HC-01: 503 first, then 200. The replica is healthy only after healthy_threshold passes in a row.
func TestAC_HC_01_HealthyAfterThreeOK(t *testing.T) {
	start := time.Now()
	var ok int32
	hc := &agentv1.HealthCheckSpec{Type: "http", Path: "/ready", IntervalSeconds: 1, TimeoutSeconds: 1, HealthyThreshold: 3, UnhealthyThreshold: 50}
	e, _, _, ctx := healthEnv(t, hc, func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < 3*time.Second {
			w.WriteHeader(503)
			return
		}
		atomic.AddInt32(&ok, 1)
	})
	_, code, msg := e.checkHealth(ctx, &agentv1.CheckHealth{PlacementId: "p1", TimeoutSeconds: 20})
	if code != "" {
		t.Fatalf("%s %s", code, msg)
	}
	if n := atomic.LoadInt32(&ok); n < 3 {
		t.Fatalf("healthy after %d answers with 200", n)
	}
	if time.Since(start) < 3*time.Second {
		t.Fatal("healthy during the 503 phase")
	}
}

// AC-HC-05: the failure of a check shows the last status and the body excerpt (REQ-HC-006).
func TestAC_HC_05_LastStatusAndBody(t *testing.T) {
	hc := &agentv1.HealthCheckSpec{Type: "http", Path: "/", IntervalSeconds: 1, TimeoutSeconds: 1, HealthyThreshold: 1, UnhealthyThreshold: 50}
	e, _, _, ctx := healthEnv(t, hc, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, "database is down")
	})
	_, code, msg := e.checkHealth(ctx, &agentv1.CheckHealth{PlacementId: "p1", TimeoutSeconds: 3})
	if code != "health_timeout" || !strings.Contains(msg, "503") || !strings.Contains(msg, "database is down") {
		t.Fatalf("%s %q", code, msg)
	}
}

// AC-HC-06: failures inside start_period do not count. The replica ends healthy and was never unhealthy.
func TestAC_HC_06_StartPeriodFailuresDoNotCount(t *testing.T) {
	start := time.Now()
	hc := &agentv1.HealthCheckSpec{Type: "http", Path: "/", IntervalSeconds: 1, TimeoutSeconds: 1, HealthyThreshold: 2, UnhealthyThreshold: 1, StartPeriodSeconds: 4}
	e, sk, _, ctx := healthEnv(t, hc, func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < 2500*time.Millisecond {
			w.WriteHeader(500)
		}
	})
	_, code, msg := e.checkHealth(ctx, &agentv1.CheckHealth{PlacementId: "p1", TimeoutSeconds: 15})
	if code != "" {
		t.Fatalf("%s %s", code, msg)
	}
	for _, h := range healthReports(sk) {
		if h == "unhealthy" {
			t.Fatalf("reports: %v", healthReports(sk))
		}
	}
	if e.healthOf("p1") != "healthy" {
		t.Fatal("not healthy")
	}
}

// REQ-HC-007: a live container that fails unhealthy_threshold times is reported and restarted.
func TestUnhealthyIsReportedAndRestarted(t *testing.T) {
	old := restartBackoff
	restartBackoff = []time.Duration{time.Second}
	t.Cleanup(func() { restartBackoff = old })
	hc := &agentv1.HealthCheckSpec{Type: "http", Path: "/", IntervalSeconds: 1, TimeoutSeconds: 1, HealthyThreshold: 1, UnhealthyThreshold: 2, RestartOnUnhealthy: true}
	_, sk, f, _ := healthEnv(t, hc, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	deadline := time.Now().Add(10 * time.Second)
	for !f.has("restart") && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !f.has("restart") {
		t.Fatalf("no restart: %v", f.calls)
	}
	var unhealthy bool
	for _, h := range healthReports(sk) {
		unhealthy = unhealthy || h == "unhealthy"
	}
	if !unhealthy {
		t.Fatalf("reports %v", healthReports(sk))
	}
}

// execFake answers the command check: exit code and a long output.
type execFake struct {
	*fakeDocker
	code int
}

type doneExec struct {
	*fakeExec
	code int
}

func (d *doneExec) Wait(context.Context) (int, error) { return d.code, nil }

func (e *execFake) Exec(ctx context.Context, id string, o docker.ExecOptions) (docker.ExecConn, error) {
	c, _ := e.fakeDocker.Exec(ctx, id, o)
	x := c.(*fakeExec)
	go func() {
		_, _ = x.outW.Write([]byte(strings.Repeat("x", 3000)))
		_ = x.outW.Close()
	}()
	return &doneExec{fakeExec: x, code: e.code}, nil
}

// REQ-HC-012: a command check passes on exit code 0, has no TTY, and its output is cut to 1 KiB.
func TestCommandCheck(t *testing.T) {
	f := &execFake{fakeDocker: newFake()}
	e, _ := newEngine(t, t.TempDir(), f.fakeDocker)
	e.Docker = f
	c := docker.Container{ID: "c1", Name: "pp-p1", State: "running"}
	ctx := context.Background()
	if res := e.probeCommand(ctx, c, []string{"/bin/check"}); !res.ok || len(res.body) != 1024 {
		t.Fatalf("exit 0: %+v (body %d)", res, len(res.body))
	}
	if x := f.lastExec(); x.opts.Tty {
		t.Fatal("the check has a TTY")
	}
	f.code = 3
	if res := e.probeCommand(ctx, c, []string{"/bin/check"}); res.ok || !strings.Contains(res.err, "exit code 3") {
		t.Fatalf("exit 3: %+v", res)
	}
}
