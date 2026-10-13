// SPDX-License-Identifier: Apache-2.0

// Package reconcile makes the node match the desired state and runs the commands of the server.
package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/backup"
	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/agent/internal/state"
	"github.com/polipan/polipan/agent/internal/vip"
	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/protobuf/proto"
)

// Label keys of the containers the agent manages.
const (
	LabelManaged   = "polipan.managed"
	LabelService   = "polipan.service"
	LabelPlacement = "polipan.placement"
	LabelRelease   = "polipan.release"
	LabelColor     = "polipan.color"
)

// Ingress is the part of ingress.Manager that the engine uses.
type Ingress interface {
	Start(ctx context.Context) error
	Apply(ctx context.Context, spec *agentv1.IngressSpec) (string, error)
	DomainStatus(ctx context.Context, spec *agentv1.IngressSpec) []*agentv1.DomainStatus
}

// DNSTable receives the name to IP table of local workloads.
type DNSTable interface{ Set(table map[string]string) }

// Firewall applies the node rules for the desired state.
type Firewall interface {
	Apply(ctx context.Context, ds *agentv1.DesiredState) error
}

// VIPs maps services to stable virtual addresses on the node. An error means: answer with container addresses.
type VIPs interface {
	Apply(ctx context.Context, services []vip.Service, localSubnet string) (map[string]string, error)
}

// Mesh keeps the WireGuard interface and the routes of the node (REQ-AGENT-008).
type Mesh interface {
	Apply(ctx context.Context, peers []*agentv1.WireguardPeer, meshCIDR string) error
	Check(ctx context.Context) error
}

// Sink sends messages to the server. It returns false when there is no connection.
type Sink interface {
	Send(msg *agentv1.AgentMessage) bool
}

// Engine is the reconciler.
type Engine struct {
	Docker   docker.Client
	Ingress  Ingress  // optional
	DNS      DNSTable // optional
	Firewall Firewall // optional
	VIP      VIPs     // optional: stable addresses of the services
	Mesh     Mesh     // optional
	// NetSubnet is the subnet of the Docker network of the workloads. A fixed address of the server is used only
	// when it is inside this subnet.
	NetSubnet *net.IPNet
	Store     *state.Store
	Sink      Sink // optional
	Network   string
	// Backup runs the backup jobs (F10). Nil means the agent cannot run backups.
	Backup *backup.Runner
	// DNSServers are set in the DNS field of the containers.
	DNSServers []string
	// Infra runs until it succeeds before containers are touched (network, DNS, Caddy). Optional.
	Infra    func(ctx context.Context) error
	Log      *slog.Logger
	Interval time.Duration // default 30 s
	// BindsDir, FilesDir and SecretsDir are the host folders of the mounts. Empty = the defaults.
	BindsDir   string
	FilesDir   string
	SecretsDir string
	// CheckTmpfs checks that SecretsDir is on tmpfs. Nil = a statfs check (tests replace it).
	CheckTmpfs func(dir string) error
	// Quota enforces size_limit_bytes. Nil = XFS project quota when supported.
	Quota Quota
	// Now is the clock of the usage report (REQ-CORE-031). Nil = time.Now.
	Now func() time.Time
	// OnDesired is called with every new desired state, after it is saved. Optional (M8 telemetry).
	OnDesired func(ds *agentv1.DesiredState)

	mu         sync.Mutex
	execs      map[string]*execSession // open terminals by command id
	quotas     map[string]uint64
	usage      []*agentv1.VolumeUsage
	usageAt    time.Time
	desired    *agentv1.DesiredState
	applied    int64
	running    uint32
	hold       bool
	suppressed map[string]bool
	pulled     map[string]bool
	noRestart  map[string]bool          // container ids that already have the restart policy "no" (kept old containers)
	refused    map[string]refusal       // placements that the volume guard did not start
	repaired   bool                     // the restart policies of the old containers were fixed after the agent start
	cached     bool                     // a desired state is saved on disk: the agent starts the workloads, Docker does not (REQ-DEPLOY-115)
	waiting    map[string]*depWaitState // placements that wait for their dependencies
	depWait    bool                     // the last pass left a placement waiting for a dependency
	retrySet   bool                     // a soon reconcile is already planned (retry)
	startMu    sync.Mutex               // one volume guard and container start at a time (reconcile, commands, restore)
	hs         map[string]*hstate       // health state by placement
	ingMu      sync.Mutex               // one ingress load at a time
	ingSpec    [32]byte
	ingHash    string
	errs       []string
	inflight   map[string]bool
	infraOK    bool
	trigger    chan struct{}
	runMu      sync.Mutex
}

