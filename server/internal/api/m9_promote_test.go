// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/build"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/preview"
	"github.com/polipan/polipan/server/internal/registry"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
)

const m9PullHost = "registry.test:5000"

// m9Pusher "pushes" the build output: the digest is the hash of the tag (the commit).
type m9Pusher struct{}

func (m9Pusher) Push(_ context.Context, _, repo, tag string) (string, string, error) {
	sum := sha256.Sum256([]byte(tag))
	d := "sha256:" + hex.EncodeToString(sum[:])
	return m9PullHost + "/" + repo + "@" + d, d, nil
}

// m9 is the API on top of the real deploy engine and a fake agent. A pull gives a digest that depends on the image,
// so two services of one test have two digests. failPull makes the pull of an image fail.
type m9 struct {
	*testEnv
	gw       *gatewaytest.Env
	ctl      *control.Control
	agent    *gatewaytest.Agent
	pv       *preview.Service
	fake     *git.FakeProvider // the Git provider of the connections: it records statuses and comments
	builder  *build.Fake
	mu       sync.Mutex
	pulls    []string
	cmds     []string
	failPull string
}

func newM9(t *testing.T) *m9 {
	t.Helper()
	gw := gatewaytest.Start(t)
	sec, err := secrets.NewManager(gw.Pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	gw.Cfg.ExternalURL = "https://panel.example.com"
	reg := registry.New(registry.Config{URL: "https://" + m9PullHost, PullHost: m9PullHost, Dir: t.TempDir()}, gw.Pool, gw.CA, testKEK)
	builder := &build.Fake{}
	keys := store.NewGitSecrets(gw.Pool, make([]byte, 32))
	ctl := control.New(control.Deps{DB: gw.Pool, Clock: gw.Clock, Config: gw.Cfg, CA: gw.CA, Sender: gw.GW, Secrets: sec,
		SwitchAckTimeout: 5 * time.Second, PostSwitchCheck: 50 * time.Millisecond,
		Build: &control.BuildDeps{Cloner: &git.FakeCloner{Commit: "1111111aaaaaaa", Files: map[string]string{"Dockerfile": "FROM nginx:alpine\n"}},
			Builder: builder, Pusher: m9Pusher{}, Registry: reg, Keys: keys, WorkDir: t.TempDir()}})
	t.Cleanup(ctl.Close)
	gw.GW.State = ctl
	gw.GW.Health = ctl
	fake := &git.FakeProvider{}
	st := store.New(gw.Pool)
	gd := &api.GitDeps{Keys: keys,
		Policy: func(context.Context, string) ssrf.Policy {
			p, _ := ssrf.New(false, "10.210.0.0/16", ssrf.DefaultDockerNetworks)
			p.Resolver = publicResolver{}
			return p
		},
		NewProvider: func(git.Connection, ssrf.Policy) (git.Provider, error) { return fake, nil }}
	pv := preview.New(preview.Deps{Pool: gw.Pool, Store: st, Orch: ctl, Secrets: sec, Clock: gw.Clock, Log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		ExternalURL: "https://panel.example.com",
		Provider: func(ctx context.Context, orgID, conn string) (git.Provider, error) {
			c, err := st.GetGitConnection(ctx, orgID, conn)
			if err != nil {
				return nil, err
			}
			return gd.ProviderOf(ctx, c)
		}})
	gd.Previews = pv
	h, err := api.NewRouter(api.Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: gw.Pool, Clock: gw.Clock, RateLimit: 100000,
		Pool: gw.Pool, Orchestrator: ctl, ExternalURL: "https://panel.example.com", AgentListen: ":9443", SetupToken: "setup-secret", KEK: testKEK, Secrets: sec, Git: gd})
	if err != nil {
		t.Fatal(err)
	}
	m := &m9{testEnv: &testEnv{t: t, h: h, pool: gw.Pool, orch: &fakeOrch{pool: gw.Pool}, clk: gw.Clock}, gw: gw, ctl: ctl, pv: pv, fake: fake, builder: builder}
	m.agent = gw.NewReadyNode(t, "n1")
	m.agent.Async = true
	m.agent.OnCommand = m.onCommand
	return m
}

