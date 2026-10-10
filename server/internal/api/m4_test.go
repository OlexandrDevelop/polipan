// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/tpl"
)

// raw sends a request with a text body (YAML).
func (c *client) raw(method, path, contentType, body string) *httptest.ResponseRecorder {
	c.e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = c.addr
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(c.cookie)
	req.Header.Set("X-CSRF-Token", c.csrf)
	rec := httptest.NewRecorder()
	c.e.h.ServeHTTP(rec, req)
	return rec
}

func (c *client) createDB(envID, name string, extra map[string]any) *httptest.ResponseRecorder {
	body := map[string]any{"name": name, "type": "database", "db_engine": "postgres"}
	for k, v := range extra {
		body[k] = v
	}
	return c.do("POST", "/api/v1/environments/"+envID+"/services", body)
}

// AC-DB-01: a Postgres service has a DB_PASSWORD secret that the API never returns, and a second
// service resolves ${{db.main.URL}} after a new Release.
func TestAC_DB_01_PasswordAndReference(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	rec := c.createDB(envs["staging"], "main", nil)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	dbID := str(db, "id")
	if str(db, "db_engine") != "postgres" || str(db, "db_version") != "16" || db["type"] != "database" {
		t.Fatalf("db: %v", db)
	}
	var password string
	var version int
	ctx := context.Background()
	if err := e.pool.QueryRow(ctx, `SELECT current_version FROM secrets WHERE service_id = $1 AND name = 'DB_PASSWORD'`, dbID).Scan(&version); err != nil {
		t.Fatalf("no DB_PASSWORD secret: %v", err)
	}
	// The API never returns the value.
	list := c.do("GET", "/api/v1/projects/"+prj+"/secrets?service_id="+dbID, nil)
	var plain string
	_ = e.pool.QueryRow(ctx, `SELECT 'x'`).Scan(&plain)
	if !strings.Contains(list.Body.String(), "DB_PASSWORD") || strings.Contains(list.Body.String(), `"value"`) {
		t.Fatalf("secret list: %s", list.Body)
	}
	_ = password
	for _, v := range []string{"DB_USER", "DB_NAME", "POSTGRES_PASSWORD"} {
		if e.count(`SELECT count(*) FROM variables WHERE service_id = $1 AND name = $2`, dbID, v) != 1 {
			t.Fatalf("variable %s missing", v)
		}
	}
	if e.count(`SELECT count(*) FROM volumes WHERE service_id = $1 AND name = 'data' AND access_mode = 'single_writer'`, dbID) != 1 {
		t.Fatal("no data volume")
	}
	if _, state, reason := e.deploy(c, dbID); state != "live" {
		t.Fatalf("db deploy: %s %s", state, reason)
	}

	app := c.service(envs["staging"], "api")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "DATABASE_URL", "value": "${{db.main.URL}}", "service_id": app})
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "DB_HOST", "value": "${{db.main.HOST}}:${{db.main.PORT}}", "service_id": app})
	if _, state, reason := e.deploy(c, app); state != "live" {
		t.Fatalf("app deploy: %s %s", state, reason)
	}
	w := e.workload(app)
	url := w.Env["DATABASE_URL"]
	if !strings.HasPrefix(url, "postgres://app:") || !strings.HasSuffix(url, "@svc-"+strings.ToLower(strings.TrimPrefix(dbID, "svc_"))+":5432/app") {
		t.Fatalf("DATABASE_URL = %q", url)
	}
	if strings.Contains(w.Env["DB_HOST"], "${{") || w.Env["DB_HOST"] == "" {
		t.Fatalf("DB_HOST = %q", w.Env["DB_HOST"])
	}
	for _, k := range w.SecretEnvKeys {
		if strings.Contains(k, ".") {
			t.Fatalf("secret env key %q", k)
		}
	}
	if _, bad := w.Env["db.main.PASSWORD"]; bad {
		t.Fatal("the reference secret became an environment variable")
	}
	// The release snapshot never holds the password.
	vars, versions := e.liveRelease(app)
	raw, _ := json.Marshal(vars)
	pw := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(url, "@", 2)[0], "postgres://app:"), "")
	if strings.Contains(string(raw), pw) || versions["db.main.PASSWORD"] == nil {
		t.Fatalf("snapshot: %s %v", raw, versions)
	}
	// The password of the database is a secret of the app too: it is masked in logs.
	vals, err := e.sec.ValuesForService(ctx, app)
	if err != nil || len(vals) == 0 {
		t.Fatalf("mask values: %v %v", vals, err)
	}
}

