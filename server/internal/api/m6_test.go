// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/cac"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
)

// statusProvider is a Git provider that records the commit statuses.
type statusProvider struct {
	git.FakeProvider
	mu       sync.Mutex
	statuses []git.Status
}

func (p *statusProvider) PostStatus(_ context.Context, _, _ string, st git.Status) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses = append(p.statuses, st)
	return nil
}

func (p *statusProvider) states() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, s := range p.statuses {
		out = append(out, s.State)
	}
	return out
}

// m6Env is the M1 test env with the config engine's Git side (a fake cloner) and the backup service.
type m6Env struct {
	*testEnv
	cloner *git.FakeCloner
	prov   *statusProvider
}

func newM6Env(t *testing.T) *m6Env {
	t.Helper()
	e := newTestEnv(t, "https://panel.example.com")
	sec, err := secrets.NewManager(e.pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	logOut := io.Discard
	if os.Getenv("M6_LOG") != "" {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewJSONHandler(logOut, nil))
	bk := backup.New(backup.Deps{DB: e.pool, Secrets: sec, Clock: e.clk, Log: log})
	prov := &statusProvider{}
	cl := &git.FakeCloner{Files: map[string]string{}, Commit: "0123456789abcdef0123456789abcdef01234567"}
	handler, err := api.NewRouter(api.Deps{
		Log: log, DB: e.pool, Clock: e.clk, RateLimit: 100000, Pool: e.pool, Orchestrator: gitOrch{e.orch},
		ExternalURL: "https://panel.example.com", AgentListen: ":9443", SetupToken: "setup-secret", KEK: testKEK, Secrets: sec, Backup: bk,
		Git: &api.GitDeps{Keys: store.NewGitSecrets(e.pool, make([]byte, 32)), Cloner: cl,
			NewProvider: func(git.Connection, ssrf.Policy) (git.Provider, error) { return prov, nil }, Policy: func(context.Context, string) ssrf.Policy {
				p, _ := ssrf.New(false, "10.210.0.0/16", ssrf.DefaultDockerNetworks)
				p.Resolver = publicResolver{}
				return p
			}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = handler
	return &m6Env{testEnv: e, cloner: cl, prov: prov}
}

// keepOnly deletes the other environments of a project: a file applies its services to every environment.
func (c *client) keepOnly(envs map[string]string, keep string) {
	c.e.t.Helper()
	for slug, id := range envs {
		if slug != keep {
			if rec := c.do("DELETE", "/api/v1/environments/"+id, nil); rec.Code != 204 {
				c.e.t.Fatalf("delete environment %s: %d %s", slug, rec.Code, rec.Body)
			}
		}
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../test/fixtures/config/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (c *client) plan(prj, file string) map[string]any {
	c.e.t.Helper()
	rec := c.raw("POST", "/api/v1/projects/"+prj+"/config/plan", "application/yaml", file)
	if rec.Code != 200 {
		c.e.t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// apply plans, then applies. It returns the status and the answer of apply.
func (c *client) apply(prj, file string, extra map[string]any) (int, map[string]any) {
	c.e.t.Helper()
	pl := c.plan(prj, file)
	if pl["valid"] != true {
		c.e.t.Fatalf("plan is not valid: %v", pl["errors"])
	}
	body := map[string]any{"config": file, "plan_hash": pl["plan_hash"]}
	for k, v := range extra {
		body[k] = v
	}
	rec := c.do("POST", "/api/v1/projects/"+prj+"/config/apply", body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func changes(pl map[string]any, action string) []string {
	var out []string
	list, _ := pl["changes"].([]any)
	for _, c := range list {
		m := c.(map[string]any)
		if m["action"] == action {
			out = append(out, str(m, "resource")+":"+str(m, "name"))
		}
	}
	return out
}

func (e *testEnv) setSecret(c *client, prj, name, value string) {
	e.t.Helper()
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": name, "value": value})
}

// AC-CAC-01, AC-CAC-02, AC-CAC-04: plan changes nothing, apply makes the services, a removed service is unmanaged
// and stays until prune.
func TestAC_CAC_01_02_04_PlanApplyUnmanaged(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, envs := c.project("blog")
	c.keepOnly(envs, "production")
	file := fixture(t, "example-8-1-simple-app.yaml")

	pl := c.plan(prj, file)
	if pl["valid"] != true || len(changes(pl, "create")) == 0 {
		t.Fatalf("plan %v", pl)
	}
	if n := e.count(`SELECT count(*) FROM services WHERE name = 'web'`); n != 0 {
		t.Fatalf("plan made %d services", n)
	}

	code, res := c.apply(prj, file, nil)
	if code != 202 {
		t.Fatalf("apply: %d %v", code, res)
	}
	if n := e.count(`SELECT count(*) FROM services WHERE name = 'web' AND deleted_at IS NULL`); n != 1 {
		t.Fatalf("web services: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM domains WHERE host = 'blog.example.com'`); n != 1 {
		t.Fatalf("domains with the host: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM deployments WHERE trigger = 'config_apply'`); n == 0 {
		t.Fatalf("no deployment with trigger config_apply")
	}
	// The same file again: nothing to do.
	again := c.plan(prj, file)
	if len(changes(again, "create"))+len(changes(again, "update"))+len(changes(again, "delete")) != 0 {
		t.Fatalf("second plan is not empty: %v", again["changes"])
	}

	// A service that is not in the file is unmanaged. It stays without prune (AC-CAC-04).
	two := file + "  worker:\n    source:\n      image:\n        name: busybox:1.36\n"
	if code, res := c.apply(prj, two, nil); code != 202 {
		t.Fatalf("apply two: %d %v", code, res)
	}
	pl = c.plan(prj, file)
	if len(changes(pl, "unmanaged")) == 0 {
		t.Fatalf("worker is not unmanaged: %v", pl["changes"])
	}
	if code, res := c.apply(prj, file, nil); code != 202 {
		t.Fatalf("apply without prune: %d %v", code, res)
	}
	if n := e.count(`SELECT count(*) FROM services WHERE name = 'worker' AND deleted_at IS NULL`); n == 0 {
		t.Fatalf("worker was deleted without prune")
	}
	// Prune needs an admin and deletes it.
	dev := e.userClient("usr_dev1", "dev1@example.com", "developer")
	pl = c.plan(prj, file)
	rec := dev.do("POST", "/api/v1/projects/"+prj+"/config/apply", map[string]any{"config": file, "plan_hash": pl["plan_hash"], "prune": true})
	if rec.Code != 403 {
		t.Fatalf("developer prune: %d %s", rec.Code, rec.Body)
	}
	if code, res := c.apply(prj, file, map[string]any{"prune": true}); code != 202 {
		t.Fatalf("apply prune: %d %v", code, res)
	}
	if n := e.count(`SELECT count(*) FROM services WHERE name = 'worker' AND deleted_at IS NULL`); n != 0 {
		t.Fatalf("worker is still there after prune: %d", n)
	}
	// A stale plan hash is refused.
	rec = c.do("POST", "/api/v1/projects/"+prj+"/config/apply", map[string]any{"config": file, "plan_hash": "stale"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "plan_changed") {
		t.Fatalf("stale hash: %d %s", rec.Code, rec.Body)
	}
}

// AC-CAC-03: a field from the file is read only (409 managed_by_git). An admin can override it with override_git:
// the project is drifted and the next apply reverts the value.
func TestAC_CAC_03_ManagedByGitAndDrift(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, envs := c.project("blog")
	file := "version: 1\nproject: blog\nservices:\n  web:\n    source:\n      image: { name: nginx:1.27 }\n    ports: [80]\n    env:\n      LOG_LEVEL: info\n"
	if code, res := c.apply(prj, file, nil); code != 202 {
		t.Fatalf("apply: %d %v", code, res)
	}
	svc := e.queryString(`SELECT id FROM services WHERE name = 'web' AND environment_id = $1`, envs["staging"])
	// The managed image cannot change by hand.
	rec := c.do("PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.26", "port": 80}})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "managed_by_git") {
		t.Fatalf("manual image change: %d %s", rec.Code, rec.Body)
	}
	// A value that is the same is not a change.
	// The variable of the file is read only too.
	vid := e.queryString(`SELECT id FROM variables WHERE name = 'LOG_LEVEL' AND service_id = $1`, svc)
	rec = c.do("PATCH", "/api/v1/variables/"+vid, map[string]any{"name": "LOG_LEVEL", "value": "debug"})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "managed_by_git") {
		t.Fatalf("manual variable change: %d %s", rec.Code, rec.Body)
	}
	// Developer: override_git needs an admin.
	dev := e.userClient("usr_dev2", "dev2@example.com", "developer")
	rec = dev.do("PATCH", "/api/v1/services/"+svc, map[string]any{"override_git": true, "config": map[string]any{"image": "nginx:1.26", "port": 80}})
	if rec.Code != 403 && rec.Code != 409 {
		t.Fatalf("developer override: %d %s", rec.Code, rec.Body)
	}
	// An admin override makes a drift record.
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"override_git": true, "config": map[string]any{"image": "nginx:1.26", "port": 80}})
	if st := str(c.ok(200, "GET", "/api/v1/projects/"+prj, nil), "config_sync_state"); st != "drifted" {
		t.Fatalf("sync state %q", st)
	}
	pl := c.plan(prj, file)
	if len(changes(pl, "update")) == 0 {
		t.Fatalf("the plan does not revert the drift: %v", pl["changes"])
	}
	if code, res := c.apply(prj, file, nil); code != 202 {
		t.Fatalf("apply: %d %v", code, res)
	}
	if n := e.count(`SELECT count(*) FROM services WHERE id = $1 AND config->>'image' = 'nginx:1.27'`, svc); n != 1 {
		t.Fatalf("image was not reverted")
	}
	if st := str(c.ok(200, "GET", "/api/v1/projects/"+prj, nil), "config_sync_state"); st != "in_sync" {
		t.Fatalf("sync state after apply %q", st)
	}
}

func (e *testEnv) queryString(q string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// AC-CAC-05: an unknown key is an error with a line and a field.
func TestAC_CAC_05_UnknownKey(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, _ := c.project("blog")
	pl := c.plan(prj, fixture(t, "unknown-key.yaml"))
	errs, _ := pl["errors"].([]any)
	if pl["valid"] != false || len(errs) == 0 {
		t.Fatalf("plan %v", pl)
	}
	first := errs[0].(map[string]any)
	if first["line"].(float64) < 1 || str(first, "path") == "" {
		t.Fatalf("error without line or field: %v", first)
	}
	if n := e.count(`SELECT count(*) FROM services`); n != 0 {
		t.Fatalf("an invalid file changed something")
	}
}

// AC-CAC-06: export has secret names, never values. The exported file validates and plans empty.
func TestAC_CAC_06_Export(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, envs := c.project("blog")
	svc := c.service(envs["production"], "web")
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80}})
	e.setSecret(c, prj, "API_TOKEN", "super-secret-value-123")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "REGION", "value": "eu"})
	rec := c.do("GET", "/api/v1/projects/"+prj+"/config", nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "yaml") {
		t.Fatalf("export: %d %s", rec.Code, rec.Header())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "API_TOKEN") || strings.Contains(out, "super-secret-value-123") || !strings.Contains(out, "REGION") {
		t.Fatalf("export %s", out)
	}
	if _, issues := cac.Parse([]byte(out)); len(issues) > 0 {
		t.Fatalf("the exported file is not valid: %v\n%s", issues, out)
	}
	pl := c.plan(prj, out)
	if pl["valid"] != true {
		t.Fatalf("exported file does not plan: %v\n%s", pl["errors"], out)
	}
}

// AC-CAC-08: a developer cannot set config_source. An admin can.
func TestAC_CAC_08_ConfigSourceNeedsAdmin(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, _ := c.project("blog")
	dev := e.userClient("usr_dev3", "dev3@example.com", "developer")
	src := map[string]any{"repo": "acme/blog", "branch": "main", "path": "polipan.yaml"}
	if rec := dev.do("PATCH", "/api/v1/projects/"+prj, map[string]any{"config_source": src}); rec.Code != 403 {
		t.Fatalf("developer: %d %s", rec.Code, rec.Body)
	}
	if rec := dev.do("PATCH", "/api/v1/projects/"+prj, map[string]any{"settings": map[string]any{"previews": map[string]any{"from_forks": true}}}); rec.Code != 403 {
		t.Fatalf("developer from_forks: %d %s", rec.Code, rec.Body)
	}
	got := c.ok(200, "PATCH", "/api/v1/projects/"+prj, map[string]any{"config_source": src})
	cs, _ := got["config_source"].(map[string]any)
	if cs["repo"] != "acme/blog" {
		t.Fatalf("project %v", got)
	}
	if rec := c.do("PATCH", "/api/v1/projects/"+prj, map[string]any{"config_source": map[string]any{"repo": "x"}}); rec.Code != 422 {
		t.Fatalf("source without branch: %d", rec.Code)
	}
}

func (e *m6Env) connection(c *client) (id, hook, secret string) {
	e.t.Helper()
	conn := c.ok(201, "POST", "/api/v1/git-connections", map[string]any{
		"provider": "gitea", "name": "forge", "base_url": "https://gitea.example.com", "token": "tok-123"})
	return str(conn, "id"), str(conn, "webhook_url"), str(conn, "webhook_secret")
}

func (e *m6Env) push(connID, secret, delivery string) *httptest.ResponseRecorder {
	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"acme/blog"},"pusher":{"login":"ann"}}`)
	return e.hook("/api/v1/webhooks/git/"+connID, git.SignatureHeaders(git.Gitea, []byte(secret), body, delivery), body)
}

func (e *m6Env) waitSync(prj, state string) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e.count(`SELECT count(*) FROM config_syncs WHERE project_id = $1 AND state = $2`, prj, state) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("no sync in state %s", state)
}

// AC-CAC-03 (push side), AC-CAC-07, REQ-CAC-013/014: a push applies by itself for a non protected environment. A push
// that touches the protected production environment is stored as plan_pending until an admin applies it.
func TestAC_CAC_07_PushProtectedPlanPending(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, envs := c.project("blog")
	connID, _, secret := e.connection(c)
	c.ok(200, "PATCH", "/api/v1/projects/"+prj, map[string]any{"config_source": map[string]any{
		"connection_id": connID, "repo": "acme/blog", "branch": "main", "path": "polipan.yaml"}})

	base := "version: 1\nproject: blog\nservices:\n  web:\n    source:\n      image: { name: nginx:1.27 }\n    ports: [80]\n"
	if code, res := c.apply(prj, base, nil); code != 202 {
		t.Fatalf("apply: %d %v", code, res)
	}
	prodSvc := e.queryString(`SELECT id FROM services WHERE name = 'web' AND environment_id = $1`, envs["production"])

	// 1. A push that changes only staging applies by itself. The system is the actor and the author is in the audit event.
	stagingOnly := base + "environments:\n  staging:\n    services:\n      web:\n        env:\n          MODE: test\n"
	e.cloner.Files["polipan.yaml"] = stagingOnly
	if rec := e.push(connID, secret, "d-1"); rec.Code != 202 {
		t.Fatalf("push: %d %s", rec.Code, rec.Body)
	}
	e.waitSync(prj, "applied")
	if n := e.count(`SELECT count(*) FROM variables WHERE name = 'MODE' AND git_managed`); n != 1 {
		t.Fatalf("staging variable was not applied: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE actor_type = 'system' AND action = 'config.sync' AND diff->>'author' = 'ann' AND project_id = $1`, prj); n == 0 {
		t.Fatalf("no audit event of the sync")
	}

	// 2. A push that makes production unprotected and adds a pre_deploy: nothing changes, the plan waits.
	risky := "version: 1\nproject: blog\nenvironments:\n  production:\n    kind: production\n    protected: false\nservices:\n  web:\n    source:\n      image: { name: nginx:1.27 }\n    ports: [80]\n    deploy:\n      pre_deploy: [\"./migrate\"]\n"
	e.cloner.Files["polipan.yaml"] = risky
	if rec := e.push(connID, secret, "d-2"); rec.Code != 202 {
		t.Fatalf("push 2: %d %s", rec.Code, rec.Body)
	}
	e.waitSync(prj, "plan_pending")
	if st := str(c.ok(200, "GET", "/api/v1/projects/"+prj, nil), "config_sync_state"); st != "plan_pending" {
		t.Fatalf("project state %q", st)
	}
	if n := e.count(`SELECT count(*) FROM environments WHERE id = $1 AND protected`, envs["production"]); n != 1 {
		t.Fatalf("production was changed by a push")
	}
	if n := e.count(`SELECT count(*) FROM services WHERE id = $1 AND config ? 'pre_deploy'`, prodSvc); n != 0 {
		t.Fatalf("production service was changed by a push")
	}
	// A developer cannot apply it. An admin can.
	dev := e.userClient("usr_dev4", "dev4@example.com", "developer")
	pl := c.plan(prj, risky)
	if rec := dev.do("POST", "/api/v1/projects/"+prj+"/config/apply", map[string]any{"config": risky, "plan_hash": pl["plan_hash"]}); rec.Code != 403 {
		t.Fatalf("developer apply: %d %s", rec.Code, rec.Body)
	}
	if code, res := c.apply(prj, risky, nil); code != 202 {
		t.Fatalf("admin apply: %d %v", code, res)
	}
	if n := e.count(`SELECT count(*) FROM environments WHERE id = $1 AND NOT protected`, envs["production"]); n != 1 {
		t.Fatalf("production is still protected after the admin apply")
	}
	if n := e.count(`SELECT count(*) FROM config_syncs WHERE project_id = $1 AND state = 'approved'`, prj); n != 1 {
		t.Fatalf("the pending sync was not approved")
	}
	if st := str(c.ok(200, "GET", "/api/v1/projects/"+prj, nil), "config_sync_state"); st != "in_sync" {
		t.Fatalf("project state after apply %q", st)
	}

	// The commit status told the user: pending first, then success, then pending (waiting for an admin).
	if got := strings.Join(e.prov.states(), ","); !strings.Contains(got, "success") || !strings.HasSuffix(got, "pending") {
		t.Fatalf("commit statuses: %s", got)
	}

	// 3. An invalid file on push: nothing applies, the error is stored with its line.
	e.cloner.Files["polipan.yaml"] = fixture(t, "unknown-key.yaml")
	if rec := e.push(connID, secret, "d-3"); rec.Code != 202 {
		t.Fatalf("push 3: %d %s", rec.Code, rec.Body)
	}
	e.waitSync(prj, "invalid")
	if n := e.count(`SELECT count(*) FROM config_syncs WHERE state = 'invalid' AND errors->0->>'line' IS NOT NULL`); n != 1 {
		t.Fatalf("invalid sync without error line")
	}
	if got := e.prov.states(); got[len(got)-1] != "failure" {
		t.Fatalf("commit status of an invalid file: %v", got)
	}
}

