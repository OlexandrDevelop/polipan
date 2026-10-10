// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// m2Env is the API with the real control side, a real gateway and one fake agent.
type m2Env struct {
	*testEnv
	gw    *gatewaytest.Env
	ctl   *control.Control
	sec   *secrets.Manager
	kek   []byte
	agent *gatewaytest.Agent

	mu      sync.Mutex
	pullErr string // when set, the pull fails with this message
}

func newM2Env(t *testing.T) *m2Env {
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
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: g.Pool, Clock: g.Clock, RateLimit: 100000,
		Pool: g.Pool, Orchestrator: ctl, ExternalURL: "http://localhost", AgentListen: ":9443", SetupToken: "setup-secret", Secrets: sec,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &m2Env{testEnv: &testEnv{t: t, h: h, pool: g.Pool, clk: g.Clock}, gw: g, ctl: ctl, sec: sec, kek: kek}
	e.agent = g.NewReadyNode(t, "n1")
	e.agent.OnCommand = func(c *pb.Command) *pb.CommandAck {
		if _, ok := c.Body.(*pb.Command_PullImage); ok {
			e.mu.Lock()
			msg := e.pullErr
			e.mu.Unlock()
			if msg != "" {
				return &pb.CommandAck{ErrorCode: "image_pull_failed", ErrorMessage: msg}
			}
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"nginx@sha256:abc123","digest":"sha256:abc123"}`)}
		}
		return &pb.CommandAck{Ok: true}
	}
	return e
}

// deploy starts a deployment through the API and waits for its end. It returns the deployment row fields.
func (e *m2Env) deploy(c *client, serviceID string) (depID, state, reason string) {
	e.t.Helper()
	dep := c.ok(202, "POST", "/api/v1/services/"+serviceID+"/deploy", map[string]any{"strategy_options": map[string]any{"drain_seconds": 0}})
	depID = str(dep, "id")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var r *string
		if err := e.pool.QueryRow(context.Background(), `SELECT state, failure_reason FROM deployments WHERE id = $1`, depID).Scan(&state, &r); err != nil {
			e.t.Fatal(err)
		}
		if state == "live" || state == "failed" || state == "rolled_back" || state == "cancelled" {
			if r != nil {
				reason = *r
			}
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("deployment stuck in %s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// workload returns the workload of a service in the desired state of the node.
func (e *m2Env) workload(serviceID string) *pb.WorkloadSpec {
	e.t.Helper()
	ds, err := e.ctl.DesiredState(context.Background(), e.agent.NodeID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, w := range ds.Workloads {
		if w.ServiceId == serviceID && w.Role == "live" { // older releases stay as stopped workloads (retain_stopped)
			return w
		}
	}
	e.t.Fatalf("no workload of %s", serviceID)
	return nil
}

func (e *m2Env) nodeVersion() int64 {
	var v int64
	_ = e.pool.QueryRow(context.Background(), `SELECT desired_state_version FROM nodes WHERE id = $1`, e.agent.NodeID).Scan(&v)
	return v
}

func (e *m2Env) liveRelease(serviceID string) (variables, versions map[string]any) {
	e.t.Helper()
	var vr, sv []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT r.variable_snapshot, r.secret_versions FROM releases r
		JOIN services s ON s.live_release_id = r.id WHERE s.id = $1`, serviceID).Scan(&vr, &sv); err != nil {
		e.t.Fatal(err)
	}
	_ = json.Unmarshal(vr, &variables)
	_ = json.Unmarshal(sv, &versions)
	return variables, versions
}