// AC-DB-02: blue_green on a database is recreate, with the warning zero_downtime_not_available.
func TestAC_DB_02_BlueGreenIsForcedRecreate(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	rec := c.createDB(envs["staging"], "main", map[string]any{"config": map[string]any{"strategy": "blue_green"}})
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), "zero_downtime_not_available") || !strings.Contains(rec.Body.String(), `"strategy":"recreate"`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	id := str(db, "id")
	upd := c.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"strategy": "blue_green"}})
	if upd.Code != 200 || !strings.Contains(upd.Body.String(), "zero_downtime_not_available") || !strings.Contains(upd.Body.String(), `"strategy":"recreate"`) {
		t.Fatalf("update: %d %s", upd.Code, upd.Body)
	}
	plan := c.ok(200, "POST", "/api/v1/services/"+id+"/deploy", map[string]any{"dry_run": true, "strategy": "blue_green"})
	if plan["strategy"] != "recreate" || plan["forced"] != true || !strings.Contains(mustJSON(plan), "zero_downtime_not_available") {
		t.Fatalf("plan: %v", plan)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// AC-DB-03: no host port is open, and the UI gets a backup suggestion until the user skips it.
func TestAC_DB_03_NoPublicPortAndBackupSuggestion(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	rec := c.createDB(envs["staging"], "main", nil)
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	id := str(db, "id")
	cfg := db["config"].(map[string]any)
	if _, has := cfg["host_port"]; has {
		t.Fatalf("host_port: %v", cfg)
	}
	b := db["backup"].(map[string]any)
	if b["status"] != "none" || b["suggestion"] == nil {
		t.Fatalf("backup: %v", b)
	}
	if _, state, _ := e.deploy(c, id); state != "live" {
		t.Fatal("deploy")
	}
	if w := e.workload(id); len(w.Ports) != 1 || w.Ports[0].HostPort != 0 {
		t.Fatalf("ports: %v", w.Ports)
	}
	got := c.ok(200, "PATCH", "/api/v1/services/"+id, map[string]any{"backup_skipped": true})
	if got["backup"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("skipped: %v", got["backup"])
	}
}

// REQ-DB-001, REQ-DB-005, REQ-DB-007: version rules, host port rules, major upgrade.
func TestREQ_DB_001_REQ_DB_005_REQ_DB_007_VersionAndExposureRules(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	for _, v := range []string{"latest", "99", "16; rm", "mysql"} {
		if rec := c.createDB(envs["staging"], "bad", map[string]any{"db_version": v}); rec.Code != 422 {
			t.Fatalf("version %q: %d %s", v, rec.Code, rec.Body)
		}
	}
	if rec := c.createDB(envs["staging"], "m", map[string]any{"db_engine": "mysql", "db_version": "8.4"}); rec.Code != 201 {
		t.Fatalf("mysql: %d %s", rec.Code, rec.Body)
	}
	rec := c.createDB(envs["staging"], "main", map[string]any{"db_version": "16.4"})
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	id := str(db, "id")
	// A minor change is fine, a major change is refused.
	c.ok(200, "PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"image": "postgres:16.6"}})
	if rec := c.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"image": "postgres:17"}}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "dump and restore into a new service") {
		t.Fatalf("major: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"image": "evil/postgres:16"}}); rec.Code != 422 {
		t.Fatalf("other image: %d", rec.Code)
	}
	// Host port: range 30000-39999 and not the reserved ports.
	for _, p := range []int{22, 53, 80, 443, 9443, 29999, 40000, 8080} {
		if rec := c.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"host_port": p}}); rec.Code != 422 {
			t.Fatalf("host_port %d: %d %s", p, rec.Code, rec.Body)
		}
	}
	ok := c.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"host_port": 30432}})
	if ok.Code != 200 || !strings.Contains(ok.Body.String(), "database_exposed") {
		t.Fatalf("expose: %d %s", ok.Code, ok.Body)
	}
	// The same range applies to an app (carry-over of M3).
	app := c.do("POST", "/api/v1/environments/"+envs["staging"]+"/services", map[string]any{
		"name": "web", "type": "app", "source": map[string]any{"image": "nginx:1"}, "config": map[string]any{"host_port": 80}})
	if app.Code != 422 {
		t.Fatalf("app host_port 80: %d %s", app.Code, app.Body)
	}
	// The panel port: external_url of the test is http://localhost, agent listen :9443 (9443 is reserved anyway).
}