// New creates an engine. It holds back all changes until the server answers or is found unreachable (REQ-AGENT-009).
func New(d docker.Client, store *state.Store, network string, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{Docker: d, Store: store, Network: network, Log: log, hold: true,
		suppressed: map[string]bool{}, pulled: map[string]bool{}, noRestart: map[string]bool{}, refused: map[string]refusal{}, waiting: map[string]*depWaitState{}, inflight: map[string]bool{},
		execs: map[string]*execSession{}, hs: map[string]*hstate{}, quotas: map[string]uint64{}, trigger: make(chan struct{}, 1)}
}

// Load reads the persisted desired state and the stored command acks.
func (e *Engine) Load() error {
	if err := e.Store.LoadCommands(); err != nil {
		return err
	}
	ds, err := e.Store.LoadDesired()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.desired = ds
	e.cached = ds != nil
	e.mu.Unlock()
	return nil
}

// Version returns the version of the desired state that the agent holds.
func (e *Engine) Version() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.desired == nil {
		return 0
	}
	return e.desired.Version
}

// Applied returns the last applied version and the number of running containers.
func (e *Engine) Applied() (int64, uint32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applied, e.running
}

func (e *Engine) kick() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

// OnConnect is called when the stream is open. Nothing is started until the first desired state arrives.
func (e *Engine) OnConnect() {
	e.mu.Lock()
	e.hold = true
	e.mu.Unlock()
}

// OnUnreachable is called when the server cannot be reached. The persisted state runs (offline mode).
func (e *Engine) OnUnreachable() {
	e.mu.Lock()
	was := e.hold
	e.hold = false
	e.mu.Unlock()
	if was {
		e.kick()
	}
}

// SetDesired accepts a desired state from the server. Only a higher version replaces the stored one.
func (e *Engine) SetDesired(ds *agentv1.DesiredState) {
	e.mu.Lock()
	newer := e.desired == nil || ds.Version > e.desired.Version
	if newer {
		if _, err := e.Store.SaveDesired(ds); err != nil {
			e.Log.Error("cannot save desired state", "error", err)
		} else {
			e.cached = true
		}
		e.desired = ds
		e.suppressed = map[string]bool{}
	}
	e.hold = false
	e.mu.Unlock()
	if newer && e.OnDesired != nil {
		e.OnDesired(ds)
	}
	e.kick()
}

// Run reconciles on every new state and on a timer, until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	iv := e.Interval
	if iv == 0 {
		iv = 30 * time.Second
	}
	e.runMu.Lock()
	_ = e.repairPolicies(ctx) // logged; Reconcile tries again and does nothing until it succeeds
	e.runMu.Unlock()
	t := time.NewTicker(iv)
	defer t.Stop()
	go e.healthLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.trigger:
		}
		e.Reconcile(ctx)
	}
}

func hashSpec(s *agentv1.IngressSpec) [32]byte {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	return sha256.Sum256(b)
}

