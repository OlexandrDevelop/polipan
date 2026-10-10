// SPDX-License-Identifier: Apache-2.0

package logship

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

var t0 = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

// fakeSource is a Docker with append-only logs.
type fakeSource struct {
	mu    sync.Mutex
	conts []docker.Container
	logs  map[string][]docker.LogLine
}

func newSource(names ...string) *fakeSource {
	f := &fakeSource{logs: map[string][]docker.LogLine{}}
	for _, n := range names {
		f.conts = append(f.conts, docker.Container{ID: "id-" + n, Name: n, State: "running", Labels: map[string]string{
			"polipan.managed": "true", "polipan.service": "svc_" + n, "polipan.release": "rel_1", "polipan.replica": "0"}})
	}
	return f
}

func (f *fakeSource) List(context.Context, map[string]string) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]docker.Container(nil), f.conts...), nil
}

func (f *fakeSource) append(id string, ls ...docker.LogLine) {
	f.mu.Lock()
	f.logs[id] = append(f.logs[id], ls...)
	f.mu.Unlock()
}

func (f *fakeSource) FollowLogs(ctx context.Context, id string, o docker.LogOptions, fn func(docker.LogLine) error) error {
	i := 0
	for {
		f.mu.Lock()
		var l *docker.LogLine
		for i < len(f.logs[id]) {
			c := f.logs[id][i]
			i++
			if o.Since.IsZero() || !c.Time.Before(o.Since) {
				l = &c
				break
			}
		}
		f.mu.Unlock()
		if l != nil {
			if err := fn(*l); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// numbered makes n lines for a container. Three lines share a timestamp, to test the same-time counter.
func numbered(name string, from, n int) []docker.LogLine {
	var out []docker.LogLine
	for i := from; i < from+n; i++ {
		out = append(out, docker.LogLine{Stream: "stdout", Time: t0.Add(time.Duration(i/3) * time.Millisecond), Text: fmt.Sprintf("%s-%04d", name, i)})
	}
	return out
}

// fakeSink is the server side: it ignores a batch id that it has seen.
type fakeSink struct {
	mu     sync.Mutex
	seen   map[string]bool
	order  []string // batch ids, in the order they were taken
	lines  []entry
	status int  // 0 = ok, else this HTTP status
	down   bool // network error
	calls  int
}

func newSink() *fakeSink { return &fakeSink{seen: map[string]bool{}} }

func (k *fakeSink) Send(_ context.Context, _, id string, body []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.down {
		return fmt.Errorf("connection refused")
	}
	if k.status != 0 {
		return &StatusError{Code: k.status}
	}
	if k.seen[id] {
		return nil
	}
	k.seen[id] = true
	k.order = append(k.order, id)
	for _, ln := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if ln == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			panic(err)
		}
		k.lines = append(k.lines, e)
	}
	return nil
}

func (k *fakeSink) msgs(container string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, e := range k.lines {
		if e.Container == container {
			out = append(out, e.Msg)
		}
	}
	return out
}

func (k *fakeSink) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.lines)
}

func newShipper(dir string, src Source, snd Sender) *Shipper {
	s := &Shipper{Dir: dir, Source: src, Sender: snd, FlushLines: 7, FlushEvery: 10 * time.Millisecond,
		SyncEvery: 10 * time.Millisecond, BackoffMin: time.Millisecond, BackoffMax: 5 * time.Millisecond}
	s.Update(&agentv1.TelemetrySpec{LogsUrl: "https://server/ingest/logs"}, nil)
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// run starts the shipper and returns a stop function that waits for it.
func run(t *testing.T, s *Shipper) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
}

func checkAll(t *testing.T, sink *fakeSink, names []string, n int) {
	t.Helper()
	for _, name := range names {
		got := sink.msgs(name)
		var want []string
		for _, l := range numbered(name, 0, n) {
			want = append(want, l.Text)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %d lines, want %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: line %d is %q, want %q", name, i, got[i], want[i])
			}
		}
	}
}

