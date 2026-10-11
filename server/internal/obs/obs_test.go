// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// REQ-OBS-100: the server sets the org, project and environment labels from its own data. An agent cannot write
// for a service that is not on its node, and cannot name another org.
func TestIngestSetsTenantLabelsFromTheDatabase(t *testing.T) {
	in := &Ingest{}
	sc := &nodeScope{svc: map[string]svcScope{"svc_mine": {org: "org_1", project: "prj_1", env: "env_1"}}}
	now := time.Now().UnixMilli()
	series := []rwSeries{
		{Labels: map[string]string{"__name__": "polipan_container_cpu_usage_millis", "service": "svc_mine", "org": "org_evil", "project": "prj_evil", "path": "/secret"},
			Samples: []rwSample{{Value: 5, TS: now}}},
		{Labels: map[string]string{"__name__": "polipan_container_cpu_usage_millis", "service": "svc_other", "org": "org_2"}, Samples: []rwSample{{Value: 9, TS: now}}},
		{Labels: map[string]string{"__name__": "polipan_deployments_total", "service": "svc_mine"}, Samples: []rwSample{{Value: 1, TS: now}}},
		{Labels: map[string]string{"__name__": "node_cpu_seconds_total"}, Samples: []rwSample{{Value: 1, TS: now}}},
		{Labels: map[string]string{"__name__": "polipan_node_load1", "org": "org_evil", "node": "nod_other"}, Samples: []rwSample{{Value: 0.5, TS: now}}},
	}
	text, dropped := in.cleanSeries("nod_1", sc, series)
	if dropped != 3 {
		t.Fatalf("dropped %d series", dropped)
	}
	for _, bad := range []string{"org_evil", "org_2", "svc_other", "/secret", "nod_other", "deployments_total"} {
		if strings.Contains(text, bad) {
			t.Fatalf("the text has %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{`org="org_1"`, `project="prj_1"`, `environment="env_1"`, `service="svc_mine"`, `node="nod_1"`, "polipan_node_load1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the text has no %q:\n%s", want, text)
		}
	}
}

// REQ-OBS-101: the text of a user is only ever a string literal. It cannot add LogsQL.
func TestLogQueryEscapesTheText(t *testing.T) {
	f := LogFilter{OrgID: "org_1", EnvironmentID: "env_prod", Text: `timeout" or {org="org_2"} | delete`}
	q, err := f.Query()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q, `{org="org_1",environment="env_prod"}`) {
		t.Fatalf("query %s", q)
	}
	if strings.Contains(q, `org="org_2"`) && !strings.Contains(q, `\"`) {
		t.Fatalf("the text broke out: %s", q)
	}
	if _, err := (LogFilter{Text: "x"}).Query(); err == nil {
		t.Fatal("a query with no org")
	}
	if _, err := (LogFilter{OrgID: `org_1"} or {org="org_2`}).Query(); err == nil {
		t.Fatal("an org id with a quote")
	}
	if _, err := (LogFilter{OrgID: "org_1", Level: "x or y"}).Query(); err == nil {
		t.Fatal("a bad level")
	}
	re, err := (LogFilter{OrgID: "org_1", Text: "/time(out|d)/"}).Query()
	if err != nil || !strings.HasSuffix(re, `~"time(out|d)"`) {
		t.Fatalf("regex %s %v", re, err)
	}
	if _, err := (LogFilter{OrgID: "org_1", Text: "/(/"}).Query(); err == nil {
		t.Fatal("a bad regex")
	}
}

// AC-OBS-05: a range of 60 days has at most 1500 points.
func TestAC_OBS_05_StepKeepsPointsUnder1500(t *testing.T) {
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 60 * 24 * time.Hour, 365 * 24 * time.Hour} {
		step := ChooseStep(to.Add(-d), to, 0)
		if step < ScrapeInterval {
			t.Fatalf("%s: step %s is below the scrape interval", d, step)
		}
		if n := int(d / step); n > MaxPoints {
			t.Fatalf("%s: %d points with step %s", d, n, step)
		}
	}
	// AC-OBS-03: the last hour has points at about the 15 s resolution.
	if step := ChooseStep(to.Add(-time.Hour), to, 0); step != 15*time.Second {
		t.Fatalf("1 h step: %s", step)
	}
	// A client cannot ask for a step below the scrape interval.
	if step := ChooseStep(to.Add(-time.Hour), to, time.Second); step < ScrapeInterval {
		t.Fatalf("requested 1 s: %s", step)
	}
}

// The metric queries always carry the org of the service (REQ-OBS-100).
func TestServiceMetricQueriesCarryTheOrg(t *testing.T) {
	for _, name := range MetricNames {
		if name == "load" {
			continue
		}
		expr, err := Expr(name, Scope{OrgID: "org_1", ServiceID: "svc_1"}, time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(expr, `org="org_1"`) || !strings.Contains(expr, `service="svc_1"`) {
			t.Fatalf("%s has no tenant labels: %s", name, expr)
		}
	}
	if _, err := Expr("cpu", Scope{OrgID: `x"} or {org="y`, ServiceID: "svc"}, time.Minute); err == nil {
		t.Fatal("a bad org id was accepted")
	}
}

// The first lines of a new container can come before the cached scope of the node knows its service. The ingest
// reads the scope again, so the lines are not given up (REQ-OBS-102: no loss).
func TestIngestReadsTheScopeAgainForANewService(t *testing.T) {
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO orgs (id, name, slug) VALUES ('org_1','A','a')`,
		`INSERT INTO projects (id, org_id, name, slug) VALUES ('prj_1','org_1','p','p')`,
		`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ('env_1','prj_1','prod','prod','production')`,
		`INSERT INTO nodes (id, org_id, name, state) VALUES ('nod_1','org_1','n','ready')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	clk := clock.NewFake(time.Now())
	in := &Ingest{DB: pool, Clock: clk}
	sc, err := in.scope(ctx, "nod_1")
	if err != nil || len(sc.svc) != 0 {
		t.Fatalf("scope %v %v", sc, err)
	}
	for _, q := range []string{
		`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ('svc_1','env_1','web','app','image','{}')`,
		`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source)
		   VALUES ('rel_1','svc_1',1,'x@sha256:a','sha256:a','{}','{}','{}','{}')`,
		`INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role) VALUES ('plc_1','svc_1','nod_1','rel_1',0,'blue','live')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if in.fresh(ctx, "nod_1", sc) != nil {
		t.Fatal("the scope was read again at once: a flood of unknown services would hit the database")
	}
	clk.Advance(2 * time.Second)
	fresh := in.fresh(ctx, "nod_1", sc)
	if fresh == nil || fresh.svc["svc_1"].org != "org_1" || fresh.svc["svc_1"].project != "prj_1" {
		t.Fatalf("fresh scope %+v", fresh)
	}
	line := `{"_time":"2026-10-10T10:00:00Z","_msg":"first line","service":"svc_1"}` + "\n"
	if out, dropped, _ := in.cleanLogs("nod_1", sc, strings.NewReader(line)); dropped != 1 || out.Len() != 0 {
		t.Fatal("the old scope should drop the line")
	}
	if out, dropped, _ := in.cleanLogs("nod_1", fresh, strings.NewReader(line)); dropped != 0 || !strings.Contains(out.String(), `"org":"org_1"`) {
		t.Fatalf("the fresh scope: %d dropped, %s", dropped, out)
	}
}