func (m *m9) onCommand(c *pb.Command) *pb.CommandAck {
	switch b := c.Body.(type) {
	case *pb.Command_RemoveWorkload:
		m.mu.Lock()
		m.cmds = append(m.cmds, fmt.Sprintf("remove:%s:volumes=%v", b.RemoveWorkload.PlacementId, b.RemoveWorkload.RemoveVolumes))
		m.mu.Unlock()
	}
	if p, ok := c.Body.(*pb.Command_PullImage); ok {
		ref := p.PullImage.ImageRef
		m.mu.Lock()
		m.pulls = append(m.pulls, ref)
		fail := m.failPull != "" && strings.Contains(ref, m.failPull)
		m.mu.Unlock()
		if fail {
			return &pb.CommandAck{Ok: false, ErrorCode: "pull_failed", ErrorMessage: "pull failed"}
		}
		if i := strings.LastIndex(ref, "@"); i >= 0 {
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"` + ref + `","digest":"` + ref[i+1:] + `"}`)}
		}
		sum := sha256.Sum256([]byte(ref))
		d := "sha256:" + hex.EncodeToString(sum[:])
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"` + strings.Split(ref, ":")[0] + `@` + d + `","digest":"` + d + `"}`)}
	}
	return &pb.CommandAck{Ok: true}
}

func (m *m9) q(sql string, args ...any) string {
	m.t.Helper()
	var v any
	if err := m.pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		m.t.Fatal(err)
	}
	return fmt.Sprint(v)
}

// wait polls a deployment until it ends (or until want is reached) and returns its state.
func (m *m9) wait(c *client, depID string) string {
	m.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		d := c.ok(200, "GET", "/api/v1/deployments/"+depID, nil)
		switch str(d, "state") {
		case "live", "failed", "rolled_back", "cancelled", "superseded":
			return str(d, "state")
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("deployment %s stuck in %v", depID, d["state"])
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// svc makes an image service with a port and a domain.
func (m *m9) svc(c *client, envID, name, image, host string) string {
	m.t.Helper()
	s := c.ok(201, "POST", "/api/v1/environments/"+envID+"/services", map[string]any{
		"name": name, "type": "app", "source": map[string]any{"image": image},
		"config": map[string]any{"port": 8080, "health": map[string]any{"type": "tcp", "port": 8080}, "strategy_options": map[string]any{"drain_seconds": 0}}})
	id := str(s, "id")
	if host != "" {
		c.ok(201, "POST", "/api/v1/services/"+id+"/domains", map[string]any{"host": host, "container_port": 8080})
	}
	return id
}

func (m *m9) deploy(c *client, svcID string) string {
	m.t.Helper()
	d := c.ok(202, "POST", "/api/v1/services/"+svcID+"/deploy", map[string]any{})
	id := str(d, "id")
	if st := m.wait(c, id); st != "live" {
		m.t.Fatalf("deployment %s: %s", id, st)
	}
	return m.q(`SELECT release_id FROM deployments WHERE id = $1`, id)
}

func (m *m9) devClient() *client {
	m.t.Helper()
	hash, err := auth.HashPassword(ownerPassword)
	if err != nil {
		m.t.Fatal(err)
	}
	m.addUser("usr_dev9", "dev9@example.com", "developer", hash, m.q(`SELECT org_id FROM memberships WHERE role = 'owner' LIMIT 1`))
	return m.signIn("dev9@example.com", ownerPassword)
}

type promoFixture struct {
	c               *client
	project         string
	staging, prod   string
	stgSvc, prodSvc string
	stgRel          string
	stgDig          string
}

// promoSetup makes a project with a live "web" in staging and a "web" in production (own domain, own secret).
func (m *m9) promoSetup(c *client) *promoFixture {
	m.t.Helper()
	f := &promoFixture{c: c}
	var envs map[string]string
	f.project, envs = c.project("Shop")
	f.staging, f.prod = envs["staging"], envs["production"]
	f.stgSvc = m.svc(c, f.staging, "web", "nginx:1.27", "web.staging.example.com")
	f.prodSvc = m.svc(c, f.prod, "web", "nginx:1.26", "web.example.com")
	// The same names in both environments, with other values (the target keeps its own).
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/secrets", map[string]any{"name": "DB_PASSWORD", "value": "staging-secret-1", "environment_id": f.staging})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/secrets", map[string]any{"name": "DB_PASSWORD", "value": "production-secret-9", "environment_id": f.prod})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "LOG_LEVEL", "value": "debug", "environment_id": f.staging})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "LOG_LEVEL", "value": "warn", "environment_id": f.prod})
	f.stgRel = m.deploy(c, f.stgSvc)
	f.stgDig = m.q(`SELECT image_digest FROM releases WHERE id = $1`, f.stgRel)
	return f
}

