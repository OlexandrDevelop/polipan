// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
)

type publicResolver struct{}

func (publicResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if strings.HasSuffix(host, ".example.com") {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	return nil, errors.New("no such host")
}

// gitOrch is fakeOrch, but a system deploy (no actor) stores a NULL actor like the real engine.
type gitOrch struct{ *fakeOrch }

func (g gitOrch) Deploy(ctx context.Context, serviceID, actorID, trigger string) (string, error) {
	if actorID != "" {
		return g.fakeOrch.Deploy(ctx, serviceID, actorID, trigger)
	}
	id := store.NewID("dep")
	_, err := g.pool.Exec(ctx, `INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state)
		SELECT $1, s.id, s.environment_id, $3, 'recreate', 'queued' FROM services s WHERE s.id = $2`, id, serviceID, trigger)
	return id, err
}

// gitEnv is the M1 test env with the Git side wired in.
func gitEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, "https://panel.example.com")
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: e.pool, Clock: e.clk, RateLimit: 100000,
		Pool: e.pool, Orchestrator: gitOrch{e.orch}, ExternalURL: "https://panel.example.com", AgentListen: ":9443", SetupToken: "setup-secret",
		Git: &api.GitDeps{Keys: store.NewGitSecrets(e.pool, make([]byte, 32)), Policy: func(context.Context, string) ssrf.Policy {
			p, _ := ssrf.New(false, "10.210.0.0/16", ssrf.DefaultDockerNetworks)
			p.Resolver = publicResolver{}
			return p
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	return e
}

func (e *testEnv) hook(path string, h http.Header, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:4000"
	req.Header = h
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// AC-APP-03: a valid push webhook on a non-protected environment queues a deployment with
// trigger git_push (actor system). A bad signature gives 401 and queues nothing. A replayed
// delivery is refused. A protected environment does not auto deploy until an admin turns it on.
func TestAC_APP_03_AC_AUDIT_03_PushWebhookQueuesDeployment(t *testing.T) {
	e := gitEnv(t)
	c := e.owner()
	_, envs := c.project("shop")

	conn := c.ok(201, "POST", "/api/v1/git-connections", map[string]any{
		"provider": "gitea", "name": "forge", "base_url": "https://gitea.example.com", "token": "tok-123"})
	connID, hookURL, secret := str(conn, "id"), str(conn, "webhook_url"), str(conn, "webhook_secret")
	if !strings.HasPrefix(hookURL, "https://panel.example.com/api/v1/webhooks/git/"+connID) || secret == "" {
		t.Fatalf("connection answer %v", conn)
	}
	list := c.do("GET", "/api/v1/git-connections", nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), secret) || strings.Contains(list.Body.String(), "tok-123") {
		t.Fatalf("list leaks a secret or fails: %d %s", list.Code, list.Body)
	}
	// An internal base_url is refused (SSRF).
	if rec := c.do("POST", "/api/v1/git-connections", map[string]any{"provider": "gitea", "name": "x", "base_url": "http://169.254.169.254", "token": "t"}); rec.Code != 422 {
		t.Fatalf("internal base_url: %d %s", rec.Code, rec.Body)
	}

	mk := func(envID, name string) string {
		s := c.ok(201, "POST", "/api/v1/environments/"+envID+"/services", map[string]any{"name": name, "type": "app",
			"source": map[string]any{"type": "git", "connection_id": connID, "repo": "acme/shop", "branch": "main", "builder": "dockerfile"},
			"config": map[string]any{"port": 8080}})
		if str(s, "source_type") != "git" {
			t.Fatalf("service %v", s)
		}
		return str(s, "id")
	}
	staging, prod := mk(envs["staging"], "web"), mk(envs["production"], "web")
	other := c.ok(201, "POST", "/api/v1/environments/"+envs["staging"]+"/services", map[string]any{"name": "other", "type": "app",
		"source": map[string]any{"type": "git", "connection_id": connID, "repo": "acme/other", "branch": "main"}})
	_ = other

	body := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"acme/shop"},"pusher":{"login":"ann"}}`)
	path := "/api/v1/webhooks/git/" + connID

	// Bad signature: 401, nothing queued.
	if rec := e.hook(path, git.SignatureHeaders(git.Gitea, []byte("wrong"), body, "d-1"), body); rec.Code != 401 {
		t.Fatalf("bad signature: %d %s", rec.Code, rec.Body)
	}
	if rec := e.hook("/api/v1/webhooks/git/git_unknown", git.SignatureHeaders(git.Gitea, []byte(secret), body, "d-1"), body); rec.Code != 401 {
		t.Fatalf("unknown webhook: %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM deployments`); n != 0 {
		t.Fatalf("%d deployments after a bad signature", n)
	}

	// Valid: only the staging service with the same repo and branch is queued (git_push, no user actor).
	rec := e.hook(path, git.SignatureHeaders(git.Gitea, []byte(secret), body, "d-1"), body)
	if rec.Code != 202 {
		t.Fatalf("push: %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM deployments WHERE service_id = $1 AND trigger = 'git_push' AND actor_id IS NULL`, staging); n != 1 {
		t.Fatalf("staging deployments: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM deployments`); n != 1 {
		t.Fatalf("protected or other repo was deployed: %d", n)
	}
	// AC-AUDIT-03: the auto deploy is an audit event of the system with the trigger and the commit author.
	if n := e.count(`SELECT count(*) FROM audit_events WHERE actor_type = 'system' AND actor_id = $1 AND action = 'deployment.create'
		AND target_type = 'deployment' AND diff->>'author' = 'ann' AND diff->>'trigger' = 'git_push'
		AND diff->>'commit' = '0123456789abcdef0123456789abcdef01234567' AND diff->>'service_id' = $2`, "webhook:"+connID, staging); n != 1 {
		t.Fatalf("auto deploy audit events: %d", n)
	}

	// Replay of the same delivery: refused, nothing new.
	if rec := e.hook(path, git.SignatureHeaders(git.Gitea, []byte(secret), body, "d-1"), body); rec.Code != 409 {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM deployments`); n != 1 {
		t.Fatalf("replay queued a deployment: %d", n)
	}

	// An admin turns on auto deploy on the protected environment: the next push deploys it too.
	c.ok(200, "PATCH", "/api/v1/services/"+prod, map[string]any{"config": map[string]any{"port": 8080,
		"git": map[string]any{"connection_id": connID, "repo": "acme/shop", "branch": "main", "builder": "dockerfile", "auto_deploy": true}}})
	if rec := e.hook(path, git.SignatureHeaders(git.Gitea, []byte(secret), body, "d-2"), body); rec.Code != 202 {
		t.Fatalf("second push: %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM deployments WHERE service_id = $1 AND trigger = 'git_push'`, prod); n != 1 {
		t.Fatalf("protected after admin opt-in: %d", n)
	}

	// The connection is in use: it cannot be deleted.
	if rec := c.do("DELETE", "/api/v1/git-connections/"+connID, nil); rec.Code != 409 {
		t.Fatalf("delete used connection: %d", rec.Code)
	}
}

// AC-APP-03 for a repo without a connection: the deploy key call gives the key and the service
// webhook (GitHub style signature). A manual deploy takes a commit only for git services.
func TestAC_APP_03_DeployKeyWebhook(t *testing.T) {
	e := gitEnv(t)
	c := e.owner()
	_, envs := c.project("blog")
	s := c.ok(201, "POST", "/api/v1/environments/"+envs["staging"]+"/services", map[string]any{"name": "web", "type": "app",
		"source": map[string]any{"type": "git", "repo": "git@git.example.com:acme/blog.git", "provider": "github"}})
	svc := str(s, "id")
	if rec := c.do("POST", "/api/v1/environments/"+envs["staging"]+"/services", map[string]any{"name": "bad", "type": "app",
		"source": map[string]any{"type": "git", "repo": "http://127.0.0.1:3000/acme/blog.git"}}); rec.Code != 422 {
		t.Fatalf("loopback repo: %d %s", rec.Code, rec.Body)
	}
	k := c.ok(201, "POST", "/api/v1/services/"+svc+"/deploy-key", nil)
	if !strings.HasPrefix(str(k, "public_key"), "ssh-ed25519 ") || str(k, "webhook_secret") == "" {
		t.Fatalf("deploy key answer %v", k)
	}
	if rec := c.do("GET", "/api/v1/services/"+svc, nil); !strings.Contains(rec.Body.String(), "ssh-ed25519 ") || strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Fatalf("the public key must be in the source and the private key never: %s", rec.Body)
	}
	path := strings.TrimPrefix(str(k, "webhook_url"), "https://panel.example.com")
	body := []byte(`{"ref":"refs/heads/main","after":"89abcdef0123456789abcdef0123456789abcdef","repository":{"full_name":"acme/blog"}}`)
	if rec := e.hook(path, git.SignatureHeaders(git.GitHub, []byte(str(k, "webhook_secret")), body, "x-1"), body); rec.Code != 202 {
		t.Fatalf("push: %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM deployments WHERE service_id = $1 AND trigger = 'git_push'`, svc); n != 1 {
		t.Fatalf("deployments %d", n)
	}
	img := c.service(envs["staging"], "img")
	if rec := c.do("POST", "/api/v1/services/"+img+"/deploy", map[string]any{"commit": "0123456789abcdef"}); rec.Code != 422 {
		t.Fatalf("commit on an image service: %d", rec.Code)
	}
}
