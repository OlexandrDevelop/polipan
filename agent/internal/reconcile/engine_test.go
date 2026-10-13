// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/agent/internal/state"
	"github.com/polipan/polipan/agent/internal/vip"
	"github.com/polipan/polipan/proto/agentv1"
)

type fakeDocker struct {
	mu    sync.Mutex
	cs    map[string]*docker.Container // by name
	calls []string
	n     int

	specs     map[string]docker.ContainerSpec // last create spec by name
	vols      map[string]*docker.Volume
	volRoot   string // folder of the volume data
	auths     []string
	createErr error
	execs     []*fakeExec
}

func newFake() *fakeDocker {
	return &fakeDocker{cs: map[string]*docker.Container{}, specs: map[string]docker.ContainerSpec{}, vols: map[string]*docker.Volume{}}
}

func (f *fakeDocker) rec(s string) { f.calls = append(f.calls, s) }

func (f *fakeDocker) Ping(context.Context) (string, error) { return "27", nil }
func (f *fakeDocker) EnsureNetwork(context.Context, string, string) (string, error) {
	return "172.30.0.1", nil
}
func (f *fakeDocker) PullImage(_ context.Context, ref, auth string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("pull " + ref)
	f.auths = append(f.auths, auth)
	return ref, nil
}
func (f *fakeDocker) CreateAndStart(_ context.Context, s docker.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.n++
	f.rec("create " + s.Name)
	f.specs[s.Name] = s
	id := fmt.Sprintf("id%d", f.n)
	f.cs[s.Name] = &docker.Container{ID: id, Name: s.Name, Image: s.Image, State: "running", Labels: s.Labels, IP: "172.30.0.9"}
	return id, nil
}
func (f *fakeDocker) byID(id string) *docker.Container {
	for _, c := range f.cs {
		if c.ID == id {
			return c
		}
	}
	return nil
}
func (f *fakeDocker) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("start " + f.byID(id).Name)
	f.byID(id).State = "running"
	return nil
}
func (f *fakeDocker) Stop(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("stop " + f.byID(id).Name)
	f.byID(id).State = "exited"
	return nil
}
func (f *fakeDocker) Restart(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("restart " + id)
	return nil
}
func (f *fakeDocker) Remove(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.byID(id)
	f.rec("remove " + c.Name)
	delete(f.cs, c.Name)
	return nil
}
func (f *fakeDocker) List(_ context.Context, labels map[string]string) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.Container
	for _, c := range f.cs {
		ok := true
		for k, v := range labels {
			if c.Labels[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, *c)
		}
	}
	return out, nil
}
func (f *fakeDocker) Inspect(_ context.Context, n string) (*docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cs[n]; ok {
		cp := *c
		return &cp, nil
	}
	if c := f.byID(n); c != nil {
		cp := *c
		return &cp, nil
	}
	return nil, docker.ErrNotFound
}
func (f *fakeDocker) Logs(context.Context, string, int) (string, error) { return "", nil }
func (f *fakeDocker) SetRestartPolicy(context.Context, string, string) error {
	return nil
}
func (f *fakeDocker) ImageRemove(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("rmi " + ref)
	return nil
}
func (f *fakeDocker) Events(context.Context) (<-chan docker.Event, error) {
	return make(chan docker.Event), nil
}
func (f *fakeDocker) VolumeCreate(_ context.Context, name string, labels map[string]string) (*docker.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("volume create " + name)
	if v, ok := f.vols[name]; ok {
		cp := *v
		return &cp, nil
	}
	mp := filepath.Join(f.volRoot, name)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return nil, err
	}
	f.vols[name] = &docker.Volume{Name: name, Mountpoint: mp, Labels: labels}
	cp := *f.vols[name]
	return &cp, nil
}
func (f *fakeDocker) VolumeInspect(_ context.Context, name string) (*docker.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.vols[name]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, docker.ErrNotFound
}
func (f *fakeDocker) VolumeList(_ context.Context, labels map[string]string) ([]docker.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docker.Volume
	for _, v := range f.vols {
		ok := true
		for k, val := range labels {
			if v.Labels[k] != val {
				ok = false
			}
		}
		if ok {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (f *fakeDocker) VolumeRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("volume rm " + name)
	v, ok := f.vols[name]
	if !ok {
		return docker.ErrNotFound
	}
	delete(f.vols, name)
	return os.RemoveAll(v.Mountpoint)
}
func (f *fakeDocker) Exec(_ context.Context, id string, o docker.ExecOptions) (docker.ExecConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("exec " + id + " " + strings.Join(o.Cmd, " "))
	r, w := io.Pipe()
	x := &fakeExec{out: r, outW: w, opts: o}
	f.execs = append(f.execs, x)
	return x, nil
}
func (f *fakeDocker) lastExec() *fakeExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execs) == 0 {
		return nil
	}
	return f.execs[len(f.execs)-1]
}