// Exposing a database of a protected environment needs an admin (REQ-DB-005).
func TestREQ_DB_005_ExposeNeedsAdminOnProtected(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	rec := c.createDB(envs["production"], "main", nil)
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	id := str(db, "id")
	dev := e.login("usr_dev", "dev@example.com", "developer")
	if r := dev.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"host_port": 30001}}); r.Code != 403 {
		t.Fatalf("developer: %d %s", r.Code, r.Body)
	}
	admin := e.login("usr_adm", "admin@example.com", "admin")
	if r := admin.do("PATCH", "/api/v1/services/"+id, map[string]any{"config": map[string]any{"host_port": 30001}}); r.Code != 200 {
		t.Fatalf("admin: %d %s", r.Code, r.Body)
	}
}

// AC-DB-04: a service that references a missing database fails at the Release and nothing deploys.
func TestAC_DB_04_MissingDatabase(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	app := c.service(envs["staging"], "api")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "DATABASE_URL", "value": "${{db.nope.URL}}", "service_id": app})
	_, state, reason := e.deploy(c, app)
	if state != "failed" || reason != "unresolved_reference" {
		t.Fatalf("deploy: %s %s", state, reason)
	}
	if e.count(`SELECT count(*) FROM releases WHERE service_id = $1`, app) != 0 {
		t.Fatal("a release was made")
	}
	if ds, err := e.ctl.DesiredState(context.Background(), e.agent.NodeID); err != nil || len(ds.Workloads) != 0 {
		t.Fatalf("workloads: %v %v", ds, err)
	}
	// A database of another environment is not visible.
	c.createDB(envs["production"], "nope", nil)
	if _, state, reason := e.deploy(c, app); state != "failed" || reason != "unresolved_reference" {
		t.Fatalf("other environment: %s %s", state, reason)
	}
}

