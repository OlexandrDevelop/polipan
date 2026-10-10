// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// sim is a fake agent that records the commands and answers them. A test changes the answers.
type sim struct {
	mu     sync.Mutex
	cmds   []string
	n      int
	pulls  []string
	check  func(*pb.CheckHealth) *pb.CommandAck
	oneoff func() *pb.CommandAck
	swtch  func() *pb.CommandAck
}

func newSim(a *gatewaytest.Agent) *sim {
	s := &sim{}
	a.Async = true
	a.OnCommand = s.on
	return s
}

func (s *sim) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

func (s *sim) on(c *pb.Command) *pb.CommandAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch b := c.Body.(type) {
	case *pb.Command_PullImage:
		s.cmds = append(s.cmds, "pull")
		s.pulls = append(s.pulls, b.PullImage.ImageRef)
		ref := b.PullImage.ImageRef
		if i := strings.LastIndex(ref, "@"); i >= 0 { // pulled by digest: the same digest
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"` + ref + `","digest":"` + ref[i+1:] + `"}`)}
		}
		s.n++
		d := fmt.Sprintf("sha256:%064d", s.n)
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"nginx@` + d + `","digest":"` + d + `"}`)}
	case *pb.Command_CheckHealth:
		s.cmds = append(s.cmds, "check:"+b.CheckHealth.PlacementId)
		if s.check != nil {
			f := s.check
			s.mu.Unlock()
			defer s.mu.Lock()
			return f(b.CheckHealth)
		}
	case *pb.Command_RunOneOff:
		s.cmds = append(s.cmds, "oneoff")
		if s.oneoff != nil {
			return s.oneoff()
		}
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"exit_code":0,"logs":""}`)}
	case *pb.Command_SwitchTraffic:
		s.cmds = append(s.cmds, "switch")
		if s.swtch != nil {
			f := s.swtch
			s.mu.Unlock()
			defer s.mu.Lock()
			return f()
		}
	case *pb.Command_ProbeRoute:
		s.cmds = append(s.cmds, "probe")
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"total":10,"failed":0}`)}
	case *pb.Command_StopWorkload:
		s.cmds = append(s.cmds, "stop:"+b.StopWorkload.PlacementId)
	case *pb.Command_RemoveWorkload:
		s.cmds = append(s.cmds, "remove:"+b.RemoveWorkload.PlacementId)
	case *pb.Command_PruneImages:
		s.cmds = append(s.cmds, "prune")
	}
	return &pb.CommandAck{Ok: true}
}