// fakeExec is a terminal: the test writes the output into outW, and the input is recorded.
type fakeExec struct {
	out  *io.PipeReader
	outW *io.PipeWriter
	opts docker.ExecOptions

	mu          sync.Mutex
	input       bytes.Buffer
	sizes       []string
	closedWrite bool
	closed      bool
}

func (x *fakeExec) Read(p []byte) (int, error) { return x.out.Read(p) }
func (x *fakeExec) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.input.Write(p)
}
func (x *fakeExec) CloseWrite() error {
	x.mu.Lock()
	x.closedWrite = true
	x.mu.Unlock()
	return x.outW.Close() // the shell ends at the end of input
}
func (x *fakeExec) Resize(_ context.Context, rows, cols uint32) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sizes = append(x.sizes, fmt.Sprintf("%dx%d", rows, cols))
	return nil
}
func (x *fakeExec) Wait(context.Context) (int, error) { return 0, nil }
func (x *fakeExec) Close() error {
	x.mu.Lock()
	x.closed = true
	x.mu.Unlock()
	return x.outW.Close()
}
func (x *fakeExec) state() (input string, sizes []string, closedWrite, closed bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.input.String(), append([]string(nil), x.sizes...), x.closedWrite, x.closed
}

func (f *fakeDocker) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type sink struct {
	mu   sync.Mutex
	msgs []*agentv1.AgentMessage
}

func (s *sink) Send(m *agentv1.AgentMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return true
}

// find returns the first message that matches.
func (s *sink) find(match func(*agentv1.AgentMessage) bool) *agentv1.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if match(m) {
			return m
		}
	}
	return nil
}

// last returns the last actual state.
func (s *sink) last() *agentv1.ActualState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.msgs) - 1; i >= 0; i-- {
		if a := s.msgs[i].GetActualState(); a != nil {
			return a
		}
	}
	return nil
}

func (s *sink) acks() (n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if m.GetAck() != nil {
			n++
		}
	}
	return
}

func wl(id, role string) *agentv1.WorkloadSpec {
	return &agentv1.WorkloadSpec{PlacementId: id, ServiceId: "svc_1", ReleaseId: "rel_1", Color: "blue", Role: role,
		ContainerName: "pp-" + id, ImageRef: "nginx@sha256:aa", NetworkAliases: []string{"web"}}
}

func newEngine(t *testing.T, dir string, f *fakeDocker) (*Engine, *sink) {
	st, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := New(f, st, "polipan", nil)
	sk := &sink{}
	e.Sink = sk
	if err := e.Load(); err != nil {
		t.Fatal(err)
	}
	return e, sk
}

