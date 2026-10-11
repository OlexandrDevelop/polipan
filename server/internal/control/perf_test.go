// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// TestPerf_ScaleTarget is the performance run of docs/spec/13-testing-and-quality.md section 2: 200 services,
// 20 nodes and 50 deploys per hour. The nodes are fake agents on a real gRPC gateway and a real PostgreSQL, so the
// numbers are those of the control plane without Docker. It runs only when POLIPAN_PERF=1.
//
//	POLIPAN_PERF=1 go test ./server/internal/control -run TestPerf_ScaleTarget -v -timeout 20m
func TestPerf_ScaleTarget(t *testing.T) {
	if os.Getenv("POLIPAN_PERF") == "" {
		t.Skip("set POLIPAN_PERF=1 to run the performance suite")
	}
	nodes, services, redeploys := envInt("POLIPAN_PERF_NODES", 20), envInt("POLIPAN_PERF_SERVICES", 200), envInt("POLIPAN_PERF_REDEPLOYS", 50)
	m := setupM7(t)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < nodes; i++ {
		m.addNode(t, fmt.Sprintf("perf-%02d", i))
	}
	// The fake nodes send a heartbeat every second, like real agents, so they stay ready during the run.
	stopBeats := make(chan struct{})
	var beats sync.WaitGroup
	beats.Add(1)
	go func() {
		defer beats.Done()
		for {
			select {
			case <-stopBeats:
				return
			case <-time.After(time.Second):
				for _, n := range m.nodes {
					n.a.Heartbeat(t)
				}
			}
		}
	}()
	defer func() { close(stopBeats); beats.Wait() }()
	t.Logf("PERF join %d nodes: %s", nodes, time.Since(start).Round(time.Millisecond))

	var envID string
	if err := m.env.Pool.QueryRow(ctx, `SELECT environment_id FROM services WHERE id = $1`, m.serviceID).Scan(&envID); err != nil {
		t.Fatal(err)
	}
	ids := []string{m.serviceID}
	for i := 1; i < services; i++ {
		id := gateway.NewID("svc")
		if _, err := m.env.Pool.Exec(ctx, `INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1,$2,$3,'app','image',
			'{"image":"nginx:1.27","port":8080,"health":{"type":"tcp","port":8080},"strategy_options":{"drain_seconds":0}}')`, id, envID, fmt.Sprintf("web-%03d", i)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	var deployWave func(label string, list []string)
	// deployAll starts the deploys in waves and measures the time of each until it is in a final state.
	deployAll := func(label string, all []string) {
		wave := envInt("POLIPAN_PERF_WAVE", 20)
		whole := time.Now()
		for i := 0; i < len(all); i += wave {
			end := i + wave
			if end > len(all) {
				end = len(all)
			}
			deployWave(label, all[i:end])
		}
		t.Logf("PERF %s: %d deploys in waves of %d took %s, %.1f deploys per minute (target: 50 per hour)", label, len(all), wave,
			time.Since(whole).Round(time.Millisecond), float64(len(all))/time.Since(whole).Minutes())
	}
	deployWave = func(label string, list []string) {
		var mu sync.Mutex
		var lat []time.Duration
		failed := 0
		reasons := map[string]int{}
		var wg sync.WaitGroup
		begin := time.Now()
		for _, sid := range list {
			wg.Add(1)
			go func(sid string) {
				defer wg.Done()
				t0 := time.Now()
				id, _, err := m.ctl.DeployWith(ctx, orchestrator.DeployParams{ServiceID: sid})
				state := "error"
				if err == nil {
					deadline := time.Now().Add(5 * time.Minute)
					for time.Now().Before(deadline) {
						_ = m.env.Pool.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1`, id).Scan(&state)
						if terminalDeployment[state] {
							break
						}
						time.Sleep(50 * time.Millisecond)
					}
				}
				mu.Lock()
				defer mu.Unlock()
				lat = append(lat, time.Since(t0))
				if state != "live" {
					failed++
					var reason *string
					_ = m.env.Pool.QueryRow(ctx, `SELECT failure_reason || ': ' || left(coalesce(failure_detail, ''), 90) FROM deployments WHERE id = $1`, id).Scan(&reason)
					if reason != nil {
						reasons[*reason]++
					}
				}
			}(sid)
		}
		wg.Wait()
		total := time.Since(begin)
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		t.Logf("PERF wave %s: %d deploys at once, all done in %s, p50 %s, p95 %s, max %s, not live: %d", label, len(list),
			total.Round(time.Millisecond), lat[len(lat)/2].Round(time.Millisecond), lat[len(lat)*95/100].Round(time.Millisecond),
			lat[len(lat)-1].Round(time.Millisecond), failed)
		if failed > 0 {
			t.Errorf("%s: %d deploys did not go live: %v", label, failed, reasons)
		}
		// 50 deploys per hour is one in 72 s. The whole burst must be far faster than that.
		if per := total / time.Duration(len(list)); per > 72*time.Second {
			t.Errorf("%s: %s per deploy is slower than the target of one per 72 s", label, per)
		}
	}
	deployAll(fmt.Sprintf("first deploy of %d services", services), ids)
	deployAll(fmt.Sprintf("%d redeploys at once", redeploys), ids[:redeploys])

	// The desired state of a node holds only its own workloads.
	var placements int
	_ = m.env.Pool.QueryRow(ctx, `SELECT count(*) FROM placements WHERE role = 'live'`).Scan(&placements)
	var most int
	_ = m.env.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(c), 0) FROM (SELECT count(*) c FROM placements WHERE role = 'live' GROUP BY node_id) x`).Scan(&most)
	t.Logf("PERF live placements: %d for %d services on %d nodes, the busiest node has %d (services have no resource limits)", placements, services, nodes, most)
	// Services without limits must spread: no node may take more than twice its share.
	if most > 2*(services+nodes-1)/nodes {
		t.Errorf("the busiest node has %d of %d services on %d nodes", most, services, nodes)
	}
}
