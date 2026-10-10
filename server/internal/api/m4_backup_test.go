// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/secrets"
)

// m4Env is the API with the real control side, the backup service and one fake agent that answers backup commands.
type m4Env struct {
	*testEnv
	gw    *gatewaytest.Env
	ctl   *control.Control
	bk    *backup.Service
	sec   *secrets.Manager
	agent *gatewaytest.Agent

	mu       sync.Mutex
	commands []*pb.Command
	// onBackup answers RunBackup. The default gives one good snapshot per target.
	onBackup  func(c *pb.RunBackup) *pb.CommandAck
	onCheck   func(c *pb.RunRestoreCheck) *pb.CommandAck
	onRestore func(c *pb.RestoreSnapshot) *pb.CommandAck
}

func okSnapshots(c *pb.RunBackup, fail map[string]bool) *pb.CommandAck {
	var snaps []map[string]any
	for _, t := range c.Targets {
		if fail[t.TargetId] {
			snaps = append(snaps, map[string]any{"target_id": t.TargetId, "ok": false, "error": "the target does not answer"})
			continue
		}
		snaps = append(snaps, map[string]any{"target_id": t.TargetId, "snapshot_id": "snap" + t.TargetId[len(t.TargetId)-4:], "size_bytes": 1234, "ok": true})
	}
	res, _ := json.Marshal(map[string]any{"snapshots": snaps, "manifest": map[string]any{"engine": c.Source.Engine, "tables": 3, "rows": 40}, "duration_ms": 5})
	return &pb.CommandAck{Ok: true, ResultJson: res}
}

