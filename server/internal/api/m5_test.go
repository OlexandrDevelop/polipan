// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	specapi "github.com/polipan/polipan/api"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/store"
)

// userClient makes a user with a role and logs them in.
func (e *testEnv) userClient(id, email, role string) *client {
	e.t.Helper()
	hash, err := auth.HashPassword(ownerPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	var orgID string
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM orgs`).Scan(&orgID); err != nil {
		e.t.Fatal(err)
	}
	e.addUser(id, email, role, hash, orgID)
	return e.signIn(email, ownerPassword)
}

func (e *testEnv) signIn(email, password string) *client {
	e.t.Helper()
	c := e.newClient()
	rec := c.do("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": password})
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	c.takeSession(rec)
	return c
}

// bearer makes a request with an API token and no cookie.
func (e *testEnv) bearer(token, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	c := e.newClient()
	c.cookie, c.csrf = nil, ""
	var rd *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = c.addr
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// matrixRows reads the matrix of docs/spec/10-security.md: permission -> letters for owner, admin, developer, viewer.
func matrixRows(t *testing.T) map[string]string {
	t.Helper()
	raw, err := readFile("../../../docs/spec/10-security.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, _ := strings.Cut(raw, "### Permission matrix")
	row := regexp.MustCompile("(?m)^\\| `([a-z]+:[a-z]+)` \\| ([YP-]) \\| ([YP-]) \\| ([YP-]) \\| ([YP-]) \\|")
	out := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(after, -1) {
		out[m[1]] = m[2] + m[3] + m[4] + m[5]
	}
	if len(out) < 40 {
		t.Fatalf("only %d matrix rows", len(out))
	}
	return out
}

// fillPath puts ids in the path parameters. ids not in the map get "x" ("1" for a version number).
func fillPath(path string, ids map[string]string) string {
	return regexp.MustCompile(`\{([a-z_]+)\}`).ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := ids[name]; ok {
			return v
		}
		if name == "version" {
			return "1"
		}
		return "x"
	})
}

// call runs a request that may not take longer than 400 ms (a stream is cut).
func (c *client) call(method, path string) int {
	c.e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var req *http.Request
	if method == "DELETE" {
		// Deletes of big things need confirm. Without it the 422 comes before the login check.
		path += "?confirm=x"
	}
	if method == "GET" || method == "DELETE" {
		req = httptest.NewRequest(method, path, nil).WithContext(ctx)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader("{}")).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = c.addr
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	c.e.h.ServeHTTP(rec, req)
	return rec.Code
}

// T-E2E-10, AC-RBAC-01, AC-RBAC-02: the matrix test is generated from the OpenAPI file and the matrix of the spec.
// Every operation with a matrix permission is called by the four roles. A role that may not gets 403, a role
// that may gets anything else. Then the protected production: a developer gets 403 on every P permission, on staging not.
func TestT_E2E_10_PermissionMatrix(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	clients := []*client{owner,
		e.userClient("usr_a", "admin@example.com", "admin"),
		e.userClient("usr_d", "dev@example.com", "developer"),
		e.userClient("usr_v", "viewer@example.com", "viewer")}
	roles := []string{"owner", "admin", "developer", "viewer"}
	matrix := matrixRows(t)

	ops, err := api.ParseOperations(specapi.Spec)
	if err != nil {
		t.Fatal(err)
	}
	tested := 0
	for _, op := range ops {
		letters, ok := matrix[op.Permission]
		if !ok {
			continue
		}
		path := "/api/v1" + fillPath(op.Path, nil)
		for i, c := range clients {
			status := c.call(op.Method, path)
			allowed := letters[i] != '-'
			if allowed && (status == 403 || status == 401) || !allowed && status != 403 {
				t.Errorf("%s %s (%s) as %s: status %d, the matrix says %q", op.Method, op.Path, op.Permission, roles[i], status, string(letters[i]))
			}
			tested++
		}
	}
	if tested < 400 {
		t.Fatalf("only %d calls: the generator found too few operations", tested)
	}

	// Protected environments. Production is protected by default.
	projectID, envs := owner.project("shop")
	prodSvc, stagSvc := owner.service(envs["production"], "web"), owner.service(envs["staging"], "web")
	scopes := map[string]map[string]string{
		"production": {"project_id": projectID, "environment_id": envs["production"], "service_id": prodSvc},
		"staging":    {"project_id": projectID, "environment_id": envs["staging"], "service_id": stagSvc},
	}
	dev := clients[2]
	// AC-RBAC-02 in words: deploy is 403 on production and 202 on staging for a developer.
	if rec := dev.do("POST", "/api/v1/services/"+prodSvc+"/deploy", nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "protected_environment") {
		t.Fatalf("developer deploy on production: %d %s", rec.Code, rec.Body)
	}
	if rec := dev.do("POST", "/api/v1/services/"+stagSvc+"/deploy", nil); rec.Code != 202 {
		t.Fatalf("developer deploy on staging: %d %s", rec.Code, rec.Body)
	}
	// A developer cannot lift the protection (REQ-RBAC-004).
	if rec := dev.do("PATCH", "/api/v1/environments/"+envs["production"], map[string]any{"protected": false}); rec.Code != 403 {
		t.Fatalf("developer unprotects production: %d %s", rec.Code, rec.Body)
	}

	for _, step := range []struct {
		scope  string
		client int
		deny   bool
	}{{"production", 2, true}, {"production", 3, true}, {"staging", 2, false}, {"production", 1, false}} {
		for _, op := range ops {
			letters, ok := matrix[op.Permission]
			if !ok || letters[2] != 'P' {
				continue
			}
			if !strings.Contains(op.Path, "{service_id}") && !strings.Contains(op.Path, "{environment_id}") {
				continue
			}
			status := clients[step.client].call(op.Method, "/api/v1"+fillPath(op.Path, scopes[step.scope]))
			if step.deny && status != 403 || !step.deny && (status == 403 || status == 401) {
				t.Errorf("%s %s (%s) as %s on %s: status %d, deny=%v", op.Method, op.Path, op.Permission, roles[step.client], step.scope, status, step.deny)
			}
		}
	}
}

// AC-RBAC-03: a project role of viewer for an org developer makes the project read only for them. A team role raises a viewer.
func TestAC_RBAC_03_ProjectRoleOverride(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	dev := e.userClient("usr_d", "dev@example.com", "developer")
	viewer := e.userClient("usr_v", "viewer@example.com", "viewer")
	e.userClient("usr_a", "admin@example.com", "admin")
	projectID, envs := owner.project("shop")
	other, _ := owner.project("other")
	svc := owner.service(envs["staging"], "web")
	patch := map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 8080}}

	dev.ok(200, "PATCH", "/api/v1/services/"+svc, patch)
	if rec := viewer.do("PATCH", "/api/v1/services/"+svc, patch); rec.Code != 403 {
		t.Fatalf("viewer edits: %d", rec.Code)
	}

	// Only members with member:manage change roles.
	if rec := dev.do("PUT", "/api/v1/projects/"+projectID+"/roles", []map[string]any{{"user_id": "usr_d", "role": "admin"}}); rec.Code != 403 {
		t.Fatalf("developer sets roles: %d", rec.Code)
	}
	owner.ok(200, "PUT", "/api/v1/projects/"+projectID+"/roles", []map[string]any{{"user_id": "usr_d", "role": "viewer"}})
	if rec := dev.do("PATCH", "/api/v1/services/"+svc, patch); rec.Code != 403 {
		t.Fatalf("developer with a viewer override edits: %d %s", rec.Code, rec.Body)
	}
	dev.ok(200, "GET", "/api/v1/services/"+svc, nil)
	// The override is for this project only.
	if rec := dev.do("POST", "/api/v1/projects/"+other+"/environments", map[string]any{"name": "qa", "kind": "staging"}); rec.Code == 403 {
		t.Fatalf("the override leaked to another project: %d", rec.Code)
	}
	me := dev.ok(200, "GET", "/api/v1/me", nil)
	if roles, _ := me["project_roles"].([]any); len(roles) != 1 || str(roles[0].(map[string]any), "project_id") != projectID {
		t.Fatalf("me project_roles: %v", me["project_roles"])
	}

	// SEC-1: the project is in the body of instantiateTemplate, the override must count there too.
	tplBody := map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{}}
	if rec := dev.do("POST", "/api/v1/templates/wordpress/instantiate", tplBody); rec.Code != 403 {
		t.Fatalf("developer with a viewer override instantiates a template: %d %s", rec.Code, rec.Body)
	}
	// A team with the developer role on the project raises the viewer. The highest override wins.
	team := owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "web"})
	owner.ok(204, "PUT", "/api/v1/teams/"+str(team, "id")+"/members/usr_v", nil)
	owner.ok(200, "PUT", "/api/v1/projects/"+projectID+"/roles", []map[string]any{
		{"user_id": "usr_d", "role": "viewer"}, {"team_id": str(team, "id"), "role": "developer"}})
	viewer.ok(200, "PATCH", "/api/v1/services/"+svc, patch)
	// Owner and admin keep their role: an override cannot lower them.
	owner.ok(200, "PUT", "/api/v1/projects/"+projectID+"/roles", []map[string]any{{"user_id": "usr_a", "role": "viewer"}})
	admin := e.signIn("admin@example.com", ownerPassword)
	admin.ok(200, "PATCH", "/api/v1/services/"+svc, patch)
	// An override for a user outside the org is refused.
	if rec := owner.do("PUT", "/api/v1/projects/"+projectID+"/roles", []map[string]any{{"user_id": "usr_nobody", "role": "viewer"}}); rec.Code != 422 {
		t.Fatalf("override for a stranger: %d", rec.Code)
	}
	// Deleting the team removes its roles.
	owner.ok(204, "DELETE", "/api/v1/teams/"+str(team, "id"), nil)
	if n := e.count(`SELECT count(*) FROM project_roles WHERE team_id IS NOT NULL`); n != 0 {
		t.Fatalf("team roles left: %d", n)
	}
}

// AC-RBAC-04, REQ-RBAC-006, REQ-RBAC-007, REQ-RBAC-008: invitations, roles, the last owner and removing a member.
func TestAC_RBAC_04_MembersAndLastOwner(t *testing.T) {
	e := newTestEnv(t, "https://panel.example.com")
	owner := e.owner()
	me := owner.ok(200, "GET", "/api/v1/me", nil)
	ownerID := str(me["user"].(map[string]any), "id")

	// The last owner cannot leave, be demoted or be disabled: 409 last_owner.
	for _, tc := range []struct{ method, body string }{{"DELETE", ""}, {"PATCH", `{"role":"admin"}`}, {"PATCH", `{"disabled":true}`}} {
		var body any
		if tc.body != "" {
			_ = json.Unmarshal([]byte(tc.body), &body)
		}
		rec := owner.do(tc.method, "/api/v1/members/"+ownerID, body)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"last_owner"`) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.body, rec.Code, rec.Body)
		}
	}

	// Invite: the answer has the link once. The token is stored as a hash.
	inv := owner.ok(201, "POST", "/api/v1/members/invitations", map[string]any{"email": "New@Example.com", "role": "admin"})
	link := str(inv, "invite_url")
	if !strings.HasPrefix(link, "https://panel.example.com/invite?token=") || str(inv, "status") != "invited" {
		t.Fatalf("invitation: %v", inv)
	}
	token := strings.TrimPrefix(link, "https://panel.example.com/invite?token=")
	if n := e.count(`SELECT count(*) FROM invitations WHERE token_hash = $1`, auth.HashToken(token)); n != 1 {
		t.Fatalf("invitation hash not stored")
	}
	if n := e.count(`SELECT count(*) FROM invitations WHERE token_hash = $1`, token); n != 0 {
		t.Fatalf("the invitation token is stored in clear")
	}
	list := owner.ok(200, "GET", "/api/v1/members", nil)["items"].([]any)
	if len(list) != 2 {
		t.Fatalf("members and invitations: %v", list)
	}
	// Accept: wrong token 404, then the user is created, logged in, and the link works once.
	pub := e.newClient()
	if rec := pub.do("POST", "/api/v1/auth/invitations/accept", map[string]any{"token": "nope", "name": "Neo", "password": ownerPassword}); rec.Code != 404 {
		t.Fatalf("bad token: %d", rec.Code)
	}
	rec := pub.do("POST", "/api/v1/auth/invitations/accept", map[string]any{"token": token, "name": "Neo", "password": ownerPassword})
	if rec.Code != 201 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	pub.takeSession(rec)
	if got := pub.ok(200, "GET", "/api/v1/me", nil); str(got, "org_role") != "admin" {
		t.Fatalf("new member: %v", got)
	}
	if rec := e.newClient().do("POST", "/api/v1/auth/invitations/accept", map[string]any{"token": token, "name": "Neo", "password": ownerPassword}); rec.Code != 404 {
		t.Fatalf("second accept: %d", rec.Code)
	}
	newID := str(pub.ok(200, "GET", "/api/v1/me", nil)["user"].(map[string]any), "id")

	// An expired invitation does not work.
	inv2 := owner.ok(201, "POST", "/api/v1/members/invitations", map[string]any{"email": "late@example.com", "role": "viewer"})
	e.clk.Advance(8 * 24 * time.Hour)
	tok2 := strings.TrimPrefix(str(inv2, "invite_url"), "https://panel.example.com/invite?token=")
	if rec := e.newClient().do("POST", "/api/v1/auth/invitations/accept", map[string]any{"token": tok2, "name": "Late", "password": ownerPassword}); rec.Code != 404 {
		t.Fatalf("expired invitation: %d", rec.Code)
	}
	// The sessions are older than the fake week: log in again.
	owner = e.signIn("owner@example.com", ownerPassword)
	pub = e.signIn("new@example.com", ownerPassword)

	// REQ-RBAC-008: an admin cannot change an owner, cannot make an owner, and cannot invite one.
	if rec := pub.do("PATCH", "/api/v1/members/"+ownerID, map[string]any{"role": "viewer"}); rec.Code != 403 {
		t.Fatalf("admin demotes owner: %d %s", rec.Code, rec.Body)
	}
	if rec := pub.do("PATCH", "/api/v1/members/"+newID, map[string]any{"role": "owner"}); rec.Code != 403 {
		t.Fatalf("admin makes an owner: %d", rec.Code)
	}
	if rec := pub.do("DELETE", "/api/v1/members/"+ownerID, nil); rec.Code != 403 {
		t.Fatalf("admin removes owner: %d", rec.Code)
	}
	if rec := pub.do("POST", "/api/v1/members/invitations", map[string]any{"email": "x@example.com", "role": "owner"}); rec.Code != 403 {
		t.Fatalf("admin invites an owner: %d", rec.Code)
	}
	// With a second owner the first one can leave.
	owner.ok(200, "PATCH", "/api/v1/members/"+newID, map[string]any{"role": "owner"})
	owner.ok(204, "DELETE", "/api/v1/members/"+ownerID, nil)
	if rec := owner.do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("a removed user keeps the session: %d", rec.Code)
	}
	// And now Neo is the last owner.
	if rec := pub.do("DELETE", "/api/v1/members/"+newID, nil); rec.Code != 409 {
		t.Fatalf("last owner leaves: %d", rec.Code)
	}
}