// AC-PROMO-01, AC-PROMO-02, AC-PROMO-04: a promotion makes a Release with the same digest, runs no build, keeps the
// domains and the secrets of the target. A dry run changes nothing and shows the diff.
func TestAC_PROMO_01_02_04_SameDigestTargetKeepsItsOwn(t *testing.T) {
	m := newM9(t)
	c := m.owner()
	f := m.promoSetup(c)
	domainsBefore := m.q(`SELECT string_agg(host, ',' ORDER BY host) FROM domains WHERE service_id = $1`, f.prodSvc)
	secretBefore := m.q(`SELECT id || ':' || current_version FROM secrets WHERE service_id IS NULL AND environment_id = $1 AND name = 'DB_PASSWORD'`, f.prod)
	releasesBefore := m.q(`SELECT count(*) FROM releases WHERE service_id = $1`, f.prodSvc)
	buildsBefore := m.q(`SELECT count(*) FROM deployment_builds`)

	// AC-PROMO-04: a dry run changes nothing.
	plan := c.ok(200, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	items, _ := plan["items"].([]any)
	if str(plan, "plan_hash") == "" || len(items) != 1 {
		t.Fatalf("plan: %v", plan)
	}
	it := items[0].(map[string]any)
	if str(it, "image_digest") != f.stgDig || str(it, "service") != "web" || str(it, "strategy") == "" {
		t.Fatalf("plan item: %v", it)
	}
	if changes, _ := it["changes"].([]any); len(changes) == 0 || !strings.Contains(fmt.Sprint(changes), "image_digest") {
		t.Fatalf("the plan shows no diff: %v", it["changes"])
	}
	if m.q(`SELECT count(*) FROM releases WHERE service_id = $1`, f.prodSvc) != releasesBefore || m.q(`SELECT count(*) FROM deployments WHERE service_id = $1`, f.prodSvc) != "0" {
		t.Fatal("a dry run changed the target")
	}
	if strings.Contains(fmt.Sprint(plan), "production-secret-9") || strings.Contains(fmt.Sprint(plan), "staging-secret-1") {
		t.Fatal("a secret value is in the plan")
	}

	// The promotion.
	dep := c.ok(202, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "plan_hash": plan["plan_hash"]})
	if str(dep, "trigger") != "promotion" {
		t.Fatalf("deployment: %v", dep)
	}
	if st := m.wait(c, str(dep, "id")); st != "live" {
		t.Fatalf("promotion: %s", st)
	}
	newRel := m.q(`SELECT release_id FROM deployments WHERE id = $1`, str(dep, "id"))
	if m.q(`SELECT image_digest FROM releases WHERE id = $1`, newRel) != f.stgDig {
		t.Fatal("the digest of the new release is not the digest of the source")
	}
	if m.q(`SELECT source->>'promoted_from_release_id' FROM releases WHERE id = $1`, newRel) != f.stgRel {
		t.Fatal("promoted_from_release_id is not set")
	}
	if m.q(`SELECT count(*) FROM deployment_builds`) != buildsBefore {
		t.Fatal("a build ran")
	}
	if m.q(`SELECT string_agg(host, ',' ORDER BY host) FROM domains WHERE service_id = $1`, f.prodSvc) != domainsBefore {
		t.Fatal("the domains of production changed")
	}
	if m.q(`SELECT id || ':' || current_version FROM secrets WHERE service_id IS NULL AND environment_id = $1 AND name = 'DB_PASSWORD'`, f.prod) != secretBefore {
		t.Fatal("the secret of production changed")
	}
	// The release uses the secret and the variable of production, not the ones of staging.
	prodSecret := strings.Split(secretBefore, ":")[0]
	if m.q(`SELECT config_snapshot->'secret_refs'->>'DB_PASSWORD' FROM releases WHERE id = $1`, newRel) != prodSecret {
		t.Fatal("the release does not use the secret of production")
	}
	if m.q(`SELECT variable_snapshot->>'LOG_LEVEL' FROM releases WHERE id = $1`, newRel) != "warn" {
		t.Fatal("the release does not use the variable of production")
	}
	if got := m.q(`SELECT count(*) FROM audit_events WHERE action = 'release.promote' AND diff->>'plan_hash' = $1`, str(plan, "plan_hash")); got != "1" {
		t.Fatalf("promotion audit events: %s", got)
	}
}

