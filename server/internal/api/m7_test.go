// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/orchestrator"
)

// The node actions of the fake orchestrator. An action on a state that does not allow it answers like the real one.
type nodeCalls struct {
	actions []string
	err     error
	moves   []string
}

var m7Calls = map[*fakeOrch]*nodeCalls{}

func (f *fakeOrch) calls() *nodeCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m7Calls[f] == nil {
		m7Calls[f] = &nodeCalls{}
	}
	return m7Calls[f]
}

func (f *fakeOrch) record(action string) error {
	c := f.calls()
	c.actions = append(c.actions, action)
	return c.err
}

func (f *fakeOrch) CordonNode(_ context.Context, _, id, _ string) error {
	return f.record("cordon:" + id)
}
func (f *fakeOrch) UncordonNode(_ context.Context, _, id, _ string) error {
	return f.record("uncordon:" + id)
}
func (f *fakeOrch) DrainNode(_ context.Context, _, id, _ string) error {
	return f.record("drain:" + id)
}
func (f *fakeOrch) SetMaintenance(_ context.Context, _, id string, on bool, _ string) error {
	if on {
		return f.record("maintenance:on:" + id)
	}
	return f.record("maintenance:off:" + id)
}
func (f *fakeOrch) DeleteNode(_ context.Context, _, id string, force bool, _ string) error {
	if force {
		return f.record("delete:force:" + id)
	}
	return f.record("delete:" + id)
}
func (f *fakeOrch) NodesChanged(context.Context) error { return f.record("changed") }
func (f *fakeOrch) SelfCheck(context.Context, string, string) (orchestrator.SelfCheckResult, error) {
	return orchestrator.SelfCheckResult{Docker: true, Caddy: true, Wireguard: false, Errors: []string{"wireguard: no interface"}}, nil
}
func (f *fakeOrch) MoveService(ctx context.Context, serviceID, target, copyMode, actor string) (string, []orchestrator.Warning, error) {
	f.calls().moves = append(f.calls().moves, serviceID+">"+target+":"+copyMode)
	id, err := f.Deploy(ctx, serviceID, actor, "move")
	return id, nil, err
}

func (e *testEnv) addNode(t *testing.T, id, name, state string) {
	t.Helper()
	var orgID string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM orgs`).Scan(&orgID)
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO nodes (id, org_id, name, state, labels, mesh_subnet) VALUES ($1,$2,$3,$4,'{"ingress":"true"}','10.210.1.0/24')`, id, orgID, name, state); err != nil {
		t.Fatal(err)
	}
}

