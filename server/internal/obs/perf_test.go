// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestPerf_LogSearch1GB is the log target of docs/spec/13-testing-and-quality.md section 2: a log search over 1 GB
// of logs answers in 3 s. It fills a real VictoriaLogs with synthetic lines of one org (about POLIPAN_PERF_MB MB of raw
// JSON, default 1024) and runs the searches of the panel through Backends.Search. It runs only when
// POLIPAN_PERF_VL_URL is set, for example http://127.0.0.1:19428 of a throw-away victoria-logs container.
func TestPerf_LogSearch1GB(t *testing.T) {
	base := os.Getenv("POLIPAN_PERF_VL_URL")
	if base == "" {
		t.Skip("set POLIPAN_PERF_VL_URL to run the log search performance test")
	}
	mb := 1024
	if v, err := strconv.Atoi(os.Getenv("POLIPAN_PERF_MB")); err == nil && v > 0 {
		mb = v
	}
	b := NewBackends("", base, "", "")
	ctx := context.Background()
	words := []string{"GET", "POST", "/api/v1/items", "/healthz", "200", "404", "user", "session", "cache", "miss", "hit", "query", "took", "ms",
		"connection", "pool", "worker", "started", "finished", "retry", "upstream", "payload", "bytes", "order", "created"}
	rng := rand.New(rand.NewSource(7))
	now := time.Now().UTC()
	span := 7 * 24 * time.Hour
	var batch bytes.Buffer
	total, lines, hits := 0, 0, 0
	post := func() {
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/insert/jsonline?_stream_fields=org,project,environment,service,node&_msg_field=_msg&_time_field=_time", bytes.NewReader(batch.Bytes()))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("insert: %d", resp.StatusCode)
		}
		batch.Reset()
	}
	start := time.Now()
	for total < mb<<20 {
		msg := ""
		for i := 0; i < 14; i++ {
			msg += words[rng.Intn(len(words))] + " "
		}
		msg += strconv.Itoa(rng.Intn(1_000_000))
		if rng.Intn(5000) == 0 {
			msg += " upstream timeout"
			hits++
		}
		ts := now.Add(-time.Duration(rng.Int63n(int64(span))))
		line := fmt.Sprintf(`{"_time":%q,"_msg":%q,"org":"org_perf","project":"prj_%d","environment":"env_%d","service":"svc_%02d","node":"nod_%d","level":"info","stream":"stdout"}`+"\n",
			ts.Format(time.RFC3339Nano), msg, rng.Intn(4), rng.Intn(8), rng.Intn(20), rng.Intn(5))
		batch.WriteString(line)
		total += len(line)
		lines++
		if batch.Len() > 16<<20 {
			post()
		}
	}
	post()
	t.Logf("PERF logs: ingested %d lines, %d MB of JSON, %d lines with the search text, in %s", lines, total>>20, hits, time.Since(start).Round(time.Second))
	// Wait until VictoriaLogs has flushed the data to searchable parts.
	time.Sleep(5 * time.Second)

	run := func(name string, f LogFilter) {
		f.OrgID, f.Limit = "org_perf", 100
		f.From, f.To = now.Add(-span-time.Hour), now.Add(time.Hour)
		for i := 0; i < 3; i++ {
			t0 := time.Now()
			page, err := b.Search(ctx, now, f)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			d := time.Since(t0)
			t.Logf("PERF logs search %q run %d: %s, %d lines", name, i+1, d.Round(time.Millisecond), len(page.Items))
			if d > 3*time.Second {
				t.Errorf("%s: %s is over the target of 3 s", name, d)
			}
		}
	}
	run("text timeout, all services, 7 days", LogFilter{Text: "timeout"})
	run("text timeout, one service", LogFilter{Text: "timeout", ServiceID: "svc_03"})
	run("regex, one environment", LogFilter{Text: "/upstream time(out)?/", EnvironmentID: "env_2"})
	run("no text, newest 100", LogFilter{})
}
