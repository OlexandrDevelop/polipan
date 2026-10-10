// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

type fixture struct {
	env       *gatewaytest.Env
	ctl       *control.Control
	serviceID string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	env := gatewaytest.Start(t)
	env.Cfg.ExternalURL = "https://panel.example.com"
	env.Cfg.ACMEEmail = "ops@example.com"
	ctl := control.New(control.Deps{DB: env.Pool, Clock: env.Clock, Config: env.Cfg, CA: env.CA, Sender: env.GW})
	t.Cleanup(ctl.Close)
	env.GW.State = ctl

	ctx := context.Background()
	prj, envID, svc := gateway.NewID("prj"), gateway.NewID("env"), gateway.NewID("svc")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, org_id, name, slug) VALUES ($1,$2,'p','p')`, []any{prj, env.OrgID}},
		{`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ($1,$2,'prod','prod','production')`, []any{envID, prj}},
		{`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1,$2,'web','app','image',
		   '{"image":"nginx:1.27","port":8080,"health":{"type":"tcp","port":8080}}')`, []any{svc, envID}},
	} {
		if _, err := env.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{env: env, ctl: ctl, serviceID: svc}
}

func okAgent(a *gatewaytest.Agent, healthErr *pb.CommandAck, pullErr *pb.CommandAck) {
	a.OnCommand = func(c *pb.Command) *pb.CommandAck {
		switch c.Body.(type) {
		case *pb.Command_PullImage:
			if pullErr != nil {
				return pullErr
			}
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"nginx@sha256:abc123","digest":"sha256:abc123","image_digest":"nginx@sha256:abc123"}`)}
		case *pb.Command_CheckHealth:
			if healthErr != nil {
				return healthErr
			}
		}
		return &pb.CommandAck{Ok: true}
	}
}

