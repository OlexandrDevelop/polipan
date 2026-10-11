// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/alert"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// CanaryAction of the fake orchestrator: only a deployment in the state shifting is a running canary.
func (f *fakeOrch) CanaryAction(ctx context.Context, depID, action, _ string) error {
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1`, depID).Scan(&state); err != nil {
		return orchestrator.ErrNotFound
	}
	if state != "shifting" {
		return orchestrator.ErrNotCanary
	}
	f.mu.Lock()
	f.canary = append(f.canary, action+":"+depID)
	f.mu.Unlock()
	return nil
}

// stores are fake VictoriaMetrics and VictoriaLogs. They keep the requests.
type stores struct {
	mu    sync.Mutex
	logQ  []url.Values
	metQ  []url.Values
	lines string
}

func (s *stores) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/select/logsql/query":
			s.logQ = append(s.logQ, r.PostForm)
			_, _ = io.WriteString(w, s.lines)
		case "/api/v1/query_range":
			s.metQ = append(s.metQ, r.PostForm)
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1760000000,"1"],[1760000015,"2"]]}]}}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *stores) lastLog() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.logQ) == 0 {
		return nil
	}
	return s.logQ[len(s.logQ)-1]
}

func newM8Env(t *testing.T) (*testEnv, *stores) {
	t.Helper()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.NewManager(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	st := &stores{}
	srv := httptest.NewServer(st.handler())
	t.Cleanup(srv.Close)
	backends := obs.NewBackends(srv.URL, srv.URL, "", "")
	orch := &fakeOrch{pool: pool}
	clk := clock.NewFake(time.Now())
	eng := &alert.Engine{DB: pool, Store: &alert.Store{DB: pool, Secrets: sec}, Clock: clk, Backends: backends}
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: pool, Clock: clk, RateLimit: 100000,
		Pool: pool, Orchestrator: orch, ExternalURL: "http://localhost", AgentListen: ":9443", SetupToken: "setup-secret", KEK: testKEK, Secrets: sec,
		Obs: backends, Alerts: eng, Monitoring: api.MonitoringSettings{MetricsRetentionDays: 365, LogsRetentionDays: 90},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, h: h, pool: pool, orch: orch, clk: clk}, st
}

// The org of the owner, and a second org with a service of its own.
func (e *testEnv) otherOrgService(t *testing.T) (orgID, serviceID, nodeID string) {
	t.Helper()
	orgID, serviceID, nodeID = "org_other", "svc_other", "nod_other"
	for _, q := range []string{
		`INSERT INTO orgs (id, name, slug) VALUES ('org_other','Other','other')`,
		`INSERT INTO projects (id, org_id, name, slug) VALUES ('prj_other','org_other','x','x')`,
		`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ('env_other','prj_other','prod','prod','production')`,
		`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ('svc_other','env_other','web','app','image','{}')`,
		`INSERT INTO nodes (id, org_id, name, state) VALUES ('nod_other','org_other','n','ready')`,
	} {
		if _, err := e.pool.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func (e *testEnv) orgOf(t *testing.T) string {
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT id FROM orgs ORDER BY created_at LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// AC-OBS-06 and REQ-OBS-100: the search always carries the org of the user. Ids of another org are 404.
// The text of the user stays a string literal.
func TestM8_LogSearchIsScopedToTheOrg(t *testing.T) {
	e, st := newM8Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["production"], "web")
	_, otherSvc, _ := e.otherOrgService(t)
	org := e.orgOf(t)
	st.lines = `{"_time":"2026-10-10T10:00:00Z","_msg":"timeout while calling db","level":"error","stream":"stderr","service":"` + svc + `","environment":"` + envs["production"] + `","project":"` + prj + `","node":"nod_1"}` + "\n"

	out := c.ok(200, "GET", "/api/v1/logs/search?q="+url.QueryEscape(`timeout" or {org="org_other"}`)+"&environment_id="+envs["production"], nil)
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items: %v", out)
	}
	q := st.lastLog().Get("query")
	if !strings.HasPrefix(q, `{org="`+org+`",project="`+prj+`",environment="`+envs["production"]+`"}`) {
		t.Fatalf("query %s", q)
	}
	if strings.Contains(strings.ReplaceAll(q, `\"`, ""), `{org="org_other"}`) {
		t.Fatalf("the text broke out of its literal: %s", q)
	}
	c.ok(404, "GET", "/api/v1/logs/search?service_id="+otherSvc, nil)
	c.ok(404, "GET", "/api/v1/logs/search?environment_id=env_other", nil)
	c.ok(404, "GET", "/api/v1/services/"+otherSvc+"/logs", nil)
	// AC-OBS-07: the start of the range is cut to the retention of the org (30 days by default).
	c.ok(200, "GET", "/api/v1/logs/search?from="+url.QueryEscape("2020-01-01T00:00:00Z"), nil)
	start, err := time.Parse(time.RFC3339Nano, st.lastLog().Get("start"))
	if err != nil || time.Since(start) > 31*24*time.Hour || time.Since(start) < 29*24*time.Hour {
		t.Fatalf("start %q %v", st.lastLog().Get("start"), err)
	}
	// The log lines of a service are read from the store, newest last.
	lines := c.do("GET", "/api/v1/services/"+svc+"/logs", nil)
	if lines.Code != 200 || !strings.Contains(lines.Body.String(), "timeout while calling db") {
		t.Fatalf("service logs: %d %s", lines.Code, lines.Body)
	}
}

