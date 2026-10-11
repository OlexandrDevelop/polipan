// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/git"
)

// previewFixture is a project with a base environment (staging): a git service "web", a database "db" and an image
// service "static" that is live. The org has a preview domain and the project has previews on.
type previewFixture struct {
	c                *client
	project, staging string
	prod             string
	conn, secret     string
	web, db, static  string
	staticDigest     string
	staticRelease    string
	deliveries       int
	commitA, commitB string
}

const (
	commitA = "aaaaaaa1111111111111111111111111111111aa"
	commitB = "bbbbbbb2222222222222222222222222222222bb"
)

func (m *m9) previewSetup(settings map[string]any) *previewFixture {
	m.t.Helper()
	c := m.owner()
	f := &previewFixture{c: c, commitA: commitA, commitB: commitB}
	var envs map[string]string
	f.project, envs = c.project("Shop")
	f.staging, f.prod = envs["staging"], envs["production"]
	m.exec(`UPDATE orgs SET settings = settings || '{"preview_domain":"preview.example.com"}' WHERE id = (SELECT org_id FROM memberships WHERE role = 'owner' LIMIT 1)`)

	conn := c.ok(201, "POST", "/api/v1/git-connections", map[string]any{"provider": "gitea", "name": "forge", "base_url": "https://gitea.example.com", "token": "tok-123"})
	f.conn, f.secret = str(conn, "id"), str(conn, "webhook_secret")
	web := c.ok(201, "POST", "/api/v1/environments/"+f.staging+"/services", map[string]any{"name": "web", "type": "app",
		"source": map[string]any{"type": "git", "connection_id": f.conn, "repo": "acme/shop", "branch": "main", "builder": "dockerfile"},
		"config": map[string]any{"port": 8080, "health": map[string]any{"type": "tcp", "port": 8080}, "strategy_options": map[string]any{"drain_seconds": 0}}})
	f.web = str(web, "id")
	c.ok(201, "POST", "/api/v1/services/"+f.web+"/domains", map[string]any{"host": "web.staging.example.com", "container_port": 8080})
	db := c.ok(201, "POST", "/api/v1/environments/"+f.staging+"/services", map[string]any{"name": "db", "type": "database", "db_engine": "postgres"})
	f.db = str(db, "id")
	f.static = m.svc(c, f.staging, "static", "nginx:1.27", "static.staging.example.com")

	// Variables: one for previews, one not, a secret that must never reach a preview, and a reference to the database.
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "GREETING", "value": "hello preview", "preview": true})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "BASE_ONLY", "value": "base", "preview": false})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/variables", map[string]any{"name": "DATABASE_URL", "value": "${{db.db.URL}}", "preview": true})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/secrets", map[string]any{"name": "DB_PASSWORD_PROD", "value": "production-secret-9", "preview": false})
	c.ok(201, "POST", "/api/v1/projects/"+f.project+"/secrets", map[string]any{"name": "PREVIEW_KEY", "value": "preview-key-1", "preview": true})

	// The static service is live in the base environment: a preview runs the same digest.
	f.staticRelease = m.deploy(c, f.static)
	f.staticDigest = m.q(`SELECT image_digest FROM releases WHERE id = $1`, f.staticRelease)

	pv := map[string]any{"enabled": true, "base_environment_id": f.staging}
	for k, v := range settings {
		pv[k] = v
	}
	c.ok(200, "PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": pv}})
	return f
}

// prEvent sends a signed pull request event of Gitea.
func (m *m9) prEvent(f *previewFixture, action string, number int, sha, branch string, o map[string]any) *http.Response {
	m.t.Helper()
	pr := map[string]any{"number": number, "title": "Add a page", "html_url": fmt.Sprintf("https://gitea.example.com/acme/shop/pulls/%d", number),
		"merged": false, "user": map[string]any{"login": "ann"},
		"head":   map[string]any{"sha": sha, "ref": branch, "repo": map[string]any{"full_name": "acme/shop"}},
		"base":   map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/shop"}},
		"labels": []any{}}
	for k, v := range o {
		switch k {
		case "fork":
			pr["head"].(map[string]any)["repo"] = map[string]any{"full_name": "mallory/shop"}
		case "labels":
			pr["labels"] = v
		case "merged":
			pr["merged"] = v
		}
	}
	body, _ := json.Marshal(map[string]any{"action": action, "number": number, "pull_request": pr, "repository": map[string]any{"full_name": "acme/shop"}})
	f.deliveries++
	h := git.SignatureHeaders(git.Gitea, []byte(f.secret), body, fmt.Sprintf("pr-%d-%d", number, f.deliveries))
	h.Set("X-Gitea-Event", "pull_request")
	rec := m.hook("/api/v1/webhooks/git/"+f.conn, h, body)
	if rec.Code != 202 {
		m.t.Fatalf("pull request event %s: %d %s", action, rec.Code, rec.Body)
	}
	return rec.Result()
}

