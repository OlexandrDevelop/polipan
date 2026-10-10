// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// gates is a fake metrics store for the canary gate.
type gates struct {
	mu    sync.Mutex
	stats obs.CanaryStats
	calls int
}

func (g *gates) CanaryStats(context.Context, string, string, string, time.Duration) (obs.CanaryStats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.stats, nil
}

func (g *gates) set(s obs.CanaryStats) {
	g.mu.Lock()
	g.stats = s
	g.mu.Unlock()
}

func one(i int) *int { return &i }

// runClock moves the fake clock of the test forward, so the wait of a canary step ends. It stops with the test.
func (m *m3) runClock(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				m.env.Clock.Advance(500 * time.Millisecond)
			}
		}
	}()
}

func canaryParams() orchestrator.DeployParams {
	return orchestrator.DeployParams{Strategy: "canary", CanarySteps: []int{10, 25, 50, 100}, CanaryStepIntervalSeconds: one(1)}
}

func setupCanary(t *testing.T) (*m3, *gates) {
	g := &gates{}
	m := setupM3(t, func(d *control.Deps) { d.Gates = g })
	m.mustDeploy(t) // v1 is live (blue)
	return m, g
}

// AC-DEPLOY-03: the new version goes 10, 25, 50, 100 and the old containers stop at the end.
func TestAC_DEPLOY_03_CanaryGoodVersionGoesLive(t *testing.T) {
	m, g := setupCanary(t)
	g.set(obs.CanaryStats{GreenRequests: 200, Green5xx: 0, GreenP95: 0.05, BlueP95: 0.05, GreenP95Known: true, BlueP95Known: true})
	m.runClock(t)
	id, state, reason := m.deploy(t, canaryParams())
	if state != "live" {
		t.Fatalf("canary %s %s", state, reason)
	}
	steps := m.q(t, `SELECT string_agg(data->>'canary_step', ',' ORDER BY id) FROM deployment_steps WHERE deployment_id = $1 AND data ? 'canary_step'`, id)
	if steps != "10,25,50,100" {
		t.Fatalf("steps %q", steps)
	}
	if m.q(t, `SELECT strategy FROM deployments WHERE id = $1`, id) != "canary" || m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != m.releaseID(t, 2) {
		t.Fatal("the new release is not live")
	}
	if m.q(t, `SELECT role FROM placements WHERE release_id = $1`, m.releaseID(t, 1)) != "stopped" {
		t.Fatal("the old containers did not stop")
	}
	if n := m.q(t, `SELECT count(*) FROM events WHERE topic = 'deployment' AND object_id = $1 AND payload->>'type' = 'canary_gate'`, id); n != "3" {
		t.Fatalf("canary_gate events: %s", n)
	}
	if m.q(t, `SELECT canary_routing::text FROM deployments WHERE id = $1`, id) != "false" {
		t.Fatal("canary routing is still on")
	}
}

// AC-DEPLOY-03: the 5xx gate trips at step 1, traffic returns to blue, the state is rolled_back with gate_5xx.
func TestAC_DEPLOY_03_CanaryBadGateRollsBack(t *testing.T) {
	m, g := setupCanary(t)
	g.set(obs.CanaryStats{GreenRequests: 100, Green5xx: 40, GreenP95: 0.05, BlueP95: 0.05, GreenP95Known: true, BlueP95Known: true})
	m.runClock(t)
	id, state, reason := m.deploy(t, canaryParams())
	if state != "rolled_back" || reason != "gate_5xx" {
		t.Fatalf("canary %s %s", state, reason)
	}
	v1 := m.releaseID(t, 1)
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v1 || m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v1) != "live" {
		t.Fatal("blue does not serve")
	}
	if m.q(t, `SELECT canary_step FROM deployments WHERE id = $1`, id) != "10" {
		t.Fatalf("the gate did not trip at step 1: %s", m.q(t, `SELECT canary_step FROM deployments WHERE id = $1`, id))
	}
	if !strings.Contains(m.q(t, `SELECT failure_detail FROM deployments WHERE id = $1`, id), "40.0%") {
		t.Fatalf("detail: %s", m.q(t, `SELECT failure_detail FROM deployments WHERE id = $1`, id))
	}
	// The route has only blue again and no weights of a canary.
	ds := lastState(t, m.agent)
	r := routeByID(ds, m.q(t, `SELECT id FROM domains WHERE service_id = $1`, m.serviceID))
	if len(r.Upstreams) != 1 || r.Upstreams[0].ReleaseId != v1 || r.Upstreams[0].Weight != 100 {
		t.Fatalf("upstreams %v", r.Upstreams)
	}
	// A failed deployment makes an alert event with the reason code (REQ-ALERT-007).
	if n := m.q(t, `SELECT count(*) FROM events WHERE topic = 'alert' AND object_id = $1 AND payload->>'rule' = 'deployment_rolled_back' AND payload->>'reason' = 'gate_5xx'`, id); n != "1" {
		t.Fatalf("alert events: %s", n)
	}
}

