// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"strings"
	"testing"
)

// UI-12 moveDomain: one change, both services told, the audit event, the protected-environment rule.
func TestM10b_MoveDomain(t *testing.T) {
	e := newTestEnv(t, "http://panel.example.com")
	c := e.owner()
	_, envs := c.project("Shop")
	a, b := c.service(envs["staging"], "web"), c.service(envs["staging"], "api")
	prod := c.service(envs["production"], "web")
	dom := c.ok(201, "POST", "/api/v1/services/"+a+"/domains", map[string]any{"host": "move.example.com"})
	id := str(dom, "id")
	dev := e.userClient("usr_dev", "dev@example.com", "developer")

	if rec := c.do("POST", "/api/v1/domains/"+id+"/move", map[string]any{}); rec.Code != 422 {
		t.Fatalf("no target: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": "svc_missing"}); rec.Code != 422 {
		t.Fatalf("unknown target: %d %s", rec.Code, rec.Body)
	}
	// A developer cannot move a domain into a protected environment.
	if rec := dev.do("POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": prod}); rec.Code != 403 || !strings.Contains(rec.Body.String(), "protected_environment") {
		t.Fatalf("developer to protected: %d %s", rec.Code, rec.Body)
	}
	// A viewer cannot move at all.
	viewer := e.userClient("usr_view", "view@example.com", "viewer")
	if rec := viewer.do("POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": b}); rec.Code != 403 {
		t.Fatalf("viewer: %d %s", rec.Code, rec.Body)
	}
	before := len(e.orch.domains)
	moved := dev.ok(200, "POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": b})
	if str(moved, "service_id") != b || str(moved, "host") != "move.example.com" {
		t.Fatalf("moved: %v", moved)
	}
	if len(e.orch.domains) != before+2 {
		t.Fatalf("both services must be told: %v", e.orch.domains)
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'domain.move' AND target_id = $1`, id) != 1 {
		t.Fatal("no domain.move audit event")
	}
	// The owner can move it into the protected environment.
	if got := c.ok(200, "POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": prod}); str(got, "service_id") != prod {
		t.Fatalf("owner to protected: %v", got)
	}
}

// UI-28 deleteOrg: only an owner, the slug must be typed, sessions end, rows are soft deleted.
func TestM10b_DeleteOrg(t *testing.T) {
	e := newTestEnv(t, "http://panel.example.com")
	c := e.owner()
	_, envs := c.project("Shop")
	c.service(envs["staging"], "web")
	admin := e.userClient("usr_adm", "adm@example.com", "admin")
	slug := e.queryString(`SELECT slug FROM orgs`)

	if rec := admin.do("DELETE", "/api/v1/org?confirm="+slug, nil); rec.Code != 403 {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("DELETE", "/api/v1/org?confirm=wrong", nil); rec.Code != 422 {
		t.Fatalf("wrong slug: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("DELETE", "/api/v1/org?confirm="+slug, nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM orgs WHERE deleted_at IS NOT NULL`) != 1 || e.count(`SELECT count(*) FROM services WHERE deleted_at IS NULL`) != 0 ||
		e.count(`SELECT count(*) FROM projects WHERE deleted_at IS NULL`) != 0 || e.count(`SELECT count(*) FROM sessions`) != 0 {
		t.Fatal("the org was not soft deleted with its content and sessions")
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'org.delete'`) != 1 {
		t.Fatal("no org.delete audit event")
	}
	if rec := c.do("GET", "/api/v1/org", nil); rec.Code != 401 {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
	}
}

// SEC-1: moveDomain checks the target project too. A token limited to one project, and a user with a viewer role in the
// target project, cannot give a domain to a service of that project.
func TestM10b_MoveDomainChecksTheTargetProject(t *testing.T) {
	e := newTestEnv(t, "http://panel.example.com")
	c := e.owner()
	projA, envsA := c.project("alpha")
	projB, envsB := c.project("beta")
	a := c.service(envsA["staging"], "web")
	b := c.service(envsB["staging"], "internal")
	dom := c.ok(201, "POST", "/api/v1/services/"+a+"/domains", map[string]any{"host": "cross.example.com"})
	id := str(dom, "id")

	// A token of project alpha: the target is in project beta.
	tok := c.ok(201, "POST", "/api/v1/tokens", map[string]any{"name": "alpha", "scopes": []string{"write"}, "project_id": projA})
	rec := e.bearer(str(tok, "token"), "POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": b})
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "token_project") {
		t.Fatalf("limited token into another project: %d %s", rec.Code, rec.Body)
	}
	// A developer with a viewer role in project beta.
	dev := e.userClient("usr_dev", "dev@example.com", "developer")
	c.ok(200, "PUT", "/api/v1/projects/"+projB+"/roles", []map[string]any{{"user_id": "usr_dev", "role": "viewer"}})
	if rec := dev.do("POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": b}); rec.Code != 403 {
		t.Fatalf("viewer in the target project: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM domains WHERE id = $1 AND service_id = $2`, id, a) != 1 {
		t.Fatal("the domain moved although the request was refused")
	}
	// The owner still can.
	c.ok(200, "POST", "/api/v1/domains/"+id+"/move", map[string]any{"target_service_id": b})
}