// AC-TPL-01: WordPress makes a WordPress and a MySQL service, the password is a generated secret, and both go live.
func TestAC_TPL_01_Wordpress(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Blog")
	list := c.do("GET", "/api/v1/templates", nil)
	var tpls []map[string]any
	_ = json.Unmarshal(list.Body.Bytes(), &tpls)
	if len(tpls) < 12 {
		t.Fatalf("templates: %d", len(tpls))
	}
	rec := c.do("POST", "/api/v1/templates/wordpress/instantiate", map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{"domain": "Blog.Example.com"}})
	if rec.Code != 201 {
		t.Fatalf("instantiate: %d %s", rec.Code, rec.Body)
	}
	var svcs []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &svcs)
	if len(svcs) != 2 {
		t.Fatalf("services: %s", rec.Body)
	}
	ids := map[string]string{}
	for _, s := range svcs {
		ids[str(s, "name")] = str(s, "id")
	}
	if ids["wordpress"] == "" || ids["wordpress-db"] == "" || str(svcs[0], "template_instance_id") == "" {
		t.Fatalf("services: %v", ids)
	}
	if e.count(`SELECT count(*) FROM secrets WHERE service_id = $1 AND name = 'DB_PASSWORD'`, ids["wordpress-db"]) != 1 {
		t.Fatal("no generated DB_PASSWORD")
	}
	if e.count(`SELECT count(*) FROM domains WHERE service_id = $1 AND host = 'blog.example.com' AND container_port = 80`, ids["wordpress"]) != 1 {
		t.Fatal("no domain")
	}
	if e.count(`SELECT count(*) FROM template_instances WHERE template_id = 'wordpress' AND template_version = '1.0.0'`) != 1 {
		t.Fatal("no template instance")
	}
	for _, name := range []string{"wordpress-db", "wordpress"} {
		if _, state, reason := e.deploy(c, ids[name]); state != "live" {
			t.Fatalf("%s: %s %s", name, state, reason)
		}
	}
	env := e.workload(ids["wordpress"]).Env
	if env["WORDPRESS_DB_USER"] != "app" || env["WORDPRESS_DB_NAME"] != "app" || len(env["WORDPRESS_DB_PASSWORD"]) != 32 || strings.Contains(env["WORDPRESS_DB_HOST"], "${{") {
		t.Fatalf("wordpress env: %v", env)
	}
	mysql := e.workload(ids["wordpress-db"]).Env
	if mysql["MYSQL_PASSWORD"] != env["WORDPRESS_DB_PASSWORD"] || mysql["MYSQL_USER"] != "app" {
		t.Fatalf("mysql env: %v", mysql)
	}
	// A domain that is used already gives 409 before anything is created.
	before := e.count(`SELECT count(*) FROM services`)
	rec = c.do("POST", "/api/v1/templates/ghost/instantiate", map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{"domain": "blog.example.com"}})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "domain_in_use") || e.count(`SELECT count(*) FROM services`) != before {
		t.Fatalf("domain in use: %d %s", rec.Code, rec.Body)
	}
}

// AC-TPL-02: a manifest with privileged: true gives 422 template_unsafe and creates nothing.
func TestAC_TPL_02_UnsafeTemplate(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Blog")
	manifest := "slug: bad\nversion: 1.0.0\nname: Bad\nservices:\n  app:\n    source: {image: {name: 'nginx:1'}}\n    privileged: true\n    network_mode: host\n"
	rec := c.raw("POST", "/api/v1/templates", "application/yaml", manifest)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "template_unsafe") || !strings.Contains(rec.Body.String(), "privileged") {
		t.Fatalf("create template: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM templates`) != 0 || e.count(`SELECT count(*) FROM services`) != 0 {
		t.Fatal("something was created")
	}
	// A compose file is checked the same way.
	rec = c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose", "application/yaml",
		"services:\n  a:\n    image: nginx\n    volumes: ['/var/run/docker.sock:/var/run/docker.sock']\n")
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "template_unsafe") || !strings.Contains(rec.Body.String(), "Docker socket") {
		t.Fatalf("compose: %d %s", rec.Code, rec.Body)
	}
	// A safe custom template can be added once, and a second time with the same version is refused.
	ok := "slug: mine\nversion: 1.0.0\nname: Mine\nservices:\n  app:\n    source: {image: {name: 'nginx:1'}}\n    ports: [80]\n"
	if rec := c.raw("POST", "/api/v1/templates", "application/yaml", ok); rec.Code != 201 {
		t.Fatalf("custom: %d %s", rec.Code, rec.Body)
	}
	if rec := c.raw("POST", "/api/v1/templates", "application/yaml", ok); rec.Code != 409 {
		t.Fatalf("same version: %d %s", rec.Code, rec.Body)
	}
	if rec := c.raw("POST", "/api/v1/templates", "application/yaml", strings.Replace(ok, "mine", "wordpress", 1)); rec.Code != 409 {
		t.Fatalf("builtin slug: %d %s", rec.Code, rec.Body)
	}
	inst := c.do("POST", "/api/v1/templates/mine/instantiate", map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{}})
	if inst.Code != 201 {
		t.Fatalf("instantiate custom: %d %s", inst.Code, inst.Body)
	}
}