// REQ-OBS-012: a retention above the store limit is refused, a shorter one needs a confirm.
func TestM8_RetentionRules(t *testing.T) {
	e, _ := newM8Env(t)
	c := e.owner()
	if rec := c.do("PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"logs_retention_days": 400}}); rec.Code != 422 {
		t.Fatalf("above the limit: %d %s", rec.Code, rec.Body)
	}
	c.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"logs_retention_days": 60}}) // longer than 30: no confirm
	rec := c.do("PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"logs_retention_days": 20}})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "retention_shrink_needs_confirm") {
		t.Fatalf("shrink: %d %s", rec.Code, rec.Body)
	}
	out := c.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"logs_retention_days": 20, "confirm_retention_shrink": true}})
	settings, _ := out["settings"].(map[string]any)
	if settings["logs_retention_days"] != float64(20) {
		t.Fatalf("settings %v", settings)
	}
	var raw string
	_ = e.pool.QueryRow(context.Background(), `SELECT settings::text FROM orgs LIMIT 1`).Scan(&raw)
	if strings.Contains(raw, "confirm_retention_shrink") {
		t.Fatalf("the confirm flag was stored: %s", raw)
	}
	c.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"logs_retention_days": 40}}) // longer: no confirm
}

// REQ-OBS-100: a metrics query has the org label. A node of another org is not found.
func TestM8_MetricsAreScopedToTheOrg(t *testing.T) {
	e, st := newM8Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["production"], "web")
	_, _, otherNode := e.otherOrgService(t)
	out := c.ok(200, "GET", "/api/v1/services/"+svc+"/metrics?metrics=cpu&metrics=requests&step=60s", nil)
	if series, _ := out["series"].([]any); len(series) != 2 {
		t.Fatalf("series: %v", out)
	}
	st.mu.Lock()
	for _, q := range st.metQ {
		if !strings.Contains(q.Get("query"), `org="`+e.orgOf(t)+`"`) || !strings.Contains(q.Get("query"), `service="`+svc+`"`) {
			t.Fatalf("query without tenant labels: %s", q.Get("query"))
		}
	}
	st.mu.Unlock()
	c.ok(404, "GET", "/api/v1/nodes/"+otherNode+"/metrics?metrics=cpu", nil)
	if rec := c.do("GET", "/api/v1/services/"+svc+"/metrics?metrics=nonsense", nil); rec.Code != 422 && rec.Code != 400 {
		t.Fatalf("unknown metric: %d", rec.Code)
	}
}