func (s *sim) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// index returns the position of the first command with the prefix, or -1.
func (s *sim) index(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.cmds {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// m3 is a fixture with a ready node, a domain and a fake agent.
type m3 struct {
	*fixture
	agent *gatewaytest.Agent
	sim   *sim
}

func setupM3(t *testing.T, tweak func(*control.Deps)) *m3 {
	t.Helper()
	f := setupWith(t, tweak)
	a := f.env.NewReadyNode(t, "n1")
	m := &m3{fixture: f, agent: a, sim: newSim(a)}
	if _, err := f.env.Pool.Exec(context.Background(), `INSERT INTO domains (id, service_id, host, container_port) VALUES ($1,$2,'app.example.com',8080)`,
		gateway.NewID("dom"), f.serviceID); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *m3) deploy(t *testing.T, p orchestrator.DeployParams) (id, state, reason string) {
	t.Helper()
	p.ServiceID = m.serviceID
	id, _, err := m.ctl.DeployWith(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	state, reason = m.waitDeployment(t, id)
	return
}

func (m *m3) mustDeploy(t *testing.T) string {
	t.Helper()
	id, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "live" {
		t.Fatalf("deployment %s %s", state, reason)
	}
	return id
}

func (m *m3) q(t *testing.T, sql string, args ...any) (out string) {
	t.Helper()
	var v any
	if err := m.env.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(v)
}

func (m *m3) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := m.env.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (m *m3) releaseID(t *testing.T, number int) string {
	return m.q(t, `SELECT id FROM releases WHERE service_id = $1 AND number = $2`, m.serviceID, number)
}

func (m *m3) placementOf(t *testing.T, release string) string {
	return m.q(t, `SELECT id FROM placements WHERE release_id = $1 AND service_id = $2 ORDER BY created_at DESC LIMIT 1`, release, m.serviceID)
}

func (m *m3) waitState(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := m.q(t, `SELECT state FROM deployments WHERE id = $1`, id); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("deployment is %s, want %s", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The new containers get traffic only after all ingress nodes acked, the old ones are stopped after that and kept
// (REQ-DEPLOY-101, AC-DEPLOY-07). Only retain_stopped sets stay.
func TestAC_DEPLOY_07_BlueGreenKeepsStoppedBlue(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	v1 := m.placementOf(t, m.releaseID(t, 1))
	if got := m.q(t, `SELECT color || '/' || role FROM placements WHERE id = $1`, v1); got != "blue/live" {
		t.Fatalf("first placement %s", got)
	}
	id := m.mustDeploy(t)
	v2 := m.placementOf(t, m.releaseID(t, 2))
	if got := m.q(t, `SELECT strategy FROM deployments WHERE id = $1`, id); got != "blue_green" {
		t.Fatalf("strategy %s", got)
	}
	if got := m.q(t, `SELECT color || '/' || role FROM placements WHERE id = $1`, v2); got != "green/live" {
		t.Fatalf("new placement %s", got)
	}
	if got := m.q(t, `SELECT role FROM placements WHERE id = $1`, v1); got != "stopped" {
		t.Fatalf("old placement %s", got)
	}
	if sw, stop := m.sim.index("switch"), m.sim.index("stop:"+v1); sw < 0 || stop < sw {
		t.Fatalf("blue was stopped before the switch was acked: %v", m.sim.cmds)
	}
	ds := lastState(t, m.agent)
	roles := map[string]string{}
	for _, w := range ds.Workloads {
		roles[w.PlacementId] = w.Role
	}
	if roles[v1] != "stopped" || roles[v2] != "live" {
		t.Fatalf("workload roles %v", roles)
	}
	r := routeByID(ds, m.q(t, `SELECT id FROM domains WHERE service_id = $1`, m.serviceID))
	if len(r.Upstreams) != 1 || r.Upstreams[0].Address != "pp-"+v2+":8080" {
		t.Fatalf("upstreams %v", r.Upstreams)
	}
	// A third deploy keeps one stopped set: the container of v1 is removed.
	m.mustDeploy(t)
	if got := m.q(t, `SELECT count(*) FROM placements WHERE release_id = $1`, m.releaseID(t, 1)); got != "0" {
		t.Fatalf("old containers of v1: %s", got)
	}
	if m.sim.index("remove:"+v1) < 0 || m.q(t, `SELECT role FROM placements WHERE id = $1`, v2) != "stopped" {
		t.Fatalf("retention: %v", m.sim.cmds)
	}
}

// AC-DEPLOY-02, AC-HC-05: green never gets healthy. The deployment fails with the last status of the check, blue serves.
func TestAC_DEPLOY_02_HealthTimeoutBlueKeepsServing(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.sim.set(func() {
		m.sim.check = func(*pb.CheckHealth) *pb.CommandAck {
			return &pb.CommandAck{ErrorCode: "health_timeout", ErrorMessage: "the health check did not pass in time: last result: status 503 (expected 200-399): not ready"}
		}
	})
	id, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "failed" || reason != "health_timeout" {
		t.Fatalf("%s %s", state, reason)
	}
	if got := m.q(t, `SELECT failure_detail FROM deployments WHERE id = $1`, id); !strings.Contains(got, "status 503") || !strings.Contains(got, "not ready") {
		t.Fatalf("detail: %s", got)
	}
	v1 := m.releaseID(t, 1)
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v1 || m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v1) != "live" {
		t.Fatal("blue does not serve")
	}
	// The new containers stay stopped for failed_green_keep, then they go.
	if m.q(t, `SELECT count(*) FROM placements WHERE release_id <> $1`, v1) != "1" || m.q(t, `SELECT (expires_at IS NOT NULL)::text FROM placements WHERE release_id <> $1`, v1) != "true" {
		t.Fatal("failed green is not kept with an expiry")
	}
	if m.sim.count("switch") != 1 { // the switch of the first deploy only
		t.Fatalf("a switch was sent: %v", m.sim.cmds)
	}
}

// AC-DEPLOY-04 and AC-VOL-02: a single-writer volume forces recreate. The plan shows it and the warning (REQ-DEPLOY-007).
func TestAC_DEPLOY_04_ForcedRecreateSingleWriter(t *testing.T) {
	m := setupM3(t, nil)
	m.exec(t, `INSERT INTO volumes (id, service_id, name, kind, mount_path, access_mode) VALUES ($1,$2,'data','volume','/data','single_writer')`,
		gateway.NewID("vol"), m.serviceID)
	plan, err := m.ctl.Plan(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, Strategy: "blue_green"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "recreate" || !plan.Forced || plan.ExpectedDowntime != "short" || len(plan.Warnings) != 1 || plan.Warnings[0].Code != "zero_downtime_not_available" {
		t.Fatalf("plan %+v", plan)
	}
	m.mustDeploy(t)
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, Strategy: "blue_green"})
	if err != nil {
		t.Fatal(err)
	}
	if st, reason := m.waitDeployment(t, id); st != "live" {
		t.Fatalf("%s %s", st, reason)
	}
	if m.q(t, `SELECT strategy FROM deployments WHERE id = $1`, id) != "recreate" || !strings.Contains(m.q(t, `SELECT warnings::text FROM deployments WHERE id = $1`, id), "zero_downtime_not_available") {
		t.Fatal("the deployment is not a recreate with the warning")
	}
	// Recreate: the old container stops before the new one is checked.
	v1 := m.placementOf(t, m.releaseID(t, 1))
	if stop, check := m.sim.index("stop:"+v1), m.sim.index("check:"+m.placementOf(t, m.releaseID(t, 2))); stop < 0 || check < stop {
		t.Fatalf("recreate order: %v", m.sim.cmds)
	}
	// Canary is not in this version.
	if _, err := m.ctl.Plan(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, Strategy: "canary"}); !errors.Is(err, orchestrator.ErrStrategyUnavailable) {
		t.Fatalf("canary: %v", err)
	}
}

// A recreate whose health check fails starts the old container again (rolled_back).
func TestRecreateHealthFailureRestoresBlue(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.sim.set(func() {
		m.sim.check = func(*pb.CheckHealth) *pb.CommandAck {
			return &pb.CommandAck{ErrorCode: "health_timeout", ErrorMessage: "down"}
		}
	})
	_, state, reason := m.deploy(t, orchestrator.DeployParams{Strategy: "recreate"})
	if state != "rolled_back" || reason != "health_timeout" {
		t.Fatalf("%s %s", state, reason)
	}
	if m.q(t, `SELECT role FROM placements WHERE release_id = $1`, m.releaseID(t, 1)) != "live" ||
		m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != m.releaseID(t, 1) {
		t.Fatal("the old release is not live again")
	}
}

// AC-DEPLOY-05: no room for two copies fails before any container starts. With the opt-in it becomes a recreate.
func TestAC_DEPLOY_05_NoCapacityForTwoCopies(t *testing.T) {
	m := setupM3(t, nil)
	m.exec(t, `UPDATE nodes SET capacity = '{"cpu_millis":4000,"memory_bytes":1000000000}'`)
	m.exec(t, `UPDATE services SET config = '{"image":"nginx:1.27","port":8080,"resources":{"memory_bytes":600000000},"strategy_options":{"drain_seconds":0}}' WHERE id = $1`, m.serviceID)
	m.mustDeploy(t)
	pulls := m.sim.count("pull")
	_, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "failed" || reason != "node_unavailable" {
		t.Fatalf("%s %s", state, reason)
	}
	if m.sim.count("pull") != pulls || m.q(t, `SELECT count(*) FROM placements`) != "1" {
		t.Fatal("something started before the capacity check")
	}
	yes := true
	id, state, reason := m.deploy(t, orchestrator.DeployParams{AllowDowntimeOnLowCapacity: &yes})
	if state != "live" || m.q(t, `SELECT strategy FROM deployments WHERE id = $1`, id) != "recreate" ||
		!strings.Contains(m.q(t, `SELECT warnings::text FROM deployments WHERE id = $1`, id), "zero_downtime_not_available") {
		t.Fatalf("fallback: %s %s", state, reason)
	}
}

// AC-DEPLOY-06: cancel in health_checking removes green. Blue serves.
func TestAC_DEPLOY_06_CancelInHealthChecking(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	release := make(chan struct{})
	defer close(release)
	m.sim.set(func() {
		m.sim.check = func(*pb.CheckHealth) *pb.CommandAck {
			select {
			case <-release:
			case <-time.After(20 * time.Second):
			}
			return &pb.CommandAck{ErrorCode: "health_timeout", ErrorMessage: "late"}
		}
	})
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID})
	if err != nil {
		t.Fatal(err)
	}
	m.waitState(t, id, "health_checking")
	if err := m.ctl.Cancel(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	if st := m.q(t, `SELECT state FROM deployments WHERE id = $1`, id); st != "cancelled" {
		t.Fatalf("state %s", st)
	}
	if m.q(t, `SELECT count(*) FROM placements`) != "1" || m.q(t, `SELECT role FROM placements`) != "live" ||
		m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != m.releaseID(t, 1) {
		t.Fatal("green is not removed or blue does not serve")
	}
	if m.sim.count("remove:") != 1 {
		t.Fatalf("commands %v", m.sim.cmds)
	}
	// A finished deployment cannot be cancelled.
	if err := m.ctl.Cancel(context.Background(), id, ""); !errors.Is(err, orchestrator.ErrNotCancellable) {
		t.Fatalf("second cancel: %v", err)
	}
}

// In switching a cancel is refused (409 deployment_not_cancellable).
func TestCancelRefusedInSwitching(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	release := make(chan struct{})
	m.sim.set(func() {
		m.sim.swtch = func() *pb.CommandAck {
			<-release
			return &pb.CommandAck{Ok: true}
		}
	})
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID})
	if err != nil {
		t.Fatal(err)
	}
	m.waitState(t, id, "switching")
	if err := m.ctl.Cancel(context.Background(), id, ""); !errors.Is(err, orchestrator.ErrNotCancellable) {
		t.Fatalf("cancel in switching: %v", err)
	}
	close(release)
	m.waitState(t, id, "live")
}

