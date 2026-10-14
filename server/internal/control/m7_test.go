// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
)

// node7 is a fake node with a recorder of its commands and its newest desired state.
type node7 struct {
	a    *gatewaytest.Agent
	sim  *sim
	last atomic.Pointer[pb.DesiredState]
	name string
}

func (n *node7) state(t *testing.T) *pb.DesiredState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ds := n.last.Load(); ds != nil {
			time.Sleep(100 * time.Millisecond) // let a push that is on its way arrive
			return n.last.Load()
		}
		if time.Now().After(deadline) {
			t.Fatal("no desired state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeMover is the data copy of the backup service.
type fakeMover struct {
	mu        sync.Mutex
	snapshots int
	restored  []string
	when      time.Time
	has       bool
}

func (m *fakeMover) SnapshotVolumes(context.Context, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots++
	return nil
}

func (m *fakeMover) LatestBackup(context.Context, string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.when, m.has, nil
}

func (m *fakeMover) RestoreVolumes(_ context.Context, _, node string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restored = append(m.restored, node)
	return nil
}

type m7 struct {
	*fixture
	nodes map[string]*node7
	mover *fakeMover
	runWG sync.WaitGroup
}

// setupM7 starts the control loop with a fast monitor and a fake data mover.
func setupM7(t *testing.T) *m7 { return setupM7With(t, nil) }

// setupM7With is setupM7 with a change of the control deps.
func setupM7With(t *testing.T, tweak func(*control.Deps)) *m7 {
	t.Helper()
	mover := &fakeMover{}
	f := setupWith(t, func(d *control.Deps) {
		d.MonitorInterval = 20 * time.Millisecond
		d.PostSwitchCheck = 50 * time.Millisecond
		d.Mover = mover
		if tweak != nil {
			tweak(d)
		}
	})
	f.env.GW.Joined = f.ctl.NodeJoined
	m := &m7{fixture: f, nodes: map[string]*node7{}, mover: mover}
	ctx, cancel := context.WithCancel(context.Background())
	m.runWG.Add(1)
	go func() { defer m.runWG.Done(); _ = f.ctl.Run(ctx) }()
	t.Cleanup(func() { cancel(); m.runWG.Wait() })
	return m
}

// addNode joins and connects a node with a WireGuard key.
func (m *m7) addNode(t *testing.T, name string) *node7 {
	t.Helper()
	key, csr := gatewaytest.NewKeyAndCSR(t)
	token := m.env.AddToken(t, name, time.Hour)
	resp, err := pb.NewAgentJoinClient(m.env.JoinConn(t)).Join(context.Background(), &pb.JoinRequest{
		JoinToken: token, CsrPem: csr, WireguardPublicKey: "PUB-" + name,
		NodeInfo: &pb.NodeInfo{Hostname: name, Os: "linux", Arch: "amd64", AgentVersion: "test", PublicIp: "203.0.113." + fmt.Sprint(len(m.nodes)+10),
			CpuMillis: 4000, MemoryBytes: 8 << 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &gatewaytest.Agent{Env: m.env, NodeID: resp.NodeId, CertPEM: resp.ClientCertPem, Key: key,
		States: make(chan *pb.DesiredState, 64), Commands: make(chan *pb.Command, 64)}
	n := &node7{a: a, sim: newSim(a), name: name}
	a.Connect(t)
	go func() {
		for ds := range a.States {
			// Like the real agent, keep the newest version. The first state of a Hello can arrive after a newer push.
			if old := n.last.Load(); old == nil || ds.Version >= old.Version {
				n.last.Store(ds)
			}
		}
	}()
	m.nodes[name] = n
	// The node is ready after the Hello.
	m.waitNode(t, n, "ready")
	return n
}

func (m *m7) waitNode(t *testing.T, n *node7, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := m.q(t, `SELECT state FROM nodes WHERE id = $1`, n.a.NodeID); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("node %s is %s, want %s", n.name, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (m *m7) q(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var v any
	if err := m.env.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(v)
}

func (m *m7) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := m.env.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (m *m7) deploy(t *testing.T, p orchestrator.DeployParams) string {
	t.Helper()
	p.ServiceID = m.serviceID
	id, _, err := m.ctl.DeployWith(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := m.waitDeployment(t, id); state != "live" {
		t.Fatalf("deployment %s: %s %s: %s", id, state, reason, m.q(t, `SELECT COALESCE(failure_detail,'') FROM deployments WHERE id = $1`, id))
	}
	return id
}

// nodesOfService lists the node names of the placements that are not stopped.
func (m *m7) nodesOfService(t *testing.T) string {
	t.Helper()
	return m.q(t, `SELECT COALESCE(string_agg(n.name, ',' ORDER BY n.name), '') FROM placements p JOIN nodes n ON n.id = p.node_id
		WHERE p.service_id = $1 AND p.role <> 'stopped'`, m.serviceID)
}

func (m *m7) addDomain(t *testing.T) string {
	id := gateway.NewID("dom")
	m.exec(t, `INSERT INTO domains (id, service_id, host, container_port) VALUES ($1,$2,'app.example.com',8080)`, id, m.serviceID)
	return id
}

// beat sends heartbeats of the given nodes.
func (m *m7) beat(t *testing.T, nodes ...*node7) {
	t.Helper()
	for _, n := range nodes {
		n.a.Heartbeat(t)
	}
	time.Sleep(50 * time.Millisecond)
}

// failNode lets the fake clock run until the node is down, while the other nodes keep sending heartbeats.
func (m *m7) failNode(t *testing.T, dead *node7) {
	t.Helper()
	var alive []*node7
	for _, n := range m.nodes {
		if n != dead {
			alive = append(alive, n)
		}
	}
	for i := 0; i < 7; i++ {
		m.env.Clock.Advance(10 * time.Second)
		m.beat(t, alive...)
	}
	m.waitNode(t, dead, "down")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// AC-NODE-01 (server side): a joined node has a mesh subnet of its own and a mesh address, and every node gets the
// new peer in its desired state (REQ-AGENT-008).
func TestAC_NODE_01_JoinMeshAndPeers(t *testing.T) {
	m := setupM7(t)
	a := m.addNode(t, "n-a")
	b := m.addNode(t, "n-b")
	subA, subB := m.q(t, `SELECT mesh_subnet FROM nodes WHERE id = $1`, a.a.NodeID), m.q(t, `SELECT mesh_subnet FROM nodes WHERE id = $1`, b.a.NodeID)
	if subA == subB || !strings.HasSuffix(subA, "/24") || !strings.HasSuffix(subB, "/24") {
		t.Fatalf("subnets %s %s", subA, subB)
	}
	if ip := m.q(t, `SELECT host(mesh_ip) FROM nodes WHERE id = $1`, b.a.NodeID); !strings.HasSuffix(ip, ".1") {
		t.Fatalf("mesh ip %s", ip)
	}
	waitFor(t, "the new peer reaches node a", func() bool {
		ds := a.last.Load()
		return ds != nil && len(ds.Peers) == 1
	})
	p := a.last.Load().Peers[0]
	if p.NodeId != b.a.NodeID || p.PublicKey != "PUB-n-b" || p.AllowedIps != subB || !strings.HasSuffix(p.Endpoint, ":51820") {
		t.Fatalf("peer of a: %v", p)
	}
	if ds := b.state(t); len(ds.Peers) != 1 || ds.Peers[0].NodeId != a.a.NodeID || ds.MeshCidr != "10.210.0.0/16" {
		t.Fatalf("peer of b: %v", ds.Peers)
	}
}

// AC-NODE-03: a stateless service moves to another node with blue-green: the new container is live and acked on the
// ingress before the old one is stopped, and the old one is removed.
func TestAC_NODE_03_MoveStateless(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	dom := m.addDomain(t)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-a" {
		t.Fatalf("first placement on %s", got)
	}
	oldPlacement := m.q(t, `SELECT id FROM placements WHERE service_id = $1`, m.serviceID)
	release := m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID)

	id, _, err := m.ctl.MoveService(context.Background(), m.serviceID, b.a.NodeID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := m.waitDeployment(t, id); state != "live" {
		t.Fatalf("move: %s %s %s", state, reason, m.q(t, `SELECT COALESCE(failure_detail,'') FROM deployments WHERE id = $1`, id))
	}
	if m.q(t, `SELECT trigger || '/' || strategy || '/' || target_node_id FROM deployments WHERE id = $1`, id) != "move/blue_green/"+b.a.NodeID {
		t.Fatalf("deployment: %s", m.q(t, `SELECT trigger || '/' || strategy FROM deployments WHERE id = $1`, id))
	}
	if got := m.nodesOfService(t); got != "n-b" {
		t.Fatalf("the container runs on %s", got)
	}
	if m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID) != release {
		t.Fatal("a move keeps the release")
	}
	// No downtime: the ingress acked the switch before the old container was stopped, then it was removed.
	sw, rm := a.sim.index("switch"), a.sim.index("remove:"+oldPlacement)
	if sw < 0 || rm < sw {
		t.Fatalf("old container removed before the ingress switch: %v", a.sim.cmds)
	}
	if got := m.q(t, `SELECT count(*) FROM placements WHERE id = $1`, oldPlacement); got != "0" {
		t.Fatal("the old placement must be gone")
	}
	// Both ingress nodes route to the new container by its mesh address (REQ-NODE-010).
	newIP := m.q(t, `SELECT host(ip) FROM placements WHERE service_id = $1`, m.serviceID)
	for _, n := range []*node7{a, b} {
		r := routeByID(n.state(t), dom)
		if r == nil || len(r.Upstreams) != 1 {
			t.Fatalf("%s route: %v", n.name, r)
		}
		want := newIP + ":8080"
		if n == b {
			want = "pp-" + m.q(t, `SELECT id FROM placements WHERE service_id = $1`, m.serviceID) + ":8080"
		}
		if r.Upstreams[0].Address != want {
			t.Fatalf("%s upstream %s, want %s", n.name, r.Upstreams[0].Address, want)
		}
	}
	// The audit event of the move.
	if got := m.q(t, `SELECT count(*) FROM audit_events WHERE action = 'service.move' AND target_id = $1`, m.serviceID); got != "1" {
		t.Fatalf("audit events of the move: %s", got)
	}
	// A move to the node it is on, or to a node that is not ready, is refused.
	if _, _, err := m.ctl.MoveService(context.Background(), m.serviceID, b.a.NodeID, "", ""); !errors.Is(err, orchestrator.ErrNodeState) {
		t.Fatalf("same node: %v", err)
	}
}

// REQ-NODE-004: the placement settings (names, labels, spread) and the replicas decide where the containers go.
func TestREQ_NODE_004_PlacementSettingsAndReplicas(t *testing.T) {
	m := setupM7(t)
	m.addNode(t, "n-a")
	b, c := m.addNode(t, "n-b"), m.addNode(t, "n-c")
	m.exec(t, `UPDATE nodes SET labels = labels || '{"zone":"east"}' WHERE id = $1`, c.a.NodeID)
	m.exec(t, `UPDATE services SET config = config || '{"placement":{"labels":{"zone":"east"}}}' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-c" {
		t.Fatalf("the label zone=east allows only n-c, got %s", got)
	}
	m.exec(t, `UPDATE services SET config = config || '{"placement":{"nodes":["n-b"]}}' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-b" {
		t.Fatalf("the name list allows only n-b, got %s", got)
	}
	_ = b
	// Replicas with spread: one on each of three nodes. Every replica has its own fixed address.
	m.exec(t, `UPDATE services SET config = (config - 'placement') || '{"replicas":3}' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-a,n-b,n-c" {
		t.Fatalf("3 replicas on 3 nodes: %s", got)
	}
	if got := m.q(t, `SELECT count(DISTINCT ip) FROM placements WHERE service_id = $1 AND role = 'live'`, m.serviceID); got != "3" {
		t.Fatalf("addresses: %s", got)
	}
	// A route has the three upstreams on every ingress node.
	dom := m.addDomain(t)
	if err := m.ctl.DomainsChanged(context.Background(), m.serviceID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if r := routeByID(c.state(t), dom); r == nil || len(r.Upstreams) != 3 {
		t.Fatalf("upstreams: %v", r)
	}
	// No node has the label: the deployment fails with node_unavailable and says what is missing.
	m.exec(t, `UPDATE services SET config = config || '{"replicas":1,"placement":{"labels":{"gpu":"yes"}}}' WHERE id = $1`, m.serviceID)
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID})
	if err != nil {
		t.Fatal(err)
	}
	state, reason := m.waitDeployment(t, id)
	if state != "failed" || reason != "node_unavailable" || !strings.Contains(m.q(t, `SELECT failure_detail FROM deployments WHERE id = $1`, id), "missing labels gpu=yes") {
		t.Fatalf("%s %s %s", state, reason, m.q(t, `SELECT failure_detail FROM deployments WHERE id = $1`, id))
	}
}

// AC-NODE-08: drain moves every placement away and the node ends in maintenance.
func TestAC_NODE_08_DrainEndsInMaintenance(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.addDomain(t)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-a" {
		t.Fatal(got)
	}
	ctx := context.Background()
	// A node with workloads cannot be deleted without force (REQ-NODE-008).
	if err := m.ctl.DeleteNode(ctx, m.env.OrgID, a.a.NodeID, false, ""); !errors.Is(err, orchestrator.ErrNodeNotEmpty) {
		t.Fatalf("delete: %v", err)
	}
	if err := m.ctl.DrainNode(ctx, m.env.OrgID, a.a.NodeID, ""); err != nil {
		t.Fatal(err)
	}
	if m.q(t, `SELECT state FROM nodes WHERE id = $1`, a.a.NodeID) != "draining" {
		t.Fatal("the node must be draining at once")
	}
	m.waitNode(t, a, "maintenance")
	if got := m.nodesOfService(t); got != "n-b" {
		t.Fatalf("after the drain the service runs on %s", got)
	}
	if m.q(t, `SELECT count(*) FROM placements WHERE node_id = $1`, a.a.NodeID) != "0" {
		t.Fatal("the node must be empty")
	}
	if got := m.q(t, `SELECT count(*) FROM audit_events WHERE action IN ('node.drain','node.drained') AND target_id = $1`, a.a.NodeID); got != "2" {
		t.Fatalf("audit of the drain: %s", got)
	}
	// A node in maintenance is not scheduled, and it comes back with maintenance off.
	m.exec(t, `UPDATE services SET config = config || '{"replicas":2}' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-b,n-b" { // two replicas on the only node that is ready (F06: 2+0 and a warning)
		t.Fatalf("n-a is in maintenance: %s", got)
	}
	if err := m.ctl.SetMaintenance(ctx, m.env.OrgID, a.a.NodeID, false, ""); err != nil {
		t.Fatal(err)
	}
	m.waitNode(t, a, "ready")
	_ = b
}

// REQ-NODE-003: cordon stops scheduling, existing workloads stay; uncordon undoes it. Wrong states are refused.
func TestREQ_NODE_003_CordonAndUncordon(t *testing.T) {
	m := setupM7(t)
	a := m.addNode(t, "n-a")
	m.addNode(t, "n-b")
	m.deploy(t, orchestrator.DeployParams{})
	ctx := context.Background()
	if err := m.ctl.CordonNode(ctx, m.env.OrgID, a.a.NodeID, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ctl.CordonNode(ctx, m.env.OrgID, a.a.NodeID, ""); !errors.Is(err, orchestrator.ErrNodeState) {
		t.Fatalf("cordon twice: %v", err)
	}
	if got := m.nodesOfService(t); got != "n-a" {
		t.Fatalf("existing workloads stay: %s", got)
	}
	// A deploy of a service with replicas 1 keeps its node only when the node is ready: now it moves to n-b.
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-b" {
		t.Fatalf("a cordoned node gets no new workloads: %s", got)
	}
	if err := m.ctl.UncordonNode(ctx, m.env.OrgID, a.a.NodeID, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ctl.UncordonNode(ctx, m.env.OrgID, a.a.NodeID, ""); !errors.Is(err, orchestrator.ErrNodeState) {
		t.Fatalf("uncordon twice: %v", err)
	}
}

// AC-NODE-04: a stateless service with failover auto runs on another node after its node went down, with an audit
// event and an alert. The old placement is gone (split-brain guard).
func TestAC_NODE_04_07_FailoverStateless(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	dom := m.addDomain(t)
	m.exec(t, `UPDATE services SET failover = 'auto' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if got := m.nodesOfService(t); got != "n-a" {
		t.Fatal(got)
	}
	m.failNode(t, a)
	waitFor(t, "the service runs on n-b", func() bool { return m.nodesOfService(t) == "n-b" })
	waitFor(t, "the failover deployment is live", func() bool {
		return m.q(t, `SELECT COALESCE(string_agg(state, ','), '') FROM deployments WHERE trigger = 'failover'`) == "live"
	})
	if got := m.q(t, `SELECT count(*) FROM placements WHERE node_id = $1`, a.a.NodeID); got != "0" {
		t.Fatalf("the old placement must be gone: %s", got)
	}
	// The audit event is written when the failover is done, a moment after the deployment is live.
	waitFor(t, "the audit event of the failover", func() bool {
		return m.q(t, `SELECT count(*) FROM audit_events WHERE action = 'service.failover' AND actor_type = 'system' AND target_id = $1`, m.serviceID) == "1"
	})
	if got := m.q(t, `SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' IN ('node_down','service_failover')`); got < "2" {
		t.Fatalf("alerts: %s", got)
	}
	if m.q(t, `SELECT failover_state FROM services WHERE id = $1`, m.serviceID) != "moved" {
		t.Fatalf("service state: %s", m.q(t, `SELECT failover_state FROM services WHERE id = $1`, m.serviceID))
	}
	// The ingress on b serves the new container; the down node is not an upstream.
	r := routeByID(b.state(t), dom)
	if r == nil || len(r.Upstreams) != 1 || !strings.HasPrefix(r.Upstreams[0].Address, "pp-") {
		t.Fatalf("route on b: %v", r)
	}

	// AC-NODE-07 (server side): node a returns. Its desired state holds no workload of the service, so the agent
	// removes the old container and does not start it.
	m.env.Clock.Advance(time.Second)
	a.a.Heartbeat(t)
	m.waitNode(t, a, "ready")
	time.Sleep(300 * time.Millisecond)
	ds := a.state(t)
	if len(ds.Workloads) != 0 {
		t.Fatalf("a returned node must get no workload of the moved service: %v", ds.Workloads)
	}
	// And the control plane never has the service on two nodes.
	if m.nodesOfService(t) != "n-b" {
		t.Fatal("the service must stay on n-b")
	}
}

// AC-NODE-05 and REQ-NODE-007: a service with failover none, or with a local volume and no opt-in, is not moved,
// and an alert says why.
func TestAC_NODE_05_FailoverBlocked(t *testing.T) {
	m := setupM7(t)
	a := m.addNode(t, "n-a")
	m.addNode(t, "n-b")
	m.exec(t, `INSERT INTO volumes (id, service_id, name, kind, mount_path) VALUES ('vol_x', $1, 'data', 'volume', '/data')`, m.serviceID)
	m.exec(t, `UPDATE services SET failover = 'auto', allow_data_loss_failover = false WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	if m.q(t, `SELECT node_id FROM volumes WHERE id = 'vol_x'`) != a.a.NodeID {
		t.Fatal("the volume is pinned to the first node")
	}
	m.failNode(t, a)
	waitFor(t, "failover is blocked", func() bool {
		return m.q(t, `SELECT failover_state FROM services WHERE id = $1`, m.serviceID) == "blocked"
	})
	reason := m.q(t, `SELECT failover_reason FROM services WHERE id = $1`, m.serviceID)
	if !strings.Contains(reason, "allow_data_loss_failover is off") {
		t.Fatalf("reason: %s", reason)
	}
	// The state is saved before the audit event and the alert, so wait for the alert.
	const blockedAlert = `SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' = 'failover_blocked' AND payload->>'reason' LIKE '%allow_data_loss_failover%'`
	waitFor(t, "the alert is written", func() bool { return m.q(t, blockedAlert) != "0" })
	if got := m.q(t, blockedAlert); got != "1" {
		t.Fatalf("one alert, written once: %s", got)
	}
	time.Sleep(300 * time.Millisecond) // more ticks must not write it again
	if got := m.q(t, `SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' = 'failover_blocked'`); got != "1" {
		t.Fatalf("alert written again: %s", got)
	}
	if m.q(t, `SELECT count(*) FROM deployments WHERE trigger = 'failover'`) != "0" {
		t.Fatal("nothing may move")
	}
	if got := m.nodesOfService(t); got != "n-a" {
		t.Fatalf("the placement stays: %s", got)
	}
	// The same service with the opt-in but no backup is blocked with another reason.
	m.exec(t, `UPDATE services SET allow_data_loss_failover = true WHERE id = $1`, m.serviceID)
	waitFor(t, "no backup reason", func() bool {
		return strings.Contains(m.q(t, `SELECT COALESCE(failover_reason,'') FROM services WHERE id = $1`, m.serviceID), "no successful backup")
	})
}

// failover none: the service stays and the reason is shown.
func TestREQ_NODE_007_FailoverNoneStays(t *testing.T) {
	m := setupM7(t)
	a := m.addNode(t, "n-a")
	m.addNode(t, "n-b")
	m.deploy(t, orchestrator.DeployParams{})
	m.failNode(t, a)
	waitFor(t, "blocked", func() bool {
		return m.q(t, `SELECT failover_state FROM services WHERE id = $1`, m.serviceID) == "blocked"
	})
	if !strings.Contains(m.q(t, `SELECT failover_reason FROM services WHERE id = $1`, m.serviceID), "failover is off") {
		t.Fatal("reason")
	}
	// The node returns: the service is healthy again, nothing moved.
	m.env.Clock.Advance(time.Second)
	a.a.Heartbeat(t)
	waitFor(t, "healthy", func() bool {
		return m.q(t, `SELECT failover_state FROM services WHERE id = $1`, m.serviceID) == "healthy"
	})
	if m.nodesOfService(t) != "n-a" {
		t.Fatal("the service stays on its node")
	}
}

// AC-NODE-06: a service with a local volume, a successful backup and the opt-in starts on another node with the
// restored snapshot, and the loss of data is recorded.
func TestAC_NODE_06_FailoverVolumeRestore(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.exec(t, `INSERT INTO volumes (id, service_id, name, kind, mount_path) VALUES ('vol_x', $1, 'data', 'volume', '/data')`, m.serviceID)
	m.exec(t, `UPDATE services SET failover = 'auto', allow_data_loss_failover = true WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	m.mover.mu.Lock()
	m.mover.when, m.mover.has = when, true
	m.mover.mu.Unlock()

	m.failNode(t, a)
	waitFor(t, "the service runs on n-b", func() bool { return m.nodesOfService(t) == "n-b" })
	waitFor(t, "failover is live", func() bool {
		return m.q(t, `SELECT COALESCE(string_agg(state, ','), '') FROM deployments WHERE trigger = 'failover'`) == "live"
	})
	m.mover.mu.Lock()
	restored := append([]string(nil), m.mover.restored...)
	m.mover.mu.Unlock()
	if len(restored) != 1 || restored[0] != b.a.NodeID {
		t.Fatalf("the snapshot must be restored on n-b: %v", restored)
	}
	if m.q(t, `SELECT node_id FROM volumes WHERE id = 'vol_x'`) != b.a.NodeID {
		t.Fatal("the volume moves to n-b")
	}
	if got := m.q(t, `SELECT to_char(failover_data_lost_since AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS')`+` FROM services WHERE id = $1`, m.serviceID); got != when.Format("2006-01-02T15:04:05") {
		t.Fatalf("data lost since %s, want %s", got, when)
	}
	if got := m.q(t, `SELECT count(*) FROM deployment_steps ds JOIN deployments d ON d.id = ds.deployment_id WHERE d.trigger = 'failover' AND ds.message LIKE '%is lost%'`); got == "0" {
		t.Fatal("the warning about lost data must be recorded in the deployment")
	}
	// The deployment is live before the move is recorded, so wait for the audit event.
	waitFor(t, "the failover is in the audit log", func() bool {
		return m.q(t, `SELECT count(*) FROM audit_events WHERE action = 'service.failover' AND diff->>'data_copy' = 'true'`) == "1"
	})
}

// Move of a service with a local volume: stopped, snapshot, restore on the target, start. The data copy is
// visible (recreate, warning).
func TestREQ_NODE_006_MoveWithVolume(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.exec(t, `INSERT INTO volumes (id, service_id, name, kind, mount_path) VALUES ('vol_x', $1, 'data', 'volume', '/data')`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	oldPlacement := m.q(t, `SELECT id FROM placements WHERE service_id = $1`, m.serviceID)
	id, warnings, err := m.ctl.MoveService(context.Background(), m.serviceID, b.a.NodeID, "backup", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || warnings[0].Code != control.WarnZeroDowntime {
		t.Fatalf("the downtime warning is missing: %v", warnings)
	}
	if state, reason := m.waitDeployment(t, id); state != "live" {
		t.Fatalf("%s %s %s", state, reason, m.q(t, `SELECT COALESCE(failure_detail,'') FROM deployments WHERE id = $1`, id))
	}
	if m.q(t, `SELECT strategy FROM deployments WHERE id = $1`, id) != "recreate" {
		t.Fatal("a move with data is a recreate")
	}
	m.mover.mu.Lock()
	snaps, restored := m.mover.snapshots, append([]string(nil), m.mover.restored...)
	m.mover.mu.Unlock()
	if snaps != 1 || len(restored) != 1 || restored[0] != b.a.NodeID {
		t.Fatalf("snapshots %d restored %v", snaps, restored)
	}
	if a.sim.index("stop:"+oldPlacement) < 0 {
		t.Fatal("the old container must be stopped first")
	}
	if m.nodesOfService(t) != "n-b" || m.q(t, `SELECT node_id FROM volumes WHERE id = 'vol_x'`) != b.a.NodeID {
		t.Fatal("service and volume are on n-b")
	}
	if m.q(t, `SELECT count(*) FROM placements WHERE node_id = $1`, a.a.NodeID) != "0" {
		t.Fatal("the old placement must be removed")
	}
}

// AC-NODE-10: when the server cannot reach any node (all look down), no failover starts. It waits.
func TestAC_NODE_10_NoFailoverWhenNoNodeIsReachable(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.exec(t, `UPDATE services SET failover = 'auto' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	// No heartbeat from anybody: the network of the server is down.
	for i := 0; i < 7; i++ {
		m.env.Clock.Advance(10 * time.Second)
		time.Sleep(50 * time.Millisecond)
	}
	m.waitNode(t, a, "down")
	m.waitNode(t, b, "down")
	waitFor(t, "the service says why it waits", func() bool {
		return strings.Contains(m.q(t, `SELECT COALESCE(failover_reason,'') FROM services WHERE id = $1`, m.serviceID), "cannot reach any other node")
	})
	time.Sleep(300 * time.Millisecond)
	if got := m.q(t, `SELECT count(*) FROM deployments WHERE trigger = 'failover'`); got != "0" {
		t.Fatalf("no failover may start: %s deployments", got)
	}
	if m.nodesOfService(t) != "n-a" {
		t.Fatal("the placement stays")
	}
	// The network is back: b answers again, then the failover goes on (a is still silent).
	for i := 0; i < 3; i++ {
		m.env.Clock.Advance(time.Second)
		b.a.Heartbeat(t)
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, "failover after b is back", func() bool { return m.nodesOfService(t) == "n-b" })
}

// REQ-SEC-100: node B gets nothing of node A: no workload, no secret value, no variable value.
func TestREQ_SEC_100_NodeGetsOnlyItsOwnSecrets(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.addDomain(t)
	// A real secret of the service on a (encrypted in the store, decrypted only into the desired state of its node).
	kek := make([]byte, 32)
	sec, err := secrets.NewManager(m.env.Pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	m.ctl.Secrets = sec
	m.deploy(t, orchestrator.DeployParams{})
	prj := m.q(t, `SELECT e.project_id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.id = $1`, m.serviceID)
	ctx := context.Background()
	m.exec(t, `INSERT INTO secrets (id, project_id, name) VALUES ('sec_db', $1, 'DB_PASSWORD')`, prj)
	if err := sec.Provider.Put(ctx, m.env.Pool, prj, "sec_db", 1, []byte("secret-of-node-a-0123"), ""); err != nil {
		t.Fatal(err)
	}
	rel := m.q(t, `SELECT live_release_id FROM services WHERE id = $1`, m.serviceID)
	m.exec(t, `UPDATE releases SET secret_versions = '{"DB_PASSWORD":1}', variable_snapshot = variable_snapshot || '{"API_URL":"https://plain-value-of-a"}',
		config_snapshot = jsonb_set(config_snapshot, '{secret_refs}', '{"DB_PASSWORD":"sec_db"}') WHERE id = $1`, rel)
	if err := m.ctl.NodesChanged(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	dsA := a.state(t)
	if len(dsA.Workloads) == 0 || dsA.Workloads[0].Env["DB_PASSWORD"] != "secret-of-node-a-0123" {
		t.Fatalf("node a must get its own secret: %v", dsA.Workloads)
	}
	dsB := b.state(t)
	raw, err := proto.Marshal(dsB)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-of-node-a-0123", "plain-value-of-a", dsA.Workloads[0].PlacementId, "DB_PASSWORD"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the desired state of node b holds %q", leak)
		}
	}
	if len(dsB.Workloads) != 0 || len(dsB.RegistryAuths) != 0 {
		t.Fatalf("node b workloads: %v", dsB.Workloads)
	}
}

// REQ-NODE-009 and REQ-SEC-106 (server side): a node that runs a workload of an environment gets the names and mesh
// addresses of the replicas of that environment on other nodes, and nothing of other environments.
func TestAC_NODE_09_RemoteEndpointsSameEnvironmentOnly(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.exec(t, `UPDATE services SET config = config || '{"placement":{"nodes":["n-a"]}}' WHERE id = $1`, m.serviceID)
	m.deploy(t, orchestrator.DeployParams{})
	// A second service of the same environment on node b, and a service of another environment on node a.
	envID := m.q(t, `SELECT environment_id FROM services WHERE id = $1`, m.serviceID)
	prj := m.q(t, `SELECT project_id FROM environments WHERE id = $1`, envID)
	m.exec(t, `INSERT INTO environments (id, project_id, name, slug, kind) VALUES ('env_other', $1, 'staging', 'staging', 'staging')`, prj)
	m.exec(t, `INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ('svc_client', $1, 'client', 'app', 'image',
		'{"image":"nginx:1.27","port":8080,"placement":{"nodes":["n-b"]},"strategy_options":{"drain_seconds":0}}')`, envID)
	m.exec(t, `INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ('svc_other', 'env_other', 'other', 'app', 'image',
		'{"image":"nginx:1.27","port":8080,"placement":{"nodes":["n-a"]},"strategy_options":{"drain_seconds":0}}')`)
	for _, svc := range []string{"svc_client", "svc_other"} {
		id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: svc})
		if err != nil {
			t.Fatal(err)
		}
		if state, reason := m.waitDeployment(t, id); state != "live" {
			t.Fatalf("%s: %s %s", svc, state, reason)
		}
	}
	time.Sleep(300 * time.Millisecond)
	dsB := b.state(t)
	webIP := m.q(t, `SELECT host(ip) FROM placements WHERE service_id = $1 AND role = 'live'`, m.serviceID)
	otherIP := m.q(t, `SELECT host(ip) FROM placements WHERE service_id = 'svc_other' AND role = 'live'`)
	var found bool
	for _, e := range dsB.RemoteEndpoints {
		if e.Ip == otherIP {
			t.Fatalf("node b must not know the other environment: %v", e)
		}
		if e.Ip == webIP {
			found = true
			// The names are the replica, the service id and the stable name of the service (service.environment).
			if e.EnvironmentId != envID || len(e.Names) != 4 || !strings.HasPrefix(e.Names[1], "svc-") || e.Names[2] != "web.prod" || e.Names[3] != control.EnvServiceName("web", envID) {
				t.Fatalf("endpoint: %v", e)
			}
		}
	}
	if !found {
		t.Fatalf("node b must know the web service of its environment on node a: %v", dsB.RemoteEndpoints)
	}
	// Node a runs workloads of both environments, so it knows the replica of its environment on node b only.
	for _, e := range a.state(t).RemoteEndpoints {
		if e.EnvironmentId != envID && e.EnvironmentId != "env_other" {
			t.Fatalf("unknown environment: %v", e)
		}
	}
	// The workloads carry their fixed address and the search domain of their environment (T-383).
	for _, w := range dsB.Workloads {
		if w.Ip == "" || w.EnvironmentId != envID {
			t.Fatalf("workload of b: %v", w)
		}
		if len(w.DnsSearch) != 1 || w.DnsSearch[0] != control.EnvDomain(envID)+".env.polipan.internal" {
			t.Fatalf("dns search of b: %v", w.DnsSearch)
		}
		if got := w.NetworkAliases[len(w.NetworkAliases)-1]; got != control.EnvServiceName("client", envID) {
			t.Fatalf("aliases of b: %v", w.NetworkAliases)
		}
	}
}

// REQ-NODE-008: a force delete of a node with workloads: services with failover none stop with a reason, the node is
// gone from the list of peers, and its certificate is revoked.
func TestREQ_NODE_008_DeleteNodeForce(t *testing.T) {
	m := setupM7(t)
	a, b := m.addNode(t, "n-a"), m.addNode(t, "n-b")
	m.deploy(t, orchestrator.DeployParams{})
	ctx := context.Background()
	if err := m.ctl.DeleteNode(ctx, m.env.OrgID, a.a.NodeID, true, ""); err != nil {
		t.Fatal(err)
	}
	if m.q(t, `SELECT desired_state || '/' || failover_state FROM services WHERE id = $1`, m.serviceID) != "stopped/blocked" ||
		!strings.Contains(m.q(t, `SELECT failover_reason FROM services WHERE id = $1`, m.serviceID), "was deleted") {
		t.Fatal("a service without failover stops with a reason")
	}
	if m.q(t, `SELECT count(*) FROM placements WHERE node_id = $1`, a.a.NodeID) != "0" {
		t.Fatal("placements of the deleted node are lost")
	}
	if m.q(t, `SELECT count(*) FROM revoked_certs WHERE node_id = $1`, a.a.NodeID) != "1" {
		t.Fatal("the certificate must be revoked")
	}
	waitFor(t, "the peer is removed", func() bool {
		ds := b.last.Load()
		return ds != nil && len(ds.Peers) == 0
	})
	// The name can be used again.
	m.addNode(t, "n-a")
}