// TestRestartAtEveryPoint kills the shipper at random points (before the spool write, after it, before
// the send, after the 2xx and before the delete) and starts it again: every line is in the sink once and in order.
func TestRestartAtEveryPoint(t *testing.T) {
	names := []string{"web", "api"}
	const n = 240
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			src := newSource(names...)
			for _, nm := range names {
				src.append("id-"+nm, numbered(nm, 0, n/2)...)
			}
			sink := newSink()
			dir := t.TempDir()
			for round := 0; round < 10; round++ {
				s := newShipper(dir, src, sink)
				kills := 1 + rng.IntN(6)
				seen := 0
				s.hook = func(string) bool { seen++; return seen == kills }
				stop := run(t, s)
				time.Sleep(time.Duration(5+rng.IntN(40)) * time.Millisecond)
				stop()
				if round == 3 { // more lines arrive while the agent restarts
					for _, nm := range names {
						src.append("id-"+nm, numbered(nm, n/2, n/2)...)
					}
				}
			}
			for _, nm := range names { // make sure all lines are there
				if len(src.logs["id-"+nm]) < n {
					src.append("id-"+nm, numbered(nm, n/2, n/2)...)
				}
			}
			s := newShipper(dir, src, sink)
			stop := run(t, s)
			waitFor(t, "all lines in the sink", func() bool { return sink.count() >= len(names)*n })
			time.Sleep(50 * time.Millisecond) // a repeat would show up here
			stop()
			checkAll(t, sink, names, n)
		})
	}
}

func TestKillPoints(t *testing.T) {
	for _, point := range []string{"before-spool", "spooled", "before-send", "after-send"} {
		for _, nth := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s-%d", point, nth), func(t *testing.T) {
				src := newSource("web")
				src.append("id-web", numbered("web", 0, 60)...)
				sink := newSink()
				dir := t.TempDir()
				s := newShipper(dir, src, sink)
				seen := 0
				s.hook = func(p string) bool {
					if p == point {
						seen++
					}
					return p == point && seen == nth
				}
				stop := run(t, s)
				waitFor(t, "the kill point", s.dead.Load)
				stop()
				s2 := newShipper(dir, src, sink)
				stop = run(t, s2)
				waitFor(t, "all lines", func() bool { return sink.count() >= 60 })
				time.Sleep(30 * time.Millisecond)
				stop()
				checkAll(t, sink, []string{"web"}, 60)
			})
		}
	}
}

func TestOfflineBufferThenOnline(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	sink.down = true
	s := newShipper(t.TempDir(), src, sink)
	stop := run(t, s)
	defer stop()
	src.append("id-web", numbered("web", 0, 50)...)
	waitFor(t, "batches in the spool", func() bool { return spoolSize(s) > 0 })
	time.Sleep(50 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatal("the sink is down")
	}
	src.append("id-web", numbered("web", 50, 50)...)
	sink.mu.Lock()
	sink.down = false
	sink.mu.Unlock()
	waitFor(t, "all lines", func() bool { return sink.count() >= 100 })
	time.Sleep(30 * time.Millisecond)
	stop()
	checkAll(t, sink, []string{"web"}, 100)
	if spoolSize(s) != 0 {
		t.Fatal("the spool must be empty after the server took everything")
	}
}

