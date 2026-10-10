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
	Mesh     Mesh     // optional
	// NetSubnet is the subnet of the Docker network of the workloads. A fixed address of the server is used only
	// when it is inside this subnet.
	NetSubnet *net.IPNet
	Store     *state.Store
	Sink     Sink // optional
	Network  string
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
	hs         map[string]*hstate // health state by placement
	ingMu      sync.Mutex         // one ingress load at a time
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
		suppressed: map[string]bool{}, pulled: map[string]bool{}, inflight: map[string]bool{},
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
	e.mu.Lock()
	ds, hold := e.desired, e.hold
	e.mu.Unlock()
	if hold || ds == nil {
		return
	}
	var errs []string
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
	for _, w := range ds.Workloads {
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
		if err := e.ensure(ctx, w, ex); err != nil {
			msg := e.scrub(err.Error())
			e.Log.Error("workload failed", "placement", w.PlacementId, "error", msg)
			errs = append(errs, fmt.Sprintf("%s: placement %s: %s", codeFor(err, "container_start_failed"), w.PlacementId, msg))
		}
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
	if docked && len(errs) == 0 {
		e.applied = ds.Version
	}
	actual.AppliedStateVersion = e.applied
	e.errs = errs
	e.mu.Unlock()
	if len(errs) > 0 {
		e.Log.Warn("reconcile finished with errors", "errors", errs)
		time.AfterFunc(5*time.Second, e.kick) // try again soon, do not wait for the timer
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
	table := map[string][]string{}
	for _, w := range ds.Workloads {
		if c, ok := byName[w.ContainerName]; ok && c.State == "running" && c.IP != "" {
			table[w.ContainerName] = append(table[w.ContainerName], c.IP)
			for _, a := range w.NetworkAliases {
				// The name of the service points only to live containers: a new container is not used before the switch.
				if a == w.ContainerName || w.Role == "live" {
					table[a] = append(table[a], c.IP)
				}
			}
		}
	}
	// Replicas on other nodes: the names and mesh addresses the server lists (REQ-NODE-009).
	for _, r := range ds.RemoteEndpoints {
		for _, n := range r.Names {
			table[n] = append(table[n], r.Ip)
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
	}
	if sp.RestartPolicy == "" {
		sp.RestartPolicy = "always"
	}
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
	if ex != nil && (ex.Image != w.ImageRef || ex.Labels[LabelRelease] != w.ReleaseId ||
		(ex.State == "running" && ex.IP == "" && e.Network != "host") ||
		(ex.State == "running" && e.wantIP(w.Ip) != "" && ex.IP != w.Ip)) {
		e.Log.Info("recreating container", "name", w.ContainerName)
		e.closeExecs(w.PlacementId)
		// The volumes are not touched: the new container mounts the same data (AC-VOL-01).
		if err := e.Docker.Remove(ctx, ex.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
		ex = nil
	}
	if stopped {
		if ex != nil && ex.State == "running" {
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
		sp := e.containerSpec(w)
		sp.Mounts = mounts
		if _, err := e.Docker.CreateAndStart(ctx, sp); err != nil {
			return &codedErr{"container_start_failed", err}
		}
		return nil
	}
	switch ex.State {
	case "running", "restarting", "paused", "removing":
		return nil
	}
	// The file mounts are written again: /run is empty after a reboot.
	if _, err := e.prepareMounts(ctx, w); err != nil {
		return err
	}
	if err := e.Docker.Start(ctx, ex.ID); err != nil {
		return &codedErr{"container_start_failed", err}
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
	switch {
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