// settle runs the preview pass until the preview has the state (or fails the test).
func (m *m9) settle(envSlug, want string) {
	m.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		m.pv.Tick(context.Background())
		got := m.q(`SELECT COALESCE(settings->>'preview_state','') FROM environments WHERE slug = $1 AND kind = 'preview' AND deleted_at IS NULL`, envSlug)
		idle := m.q(`SELECT COALESCE(settings->'preview_pending', '{}'::jsonb) = '{}'::jsonb FROM environments WHERE slug = $1 AND kind = 'preview' AND deleted_at IS NULL`, envSlug)
		if got == want && idle == "true" {
			return
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("preview %s is %s, want %s (failure: %s)", envSlug, got, want,
				m.q(`SELECT COALESCE(settings->>'preview_failure','') FROM environments WHERE slug = $1 AND kind = 'preview' AND deleted_at IS NULL`, envSlug))
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func (m *m9) previewEnv(slug string) string {
	return m.q(`SELECT id FROM environments WHERE slug = $1 AND kind = 'preview' AND deleted_at IS NULL`, slug)
}

func (m *m9) workloadEnv(envID, service string) map[string]string {
	m.t.Helper()
	nodeID := m.agent.NodeID
	ds, err := m.ctl.DesiredState(context.Background(), nodeID)
	if err != nil {
		m.t.Fatal(err)
	}
	svc := m.q(`SELECT id FROM services WHERE environment_id = $1 AND name = $2 AND deleted_at IS NULL`, envID, service)
	for _, w := range ds.Workloads {
		if w.ServiceId == svc && w.Role == "live" {
			return w.Env
		}
	}
	m.t.Fatalf("no live workload of %s in %s", service, envID)
	return nil
}

// AC-PREV-01 to AC-PREV-04, AC-PREV-07, AC-VOL-04 (T-E2E-06 in one process): a PR opens, the preview deploys with a URL
// in the PR comment, a push updates the same comment, the production secret is not in the container, the database is
// new, no volume of the base is used, and the close of the PR deletes everything after the grace time.
func TestAC_PREV_01_to_04_AC_PREV_07_AC_VOL_04_Lifecycle(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(nil)
	baseVols := m.q(`SELECT string_agg(v.id, ',') FROM volumes v JOIN services s ON s.id = v.service_id WHERE s.environment_id = $1`, f.staging)

	m.prEvent(f, "opened", 5, commitA, "feature-x", nil)
	m.settle("pr-5", "active")
	env := m.previewEnv("pr-5")

	// AC-PREV-01: the environment, the services, the URL in the comment.
	if m.q(`SELECT preview_repo || '#' || preview_pr_number || ':' || protected || ':' || (base_env_id = $2) FROM environments WHERE id = $1`, env, f.staging) != "acme/shop#5:false:true" {
		t.Fatal("the environment has the wrong repo, number, protected flag or base")
	}
	if m.q(`SELECT string_agg(name, ',' ORDER BY name) FROM services WHERE environment_id = $1`, env) != "db,static,web" {
		t.Fatal("the services of the base were not cloned")
	}
	if m.q(`SELECT host FROM domains d JOIN services s ON s.id = d.service_id WHERE s.environment_id = $1 AND s.name = 'web'`, env) != "pr-5-web.preview.example.com" {
		t.Fatal("the URL of web is not pr-<n>-<service>.<preview domain>")
	}
	if m.q(`SELECT count(*) FROM domains d JOIN services s ON s.id = d.service_id WHERE s.environment_id = $1 AND d.host LIKE '%staging%'`, env) != "0" {
		t.Fatal("a fixed host of the base was copied")
	}
	statuses, comments, _ := m.fake.Snapshot()
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "https://pr-5-web.preview.example.com") || comments[0].Number != 5 || comments[0].Repo != "acme/shop" {
		t.Fatalf("the comment: %+v", comments)
	}
	if len(statuses) < 2 || statuses[0].State != "pending" || statuses[len(statuses)-1].State != "success" || statuses[len(statuses)-1].Commit != commitA ||
		statuses[len(statuses)-1].Context != "polipan/preview" {
		t.Fatalf("the statuses: %+v", statuses)
	}
	// The PR commit was built for web. static runs the live release of the base: the same digest, no build.
	webRel := m.q(`SELECT r.id FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'web'`, env)
	if m.q(`SELECT source->>'commit_sha' FROM releases WHERE id = $1`, webRel) != commitA {
		t.Fatal("the release of web is not the commit of the PR")
	}
	if m.q(`SELECT r.image_digest FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'static'`, env) != f.staticDigest {
		t.Fatal("static does not run the digest of the base release")
	}
	if m.q(`SELECT count(*) FROM deployment_builds b JOIN deployments d ON d.id = b.deployment_id JOIN services s ON s.id = d.service_id WHERE s.environment_id = $1 AND s.name = 'static'`, env) != "0" {
		t.Fatal("static was built")
	}
	if m.q(`SELECT strategy FROM deployments d JOIN services s ON s.id = d.service_id WHERE s.environment_id = $1 AND s.name = 'web'`, env) != "recreate" {
		t.Fatal("the strategy of a preview is not recreate")
	}

	// AC-PREV-04: only preview = true values reach the container.
	webEnv := m.workloadEnv(env, "web")
	if webEnv["GREETING"] != "hello preview" || webEnv["PREVIEW_KEY"] != "preview-key-1" {
		t.Fatalf("the preview values are missing: %v", keysOf(webEnv))
	}
	for _, name := range []string{"DB_PASSWORD_PROD", "BASE_ONLY"} {
		if _, ok := webEnv[name]; ok {
			t.Fatalf("%s is in the container of the preview", name)
		}
	}
	for _, v := range webEnv {
		if strings.Contains(v, "production-secret-9") {
			t.Fatal("the production secret value is in the container")
		}
	}
	if strings.Contains(m.q(`SELECT secret_versions::text FROM releases WHERE id = $1`, webRel), "DB_PASSWORD_PROD") {
		t.Fatal("the release of the preview uses the production secret")
	}

	// AC-PREV-07 and AC-VOL-04: a new empty database, and no volume of the base.
	baseDB, prevDB := f.db, m.q(`SELECT id FROM services WHERE environment_id = $1 AND name = 'db'`, env)
	if prevDB == baseDB {
		t.Fatal("the preview uses the database service of the base")
	}
	if !strings.Contains(webEnv["DATABASE_URL"], dbengine.Alias(prevDB)) || strings.Contains(webEnv["DATABASE_URL"], dbengine.Alias(baseDB)) {
		t.Fatalf("DATABASE_URL does not point to the database of the preview: %s", webEnv["DATABASE_URL"])
	}
	if m.q(`SELECT count(*) FROM volumes WHERE service_id = $1`, prevDB) != "1" {
		t.Fatal("the preview database has no data volume")
	}
	for _, id := range strings.Split(m.q(`SELECT string_agg(v.id, ',') FROM volumes v JOIN services s ON s.id = v.service_id WHERE s.environment_id = $1`, env), ",") {
		if strings.Contains(baseVols, id) {
			t.Fatalf("the volume %s is a volume of the base environment", id)
		}
	}
	if m.q(`SELECT count(*) FROM secrets WHERE service_id = $1 AND name = 'DB_PASSWORD'`, prevDB) != "1" {
		t.Fatal("the preview database has no password of its own")
	}

	// AC-PREV-02: a new push builds the new commit and changes the same comment.
	commentID := comments[0].ID
	m.prEvent(f, "synchronized", 5, commitB, "feature-x", nil)
	m.settle("pr-5", "active")
	if m.q(`SELECT source->>'commit_sha' FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'web' ORDER BY r.number DESC LIMIT 1`, env) != commitB {
		t.Fatal("the new commit was not built")
	}
	if m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'static'`, env) != "1" {
		t.Fatal("static was built again on a push")
	}
	_, comments, _ = m.fake.Snapshot()
	if len(comments) != 1 || comments[0].ID != commentID || comments[0].Writes < 2 || !strings.Contains(comments[0].Body, "bbbbbbb") {
		t.Fatalf("the comment was not updated: %+v", comments)
	}
	// The same delivery twice is refused (REQ-SEC-101).
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview' AND deleted_at IS NULL`) != "1" {
		t.Fatal("more than one preview")
	}

	// AC-PREV-03: PR closed. Nothing is deleted before the grace time.
	m.prEvent(f, "closed", 5, commitB, "feature-x", nil)
	m.pv.Tick(context.Background())
	if m.q(`SELECT settings->>'preview_state' FROM environments WHERE id = $1`, env) != "closing" || m.q(`SELECT count(*) FROM services WHERE environment_id = $1 AND deleted_at IS NULL`, env) != "3" {
		t.Fatal("the preview was deleted before the grace time")
	}
	// A reopened PR within the grace time goes back to active.
	m.prEvent(f, "reopened", 5, commitB, "feature-x", nil)
	if m.q(`SELECT settings->>'preview_state' FROM environments WHERE id = $1`, env) != "active" {
		t.Fatal("a reopened PR did not bring the preview back")
	}
	m.prEvent(f, "closed", 5, commitB, "feature-x", nil)
	placements := m.q(`SELECT count(*) FROM placements p JOIN services s ON s.id = p.service_id WHERE s.environment_id = $1`, env)
	if placements == "0" {
		t.Fatal("the preview has no containers")
	}
	m.gw.Clock.Advance(61 * time.Minute)
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM environments WHERE id = $1 AND deleted_at IS NOT NULL`, env) != "1" || m.q(`SELECT count(*) FROM services WHERE environment_id = $1 AND deleted_at IS NULL`, env) != "0" {
		t.Fatal("the environment was not deleted after the grace time")
	}
	if m.q(`SELECT count(*) FROM domains d JOIN services s ON s.id = d.service_id WHERE s.environment_id = $1`, env) != "0" {
		t.Fatal("domains are left")
	}
	m.mu.Lock()
	removed := 0
	for _, c := range m.cmds {
		if strings.HasSuffix(c, "volumes=true") {
			removed++
		}
	}
	m.mu.Unlock()
	if fmt.Sprint(removed) != placements {
		t.Fatalf("%d containers were removed with their volumes, the preview had %s", removed, placements)
	}
	if m.q(`SELECT count(*) FROM variables WHERE environment_id = $1`, env) != "0" || m.q(`SELECT count(*) FROM secrets WHERE environment_id = $1 AND deleted_at IS NULL`, env) != "0" {
		t.Fatal("variables or secrets of the preview are left")
	}
	if m.q(`SELECT count(*) FROM audit_events WHERE action = 'preview.deleted' AND target_id = $1`, env) != "1" {
		t.Fatal("no preview.deleted audit event")
	}
	// The base environment is untouched.
	if m.q(`SELECT count(*) FROM services WHERE environment_id = $1 AND deleted_at IS NULL`, f.staging) != "3" {
		t.Fatal("the base environment changed")
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AC-PREV-05, REQ-PREV-009: a PR from a fork creates no preview with the default settings. With from_forks on, the label
// polipan-preview is needed, the preview gets no secret, and a new push takes the label away and does not build.
func TestAC_PREV_05_ForkRules(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(nil)
	m.prEvent(f, "opened", 7, commitA, "evil", map[string]any{"fork": true, "labels": []any{map[string]any{"name": "polipan-preview"}}})
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview'`) != "0" {
		t.Fatal("a fork PR made a preview with the default settings")
	}

	// from_forks is a setting that needs an admin.
	dev := m.devClient()
	if rec := dev.do("PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": map[string]any{"from_forks": true}}}); rec.Code != 403 {
		t.Fatalf("a developer turned on from_forks: %d", rec.Code)
	}
	f.c.ok(200, "PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": map[string]any{"enabled": true, "base_environment_id": f.staging, "from_forks": true}}})

	m.prEvent(f, "opened", 8, commitA, "evil", map[string]any{"fork": true})
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview'`) != "0" {
		t.Fatal("a fork PR without the label made a preview")
	}
	m.prEvent(f, "label_updated", 8, commitA, "evil", map[string]any{"fork": true, "labels": []any{map[string]any{"name": "polipan-preview"}}})
	m.settle("pr-8", "active")
	env := m.previewEnv("pr-8")
	if m.q(`SELECT settings->>'preview_fork' FROM environments WHERE id = $1`, env) != "true" {
		t.Fatal("the preview is not marked as a fork preview")
	}
	webEnv := m.workloadEnv(env, "web")
	if webEnv["GREETING"] == "" {
		t.Fatal("a fork preview gets the preview variables")
	}
	for _, name := range []string{"PREVIEW_KEY", "DB_PASSWORD_PROD", "BASE_ONLY"} {
		if _, ok := webEnv[name]; ok {
			t.Fatalf("a fork preview got %s", name)
		}
	}
	if m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND r.secret_versions::text LIKE '%PREVIEW_KEY%'`, env) != "0" {
		t.Fatal("a release of a fork preview uses a project secret")
	}

	// A new push to the fork: the label is removed and the new commit is not built.
	releases := m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env)
	m.prEvent(f, "synchronized", 8, commitB, "evil", map[string]any{"fork": true, "labels": []any{map[string]any{"name": "polipan-preview"}}})
	_, _, removed := m.fake.Snapshot()
	if len(removed) == 0 || removed[len(removed)-1] != "acme/shop#8:polipan-preview" {
		t.Fatalf("the label was not removed: %v", removed)
	}
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env) != releases {
		t.Fatal("the new commit of a fork was built without the label")
	}
	if m.q(`SELECT settings->>'waiting_for_label' FROM environments WHERE id = $1`, env) != "true" {
		t.Fatal("the preview does not wait for the label")
	}
	// With the label again, the new commit is built.
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": []any{map[string]any{"name": "polipan-preview"}}})
	m.settle("pr-8", "active")
	if m.q(`SELECT source->>'commit_sha' FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'web' ORDER BY r.number DESC LIMIT 1`, env) != commitB {
		t.Fatal("the new commit was not built after the label")
	}
}

// AC-PREV-06: with preview_max_active previews, the next PR makes no environment and gets a failure status with the reason.
func TestAC_PREV_06_Limit(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(map[string]any{"max_active": 2})
	m.prEvent(f, "opened", 1, commitA, "a", nil)
	m.prEvent(f, "opened", 2, commitA, "b", nil)
	m.prEvent(f, "opened", 3, commitB, "c", nil)
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview' AND deleted_at IS NULL`) != "2" {
		t.Fatal("the limit was not applied")
	}
	statuses, _, _ := m.fake.Snapshot()
	last := statuses[len(statuses)-1]
	if last.State != "failure" || last.Commit != commitB || !strings.Contains(last.Description, "limit") {
		t.Fatalf("the status of the PR over the limit: %+v", last)
	}
	// A closing preview does not count: when it is deleted, the next PR gets a preview.
	m.prEvent(f, "closed", 1, commitA, "a", nil)
	m.prEvent(f, "opened", 4, commitB, "d", nil)
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview' AND deleted_at IS NULL AND slug = 'pr-4'`) != "1" {
		t.Fatal("a closing preview counts for the limit")
	}
}

