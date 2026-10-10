// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

type fakeDocker struct {
	list  []docker.Container
	stats map[string]*docker.Stats
	rc    map[string]int
}

func (f *fakeDocker) List(context.Context, map[string]string) ([]docker.Container, error) {
	return f.list, nil
}
func (f *fakeDocker) Inspect(_ context.Context, id string) (*docker.Container, error) {
	return &docker.Container{ID: id, RestartCount: f.rc[id]}, nil
}
func (f *fakeDocker) Stats(_ context.Context, id string) (*docker.Stats, error) {
	return f.stats[id], nil
}

func fakeProc(t *testing.T, stat string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := func(name, s string) {
		if err := os.WriteFile(filepath.Join(root, "proc", name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("stat", stat)
	w("meminfo", "MemTotal:        8000 kB\nMemFree:          100 kB\nMemAvailable:    3000 kB\n")
	w("loadavg", "0.52 0.40 0.30 1/200 999\n")
	return root
}

func has(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want+"\n") {
		t.Fatalf("missing line %q in:\n%s", want, out)
	}
}

func TestRenderFamilies(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	lab := map[string]string{"polipan.managed": "true", "polipan.org": "org_1", "polipan.project": "prj_1",
		"polipan.environment": "env_1", "polipan.service": "svc_1", "polipan.release": "rel_1", "polipan.replica": "0"}
	fd := &fakeDocker{
		list: []docker.Container{
			{ID: "c1", State: "running", Labels: lab},
			{ID: "c2", State: "exited", Labels: map[string]string{"polipan.managed": "true", "polipan.service": "svc_2"}},
			{ID: "c3", State: "running", Labels: map[string]string{"polipan.managed": "true"}}, // no service: skipped
		},
		rc: map[string]int{"c1": 2},
		stats: map[string]*docker.Stats{"c1": {Time: base, CPUTotalNs: 1_000_000_000, MemoryBytes: 100, MemoryLimit: 200,
			NetRxBytes: 5, NetTxBytes: 6, BlkioBytes: 7}},
	}
	now := base
	root := fakeProc(t, "cpu  100 0 100 800 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0\n")
	c := &Collector{Docker: fd, NodeID: "nod_1", Root: root,
		Disk: func(string) (uint64, uint64, error) { return 40, 100, nil },
		Health: func() []HealthSample {
			return []HealthSample{{Service: "svc_1", Release: "rel_1", Replica: "0", Up: true, DurationSeconds: 0.25}}
		},
		Volumes: func() []VolumeSample {
			return []VolumeSample{{Service: "svc_1", Volume: "data", UsedBytes: 10, UsedInodes: 3}}
		},
		DesiredVersion: func() int64 { return 42 },
		Now:            func() time.Time { return now },
	}
	out := string(c.Render(context.Background()))
	l := `{org="org_1",project="prj_1",environment="env_1",service="svc_1",release="rel_1",node="nod_1",replica="0"}`
	has(t, out, "polipan_container_memory_bytes"+l+" 100")
	has(t, out, "polipan_container_memory_limit_bytes"+l+" 200")
	has(t, out, "polipan_container_net_rx_bytes"+l+" 5")
	has(t, out, "polipan_container_net_tx_bytes"+l+" 6")
	has(t, out, "polipan_container_blkio_bytes"+l+" 7")
	has(t, out, "polipan_container_restarts_total"+l+" 2")
	has(t, out, `polipan_container_restarts_total{org="",project="",environment="",service="svc_2",release="",node="nod_1",replica=""} 0`)
	has(t, out, `polipan_node_containers{node="nod_1"} 1`)
	has(t, out, `polipan_node_memory_bytes{node="nod_1"} 5120000`)
	has(t, out, `polipan_node_load1{node="nod_1"} 0.52`)
	has(t, out, `polipan_node_disk_used_bytes{node="nod_1"} 40`)
	has(t, out, `polipan_node_disk_total_bytes{node="nod_1"} 100`)
	// 20% busy since boot, 2 cores: 400 thousandths of a core.
	has(t, out, `polipan_node_cpu_usage_millis{node="nod_1"} 400`)
	has(t, out, `polipan_volume_used_bytes{service="svc_1",volume="data",node="nod_1"} 10`)
	has(t, out, `polipan_volume_inodes_used{service="svc_1",volume="data",node="nod_1"} 3`)
	has(t, out, `polipan_health_check_up{service="svc_1",release="rel_1",replica="0"} 1`)
	has(t, out, `polipan_health_check_duration_seconds{service="svc_1",release="rel_1",replica="0"} 0.25`)
	has(t, out, `polipan_agent_desired_state_version{node="nod_1"} 42`)
	has(t, out, "# TYPE polipan_container_restarts_total counter")
	if strings.Contains(out, "polipan_container_cpu_usage_millis") {
		t.Fatal("the first reading has no CPU rate yet")
	}
	if strings.Contains(out, "svc_3") || strings.Count(out, "# TYPE polipan_node_load1") != 1 {
		t.Fatalf("bad output:\n%s", out)
	}

	// Second reading 10 s later: the container used 5 s of CPU, the node 50% of 2 cores.
	now = base.Add(10 * time.Second)
	fd.stats["c1"] = &docker.Stats{Time: now, CPUTotalNs: 6_000_000_000, MemoryBytes: 100}
	root2 := fakeProc(t, "cpu  200 0 200 1600 0 0 0 0\ncpu0 1 0 0 0 0 0 0 0\ncpu1 1 0 0 0 0 0 0 0\n")
	c.Root = root2
	out = string(c.Render(context.Background()))
	has(t, out, "polipan_container_cpu_usage_millis"+l+" 500")
	has(t, out, `polipan_node_cpu_usage_millis{node="nod_1"} 400`)
}

func TestRenderCache(t *testing.T) {
	root := fakeProc(t, "cpu  1 0 1 8 0 0 0 0\ncpu0 1 0 1 8 0 0 0 0\n")
	now := time.Now()
	c := &Collector{NodeID: "n", Root: root, Disk: func(string) (uint64, uint64, error) { return 1, 2, nil }, Now: func() time.Time { return now }}
	a := c.Render(context.Background())
	b := c.Render(context.Background())
	if &a[0] != &b[0] {
		t.Fatal("two close scrapes must share one result")
	}
}

const (
	line200 = `{"level":"info","ts":1.7e9,"logger":"http.log.access.access","msg":"handled request","request":{"host":"App.Example.com:443","uri":"/secret/path?id=1"},"status":200,"duration":0.03,"upstream":"10.0.0.2:8080"}`
	line503 = `{"level":"error","logger":"http.log.access.access","msg":"handled request","request":{"host":"app.example.com"},"status":503,"duration":12,"upstream":"10.0.0.3:8080"}`
)

func TestAccessMapping(t *testing.T) {
	a := &Access{NodeID: "nod_1"}
	a.SetSpec(&agentv1.IngressSpec{Routes: []*agentv1.Route{{
		RouteId: "r1", ServiceId: "svc_1", Hosts: []string{"app.example.com"},
		Upstreams: []*agentv1.Upstream{{Address: "10.0.0.2:8080", ReleaseId: "rel_blue"}, {Address: "10.0.0.3:8080", ReleaseId: "rel_green"}},
	}}})
	for _, l := range []string{
		line200, line200, line503,
		`{"logger":"http.log.access.access","status":200,"duration":1,"request":{"host":"app.example.com"},"upstream":""}`,               // no upstream
		`{"logger":"http.log.access.access","status":200,"duration":1,"request":{"host":"evil.example.net"},"upstream":"10.0.0.2:8080"}`, // host not in the spec
		`{"logger":"http.log.access.access","status":200,"duration":1,"request":{"host":"app.example.com"},"upstream":"10.9.9.9:1"}`,     // unknown upstream
		`{"logger":"admin","msg":"x","status":200}`,
		`not json http.log.access`,
	} {
		a.Observe(l)
	}
	e := newExposition()
	a.write(e)
	out := string(e.bytes())
	has(t, out, `polipan_http_requests_total{service="svc_1",release="rel_blue",domain="app.example.com",status_class="2xx",node="nod_1"} 2`)
	has(t, out, `polipan_http_requests_total{service="svc_1",release="rel_green",domain="app.example.com",status_class="5xx",node="nod_1"} 1`)
	if strings.Count(out, "polipan_http_requests_total{") != 2 || strings.Contains(out, "secret") || strings.Contains(out, "evil") {
		t.Fatalf("unexpected series:\n%s", out)
	}
	// 0.03 s twice: first bucket that holds it is 0.05.
	pre := `polipan_http_request_duration_seconds`
	b := `{service="svc_1",release="rel_blue",domain="app.example.com",node="nod_1"`
	has(t, out, pre+`_bucket`+b+`,le="0.025"} 0`)
	has(t, out, pre+`_bucket`+b+`,le="0.05"} 2`)
	has(t, out, pre+`_bucket`+b+`,le="10"} 2`)
	has(t, out, pre+`_bucket`+b+`,le="+Inf"} 2`)
	has(t, out, pre+`_sum`+b+`} 0.06`)
	has(t, out, pre+`_count`+b+`} 2`)
	// 12 s is above the last bucket: only +Inf counts it.
	g := `{service="svc_1",release="rel_green",domain="app.example.com",node="nod_1"`
	has(t, out, pre+`_bucket`+g+`,le="10"} 0`)
	has(t, out, pre+`_bucket`+g+`,le="+Inf"} 1`)
	if strings.Count(out, "# TYPE "+pre+" histogram") != 1 {
		t.Fatal("one TYPE line per family")
	}
}
