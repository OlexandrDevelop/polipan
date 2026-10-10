// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
)

// fakeOrch records the calls of the API. Deploy writes a queued deployment row, like the real engine.
type fakeOrch struct {
	mu          sync.Mutex
	pool        *pgxpool.Pool
	deploys     []string
	running     []bool
	domains     []string
	removed     []string
	joinCalls   int
	logsTail    int
	deployError error
}

func (f *fakeOrch) Deploy(ctx context.Context, serviceID, actorID, trigger string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deployError != nil {
		return "", f.deployError
	}
	f.deploys = append(f.deploys, serviceID)
	id := store.NewID("dep")
	_, err := f.pool.Exec(ctx, `INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state, actor_id)
		SELECT $1, s.id, s.environment_id, $3, 'recreate', 'queued', $4 FROM services s WHERE s.id = $2`, id, serviceID, trigger, actorID)
	return id, err
}

func (f *fakeOrch) SetRunning(_ context.Context, _ string, running bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = append(f.running, running)
	return nil
}

func (f *fakeOrch) DomainsChanged(_ context.Context, serviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains = append(f.domains, serviceID)
	return nil
}

func (f *fakeOrch) ServiceRemoved(_ context.Context, serviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, serviceID)
	return nil
}

func (f *fakeOrch) Logs(_ context.Context, _ string, tail int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logsTail = tail
	return "line one\nline two\n", nil
}

func (f *fakeOrch) CreateJoinToken(_ context.Context, _, _ string, _ map[string]string, _ string) (orchestrator.JoinToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joinCalls++
	return orchestrator.JoinToken{Token: "pj_secret", ExpiresAt: time.Unix(1_700_003_600, 0).UTC(), CAFingerprint: "abcd1234"}, nil
}

type testEnv struct {
	t      *testing.T
	h      http.Handler
	pool   *pgxpool.Pool
	orch   *fakeOrch
	clk    *clock.Fake
	nextIP int
}

func newTestEnv(t *testing.T, externalURL string) *testEnv {
	t.Helper()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	orch := &fakeOrch{pool: pool}
	clk := clock.NewFake(time.Now())
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: pool, Clock: clk, RateLimit: 100000,
		Pool: pool, Orchestrator: orch, ExternalURL: externalURL, AgentListen: ":9443", SetupToken: "setup-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, h: h, pool: pool, orch: orch, clk: clk}
}

// client is one browser: it keeps the cookie and the CSRF token.
type client struct {
	e      *testEnv
	cookie *http.Cookie
	csrf   string
	addr   string
}

func (e *testEnv) newClient() *client {
	e.nextIP++
	return &client{e: e, addr: fmt.Sprintf("198.51.100.%d:5000", e.nextIP)}
}

func (c *client) do(method, path string, body any) *httptest.ResponseRecorder {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = c.addr
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	c.e.h.ServeHTTP(rec, req)
	return rec
}