func TestBufferCapDropsOldest(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	sink.down = true
	s := newShipper(t.TempDir(), src, sink)
	s.FlushLines = 20
	s.Update(&agentv1.TelemetrySpec{LogsUrl: "https://server/ingest/logs", BufferMib: 1}, nil)
	stop := run(t, s)
	defer stop()
	big := strings.Repeat("x", 10_000)
	const total = 400 // about 4 MB
	for i := 0; i < total; i++ {
		src.append("id-web", docker.LogLine{Stream: "stdout", Time: t0.Add(time.Duration(i) * time.Millisecond), Text: fmt.Sprintf("%04d %s", i, big)})
	}
	waitFor(t, "batches dropped", func() bool { return s.Stats().DroppedBatches > 0 && spoolSize(s) > 0 })
	// Wait until every line is read and written, so that no trim runs while the sink takes batches.
	waitFor(t, "all lines read", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cur["id-web"].TS == t0.Add((total-1)*time.Millisecond).UnixNano()
	})
	time.Sleep(200 * time.Millisecond)
	sink.mu.Lock()
	sink.down = false
	sink.mu.Unlock()
	waitFor(t, "the rest is sent", func() bool { return sink.count() > 0 && spoolSize(s) == 0 })
	stop()
	got := sink.msgs("web")
	if len(got) >= total || len(got) == 0 {
		t.Fatalf("got %d lines of %d: the oldest must be dropped", len(got), total)
	}
	// What is left is the newest part, in order, with no gap.
	first := 0
	fmt.Sscanf(got[0], "%d", &first)
	for i, m := range got {
		var v int
		fmt.Sscanf(m, "%d", &v)
		if v != first+i {
			t.Fatalf("line %d is %d, want %d", i, v, first+i)
		}
	}
	if first+len(got) != total {
		t.Fatalf("the newest line is missing: first %d, count %d", first, len(got))
	}
}

func TestFormat(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	s := newShipper(t.TempDir(), src, sink)
	s.Update(&agentv1.TelemetrySpec{LogsUrl: "https://server/ingest/logs"}, []string{"hunter2-secret", "abc"}) // "abc" is too short to mask
	long := strings.Repeat("é", 40000)                                                                         // 80000 bytes
	src.append("id-web",
		docker.LogLine{Stream: "stdout", Time: t0, Text: `{"level":"warn","msg":"slow"}`},
		docker.LogLine{Stream: "stderr", Time: t0.Add(time.Millisecond), Text: "ERROR boom"},
		docker.LogLine{Stream: "stdout", Time: t0.Add(2 * time.Millisecond), Text: "login with hunter2-secret ok abc"},
		docker.LogLine{Stream: "stdout", Time: t0.Add(3 * time.Millisecond), Text: long},
		docker.LogLine{Stream: "stdout", Time: t0.Add(4 * time.Millisecond), Text: "plain text"},
	)
	stop := run(t, s)
	waitFor(t, "5 lines", func() bool { return sink.count() >= 5 })
	stop()
	e := sink.lines
	if e[0].Level != "warn" || e[1].Level != "error" || e[1].Stream != "stderr" || e[4].Level != "unknown" {
		t.Fatalf("levels: %+v", e)
	}
	if e[2].Msg != "login with *** ok abc" {
		t.Fatalf("secret not masked: %q", e[2].Msg)
	}
	if !strings.HasSuffix(e[3].Msg, cutMarker) || len(e[3].Msg) > MaxLineBytes+len(cutMarker) || !strings.HasPrefix(e[3].Msg, "éé") {
		t.Fatalf("long line: %d bytes", len(e[3].Msg))
	}
	if !strings.Contains(strings.TrimSuffix(e[3].Msg, cutMarker), "é") || strings.Contains(e[3].Msg, "�") {
		t.Fatal("the cut must be on a character border")
	}
	f := e[0]
	if f.Service != "svc_web" || f.Release != "rel_1" || f.Container != "web" || f.Replica != "0" || f.Time != "2025-03-01T12:00:00Z" {
		t.Fatalf("fields: %+v", f)
	}
}