// AC-TPL-03: a Compose file with build: is refused with a clear message.
func TestAC_TPL_03_ComposeBuild(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Blog")
	rec := c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose", "application/yaml", "services:\n  web:\n    build: .\n")
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "build: is not supported") || !strings.Contains(rec.Body.String(), "Git service") {
		t.Fatalf("build: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM services`) != 0 {
		t.Fatal("a service was created")
	}
	ok := "services:\n  web:\n    image: nginx:1\n    ports: ['8080:80']\n    environment: {A: '1'}\n    unknown_key: 1\n"
	dry := c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose?dry_run=true", "application/yaml", ok)
	if dry.Code != 200 || !strings.Contains(dry.Body.String(), "unknown_key") || e.count(`SELECT count(*) FROM services`) != 0 {
		t.Fatalf("dry run: %d %s", dry.Code, dry.Body)
	}
	real := c.raw("POST", "/api/v1/environments/"+envs["staging"]+"/import-compose", "application/yaml", ok)
	if real.Code != 200 || e.count(`SELECT count(*) FROM services WHERE name = 'web'`) != 1 || e.count(`SELECT count(*) FROM variables WHERE name = 'A'`) != 1 {
		t.Fatalf("import: %d %s", real.Code, real.Body)
	}
}

// AC-TPL-04: if the second service fails, neither is created.
func TestAC_TPL_04_AllOrNothing(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Blog")
	// 1. The second service of the group meets a service with the same name: the first one is rolled back.
	c.service(envs["staging"], "wordpress")
	before := e.count(`SELECT count(*) FROM services`)
	rec := c.do("POST", "/api/v1/templates/wordpress/instantiate", map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{"domain": "b.example.com"}})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "name_conflict") {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	for _, q := range []string{`SELECT count(*) FROM services`, `SELECT count(*) FROM template_instances`, `SELECT count(*) FROM volumes`,
		`SELECT count(*) FROM secrets`, `SELECT count(*) FROM domains`, `SELECT count(*) FROM variables`} {
		want := 0
		if q == `SELECT count(*) FROM services` {
			want = before
		}
		if e.count(q) != want {
			t.Fatalf("%s = %d, want %d", q, e.count(q), want)
		}
	}
	// 2. A stored template whose second service is not valid: 422 and nothing is created.
	manifest := `{"slug":"half","version":"1.0.0","name":"Half","services":{
		"a-db":{"type":"database","db":{"engine":"postgres"}},
		"b-app":{"source":{"image":{"name":"nginx:1"}},"volumes":[{"name":"x","path":"relative/path"}]}}}`
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO templates (id, org_id, slug, version, manifest) SELECT 'tem_half', id, 'half', '1.0.0', $1 FROM orgs LIMIT 1`, manifest); err != nil {
		t.Fatal(err)
	}
	rec = c.do("POST", "/api/v1/templates/half/instantiate", map[string]any{"environment_id": envs["staging"], "inputs": map[string]any{}})
	if rec.Code != 422 || e.count(`SELECT count(*) FROM services`) != before || e.count(`SELECT count(*) FROM secrets`) != 0 {
		t.Fatalf("invalid second service: %d %s", rec.Code, rec.Body)
	}
}

// The 12 built-in templates pass the checks and expand with sample inputs (REQ-TPL-004).
func TestREQ_TPL_004_BuiltinTemplatesAreValid(t *testing.T) {
	list, err := tpl.Builtin()
	if err != nil || len(list) != 12 {
		t.Fatalf("builtin: %d %v", len(list), err)
	}
	for _, m := range list {
		if err := api.TrialPlanForTest(m); err != nil {
			t.Errorf("%s: %v", m.Slug, err)
		}
	}
}