// ok runs a request and fails the test when the status is not want.
func (c *client) ok(want int, method, path string, body any) map[string]any {
	c.e.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code != want {
		c.e.t.Fatalf("%s %s: status %d (want %d), body %s", method, path, rec.Code, want, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func (c *client) takeSession(rec *httptest.ResponseRecorder) {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "pp_session" && ck.MaxAge >= 0 && ck.Value != "" {
			c.cookie = ck
			c.csrf = rec.Header().Get("X-CSRF-Token")
		}
	}
}

const ownerPassword = "correct horse battery"

// owner makes the first user and returns a logged-in client.
func (e *testEnv) owner() *client {
	c := e.newClient()
	rec := c.do("POST", "/api/v1/setup/first-user", map[string]any{
		"setup_token": "setup-secret", "email": "Owner@Example.com", "name": "Olga", "password": ownerPassword})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	c.takeSession(rec)
	return c
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// AC-AUTH-03: the first user is created once, then the endpoint is closed. Login, cookie flags, CSRF and lockout.
func TestAC_AUTH_03_FirstUserLoginCSRF(t *testing.T) {
	e := newTestEnv(t, "https://panel.example.com")
	c := e.newClient()

	// Wrong token, short password: nothing is created.
	if rec := c.do("POST", "/api/v1/setup/first-user", map[string]any{"setup_token": "nope", "email": "a@b.co", "name": "A", "password": ownerPassword}); rec.Code != 403 {
		t.Fatalf("wrong setup token: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", "/api/v1/setup/first-user", map[string]any{"setup_token": "setup-secret", "email": "a@b.co", "name": "A", "password": "short"}); rec.Code != 422 {
		t.Fatalf("short password: %d %s", rec.Code, rec.Body)
	}

	rec := c.do("POST", "/api/v1/setup/first-user", map[string]any{
		"setup_token": "setup-secret", "email": "Owner@Example.com", "name": "Olga", "password": ownerPassword})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("setup answer has a password field: %s", rec.Body)
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS header with an https external_url")
	}

	// The second call is 404 and creates nothing.
	rec = c.do("POST", "/api/v1/setup/first-user", map[string]any{
		"setup_token": "setup-secret", "email": "other@example.com", "name": "B", "password": ownerPassword})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("second setup: %d %s", rec.Code, rec.Body)
	}
	var users int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&users)
	if users != 1 {
		t.Fatalf("%d users", users)
	}

	// The hash in the database is argon2id with the glossary parameters.
	var hash string
	_ = e.pool.QueryRow(context.Background(), `SELECT password_hash FROM users`).Scan(&hash)
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("hash %q", hash)
	}

	// Wrong password.
	if rec := c.do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": "wrong wrong wrong"}); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	// Good login: cookie flags and CSRF token.
	rec = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "OWNER@example.com", "password": ownerPassword})
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c.takeSession(rec)
	ck := c.cookie
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || len(ck.Value) < 40 {
		t.Fatalf("cookie flags: %+v", ck)
	}
	var stored string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM sessions WHERE id = $1`, auth.SessionID(ck.Value)).Scan(&stored)
	if stored == "" || stored == ck.Value {
		t.Fatalf("the session id must be the hash of the cookie value")
	}
	if c.csrf == "" {
		t.Fatal("no X-CSRF-Token on login")
	}

	me := c.ok(200, "GET", "/api/v1/me", nil)
	if str(me, "org_role") != "owner" || str(me["user"].(map[string]any), "email") != "owner@example.com" {
		t.Fatalf("me: %v", me)
	}
	org := c.ok(200, "GET", "/api/v1/org", nil)
	if str(org, "slug") == "" {
		t.Fatalf("org: %v", org)
	}

	// No cookie: 401. State-changing request without CSRF: 403. Wrong CSRF: 403.
	if rec := (&client{e: e, addr: c.addr}).do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	good := c.csrf
	c.csrf = ""
	if rec := c.do("PATCH", "/api/v1/me", map[string]any{"name": "Olga 2"}); rec.Code != 403 || !strings.Contains(rec.Body.String(), "csrf") {
		t.Fatalf("no csrf: %d %s", rec.Code, rec.Body)
	}
	c.csrf = "wrong"
	if rec := c.do("PATCH", "/api/v1/me", map[string]any{"name": "Olga 2"}); rec.Code != 403 {
		t.Fatalf("wrong csrf: %d", rec.Code)
	}
	c.csrf = good
	if u := c.ok(200, "PATCH", "/api/v1/me", map[string]any{"name": "Olga 2"}); str(u, "name") != "Olga 2" {
		t.Fatalf("patch me: %v", u)
	}
	// GET /me gives the same CSRF token again.
	if rec := c.do("GET", "/api/v1/me", nil); rec.Header().Get("X-CSRF-Token") != good {
		t.Fatalf("csrf from /me: %q", rec.Header().Get("X-CSRF-Token"))
	}

	// A viewer cannot create a project (role matrix).
	viewerHash, _ := auth.HashPassword(ownerPassword)
	var orgID string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM orgs`).Scan(&orgID)
	e.addUser("usr_v", "viewer@example.com", "viewer", viewerHash, orgID)
	v := e.newClient()
	rec = v.do("POST", "/api/v1/auth/login", map[string]any{"email": "viewer@example.com", "password": ownerPassword})
	v.takeSession(rec)
	if rec := v.do("POST", "/api/v1/projects", map[string]any{"name": "x"}); rec.Code != 403 {
		t.Fatalf("viewer create project: %d %s", rec.Code, rec.Body)
	}
	v.ok(200, "GET", "/api/v1/projects", nil)

	// Logout ends the session.
	if rec := c.do("POST", "/api/v1/auth/logout", nil); rec.Code != 204 {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := c.do("GET", "/api/v1/me", nil); rec.Code != 401 {
		t.Fatalf("after logout: %d", rec.Code)
	}

	// 5 failed tries lock the account and the IP for 15 minutes, even with the right password.
	// One failed try was made above, so 4 more are enough.
	for i := 0; i < 4; i++ {
		if rec := c.do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": "wrong wrong wrong"}); rec.Code != 401 {
			t.Fatalf("try %d: %d", i, rec.Code)
		}
	}
	rec = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": ownerPassword})
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Fatalf("locked: %d %s", rec.Code, rec.Body)
	}
	e.clk.Advance(16 * time.Minute)
	if rec := c.do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": ownerPassword}); rec.Code != 200 {
		t.Fatalf("after the lock: %d %s", rec.Code, rec.Body)
	}
}