func TestReconcileCreateRemoveStop(t *testing.T) {
	f := newFake()
	f.cs["foreign"] = &docker.Container{ID: "fx", Name: "foreign", State: "running", Labels: map[string]string{"app": "x"}}
	f.cs["pp-old"] = &docker.Container{ID: "fo", Name: "pp-old", State: "running",
		Labels: map[string]string{LabelManaged: "true", LabelPlacement: "old"}}
	e, sk := newEngine(t, t.TempDir(), f)
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{wl("a", "live"), wl("b", "stopped")}})
	e.Reconcile(context.Background())

	if !f.has("pull nginx@sha256:aa") || !f.has("create pp-a") {
		t.Fatalf("calls: %v", f.calls)
	}
	if f.has("create pp-b") {
		t.Fatal("stopped workload without container must not be created")
	}
	if _, ok := f.cs["pp-old"]; ok {
		t.Fatal("managed container not in desired state must be removed")
	}
	if _, ok := f.cs["foreign"]; !ok {
		t.Fatal("foreign container must stay")
	}
	if f.cs["pp-a"].Labels[LabelPlacement] != "a" || f.cs["pp-a"].Labels[LabelManaged] != "true" {
		t.Fatalf("labels: %v", f.cs["pp-a"].Labels)
	}
	// role stopped: stop but keep
	e.SetDesired(&agentv1.DesiredState{Version: 2, Workloads: []*agentv1.WorkloadSpec{wl("a", "stopped")}})
	e.Reconcile(context.Background())
	if c, ok := f.cs["pp-a"]; !ok || c.State != "exited" {
		t.Fatalf("stopped container: %+v", f.cs["pp-a"])
	}
	// an exited live container is started again
	e.SetDesired(&agentv1.DesiredState{Version: 3, Workloads: []*agentv1.WorkloadSpec{wl("a", "live")}})
	e.Reconcile(context.Background())
	if f.cs["pp-a"].State != "running" || !f.has("start pp-a") {
		t.Fatalf("calls: %v", f.calls)
	}
	if v, run := e.Applied(); v != 3 || run != 1 {
		t.Fatalf("applied %d running %d", v, run)
	}
	if len(sk.msgs) == 0 || sk.msgs[len(sk.msgs)-1].GetActualState() == nil {
		t.Fatal("actual state must be sent")
	}
}

// REQ-AGENT-005 / T-E2E-13 (agent side): the server is down, the agent restarts and runs the saved state.
func TestOfflineRestartRunsPersistedState(t *testing.T) {
	dir := t.TempDir()
	f := newFake()
	e1, _ := newEngine(t, dir, f)
	e1.SetDesired(&agentv1.DesiredState{Version: 5, Workloads: []*agentv1.WorkloadSpec{wl("a", "live")}})
	e1.Reconcile(context.Background())
	// the agent restarts, the container died, the server is down
	f.cs["pp-a"].State = "exited"
	e2, _ := newEngine(t, dir, f)
	e2.Reconcile(context.Background())
	if f.cs["pp-a"].State != "exited" {
		t.Fatal("nothing may start before the server answers or is found unreachable")
	}
	e2.OnUnreachable()
	e2.Reconcile(context.Background())
	if f.cs["pp-a"].State != "running" {
		t.Fatal("dead container must be started in offline mode")
	}
	delete(f.cs, "pp-a") // missing container is created again
	e2.Reconcile(context.Background())
	if _, ok := f.cs["pp-a"]; !ok {
		t.Fatal("missing container must be created in offline mode")
	}
}

// REQ-AGENT-009: after a reconnect the new desired state is applied before any container starts.
func TestNewStateBeforeStart(t *testing.T) {
	dir := t.TempDir()
	f := newFake()
	e1, _ := newEngine(t, dir, f)
	e1.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{wl("a", "live")}})
	e1.Reconcile(context.Background())
	delete(f.cs, "pp-a") // failed over meanwhile; the old node comes back
	f.calls = nil
	e2, _ := newEngine(t, dir, f)
	e2.OnConnect()
	e2.Reconcile(context.Background()) // timer pass before the first desired state
	if len(f.calls) != 0 {
		t.Fatalf("no container may start before the new desired state: %v", f.calls)
	}
	e2.SetDesired(&agentv1.DesiredState{Version: 2}) // the service moved away
	e2.Reconcile(context.Background())
	if f.has("create") || f.has("start") {
		t.Fatalf("stale workload was started: %v", f.calls)
	}
}

