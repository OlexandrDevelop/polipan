// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
)

// DefaultAddr is where the agent serves the metrics. It is the local address only: vmagent runs on the host network.
const DefaultAddr = "127.0.0.1:9445"

// Labels the server puts on every workload (12 section 7). Their values are ids.
const (
	labelManaged     = "polipan.managed"
	labelOrg         = "polipan.org"
	labelProject     = "polipan.project"
	labelEnvironment = "polipan.environment"
	labelService     = "polipan.service"
	labelRelease     = "polipan.release"
	labelReplica     = "polipan.replica"
)

// Docker is the part of the Docker client that the collector uses.
type Docker interface {
	List(ctx context.Context, labels map[string]string) ([]docker.Container, error)
	Inspect(ctx context.Context, idOrName string) (*docker.Container, error)
	Stats(ctx context.Context, idOrName string) (*docker.Stats, error)
}

// HealthSample is the last health check result of one replica.
type HealthSample struct {
	Service, Release, Replica string
	Up                        bool
	DurationSeconds           float64
}

// VolumeSample is the usage of one volume.
type VolumeSample struct {
	Service, Volume string
	UsedBytes       uint64
	UsedInodes      uint64
}

// Collector makes the metrics text. Only the fixed labels of the spec are used, and the request path is never a label.
type Collector struct {
	Docker Docker
	NodeID string
	// Root is the folder that holds proc/. Empty = "/".
	Root string
	// DiskPath is the file system that polipan_node_disk_* measures. Empty = "/".
	DiskPath string
	// Disk reads the disk usage. Nil = statfs (tests replace it).
	Disk func(path string) (used, total uint64, err error)
	// The sources below come from the reconcile engine. Each one is optional.
	Health         func() []HealthSample
	Volumes        func() []VolumeSample
	DesiredVersion func() int64
	Access         *Access
	Log            *slog.Logger
	Now            func() time.Time

	mu       sync.Mutex
	cpuPrev  map[string]cpuPoint // container id -> last CPU reading
	nodePrev *cpuTimes
	cacheAt  time.Time
	cache    []byte
}

type cpuPoint struct {
	ns uint64
	at time.Time
}

const (
	statsTimeout  = 4 * time.Second
	statsParallel = 8
	cacheFor      = 3 * time.Second // two scrapes close together share the result
)

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Collector) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Render returns the metrics in the Prometheus text format.
func (c *Collector) Render(ctx context.Context) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); c.cache != nil && now.Sub(c.cacheAt) < cacheFor {
		return c.cache
	}
	e := newExposition()
	c.containers(ctx, e)
	c.node(e)
	c.volumes(e)
	c.health(e)
	if c.DesiredVersion != nil {
		e.add("polipan_agent_desired_state_version", "Version of the desired state that the agent holds.", "gauge",
			float64(c.DesiredVersion()), "node", c.NodeID)
	}
	if c.Access != nil {
		c.Access.write(e)
	}
	c.cache, c.cacheAt = e.bytes(), c.now()
	return c.cache
}

type containerRow struct {
	ct    docker.Container
	stats *docker.Stats
	rc    int
}

func (c *Collector) containers(ctx context.Context, e *exposition) {
	if c.Docker == nil {
		return
	}
	list, err := c.Docker.List(ctx, map[string]string{labelManaged: "true"})
	if err != nil {
		c.log().Warn("metrics: cannot list containers", "error", err)
		return
	}
	var running float64
	var rows []*containerRow
	for _, ct := range list {
		if ct.Labels[labelService] == "" {
			continue // not a workload (a one-off job, for example)
		}
		if ct.State == "running" {
			running++
		}
		rows = append(rows, &containerRow{ct: ct})
	}
	e.add("polipan_node_containers", "Running containers of services on this node.", "gauge", running, "node", c.NodeID)

	sem := make(chan struct{}, statsParallel)
	var wg sync.WaitGroup
	for _, r := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()
			if ic, err := c.Docker.Inspect(cctx, r.ct.ID); err == nil {
				r.rc = ic.RestartCount
			}
			if r.ct.State == "running" {
				if st, err := c.Docker.Stats(cctx, r.ct.ID); err == nil {
					r.stats = st
				}
			}
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	if c.cpuPrev == nil {
		c.cpuPrev = map[string]cpuPoint{}
	}
	for _, r := range rows {
		l := []string{"org", r.ct.Labels[labelOrg], "project", r.ct.Labels[labelProject],
			"environment", r.ct.Labels[labelEnvironment], "service", r.ct.Labels[labelService],
			"release", r.ct.Labels[labelRelease], "node", c.NodeID, "replica", r.ct.Labels[labelReplica]}
		e.add("polipan_container_restarts_total", "Restarts of the container by Docker.", "counter", float64(r.rc), l...)
		st := r.stats
		if st == nil {
			continue
		}
		seen[r.ct.ID] = true
		if millis, ok := c.cpuMillis(r.ct.ID, st); ok {
			e.add("polipan_container_cpu_usage_millis", "CPU in use, in thousandths of a core.", "gauge", millis, l...)
		}
		e.add("polipan_container_memory_bytes", "Memory in use, without the page cache.", "gauge", float64(st.MemoryBytes), l...)
		e.add("polipan_container_memory_limit_bytes", "Memory limit of the container.", "gauge", float64(st.MemoryLimit), l...)
		e.add("polipan_container_net_rx_bytes", "Bytes received on the network.", "counter", float64(st.NetRxBytes), l...)
		e.add("polipan_container_net_tx_bytes", "Bytes sent on the network.", "counter", float64(st.NetTxBytes), l...)
		e.add("polipan_container_blkio_bytes", "Bytes read from and written to block devices.", "counter", float64(st.BlkioBytes), l...)
	}
	for id := range c.cpuPrev {
		if !seen[id] {
			delete(c.cpuPrev, id)
		}
	}
}