// AC-ENV-01: project A=1, environment A=2: the release snapshot has A=2. Service scope wins over both,
// POLIPAN_* names are reserved and set by Polipan.
func TestAC_ENV_01_ReleaseSnapshotScopes(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc, other := c.service(envs["staging"], "web"), c.service(envs["staging"], "api")
	base := "/api/v1/projects/" + prj + "/variables"
	c.ok(201, "POST", base, map[string]any{"name": "A", "value": "1"})
	c.ok(201, "POST", base, map[string]any{"name": "A", "value": "2", "environment_id": envs["staging"]})
	c.ok(201, "POST", base, map[string]any{"name": "B", "value": "b-${{A}}"})
	c.ok(201, "POST", base, map[string]any{"name": "C", "value": "svc", "service_id": other})
	if rec := c.do("POST", base, map[string]any{"name": "POLIPAN_NODE", "value": "x"}); rec.Code != 422 {
		t.Fatalf("reserved name: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("POST", base, map[string]any{"name": "D", "value": "x", "environment_id": "env_other"}); rec.Code != 422 {
		t.Fatalf("scope outside the project: %d %s", rec.Code, rec.Body)
	}
	if _, _, state := e.deploy(c, svc); state != "" {
		t.Fatalf("deploy failed: %s", state)
	}
	variables, _ := e.liveRelease(svc)
	if variables["A"] != "2" || variables["B"] != "b-2" || variables["C"] != nil {
		t.Fatalf("snapshot: %v", variables)
	}
	w := e.workload(svc)
	if w.Env["A"] != "2" || w.Env["POLIPAN_SERVICE"] != "web" || w.Env["POLIPAN_ENV"] != "staging" || w.Env["POLIPAN_RELEASE"] != w.ReleaseId ||
		w.Env["POLIPAN_NODE"] != "n1" {
		t.Fatalf("workload env: %v", w.Env)
	}
	// The service variable wins in its service.
	c.ok(201, "POST", base, map[string]any{"name": "A", "value": "3", "service_id": other})
	if _, _, state := e.deploy(c, other); state != "" {
		t.Fatalf("deploy other: %s", state)
	}
	if variables, _ := e.liveRelease(other); variables["A"] != "3" || variables["C"] != "svc" {
		t.Fatalf("service scope: %v", variables)
	}
	if list := c.do("GET", base+"?environment_id="+envs["staging"], nil); !strings.Contains(list.Body.String(), `"value":"2"`) {
		t.Fatalf("list by environment: %s", list.Body)
	}
}

// AC-ENV-02 and AC-SEC-01: no GET answer has a secret value or a value field for a secret.
func TestAC_ENV_02_AC_SEC_01_SecretsAreWriteOnly(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	value := "sk_live_write_only_value"
	x := c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "STRIPE_KEY", "value": value, "environment_id": envs["staging"]})
	if _, ok := x["value"]; ok || x["current_version"] != float64(1) {
		t.Fatalf("create answer: %v", x)
	}
	id := str(x, "id")
	c.ok(200, "PUT", "/api/v1/secrets/"+id, map[string]any{"value": value + "-2"})
	c.ok(200, "POST", "/api/v1/secrets/"+id+"/versions/1/restore", nil)
	for _, p := range []string{"/api/v1/projects/" + prj + "/secrets", "/api/v1/projects/" + prj + "/secrets?environment_id=" + envs["staging"], "/api/v1/secrets/" + id + "/versions"} {
		rec := c.do("GET", p, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), value) || strings.Contains(rec.Body.String(), `"value"`) {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	var list []map[string]any
	_ = json.Unmarshal(c.do("GET", "/api/v1/secrets/"+id+"/versions", nil).Body.Bytes(), &list)
	if len(list) != 3 || list[0]["version"] != float64(3) {
		t.Fatalf("versions: %v", list)
	}
	// The value of v3 is the value of v1 (restore writes a new version).
	got, err := e.sec.Provider.Get(context.Background(), e.pool, prj, id, 3)
	if err != nil || string(got) != value {
		t.Fatalf("restored value: %q %v", got, err)
	}
	// Old, unused versions can be destroyed; the current one cannot.
	c.ok(204, "DELETE", "/api/v1/secrets/"+id+"/versions/2", nil)
	if rec := c.do("DELETE", "/api/v1/secrets/"+id+"/versions/3", nil); rec.Code != 409 {
		t.Fatalf("destroy current: %d", rec.Code)
	}
	if rec := c.do("POST", "/api/v1/secrets/"+id+"/versions/2/restore", nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "secret_version_destroyed") {
		t.Fatalf("restore destroyed: %d %s", rec.Code, rec.Body)
	}
}