// AC-PROMO-03: a name that the source release used and the target does not have gives 422 missing_variables.
// allow_missing lets it through.
func TestAC_PROMO_03_MissingVariables(t *testing.T) {
	m := newM9(t)
	c := m.owner()
	f := m.promoSetup(c)
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "ONLY_STAGING", "value": "x", "service_id": f.stgSvc})
	rel := m.deploy(c, f.stgSvc)

	rec := c.do("POST", "/api/v1/releases/"+rel+"/promote", map[string]any{"target_environment_id": f.prod})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"missing_variables"`) || !strings.Contains(rec.Body.String(), "ONLY_STAGING") {
		t.Fatalf("missing variables: %d %s", rec.Code, rec.Body)
	}
	if m.q(`SELECT count(*) FROM deployments WHERE service_id = $1`, f.prodSvc) != "0" {
		t.Fatal("a deployment was queued")
	}
	plan := c.ok(200, "POST", "/api/v1/releases/"+rel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	if !strings.Contains(fmt.Sprint(plan["items"]), "ONLY_STAGING") {
		t.Fatalf("the plan does not list the name: %v", plan)
	}
	dep := c.ok(202, "POST", "/api/v1/releases/"+rel+"/promote", map[string]any{"target_environment_id": f.prod, "allow_missing": []string{"ONLY_STAGING"}})
	if st := m.wait(c, str(dep, "id")); st != "live" {
		t.Fatalf("promotion with allow_missing: %s", st)
	}
}

// AC-PROMO-05: the deployment uses the strategy of the target environment.
func TestAC_PROMO_05_TargetStrategyCanary(t *testing.T) {
	m := newM9(t)
	c := m.owner()
	f := m.promoSetup(c)
	m.deploy(c, f.prodSvc) // production has a live release, so a canary has something to compare to
	c.ok(200, "PATCH", "/api/v1/environments/"+f.prod, map[string]any{"settings": map[string]any{"strategy": "canary"}})
	plan := c.ok(200, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	if !strings.Contains(fmt.Sprint(plan["items"]), "strategy:canary") {
		t.Fatalf("the plan has no canary: %v", plan["items"])
	}
	dep := c.ok(202, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod})
	if str(dep, "strategy") != "canary" {
		t.Fatalf("strategy: %v", dep["strategy"])
	}
	_ = c.do("POST", "/api/v1/deployments/"+str(dep, "id")+"/cancel", nil)
}

// AC-PROMO-06: a developer cannot promote into a protected environment (403). A dry run is allowed.
// A preview or another project cannot be the target.
func TestAC_PROMO_06_ProtectedTarget(t *testing.T) {
	m := newM9(t)
	owner := m.owner()
	f := m.promoSetup(owner)
	dev := m.devClient()
	if m.q(`SELECT protected FROM environments WHERE id = $1`, f.prod) != "true" {
		t.Fatal("production is not protected")
	}
	rec := dev.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod})
	if rec.Code != 403 {
		t.Fatalf("developer into protected production: %d %s", rec.Code, rec.Body)
	}
	if m.q(`SELECT count(*) FROM releases WHERE service_id = $1`, f.prodSvc) != "0" {
		t.Fatal("a release was made")
	}
	dev.ok(200, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	// An admin can.
	owner.ok(202, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod})

	// REQ-PROMO-009
	if rec := owner.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.staging}); rec.Code != 422 {
		t.Fatalf("same environment: %d", rec.Code)
	}
	other, otherEnvs := owner.project("Other")
	_ = other
	if rec := owner.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": otherEnvs["production"]}); rec.Code != 422 {
		t.Fatalf("other project: %d %s", rec.Code, rec.Body)
	}
	m.exec(`INSERT INTO environments (id, project_id, name, slug, kind, preview_repo, preview_pr_number) VALUES ('env_pv9', $1, 'pr-1', 'pr-1', 'preview', 'acme/shop', 1)`, f.project)
	if rec := owner.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": "env_pv9"}); rec.Code != 422 {
		t.Fatalf("preview target: %d %s", rec.Code, rec.Body)
	}
}

func (m *m9) exec(sql string, args ...any) {
	m.t.Helper()
	if _, err := m.pool.Exec(context.Background(), sql, args...); err != nil {
		m.t.Fatal(err)
	}
}

// AC-PROMO-08: a plan_hash of a dry run does not work when the target changed. REQ-PROMO-010: a release that was never live
// cannot be promoted.
func TestAC_PROMO_08_PlanChanged(t *testing.T) {
	m := newM9(t)
	c := m.owner()
	f := m.promoSetup(c)
	plan := c.ok(200, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	// The target changes: a new variable in production.
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "NEW_FLAG", "value": "1", "environment_id": f.prod})
	rec := c.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "plan_hash": plan["plan_hash"]})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"plan_changed"`) {
		t.Fatalf("changed target: %d %s", rec.Code, rec.Body)
	}
	if m.q(`SELECT count(*) FROM deployments WHERE service_id = $1`, f.prodSvc) != "0" {
		t.Fatal("a deployment was queued")
	}
	// The new plan hash works.
	plan2 := c.ok(200, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "dry_run": true})
	if plan2["plan_hash"] == plan["plan_hash"] {
		t.Fatal("the plan hash did not change")
	}
	c.ok(202, "POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod, "plan_hash": plan2["plan_hash"]})

	// A release that was never live.
	m.exec(`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source)
		VALUES ('rel_never', $1, 99, 'nginx@sha256:aaa', 'sha256:aaa', '{}', '{}', '{}', '{}')`, f.stgSvc)
	if rec := c.do("POST", "/api/v1/releases/rel_never/promote", map[string]any{"target_environment_id": f.prod}); rec.Code != 409 || !strings.Contains(rec.Body.String(), "release_not_promotable") {
		t.Fatalf("never live: %d %s", rec.Code, rec.Body)
	}
	m.exec(`UPDATE releases SET pruned_at = now() WHERE id = $1`, f.stgRel)
	if rec := c.do("POST", "/api/v1/releases/"+f.stgRel+"/promote", map[string]any{"target_environment_id": f.prod}); rec.Code != 409 || !strings.Contains(rec.Body.String(), "release_not_promotable") {
		t.Fatalf("pruned: %d %s", rec.Code, rec.Body)
	}
}