// Reconcile runs one pass and reports the actual state.
func (e *Engine) Reconcile(ctx context.Context) {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	_ = e.repairPolicies(ctx)
	e.mu.Lock()
	ds, hold := e.desired, e.hold
	e.mu.Unlock()
	if hold || ds == nil {
		return
	}
	var errs []string
	// No container is touched before the old ones have the restart policy "no" (REQ-DEPLOY-113).
	if err := e.requireRepaired(ctx); err != nil {
		errs = append(errs, codeFor(err, "internal_error")+": "+err.Error())
		e.finish(ctx, ds, errs, false)
		return
	}
	if e.Infra != nil && !e.infraOK {
		if err := e.Infra(ctx); err != nil {
			errs = append(errs, "infra: "+err.Error())
			e.finish(ctx, ds, errs, false)
			return
		}
		e.infraOK = true
	}
	if e.Mesh != nil {
		// The mesh is part of the state only when there are peers: a single node needs no WireGuard.
		if err := e.Mesh.Apply(ctx, ds.Peers, ds.MeshCidr); err != nil {
			e.Log.Warn("mesh not applied", "error", err)
			if len(ds.Peers) > 0 {
				errs = append(errs, "mesh: "+err.Error())
			}
		}
	}
	managed, err := e.Docker.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		errs = append(errs, codeFor(err, "internal_error")+": "+err.Error())
		e.finish(ctx, ds, errs, false)
		return
	}
	byName := map[string]docker.Container{}
	for _, c := range managed {
		byName[c.Name] = c
	}
	wanted := map[string]bool{}
	cycle := depCycles(ds)
	waiting := false
	for _, w := range stoppedFirst(ds.Workloads) {
		wanted[w.ContainerName] = true
		e.mu.Lock()
		skip := e.suppressed[w.PlacementId]
		e.mu.Unlock()
		if skip {
			continue
		}
		var ex *docker.Container
		if c, ok := byName[w.ContainerName]; ok {
			ex = &c
		}
		// A workload that must start, or be made again, waits until its dependencies on this node are healthy
		// (REQ-DEPLOY-116). A running container that must be made again keeps running while it waits.
		if w.Role != "stopped" && (!isUp(ex) || e.stale(w, ex)) && !cycle[w.PlacementId] {
			if wait, err := e.waitDeps(ctx, ds, w, byName); wait {
				waiting = true
				if err != nil {
					errs = append(errs, fmt.Sprintf("dependency_unhealthy: placement %s: %s", w.PlacementId, err))
				}
				continue
			}
		} else {
			e.mu.Lock()
			delete(e.waiting, w.PlacementId)
			e.mu.Unlock()
		}
		if err := e.ensure(ctx, w, ex); err != nil {
			msg := e.scrub(err.Error())
			e.Log.Error("workload failed", "placement", w.PlacementId, "error", msg)
			errs = append(errs, fmt.Sprintf("%s: placement %s: %s", codeFor(err, "container_start_failed"), w.PlacementId, msg))
		}
	}
	e.mu.Lock()
	for p := range e.refused {
		if !inDesired(ds, p) {
			delete(e.refused, p)
		}
	}
	for p := range e.waiting {
		if !inDesired(ds, p) {
			delete(e.waiting, p)
		}
	}
	e.depWait = waiting
	e.mu.Unlock()
	if waiting && len(errs) == 0 {
		e.retry(depPoll) // look again soon: a dependency may be healthy now
	}
	for _, c := range managed {
		if wanted[c.Name] || c.Labels[LabelPlacement] == "" {
			continue
		}
		e.Log.Info("removing container that is not in the desired state", "name", c.Name)
		e.closeExecs(c.Labels[LabelPlacement])
		if err := e.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Sprintf("remove %s: %s", c.Name, e.scrub(err.Error())))
			continue
		}
		// Volumes stay. Only the file mounts of the placement go.
		e.removeFiles(c.Labels[LabelPlacement])
	}
	e.finish(ctx, ds, errs, true)
}

// finish does ingress, DNS, firewall and sends the actual state.
func (e *Engine) finish(ctx context.Context, ds *agentv1.DesiredState, errs []string, docked bool) {
	spec := ds.Ingress
	if spec == nil {
		spec = &agentv1.IngressSpec{}
	}
	var domains []*agentv1.DomainStatus
	if docked && e.Ingress != nil {
		h := hashSpec(spec)
		e.mu.Lock()
		changed := h != e.ingSpec || e.ingHash == ""
		e.mu.Unlock()
		if changed {
			if _, err := e.applyIngress(ctx, spec); err != nil {
				errs = append(errs, "caddy_load_failed: "+err.Error())
			}
		}
		domains = e.Ingress.DomainStatus(ctx, spec)
	}
	actual := e.actual(ctx, ds)
	if e.Firewall != nil {
		if err := e.Firewall.Apply(ctx, ds); err != nil {
			e.Log.Warn("firewall not applied", "error", err)
		}
	}
	e.mu.Lock()
	actual.CaddyConfigHash = e.ingHash
	if docked && len(errs) == 0 && !e.depWait {
		e.applied = ds.Version
	}
	actual.AppliedStateVersion = e.applied
	e.errs = errs
	e.mu.Unlock()
	if len(errs) > 0 {
		e.Log.Warn("reconcile finished with errors", "errors", errs)
		e.retry(5 * time.Second) // try again soon, do not wait for the timer
	}
	actual.Errors = errs
	actual.Domains = domains
	if e.Sink != nil {
		e.Sink.Send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_ActualState{ActualState: actual}})
	}
}