// AC-ENV-03: a variable change does not touch the running containers; the service shows pending changes.
func TestAC_ENV_03_PendingChanges(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	if s := c.ok(200, "GET", "/api/v1/services/"+svc, nil); s["pending_changes"] != false {
		t.Fatalf("never deployed: %v", s["pending_changes"])
	}
	v := c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "LOG_LEVEL", "value": "info"})
	if _, _, state := e.deploy(c, svc); state != "" {
		t.Fatalf("deploy: %s", state)
	}
	if s := c.ok(200, "GET", "/api/v1/services/"+svc, nil); s["pending_changes"] != false {
		t.Fatalf("after deploy: %v", s["pending_changes"])
	}
	before, deps, wl := e.nodeVersion(), e.count(`SELECT count(*) FROM deployments`), e.workload(svc)

	c.ok(200, "PATCH", "/api/v1/variables/"+str(v, "id"), map[string]any{"name": "LOG_LEVEL", "value": "debug"})
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "TOKEN", "value": "tok-123456"})
	if s := c.ok(200, "GET", "/api/v1/services/"+svc, nil); s["pending_changes"] != true {
		t.Fatalf("after a change: %v", s["pending_changes"])
	}
	var list []map[string]any
	_ = json.Unmarshal(c.do("GET", "/api/v1/environments/"+envs["staging"]+"/services", nil).Body.Bytes(), &list)
	if len(list) != 1 || list[0]["pending_changes"] != true {
		t.Fatalf("list: %v", list)
	}
	if e.nodeVersion() != before || e.count(`SELECT count(*) FROM deployments`) != deps || e.workload(svc).Env["LOG_LEVEL"] != "info" ||
		wl.Env["LOG_LEVEL"] != "info" {
		t.Fatal("a variable change touched the running service")
	}
	// The next deployment takes the new values, then nothing is pending.
	if _, _, state := e.deploy(c, svc); state != "" {
		t.Fatalf("redeploy: %s", state)
	}
	w := e.workload(svc)
	if w.Env["LOG_LEVEL"] != "debug" || w.Env["TOKEN"] != "tok-123456" || len(w.SecretEnvKeys) != 1 || w.SecretEnvKeys[0] != "TOKEN" {
		t.Fatalf("new env: %v %v", w.Env, w.SecretEnvKeys)
	}
	if s := c.ok(200, "GET", "/api/v1/services/"+svc, nil); s["pending_changes"] != false {
		t.Fatalf("after redeploy: %v", s["pending_changes"])
	}
}

// AC-ENV-04: A=${{B}} and B=${{A}} gives 422 reference_cycle at save. A missing name fails the deployment.
func TestAC_ENV_04_ReferenceCycleAndUnresolved(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	base := "/api/v1/projects/" + prj + "/variables"
	c.ok(201, "POST", base, map[string]any{"name": "A", "value": "${{B}}"})
	rec := c.do("POST", base, map[string]any{"name": "B", "value": "${{A}}", "environment_id": envs["staging"]})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"reference_cycle"`) {
		t.Fatalf("cycle: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM variables WHERE name = 'B'`) != 0 {
		t.Fatal("the cycle was saved")
	}
	rec = c.do("POST", base+":bulk", map[string]any{"items": []map[string]any{{"name": "B", "value": "${{C}}"}, {"name": "C", "value": "${{A}}"}}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"reference_cycle"`) {
		t.Fatalf("bulk cycle: %d %s", rec.Code, rec.Body)
	}
	// B is missing: the deployment fails with unresolved_reference and lists the name.
	_, state, reason := e.deploy(c, svc)
	if state != "failed" || reason != "unresolved_reference" {
		t.Fatalf("unresolved: %s %s", state, reason)
	}
	var detail string
	_ = e.pool.QueryRow(context.Background(), `SELECT failure_detail FROM deployments WHERE state = 'failed'`).Scan(&detail)
	if !strings.Contains(detail, "B") || e.count(`SELECT count(*) FROM releases`) != 0 {
		t.Fatalf("detail %q", detail)
	}
	out := c.ok(200, "POST", base+":bulk", map[string]any{"items": []map[string]any{{"name": "B", "value": "bee"}, {"name": "S", "value": "sec-value-1", "secret": true}}})
	if out["created"] != float64(2) {
		t.Fatalf("bulk: %v", out)
	}
	if _, state, reason := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy after fix: %s %s", state, reason)
	}
}

// AC-ENV-05: a variable and a secret with the same name in one scope: 409 name_conflict.
func TestAC_ENV_05_NameConflict(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "KEY", "value": "v"})
	for _, p := range []string{"/secrets", "/variables"} {
		rec := c.do("POST", "/api/v1/projects/"+prj+p, map[string]any{"name": "KEY", "value": "s"})
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"name_conflict"`) {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "TOKEN", "value": "s"})
	if rec := c.do("POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "TOKEN", "value": "v"}); rec.Code != 409 {
		t.Fatalf("variable over secret: %d", rec.Code)
	}
	// Another scope is fine: it overrides.
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "KEY", "value": "s", "environment_id": envs["staging"]})
}