func TestGuessLevel(t *testing.T) {
	for in, want := range map[string]string{
		`{"level":"error","msg":"x"}`:      "error",
		`{"severity":"WARNING"}`:           "warn",
		`{"level":30}`:                     "info",
		`{"msg":"no level"}`:               "unknown",
		"ERROR: failed":                    "error",
		"[warn] disk":                      "warn",
		"[error] x":                        "error",
		"2025-01-02 10:00:00 INFO started": "info",
		"2025-01-02T10:00:00Z DEBUG x":     "debug",
		`time="x" level=info msg="y"`:      "unknown", // not a prefix
		"level=warn msg=y":                 "warn",
		"INFO[0000] hello":                 "info",
		"just text":                        "unknown",
		"":                                 "unknown",
	} {
		if got := guessLevel(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

func TestRateLimit(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	s := newShipper(t.TempDir(), src, sink)
	s.RatePerSecond = 10
	for i := 0; i < 25; i++ { // all in one second
		src.append("id-web", docker.LogLine{Stream: "stdout", Time: t0.Add(time.Duration(i) * time.Millisecond), Text: fmt.Sprintf("l%d", i)})
	}
	src.append("id-web", docker.LogLine{Stream: "stdout", Time: t0.Add(2 * time.Second), Text: "later"}) // next second: allowed again
	stop := run(t, s)
	waitFor(t, "12 lines", func() bool { return sink.count() >= 12 })
	time.Sleep(30 * time.Millisecond)
	stop()
	var msgs []string
	for _, e := range sink.lines {
		msgs = append(msgs, e.Msg)
	}
	joined := strings.Join(msgs, "|")
	if len(msgs) != 12 || !strings.Contains(joined, "15 lines dropped") || !strings.Contains(joined, "later") || strings.Contains(joined, "l10") {
		t.Fatalf("got %d: %v", len(msgs), msgs)
	}
	if s.Stats().RateLimited != 15 {
		t.Fatalf("rate limited %d", s.Stats().RateLimited)
	}
}

func TestStatusCodes(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	sink.status = 400
	s := newShipper(t.TempDir(), src, sink)
	src.append("id-web", numbered("web", 0, 14)...)
	stop := run(t, s)
	defer stop()
	waitFor(t, "rejected batches", func() bool { return s.Stats().RejectedBatches >= 2 && spoolSize(s) == 0 })
	// 429 and 500 are retried and not dropped.
	sink.mu.Lock()
	sink.status = 429
	sink.mu.Unlock()
	src.append("id-web", numbered("web", 14, 7)...)
	time.Sleep(100 * time.Millisecond)
	if s.Stats().RejectedBatches != 2 || spoolSize(s) == 0 {
		t.Fatalf("429 must be retried: %+v", s.Stats())
	}
	sink.mu.Lock()
	sink.status = 0
	sink.mu.Unlock()
	waitFor(t, "the batch is taken", func() bool { return sink.count() >= 7 && spoolSize(s) == 0 })
}

func TestHTTPSenderHeader(t *testing.T) {
	var got string
	var ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ctype = r.Header.Get("X-Polipan-Batch"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	h := &HTTPSender{DataDir: t.TempDir()}
	// No identity in the folder: an error, and no panic.
	if err := h.Send(context.Background(), srv.URL, "ab.1", []byte("x")); err == nil {
		t.Fatal("want an error without identity")
	}
	_ = got
	_ = ctype
}

func TestEmptyURLShipsNothing(t *testing.T) {
	src := newSource("web")
	sink := newSink()
	s := newShipper(t.TempDir(), src, sink)
	s.Update(&agentv1.TelemetrySpec{}, nil)
	src.append("id-web", numbered("web", 0, 10)...)
	stop := run(t, s)
	time.Sleep(80 * time.Millisecond)
	if sink.count() != 0 || sink.calls != 0 {
		t.Fatal("nothing must be sent without logs_url")
	}
	s.Update(&agentv1.TelemetrySpec{LogsUrl: "https://server/ingest/logs"}, nil)
	waitFor(t, "lines after the url is set", func() bool { return sink.count() >= 10 })
	stop()
	checkAll(t, sink, []string{"web"}, 10)
}

// spoolSize is the size of the spool. It is safe to call while the shipper starts.
func spoolSize(s *Shipper) int64 {
	s.mu.Lock()
	sp := s.sp
	s.mu.Unlock()
	if sp == nil {
		return 0
	}
	return sp.size()
}