// Idempotency (AC-API-02): the same key and body gives the first answer, no second deployment.
func TestAC_API_02_ApplyIdempotent(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, _ := c.project("blog")
	file := "version: 1\nproject: blog\nenvironments:\n  production: { kind: production }\nservices:\n  web:\n    source:\n      image: { name: nginx:1.27 }\n    ports: [80]\n"
	pl := c.plan(prj, file)
	send := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"config": file, "plan_hash": pl["plan_hash"]})
		req := httptest.NewRequest("POST", "/api/v1/projects/"+prj+"/config/apply", bytes.NewReader(raw))
		req.RemoteAddr = c.addr
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k-1")
		req.AddCookie(c.cookie)
		req.Header.Set("X-CSRF-Token", c.csrf)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	first, second := send(), send()
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted || second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("answers: %d %d %s", first.Code, second.Code, second.Body)
	}
	if n := e.count(`SELECT count(*) FROM deployments WHERE trigger = 'config_apply'`); n != 2 { // one per environment, once
		t.Fatalf("deployments: %d", n)
	}
}

// The three examples of 07 section 8 validate and apply (done criterion 2). The secrets they name are set first.
func TestM6_SpecExamplesApply(t *testing.T) {
	for _, tc := range []struct {
		file, project, only string
		secrets             map[string]string
	}{
		{"example-8-1-simple-app.yaml", "blog", "production", nil},
		{"example-8-2-postgres-bluegreen-backup.yaml", "shop", "", map[string]string{"B2_CREDENTIALS": `{"account_id":"a1","account_key":"k1"}`, "SESSION_KEY": "s3cret-session"}},
		{"example-8-3-canary-previews-domains.yaml", "shop", "", map[string]string{"SESSION_KEY": "s3cret-session", "SENTRY_DSN": "https://x@sentry.example.com/1"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			e := newM6Env(t)
			c := e.owner()
			prj, envs := c.project(tc.project)
			if tc.only != "" {
				c.keepOnly(envs, tc.only)
			}
			for k, v := range tc.secrets {
				e.setSecret(c, prj, k, v)
			}
			file := fixture(t, tc.file)
			code, res := c.apply(prj, file, nil)
			if code != 202 {
				t.Fatalf("apply: %d %v", code, res)
			}
			again := c.plan(prj, file)
			if left := append(changes(again, "create"), changes(again, "update")...); len(left) != 0 {
				t.Fatalf("a second plan still has changes: %v", left)
			}
		})
	}
}

// SEC-1: the file follows the host port rule of the API (30000-39999, not reserved, not a panel port).
func TestM6_HostPortRuleInFile(t *testing.T) {
	e := newM6Env(t)
	c := e.owner()
	prj, _ := c.project("blog")
	for port, ok := range map[string]bool{"22": false, "443": false, "9443": false, "80": false, "40000": false, "30500": true} {
		file := "version: 1\nproject: blog\nservices:\n  web:\n    source: { image: { name: nginx } }\n    ports:\n      - { container: 80, host: " + port + " }\n"
		pl := c.plan(prj, file)
		if (pl["valid"] == true) != ok {
			t.Errorf("host port %s: valid=%v errors=%v", port, pl["valid"], pl["errors"])
		}
	}
	db := "version: 1\nproject: blog\nservices:\n  db:\n    type: database\n    db: { engine: postgres, expose_host_port: 5432 }\n"
	if pl := c.plan(prj, db); pl["valid"] == true {
		t.Errorf("db port 5432 is accepted")
	}
}