// The p95 gate: green is slower than blue x ratio.
func TestCanaryLatencyGateRollsBack(t *testing.T) {
	m, g := setupCanary(t)
	g.set(obs.CanaryStats{GreenRequests: 100, GreenP95: 0.5, BlueP95: 0.1, GreenP95Known: true, BlueP95Known: true})
	m.runClock(t)
	_, state, reason := m.deploy(t, canaryParams())
	if state != "rolled_back" || reason != "gate_latency" {
		t.Fatalf("canary %s %s", state, reason)
	}
}

// During a step the green containers get the share of the traffic as weights (REQ-DEPLOY-006).
func TestCanaryStepWeights(t *testing.T) {
	m, g := setupCanary(t)
	g.set(obs.CanaryStats{GreenRequests: 200})
	p := canaryParams()
	p.CanaryStepIntervalSeconds = one(3)
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, Strategy: p.Strategy,
		CanarySteps: p.CanarySteps, CanaryStepIntervalSeconds: p.CanaryStepIntervalSeconds})
	if err != nil {
		t.Fatal(err)
	}
	m.waitState(t, id, "shifting")
	ds := lastState(t, m.agent)
	r := routeByID(ds, m.q(t, `SELECT id FROM domains WHERE service_id = $1`, m.serviceID))
	w := map[string]uint32{}
	for _, u := range r.Upstreams {
		w[u.ReleaseId] = u.Weight
	}
	if len(r.Upstreams) != 2 || w[m.releaseID(t, 1)] != 9 || w[m.releaseID(t, 2)] != 1 {
		t.Fatalf("weights at 10%%: %v", r.Upstreams)
	}
	if err := m.ctl.CanaryAction(context.Background(), id, "abort", ""); err != nil {
		t.Fatal(err)
	}
	if state, reason := m.waitDeployment(t, id); state != "rolled_back" || reason != "cancelled_by_user" {
		t.Fatalf("abort: %s %s", state, reason)
	}
}

// Promote skips the wait of the step. Pause stops the timer.
func TestCanaryPromoteAndPause(t *testing.T) {
	m, g := setupCanary(t)
	g.set(obs.CanaryStats{GreenRequests: 200})
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, Strategy: "canary",
		CanarySteps: []int{10, 100}, CanaryStepIntervalSeconds: one(600)})
	if err != nil {
		t.Fatal(err)
	}
	m.waitState(t, id, "shifting")
	if err := m.ctl.CanaryAction(context.Background(), id, "pause", ""); err != nil {
		t.Fatal(err)
	}
	if m.q(t, `SELECT canary_paused::text FROM deployments WHERE id = $1`, id) != "true" {
		t.Fatal("not paused")
	}
	if err := m.ctl.CanaryAction(context.Background(), id, "resume", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ctl.CanaryAction(context.Background(), id, "promote", ""); err != nil {
		t.Fatal(err)
	}
	if state, reason := m.waitDeployment(t, id); state != "live" {
		t.Fatalf("promote: %s %s", state, reason)
	}
	// A finished deployment is not a running canary.
	if err := m.ctl.CanaryAction(context.Background(), id, "abort", ""); err == nil {
		t.Fatal("abort of a finished canary worked")
	}
}