// project makes a project and returns the ids of the environments by slug.
func (c *client) project(name string) (projectID string, envs map[string]string) {
	c.e.t.Helper()
	p := c.ok(201, "POST", "/api/v1/projects", map[string]any{"name": name})
	projectID = str(p, "id")
	envs = map[string]string{}
	rec := c.do("GET", "/api/v1/projects/"+projectID+"/environments", nil)
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	for _, e := range list {
		envs[str(e, "slug")] = str(e, "id")
	}
	return projectID, envs
}

func (c *client) service(envID, name string) string {
	c.e.t.Helper()
	s := c.ok(201, "POST", "/api/v1/environments/"+envID+"/services", map[string]any{
		"name": name, "type": "app", "source": map[string]any{"image": "nginx:1.27"}, "config": map[string]any{"port": 80}})
	return str(s, "id")
}

func (e *testEnv) count(query string, args ...any) int {
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// AC-DOM-02 and AC-DOM-03: a used host gives 409 domain_in_use. A domain change calls DomainsChanged,
// makes no release and no deployment.
func TestAC_DOM_02_03_Domains(t *testing.T) {
	e := newTestEnv(t, "http://panel.example.com")
	c := e.owner()
	prj, envs := c.project("Shop")
	if len(envs) != 2 || envs["production"] == "" || envs["staging"] == "" {
		t.Fatalf("default environments: %v", envs)
	}
	a, b := c.service(envs["staging"], "web"), c.service(envs["staging"], "api")

	// Service config: defaults, validation, unique name.
	svc := c.ok(200, "GET", "/api/v1/services/"+a, nil)
	cfg := svc["config"].(map[string]any)
	if cfg["image"] != "nginx:1.27" || cfg["strategy"] != "recreate" || cfg["restart_policy"] != "always" || cfg["health"].(map[string]any)["type"] != "tcp" {
		t.Fatalf("config: %v", cfg)
	}
	if str(svc, "status") != "no_release" || str(svc, "source_type") != "image" {
		t.Fatalf("service: %v", svc)
	}
	body := map[string]any{"name": "web", "type": "app", "config": map[string]any{"image": "nginx"}}
	if rec := c.do("POST", "/api/v1/environments/"+envs["staging"]+"/services", body); rec.Code != 409 {
		t.Fatalf("same name: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []map[string]any{
		{"name": "x1", "type": "app"},
		{"name": "x1", "type": "app", "config": map[string]any{"image": "bad image"}},
		{"name": "x1", "type": "app", "config": map[string]any{"image": "nginx", "env": map[string]any{}}},
		{"name": "x1", "type": "database", "config": map[string]any{"image": "nginx"}},
		{"name": "X_1", "type": "app", "config": map[string]any{"image": "nginx"}},
	} {
		if rec := c.do("POST", "/api/v1/environments/"+envs["staging"]+"/services", bad); rec.Code != 422 {
			t.Fatalf("bad service %v: %d %s", bad, rec.Code, rec.Body)
		}
	}

	dom := c.ok(201, "POST", "/api/v1/services/"+a+"/domains", map[string]any{"host": "App.Example.COM"})
	if str(dom, "host") != "app.example.com" || str(dom, "tls_status") != "pending" || dom["container_port"] != float64(80) || dom["https"] != true {
		t.Fatalf("domain: %v", dom)
	}
	if _, ok := dom["tls_error"]; ok {
		t.Fatalf("tls_error should be empty: %v", dom)
	}
	if len(e.orch.domains) != 1 || e.orch.domains[0] != a {
		t.Fatalf("DomainsChanged calls: %v", e.orch.domains)
	}
	// AC-DOM-03: no release, no deployment, no Deploy call, no stop or start.
	if n := e.count(`SELECT (SELECT count(*) FROM releases) + (SELECT count(*) FROM deployments)`); n != 0 || len(e.orch.deploys) != 0 || len(e.orch.running) != 0 {
		t.Fatalf("a domain change did work: rows %d, deploys %v, running %v", n, e.orch.deploys, e.orch.running)
	}

	// AC-DOM-02: the same host in another service (also in other case) is 409 domain_in_use.
	for _, host := range []string{"app.example.com", "APP.example.com"} {
		rec := c.do("POST", "/api/v1/services/"+b+"/domains", map[string]any{"host": host})
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"domain_in_use"`) {
			t.Fatalf("second add of %s: %d %s", host, rec.Code, rec.Body)
		}
	}
	// SEC-1: the host of the panel (and its www or apex partner) is reserved, with any path prefix.
	panel := "panel.example.com"
	for _, host := range []string{panel, strings.ToUpper(panel), "www." + panel} {
		rec := c.do("POST", "/api/v1/services/"+b+"/domains", map[string]any{"host": host, "path_prefix": "/api"})
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"domain_in_use"`) {
			t.Fatalf("panel host %s: %d %s", host, rec.Code, rec.Body)
		}
	}
	if len(e.orch.domains) != 1 {
		t.Fatalf("a refused domain called DomainsChanged: %v", e.orch.domains)
	}
	// The same host with another path is fine.
	other := c.ok(201, "POST", "/api/v1/services/"+b+"/domains", map[string]any{"host": "app.example.com", "path_prefix": "/api", "container_port": 8080})
	// Syntax checks.
	for _, host := range []string{"*.example.com", "localhost", "exa mple.com", "-a.example.com", "10.0.0.1", ""} {
		if rec := c.do("POST", "/api/v1/services/"+b+"/domains", map[string]any{"host": host}); rec.Code != 422 {
			t.Fatalf("host %q: %d %s", host, rec.Code, rec.Body)
		}
	}
	// A change of the path to a used pair is 409 too. A good change keeps the domain id and resets TLS.
	if rec := c.do("PATCH", "/api/v1/domains/"+str(other, "id"), map[string]any{"path_prefix": "/"}); rec.Code != 409 {
		t.Fatalf("patch to used pair: %d %s", rec.Code, rec.Body)
	}
	up := c.ok(200, "PATCH", "/api/v1/domains/"+str(other, "id"), map[string]any{"host": "api.example.com", "https": false})
	if str(up, "id") != str(other, "id") || str(up, "host") != "api.example.com" || up["https"] != false {
		t.Fatalf("patched: %v", up)
	}
	list := c.do("GET", "/api/v1/services/"+b+"/domains", nil)
	if !strings.Contains(list.Body.String(), "api.example.com") {
		t.Fatalf("list: %s", list.Body)
	}
	c.ok(204, "DELETE", "/api/v1/domains/"+str(other, "id"), nil)
	if len(e.orch.domains) != 4 {
		t.Fatalf("DomainsChanged calls: %v", e.orch.domains)
	}
	// The TLS status written by the control side is shown (AC-DOM-05).
	if _, err := e.pool.Exec(context.Background(), `UPDATE domains SET tls_status = 'failed', tls_error = 'DNS does not point to an ingress node' WHERE id = $1`, str(dom, "id")); err != nil {
		t.Fatal(err)
	}
	got := c.do("GET", "/api/v1/services/"+a+"/domains", nil)
	if !strings.Contains(got.Body.String(), `"tls_status":"failed"`) || !strings.Contains(got.Body.String(), "DNS does not point") {
		t.Fatalf("tls error: %s", got.Body)
	}
	if n := e.count(`SELECT (SELECT count(*) FROM releases) + (SELECT count(*) FROM deployments)`); n != 0 {
		t.Fatalf("releases or deployments were made: %d", n)
	}

	// Deleting a service frees its domains.
	if rec := c.do("DELETE", "/api/v1/services/"+a+"?confirm=nope", nil); rec.Code != 422 {
		t.Fatalf("delete without the right confirm: %d", rec.Code)
	}
	c.ok(204, "DELETE", "/api/v1/services/"+a+"?confirm=web", nil)
	c.ok(201, "POST", "/api/v1/services/"+b+"/domains", map[string]any{"host": "app.example.com"})
	_ = prj
}

// AC-APP-05 (API part): stop and start call SetRunning, the service stays and can be read.
// Also deploy, deployments, logs and delete.
func TestAC_APP_05_StopStartDeploy(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	prj, envs := c.project("Blog")
	svc := c.service(envs["staging"], "web")

	c.ok(202, "POST", "/api/v1/services/"+svc+"/stop", nil)
	c.ok(202, "POST", "/api/v1/services/"+svc+"/start", nil)
	if fmt.Sprint(e.orch.running) != "[false true]" {
		t.Fatalf("SetRunning calls: %v", e.orch.running)
	}
	if got := c.ok(200, "GET", "/api/v1/services/"+svc, nil); str(got, "id") != svc {
		t.Fatalf("service after stop and start: %v", got)
	}
	if e.count(`SELECT count(*) FROM services WHERE id = $1 AND deleted_at IS NULL`, svc) != 1 || len(e.orch.deploys) != 0 {
		t.Fatal("stop and start changed the service rows or deployed")
	}
	c.ok(404, "POST", "/api/v1/services/svc_missing/stop", nil)

	// Deploy: 202 with the deployment, readable by id and in the list.
	dep := c.ok(202, "POST", "/api/v1/services/"+svc+"/deploy", nil)
	if str(dep, "state") != "queued" || str(dep, "trigger") != "manual" || str(dep, "service_id") != svc {
		t.Fatalf("deployment: %v", dep)
	}
	if len(e.orch.deploys) != 1 {
		t.Fatalf("Deploy calls: %v", e.orch.deploys)
	}
	got := c.ok(200, "GET", "/api/v1/deployments/"+str(dep, "id"), nil)
	if str(got, "id") != str(dep, "id") {
		t.Fatalf("get deployment: %v", got)
	}
	list := c.ok(200, "GET", "/api/v1/services/"+svc+"/deployments?limit=1", nil)
	if items := list["items"].([]any); len(items) != 1 || list["next_cursor"] != nil {
		t.Fatalf("deployment list: %v", list)
	}
	e.orch.deployError = orchestrator.ErrNoNode
	if rec := c.do("POST", "/api/v1/services/"+svc+"/deploy", nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "node_unavailable") {
		t.Fatalf("no node: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", "/api/v1/services/"+svc+"/deploy", map[string]any{"dry_run": true, "strategy": "blue_green"}); rec.Code != 422 {
		t.Fatalf("blue green: %d", rec.Code)
	}
	if plan := c.ok(200, "POST", "/api/v1/services/"+svc+"/deploy", map[string]any{"dry_run": true}); str(plan, "strategy") != "recreate" {
		t.Fatalf("plan: %v", plan)
	}

	rec := c.do("GET", "/api/v1/services/"+svc+"/logs?limit=50", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "line two") || e.orch.logsTail != 50 {
		t.Fatalf("logs: %d %s tail %d", rec.Code, rec.Body, e.orch.logsTail)
	}

	// Restart is not part of M1.
	if rec := c.do("POST", "/api/v1/services/"+svc+"/restart", nil); rec.Code != 501 {
		t.Fatalf("restart: %d", rec.Code)
	}

	// A developer cannot change a protected environment (production) but can use staging.
	devHash, _ := auth.HashPassword(ownerPassword)
	var orgID string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM orgs`).Scan(&orgID)
	e.addUser("usr_d", "dev@example.com", "developer", devHash, orgID)
	d := e.newClient()
	d.takeSession(d.do("POST", "/api/v1/auth/login", map[string]any{"email": "dev@example.com", "password": ownerPassword}))
	d.ok(201, "POST", "/api/v1/environments/"+envs["staging"]+"/services", map[string]any{"name": "dev-svc", "type": "app", "config": map[string]any{"image": "nginx"}})
	if rec := d.do("POST", "/api/v1/environments/"+envs["production"]+"/services", map[string]any{"name": "dev-svc", "type": "app", "config": map[string]any{"image": "nginx"}}); rec.Code != 403 {
		t.Fatalf("developer in protected env: %d %s", rec.Code, rec.Body)
	}

	// Delete: the orchestrator is told, the service is gone, the name can be used again.
	c.ok(204, "DELETE", "/api/v1/services/"+svc+"?confirm=web", nil)
	if len(e.orch.removed) != 1 || e.orch.removed[0] != svc {
		t.Fatalf("ServiceRemoved: %v", e.orch.removed)
	}
	c.ok(404, "GET", "/api/v1/services/"+svc, nil)
	c.service(envs["staging"], "web")

	// Projects: update, conflict, delete with confirm.
	c.ok(200, "PATCH", "/api/v1/projects/"+prj, map[string]any{"name": "Blog 2", "settings": map[string]any{"retain_releases": 5}})
	if rec := c.do("POST", "/api/v1/projects", map[string]any{"name": "Blog"}); rec.Code != 409 {
		t.Fatalf("same slug: %d", rec.Code)
	}
	if rec := c.do("DELETE", "/api/v1/projects/"+prj+"?confirm=wrong", nil); rec.Code != 422 {
		t.Fatalf("project confirm: %d", rec.Code)
	}
	c.ok(204, "DELETE", "/api/v1/projects/"+prj+"?confirm=blog", nil)
	if len(e.orch.removed) < 3 {
		t.Fatalf("services of the deleted project were not removed: %v", e.orch.removed)
	}
	c.ok(404, "GET", "/api/v1/projects/"+prj, nil)
	c.ok(201, "POST", "/api/v1/projects", map[string]any{"name": "Blog"})
}

// Nodes: read, and the join token with the install command (the token is shown once).
func TestM1_NodesAndJoinToken(t *testing.T) {
	e := newTestEnv(t, "https://panel.example.com")
	c := e.owner()
	var orgID string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM orgs`).Scan(&orgID)
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO nodes (id, org_id, name, state, labels, public_ip) VALUES ('nod_1', $1, 'main', 'ready', '{"ingress":"true"}', '203.0.113.7')`, orgID); err != nil {
		t.Fatal(err)
	}
	rec := c.do("GET", "/api/v1/nodes", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"nod_1"`) || !strings.Contains(rec.Body.String(), `"203.0.113.7"`) {
		t.Fatalf("nodes: %d %s", rec.Code, rec.Body)
	}
	c.ok(200, "GET", "/api/v1/nodes/nod_1", nil)
	c.ok(404, "GET", "/api/v1/nodes/nod_none", nil)

	// The DNS records of a domain list the ingress IP.
	_, envs := c.project("App")
	svc := c.service(envs["staging"], "web")
	dom := c.do("POST", "/api/v1/services/"+svc+"/domains", map[string]any{"host": "a.example.com"})
	if !strings.Contains(dom.Body.String(), `"value":"203.0.113.7"`) {
		t.Fatalf("dns records: %s", dom.Body)
	}

	if rec := c.do("POST", "/api/v1/nodes", map[string]any{"name": "main"}); rec.Code != 409 {
		t.Fatalf("node name in use: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/nodes", map[string]any{"name": "Bad Name"}); rec.Code != 422 {
		t.Fatalf("bad node name: %d", rec.Code)
	}
	out := c.ok(201, "POST", "/api/v1/nodes", map[string]any{"name": "second", "labels": map[string]any{"region": "eu"}})
	want := "curl -fsSL https://panel.example.com/install-agent.sh | sh -s -- --server panel.example.com:9443 --token pj_secret --ca-fingerprint abcd1234"
	if str(out, "token") != "pj_secret" || str(out, "install_command") != want {
		t.Fatalf("join token answer: %v", out)
	}
}

// R7: the rate limit is per client IP. One IP is limited, another IP is not. X-Forwarded-For is not trusted.
func TestR7_RateLimitPerIP(t *testing.T) {
	r := newRouter(t, fakeDB{}, nil, 2)
	get := func(addr, xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
		req.RemoteAddr = addr
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	if get("203.0.113.1:1000", "") != 200 || get("203.0.113.1:1001", "") != 200 {
		t.Fatal("the first two requests must pass")
	}
	if code := get("203.0.113.1:1002", "9.9.9.9"); code != http.StatusTooManyRequests {
		t.Fatalf("third request of one IP (new port, forged XFF): %d", code)
	}
	if code := get("203.0.113.2:1000", ""); code != 200 {
		t.Fatalf("another IP: %d", code)
	}
}

// R9: security headers on every answer and a body size limit with 413.
func TestR9_SecurityHeadersAndBodyLimit(t *testing.T) {
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = io.WriteString(w, "// theme init")
	})
	build := func(external string) http.Handler {
		h, err := api.NewRouter(api.Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: fakeDB{}, Clock: clock.Real{},
			RateLimit: 1000, Web: web, ExternalURL: external, MaxBodyBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := build("https://panel.example.com")
	check := func(name string, rec *httptest.ResponseRecorder, hsts bool) {
		t.Helper()
		hd := rec.Header()
		csp := hd.Get("Content-Security-Policy")
		if !strings.HasPrefix(csp, "default-src 'self'; script-src 'self'; style-src 'self'") || strings.Contains(csp, "unsafe-") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q", name, csp)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("X-Frame-Options") != "DENY" || hd.Get("Referrer-Policy") == "" {
			t.Errorf("%s: headers %v", name, hd)
		}
		if (hd.Get("Strict-Transport-Security") != "") != hsts {
			t.Errorf("%s: HSTS %q, want %v", name, hd.Get("Strict-Transport-Security"), hsts)
		}
	}
	for _, p := range []string{"/healthz", "/api/v1/nothing", "/theme-init.js", "/api/v1/me"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		check(p, rec, true)
		if p == "/theme-init.js" && (rec.Body.String() != "// theme init" || rec.Header().Get("Content-Type") != "text/javascript") {
			t.Errorf("web file changed: %q %q", rec.Body, rec.Header().Get("Content-Type"))
		}
	}
	rec := httptest.NewRecorder()
	build("http://localhost").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	check("http", rec, false)

	// 413 with a Content-Length over the limit, and with a body of unknown length.
	big := `{"email":"` + strings.Repeat("a", 4096) + `","password":"x"}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(big)))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "body_too_large") || !strings.Contains(rec.Body.String(), `"request_id"`) {
		t.Fatalf("content-length: %d %s", rec.Code, rec.Body)
	}
	check("413", rec, true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "body_too_large") {
		t.Fatalf("chunked: %d %s", rec.Code, rec.Body)
	}
}

// R10: the server has timeouts, and a streaming handler that clears the write deadline keeps
// streaming for longer than the write timeout. A handler that does not clear it is cut.
func TestR10_StreamsOutliveWriteTimeout(t *testing.T) {
	srv := api.NewHTTPServer("127.0.0.1:0", nil)
	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 30*time.Second || srv.WriteTimeout != 60*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("timeouts: %v %v %v %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}

	// The same stream goes through the real router, so the wrappers must not hide the writer.
	stream := func(clear bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if clear {
				if err := api.ClearWriteDeadline(w); err != nil {
					t.Errorf("ClearWriteDeadline: %v", err)
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 8; i++ {
				if _, err := fmt.Fprintf(w, "data: %d\n\n", i); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
	run := func(clear bool) int {
		router, err := api.NewRouter(api.Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: fakeDB{}, Clock: clock.Real{}, RateLimit: 1000, Web: stream(clear)})
		if err != nil {
			t.Fatal(err)
		}
		srv := api.NewHTTPServer("", router)
		srv.WriteTimeout = 300 * time.Millisecond // the stream below takes about 800 ms
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()
		resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return strings.Count(string(raw), "data: ")
	}
	if n := run(true); n != 8 {
		t.Fatalf("with ClearWriteDeadline the client got %d of 8 events", n)
	}
	if n := run(false); n >= 8 {
		t.Fatalf("without ClearWriteDeadline the stream was not cut (%d events), so the test proves nothing", n)
	}
}

func (e *testEnv) addUser(id, email, role, hash, orgID string) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', $3)`, id, email, hash); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, $4)`, "mem_"+id, orgID, id, role); err != nil {
		e.t.Fatal(err)
	}
}

// R7: behind the trusted proxy the client address comes from X-Forwarded-For, from anyone else it does not.
func TestR7_TrustedProxyClientAddress(t *testing.T) {
	var got string
	h := api.TrustedProxies([]string{"172.30.0.0/24"})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		got = "ip:" + host
	}))
	cases := []struct{ remote, xff, want string }{
		{"172.30.0.5:4000", "203.0.113.9", "ip:203.0.113.9"},
		{"172.30.0.5:4000", "1.1.1.1, 203.0.113.9", "ip:203.0.113.9"},
		{"198.51.100.7:4000", "203.0.113.9", "ip:198.51.100.7"},
		{"172.30.0.5:4000", "", "ip:172.30.0.5"},
		{"172.30.0.5:4000", "not-an-ip", "ip:172.30.0.5"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != c.want {
			t.Errorf("remote %s xff %q: key %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
