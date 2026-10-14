// SPDX-License-Identifier: Apache-2.0

package tpl_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/tpl"
)

// A Laravel stack like car_driver_back: app and worker read .env (which is in .gitignore), and the file needs
// passwords that nobody gave.
const laravelFile = `
services:
  app:
    build: .
    env_file: .env
    depends_on: [postgres, redis]
  worker:
    build: .
    env_file: .env
    command: php artisan queue:work
  nginx:
    image: nginx:1.27
    ports: ["80:80"]
    depends_on: [app]
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: laravel
      POSTGRES_PASSWORD: ${DB_PASSWORD:?set it}
      POSTGRES_DB: ${DB_DATABASE:-laravel}
    volumes:
      - pgdata:/var/lib/postgresql/data
  redis:
    image: redis:7
    command: redis-server --requirepass ${REDIS_PASSWORD:?set it}
volumes:
  pgdata: {}
`

const laravelExample = `APP_NAME=Laravel
APP_KEY=
DB_CONNECTION=sqlite
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=laravel
DB_USERNAME=root
DB_PASSWORD=
REDIS_HOST=127.0.0.1
REDIS_PASSWORD=null
REDIS_PORT=6379
`

func laravelOpts() tpl.ComposeOptions {
	return tpl.ComposeOptions{
		Repo:        &tpl.ComposeRepo{URL: "https://example.com/a/b.git", Branch: "main"},
		EnvFiles:    map[string][]byte{".env.example": []byte(laravelExample)},
		EnvExamples: map[string]string{".env": ".env.example"},
	}
}