func newM4Env(t *testing.T) *m4Env {
	t.Helper()
	g := gatewaytest.Start(t)
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	sec, err := secrets.NewManager(g.Pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	ctl := control.New(control.Deps{DB: g.Pool, Clock: g.Clock, Config: g.Cfg, CA: g.CA, Sender: g.GW, Secrets: sec})
	t.Cleanup(ctl.Close)
	g.GW.State = ctl
	bk := backup.New(backup.Deps{DB: g.Pool, Secrets: sec, Sender: g.GW, Orch: ctl, Clock: g.Clock, Log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		LocalRoot: "/var/lib/polipan/backups"})
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: g.Pool, Clock: g.Clock, RateLimit: 100000,
		Pool: g.Pool, Orchestrator: ctl, ExternalURL: "http://localhost", AgentListen: ":9443", SetupToken: "setup-secret", Secrets: sec, Backup: bk,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &m4Env{testEnv: &testEnv{t: t, h: h, pool: g.Pool, clk: g.Clock}, gw: g, ctl: ctl, bk: bk, sec: sec}
	e.agent = g.NewReadyNode(t, "n1")
	e.agent.OnCommand = func(c *pb.Command) *pb.CommandAck {
		e.mu.Lock()
		e.commands = append(e.commands, c)
		onBackup, onCheck, onRestore := e.onBackup, e.onCheck, e.onRestore
		e.mu.Unlock()
		switch b := c.Body.(type) {
		case *pb.Command_PullImage:
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"postgres@sha256:abc123","digest":"sha256:abc123"}`)}
		case *pb.Command_RunBackup:
			if onBackup != nil {
				return onBackup(b.RunBackup)
			}
			return okSnapshots(b.RunBackup, nil)
		case *pb.Command_RunRestoreCheck:
			if onCheck != nil {
				return onCheck(b.RunRestoreCheck)
			}
			res, _ := json.Marshal(map[string]any{"passed": true, "checks": []map[string]any{{"name": "restore", "ok": true}, {"name": "table_count", "ok": true, "detail": "found 3, the backup run had 3"}}, "duration_ms": 12})
			return &pb.CommandAck{Ok: true, ResultJson: res}
		case *pb.Command_RestoreSnapshot:
			if onRestore != nil {
				return onRestore(b.RestoreSnapshot)
			}
			return &pb.CommandAck{Ok: true, ResultJson: []byte("{}")}
		case *pb.Command_TestBackupTarget:
			return &pb.CommandAck{Ok: true, ResultJson: []byte("{}")}
		}
		return &pb.CommandAck{Ok: true}
	}
	return e
}

func (e *m4Env) sent(match func(*pb.Command) bool) []*pb.Command {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*pb.Command
	for _, c := range e.commands {
		if match(c) {
			out = append(out, c)
		}
	}
	return out
}

// until runs ticks of the backup service until cond is true (the real loop does the same every 15 s).
func (e *m4Env) until(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		e.bk.Tick1(context.Background())
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("timeout: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *m4Env) runState(runID string) string {
	var s string
	_ = e.pool.QueryRow(context.Background(), `SELECT state FROM backup_runs WHERE id = $1`, runID).Scan(&s)
	return s
}

func (e *m4Env) target(c *client, name string) string {
	e.t.Helper()
	t := c.ok(201, "POST", "/api/v1/backup-targets", map[string]any{"name": name, "kind": "s3",
		"config": map[string]any{"endpoint": "http://minio:9000", "bucket": "backups", "path": "pp"}, "credentials": map[string]string{"access_key_id": "AKIAEXAMPLE1", "secret_access_key": "sekret-value-123456"}})
	return str(t, "id")
}

// dbService makes a database and deploys it.
func (e *m4Env) dbService(c *client, envID, name string) string {
	e.t.Helper()
	rec := c.createDB(envID, name, nil)
	var db map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &db)
	id := str(db, "id")
	m2 := &m2Env{testEnv: e.testEnv, gw: e.gw, ctl: e.ctl, sec: e.sec, agent: e.agent}
	if _, state, reason := m2.deploy(c, id); state != "live" {
		e.t.Fatalf("deploy %s: %s %s", name, state, reason)
	}
	return id
}

func (e *m4Env) policy(c *client, serviceID string, body map[string]any) map[string]any {
	e.t.Helper()
	return c.ok(201, "POST", "/api/v1/services/"+serviceID+"/backup-policies", body)
}

func (e *m4Env) alerts(rule string) int {
	return e.count(`SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' = $1`, rule)
}

// AC-BKP-01, AC-BKP-03 and the list of REQ-BKP-008: a manual run with two targets lists both snapshots.
func TestAC_BKP_01_03_ManualRunTwoTargets(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	a, b := e.target(c, "minio"), e.target(c, "offsite")
	pol := e.policy(c, db, map[string]any{"schedule": "0 2 * * *", "target_ids": []string{a, b}, "retention": map[string]int{"keep_daily": 7}})
	pid := str(pol, "id")
	if pol["engine"] != "postgres" || pol["health"] != "unverified" || pol["restore_check_enabled"] != true {
		t.Fatalf("policy: %v", pol)
	}
	// Only the rules that were given are used (AC-BKP-05 needs keep_daily alone).
	ret := pol["retention"].(map[string]any)
	if ret["keep_daily"] != float64(7) || ret["keep_weekly"] != float64(0) || ret["keep_monthly"] != float64(0) {
		t.Fatalf("retention: %v", ret)
	}
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	runID := str(run, "id")
	if run["state"] != "queued" || run["trigger"] != "manual" {
		t.Fatalf("run: %v", run)
	}
	e.until("run succeeded", func() bool { return e.runState(runID) == "succeeded" })
	list := c.ok(200, "GET", "/api/v1/backup-policies/"+pid+"/runs", nil)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("runs: %v", list)
	}
	got := items[0].(map[string]any)
	snaps := got["snapshots"].([]any)
	if got["state"] != "succeeded" || len(snaps) != 2 || got["duration_ms"] == nil || got["manifest"] == nil || snaps[0].(map[string]any)["snapshot_id"] == "" {
		t.Fatalf("run list: %v", got)
	}
	// The command carried both targets, the password of the policy, the retention, and no password of the database.
	cmds := e.sent(func(c *pb.Command) bool { return c.GetRunBackup() != nil })
	if len(cmds) != 1 {
		t.Fatalf("commands: %d", len(cmds))
	}
	rb := cmds[0].GetRunBackup()
	if len(rb.Targets) != 2 || rb.RepoPassword == "" || rb.KeepDaily != 7 || rb.KeepWeekly != 0 || rb.Source.Engine != "postgres" || !strings.HasPrefix(rb.Source.Container, "pp-plc_") && !strings.HasPrefix(rb.Source.Container, "pp-") {
		t.Fatalf("command: %v", rb)
	}
	if !strings.HasPrefix(rb.Targets[0].Repository, "s3:http://minio:9000/backups/pp/"+pid) || rb.Targets[0].Env["AWS_ACCESS_KEY_ID"] != "AKIAEXAMPLE1" {
		t.Fatalf("target: %v", rb.Targets[0])
	}
	// After the first good run a restore check runs by itself (REQ-BKP-100) and the policy turns verified (AC-BKP-07 plumbing).
	e.until("check passed", func() bool {
		return e.count(`SELECT count(*) FROM restore_checks WHERE policy_id = $1 AND state = 'passed' AND trigger = 'first_backup'`, pid) == 1
	})
	pol = c.ok(200, "GET", "/api/v1/backup-policies/"+pid, nil)
	if pol["health"] != "verified" || pol["last_restore_check"] == nil || pol["check_overdue"] != false {
		t.Fatalf("policy after check: %v", pol)
	}
	checks := c.ok(200, "GET", "/api/v1/restore-checks?policy_id="+pid, nil)
	ci := checks["items"].([]any)[0].(map[string]any)
	if ci["state"] != "passed" || len(ci["checks"].([]any)) != 2 {
		t.Fatalf("checks: %v", checks)
	}
	if e.alerts("backup_failed") != 0 || e.alerts("restore_check_failed") != 0 {
		t.Fatal("an alert fired for a good run")
	}
	// The credentials are never returned and live in a hidden project (REQ-BKP-004).
	for _, path := range []string{"/api/v1/backup-targets", "/api/v1/backup-policies/" + pid, "/api/v1/backup-policies"} {
		rec := c.do("GET", path, nil)
		if strings.Contains(rec.Body.String(), "sekret-value") || strings.Contains(rec.Body.String(), "AKIAEXAMPLE1") || strings.Contains(rec.Body.String(), "repo_password") {
			t.Fatalf("%s leaks: %s", path, rec.Body)
		}
	}
	if projects := c.do("GET", "/api/v1/projects", nil); strings.Contains(projects.Body.String(), "polipan-system") {
		t.Fatalf("the system project is listed: %s", projects.Body)
	}
}

// AC-BKP-02: one of two targets fails: the run is failed, the other target has its snapshot, an alert fires.
func TestAC_BKP_02_OneTargetFails(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	a, b := e.target(c, "minio"), e.target(c, "offsite")
	e.onBackup = func(rb *pb.RunBackup) *pb.CommandAck { return okSnapshots(rb, map[string]bool{b: true}) }
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{a, b}}), "id")
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("run failed", func() bool { return e.runState(str(run, "id")) == "failed" })
	got := c.ok(200, "GET", "/api/v1/backup-policies/"+pid+"/runs", nil)["items"].([]any)[0].(map[string]any)
	snaps := got["snapshots"].([]any)
	if len(snaps) != 2 || snaps[0].(map[string]any)["ok"] != true || snaps[1].(map[string]any)["ok"] != false || !strings.Contains(str(got, "error"), "target failed") {
		t.Fatalf("run: %v", got)
	}
	if e.alerts("backup_failed") != 1 {
		t.Fatalf("alerts: %d", e.alerts("backup_failed"))
	}
	if pol := c.ok(200, "GET", "/api/v1/backup-policies/"+pid, nil); pol["health"] != "failing" {
		t.Fatalf("health: %v", pol["health"])
	}
	// No restore check runs for a failed run.
	if e.count(`SELECT count(*) FROM restore_checks`) != 0 {
		t.Fatal("a check was queued after a failed run")
	}
}