// REQ-AUTH-003, REQ-RBAC-006: sessions are listed and revoked, a removed member loses sessions and tokens at once.
func TestM5_SessionsTokensOfRemovedMember(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	dev := e.userClient("usr_d", "dev@example.com", "developer")
	second := e.signIn("dev@example.com", ownerPassword)

	list := dev.do("GET", "/api/v1/me/sessions", nil)
	var sessions []map[string]any
	_ = json.Unmarshal(list.Body.Bytes(), &sessions)
	if len(sessions) != 2 {
		t.Fatalf("sessions: %s", list.Body)
	}
	current := ""
	for _, s := range sessions {
		if s["current"] == true {
			current = str(s, "id")
		}
	}
	if current == "" || strings.Contains(list.Body.String(), "csrf") {
		t.Fatalf("current session: %s", list.Body)
	}
	for _, s := range sessions {
		if str(s, "id") != current {
			dev.ok(204, "DELETE", "/api/v1/me/sessions/"+str(s, "id"), nil)
		}
	}
	if rec := second.do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("revoked session still works: %d", rec.Code)
	}
	if rec := dev.do("DELETE", "/api/v1/me/sessions/"+current+"x", nil); rec.Code != 404 {
		t.Fatalf("unknown session: %d", rec.Code)
	}

	tok := dev.ok(201, "POST", "/api/v1/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}})
	if rec := e.bearer(str(tok, "token"), "GET", "/api/v1/projects", nil); rec.Code != 200 {
		t.Fatalf("token before removal: %d", rec.Code)
	}
	// An admin revokes all sessions of a member.
	owner.ok(204, "DELETE", "/api/v1/members/usr_d/sessions", nil)
	if rec := dev.do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("sessions after revoke all: %d", rec.Code)
	}
	owner.ok(204, "DELETE", "/api/v1/members/usr_d", nil)
	if rec := e.bearer(str(tok, "token"), "GET", "/api/v1/projects", nil); rec.Code != 401 {
		t.Fatalf("the token of a removed member works: %d", rec.Code)
	}
}

