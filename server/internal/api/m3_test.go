// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// AC-REL-06: a developer cannot roll back on a protected environment (403), an admin can. REQ-REL-010.
func TestAC_REL_06_RollbackNeedsAdminOnProtected(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	owner := e.owner()
	_, envs := owner.project("Shop")
	prod := owner.service(envs["production"], "web")
	staging := owner.service(envs["staging"], "web")
	dev := e.login("usr_dev", "dev@example.com", "developer")
	admin := e.login("usr_adm", "admin@example.com", "admin")
	body := map[string]any{"release_id": "rel_1"}

	rec := dev.do("POST", "/api/v1/services/"+prod+"/rollback", body)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "protected_environment") {
		t.Fatalf("developer on production: %d %s", rec.Code, rec.Body)
	}
	if len(e.orch.deploys) != 0 {
		t.Fatal("the rollback reached the engine")
	}
	admin.ok(202, "POST", "/api/v1/services/"+prod+"/rollback", body)
	dev.ok(202, "POST", "/api/v1/services/"+staging+"/rollback", body)
	if rec := dev.do("POST", "/api/v1/services/"+prod+"/deploy", nil); rec.Code != 403 {
		t.Fatalf("deploy on production: %d", rec.Code)
	}
}

// The same Idempotency-Key gives the same deployment and starts no second one. Another body with the key is refused.
func TestIdempotencyKeyOnDeploy(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	post := func(key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/v1/services/"+svc+"/deploy", strings.NewReader(string(raw)))
		req.RemoteAddr, req.Header = c.addr, make(map[string][]string)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(c.cookie)
		req.Header.Set("X-CSRF-Token", c.csrf)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	first := post("k-1", map[string]any{})
	second := post("k-1", map[string]any{})
	var a, b map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if first.Code != 202 || second.Code != 202 || str(a, "id") == "" || str(a, "id") != str(b, "id") || second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay: %d %d %s | %s", first.Code, second.Code, first.Body, second.Body)
	}
	if len(e.orch.deploys) != 1 {
		t.Fatalf("deploys: %d", len(e.orch.deploys))
	}
	if rec := post("k-1", map[string]any{"strategy": "recreate"}); rec.Code != 409 || !strings.Contains(rec.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("other body: %d %s", rec.Code, rec.Body)
	}
	if third := post("k-2", map[string]any{}); third.Code != 202 || len(e.orch.deploys) != 2 {
		t.Fatalf("new key: %d %d", third.Code, len(e.orch.deploys))
	}
}

// REQ-API-103: the SSE stream of a deployment resumes after Last-Event-ID.
func TestDeploymentEventsResume(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	dep := c.ok(202, "POST", "/api/v1/services/"+svc+"/deploy", nil)
	id := str(dep, "id")
	ctx := context.Background()
	for _, st := range []string{"image_ready", "starting", "live"} {
		payload, _ := json.Marshal(map[string]any{"type": "state", "state": st, "message": st + " now"})
		if _, err := e.pool.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) SELECT id, 'deployment', $1, $2 FROM orgs LIMIT 1`, id, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE deployments SET state = 'live' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	read := func(lastID string) (ids []string, body string) {
		req := httptest.NewRequest("GET", "/api/v1/deployments/"+id+"/events", nil)
		req.RemoteAddr = c.addr
		req.AddCookie(c.cookie)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream: %d %s", rec.Code, rec.Header().Get("Content-Type"))
		}
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(line, "id: ") {
				ids = append(ids, strings.TrimPrefix(line, "id: "))
			}
		}
		return ids, rec.Body.String()
	}
	ids, body := read("")
	if len(ids) != 3 || !strings.Contains(body, `"state":"live"`) || !strings.Contains(body, "event: state") {
		t.Fatalf("full stream: %v\n%s", ids, body)
	}
	rest, body := read(ids[1]) // after the second event: only the third comes
	if len(rest) != 1 || !strings.Contains(body, `"state":"live"`) || strings.Contains(body, "starting") {
		t.Fatalf("resumed stream: %v\n%s", rest, body)
	}
}

// F02: list with status and fast rollback flag, a note (audited), and the diff of two releases.
func TestReleasesListNoteDiff(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	ctx := context.Background()
	for n, q := range []string{
		`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source, pruned_at)
		 VALUES ('rel_a', $1, 1, 'nginx@sha256:aa', 'sha256:aa', '{"port":80}', '{"A":"1"}', '{"S":1}', '{"type":"image"}', now())`,
		`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source)
		 VALUES ('rel_b', $1, 2, 'nginx@sha256:bb', 'sha256:bb', '{"port":8080}', '{"A":"2"}', '{"S":2}', '{"type":"image"}')`,
	} {
		if _, err := e.pool.Exec(ctx, q, svc); err != nil {
			t.Fatal(n, err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE services SET live_release_id = 'rel_b' WHERE id = $1`, svc); err != nil {
		t.Fatal(err)
	}
	list := c.ok(200, "GET", "/api/v1/services/"+svc+"/releases", nil)
	items := list["items"].([]any)
	if len(items) != 2 || str(items[0].(map[string]any), "status") != "live" || str(items[1].(map[string]any), "status") != "pruned" {
		t.Fatalf("list: %v", list)
	}
	rel := c.ok(200, "PATCH", "/api/v1/releases/rel_b", map[string]any{"note": "the good one"})
	if str(rel, "note") != "the good one" {
		t.Fatalf("note: %v", rel)
	}
	if e.count(`SELECT count(*) FROM events WHERE topic = 'audit' AND payload->>'action' = 'release.annotate'`) != 1 {
		t.Fatal("the note was not audited")
	}
	diff := c.ok(200, "GET", "/api/v1/releases/rel_a/diff/rel_b", nil)
	raw, _ := json.Marshal(diff)
	for _, want := range []string{`"image_digest"`, `"config.port"`, `"variables.A"`, `"secrets.S"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("diff has no %s: %s", want, raw)
		}
	}
}