// REQ-BKP-009: a node that is not connected fails the run with node_unavailable and an alert.
func TestBackupNodeUnavailable(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{e.target(c, "minio")}}), "id")
	e.gw.GW.Disconnect(e.agent.NodeID)
	deadline := time.Now().Add(5 * time.Second)
	for e.gw.GW.Connected(e.agent.NodeID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("run failed", func() bool { return e.runState(str(run, "id")) == "failed" })
	var msg string
	_ = e.pool.QueryRow(context.Background(), `SELECT error FROM backup_runs WHERE id = $1`, str(run, "id")).Scan(&msg)
	if !strings.HasPrefix(msg, "node_unavailable") || e.alerts("backup_failed") != 1 {
		t.Fatalf("error %q alerts %d", msg, e.alerts("backup_failed"))
	}
}

// AC-BKP-08: a failed restore check makes a critical alert.
func TestAC_BKP_08_FailedCheckAlert(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	e.onCheck = func(*pb.RunRestoreCheck) *pb.CommandAck {
		res, _ := json.Marshal(map[string]any{"passed": false, "checks": []map[string]any{{"name": "restore", "ok": false, "detail": "pg_restore: error: unexpected end of file"}}, "duration_ms": 3})
		return &pb.CommandAck{Ok: true, ResultJson: res}
	}
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{e.target(c, "minio")}}), "id")
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("check failed", func() bool {
		return e.runState(str(run, "id")) == "succeeded" && e.count(`SELECT count(*) FROM restore_checks WHERE state = 'failed'`) == 1
	})
	var sev, failed string
	if err := e.pool.QueryRow(context.Background(), `SELECT payload->>'severity', payload->>'failed' FROM events WHERE topic = 'alert' AND payload->>'rule' = 'restore_check_failed'`).Scan(&sev, &failed); err != nil {
		t.Fatalf("no alert: %v", err)
	}
	if sev != "critical" || !strings.Contains(failed, "unexpected end of file") {
		t.Fatalf("alert: %s %s", sev, failed)
	}
	if pol := c.ok(200, "GET", "/api/v1/backup-policies/"+pid, nil); pol["health"] != "failing" {
		t.Fatalf("health: %v", pol["health"])
	}
	// The restore check can be started on demand (REQ-BKP-106) and lists in /restore-checks.
	e.onCheck = nil
	chk := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/restore-checks", nil)
	e.until("on demand check passed", func() bool {
		var st string
		_ = e.pool.QueryRow(context.Background(), `SELECT state FROM restore_checks WHERE id = $1`, str(chk, "id")).Scan(&st)
		return st == "passed"
	})
}