// AC-AUTH-02, REQ-AUTH-004, REQ-AUTH-101: a token with scope read cannot deploy. Scopes, project limit, expiry,
// revoke, hash only, and never more than the user.
func TestAC_AUTH_02_ApiTokenScopes(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	viewer := e.userClient("usr_v", "viewer@example.com", "viewer")
	projectID, envs := owner.project("shop")
	_, otherEnvs := owner.project("other")
	svc := owner.service(envs["staging"], "web")
	otherSvc := owner.service(otherEnvs["staging"], "web")

	mk := func(c *client, body map[string]any) map[string]any { return c.ok(201, "POST", "/api/v1/tokens", body) }
	read := mk(owner, map[string]any{"name": "read", "scopes": []string{"read"}})
	deploy := mk(owner, map[string]any{"name": "deploy", "scopes": []string{"deploy"}, "project_id": projectID})
	write := mk(owner, map[string]any{"name": "write", "scopes": []string{"write"}})
	admin := mk(owner, map[string]any{"name": "admin", "scopes": []string{"admin"}})
	if !strings.HasPrefix(str(read, "token"), "pp_") || len(str(read, "token")) != 43 || str(read, "token_prefix") != str(read, "token")[:8] {
		t.Fatalf("token format: %v", read)
	}
	// Only the hash is stored, and the list does not show the value.
	if e.count(`SELECT count(*) FROM api_tokens WHERE token_hash = $1`, auth.HashToken(str(read, "token"))) != 1 ||
		e.count(`SELECT count(*) FROM api_tokens WHERE token_hash = $1`, str(read, "token")) != 0 {
		t.Fatal("the token is not stored as a hash")
	}
	if rec := owner.do("GET", "/api/v1/tokens", nil); strings.Contains(rec.Body.String(), str(read, "token")) {
		t.Fatalf("the list shows a token value")
	}

	deployPath, getPath := "/api/v1/services/"+svc+"/deploy", "/api/v1/services/"+svc
	// AC-AUTH-02.
	if rec := e.bearer(str(read, "token"), "POST", deployPath, nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "token_scope") {
		t.Fatalf("read token deploys: %d %s", rec.Code, rec.Body)
	}
	if rec := e.bearer(str(read, "token"), "GET", getPath, nil); rec.Code != 200 {
		t.Fatalf("read token reads: %d", rec.Code)
	}
	if rec := e.bearer(str(deploy, "token"), "POST", deployPath, nil); rec.Code != 202 {
		t.Fatalf("deploy token deploys: %d %s", rec.Code, rec.Body)
	}
	// deploy is not write.
	if rec := e.bearer(str(deploy, "token"), "PATCH", getPath, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80}}); rec.Code != 403 {
		t.Fatalf("deploy token edits: %d", rec.Code)
	}
	// The project limit.
	if rec := e.bearer(str(deploy, "token"), "POST", "/api/v1/services/"+otherSvc+"/deploy", nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "token_project") {
		t.Fatalf("project limit: %d %s", rec.Code, rec.Body)
	}
	if rec := e.bearer(str(deploy, "token"), "GET", "/api/v1/projects", nil); rec.Code != 403 {
		t.Fatalf("a project token lists all projects: %d", rec.Code)
	}
	if rec := e.bearer(str(write, "token"), "PATCH", getPath, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80}}); rec.Code != 200 {
		t.Fatalf("write token edits: %d %s", rec.Code, rec.Body)
	}
	// write is not admin: no teams. read covers every *:read permission, so it covers the audit log.
	if rec := e.bearer(str(write, "token"), "POST", "/api/v1/teams", map[string]any{"name": "w"}); rec.Code != 403 {
		t.Fatalf("write token makes a team: %d", rec.Code)
	}
	if rec := e.bearer(str(read, "token"), "GET", "/api/v1/audit-events", nil); rec.Code != 200 {
		t.Fatalf("read token reads the audit log: %d", rec.Code)
	}
	if rec := e.bearer(str(admin, "token"), "GET", "/api/v1/audit-events", nil); rec.Code != 200 {
		t.Fatalf("admin token reads the audit log: %d", rec.Code)
	}
	// A token cannot make tokens or change the login of its user.
	if rec := e.bearer(str(admin, "token"), "POST", "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"admin"}}); rec.Code != 403 {
		t.Fatalf("a token makes a token: %d", rec.Code)
	}
	// A token cannot end the sessions of its user, whatever its scope (RV-1).
	if rec := e.bearer(str(read, "token"), "DELETE", "/api/v1/me/sessions/x", nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "session_required") {
		t.Fatalf("a read token revokes a session: %d %s", rec.Code, rec.Body)
	}
	// Never more than the user: an admin-scope token of a viewer is a viewer.
	vt := mk(viewer, map[string]any{"name": "v", "scopes": []string{"admin"}})
	if rec := e.bearer(str(vt, "token"), "POST", "/api/v1/projects", map[string]any{"name": "z"}); rec.Code != 403 {
		t.Fatalf("a viewer token creates a project: %d", rec.Code)
	}
	if rec := e.bearer(str(vt, "token"), "GET", "/api/v1/projects", nil); rec.Code != 200 {
		t.Fatalf("a viewer token reads: %d", rec.Code)
	}
	// Bad input.
	for _, body := range []map[string]any{{"name": "x", "scopes": []string{}}, {"name": "x", "scopes": []string{"root"}},
		{"name": "x", "scopes": []string{"read"}, "project_id": "prj_nope"},
		{"name": "x", "scopes": []string{"read"}, "expires_at": e.clk.Now().Add(-time.Hour).Format(time.RFC3339)}} {
		if rec := owner.do("POST", "/api/v1/tokens", body); rec.Code != 422 {
			t.Fatalf("bad token %v: %d", body, rec.Code)
		}
	}
	// Expiry.
	short := mk(owner, map[string]any{"name": "short", "scopes": []string{"read"}, "expires_at": e.clk.Now().Add(time.Hour).Format(time.RFC3339)})
	if rec := e.bearer(str(short, "token"), "GET", "/api/v1/projects", nil); rec.Code != 200 {
		t.Fatalf("token before expiry: %d", rec.Code)
	}
	e.clk.Advance(2 * time.Hour)
	if rec := e.bearer(str(short, "token"), "GET", "/api/v1/projects", nil); rec.Code != 401 {
		t.Fatalf("expired token: %d", rec.Code)
	}
	// Last use is stored. Revoke by the owner of the token, and of another user by an admin only.
	if e.count(`SELECT count(*) FROM api_tokens WHERE id = $1 AND last_used_at IS NOT NULL`, str(read, "id")) != 1 {
		t.Fatal("last_used_at is not stored")
	}
	if rec := viewer.do("DELETE", "/api/v1/tokens/"+str(read, "id"), nil); rec.Code != 404 {
		t.Fatalf("revoke the token of another user: %d", rec.Code)
	}
	if rec := viewer.do("DELETE", "/api/v1/org/tokens/"+str(read, "id"), nil); rec.Code != 403 {
		t.Fatalf("viewer revokes an org token: %d", rec.Code)
	}
	if rec := viewer.do("GET", "/api/v1/tokens?all=true", nil); rec.Code != 403 {
		t.Fatalf("viewer lists all tokens: %d", rec.Code)
	}
	if all := owner.do("GET", "/api/v1/tokens?all=true", nil); !strings.Contains(all.Body.String(), str(vt, "id")) {
		t.Fatalf("all=true does not list the token of another user: %s", all.Body)
	}
	owner.ok(204, "DELETE", "/api/v1/org/tokens/"+str(read, "id"), nil)
	if rec := e.bearer(str(read, "token"), "GET", "/api/v1/projects", nil); rec.Code != 401 {
		t.Fatalf("revoked token: %d", rec.Code)
	}
	owner.ok(204, "DELETE", "/api/v1/tokens/"+str(write, "id"), nil)
	if rec := e.bearer(str(write, "token"), "GET", "/api/v1/projects", nil); rec.Code != 401 {
		t.Fatalf("revoked own token: %d", rec.Code)
	}
	// The audit log names the token as the actor of its changes (REQ-AUDIT-001: token use of admin actions).
	e.bearer(str(admin, "token"), "POST", "/api/v1/teams", map[string]any{"name": "by-token"})
	if n := e.count(`SELECT count(*) FROM audit_events WHERE actor_type = 'token' AND actor_id = $1 AND action = 'team.create'`, str(admin, "id")); n != 1 {
		t.Fatalf("token actor in the audit log: %d", n)
	}
}