// AC-SEC-02: a release records the current version number; the workload of that release keeps
// that version after a new write (a rollback or a redeploy of the release uses the stored number).
func TestAC_SEC_02_ReleaseRecordsVersions(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	x := c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "DB_PASSWORD", "value": "pw-version-1"})
	c.ok(200, "PUT", "/api/v1/secrets/"+str(x, "id"), map[string]any{"value": "pw-version-2"})
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/variables", map[string]any{"name": "DATABASE_URL", "value": "postgres://app:${{DB_PASSWORD}}@db/app"})
	if _, state, _ := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s", state)
	}
	variables, versions := e.liveRelease(svc)
	if versions["DB_PASSWORD"] != float64(2) || variables["DATABASE_URL"] != nil {
		t.Fatalf("release: %v %v", variables, versions)
	}
	c.ok(200, "PUT", "/api/v1/secrets/"+str(x, "id"), map[string]any{"value": "pw-version-3"})
	w := e.workload(svc)
	if w.Env["DB_PASSWORD"] != "pw-version-2" || w.Env["DATABASE_URL"] != "postgres://app:pw-version-2@db/app" {
		t.Fatalf("workload after a new version: %v", w.Env)
	}
	// Version 2 is used by a kept release: it cannot be destroyed. Version 1 can.
	if rec := c.do("DELETE", "/api/v1/secrets/"+str(x, "id")+"/versions/2", nil); rec.Code != 409 {
		t.Fatalf("destroy a used version: %d", rec.Code)
	}
	c.ok(204, "DELETE", "/api/v1/secrets/"+str(x, "id")+"/versions/1", nil)
	uses := e.count(`SELECT count(*) FROM events WHERE topic = 'audit' AND payload->>'action' = 'secret.use'
		AND payload->'diff'->'secrets'->0->>'version' = '2'`)
	if uses != 1 {
		t.Fatalf("secret.use audit records: %d", uses)
	}
	// A deleted secret keeps the version of the release.
	c.ok(204, "DELETE", "/api/v1/secrets/"+str(x, "id"), nil)
	if e.workload(svc).Env["DB_PASSWORD"] != "pw-version-2" {
		t.Fatal("deleting the secret changed the running release")
	}
}