// AC-BKP-10: no passed check in 2 x the check interval gives the warning on the policy and an alert.
func TestAC_BKP_10_CheckOverdue(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{e.target(c, "minio")}, "restore_check_schedule": "0 3 * * 0"}), "id")
	if pol := c.ok(200, "GET", "/api/v1/backup-policies/"+pid, nil); pol["check_overdue"] != false {
		t.Fatalf("a new policy is overdue: %v", pol)
	}
	// 15 days without a passed check is more than 2 x 7 days.
	if _, err := e.pool.Exec(context.Background(), `UPDATE backup_policies SET created_at = now() - interval '15 days' WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	e.bk.Tick1(context.Background())
	pol := c.ok(200, "GET", "/api/v1/backup-policies/"+pid, nil)
	if pol["check_overdue"] != true || e.alerts("restore_check_overdue") != 1 {
		t.Fatalf("overdue: %v alerts %d", pol["check_overdue"], e.alerts("restore_check_overdue"))
	}
	e.bk.Tick1(context.Background()) // one alert a day, not one a tick
	if e.alerts("restore_check_overdue") != 1 {
		t.Fatalf("alerts: %d", e.alerts("restore_check_overdue"))
	}
}

// AC-BKP-06 (server part): the control-plane database has a built-in daily policy with a local target and a UI warning.
func TestAC_BKP_06_ControlPlanePolicy(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	e.bk.Tick1(context.Background())
	list := c.do("GET", "/api/v1/backup-policies", nil)
	var pols []map[string]any
	_ = json.Unmarshal(list.Body.Bytes(), &pols)
	if len(pols) != 1 || pols[0]["control_plane"] != true || pols[0]["schedule"] != "0 3 * * *" || pols[0]["local_only"] != true || pols[0]["engine"] != "controlplane" {
		t.Fatalf("policies: %s", list.Body)
	}
	pid := str(pols[0], "id")
	if rec := c.do("DELETE", "/api/v1/backup-policies/"+pid, nil); rec.Code != 409 {
		t.Fatalf("delete the control-plane policy: %d", rec.Code)
	}
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("control-plane run", func() bool { return e.runState(str(run, "id")) == "succeeded" })
	rb := e.sent(func(c *pb.Command) bool { return c.GetRunBackup() != nil })[0].GetRunBackup()
	if rb.Source.Engine != "controlplane" || !strings.HasPrefix(rb.Targets[0].Repository, "/var/lib/polipan/backups/control-plane/"+pid) {
		t.Fatalf("command: %v", rb)
	}
	// A restore of the control-plane database is by hand.
	rs := c.do("POST", "/api/v1/backup-runs/"+str(run, "id")+"/restore", map[string]any{"target_id": rb.Targets[0].TargetId, "mode": "in_place", "confirm": "x"})
	if rs.Code != 409 {
		t.Fatalf("restore: %d %s", rs.Code, rs.Body)
	}
	// Adding a remote target clears the warning.
	remote := e.target(c, "offsite")
	upd := c.ok(200, "PATCH", "/api/v1/backup-policies/"+pid, map[string]any{"target_ids": []string{remote}})
	if upd["local_only"] != false {
		t.Fatalf("policy: %v", upd)
	}
}

// AC-BKP-04: a restore into a new service makes a clone, loads the snapshot there, and leaves the original unchanged.
func TestAC_BKP_04_RestoreToNewService(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	tg := e.target(c, "minio")
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{tg}}), "id")
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("run", func() bool { return e.runState(str(run, "id")) == "succeeded" })
	var origCfg, origVars string
	_ = e.pool.QueryRow(context.Background(), `SELECT config::text FROM services WHERE id = $1`, db).Scan(&origCfg)
	_ = e.pool.QueryRow(context.Background(), `SELECT string_agg(name || '=' || value, ',' ORDER BY name) FROM variables WHERE service_id = $1`, db).Scan(&origVars)

	// The mode needs a name for a clone that is not used.
	if rec := c.do("POST", "/api/v1/backup-runs/"+str(run, "id")+"/restore", map[string]any{"target_id": tg, "mode": "new_service", "new_service_name": "main"}); rec.Code != 409 {
		t.Fatalf("name in use: %d %s", rec.Code, rec.Body)
	}
	out := c.ok(202, "POST", "/api/v1/backup-runs/"+str(run, "id")+"/restore", map[string]any{"target_id": tg, "mode": "new_service", "new_service_name": "main-copy"})
	jobID, newID := str(out, "job_id"), str(out, "service_id")
	if jobID == "" || newID == "" || newID == db {
		t.Fatalf("answer: %v", out)
	}
	e.until("restore done", func() bool {
		var st string
		_ = e.pool.QueryRow(context.Background(), `SELECT state FROM backup_restores WHERE id = $1`, jobID).Scan(&st)
		return st == "succeeded" || st == "failed"
	})
	job := c.ok(200, "GET", "/api/v1/backup-restores/"+jobID, nil)
	if job["state"] != "succeeded" || job["service_id"] != newID || job["mode"] != "new_service" {
		t.Fatalf("job: %v", job)
	}
	// The new service is a database of its own, deployed and live, with its own password.
	svc := c.ok(200, "GET", "/api/v1/services/"+newID, nil)
	if svc["name"] != "main-copy" || svc["db_engine"] != "postgres" || svc["status"] != "running" {
		t.Fatalf("new service: %v", svc)
	}
	if e.count(`SELECT count(*) FROM secrets WHERE service_id = $1 AND name = 'DB_PASSWORD'`, newID) != 1 {
		t.Fatal("no password for the clone")
	}
	restores := e.sent(func(c *pb.Command) bool { return c.GetRestoreSnapshot() != nil })
	if len(restores) != 1 {
		t.Fatalf("restore commands: %d", len(restores))
	}
	rs := restores[0].GetRestoreSnapshot()
	var placement string
	_ = e.pool.QueryRow(context.Background(), `SELECT id FROM placements WHERE service_id = $1 AND role = 'live'`, newID).Scan(&placement)
	if rs.Source.Container != "pp-"+placement || rs.SnapshotId == "" || rs.RepoPassword == "" || rs.StopContainer {
		t.Fatalf("restore command: %v", rs)
	}
	// The original is unchanged and had no safety backup (a clone does not touch it).
	var cfg2, vars2 string
	_ = e.pool.QueryRow(context.Background(), `SELECT config::text FROM services WHERE id = $1`, db).Scan(&cfg2)
	_ = e.pool.QueryRow(context.Background(), `SELECT string_agg(name || '=' || value, ',' ORDER BY name) FROM variables WHERE service_id = $1`, db).Scan(&vars2)
	if cfg2 != origCfg || vars2 != origVars || e.count(`SELECT count(*) FROM backup_runs WHERE trigger = 'safety'`) != 0 {
		t.Fatal("the original service changed")
	}
}

// REQ-BKP-007: a restore in place needs the name of the service, an admin, and makes a safety backup first.
func TestRestoreInPlaceRules(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	tg := e.target(c, "minio")
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{tg}}), "id")
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("run", func() bool { return e.runState(str(run, "id")) == "succeeded" })
	path := "/api/v1/backup-runs/" + str(run, "id") + "/restore"
	dev := e.login("usr_dev", "dev@example.com", "developer")
	if rec := dev.do("POST", path, map[string]any{"target_id": tg, "mode": "in_place", "confirm": "main"}); rec.Code != 403 {
		t.Fatalf("developer: %d", rec.Code)
	}
	if rec := c.do("POST", path, map[string]any{"target_id": tg, "mode": "in_place", "confirm": "wrong"}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "confirm") {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", path, map[string]any{"target_id": "bkp_nope", "mode": "in_place", "confirm": "main"}); rec.Code != 422 {
		t.Fatalf("unknown target: %d", rec.Code)
	}
	out := c.ok(202, "POST", path, map[string]any{"target_id": tg, "mode": "in_place", "confirm": "main"})
	e.until("restore done", func() bool {
		var st string
		_ = e.pool.QueryRow(context.Background(), `SELECT state FROM backup_restores WHERE id = $1`, str(out, "job_id")).Scan(&st)
		return st == "succeeded"
	})
	// The safety backup ran before the restore command.
	e.mu.Lock()
	defer e.mu.Unlock()
	var order []string
	for _, cmd := range e.commands {
		if rb := cmd.GetRunBackup(); rb != nil {
			order = append(order, "backup:"+rb.Tag)
		}
		if cmd.GetRestoreSnapshot() != nil {
			order = append(order, "restore")
		}
	}
	if strings.Join(order, ",") != "backup:manual,backup:safety,restore" {
		t.Fatalf("order: %v", order)
	}
}

// A failed safety backup stops the restore (nothing is restored).
func TestRestoreStopsWhenSafetyBackupFails(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	db := e.dbService(c, envs["staging"], "main")
	tg := e.target(c, "minio")
	pid := str(e.policy(c, db, map[string]any{"target_ids": []string{tg}}), "id")
	run := c.ok(202, "POST", "/api/v1/backup-policies/"+pid+"/run", nil)
	e.until("run", func() bool { return e.runState(str(run, "id")) == "succeeded" })
	e.onBackup = func(rb *pb.RunBackup) *pb.CommandAck { return okSnapshots(rb, map[string]bool{tg: true}) }
	out := c.ok(202, "POST", "/api/v1/backup-runs/"+str(run, "id")+"/restore", map[string]any{"target_id": tg, "mode": "in_place", "confirm": "main"})
	e.until("restore failed", func() bool {
		var st string
		_ = e.pool.QueryRow(context.Background(), `SELECT state FROM backup_restores WHERE id = $1`, str(out, "job_id")).Scan(&st)
		return st == "failed"
	})
	if n := len(e.sent(func(c *pb.Command) bool { return c.GetRestoreSnapshot() != nil })); n != 0 {
		t.Fatalf("a restore command was sent after a failed safety backup: %d", n)
	}
}

// Targets: validation, 409 when a policy uses it, the test action.
func TestBackupTargets(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	for _, bad := range []map[string]any{
		{"name": "x", "kind": "local", "config": map[string]any{"path": "/etc"}},
		{"name": "x", "kind": "local", "config": map[string]any{"path": "/var/lib/polipan/backups/../../etc"}},
		{"name": "x", "kind": "sftp", "config": map[string]any{"host": "169.254.169.254", "user": "u", "path": "/b"}, "credentials": map[string]string{"private_key": "k"}},
		{"name": "x", "kind": "s3", "config": map[string]any{"bucket": "b", "endpoint": "http://169.254.169.254"}, "credentials": map[string]string{"access_key_id": "a", "secret_access_key": "b"}},
		{"name": "x", "kind": "s3", "config": map[string]any{"bucket": "b"}},
		{"name": "x", "kind": "s3", "config": map[string]any{"bucket": "b", "extra": 1}, "credentials": map[string]string{"access_key_id": "a", "secret_access_key": "b"}},
		{"name": "x", "kind": "ftp", "config": map[string]any{}},
	} {
		if rec := c.do("POST", "/api/v1/backup-targets", bad); rec.Code != 422 {
			t.Fatalf("%v: %d %s", bad, rec.Code, rec.Body)
		}
	}
	local := c.ok(201, "POST", "/api/v1/backup-targets", map[string]any{"name": "local", "kind": "local", "config": map[string]any{"path": "/var/lib/polipan/backups/mine"}})
	if rec := c.do("POST", "/api/v1/backup-targets", map[string]any{"name": "local", "kind": "local", "config": map[string]any{"path": "/var/lib/polipan/backups/other"}}); rec.Code != 409 {
		t.Fatalf("same name: %d", rec.Code)
	}
	sftp := c.ok(201, "POST", "/api/v1/backup-targets", map[string]any{"name": "sftp", "kind": "sftp",
		"config": map[string]any{"host": "backup.example.com", "user": "bk", "path": "/srv/restic", "port": 2222}, "credentials": map[string]string{"private_key": "-----BEGIN KEY-----"}})
	test := c.ok(200, "POST", "/api/v1/backup-targets/"+str(sftp, "id")+"/test", nil)
	if test["ok"] != true {
		t.Fatalf("test: %v", test)
	}
	got := c.ok(200, "GET", "/api/v1/backup-targets", nil)
	_ = got
	rec := c.do("GET", "/api/v1/backup-targets", nil)
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	for _, tg := range list {
		if tg["name"] == "sftp" && (tg["last_test_ok"] != true || tg["last_test_at"] == nil) {
			t.Fatalf("test result is not stored: %v", tg)
		}
	}
	e.test(sftp)
	db := e.dbService(c, envs["staging"], "main")
	e.policy(c, db, map[string]any{"target_ids": []string{str(local, "id")}})
	if rec := c.do("DELETE", "/api/v1/backup-targets/"+str(local, "id"), nil); rec.Code != 409 {
		t.Fatalf("delete used target: %d", rec.Code)
	}
	if rec := c.do("DELETE", "/api/v1/backup-targets/"+str(sftp, "id"), nil); rec.Code != 204 {
		t.Fatalf("delete target: %d", rec.Code)
	}
	// The credentials are stored as a secret that the secrets API cannot reach.
	if e.count(`SELECT count(*) FROM secrets WHERE name LIKE 'backup-target-%' AND deleted_at IS NULL`) != 1 {
		t.Fatal("the credentials of the deleted target are still active")
	}
}

func (e *m4Env) test(sftp map[string]any) {
	cmds := e.sent(func(c *pb.Command) bool { return c.GetTestBackupTarget() != nil })
	if len(cmds) != 1 {
		e.t.Fatalf("test commands: %d", len(cmds))
	}
	tg := cmds[0].GetTestBackupTarget().Target
	if tg.Repository != "sftp://bk@backup.example.com:2222/srv/restic/test" || string(tg.SshKey) != "-----BEGIN KEY-----" {
		e.t.Fatalf("sftp target: %v", tg)
	}
	_ = sftp
}

// Policies need a database or a volume, valid cron, existing targets (REQ-BKP-001).
func TestBackupPolicyValidation(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	tg := e.target(c, "minio")
	app := c.service(envs["staging"], "web")
	db := e.dbService(c, envs["staging"], "main")
	post := func(svc string, body map[string]any) int {
		return c.do("POST", "/api/v1/services/"+svc+"/backup-policies", body).Code
	}
	if code := post(app, map[string]any{"target_ids": []string{tg}}); code != 422 {
		t.Fatalf("an app without volume_id: %d", code)
	}
	vol := c.ok(201, "POST", "/api/v1/services/"+app+"/volumes", map[string]any{"name": "data", "mount_path": "/data"})
	if code := post(app, map[string]any{"target_ids": []string{tg}, "volume_id": str(vol, "id")}); code != 201 {
		t.Fatalf("volume policy: %d", code)
	}
	for name, body := range map[string]map[string]any{
		"bad cron":      {"target_ids": []string{tg}, "schedule": "every day"},
		"bad check":     {"target_ids": []string{tg}, "restore_check_schedule": "99 * * * *"},
		"no target":     {"target_ids": []string{}},
		"unknown":       {"target_ids": []string{"bkp_nope"}},
		"twice":         {"target_ids": []string{tg, tg}},
		"zero rules":    {"target_ids": []string{tg}, "retention": map[string]int{"keep_daily": 0}},
		"bad rule":      {"target_ids": []string{tg}, "retention": map[string]int{"keep_hourly": 3}},
		"foreign vol":   {"target_ids": []string{tg}, "volume_id": "vol_nope"},
		"too many hook": {"target_ids": []string{tg}, "pre_hook": []string{"a", "a", "a", "a", "a", "a", "a", "a", "a", "a", "a"}},
	} {
		if code := post(db, body); code != 422 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	// A developer cannot manage the policy of a protected environment (P in the permission matrix).
	prod := e.dbService(c, envs["production"], "main")
	dev := e.login("usr_dev", "dev@example.com", "developer")
	if rec := dev.do("POST", "/api/v1/services/"+prod+"/backup-policies", map[string]any{"target_ids": []string{tg}}); rec.Code != 403 {
		t.Fatalf("developer on production: %d %s", rec.Code, rec.Body)
	}
	if rec := dev.do("POST", "/api/v1/services/"+db+"/backup-policies", map[string]any{"target_ids": []string{tg}}); rec.Code != 201 {
		t.Fatalf("developer on staging: %d %s", rec.Code, rec.Body)
	}
	// Database state: the service shows the policy as configured.
	if svc := c.ok(200, "GET", "/api/v1/services/"+db, nil); svc["backup"].(map[string]any)["status"] != "configured" {
		t.Fatalf("backup status: %v", svc["backup"])
	}
	// A scheduled run is queued when the cron time has passed since the policy was made.
	pol := e.policy(c, db, map[string]any{"target_ids": []string{tg}, "schedule": "*/5 * * * *"})
	if e.count(`SELECT count(*) FROM backup_runs WHERE policy_id = $1`, str(pol, "id")) != 0 {
		t.Fatal("a run was made for a new policy before its time")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE backup_policies SET created_at = now() - interval '1 hour' WHERE id = $1`, str(pol, "id")); err != nil {
		t.Fatal(err)
	}
	e.until("scheduled run", func() bool {
		return e.count(`SELECT count(*) FROM backup_runs WHERE policy_id = $1 AND trigger = 'schedule' AND state = 'succeeded'`, str(pol, "id")) == 1
	})
	// Deleting a policy keeps the secret row but removes the policy (the snapshots stay unless delete_snapshots=true).
	if rec := c.do("DELETE", "/api/v1/backup-policies/"+str(pol, "id")+"?delete_snapshots=true", nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if len(e.sent(func(c *pb.Command) bool { return c.GetDeleteSnapshots() != nil })) != 1 {
		t.Fatal("the snapshots were not deleted on the node")
	}
}