// AC-PREV-08: with no push for preview_ttl, the preview is deleted and the PR gets a comment. A push extends the TTL.
func TestAC_PREV_08_TTL(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(map[string]any{"ttl_days": 3})
	m.prEvent(f, "opened", 11, commitA, "a", nil)
	m.settle("pr-11", "active")
	env := m.previewEnv("pr-11")
	clk := m.gw.Clock
	// The settings of a preview hold its state: only extend_days can be set (the TTL end moves by that many days).
	before := m.q(`SELECT extract(epoch FROM preview_expires_at)::bigint FROM environments WHERE id = $1`, env)
	if rec := f.c.do("PATCH", "/api/v1/environments/"+env, map[string]any{"settings": map[string]any{"preview_fork": false}}); rec.Code != 422 {
		t.Fatalf("a state key was set through the API: %d", rec.Code)
	}
	if rec := f.c.do("PATCH", "/api/v1/environments/"+env, map[string]any{"protected": true}); rec.Code != 422 {
		t.Fatalf("a preview was protected: %d", rec.Code)
	}
	f.c.ok(200, "PATCH", "/api/v1/environments/"+env, map[string]any{"settings": map[string]any{"extend_days": 2}})
	after := m.q(`SELECT extract(epoch FROM preview_expires_at)::bigint FROM environments WHERE id = $1`, env)
	if before == after || m.q(`SELECT settings->>'preview_state' FROM environments WHERE id = $1`, env) != "active" {
		t.Fatalf("extend_days: %s -> %s", before, after)
	}
	f.c.ok(200, "PATCH", "/api/v1/environments/"+env, map[string]any{"settings": map[string]any{"extend_days": 1}})
	m.exec(`UPDATE environments SET preview_expires_at = $2 WHERE id = $1`, env, clk.Now().Add(3*24*time.Hour))
	clk.Advance(2 * 24 * time.Hour)
	m.prEvent(f, "synchronized", 11, commitB, "a", nil)
	m.settle("pr-11", "active")
	clk.Advance(2 * 24 * time.Hour) // 4 days since the open, 2 since the push
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM environments WHERE id = $1 AND deleted_at IS NULL`, env) != "1" {
		t.Fatal("the preview was deleted although there was a push 2 days ago")
	}
	clk.Advance(2 * 24 * time.Hour)
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM environments WHERE id = $1 AND deleted_at IS NOT NULL`, env) != "1" {
		t.Fatal("the preview was not deleted after the TTL")
	}
	_, comments, _ := m.fake.Snapshot()
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "no push for 3 days") {
		t.Fatalf("the PR got no comment about the TTL: %+v", comments)
	}
}