// REQ-NODE-003, REQ-NODE-008: the node actions answer with the node, and the errors have the right code.
func TestREQ_NODE_003_NodeActions(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	e.addNode(t, "nod_a", "a", "ready")
	e.addNode(t, "nod_b", "b", "ready")
	c.ok(200, "POST", "/api/v1/nodes/nod_a/cordon", nil)
	c.ok(200, "POST", "/api/v1/nodes/nod_a/uncordon", nil)
	c.ok(202, "POST", "/api/v1/nodes/nod_a/drain", nil)
	c.ok(200, "POST", "/api/v1/nodes/nod_a/maintenance", map[string]any{"enabled": true})
	c.ok(200, "POST", "/api/v1/nodes/nod_a/maintenance", map[string]any{"enabled": false})
	c.ok(404, "POST", "/api/v1/nodes/nod_none/cordon", nil)
	if rec := c.do("POST", "/api/v1/nodes/nod_a/maintenance", map[string]any{}); rec.Code != 422 {
		t.Fatalf("missing enabled: %d", rec.Code)
	}
	want := []string{"cordon:nod_a", "uncordon:nod_a", "drain:nod_a", "maintenance:on:nod_a", "maintenance:off:nod_a"}
	if got := strings.Join(e.orch.calls().actions, ","); got != strings.Join(want, ",") {
		t.Fatalf("calls %s", got)
	}

	e.orch.calls().err = orchestrator.ErrNodeNotEmpty
	rec := c.do("DELETE", "/api/v1/nodes/nod_a", nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"node_not_empty"`) {
		t.Fatalf("delete with workloads: %d %s", rec.Code, rec.Body)
	}
	e.orch.calls().err = nil
	c.ok(204, "DELETE", "/api/v1/nodes/nod_a?force=true", nil)
	e.orch.calls().err = orchestrator.ErrNodeState
	if rec := c.do("POST", "/api/v1/nodes/nod_b/cordon", nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "node_state") {
		t.Fatalf("wrong state: %d %s", rec.Code, rec.Body)
	}
	e.orch.calls().err = nil

	out := c.ok(200, "POST", "/api/v1/nodes/nod_b/self-check", nil)
	if out["docker"] != true || out["wireguard"] != false {
		t.Fatalf("self-check: %v", out)
	}
}

// A developer cannot manage nodes (node:manage is for admin and owner).
func TestREQ_NODE_003_NodeActionsNeedPermission(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	e.owner()
	e.addNode(t, "nod_a", "a", "ready")
	dev := e.login("usr_dev", "dev@example.com", "developer")
	viewer := e.login("usr_view", "view@example.com", "viewer")
	for _, path := range []string{"/cordon", "/drain", "/uncordon"} {
		if rec := dev.do("POST", "/api/v1/nodes/nod_a"+path, nil); rec.Code != 403 {
			t.Fatalf("developer %s: %d", path, rec.Code)
		}
	}
	if rec := dev.do("DELETE", "/api/v1/nodes/nod_a", nil); rec.Code != 403 {
		t.Fatalf("developer delete: %d", rec.Code)
	}
	viewer.ok(200, "GET", "/api/v1/nodes/nod_a", nil)
	if len(e.orch.calls().actions) != 0 {
		t.Fatal("an action reached the engine")
	}
}

// REQ-NODE-004, REQ-NODE-006: the labels and the name of a node can change, the placement settings are checked, a
// move needs a target and does not accept direct copy.
func TestREQ_NODE_006_UpdateNodeMoveAndPlacement(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	e.addNode(t, "nod_a", "a", "ready")
	e.addNode(t, "nod_b", "b", "ready")
	out := c.ok(200, "PATCH", "/api/v1/nodes/nod_a", map[string]any{"labels": map[string]any{"zone": "east", "ingress": "true"}, "name": "alpha"})
	if str(out, "name") != "alpha" {
		t.Fatalf("patch: %v", out)
	}
	if rec := c.do("PATCH", "/api/v1/nodes/nod_a", map[string]any{"name": "b"}); rec.Code != 409 {
		t.Fatalf("name in use: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("PATCH", "/api/v1/nodes/nod_a", map[string]any{"name": "Bad Name"}); rec.Code != 422 {
		t.Fatalf("bad name: %d", rec.Code)
	}
	got := c.ok(200, "GET", "/api/v1/nodes/nod_a", nil)
	if str(got, "mesh_subnet") != "10.210.1.0/24" {
		t.Fatalf("mesh subnet: %v", got)
	}
	if changed := strings.Count(strings.Join(e.orch.calls().actions, ","), "changed"); changed != 1 {
		t.Fatalf("a change must send new desired states: %d", changed)
	}

	_, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	// Placement and replicas in the config.
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80, "replicas": 2,
		"placement": map[string]any{"nodes": []string{"alpha"}, "labels": map[string]any{"zone": "east"}, "spread": false}}})
	for _, bad := range []map[string]any{
		{"replicas": 101}, {"replicas": -1}, {"placement": "alpha"}, {"placement": map[string]any{"nodes": "alpha"}},
		{"placement": map[string]any{"labels": map[string]any{"a": 1}}}, {"placement": map[string]any{"spread": "yes"}}, {"placement": map[string]any{"colour": "red"}},
	} {
		bad["image"] = "nginx:1.27"
		if rec := c.do("PATCH", "/api/v1/services/"+svc, map[string]any{"config": bad}); rec.Code != 422 {
			t.Fatalf("%v: %d", bad, rec.Code)
		}
	}
	// failover settings and the failover status of the service.
	s := c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"failover": "auto", "allow_data_loss_failover": true})
	fs, _ := s["failover_status"].(map[string]any)
	if s["failover"] != "auto" || fs["state"] != "healthy" {
		t.Fatalf("service: %v", s)
	}

	if rec := c.do("POST", "/api/v1/services/"+svc+"/move", map[string]any{}); rec.Code != 422 {
		t.Fatalf("move without a target: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/services/"+svc+"/move", map[string]any{"target_node_id": "nod_none"}); rec.Code != 422 {
		t.Fatalf("unknown target: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/services/"+svc+"/move", map[string]any{"target_node_id": "nod_b", "volume_copy": "direct"}); rec.Code != 422 {
		t.Fatalf("direct copy: %d", rec.Code)
	}
	dep := c.ok(202, "POST", "/api/v1/services/"+svc+"/move", map[string]any{"target_node_id": "nod_b", "volume_copy": "backup"})
	if str(dep, "id") == "" || strings.Join(e.orch.calls().moves, ",") != svc+">nod_b:backup" {
		t.Fatalf("move: %v %v", dep, e.orch.calls().moves)
	}
}

// The install command points to /install-agent.sh. The panel serves the script without a login.
func TestREQ_NODE_001_InstallAgentScript(t *testing.T) {
	e := newTestEnv(t, "https://panel.example.com")
	req := httptest.NewRequest("GET", "/install-agent.sh", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(body, "#!/bin/sh") || !strings.Contains(body, "polipan-agent join") || !strings.Contains(body, "--ca-fingerprint") ||
		!strings.Contains(rec.Header().Get("Content-Type"), "shellscript") {
		t.Fatalf("script: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if strings.Contains(body, "pj_") && !strings.Contains(body, "pj_...") {
		t.Fatal("the script must not hold a token")
	}
}