func (f *fixture) waitDeployment(t *testing.T, id string) (state, reason string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var r *string
		if err := f.env.Pool.QueryRow(context.Background(), `SELECT state, failure_reason FROM deployments WHERE id = $1`, id).Scan(&state, &r); err != nil {
			t.Fatal(err)
		}
		if state == "live" || state == "failed" {
			if r != nil {
				reason = *r
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment stuck in %s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lastState returns the newest desired state the agent got (it waits for at least one).
func lastState(t *testing.T, a *gatewaytest.Agent) *pb.DesiredState {
	t.Helper()
	ds := a.WaitState(t)
	for {
		select {
		case n := <-a.States:
			ds = n
		case <-time.After(200 * time.Millisecond):
			return ds
		}
	}
}

func routeByID(ds *pb.DesiredState, id string) *pb.Route {
	for _, r := range ds.Ingress.Routes {
		if r.RouteId == id {
			return r
		}
	}
	return nil
}

func TestNodeStateMonitor(t *testing.T) {
	f := setup(t)
	a := f.env.NewReadyNode(t, "n1")
	nodeState := func() string {
		var s string
		if err := f.env.Pool.QueryRow(context.Background(), `SELECT state FROM nodes WHERE id = $1`, a.NodeID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for nodeState() != "ready" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := nodeState(); s != "ready" {
		t.Fatalf("state %s", s)
	}
	f.env.Clock.Advance(10 * time.Second)
	_ = f.ctl.CheckNodes(ctx)
	if s := nodeState(); s != "ready" {
		t.Fatalf("after 10s: %s", s)
	}
	f.env.Clock.Advance(10 * time.Second)
	_ = f.ctl.CheckNodes(ctx)
	if s := nodeState(); s != "suspect" {
		t.Fatalf("after 20s: %s", s)
	}
	f.env.Clock.Advance(60 * time.Second)
	_ = f.ctl.CheckNodes(ctx)
	if s := nodeState(); s != "down" {
		t.Fatalf("after 80s: %s", s)
	}
	a.Heartbeat(t)
	deadline = time.Now().Add(5 * time.Second)
	for nodeState() != "ready" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := nodeState(); s != "ready" {
		t.Fatalf("after heartbeat: %s", s)
	}
	var n int
	_ = f.env.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'node' AND object_id = $1 AND payload->>'to' IN ('suspect','down')`, a.NodeID).Scan(&n)
	if n != 2 {
		t.Fatalf("expected 2 state events, got %d", n)
	}
}

func TestDeployAndDesiredState(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.ctl.Deploy(ctx, f.serviceID, "", "manual"); !errors.Is(err, orchestrator.ErrNoNode) {
		t.Fatalf("no node: %v", err)
	}
	a := f.env.NewReadyNode(t, "n1")
	okAgent(a, nil, nil)
	// A domain exists before the first deploy: the route has no upstream (503).
	if _, err := f.env.Pool.Exec(ctx, `INSERT INTO domains (id, service_id, host, container_port) VALUES ($1,$2,'app.example.com',8080)`,
		gateway.NewID("dom"), f.serviceID); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.DomainsChanged(ctx, f.serviceID); err != nil {
		t.Fatal(err)
	}
	ds := lastState(t, a)
	if r := routeByID(ds, "panel"); r == nil || !r.Https || r.Hosts[0] != "panel.example.com" ||
		r.Upstreams[0].Address != "polipan-server:8080" || r.ServiceId != "" {
		t.Fatalf("panel route: %v", r)
	}
	if ds.Ingress.AcmeEmail != "ops@example.com" || len(ds.Workloads) != 0 {
		t.Fatalf("state: %v", ds)
	}
	var domRoute *pb.Route
	for _, r := range ds.Ingress.Routes {
		if r.ServiceId == f.serviceID {
			domRoute = r
		}
	}
	if domRoute == nil || len(domRoute.Upstreams) != 0 {
		t.Fatalf("never deployed route: %v", domRoute)
	}

	// Deploy: pull, release, placement, health, live.
	depID, err := f.ctl.Deploy(ctx, f.serviceID, "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := f.waitDeployment(t, depID); state != "live" {
		t.Fatalf("deployment %s %s", state, reason)
	}
	var number int
	var imageRef, live, role string
	var placement string
	if err := f.env.Pool.QueryRow(ctx, `SELECT r.number, r.image_ref, s.live_release_id = r.id FROM releases r JOIN services s ON s.id = r.service_id`).
		Scan(&number, &imageRef, new(bool)); err != nil {
		t.Fatal(err)
	}
	_ = live
	if number != 1 || imageRef != "nginx@sha256:abc123" {
		t.Fatalf("release %d %s", number, imageRef)
	}
	if err := f.env.Pool.QueryRow(ctx, `SELECT id, role FROM placements WHERE service_id = $1`, f.serviceID).Scan(&placement, &role); err != nil || role != "live" {
		t.Fatalf("placement %s %v", role, err)
	}
	ds = lastState(t, a)
	if len(ds.Workloads) != 1 || ds.Workloads[0].PlacementId != placement || ds.Workloads[0].ContainerName != "pp-"+placement {
		t.Fatalf("workloads: %v", ds.Workloads)
	}
	for _, r := range ds.Ingress.Routes {
		if r.ServiceId == f.serviceID {
			domRoute = r
		}
	}
	if len(domRoute.Upstreams) != 1 || domRoute.Upstreams[0].Address != "pp-"+placement+":8080" {
		t.Fatalf("running route: %v", domRoute.Upstreams)
	}

	// Stop: zero upstreams, container stays. Start: back.
	if err := f.ctl.SetRunning(ctx, f.serviceID, false); err != nil {
		t.Fatal(err)
	}
	ds = lastState(t, a)
	if len(ds.Workloads) != 1 || ds.Workloads[0].Role != "stopped" {
		t.Fatalf("stopped workloads: %v", ds.Workloads)
	}
	for _, r := range ds.Ingress.Routes {
		if r.ServiceId == f.serviceID && len(r.Upstreams) != 0 {
			t.Fatalf("stopped route has upstreams: %v", r.Upstreams)
		}
	}
	if err := f.ctl.SetRunning(ctx, f.serviceID, true); err != nil {
		t.Fatal(err)
	}
	before := lastState(t, a)
	if before.Workloads[0].Role != "live" {
		t.Fatalf("started workloads: %v", before.Workloads)
	}

	// A new domain changes the ingress and the version, not the workloads.
	if _, err := f.env.Pool.Exec(ctx, `INSERT INTO domains (id, service_id, host, container_port) VALUES ($1,$2,'two.example.com',8080)`,
		gateway.NewID("dom"), f.serviceID); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.DomainsChanged(ctx, f.serviceID); err != nil {
		t.Fatal(err)
	}
	after := lastState(t, a)
	if after.Version <= before.Version || len(after.Ingress.Routes) != len(before.Ingress.Routes)+1 {
		t.Fatalf("versions %d -> %d, routes %d -> %d", before.Version, after.Version, len(before.Ingress.Routes), len(after.Ingress.Routes))
	}
	if len(after.Workloads) != 1 || !proto.Equal(after.Workloads[0], before.Workloads[0]) {
		t.Fatal("workloads changed after a domain change")
	}
	var releases int
	_ = f.env.Pool.QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&releases)
	if releases != 1 {
		t.Fatalf("releases: %d", releases)
	}
}

func TestDeployFailures(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := f.env.NewReadyNode(t, "n1")

	okAgent(a, nil, &pb.CommandAck{ErrorCode: "image_pull_failed", ErrorMessage: "not found"})
	id, err := f.ctl.Deploy(ctx, f.serviceID, "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := f.waitDeployment(t, id); state != "failed" || reason != "image_pull_failed" {
		t.Fatalf("pull failure: %s %s", state, reason)
	}

	okAgent(a, &pb.CommandAck{ErrorCode: "deadline_exceeded", ErrorMessage: "not healthy"}, nil)
	id, err = f.ctl.Deploy(ctx, f.serviceID, "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := f.waitDeployment(t, id); state != "failed" || reason != "health_check_failed" {
		t.Fatalf("health failure: %s %s", state, reason)
	}
	var live *string
	_ = f.env.Pool.QueryRow(ctx, `SELECT live_release_id FROM services WHERE id = $1`, f.serviceID).Scan(&live)
	if live != nil {
		t.Fatal("a failed deployment must not set the live release")
	}

	// Restart recovery: a deployment left in an active state is marked failed.
	var envID string
	_ = f.env.Pool.QueryRow(ctx, `SELECT environment_id FROM services WHERE id = $1`, f.serviceID).Scan(&envID)
	stale := gateway.NewID("dep")
	if _, err := f.env.Pool.Exec(ctx, `INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state) VALUES ($1,$2,$3,'manual','recreate','starting')`,
		stale, f.serviceID, envID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = f.ctl.Run(runCtx); close(done) }()
	if state, reason := f.waitDeployment(t, stale); state != "failed" || reason != "server_restarted" {
		t.Fatalf("recovery: %s %s", state, reason)
	}
	cancel()
	<-done
}
