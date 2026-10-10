// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// Buckets are the upper bounds of the request duration histogram, in seconds.
var Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// maxLogLine is the longest Caddy line that is read. An access line is far shorter.
const maxLogLine = 64 << 10

type target struct{ service, release string }

type reqKey struct{ service, release, domain, class string }

type histKey struct{ service, release, domain string }

type hist struct {
	counts []uint64 // per bucket, not cumulative (the +Inf bucket is the count)
	sum    float64
	count  uint64
}

// Access counts the requests of the Caddy access log (REQ-OBS-103). Caddy has no release label: the upstream
// of each request is mapped to its route (service) and release with the ingress spec.
type Access struct {
	NodeID string
	Log    *slog.Logger

	mu       sync.Mutex
	upstream map[string]target
	hosts    map[string]bool
	reqs     map[reqKey]uint64
	hists    map[histKey]*hist
}

// SetSpec reads the upstreams and the hosts of the current ingress spec.
func (a *Access) SetSpec(spec *agentv1.IngressSpec) {
	up := map[string]target{}
	hosts := map[string]bool{}
	for _, r := range spec.GetRoutes() {
		for _, h := range r.GetHosts() {
			hosts[strings.ToLower(strings.TrimSpace(h))] = true
		}
		for _, u := range r.GetUpstreams() {
			up[u.GetAddress()] = target{service: r.GetServiceId(), release: u.GetReleaseId()}
		}
	}
	a.mu.Lock()
	a.upstream, a.hosts = up, hosts
	a.mu.Unlock()
}

type accessLine struct {
	Logger   string  `json:"logger"`
	Status   int     `json:"status"`
	Duration float64 `json:"duration"`
	Upstream string  `json:"upstream"`
	Request  struct {
		Host string `json:"host"`
	} `json:"request"`
}

// Observe reads one line of the Caddy log. It ignores everything that is not an access line it can map.
// A request with no known upstream (a redirect, the 404 page) and a host that is not in the spec are dropped:
// the Host header comes from the client and must not make new series.
func (a *Access) Observe(line string) {
	if !strings.Contains(line, "http.log.access") {
		return
	}
	var l accessLine
	if json.Unmarshal([]byte(line), &l) != nil || !strings.HasPrefix(l.Logger, "http.log.access") || l.Status == 0 {
		return
	}
	host := strings.ToLower(l.Request.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.upstream[l.Upstream]
	if !ok || !a.hosts[host] {
		return
	}
	if a.reqs == nil {
		a.reqs, a.hists = map[reqKey]uint64{}, map[histKey]*hist{}
	}
	a.reqs[reqKey{t.service, t.release, host, strconv.Itoa(l.Status/100) + "xx"}]++
	hk := histKey{t.service, t.release, host}
	h := a.hists[hk]
	if h == nil {
		h = &hist{counts: make([]uint64, len(Buckets))}
		a.hists[hk] = h
	}
	h.sum += l.Duration
	h.count++
	for i, b := range Buckets {
		if l.Duration <= b {
			h.counts[i]++
			break
		}
	}
}

func (a *Access) write(e *exposition) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, n := range a.reqs {
		e.add("polipan_http_requests_total", "Requests handled by the ingress.", "counter", float64(n),
			"service", k.service, "release", k.release, "domain", k.domain, "status_class", k.class, "node", a.NodeID)
	}
	keys := make([]histKey, 0, len(a.hists))
	for k := range a.hists {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		h := a.hists[k]
		e.addHistogram("polipan_http_request_duration_seconds", "Duration of requests handled by the ingress.",
			Buckets, h.counts, h.sum, h.count, "service", k.service, "release", k.release, "domain", k.domain, "node", a.NodeID)
	}
}

// LogSource follows the log of a container.
type LogSource interface {
	FollowLogs(ctx context.Context, idOrName string, o docker.LogOptions, fn func(docker.LogLine) error) error
}

// Follow reads the Caddy container log until ctx ends. Only new lines count: after an agent restart the
// counters start again, which Prometheus rate() handles.
func (a *Access) Follow(ctx context.Context, src LogSource, container string) {
	since := time.Now()
	for ctx.Err() == nil {
		err := src.FollowLogs(ctx, container, docker.LogOptions{Since: since, Follow: true, MaxLine: maxLogLine}, func(l docker.LogLine) error {
			if !l.Time.IsZero() {
				since = l.Time.Add(time.Nanosecond)
			}
			a.Observe(l.Text)
			return nil
		})
		wait := time.Second
		if err != nil && ctx.Err() == nil {
			if a.Log != nil {
				a.Log.Debug("cannot follow the caddy log", "error", err)
			}
			wait = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