// AC-DEPLOY-08: pre_deploy exits with 1. Green never starts. The log is kept.
func TestAC_DEPLOY_08_PreDeployFails(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.exec(t, `UPDATE services SET config = '{"image":"nginx:1.27","port":8080,"pre_deploy":["./migrate"],"strategy_options":{"drain_seconds":0}}' WHERE id = $1`, m.serviceID)
	m.sim.set(func() {
		m.sim.oneoff = func() *pb.CommandAck {
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"exit_code":1,"logs":"migration failed\n"}`)}
		}
	})
	checks := m.sim.count("check:")
	id, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "failed" || reason != "pre_deploy_failed" {
		t.Fatalf("%s %s", state, reason)
	}
	if m.sim.count("check:") != checks || m.q(t, `SELECT count(*) FROM placements`) != "1" {
		t.Fatal("green started")
	}
	if got := m.q(t, `SELECT string_agg(line, ',') FROM deployment_logs WHERE deployment_id = $1 AND stream = 'pre_deploy'`, id); !strings.Contains(got, "migration failed") {
		t.Fatalf("pre_deploy log: %s", got)
	}
	// With exit code 0 it runs once, before green starts (REQ-DEPLOY-109).
	m.sim.set(func() { m.sim.oneoff = nil })
	m.mustDeploy(t)
	if m.sim.count("oneoff") != 2 {
		t.Fatalf("pre_deploy runs: %v", m.sim.cmds)
	}
}

// AC-REL-01, 02, 04, 05, 07: rollback to v2 is fast (no pull), goes live, restores the variables of v2, changes no domain or volume.
func TestAC_REL_RollbackFastAndNormal(t *testing.T) {
	m := setupM3(t, nil)
	var prj string
	_ = m.env.Pool.QueryRow(context.Background(), `SELECT e.project_id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.id = $1`, m.serviceID).Scan(&prj)
	m.exec(t, `INSERT INTO volumes (id, service_id, name, kind, mount_path, access_mode) VALUES ($1,$2,'cache','volume','/cache','shared')`, gateway.NewID("vol"), m.serviceID)
	setVar := func(v string) {
		m.exec(t, `INSERT INTO variables (id, project_id, name, value) VALUES ($1,$2,'FOO',$3)
			ON CONFLICT (project_id, COALESCE(environment_id,''), COALESCE(service_id,''), name) DO UPDATE SET value = $3`, gateway.NewID("var"), prj, v)
	}
	for _, v := range []string{"one", "two", "three"} {
		setVar(v)
		m.mustDeploy(t)
	}
	v1, v2, v3 := m.releaseID(t, 1), m.releaseID(t, 2), m.releaseID(t, 3)
	for _, r := range []string{v1, v2, v3} {
		m.exec(t, `UPDATE placements SET container_id = 'c-' || id WHERE release_id = $1`, r) // the agent reported the containers
	}
	// Only the set of v2 is kept (v1 was removed): a fast rollback to v2.
	if fast := m.q(t, `SELECT fast_rollback_possible FROM (SELECT (EXISTS (SELECT 1 FROM placements WHERE release_id = $1 AND role = 'stopped')) AS fast_rollback_possible) x`, v2); fast != "true" {
		t.Fatal("v2 has no kept container")
	}
	pulls := m.sim.count("pull")
	start := time.Now()
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, RollbackReleaseID: v2})
	if err != nil {
		t.Fatal(err)
	}
	if st, reason := m.waitDeployment(t, id); st != "live" {
		t.Fatalf("rollback %s %s", st, reason)
	}
	t.Logf("fast rollback took %s", time.Since(start))
	if m.sim.count("pull") != pulls {
		t.Fatal("the fast rollback pulled an image")
	}
	if m.q(t, `SELECT trigger FROM deployments WHERE id = $1`, id) != "rollback" || m.q(t, `SELECT release_id FROM deployments WHERE id = $1`, id) != v2 ||
		m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v2 || m.q(t, `SELECT count(*) FROM releases WHERE service_id = $1`, m.serviceID) != "3" {
		t.Fatal("rollback state")
	}
	if m.q(t, `SELECT count(*) FROM deployment_builds WHERE deployment_id = $1`, id) != "0" {
		t.Fatal("a build ran")
	}
	ds := lastState(t, m.agent)
	for _, w := range ds.Workloads {
		if w.ReleaseId == v2 && (w.Role != "live" || w.Env["FOO"] != "two") {
			t.Fatalf("workload of v2: role %s FOO %s", w.Role, w.Env["FOO"])
		}
	}
	if m.q(t, `SELECT count(*) FROM domains WHERE service_id = $1`, m.serviceID) != "1" || m.q(t, `SELECT count(*) FROM volumes WHERE service_id = $1`, m.serviceID) != "1" ||
		m.q(t, `SELECT count(*) FILTER (WHERE role = 'live') FROM placements WHERE service_id = $1`, m.serviceID) != "1" {
		t.Fatal("domains, volumes or replicas changed")
	}
	// AC-REL-07: the live release cannot be rolled back to.
	if _, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, RollbackReleaseID: v2}); !errors.Is(err, orchestrator.ErrAlreadyLive) {
		t.Fatalf("already live: %v", err)
	}
	// Normal rollback to v1: its containers are gone, so the image is pulled by digest.
	var ref string
	_ = m.env.Pool.QueryRow(context.Background(), `SELECT image_ref FROM releases WHERE id = $1`, v1).Scan(&ref)
	id, _, err = m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, RollbackReleaseID: v1})
	if err != nil {
		t.Fatal(err)
	}
	if st, reason := m.waitDeployment(t, id); st != "live" {
		t.Fatalf("normal rollback %s %s", st, reason)
	}
	if m.sim.count("pull") != pulls+1 || m.sim.pulls[len(m.sim.pulls)-1] != ref {
		t.Fatalf("pulls %v want %s", m.sim.pulls, ref)
	}
	ds = lastState(t, m.agent)
	for _, w := range ds.Workloads {
		if w.ReleaseId == v1 && w.Env["FOO"] != "one" {
			t.Fatalf("FOO of v1: %s", w.Env["FOO"])
		}
	}
}

// AC-REL-03: a release with a pruned image cannot be rolled back to. Old releases get pruned_at after retain_releases.
func TestAC_REL_03_PrunedRelease(t *testing.T) {
	m := setupM3(t, nil)
	m.exec(t, `UPDATE projects SET settings = '{"retain_releases": 2}'`)
	for i := 0; i < 4; i++ {
		m.mustDeploy(t)
	}
	if got := m.q(t, `SELECT string_agg(number::text, ',' ORDER BY number) FROM releases WHERE pruned_at IS NOT NULL`); got != "1,2" {
		t.Fatalf("pruned releases: %s", got)
	}
	if m.sim.count("prune") == 0 {
		t.Fatal("no image gc command")
	}
	if _, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, RollbackReleaseID: m.releaseID(t, 1)}); !errors.Is(err, orchestrator.ErrImagePruned) {
		t.Fatalf("pruned: %v", err)
	}
}

// AC-REL-08: a destroyed secret version stops the rollback before anything starts.
func TestAC_REL_08_DestroyedSecretVersion(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.mustDeploy(t)
	v1 := m.releaseID(t, 1)
	var prj string
	_ = m.env.Pool.QueryRow(context.Background(), `SELECT e.project_id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.id = $1`, m.serviceID).Scan(&prj)
	m.exec(t, `INSERT INTO secrets (id, project_id, name) VALUES ('sec_x', $1, 'DB_PASSWORD')`, prj)
	m.exec(t, `INSERT INTO secret_versions (secret_id, version, nonce, ciphertext, destroyed_at) VALUES ('sec_x', 1, '\x00', '\x00', now())`)
	m.exec(t, `UPDATE releases SET secret_versions = '{"DB_PASSWORD":1}', config_snapshot = jsonb_set(config_snapshot, '{secret_refs}', '{"DB_PASSWORD":"sec_x"}') WHERE id = $1`, v1)
	cmds := len(m.sim.cmds)
	if _, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID, RollbackReleaseID: v1}); !errors.Is(err, orchestrator.ErrSecretDestroyed) {
		t.Fatalf("destroyed secret: %v", err)
	}
	if len(m.sim.cmds) != cmds || m.q(t, `SELECT count(*) FROM deployments`) != "2" {
		t.Fatal("something started")
	}
}

// A newer request replaces the queued ones (08 section 10).
func TestQueueSupersedesOlderQueued(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	release := make(chan struct{})
	m.sim.set(func() {
		m.sim.check = func(*pb.CheckHealth) *pb.CommandAck {
			<-release
			return &pb.CommandAck{Ok: true}
		}
	})
	ctx := context.Background()
	a, _, _ := m.ctl.DeployWith(ctx, orchestrator.DeployParams{ServiceID: m.serviceID})
	m.waitState(t, a, "health_checking")
	b, _, _ := m.ctl.DeployWith(ctx, orchestrator.DeployParams{ServiceID: m.serviceID})
	c, _, _ := m.ctl.DeployWith(ctx, orchestrator.DeployParams{ServiceID: m.serviceID})
	if m.q(t, `SELECT state FROM deployments WHERE id = $1`, b) != "superseded" || m.q(t, `SELECT state FROM deployments WHERE id = $1`, c) != "queued" {
		t.Fatalf("queue: %s %s", m.q(t, `SELECT state FROM deployments WHERE id = $1`, b), m.q(t, `SELECT state FROM deployments WHERE id = $1`, c))
	}
	close(release)
	m.waitState(t, a, "live")
	m.waitState(t, c, "live")
	if m.q(t, `SELECT failure_reason FROM deployments WHERE id = $1`, b) != "superseded" {
		t.Fatal("reason")
	}
}

// REQ-DEPLOY-107: the server dies in switching. The next server finishes the deployment (the switch was saved).
func TestChaosKillServerInSwitching(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	block := make(chan struct{})
	m.sim.set(func() {
		m.sim.swtch = func() *pb.CommandAck {
			<-block
			return &pb.CommandAck{Ok: true}
		}
	})
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID})
	if err != nil {
		t.Fatal(err)
	}
	m.waitState(t, id, "switching")
	for deadline := time.Now().Add(10 * time.Second); m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != m.q(t, `SELECT release_id FROM deployments WHERE id = $1`, id); {
		if time.Now().After(deadline) {
			t.Fatal("the switch was not saved")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.ctl.Close() // the server dies: the run stops, the state stays switching
	if st := m.q(t, `SELECT state FROM deployments WHERE id = $1`, id); st != "switching" {
		t.Fatalf("after the kill: %s", st)
	}
	m.sim.set(func() { m.sim.swtch = nil })
	close(block)

	ctl2 := control.New(control.Deps{DB: m.env.Pool, Clock: m.env.Clock, Config: m.env.Cfg, CA: m.env.CA, Sender: m.env.GW})
	t.Cleanup(ctl2.Close)
	m.env.GW.State, m.env.GW.Health = ctl2, ctl2
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = ctl2.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	m.waitState(t, id, "live") // the switch was saved: the deployment goes forward
	v1, v2 := m.releaseID(t, 1), m.releaseID(t, 2)
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v2 || m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v2) != "live" ||
		m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v1) != "stopped" {
		t.Fatal("the end state is not consistent")
	}
}

// REQ-DEPLOY-107: a switch that was not saved is rolled back, and a deployment before the switch fails cleanly.
func TestRecoveryBeforeTheSwitch(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.mustDeploy(t)
	v1, v2 := m.releaseID(t, 1), m.releaseID(t, 2) // v2 is live. The deployments of the test went to v1.
	ctx := context.Background()
	for _, state := range []string{"switching", "health_checking"} {
		dep, plc := gateway.NewID("dep"), gateway.NewID("plc")
		m.exec(t, `INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state, release_id, previous_release_id)
			SELECT $1, s.id, s.environment_id, 'manual', 'blue_green', $2, $3, $5 FROM services s WHERE s.id = $4`, dep, state, v1, m.serviceID, v2)
		m.exec(t, `INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role, deployment_id)
			SELECT $1, $2, node_id, $3, 0, 'green', 'candidate', $4 FROM placements WHERE service_id = $2 LIMIT 1`, plc, m.serviceID, v1, dep)
		ctl2 := control.New(control.Deps{DB: m.env.Pool, Clock: m.env.Clock, Config: m.env.Cfg, CA: m.env.CA, Sender: m.env.GW})
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = ctl2.Run(rctx); close(done) }()
		want := "failed"
		if state == "switching" {
			want = "rolled_back"
		}
		m.waitState(t, dep, want)
		cancel()
		<-done
		ctl2.Close()
		if m.q(t, `SELECT role FROM placements WHERE id = $1`, plc) != "stopped" || m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v2 {
			t.Fatalf("%s: green is not stopped or blue does not serve", state)
		}
		m.exec(t, `DELETE FROM placements WHERE id = $1`, plc)
	}
}

// AC-HC-03, AC-HC-04: an unhealthy live replica leaves the upstreams. When all are unhealthy, all stay and the alert is critical.
func TestAC_HC_03_04_UnhealthyReplicas(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	v1 := m.releaseID(t, 1)
	first := m.placementOf(t, v1)
	second := gateway.NewID("plc")
	m.exec(t, `INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role, health)
		SELECT $1, service_id, node_id, release_id, 1, color, 'live', 'healthy' FROM placements WHERE id = $2`, second, first)
	if err := m.ctl.DomainsChanged(context.Background(), m.serviceID); err != nil {
		t.Fatal(err)
	}
	upstreams := func() []string {
		ds := lastState(t, m.agent)
		var out []string
		for _, u := range routeByID(ds, m.q(t, `SELECT id FROM domains WHERE service_id = $1`, m.serviceID)).Upstreams {
			out = append(out, u.Address)
		}
		return out
	}
	if len(upstreams()) != 2 {
		t.Fatalf("upstreams %v", upstreams())
	}
	m.exec(t, `UPDATE placements SET health = 'unhealthy' WHERE id = $1`, second)
	m.ctl.HealthChanged(context.Background(), m.agent.NodeID, second, "healthy", "unhealthy")
	if got := upstreams(); len(got) != 1 || got[0] != "pp-"+first+":8080" {
		t.Fatalf("upstreams after one unhealthy: %v", got)
	}
	m.exec(t, `UPDATE placements SET health = 'unhealthy' WHERE id = $1`, first)
	m.ctl.HealthChanged(context.Background(), m.agent.NodeID, first, "healthy", "unhealthy")
	if got := upstreams(); len(got) != 2 {
		t.Fatalf("all unhealthy must keep routing: %v", got)
	}
	var sev string
	if err := m.env.Pool.QueryRow(context.Background(), `SELECT payload->>'severity' FROM events WHERE topic = 'alert' AND object_id = $1 ORDER BY id DESC LIMIT 1`, m.serviceID).Scan(&sev); err != nil || sev != "critical" {
		t.Fatalf("alert severity %q %v", sev, err)
	}
	// It rejoins when healthy again.
	m.exec(t, `UPDATE placements SET health = 'healthy' WHERE id = $1`, second)
	m.ctl.HealthChanged(context.Background(), m.agent.NodeID, second, "unhealthy", "healthy")
	if got := upstreams(); len(got) != 1 || got[0] != "pp-"+second+":8080" {
		t.Fatalf("after recovery: %v", got)
	}
}

// AC-HC-02: without a health check a service with a port gets tcp on the port (REQ-HC-003).
func TestAC_HC_02_DefaultTCPCheck(t *testing.T) {
	m := setupM3(t, nil)
	m.exec(t, `UPDATE services SET config = '{"image":"nginx:1.27","port":9000,"strategy_options":{"drain_seconds":0}}' WHERE id = $1`, m.serviceID)
	m.mustDeploy(t)
	ds := lastState(t, m.agent)
	h := ds.Workloads[0].Health
	if h == nil || h.Type != "tcp" || h.Port != 9000 || h.HealthyThreshold != 3 || h.StartPeriodSeconds != 30 {
		t.Fatalf("health spec %v", h)
	}
}

// The switch is undone when an ingress node does not ack in switch_ack_timeout (08 section 5.3).
func TestSwitchNotAckedRollsBack(t *testing.T) {
	m := setupM3(t, func(d *control.Deps) { d.SwitchAckTimeout = 300 * time.Millisecond })
	m.mustDeploy(t)
	m.sim.set(func() {
		first := true
		m.sim.swtch = func() *pb.CommandAck {
			if first {
				first = false
				time.Sleep(time.Second)
				return &pb.CommandAck{Ok: true}
			}
			return &pb.CommandAck{Ok: true}
		}
	})
	_, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "rolled_back" || reason != "switch_failed" {
		t.Fatalf("%s %s", state, reason)
	}
	v1 := m.releaseID(t, 1)
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v1 || m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v1) != "live" {
		t.Fatal("blue is not live again")
	}
	if m.q(t, `SELECT count(*) FROM placements WHERE role = 'live'`) != "1" {
		t.Fatal("two live placements")
	}
}

// The post-switch check sends traffic back to blue when the route fails (REQ-DEPLOY-110).
func TestPostSwitchCheckRollsBack(t *testing.T) {
	m := setupM3(t, nil)
	m.mustDeploy(t)
	m.agent.OnCommand = func(c *pb.Command) *pb.CommandAck {
		if _, ok := c.Body.(*pb.Command_ProbeRoute); ok {
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"total":100,"failed":50}`)}
		}
		return m.sim.on(c)
	}
	_, state, reason := m.deploy(t, orchestrator.DeployParams{})
	if state != "rolled_back" || reason != "gate_5xx" {
		t.Fatalf("%s %s", state, reason)
	}
	v1 := m.releaseID(t, 1)
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != v1 || m.q(t, `SELECT role FROM placements WHERE release_id = $1`, v1) != "live" {
		t.Fatal("blue is not live again")
	}
}