// AC-PROMO-07: in a project-wide promotion the services run in the order of their dependencies. When the second
// fails, the third is not started and the answer lists the three outcomes. Databases are skipped by default.
func TestAC_PROMO_07_BatchStopsAtTheFirstFailure(t *testing.T) {
	m := newM9(t)
	c := m.owner()
	project, envs := c.project("Suite")
	stg, prod := envs["staging"], envs["production"]
	for _, n := range []string{"a", "b", "c"} {
		m.svc(c, stg, n, n+"-img:1", "")
		m.svc(c, prod, n, n+"-old:1", "")
	}
	_ = project
	ids := map[string]string{}
	for _, n := range []string{"a", "b", "c"} {
		ids[n] = m.q(`SELECT id FROM services WHERE environment_id = $1 AND name = $2`, stg, n)
		m.deploy(c, ids[n])
	}
	// b depends on c, so the order is a, c, b (not the order of the names).
	m.exec(`UPDATE services SET config = config || '{"depends_on":["c"]}' WHERE id = $1`, ids["b"])
	m.mu.Lock()
	m.failPull = "b-img"
	m.mu.Unlock()
	plan := c.ok(200, "POST", "/api/v1/environments/"+stg+"/promote", map[string]any{"target_environment_id": prod, "dry_run": true})
	var order []string
	for _, it := range plan["items"].([]any) {
		order = append(order, str(it.(map[string]any), "service"))
	}
	if strings.Join(order, ",") != "a,c,b" {
		t.Fatalf("the order of the batch: %v", order)
	}
	rec := c.do("POST", "/api/v1/environments/"+stg+"/promote", map[string]any{"target_environment_id": prod})
	if rec.Code != 202 {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	var deps []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &deps)
	if len(deps) != 3 {
		t.Fatalf("the batch lists %d deployments", len(deps))
	}
	states := map[string]string{}
	for _, d := range deps {
		states[m.q(`SELECT name FROM services WHERE id = $1`, str(d, "service_id"))] = m.wait(c, str(d, "id"))
	}
	if states["a"] != "live" || states["c"] != "live" || states["b"] != "failed" {
		t.Fatalf("outcomes: %v", states)
	}

	// Without the dependency the order is a, b, c. The second fails, the third is not started (cancelled).
	for _, n := range []string{"a", "b", "c"} {
		m.exec(`UPDATE services SET config = config - 'depends_on' WHERE id = $1`, ids[n])
	}
	m.mu.Lock()
	m.pulls = nil
	m.mu.Unlock()
	rec = c.do("POST", "/api/v1/environments/"+stg+"/promote", map[string]any{"target_environment_id": prod})
	if rec.Code != 202 {
		t.Fatalf("batch 2: %d %s", rec.Code, rec.Body)
	}
	deps = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &deps)
	if len(deps) != 3 {
		t.Fatalf("the batch lists %d deployments", len(deps))
	}
	got := []string{}
	for _, d := range deps {
		got = append(got, m.wait(c, str(d, "id")))
	}
	if strings.Join(got, ",") != "live,failed,cancelled" {
		t.Fatalf("outcomes of a, b, c: %v", got)
	}
	if m.q(`SELECT failure_reason FROM deployments WHERE id = $1`, str(deps[2], "id")) != "batch_stopped" {
		t.Fatal("the third deployment has no batch_stopped reason")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pulls {
		if strings.Contains(p, "c-img") {
			t.Errorf("c was pulled: %s", p)
		}
	}
}