// AC-SEC-03: a secret value in a log line is stored as ***.
func TestAC_SEC_03_DeploymentLogsMasked(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	value := "registry-password-0042"
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "REG_PASS", "value": value})
	e.mu.Lock()
	e.pullErr = "login failed for user bob with password " + value
	e.mu.Unlock()
	if _, state, reason := e.deploy(c, svc); state != "failed" || reason != "image_pull_failed" {
		t.Fatalf("deploy: %s %s", state, reason)
	}
	if n := e.count(`SELECT count(*) FROM deployment_logs WHERE line LIKE '%password ***%'`); n != 1 {
		t.Fatalf("masked lines: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM deployment_logs l, deployments d, deployment_steps s
		WHERE l.line LIKE '%'||$1||'%' OR d.failure_detail LIKE '%'||$1||'%' OR s.message LIKE '%'||$1||'%'`, value); n != 0 {
		t.Fatalf("the value is stored %d times", n)
	}
}

// AC-APP-06 (store and mask level): the masker of a service hides the current and the kept
// versions of its secrets, as the build step uses it for build logs.
func TestAC_APP_06_BuildLogMasker(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	other := c.service(envs["production"], "web")
	x := c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "NPM_TOKEN", "value": "npm-old-token", "build_time": true})
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "PROD_ONLY", "value": "prod-only-value", "environment_id": envs["production"]})
	if _, state, _ := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s", state)
	}
	c.ok(200, "PUT", "/api/v1/secrets/"+str(x, "id"), map[string]any{"value": "npm-new-token"})
	m, err := e.sec.Masker(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	line := "#5 RUN npm ci --token npm-new-token (was npm-old-token) prod-only-value"
	if got := m.Mask(line); got != "#5 RUN npm ci --token *** (was ***) prod-only-value" {
		t.Fatalf("masked: %q", got)
	}
	m2, _ := e.sec.Masker(context.Background(), other)
	if got := m2.Mask(line); !strings.Contains(got, "npm-old-token") || strings.Contains(got, "prod-only-value") {
		t.Fatalf("other service: %q", got)
	}
}

// AC-SEC-04 (running side): rotate-kek and rotate-dek keep the desired state and the values; no workload changes.
func TestAC_SEC_04_RotationKeepsServicesRunning(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "API_KEY", "value": "api-key-value"})
	if _, state, _ := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s", state)
	}
	before, version := e.workload(svc), e.nodeVersion()
	ctx := context.Background()
	// The test gateway made its CA with another key: make it again with the key of this test.
	if _, err := e.pool.Exec(ctx, `DELETE FROM server_ca`); err != nil {
		t.Fatal(err)
	}
	newKEK := make([]byte, 32)
	_, _ = rand.Read(newKEK)
	if _, err := secrets.RotateKEK(ctx, e.pool, e.kek, newKEK); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.RotateDEK(ctx, e.pool, newKEK, prj); err != nil {
		t.Fatal(err)
	}
	sec, _ := secrets.NewManager(e.pool, newKEK)
	ctl := control.New(control.Deps{DB: e.pool, Clock: e.clk, Config: e.gw.Cfg, CA: e.gw.CA, Secrets: sec})
	defer ctl.Close()
	ds, err := ctl.DesiredState(ctx, e.agent.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if e.nodeVersion() != version || len(ds.Workloads) != 1 || ds.Workloads[0].Env["API_KEY"] != "api-key-value" ||
		ds.Workloads[0].ReleaseId != before.ReleaseId || ds.Workloads[0].PlacementId != before.PlacementId {
		t.Fatalf("after rotation: version %d -> %d, %v", version, e.nodeVersion(), ds.Workloads)
	}
}

// AC-VOL-01 (server side): a redeploy mounts the same volume (same Docker volume on the same node),
// with file mounts and secret files.
func TestAC_VOL_01_SameVolumeAfterRedeploy(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	c.ok(201, "POST", "/api/v1/projects/"+prj+"/secrets", map[string]any{"name": "TLS_KEY", "value": "-----BEGIN KEY-----"})
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80,
		"files":        []map[string]any{{"path": "/etc/app/config.yml", "content": "a: 1\n"}},
		"secret_files": []map[string]any{{"path": "/run/secrets/tls.key", "secret": "TLS_KEY"}}}})
	vol := c.ok(201, "POST", "/api/v1/services/"+svc+"/volumes", map[string]any{"name": "uploads", "mount_path": "/data"})
	c.ok(201, "POST", "/api/v1/services/"+svc+"/volumes", map[string]any{"name": "cache", "kind": "tmpfs", "mount_path": "/cache", "size_limit_bytes": 1048576})
	for _, bad := range []map[string]any{
		{"name": "two", "mount_path": "/data"},
		{"name": "three", "mount_path": "/etc/app/config.yml"},
		{"name": "Bad Name", "mount_path": "/x"},
		{"name": "rel", "mount_path": "data"},
	} {
		if rec := c.do("POST", "/api/v1/services/"+svc+"/volumes", bad); rec.Code != 422 {
			t.Fatalf("bad volume %v: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := c.do("POST", "/api/v1/services/"+svc+"/volumes", map[string]any{"name": "uploads", "mount_path": "/other"}); rec.Code != 409 {
		t.Fatalf("same name: %d", rec.Code)
	}
	if _, state, reason := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s %s", state, reason)
	}
	first := e.workload(svc)
	if _, state, _ := e.deploy(c, svc); state != "live" {
		t.Fatalf("redeploy: %s", state)
	}
	second := e.workload(svc)
	if first.ReleaseId == second.ReleaseId {
		t.Fatal("no new release")
	}
	want := store.VolumeDockerName(str(vol, "id"))
	byTarget := map[string]*pb.Mount{}
	for _, m := range second.Mounts {
		byTarget[m.Target] = m
	}
	if m := byTarget["/data"]; m == nil || m.Kind != "volume" || m.Source != want || first.Mounts[1].Source != want {
		t.Fatalf("volume mounts: %v / %v", first.Mounts, second.Mounts)
	}
	if m := byTarget["/cache"]; m == nil || m.Kind != "tmpfs" || m.SizeLimitBytes != 1048576 {
		t.Fatalf("tmpfs: %v", m)
	}
	if m := byTarget["/etc/app/config.yml"]; m == nil || m.Kind != "file" || string(m.Content) != "a: 1\n" || !m.ReadOnly || m.Secret {
		t.Fatalf("file: %v", m)
	}
	if m := byTarget["/run/secrets/tls.key"]; m == nil || m.Kind != "file" || !m.Secret || string(m.Content) != "-----BEGIN KEY-----" {
		t.Fatalf("secret file: %v", m)
	}
	var node string
	_ = e.pool.QueryRow(context.Background(), `SELECT COALESCE(node_id, '') FROM volumes WHERE id = $1`, str(vol, "id")).Scan(&node)
	if node != e.agent.NodeID {
		t.Fatalf("the volume is not pinned to the node: %q", node)
	}
	// A secret file must name a secret: a missing one fails the deployment.
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.27",
		"secret_files": []map[string]any{{"path": "/run/secrets/x", "secret": "MISSING"}}}})
	if _, state, reason := e.deploy(c, svc); state != "failed" || reason != "unresolved_reference" {
		t.Fatalf("missing secret file: %s %s", state, reason)
	}
}

// AC-VOL-03: a service deleted without volume confirmation keeps its volumes; they are listed as orphans.
func TestAC_VOL_03_DeletedServiceVolumesAreOrphans(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	_, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	vol := c.ok(201, "POST", "/api/v1/services/"+svc+"/volumes", map[string]any{"name": "data", "mount_path": "/data"})
	c.ok(201, "POST", "/api/v1/services/"+svc+"/volumes", map[string]any{"name": "tmp", "kind": "tmpfs", "mount_path": "/tmp"})
	if _, state, _ := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s", state)
	}
	orphans := "/api/v1/nodes/" + e.agent.NodeID + "/orphan-volumes"
	if rec := c.do("GET", orphans, nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("orphans before delete: %s", rec.Body)
	}
	c.ok(204, "DELETE", "/api/v1/services/"+svc+"?confirm=web", nil)
	var list []map[string]any
	_ = json.Unmarshal(c.do("GET", orphans, nil).Body.Bytes(), &list)
	name := store.VolumeDockerName(str(vol, "id"))
	if len(list) != 1 || list[0]["name"] != name || list[0]["last_service"] != "web" {
		t.Fatalf("orphans: %v", list)
	}
	if e.count(`SELECT count(*) FROM volumes WHERE id = $1`, str(vol, "id")) != 1 {
		t.Fatal("the volume record is gone")
	}
	c.ok(204, "DELETE", "/api/v1/orphan-volumes/"+e.agent.NodeID+"/"+name, nil)
	c.ok(404, "DELETE", "/api/v1/orphan-volumes/"+e.agent.NodeID+"/"+name, nil)
	if rec := c.do("GET", orphans, nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("orphans after delete: %s", rec.Body)
	}
}

// Gate 9 (secret leak test): with secrets in use, no API answer, deployment log, step, event,
// audit record or release row holds a secret value. Only the desired state for the agent does.
func TestGate9_SecretLeak(t *testing.T) {
	e := newM2Env(t)
	c := e.owner()
	prj, envs := c.project("Shop")
	svc := c.service(envs["staging"], "web")
	values := []string{"gate9-project-secret", "gate9-env-secret", "gate9-service-secret", "gate9-file-secret", "gate9-rotated-secret"}
	base := "/api/v1/projects/" + prj
	c.ok(201, "POST", base+"/secrets", map[string]any{"name": "P_SECRET", "value": values[0]})
	c.ok(201, "POST", base+"/secrets", map[string]any{"name": "E_SECRET", "value": values[1], "environment_id": envs["staging"]})
	sx := c.ok(201, "POST", base+"/secrets", map[string]any{"name": "S_SECRET", "value": values[2], "service_id": svc})
	c.ok(200, "POST", base+"/variables:bulk", map[string]any{"service_id": svc, "items": []map[string]any{
		{"name": "F_SECRET", "value": values[3], "secret": true}, {"name": "URL", "value": "https://u:${{S_SECRET}}@h"}}})
	c.ok(200, "PATCH", "/api/v1/services/"+svc, map[string]any{"config": map[string]any{"image": "nginx:1.27", "port": 80,
		"secret_files": []map[string]any{{"path": "/run/secrets/f", "secret": "F_SECRET"}}}})
	if _, state, reason := e.deploy(c, svc); state != "live" {
		t.Fatalf("deploy: %s %s", state, reason)
	}
	c.ok(200, "PUT", "/api/v1/secrets/"+str(sx, "id"), map[string]any{"value": values[4]})
	e.mu.Lock()
	e.pullErr = "denied: token " + values[0] + " and " + values[4]
	e.mu.Unlock()
	depID, state, _ := e.deploy(c, svc)
	if state != "failed" {
		t.Fatalf("failing deploy: %s", state)
	}
	// The values reach the agent.
	if w := e.workload(svc); w.Env["S_SECRET"] != values[2] || w.Env["URL"] != "https://u:"+values[2]+"@h" {
		t.Fatalf("workload env: %v", w.Env)
	}

	leak := func(where, text string) {
		t.Helper()
		for _, v := range values {
			if strings.Contains(text, v) {
				t.Fatalf("%s holds the secret value %q", where, v)
			}
		}
	}
	var secretIDs []string
	var list []map[string]any
	_ = json.Unmarshal(c.do("GET", base+"/secrets", nil).Body.Bytes(), &list)
	for _, s := range list {
		secretIDs = append(secretIDs, str(s, "id"))
	}
	gets := []string{"/api/v1/me", "/api/v1/projects", base, base + "/environments", base + "/variables", base + "/secrets",
		"/api/v1/environments/" + envs["staging"] + "/services", "/api/v1/services/" + svc, "/api/v1/services/" + svc + "/deployments",
		"/api/v1/services/" + svc + "/domains", "/api/v1/services/" + svc + "/volumes", "/api/v1/deployments/" + depID, "/api/v1/nodes",
		"/api/v1/nodes/" + e.agent.NodeID, "/api/v1/nodes/" + e.agent.NodeID + "/orphan-volumes"}
	for _, id := range secretIDs {
		gets = append(gets, "/api/v1/secrets/"+id+"/versions")
	}
	for _, p := range gets {
		rec := c.do("GET", p, nil)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotImplemented {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
		leak("GET "+p, rec.Body.String())
	}
	for _, q := range []string{
		`SELECT t::text FROM deployment_logs t`, `SELECT t::text FROM deployment_steps t`, `SELECT t::text FROM deployments t`,
		`SELECT t::text FROM events t`, `SELECT t::text FROM releases t`, `SELECT t::text FROM secrets t`,
		`SELECT t::text FROM secret_versions t`, `SELECT t::text FROM data_keys t`, `SELECT t::text FROM variables t WHERE name <> 'URL'`,
	} {
		rows, err := e.pool.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			leak(q, s)
			n++
		}
		rows.Close()
		if n == 0 && !strings.Contains(q, "variables") {
			t.Fatalf("%s: no rows, the scan proves nothing", q)
		}
	}
	if e.count(`SELECT count(*) FROM events WHERE topic = 'audit' AND payload->>'action' = 'secret.use'`) == 0 {
		t.Fatal("no secret.use audit record")
	}
}