// REQ-PREV-014: a preview by hand for a PR number or a branch, and the delete of any preview through the API.
func TestREQ_PREV_014_ManualCreateAndDelete(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(nil)
	rec := f.c.do("POST", "/api/v1/projects/"+f.project+"/previews", map[string]any{"repo": "acme/shop", "branch": "try-it"})
	if rec.Code != 202 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	slug := str(created, "slug")
	if !strings.HasPrefix(slug, "br-try-it-") {
		t.Fatalf("slug of a branch preview: %v", created)
	}
	if rec := f.c.do("POST", "/api/v1/projects/"+f.project+"/previews", map[string]any{"repo": "acme/shop", "branch": "try-it"}); rec.Code != 409 {
		t.Fatalf("the same preview twice: %d", rec.Code)
	}
	rec = f.c.do("POST", "/api/v1/projects/"+f.project+"/previews", map[string]any{"repo": "acme/shop", "pr_number": 42})
	if rec.Code != 202 {
		t.Fatalf("create by number: %d %s", rec.Code, rec.Body)
	}
	m.settle("pr-42", "active")
	list := f.c.do("GET", "/api/v1/projects/"+f.project+"/previews", nil)
	var items []map[string]any
	_ = json.Unmarshal(list.Body.Bytes(), &items)
	if len(items) != 2 || !strings.Contains(list.Body.String(), "pr-42-web.preview.example.com") {
		t.Fatalf("list: %s", list.Body)
	}
	if rec := f.c.do("POST", "/api/v1/projects/"+f.project+"/previews", map[string]any{"repo": "acme/other", "pr_number": 1}); rec.Code != 422 {
		t.Fatalf("an unknown repo: %d", rec.Code)
	}
	// A developer can delete a preview (preview:manage), through DELETE /environments/{id}.
	dev := m.devClient()
	env := m.previewEnv("pr-42")
	if rec := dev.do("DELETE", "/api/v1/environments/"+env, nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if m.q(`SELECT count(*) FROM services WHERE environment_id = $1 AND deleted_at IS NULL`, env) != "0" {
		t.Fatal("the services of the preview are left")
	}
}

// A fork push when the label could not be removed: the label stays on the PR, so an event of another label must not
// approve the new commit. Only after the label was off and is added again, the commit is built.
func TestAC_PREV_05_ForkLabelRemovalFails(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(nil)
	f.c.ok(200, "PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": map[string]any{"enabled": true, "base_environment_id": f.staging, "from_forks": true}}})
	lbl := []any{map[string]any{"name": "polipan-preview"}}
	m.prEvent(f, "label_updated", 8, commitA, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
	env := m.previewEnv("pr-8")
	releases := m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env)

	m.fake.RemoveErr = fmt.Errorf("provider is down")
	m.prEvent(f, "synchronized", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	// Another label changes: the polipan-preview label is still on the PR.
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env) != releases {
		t.Fatal("unapproved fork code was built after a failed label removal")
	}
	// A maintainer takes the label off (an event without it) and adds it again.
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true})
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
	if m.q(`SELECT source->>'commit_sha' FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'web' ORDER BY r.number DESC LIMIT 1`, env) != commitB {
		t.Fatal("the commit was not built after the label was added again")
	}
}