// actual reads the containers and updates the DNS table.
func (e *Engine) actual(ctx context.Context, ds *agentv1.DesiredState) *agentv1.ActualState {
	out := &agentv1.ActualState{}
	managed, err := e.Docker.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return out
	}
	byName := map[string]docker.Container{}
	var running uint32
	for _, c := range managed {
		byName[c.Name] = c
		st := &agentv1.ContainerStatus{PlacementId: c.Labels[LabelPlacement], ContainerId: c.ID,
			ReleaseId: c.Labels[LabelRelease], Color: c.Labels[LabelColor], State: c.State, Health: "unknown"}
		if ic, err := e.Docker.Inspect(ctx, c.ID); err == nil {
			st.ExitCode = int32(ic.ExitCode)
			if !ic.StartedAt.IsZero() {
				st.StartedUnixMs = ic.StartedAt.UnixMilli()
			}
		}
		st.Health = e.healthOf(c.Labels[LabelPlacement])
		if c.State == "running" {
			running++
		}
		out.Containers = append(out.Containers, st)
	}
	sort.Slice(out.Containers, func(i, j int) bool { return out.Containers[i].PlacementId < out.Containers[j].PlacementId })
	// Container names point to the container. A service name points to its live containers, through a stable
	// virtual address when the node can map it (package vip), so a client that keeps the address survives a deploy.
	table := map[string][]string{}
	type svcNames struct {
		names []string
		ips   []string
	}
	services := map[string]*svcNames{}
	service := func(id string, names []string, ip string) {
		sv := services[id]
		if sv == nil {
			sv = &svcNames{}
			services[id] = sv
		}
		sv.ips = append(sv.ips, ip)
		for _, n := range names {
			found := false
			for _, o := range sv.names {
				found = found || o == n
			}
			if !found {
				sv.names = append(sv.names, n)
			}
		}
	}
	for _, w := range ds.Workloads {
		if c, ok := byName[w.ContainerName]; ok && c.State == "running" && c.IP != "" {
			table[w.ContainerName] = append(table[w.ContainerName], c.IP)
			// The name of the service points only to live containers: a new container is not used before the switch.
			if w.Role == "live" {
				var names []string
				for _, a := range w.NetworkAliases {
					if a != w.ContainerName {
						names = append(names, a)
					}
				}
				service(w.ServiceId, names, c.IP)
			}
		}
	}
	// Replicas on other nodes: the names and mesh addresses the server lists (REQ-NODE-009).
	// The first name is the container, the others are the names of the service.
	for _, r := range ds.RemoteEndpoints {
		for i, n := range r.Names {
			if i == 0 {
				table[n] = append(table[n], r.Ip)
			} else {
				service(r.ServiceId, []string{n}, r.Ip)
			}
		}
	}
	var vips map[string]string
	if e.VIP != nil {
		list := make([]vip.Service, 0, len(services))
		for id, sv := range services {
			list = append(list, vip.Service{ID: id, Backends: sv.ips})
		}
		subnet := ""
		if e.NetSubnet != nil {
			subnet = e.NetSubnet.String()
		}
		var err error
		if vips, err = e.VIP.Apply(ctx, list, subnet); err != nil {
			e.Log.Warn("virtual addresses not applied, the names answer with container addresses", "error", err)
		}
	}
	for id, sv := range services {
		for _, n := range sv.names {
			if v := vips[id]; v != "" {
				table[n] = append(table[n], v)
			} else {
				table[n] = append(table[n], sv.ips...)
			}
		}
	}
	switch d := e.DNS.(type) {
	case nil:
	case interface{ SetMulti(map[string][]string) }:
		d.SetMulti(table)
	default:
		single := map[string]string{}
		for n, ips := range table {
			single[n] = ips[0]
		}
		e.DNS.Set(single)
	}
	e.mu.Lock()
	e.running = running
	e.mu.Unlock()
	out.Volumes = e.volumeUsage(ctx, ds)
	return out
}

