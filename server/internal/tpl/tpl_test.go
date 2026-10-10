// SPDX-License-Identifier: Apache-2.0

package tpl_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/tpl"
)

func TestBuiltinTemplates(t *testing.T) {
	list, err := tpl.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"directus", "ghost", "gitea", "metabase", "minio", "n8n", "nextcloud", "pgadmin", "plausible", "uptime-kuma", "vaultwarden", "wordpress"}
	if len(list) != len(want) {
		t.Fatalf("got %d templates, want %d", len(list), len(want))
	}
	for i, m := range list {
		if m.Slug != want[i] {
			t.Fatalf("template %d is %s, want %s", i, m.Slug, want[i])
		}
	}
}

// AC-TPL-02: privileged is refused with the list of what is wrong.
func TestAC_TPL_02_Privileged(t *testing.T) {
	_, err := tpl.ParseManifest([]byte(`
slug: bad
version: 1.0.0
name: Bad
services:
  app:
    source: { image: { name: "nginx:1" } }
    privileged: true
    cap_add: [SYS_ADMIN, NET_BIND_SERVICE]
    network_mode: host
    pid: host
    devices: ["/dev/sda:/dev/sda"]
    volumes: ["/var/run/docker.sock:/var/run/docker.sock", "/etc:/host-etc"]
`))
	var u *tpl.UnsafeError
	if !errors.As(err, &u) {
		t.Fatalf("want UnsafeError, got %v", err)
	}
	text := u.Error()
	for _, w := range []string{"privileged", "SYS_ADMIN", "network_mode host", "pid host", "devices", "Docker socket", "/etc"} {
		if !strings.Contains(text, w) {
			t.Errorf("violations miss %q: %s", w, text)
		}
	}
	if strings.Contains(text, "capability NET_BIND_SERVICE") {
		t.Errorf("NET_BIND_SERVICE is allowed: %s", text)
	}
}

// AC-TPL-03: build: is refused with a clear message.
func TestAC_TPL_03_ComposeBuild(t *testing.T) {
	_, err := tpl.ImportCompose([]byte("services:\n  web:\n    build: .\n    image: x\n"))
	var ps tpl.Problems
	if !errors.As(err, &ps) || !strings.Contains(err.Error(), "build: is not supported") || !strings.Contains(err.Error(), "Git service") {
		t.Fatalf("got %v", err)
	}
}

func TestComposeImport(t *testing.T) {
	res, err := tpl.ImportCompose([]byte(`
services:
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: secret
    volumes: ["pgdata:/var/lib/postgresql/data"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 10s
      retries: 5
    restart: unless-stopped
    deploy:
      resources:
        limits: { cpus: "0.5", memory: 512m }
    labels: { a: b }
    some_unknown_key: 1
  web:
    image: nginx:1
    ports: ["8080:80", "443"]
    depends_on: [db]
    environment: ["A=1", "B"]
volumes:
  pgdata: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	web, db := res.Manifest.Services["web"], res.Manifest.Services["db"]
	if len(web.Ports) != 2 || web.Ports[0].Container != 80 || web.DependsOn[0] != "db" || web.Env["A"] != "1" {
		t.Fatalf("web: %+v", web)
	}
	if db.Resources["memory"] != "524288Ki" || db.Resources["cpu"] != "0.5" || db.Restart != "always" || db.Volumes[0].Name != "pgdata" {
		t.Fatalf("db: %+v", db)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, w := range []string{"some_unknown_key", "host port 8080"} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings miss %q: %s", w, joined)
		}
	}
	plan, err := res.Manifest.Expand(nil, tpl.Options{ServiceIDs: map[string]string{"db": "svc_1", "web": "svc_2"}})
	if err != nil || len(plan.Services) != 2 || plan.Services[0].Name != "db" {
		t.Fatalf("plan %v %v", plan, err)
	}
}

func TestComposeUnsafeBind(t *testing.T) {
	_, err := tpl.ImportCompose([]byte("services:\n  a:\n    image: x\n    volumes: ['./data:/data']\n    privileged: true\n"))
	var u *tpl.UnsafeError
	if !errors.As(err, &u) || len(u.Violations) != 2 {
		t.Fatalf("got %v", err)
	}
	if _, err := tpl.ImportCompose([]byte("services:\n  a:\n    image: x\n    volumes: ['/var/lib/polipan/binds/a/data:/data']\n")); err != nil {
		t.Fatalf("a bind inside the binds folder is allowed: %v", err)
	}
}

func TestExpandInputs(t *testing.T) {
	m, err := tpl.ParseManifest([]byte(`
slug: t
version: 1.0.0
name: T
inputs:
  - { name: domain, type: domain, required: true }
  - { name: pw, type: secret, generate: true }
  - { name: n, type: number, default: 3 }
services:
  db:
    type: database
    db: { engine: postgres }
  app:
    source: { image: { name: "x:1" } }
    ports: [8080]
    env:
      URL: "https://${{inputs.domain}}"
      PW: "${{inputs.pw}}"
      DB: "${{services.db.host}}:${{services.db.port}}"
      REF: "${{db.db.URL}}"
    domains: [{ host: "${{inputs.domain}}" }]
    depends_on: [db]
`))
	if err != nil {
		t.Fatal(err)
	}
	opt := tpl.Options{ServiceIDs: map[string]string{"db": "svc_AA", "app": "svc_BB"}}
	if _, err := m.Expand(map[string]any{}, opt); err == nil || !strings.Contains(err.Error(), "inputs.domain: is required") {
		t.Fatalf("missing domain: %v", err)
	}
	plan, err := m.Expand(map[string]any{"domain": "App.Example.com"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := plan.Inputs["pw"]; has {
		t.Fatal("a secret input must not be in the instance inputs")
	}
	app := plan.Services[1]
	env := map[string]string{}
	secret := map[string]bool{}
	for _, e := range app.Env {
		env[e.Name], secret[e.Name] = e.Value, e.Secret
	}
	if env["URL"] != "https://app.example.com" || secret["URL"] || !secret["PW"] || len(env["PW"]) != 32 {
		t.Fatalf("env %v %v", env, secret)
	}
	if env["DB"] != "svc-aa:5432" || env["REF"] != "${{db.db.URL}}" {
		t.Fatalf("env %v", env)
	}
	if app.Domains[0].Port != 8080 || app.Domains[0].Host != "app.example.com" {
		t.Fatalf("domain %+v", app.Domains)
	}
	db := plan.Services[0]
	if db.DBEngine != "postgres" || db.Config["strategy"] != "recreate" || db.Volumes[0].MountPath != "/var/lib/postgresql/data" {
		t.Fatalf("db %+v", db)
	}
}