// REQ-ALERT-001: channel secrets are never returned. Only the roles with alert:manage may change channels.
func TestM8_AlertChannelsAndRules(t *testing.T) {
	e, _ := newM8Env(t)
	c := e.owner()
	dev := e.login("usr_dev", "dev@example.com", "developer")
	ch := c.ok(201, "POST", "/api/v1/alert-channels", map[string]any{"kind": "telegram", "name": "ops", "config": map[string]any{"chat_id": "42"}, "secret": "123:bot-token"})
	id, _ := ch["id"].(string)
	list := c.do("GET", "/api/v1/alert-channels", nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), "bot-token") || !strings.Contains(list.Body.String(), id) {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	if strings.Contains(e.queryString(`SELECT string_agg(config::text, ' ') FROM alert_channels`), "bot-token") {
		t.Fatal("the secret is in the config column")
	}
	if rec := dev.do("POST", "/api/v1/alert-channels", map[string]any{"kind": "telegram", "name": "x", "config": map[string]any{"chat_id": "1"}, "secret": "t"}); rec.Code != 403 {
		t.Fatalf("developer: %d", rec.Code)
	}
	if dev.do("GET", "/api/v1/alert-channels", nil).Code != 200 {
		t.Fatal("a developer cannot read the channels")
	}
	if rec := c.do("POST", "/api/v1/alert-channels", map[string]any{"kind": "webhook", "name": "bad", "config": map[string]any{"url": "ftp://x"}}); rec.Code != 422 {
		t.Fatalf("bad url: %d", rec.Code)
	}
	rule := c.ok(201, "POST", "/api/v1/alert-rules", map[string]any{"name": "disk", "type": "disk_high", "severity": "warning", "channel_ids": []string{id},
		"params": map[string]any{"threshold": 85, "for_seconds": 300}, "repeat_interval_seconds": 3600})
	rid, _ := rule["id"].(string)
	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	m := c.ok(200, "POST", "/api/v1/alert-rules/"+rid+"/mute", map[string]any{"until": until, "reason": "planned work"})
	if m["mute_reason"] != "planned work" || m["muted_until"] == nil {
		t.Fatalf("mute: %v", m)
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'alert_rule.mute'`) != 1 {
		t.Fatal("the mute is not audited")
	}
	if rec := c.do("POST", "/api/v1/alert-rules/"+rid+"/mute", map[string]any{"until": "2020-01-01T00:00:00Z", "reason": "x"}); rec.Code != 422 {
		t.Fatalf("mute in the past: %d", rec.Code)
	}
	c.ok(204, "DELETE", "/api/v1/alert-rules/"+rid, nil)
	c.ok(204, "DELETE", "/api/v1/alert-channels/"+id, nil)
	c.ok(404, "DELETE", "/api/v1/alert-channels/"+id, nil)
}

// REQ-DEPLOY-006: promote, abort, pause and resume answer 409 when the deployment is not a running canary.
func TestM8_CanaryActions(t *testing.T) {
	e, _ := newM8Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["production"], "web")
	dep := "dep_canary"
	for _, q := range []string{
		`INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state, canary_step) SELECT 'dep_canary', s.id, s.environment_id, 'manual', 'canary', 'shifting', 25 FROM services s WHERE s.id = '` + svc + `'`,
		`INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state) SELECT 'dep_live', s.id, s.environment_id, 'manual', 'recreate', 'live' FROM services s WHERE s.id = '` + svc + `'`,
	} {
		if _, err := e.pool.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"promote", "abort", "pause", "resume"} {
		out := c.ok(200, "POST", "/api/v1/deployments/"+dep+"/"+action, nil)
		if out["strategy"] != "canary" || out["canary_step"] != float64(25) {
			t.Fatalf("%s: %v", action, out)
		}
		rec := c.do("POST", "/api/v1/deployments/dep_live/"+action, nil)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "deployment_not_canary") {
			t.Fatalf("%s on a live deployment: %d %s", action, rec.Code, rec.Body)
		}
	}
	if len(e.orch.canary) != 4 {
		t.Fatalf("calls %v", e.orch.canary)
	}
	if json.Valid([]byte(`{}`)) && c.do("POST", "/api/v1/deployments/dep_none/abort", nil).Code != 404 {
		t.Fatal("an unknown deployment is not 404")
	}
}