func TestComposeLaravelStack(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(laravelFile), laravelOpts())
	if err != nil {
		t.Fatal(err)
	}
	svc := res.Manifest.Services
	for _, name := range []string{"app", "worker"} {
		env := svc[name].Env
		want := map[string]string{
			"DB_HOST": "${{services.postgres.host}}", "REDIS_HOST": "${{services.redis.host}}",
			"DB_CONNECTION": "pgsql", "DB_PORT": "5432", "DB_DATABASE": "laravel", "DB_USERNAME": "laravel",
			"DB_PASSWORD": "${{inputs.g_db_password}}", "REDIS_PASSWORD": "${{inputs.g_redis_password}}", "APP_KEY": "${{inputs.g_app_key}}",
		}
		for k, v := range want {
			if fmt.Sprint(env[k]) != v {
				t.Errorf("%s %s = %v, want %s", name, k, env[k], v)
			}
		}
	}
	if got := fmt.Sprint(svc["postgres"].Env["POSTGRES_PASSWORD"]); got != "${{inputs.g_db_password}}" {
		t.Errorf("the database and the app must share one password input, got %s", got)
	}
	redis := svc["redis"]
	if len(redis.Command) != 3 || redis.Command[0] != "sh" || redis.Command[2] != "redis-server --requirepass ${REDIS_PASSWORD}" {
		t.Errorf("redis command: %q", redis.Command)
	}
	if fmt.Sprint(redis.Env["REDIS_PASSWORD"]) != "${{inputs.g_redis_password}}" {
		t.Errorf("redis must get the secret as an environment variable: %v", redis.Env)
	}
	for _, in := range []string{"g_db_password", "g_redis_password", "g_app_key"} {
		if res.Inputs[in] == nil || res.Inputs[in] == "" {
			t.Errorf("input %s has no value", in)
		}
	}
	if !strings.HasPrefix(fmt.Sprint(res.Inputs["g_app_key"]), "base64:") {
		t.Error("APP_KEY must have the Laravel format")
	}
	// The plan: one value for the password, stored as a secret in the database and in the app.
	ids := map[string]string{}
	for n := range svc {
		ids[n] = "svc_" + n
	}
	plan, err := res.Manifest.Expand(res.Inputs, tpl.Options{ServiceIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{}
	for _, p := range plan.Services {
		for _, e := range p.Env {
			if e.Name == "DB_PASSWORD" || e.Name == "POSTGRES_PASSWORD" {
				if !e.Secret {
					t.Errorf("%s of %s must be a secret", e.Name, p.Name)
				}
				vals[p.Name+"."+e.Name] = e.Value
			}
		}
	}
	if vals["app.DB_PASSWORD"] == "" || vals["app.DB_PASSWORD"] != vals["postgres.POSTGRES_PASSWORD"] || vals["worker.DB_PASSWORD"] != vals["app.DB_PASSWORD"] {
		t.Errorf("the services must share one value: %v", vals)
	}
	var generated, imported int
	for _, l := range res.Losses {
		switch l.Disposition {
		case tpl.LossGenerated:
			generated++
		case tpl.LossImported:
			imported++
		}
	}
	if generated != 3 || imported != 2 {
		t.Errorf("losses: %d generated, %d imported: %+v", generated, imported, res.Losses)
	}
	if !strings.Contains(res.ReplacementYAML, "DB_PASSWORD") || strings.Contains(res.ReplacementYAML, "inputs.") {
		t.Errorf("replacement yaml must list the secrets by name:\n%s", res.ReplacementYAML)
	}
	if strings.Contains(strings.Join(res.Warnings, "\n"), "no value") {
		t.Errorf("generated variables are not warned about: %v", res.Warnings)
	}
}

// No generated value is in any text of the answer, and no marker is left.
func TestComposeGeneratedValueDoesNotLeak(t *testing.T) {
	res, err := tpl.ImportComposeWith([]byte(laravelFile), laravelOpts())
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	texts = append(texts, res.Warnings...)
	texts = append(texts, res.ReplacementYAML)
	for _, v := range []any{res.Losses, res.Manifest} {
		b, _ := json.Marshal(v)
		texts = append(texts, string(b))
	}
	all := strings.Join(texts, "\n")
	n := 0
	for k, v := range res.Inputs {
		if strings.HasPrefix(k, "g_") {
			n++
			if strings.Contains(all, fmt.Sprint(v)) {
				t.Errorf("the value of %s is in a message", k)
			}
		}
	}
	if n != 3 || strings.Contains(all, "__polipan_gen_") {
		t.Errorf("%d generated inputs, or a marker is left", n)
	}
	// An error after the generation (a bad port) names only the variable.
	bad := "services:\n  a:\n    image: x\n    ports: ['${DB_PASSWORD:?x}']\n    environment: {P: '${DB_PASSWORD}'}\n"
	_, err = tpl.ImportComposeWith([]byte(bad), tpl.ComposeOptions{})
	if err == nil {
		t.Fatal("a bad port must be refused")
	}
	if strings.Contains(err.Error(), "__polipan_gen_") || !strings.Contains(err.Error(), "DB_PASSWORD") {
		t.Errorf("the error must name only the variable: %v", err)
	}
}

func TestComposeInterpolationOperators(t *testing.T) {
	cases := []struct {
		text, want string
		vars       map[string]string
		gen        bool // the result is a marker for a generated secret
	}{
		{"${DB_PASSWORD:?m}", "", nil, true},
		{"${DB_PASSWORD?m}", "", nil, true},
		{"${DB_PASSWORD}", "", nil, true},
		{"${DB_PASSWORD:?m}", "given", map[string]string{"DB_PASSWORD": "given"}, false},
		{"${DB_PASSWORD:?m}", "", map[string]string{"DB_PASSWORD": ""}, true},
		{"${DB_PASSWORD?m}", "", map[string]string{"DB_PASSWORD": ""}, false},
		{"${NAME:?m}", "", nil, false}, // not a name that is generated
		{"${X:-d}|${X-d}|${X:+alt}|${X+alt}", "d|d||", nil, false},
		{"${X:+alt}|${X+alt}", "alt|alt", map[string]string{"X": "v"}, false},
	}
	for _, c := range cases {
		doc := "services:\n  a:\n    image: x\n    environment:\n      V: \"" + c.text + "\"\n"
		res, err := tpl.ImportComposeWith([]byte(doc), tpl.ComposeOptions{Vars: c.vars})
		if err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		got := fmt.Sprint(res.Manifest.Services["a"].Env["V"])
		if c.gen {
			if !strings.HasPrefix(got, "${{inputs.g_db_password}}") {
				t.Errorf("%s: want a generated secret, got %q", c.text, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.text, got, c.want)
		}
	}
	// A name that cannot be generated is still told as a variable without a value.
	res, _ := tpl.ImportComposeWith([]byte("services:\n  a:\n    image: x\n    environment: {V: '${API_TOKEN:?m}'}\n"), tpl.ComposeOptions{})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "API_TOKEN") {
		t.Errorf("warnings: %v", res.Warnings)
	}
}

func TestComposeMarkerPlaces(t *testing.T) {
	// A list command and a health check are wrapped in a shell. A marker in a URL variable stays a secret.
	doc := `
services:
  a:
    image: x
    entrypoint: ["run", "--key=${JWT_SECRET:?x}"]
    healthcheck:
      test: ["CMD", "check", "${DB_PASS:?x}"]
    environment:
      DB_URL: postgres://u:${DB_PASS:?x}@h/db
`
	res, err := tpl.ImportComposeWith([]byte(doc), tpl.ComposeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Manifest.Services["a"]
	if got := strings.Join(a.Entrypoint, "|"); got != `sh|-c|exec 'run' "--key=${JWT_SECRET}" "$@"|sh` {
		t.Errorf("entrypoint: %s", got)
	}
	if hc := fmt.Sprint(a.Healthcheck["command"]); hc != `[sh -c exec 'check' "${DB_PASS}"]` {
		t.Errorf("health check: %s", hc)
	}
	if fmt.Sprint(a.Env["JWT_SECRET"]) != "${{inputs.g_jwt_secret}}" || fmt.Sprint(a.Env["DB_PASS"]) != "${{inputs.g_db_pass}}" {
		t.Errorf("env: %v", a.Env)
	}
	if fmt.Sprint(a.Env["DB_URL"]) != "postgres://u:${{inputs.g_db_pass}}@h/db" {
		t.Errorf("DB_URL: %v", a.Env["DB_URL"])
	}
	// Anywhere else it is a problem, and it names only the variable.
	for _, bad := range []string{"image: x\n    ports: ['${DB_PASS:?x}']", "image: x${DB_PASS:?x}", "image: x\n    volumes: ['${DB_PASS:?x}:/data']"} {
		_, err := tpl.ImportComposeWith([]byte("services:\n  a:\n    "+bad+"\n"), tpl.ComposeOptions{})
		if err == nil || strings.Contains(err.Error(), "__polipan_gen_") {
			t.Errorf("%q: want a problem without a marker, got %v", bad, err)
		}
	}
	_, err = tpl.ImportComposeWith([]byte("services:\n  a:\n    image: x\n    labels: ['k=${DB_PASS:?x}']\n    working_dir: ${DB_PASS}\n    command: ['x', '${DB_PASS}']\n"), tpl.ComposeOptions{})
	if err != nil {
		t.Errorf("a marker in an ignored key is fine: %v", err)
	}
	// The command wants the variable, but it has another value.
	_, err = tpl.ImportComposeWith([]byte("services:\n  a:\n    image: x\n    command: ['x', '${DB_PASS}']\n    environment: {DB_PASS: hunter22}\n"), tpl.ComposeOptions{})
	if err == nil || !strings.Contains(err.Error(), "DB_PASS") || strings.Contains(err.Error(), "hunter22") {
		t.Errorf("want a problem that names the variable: %v", err)
	}
}

func TestComposeMissingEnvFile(t *testing.T) {
	opts := laravelOpts()
	// With no example, the loss is manual, as before, and nothing is generated for the file.
	opts.EnvFiles, opts.EnvExamples = nil, nil
	res, err := tpl.ImportComposeWith([]byte(laravelFile), opts)
	if err != nil {
		t.Fatal(err)
	}
	var manual int
	for _, l := range res.Losses {
		if l.Disposition == tpl.LossManual {
			manual++
		}
	}
	if manual != 2 || len(res.Manifest.Services["app"].Env) != 0 {
		t.Errorf("want 2 manual losses and no variables: %d %v", manual, res.Manifest.Services["app"].Env)
	}
}

func TestEnvExampleCandidates(t *testing.T) {
	got := strings.Join(tpl.EnvExampleCandidates("deploy/.env"), " ")
	if got != "deploy/.env.example deploy/.env.sample deploy/.env.dist" {
		t.Error(got)
	}
	got = strings.Join(tpl.EnvExampleCandidates("x.env"), " ")
	if got != "x.env.example x.env.sample x.env.dist .env.example .env.sample .env.dist" {
		t.Error(got)
	}
}

func TestComposeHostByServiceName(t *testing.T) {
	doc := `
services:
  web:
    image: x
    environment:
      DB_HOST: db
      CACHE_HOST: cache
      DATABASE_URL: postgres://u:${DB_PASSWORD:?x}@db:5432/app
      PLAIN_URL: http://other:80
  db:
    image: postgres:16
  cache:
    image: redis:7
`
	res, err := tpl.ImportComposeWith([]byte(doc), tpl.ComposeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := res.Manifest.Services["web"].Env
	want := map[string]string{"DB_HOST": "${{services.db.host}}", "CACHE_HOST": "${{services.cache.host}}",
		"DATABASE_URL": "postgres://u:${{inputs.g_db_password}}@${{services.db.host}}:5432/app", "PLAIN_URL": "http://other:80"}
	for k, v := range want {
		if fmt.Sprint(env[k]) != v {
			t.Errorf("%s = %v, want %s", k, env[k], v)
		}
	}
}