func buildLabels(w *agentv1.WorkloadSpec) map[string]string {
	l := map[string]string{}
	for k, v := range w.Labels {
		l[k] = v
	}
	l[LabelManaged] = "true"
	l[LabelService] = w.ServiceId
	l[LabelPlacement] = w.PlacementId
	l[LabelRelease] = w.ReleaseId
	if w.Color != "" {
		l[LabelColor] = w.Color
	}
	return l
}

func (e *Engine) containerSpec(w *agentv1.WorkloadSpec) docker.ContainerSpec {
	aliases := []string{w.ContainerName}
	for _, a := range w.NetworkAliases {
		if a != w.ContainerName {
			aliases = append(aliases, a)
		}
	}
	sp := docker.ContainerSpec{
		Name: w.ContainerName, Image: w.ImageRef, Cmd: w.Command, Entrypoint: w.Entrypoint,
		Env: w.Env, Labels: buildLabels(w), Network: e.Network, Aliases: aliases,
		RestartPolicy: w.RestartPolicy, StopTimeout: int(w.StopGraceSeconds), DNS: e.DNSServers,
		// No NET_RAW: a workload must not forge source addresses or ARP on the shared bridge (REQ-SEC-106).
		CapDrop: []string{"NET_RAW"},
	}
	sp.RestartPolicy = e.dockerPolicy(w)
	sp.IP = e.wantIP(w.Ip)
	for _, p := range w.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		sp.Ports = append(sp.Ports, docker.PortBinding{HostPort: int(p.HostPort), ContainerPort: int(p.ContainerPort), Proto: proto})
	}
	if r := w.Resources; r != nil {
		sp.NanoCPUs = int64(r.CpuLimitMillis) * 1_000_000
		sp.MemoryBytes = int64(r.MemoryLimitBytes)
	}
	return sp
}

// wantIP returns the fixed address of a workload when it can be used: it must be inside the network of the node.
func (e *Engine) wantIP(ip string) string {
	if ip == "" || e.NetSubnet == nil {
		return ""
	}
	if p := net.ParseIP(ip); p == nil || !e.NetSubnet.Contains(p) {
		return ""
	}
	return ip
}