// cpuMillis is the CPU use since the last reading. The first reading uses the sample Docker keeps from before.
func (c *Collector) cpuMillis(id string, st *docker.Stats) (float64, bool) {
	at := st.Time
	if at.IsZero() {
		at = c.now()
	}
	prev, ok := c.cpuPrev[id]
	c.cpuPrev[id] = cpuPoint{ns: st.CPUTotalNs, at: at}
	if !ok && !st.PreTime.IsZero() && st.PreTime.Year() > 1 && st.PreCPUTotal > 0 {
		prev, ok = cpuPoint{ns: st.PreCPUTotal, at: st.PreTime}, true
	}
	dt := at.Sub(prev.at)
	if !ok || dt <= 0 || st.CPUTotalNs < prev.ns {
		return 0, false
	}
	return float64(st.CPUTotalNs-prev.ns) / float64(dt.Nanoseconds()) * 1000, true
}

func (c *Collector) node(e *exposition) {
	root := c.Root
	if root == "" {
		root = "/"
	}
	s, err := readProc(root)
	if err != nil {
		c.log().Warn("metrics: cannot read /proc", "error", err)
		return
	}
	// CPU in use = busy share of the time since the last reading, times the number of cores.
	prev := c.nodePrev
	c.nodePrev = &cpuTimes{busy: s.cpu.busy, total: s.cpu.total}
	if prev == nil {
		prev = &cpuTimes{} // the first reading is the average since boot
	}
	if dt := s.cpu.total - prev.total; s.cpu.total >= prev.total && dt > 0 {
		millis := float64(s.cpu.busy-prev.busy) / float64(dt) * float64(s.cores) * 1000
		e.add("polipan_node_cpu_usage_millis", "CPU in use on the node, in thousandths of a core.", "gauge", millis, "node", c.NodeID)
	}
	e.add("polipan_node_memory_bytes", "Memory in use on the node (total minus available).", "gauge", float64(s.memUsed), "node", c.NodeID)
	e.add("polipan_node_load1", "Load average of the last minute.", "gauge", s.load1, "node", c.NodeID)
	disk, path := c.Disk, c.DiskPath
	if disk == nil {
		disk = diskUsage
	}
	if path == "" {
		path = "/"
	}
	if used, total, err := disk(path); err == nil {
		e.add("polipan_node_disk_used_bytes", "Used bytes of the node disk.", "gauge", float64(used), "node", c.NodeID)
		e.add("polipan_node_disk_total_bytes", "Size of the node disk.", "gauge", float64(total), "node", c.NodeID)
	}
}

func (c *Collector) volumes(e *exposition) {
	if c.Volumes == nil {
		return
	}
	for _, v := range c.Volumes() {
		l := []string{"service", v.Service, "volume", v.Volume, "node", c.NodeID}
		e.add("polipan_volume_used_bytes", "Bytes used in the volume.", "gauge", float64(v.UsedBytes), l...)
		e.add("polipan_volume_inodes_used", "Inodes used in the volume.", "gauge", float64(v.UsedInodes), l...)
	}
}

func (c *Collector) health(e *exposition) {
	if c.Health == nil {
		return
	}
	for _, h := range c.Health() {
		l := []string{"service", h.Service, "release", h.Release, "replica", h.Replica}
		up := 0.0
		if h.Up {
			up = 1
		}
		e.add("polipan_health_check_up", "1 when the replica passes its health check.", "gauge", up, l...)
		e.add("polipan_health_check_duration_seconds", "Duration of the last health check.", "gauge", h.DurationSeconds, l...)
	}
}

// Handler serves the text on /metrics.
func (c *Collector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		b := c.Render(r.Context())
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	})
	return mux
}

// Serve listens on addr until ctx ends. Only a loopback address is accepted: the numbers are not for the network.
func (c *Collector) Serve(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		c.log().Warn("the metrics address is not a loopback address", "addr", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