func TestCommandIdempotent(t *testing.T) {
	f := newFake()
	e, sk := newEngine(t, t.TempDir(), f)
	cmd := &agentv1.Command{CommandId: "cmd_1", Body: &agentv1.Command_PullImage{PullImage: &agentv1.PullImage{ImageRef: "nginx:1"}}}
	e.HandleCommand(context.Background(), cmd)
	e.HandleCommand(context.Background(), cmd)
	pulls := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pull") {
			pulls++
		}
	}
	if pulls != 1 || sk.acks() != 2 {
		t.Fatalf("pulls=%d acks=%d", pulls, sk.acks())
	}
	a := sk.msgs[0].GetAck()
	if !a.Ok || string(a.ResultJson) == "" {
		t.Fatalf("ack: %+v", a)
	}
	// expired deadline and unknown command
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_2", DeadlineUnixMs: 1, Body: &agentv1.Command_PullImage{PullImage: &agentv1.PullImage{ImageRef: "x"}}})
	if got := sk.msgs[len(sk.msgs)-1].GetAck(); got.ErrorCode != "deadline_exceeded" {
		t.Fatalf("ack: %+v", got)
	}
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_3", Body: &agentv1.Command_RunBackup{RunBackup: &agentv1.RunBackup{}}})
	if got := sk.msgs[len(sk.msgs)-1].GetAck(); got.ErrorCode != "unsupported_version" {
		t.Fatalf("ack: %+v", got)
	}
}

func TestREQ_SEC_106_WorkloadDropsNetRaw(t *testing.T) {
	e := &Engine{}
	sp := e.containerSpec(&agentv1.WorkloadSpec{ContainerName: "pp-1", ImageRef: "nginx@sha256:aa"})
	if len(sp.CapDrop) != 1 || sp.CapDrop[0] != "NET_RAW" {
		t.Fatalf("cap drop: %v", sp.CapDrop)
	}
}

type dnsRec struct{ table map[string][]string }

func (d *dnsRec) Set(map[string]string)          {}
func (d *dnsRec) SetMulti(t map[string][]string) { d.table = t }

type vipRec struct {
	err      error
	backends []string
}

func (v *vipRec) Apply(_ context.Context, s []vip.Service, subnet string) (map[string]string, error) {
	if v.err != nil {
		return nil, v.err
	}
	v.backends = nil
	out := map[string]string{}
	for _, sv := range s {
		out[sv.ID] = "198.18.0.7"
		v.backends = append(v.backends, sv.Backends...)
	}
	return out, nil
}

// The name of a service keeps one stable address while a deploy moves the live role from blue to green.
func TestServiceNameKeepsVirtualAddressAcrossDeploy(t *testing.T) {
	f := newFake()
	e, _ := newEngine(t, t.TempDir(), f)
	dns, v := &dnsRec{}, &vipRec{}
	e.DNS, e.VIP = dns, v
	e.NetSubnet = &net.IPNet{IP: net.IPv4(172, 30, 0, 0), Mask: net.CIDRMask(24, 32)}
	ctx := context.Background()

	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{wl("blue", "live")}})
	e.Reconcile(ctx)
	f.cs["pp-blue"].IP = "172.30.0.10"
	e.Reconcile(ctx)
	if got := dns.table["web"]; len(got) != 1 || got[0] != "198.18.0.7" {
		t.Fatalf("service name before the deploy: %v", got)
	}
	if got := dns.table["pp-blue"]; len(got) != 1 || got[0] != "172.30.0.10" {
		t.Fatalf("container name must keep the container address: %v", got)
	}

	green := wl("green", "live")
	blue := wl("blue", "draining")
	e.SetDesired(&agentv1.DesiredState{Version: 2, Workloads: []*agentv1.WorkloadSpec{blue, green}})
	e.Reconcile(ctx)
	f.cs["pp-green"].IP = "172.30.0.11"
	e.Reconcile(ctx)
	if got := dns.table["web"]; len(got) != 1 || got[0] != "198.18.0.7" {
		t.Fatalf("service name after the deploy: %v", got)
	}
	if len(v.backends) != 1 || v.backends[0] != "172.30.0.11" {
		t.Fatalf("only the live container is a backend: %v", v.backends)
	}
}

func TestServiceNameFallsBackToContainerAddress(t *testing.T) {
	f := newFake()
	e, _ := newEngine(t, t.TempDir(), f)
	dns := &dnsRec{}
	e.DNS, e.VIP = dns, &vipRec{err: errors.New("no nft")}
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{wl("a", "live")}})
	e.Reconcile(context.Background())
	if got := dns.table["web"]; len(got) != 1 || got[0] != "172.30.0.9" {
		t.Fatalf("fallback: %v", got)
	}
}