// ensure brings one workload to its role. ex is the existing container or nil.
func (e *Engine) ensure(ctx context.Context, w *agentv1.WorkloadSpec, ex *docker.Container) error {
	stopped := w.Role == "stopped"
	if e.stale(w, ex) {
		e.Log.Info("recreating container", "name", w.ContainerName)
		e.closeExecs(w.PlacementId)
		// The volumes are not touched: the new container mounts the same data (AC-VOL-01).
		if err := e.Docker.Remove(ctx, ex.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
		ex = nil
	}
	if stopped {
		if ex == nil {
			return nil
		}
		// A kept old container must not start again with Docker after a reboot: it shares its volumes with the live one.
		// This holds for every state (running, restarting, exited), so containers made before this rule are fixed too.
		e.mu.Lock()
		done := e.noRestart[ex.ID]
		e.mu.Unlock()
		if !done {
			if err := e.Docker.SetRestartPolicy(ctx, ex.ID, "no"); err != nil {
				return err
			}
			e.mu.Lock()
			e.noRestart[ex.ID] = true
			e.mu.Unlock()
		}
		if ex.State == "running" || ex.State == "restarting" {
			e.closeExecs(w.PlacementId)
			return e.Docker.Stop(ctx, ex.ID, int(w.StopGraceSeconds))
		}
		return nil
	}
	if ex == nil {
		e.mu.Lock()
		done := e.pulled[w.ImageRef]
		e.mu.Unlock()
		if !done {
			auth, err := e.pullAuth(w.RegistryAuthRef)
			if err != nil {
				return err
			}
			if _, err := e.Docker.PullImage(ctx, w.ImageRef, auth); err != nil {
				return &codedErr{"image_pull_failed", err}
			}
			e.mu.Lock()
			e.pulled[w.ImageRef] = true
			e.mu.Unlock()
		}
		mounts, err := e.prepareMounts(ctx, w)
		if err != nil {
			return err
		}
		e.startMu.Lock()
		defer e.startMu.Unlock()
		if err := e.guard(ctx, w, ""); err != nil {
			return err
		}
		sp := e.containerSpec(w)
		sp.Mounts = mounts
		id, err := e.Docker.CreateAndStart(ctx, sp)
		if err != nil {
			return &codedErr{"container_start_failed", err}
		}
		if sp.RestartPolicy == "no" {
			e.mu.Lock()
			e.noRestart[id] = true
			e.mu.Unlock()
		}
		return nil
	}
	if isUp(ex) {
		return e.agentPolicy(ctx, ex)
	}
	// The file mounts are written again: /run is empty after a reboot.
	if _, err := e.prepareMounts(ctx, w); err != nil {
		return err
	}
	e.startMu.Lock()
	defer e.startMu.Unlock()
	if err := e.guard(ctx, w, ex.ID); err != nil {
		return err
	}
	// A container that was kept stopped has the policy "no". Without a saved desired state Docker starts it again
	// after a reboot, so it gets back its own policy; with one the agent starts it and the policy stays "no".
	policy := e.dockerPolicy(w)
	if err := e.Docker.SetRestartPolicy(ctx, ex.ID, policy); err != nil {
		return &codedErr{"container_start_failed", err}
	}
	e.mu.Lock()
	if policy == "no" {
		e.noRestart[ex.ID] = true
	} else {
		delete(e.noRestart, ex.ID)
	}
	e.mu.Unlock()
	if err := e.Docker.Start(ctx, ex.ID); err != nil {
		return &codedErr{"container_start_failed", err}
	}
	return nil
}

// retry plans one reconcile after d. While one is planned, more calls do nothing: passes that plan a retry
// must not multiply the timers.
func (e *Engine) retry(d time.Duration) {
	e.mu.Lock()
	set := e.retrySet
	e.retrySet = true
	e.mu.Unlock()
	if set {
		return
	}
	time.AfterFunc(d, func() {
		e.mu.Lock()
		e.retrySet = false
		e.mu.Unlock()
		e.kick()
	})
}

// stale tells if the existing container must be removed and made again: other image, release or address.
func (e *Engine) stale(w *agentv1.WorkloadSpec, ex *docker.Container) bool {
	return ex != nil && (ex.Image != w.ImageRef || ex.Labels[LabelRelease] != w.ReleaseId ||
		(ex.State == "running" && ex.IP == "" && e.Network != "host") ||
		(ex.State == "running" && e.wantIP(w.Ip) != "" && ex.IP != w.Ip))
}

// isUp tells if a container exists and runs, or is in a state the agent must not start it from.
func isUp(c *docker.Container) bool {
	if c == nil {
		return false
	}
	switch c.State {
	case "running", "restarting", "paused", "removing":
		return true
	}
	return false
}

// dockerPolicy is the Docker restart policy of a live or candidate container. When the desired state is saved on
// disk the agent starts the workloads after a boot in the order of their dependencies, so Docker must not start
// them: "no" (REQ-DEPLOY-115). Before that (a new node, a failed save) Docker keeps the policy of the service.
func (e *Engine) dockerPolicy(w *agentv1.WorkloadSpec) string {
	e.mu.Lock()
	cached := e.cached
	e.mu.Unlock()
	if cached {
		return "no"
	}
	if w.RestartPolicy == "" {
		return "always"
	}
	return w.RestartPolicy
}

// agentPolicy sets the policy "no" on a running workload container made before the desired state was saved (an
// older agent made it with "always"). It runs once per container and agent process.
func (e *Engine) agentPolicy(ctx context.Context, c *docker.Container) error {
	e.mu.Lock()
	skip := !e.cached || e.noRestart[c.ID] || c.State == "removing"
	e.mu.Unlock()
	if skip {
		return nil
	}
	if err := e.Docker.SetRestartPolicy(ctx, c.ID, "no"); err != nil {
		return err
	}
	e.Log.Info("restart policy set to no: the agent starts this container after a boot", "name", c.Name)
	e.mu.Lock()
	e.noRestart[c.ID] = true
	e.mu.Unlock()
	return nil
}

// Dependency gate (REQ-DEPLOY-116).
const (
	depPoll    = 2 * time.Second // how often a waiting placement is looked at again
	depTimeout = 3 * time.Minute // wait for a dependency, after its start period, before dependency_unhealthy
)

// depWaitState is the wait of one placement for its dependencies.
type depWaitState struct {
	since    time.Time
	reported bool // the timeout was logged
}

// waitDeps tells if w must wait for its dependencies on this node. A dependency is ready when it is healthy (or
// runs, when it has no health check). The error is set when the wait is longer than the start period of the
// dependency plus depTimeout: the workload stays stopped and the next passes check again.
func (e *Engine) waitDeps(ctx context.Context, ds *agentv1.DesiredState, w *agentv1.WorkloadSpec, byName map[string]docker.Container) (bool, error) {
	var notReady []string
	var limit time.Duration
	for _, id := range w.StartAfterPlacementIds {
		var d *agentv1.WorkloadSpec
		for _, x := range ds.Workloads {
			if x.PlacementId == id {
				d = x
				break
			}
		}
		if d == nil || d.Role == "stopped" || d.PlacementId == w.PlacementId {
			continue // not on this node any more: not a hard dependency
		}
		// Ready only when the dependency container runs now and, with a health check, the healthy result
		// belongs to this very container and its current start (not to one that stopped, restarted or was
		// replaced since the last check).
		c, ok := byName[d.ContainerName]
		running := ok && c.State == "running"
		health := "unknown"
		if running && d.Health != nil {
			if ic, err := e.Docker.Inspect(ctx, c.ID); err == nil && ic.State == "running" {
				health = e.healthOfContainer(id, c.ID, ic.StartedAt)
			}
		}
		if running && (d.Health == nil || health == "healthy") {
			continue
		}
		notReady = append(notReady, fmt.Sprintf("%s (health: %s)", id, health))
		var start time.Duration
		if d.Health != nil {
			start = time.Duration(d.Health.StartPeriodSeconds) * time.Second
		}
		limit = max(limit, start+depTimeout)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(notReady) == 0 {
		if _, ok := e.waiting[w.PlacementId]; ok {
			e.Log.Info("dependencies are healthy, starting", "placement", w.PlacementId)
		}
		delete(e.waiting, w.PlacementId)
		return false, nil
	}
	st, ok := e.waiting[w.PlacementId]
	if !ok {
		st = &depWaitState{since: time.Now()}
		e.waiting[w.PlacementId] = st
		e.Log.Info("waiting for dependencies", "placement", w.PlacementId, "dependencies", notReady)
	}
	if waited := time.Since(st.since); waited >= limit {
		err := fmt.Errorf("the dependencies %s are not healthy after %s; the workload stays stopped and is retried",
			strings.Join(notReady, ", "), limit)
		if !st.reported {
			st.reported = true
			e.Log.Warn("workload not started", "placement", w.PlacementId, "error", err)
		}
		return true, err
	}
	return true, nil
}

// depCycles returns the placements that are in a cycle of start_after_placement_ids. The server rejects such a
// config; the agent starts them without waiting rather than never.
func depCycles(ds *agentv1.DesiredState) map[string]bool {
	edges := map[string][]string{}
	for _, w := range ds.Workloads {
		edges[w.PlacementId] = w.StartAfterPlacementIds
	}
	out := map[string]bool{}
	state := map[string]int{} // 1 visiting, 2 done
	var stack []string
	var visit func(p string)
	visit = func(p string) {
		state[p] = 1
		stack = append(stack, p)
		for _, d := range edges[p] {
			if _, ok := edges[d]; !ok {
				continue
			}
			switch state[d] {
			case 0:
				visit(d)
			case 1:
				for i := len(stack) - 1; i >= 0; i-- {
					out[stack[i]] = true
					if stack[i] == d {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[p] = 2
	}
	for _, w := range ds.Workloads {
		if state[w.PlacementId] == 0 {
			visit(w.PlacementId)
		}
	}
	return out
}

// refusal is a start that the volume guard refused, for the health check of the placement.
type refusal struct {
	msg   string
	since time.Time
}

// refusalHold is how long a refusal must last before the health check fails with it: an old
// container may still be stopping.
const refusalHold = 15 * time.Second

// guard runs the volume guard and keeps its refusal for the health check of the placement.
func (e *Engine) guard(ctx context.Context, w *agentv1.WorkloadSpec, self string) error {
	err := e.guardMounts(ctx, w, self)
	var vu *volumeInUseErr
	e.mu.Lock()
	defer e.mu.Unlock()
	if errors.As(err, &vu) {
		if r, ok := e.refused[w.PlacementId]; ok {
			e.refused[w.PlacementId] = refusal{msg: err.Error(), since: r.since}
		} else {
			e.refused[w.PlacementId] = refusal{msg: err.Error(), since: time.Now()}
		}
	} else {
		delete(e.refused, w.PlacementId)
	}
	return err
}

// refusedFor returns the refusal of a placement that lasts longer than refusalHold.
func (e *Engine) refusedFor(placement string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.refused[placement]
	if !ok || time.Since(r.since) < refusalHold {
		return "", false
	}
	return r.msg, true
}

func inDesired(ds *agentv1.DesiredState, placement string) bool {
	for _, w := range ds.Workloads {
		if w.PlacementId == placement {
			return true
		}
	}
	return false
}

// stoppedFirst puts the workloads with the role stopped first: an old container is stopped before a
// new one that uses the same volume starts.
func stoppedFirst(ws []*agentv1.WorkloadSpec) []*agentv1.WorkloadSpec {
	out := make([]*agentv1.WorkloadSpec, 0, len(ws))
	for _, w := range ws {
		if w.Role == "stopped" {
			out = append(out, w)
		}
	}
	for _, w := range ws {
		if w.Role != "stopped" {
			out = append(out, w)
		}
	}
	return out
}

// repairPolicies runs once after the agent start, before the first reconcile, also while the agent
// waits for the server. Every Polipan container that must not run (role stopped in the saved desired
// state, or not in it) gets the restart policy "no", so Docker never starts an old release next to the
// live one at the next boot. A running one with the role stopped is stopped too. Containers that
// Polipan does not manage are not touched. On a Docker error it runs again on the next reconcile.
// Without a saved desired state it waits for the first one from the server.
func (e *Engine) repairPolicies(ctx context.Context) error {
	e.mu.Lock()
	ds, done := e.desired, e.repaired
	e.mu.Unlock()
	if done || ds == nil {
		return nil // without a desired state nothing is known: it runs when the first one arrives
	}
	byName := map[string]*agentv1.WorkloadSpec{}
	for _, w := range ds.Workloads {
		byName[w.ContainerName] = w
	}
	managed, err := e.Docker.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		e.Log.Warn("restart policies not checked", "error", err)
		return err
	}
	fixed := 0
	for _, c := range managed {
		if c.Labels[LabelPlacement] == "" {
			continue
		}
		w, ok := byName[c.Name]
		if ok && w.Role != "stopped" {
			continue
		}
		grace := 0
		if ok {
			grace = int(w.StopGraceSeconds)
		}
		running := ok && (c.State == "running" || c.State == "restarting" || c.State == "paused")
		if err := e.retire(ctx, c.ID, c.Labels[LabelPlacement], running, grace); err != nil {
			e.Log.Warn("restart policy not fixed", "container", c.Name, "error", err)
			return err
		}
		fixed++
	}
	e.Log.Info("restart policies of old containers fixed", "containers", fixed)
	e.mu.Lock()
	e.repaired = true
	e.mu.Unlock()
	return nil
}

// requireRepaired runs repairPolicies when it has not succeeded yet and returns its error.
func (e *Engine) requireRepaired(ctx context.Context) error {
	if err := e.repairPolicies(ctx); err != nil {
		return fmt.Errorf("restart policies of old containers not fixed: %w", err)
	}
	return nil
}

type codedErr struct {
	code string
	err  error
}

func (c *codedErr) Error() string { return c.err.Error() }
func (c *codedErr) Unwrap() error { return c.err }

// codeFor maps an error to an agent error code (09-multi-server, section 4).
func codeFor(err error, def string) string {
	var ce *codedErr
	var vu *volumeInUseErr
	switch {
	case errors.As(err, &vu):
		return "volume_in_use"
	case errors.Is(err, docker.ErrUnavailable):
		return "docker_unavailable"
	case strings.Contains(err.Error(), "port is already allocated") || strings.Contains(err.Error(), "address already in use"):
		return "port_conflict"
	case strings.Contains(err.Error(), "no space left"):
		return "out_of_resources"
	case errors.As(err, &ce):
		return ce.code
	}
	return def
}