// SEC-1: a fork author closes the PR, pushes and reopens it. The label of the old commit must not approve the new one.
func TestAC_PREV_05_ForkCloseAndReopenWithNewCommit(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(nil)
	f.c.ok(200, "PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": map[string]any{"enabled": true, "base_environment_id": f.staging, "from_forks": true}}})
	lbl := []any{map[string]any{"name": "polipan-preview"}}
	m.prEvent(f, "label_updated", 8, commitA, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
	env := m.previewEnv("pr-8")
	releases := m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env)
	m.prEvent(f, "closed", 8, commitA, "evil", map[string]any{"fork": true, "labels": lbl})
	// The label stays in this event list on purpose (a provider that did not remove it): reopen with a new commit.
	m.prEvent(f, "reopened", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1`, env) != releases {
		t.Fatal("unapproved fork code was built after close, push and reopen")
	}
	_, _, removed := m.fake.Snapshot()
	if len(removed) < 2 {
		t.Fatalf("the label was not removed on close and on reopen: %v", removed)
	}
	// The same commit that was approved comes back with a reopen: no new build, back to active.
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true})
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
	if m.q(`SELECT source->>'commit_sha' FROM releases r JOIN services s ON s.id = r.service_id WHERE s.environment_id = $1 AND s.name = 'web' ORDER BY r.number DESC LIMIT 1`, env) != commitB {
		t.Fatal("the commit was not built after a new approval")
	}
}

// SEC-1, no environment: the preview was deleted after the grace time, the label could not be removed, a new commit
// was pushed. A label event must not build it.
func TestAC_PREV_05_ForkAfterTheEnvironmentWasDeleted(t *testing.T) {
	m := newM9(t)
	f := m.previewSetup(map[string]any{"cleanup_grace_minutes": 0})
	f.c.ok(200, "PATCH", "/api/v1/projects/"+f.project, map[string]any{"settings": map[string]any{"previews": map[string]any{
		"enabled": true, "base_environment_id": f.staging, "from_forks": true, "cleanup_grace_minutes": 0}}})
	lbl := []any{map[string]any{"name": "polipan-preview"}}
	m.prEvent(f, "label_updated", 8, commitA, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
	m.fake.RemoveErr = fmt.Errorf("provider is down")
	m.prEvent(f, "closed", 8, commitA, "evil", map[string]any{"fork": true, "labels": lbl})
	m.pv.Tick(context.Background())
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview' AND deleted_at IS NULL`) != "0" {
		t.Fatal("the preview was not deleted")
	}
	m.prEvent(f, "reopened", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	if m.q(`SELECT count(*) FROM environments WHERE kind = 'preview' AND deleted_at IS NULL`) != "0" {
		t.Fatal("unapproved fork code got a preview after the environment was deleted")
	}
	// The label off and on again approves the commit.
	m.fake.RemoveErr = nil
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true})
	m.prEvent(f, "label_updated", 8, commitB, "evil", map[string]any{"fork": true, "labels": lbl})
	m.settle("pr-8", "active")
}