// secretOf reads the TOTP secret from an otpauth URI.
func secretOf(t *testing.T, uri string) []byte {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "otpauth" {
		t.Fatalf("otpauth uri %q", uri)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// REQ-AUTH-002: TOTP with 10 recovery codes. The code works once. A recovery code works once. The org can require 2FA.
func TestM5_TOTPRecoveryAndRequired2FA(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	login := func(code string) *httptest.ResponseRecorder {
		body := map[string]any{"email": "owner@example.com", "password": ownerPassword}
		if code != "" {
			body["totp_code"] = code
		}
		return e.newClient().do("POST", "/api/v1/auth/login", body)
	}

	enroll := owner.ok(200, "POST", "/api/v1/auth/totp/enroll", nil)
	secret := secretOf(t, str(enroll, "otpauth_uri"))
	// The secret is stored encrypted, TOTP is off until the first good code.
	var enc []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT totp_secret_enc FROM users`).Scan(&enc)
	if len(enc) == 0 || strings.Contains(string(enc), string(secret)) || e.count(`SELECT count(*) FROM users WHERE totp_enabled`) != 0 {
		t.Fatal("the TOTP secret is not stored encrypted, or TOTP is on too early")
	}
	if rec := login(""); rec.Code != 200 {
		t.Fatalf("login before the enrollment ends: %d", rec.Code)
	}
	if rec := owner.do("POST", "/api/v1/auth/totp/verify", map[string]any{"totp_code": "000000"}); rec.Code != 422 {
		t.Fatalf("wrong code: %d", rec.Code)
	}
	done := owner.ok(200, "POST", "/api/v1/auth/totp/verify", map[string]any{"totp_code": auth.TOTPCode(secret, e.clk.Now())})
	codes, _ := done["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", done)
	}
	var hashes []string
	_ = e.pool.QueryRow(context.Background(), `SELECT recovery_codes_hash FROM users`).Scan(&hashes)
	if len(hashes) != 10 || strings.Contains(strings.Join(hashes, ","), codes[0].(string)) {
		t.Fatalf("recovery codes are not stored as hashes: %v", hashes)
	}
	if rec := owner.do("POST", "/api/v1/auth/totp/enroll", nil); rec.Code != 409 {
		t.Fatalf("enroll twice: %d", rec.Code)
	}

	// Login needs the code. The password alone is not enough, and that is not a failed try.
	rec := login("")
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "totp_required") {
		t.Fatalf("login without a code: %d %s", rec.Code, rec.Body)
	}
	if rec := login("123456"); rec.Code != 401 {
		t.Fatalf("login with a wrong code: %d", rec.Code)
	}
	e.clk.Advance(31 * time.Second)
	code := auth.TOTPCode(secret, e.clk.Now())
	if rec := login(code); rec.Code != 200 {
		t.Fatalf("login with a good code: %d %s", rec.Code, rec.Body)
	}
	if rec := login(code); rec.Code != 401 {
		t.Fatalf("the same code twice: %d", rec.Code)
	}
	recovery := codes[0].(string)
	if rec := login(strings.ToUpper(recovery)); rec.Code != 200 {
		t.Fatalf("recovery code: %d %s", rec.Code, rec.Body)
	}
	if rec := login(recovery); rec.Code != 401 {
		t.Fatalf("recovery code twice: %d", rec.Code)
	}
	if e.count(`SELECT cardinality(recovery_codes_hash) FROM users`) != 9 {
		t.Fatal("a used recovery code stays")
	}

	// The org requires 2FA: a user without it can only set it up (403 two_factor_required).
	dev := e.userClient("usr_d", "dev@example.com", "developer")
	dev.ok(200, "GET", "/api/v1/projects", nil)
	owner.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"require_2fa": true}})
	if rec := dev.do("GET", "/api/v1/projects", nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "two_factor_required") {
		t.Fatalf("required 2fa: %d %s", rec.Code, rec.Body)
	}
	dev.ok(200, "GET", "/api/v1/me", nil)
	en := dev.ok(200, "POST", "/api/v1/auth/totp/enroll", nil)
	dev.ok(200, "POST", "/api/v1/auth/totp/verify", map[string]any{"totp_code": auth.TOTPCode(secretOf(t, str(en, "otpauth_uri")), e.clk.Now())})
	dev.ok(200, "GET", "/api/v1/projects", nil)

	// Turning it off needs the password.
	if rec := owner.do("POST", "/api/v1/auth/totp/disable", map[string]any{"password": "wrong wrong wrong"}); rec.Code != 422 {
		t.Fatalf("disable with a wrong password: %d", rec.Code)
	}
	owner.ok(204, "POST", "/api/v1/auth/totp/disable", map[string]any{"password": ownerPassword})
	if rec := login(""); rec.Code != 200 {
		t.Fatalf("login after disable: %d", rec.Code)
	}
}

// REQ-AUTH-007: a reset link (made by the admin command) sets a new password, ends all sessions, works once. The request endpoint always says 202.
func TestM5_PasswordReset(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	pub := e.newClient()
	for _, email := range []string{"owner@example.com", "nobody@example.com"} {
		if rec := pub.do("POST", "/api/v1/auth/password-reset", map[string]any{"email": email}); rec.Code != 202 || rec.Body.Len() != 0 {
			t.Fatalf("reset request for %s: %d %s", email, rec.Code, rec.Body)
		}
	}
	st := store.New(e.pool)
	u, err := st.UserByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := auth.NewOneTimeToken()
	if err := st.CreatePasswordReset(context.Background(), u.ID, auth.HashToken(token), e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := pub.do("POST", "/api/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "short"}); rec.Code != 422 {
		t.Fatalf("short password: %d", rec.Code)
	}
	if rec := pub.do("POST", "/api/v1/auth/password-reset/confirm", map[string]any{"token": "bad", "password": "a brand new password"}); rec.Code != 422 {
		t.Fatalf("bad token: %d", rec.Code)
	}
	if rec := pub.do("POST", "/api/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "a brand new password"}); rec.Code != 204 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := pub.do("POST", "/api/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "another new password"}); rec.Code != 422 {
		t.Fatalf("the link works twice: %d", rec.Code)
	}
	if rec := owner.do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("old session after a reset: %d", rec.Code)
	}
	e.signIn("owner@example.com", "a brand new password")
	// An expired link does not work.
	token2, _ := auth.NewOneTimeToken()
	_ = st.CreatePasswordReset(context.Background(), u.ID, auth.HashToken(token2), e.clk.Now())
	e.clk.Advance(2 * time.Hour)
	if rec := pub.do("POST", "/api/v1/auth/password-reset/confirm", map[string]any{"token": token2, "password": "a brand new password"}); rec.Code != 422 {
		t.Fatalf("expired link: %d", rec.Code)
	}
}

// AC-AUDIT-01: a variable change is an audit event with the actor and a diff where a secret shows ***.
// REQ-AUDIT-004, REQ-AUDIT-005: search with filters and a cursor, export as JSON lines.
func TestAC_AUDIT_01_VariableChangeSearchExport(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	dev := e.userClient("usr_d", "dev@example.com", "developer")
	projectID, _ := owner.project("shop")
	owner.ok(201, "POST", "/api/v1/projects/"+projectID+"/variables", map[string]any{"name": "GREETING", "value": "hello"})
	owner.ok(201, "POST", "/api/v1/projects/"+projectID+"/secrets", map[string]any{"name": "DB_PASSWORD", "value": "s3cr3t-value-123"})

	rec := owner.do("GET", "/api/v1/audit-events?action=variable.*", nil)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got.Items) != 1 {
		t.Fatalf("variable events: %d %s", rec.Code, rec.Body)
	}
	ev := got.Items[0]
	if str(ev, "action") != "variable.create" || str(ev, "actor_type") != "user" || str(ev, "actor_id") == "" ||
		str(ev, "project_id") != projectID || str(ev, "request_id") == "" || str(ev, "ip") == "" || str(ev, "hash") == "" {
		t.Fatalf("variable event: %v", ev)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("a variable value is in the diff: %v", ev["diff"])
	}
	all := owner.do("GET", "/api/v1/audit-events?limit=200", nil).Body.String()
	if strings.Contains(all, "s3cr3t-value-123") || !strings.Contains(all, `"secret.create"`) {
		t.Fatalf("a secret value is in the audit log, or the secret event is missing")
	}
	if !strings.Contains(all, `"***"`) {
		t.Fatalf("the secret diff does not show ***")
	}
	// Logins are events too (good and failed).
	e.newClient().do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": "wrong wrong wrong"})
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.login_failed' AND diff->>'reason' = 'bad_password'`) != 1 ||
		e.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.login'`) < 1 {
		t.Fatal("login events are missing")
	}

	// Filters and the cursor.
	page1 := owner.ok(200, "GET", "/api/v1/audit-events?limit=2", nil)
	items, _ := page1["items"].([]any)
	cursor := str(page1, "next_cursor")
	if len(items) != 2 || cursor == "" {
		t.Fatalf("page 1: %v", page1)
	}
	page2 := owner.ok(200, "GET", "/api/v1/audit-events?limit=2&cursor="+cursor, nil)
	first2 := page2["items"].([]any)[0].(map[string]any)
	if int(first2["seq"].(float64)) >= int(items[1].(map[string]any)["seq"].(float64)) {
		t.Fatalf("page 2 starts at %v after %v", first2["seq"], items[1])
	}
	if rec := owner.do("GET", "/api/v1/audit-events?cursor=abc", nil); rec.Code != 422 {
		t.Fatalf("bad cursor: %d", rec.Code)
	}
	for _, q := range []string{"actor_id=" + str(ev, "actor_id"), "target_type=variable", "project_id=" + projectID,
		// The "+" of a +02:00 time zone must be escaped in a query, else it is read as a space.
		"from=" + url.QueryEscape(e.clk.Now().Add(-time.Hour).Format(time.RFC3339)) + "&to=" + url.QueryEscape(e.clk.Now().Add(time.Hour).Format(time.RFC3339))} {
		if r := owner.ok(200, "GET", "/api/v1/audit-events?"+q, nil); len(r["items"].([]any)) == 0 {
			t.Fatalf("filter %s found nothing", q)
		}
	}
	if r := owner.ok(200, "GET", "/api/v1/audit-events?action=nothing.here", nil); len(r["items"].([]any)) != 0 {
		t.Fatalf("filter found rows: %v", r)
	}
	// Only admins read, export and verify.
	for _, p := range []string{"/api/v1/audit-events", "/api/v1/audit-events/export", "/api/v1/audit/verify"} {
		if rec := dev.do("GET", p, nil); rec.Code != 403 {
			t.Fatalf("developer %s: %d", p, rec.Code)
		}
	}
	// Export: JSON lines in time order, and the export itself is audited.
	exp := owner.do("GET", "/api/v1/audit-events/export", nil)
	if exp.Code != 200 || !strings.HasPrefix(exp.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("export: %d %s", exp.Code, exp.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(exp.Body.String()), "\n")
	last := -1.0
	for _, l := range lines {
		var o map[string]any
		if err := json.Unmarshal([]byte(l), &o); err != nil || o["seq"].(float64) <= last {
			t.Fatalf("export line %q", l)
		}
		last = o["seq"].(float64)
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'audit.export'`) != 1 {
		t.Fatal("the export is not audited")
	}
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true {
		t.Fatalf("verify: %v", v)
	}
}

// AC-AUDIT-02, T-E2E-14: the log is append only. A row that was changed in the database is found by verify,
// with the first broken seq. A removed row is found too.
func TestAC_AUDIT_02_T_E2E_14_ChainBreakIsFound(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	for i := 0; i < 5; i++ {
		owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "team" + string(rune('a'+i))})
	}
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true || v["checked_rows"].(float64) < 5 {
		t.Fatalf("verify before: %v", v)
	}
	ctx := context.Background()
	// The database refuses a change from the server role.
	if _, err := e.pool.Exec(ctx, `UPDATE audit_events SET action = 'x'`); err == nil {
		t.Fatal("an audit row was changed without the archive switch")
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Fatal("audit rows were deleted without the archive switch")
	}
	var seq int64
	if err := e.pool.QueryRow(ctx, `SELECT seq FROM audit_events WHERE action = 'team.create' ORDER BY seq OFFSET 2 LIMIT 1`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	tamper := func(sql string, args ...any) {
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL polipan.audit_archive = 'on'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tamper(`UPDATE audit_events SET actor_id = 'usr_someone_else' WHERE seq = $1`, seq)
	v := owner.ok(200, "GET", "/api/v1/audit/verify", nil)
	if v["ok"] != false || int64(v["first_broken_seq"].(float64)) != seq {
		t.Fatalf("verify after a change: %v (want seq %d)", v, seq)
	}
	// Events written after the break do not hide it.
	owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "after"})
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); int64(v["first_broken_seq"].(float64)) != seq {
		t.Fatalf("verify after a new event: %v", v)
	}
	// A deleted row in the middle breaks the chain at the next row.
	tamper(`UPDATE audit_events SET actor_id = NULL WHERE seq = $1`, seq) // not the original value: still broken
	tamper(`DELETE FROM audit_events WHERE seq = $1`, seq-1)
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != false || int64(v["first_broken_seq"].(float64)) > seq {
		t.Fatalf("verify after a delete: %v", v)
	}
}

// REQ-AUDIT-006: rows older than the retention move to a signed archive segment. The chain stays valid.
func TestM5_AuditRetentionArchive(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	for i := 0; i < 4; i++ {
		owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "t" + string(rune('a'+i))})
	}
	ctx := context.Background()
	var orgID string
	_ = e.pool.QueryRow(ctx, `SELECT id FROM orgs`).Scan(&orgID)
	before := e.count(`SELECT count(*) FROM audit_events`)
	key := audit.ArchiveKey(testKEK)
	// Nothing is older than the cutoff: nothing happens.
	if res, err := audit.Archive(ctx, e.pool, orgID, time.Now().Add(-time.Hour), key); err != nil || res.Rows != 0 {
		t.Fatalf("archive of nothing: %v %v", res, err)
	}
	time.Sleep(20 * time.Millisecond)
	cut := time.Now()
	time.Sleep(20 * time.Millisecond)
	owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "new"})
	res, err := audit.Archive(ctx, e.pool, orgID, cut, key)
	if err != nil || res.Rows != before {
		t.Fatalf("archive: %+v %v (want %d rows)", res, err, before)
	}
	left := e.count(`SELECT count(*) FROM audit_events`)
	if left < 1 || e.count(`SELECT count(*) FROM audit_archives WHERE row_count = $1 AND length(signature) = 32`, before) != 1 {
		t.Fatalf("after the archive: %d rows left", left)
	}
	var data, sig []byte
	_ = e.pool.QueryRow(ctx, `SELECT data, signature FROM audit_archives`).Scan(&data, &sig)
	if string(audit.Sign(key, data)) != string(sig) || string(audit.Sign(audit.ArchiveKey(make([]byte, 32)), data)) == string(sig) {
		t.Fatal("the archive signature is wrong")
	}
	// The chain goes on over the checkpoint, and new events chain to the old ones.
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true {
		t.Fatalf("verify after the archive: %v", v)
	}
	owner.ok(201, "POST", "/api/v1/teams", map[string]any{"name": "newer"})
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true {
		t.Fatalf("verify after a new event: %v", v)
	}
	// The daily job does the same and refuses a broken chain.
	audit.RunOnce(ctx, e.pool, discardLog(), key, time.Now().AddDate(1, 0, 0))
	if e.count(`SELECT count(*) FROM audit_archives`) != 2 {
		t.Fatalf("the job did not archive")
	}
	if v := owner.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true {
		t.Fatalf("verify after the job: %v", v)
	}
}

// REQ-AUDIT-001: a test enumerates the operations of the OpenAPI file. Every state-changing operation
// writes an audit event, also when it is refused for its input. Then the chain verifies.
func TestM5_EveryStateChangingOperationWritesAnAuditEvent(t *testing.T) {
	e := newTestEnv(t, "")
	owner := e.owner()
	pub := e.newClient()
	ops, err := api.ParseOperations(specapi.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var logout *api.Operation
	checked := 0
	for i, op := range ops {
		if op.Method == "GET" || op.Method == "HEAD" || op.Method == "OPTIONS" {
			continue
		}
		if op.OperationID == "logout" {
			logout = &ops[i]
			continue
		}
		c := owner
		if op.Permission == api.PermPublic {
			c = pub
		}
		before := e.count(`SELECT count(*) FROM audit_events`)
		status := c.call(op.Method, "/api/v1"+fillPath(op.Path, nil))
		if status == 429 {
			t.Fatalf("%s %s: rate limited", op.Method, op.Path)
		}
		if after := e.count(`SELECT count(*) FROM audit_events`); after <= before {
			t.Errorf("%s %s (%s): status %d and no audit event", op.Method, op.Path, op.OperationID, status)
		}
		checked++
	}
	if logout == nil || checked < 80 {
		t.Fatalf("checked only %d operations", checked)
	}
	before := e.count(`SELECT count(*) FROM audit_events`)
	owner.ok(204, "POST", "/api/v1/auth/logout", nil)
	if e.count(`SELECT count(*) FROM audit_events`) <= before {
		t.Error("logout writes no audit event")
	}
	// The fallback event names the operation and the status.
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'api.createProject' AND diff->>'status' = '422'`) != 1 {
		t.Error("the generic event of createProject is wrong")
	}
	admin := e.signIn("owner@example.com", ownerPassword)
	if v := admin.ok(200, "GET", "/api/v1/audit/verify", nil); v["ok"] != true || v["checked_rows"].(float64) < float64(checked) {
		t.Fatalf("the chain after %d operations: %v", checked, v)
	}
}

func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return string(raw), err
}

func discardLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }
