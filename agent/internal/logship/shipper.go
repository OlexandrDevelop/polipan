// SPDX-License-Identifier: Apache-2.0

// Package logship reads the logs of the workload containers and sends them to the server in batches
// (12-observability, section 3 and section 7). A batch is saved on disk with the read positions before it
// is sent, so an agent restart or a server outage loses and repeats nothing (REQ-OBS-001, -006, -102).
package logship

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

const (
	// MaxLineBytes is the longest message that is sent. A longer line is cut and gets a marker.
	MaxLineBytes = 64 << 10
	// DefaultRatePerSecond is the number of lines per second and per service. More lines are dropped and counted.
	DefaultRatePerSecond = 5000
	// DefaultBufferMiB is the size of the disk buffer when telemetry.buffer_mib is 0.
	DefaultBufferMiB = 100

	cutMarker = " ...[truncated]"
	readCap   = 256 << 10 // a reader keeps this much of a line, so a secret near the cut is still found
)

const (
	labelManaged = "polipan.managed"
	labelService = "polipan.service"
	labelRelease = "polipan.release"
	labelReplica = "polipan.replica"
)

// Source is the part of the Docker client that the shipper uses.
type Source interface {
	List(ctx context.Context, labels map[string]string) ([]docker.Container, error)
	FollowLogs(ctx context.Context, idOrName string, o docker.LogOptions, fn func(docker.LogLine) error) error
}

// Stats are counters since the start of the process.
type Stats struct {
	Shipped         uint64 // lines in batches that the server took
	DroppedBatches  uint64 // batches removed by the buffer size cap
	RejectedBatches uint64 // batches the server refused with a 4xx (not 429)
	RateLimited     uint64 // lines dropped by the rate limit
}

// Shipper follows the container logs and ships them.
type Shipper struct {
	// Dir is the folder of the cursors and the spool.
	Dir    string
	Source Source
	Sender Sender
	Log    *slog.Logger

	// Settings. Zero values mean the defaults.
	RatePerSecond int
	FlushLines    int           // default 1000
	FlushBytes    int           // default 1 MiB
	FlushEvery    time.Duration // default 1 s
	SyncEvery     time.Duration // how often the container list is read, default 2 s
	BackoffMin    time.Duration // default 1 s
	BackoffMax    time.Duration // default 60 s

	mu      sync.Mutex
	cfg     *agentv1.TelemetrySpec
	masker  *strings.Replacer
	readers map[string]context.CancelFunc
	cur     map[string]Cursor // the position of every reader: the last line that went into the line channel
	wake    chan struct{}     // tells the sender that a batch is waiting
	cfgWake chan struct{}     // tells the reader loop that the telemetry spec changed
	initCh  sync.Once

	sp    *spool
	lines chan rawLine

	dead   atomic.Bool // set by a test hook: no more disk writes and no more sends, like a killed process
	killFn context.CancelFunc
	hook   func(point string) bool // tests: return true at a point to "kill" the shipper there

	shipped, droppedBatches, rejected, rateLimited atomic.Uint64
}

type rawLine struct {
	id, name                  string
	service, release, replica string
	line                      docker.LogLine
	cur                       Cursor
}

func (s *Shipper) initChans() {
	s.initCh.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.cfgWake = make(chan struct{}, 1)
	})
}

// Update sets the telemetry spec and the secret values to mask. It is called for every new desired state.
// An empty logs_url means: do not read and do not send.
func (s *Shipper) Update(t *agentv1.TelemetrySpec, secrets []string) {
	s.initChans()
	var pairs []string
	for _, v := range secrets {
		if len(v) >= 4 {
			pairs = append(pairs, v, "***")
		}
	}
	s.mu.Lock()
	s.cfg = t
	s.masker = nil
	if len(pairs) > 0 {
		s.masker = strings.NewReplacer(longestFirst(pairs)...)
	}
	s.mu.Unlock()
	select {
	case s.cfgWake <- struct{}{}:
	default:
	}
}

// longestFirst orders the value and replacement pairs so that a long secret wins over a short one inside it.
func longestFirst(p []string) []string {
	type pr struct{ a, b string }
	var l []pr
	for i := 0; i+1 < len(p); i += 2 {
		l = append(l, pr{p[i], p[i+1]})
	}
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && len(l[j].a) > len(l[j-1].a); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
	out := make([]string, 0, len(p))
	for _, x := range l {
		out = append(out, x.a, x.b)
	}
	return out
}

// Stats returns the counters.
func (s *Shipper) Stats() Stats {
	return Stats{Shipped: s.shipped.Load(), DroppedBatches: s.droppedBatches.Load(),
		RejectedBatches: s.rejected.Load(), RateLimited: s.rateLimited.Load()}
}

func (s *Shipper) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Shipper) telemetry() (url string, capBytes int64, masker *strings.Replacer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mib := DefaultBufferMiB
	if s.cfg != nil {
		url = s.cfg.GetLogsUrl()
		if m := int(s.cfg.GetBufferMib()); m > 0 {
			mib = m
		}
	}
	return url, int64(mib) << 20, s.masker
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// die is for tests: it stops the shipper like a killed process (nothing is written or sent after it).
func (s *Shipper) die() {
	s.dead.Store(true)
	if s.killFn != nil {
		s.killFn()
	}
}

// at runs the test hook for a named point. It returns true when the shipper was killed there.
func (s *Shipper) at(point string) bool {
	if s.hook != nil && s.hook(point) {
		s.die()
	}
	return s.dead.Load()
}

// Run follows the logs and ships them until ctx ends. It returns an error only when the spool cannot be opened.
func (s *Shipper) Run(ctx context.Context) error {
	s.initChans()
	sp, err := openSpool(s.Dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.sp = sp
	s.killFn = cancel
	s.readers = map[string]context.CancelFunc{}
	s.cur = map[string]Cursor{}
	s.lines = make(chan rawLine, 1024)
	s.mu.Unlock()
	s.notify()

	var wg sync.WaitGroup
	for _, f := range []func(context.Context){s.syncLoop, s.batchLoop, s.sendLoop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(ctx)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Shipper) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// syncLoop starts a reader for every managed container and stops the readers of containers that are gone.
func (s *Shipper) syncLoop(ctx context.Context) {
	t := time.NewTicker(orDur(s.SyncEvery, 2*time.Second))
	defer t.Stop()
	var rwg sync.WaitGroup
	defer func() {
		s.mu.Lock()
		for _, c := range s.readers {
			c()
		}
		s.mu.Unlock()
		rwg.Wait()
	}()
	for {
		s.syncOnce(ctx, &rwg)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.cfgWake:
		}
	}
}

func (s *Shipper) syncOnce(ctx context.Context, rwg *sync.WaitGroup) {
	url, _, _ := s.telemetry()
	var list []docker.Container
	if url != "" {
		var err error
		list, err = s.Source.List(ctx, map[string]string{labelManaged: "true"})
		if err != nil {
			if ctx.Err() == nil {
				s.log().Warn("logs: cannot list containers", "error", err)
			}
			return // keep the readers that run
		}
	}
	want := map[string]docker.Container{}
	for _, c := range list {
		if c.Labels[labelService] != "" {
			want[c.ID] = c
		}
	}
	s.mu.Lock()
	for id, cancel := range s.readers {
		if _, ok := want[id]; !ok {
			cancel()
			delete(s.readers, id)
		}
	}
	for id, c := range want {
		if _, ok := s.readers[id]; ok {
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		s.readers[id] = cancel
		if _, ok := s.cur[id]; !ok {
			s.cur[id] = s.sp.cursor(id)
		}
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			s.readLoop(rctx, c)
		}()
	}
	s.mu.Unlock()
	if url != "" {
		s.sp.prune(func(id string) bool { _, ok := want[id]; return ok })
		s.mu.Lock()
		for id := range s.cur {
			if _, ok := want[id]; !ok {
				if _, running := s.readers[id]; !running {
					delete(s.cur, id)
				}
			}
		}
		s.mu.Unlock()
	}
}

// readLoop follows one container. It starts again after the stream ends (the container restarted or stopped).
func (s *Shipper) readLoop(ctx context.Context, c docker.Container) {
	wait := 500 * time.Millisecond
	for ctx.Err() == nil {
		s.mu.Lock()
		cur := s.cur[c.ID]
		s.mu.Unlock()
		skip := cur.N
		var since time.Time
		if cur.TS > 0 {
			since = time.Unix(0, cur.TS)
		}
		got := false
		err := s.Source.FollowLogs(ctx, c.ID, docker.LogOptions{Since: since, Follow: true, MaxLine: readCap}, func(l docker.LogLine) error {
			ts := l.Time.UnixNano()
			switch {
			case ts == cur.TS && skip > 0:
				skip-- // shipped before the restart
				return nil
			case ts == cur.TS:
				cur.N++
			case ts > cur.TS:
				cur.TS, cur.N = ts, 1
			}
			// A line older than the cursor (the clock moved back) is sent, and the cursor stays.
			got = true
			rl := rawLine{id: c.ID, name: c.Name, service: c.Labels[labelService], release: c.Labels[labelRelease],
				replica: c.Labels[labelReplica], line: l, cur: cur}
			select {
			case s.lines <- rl:
				s.mu.Lock()
				s.cur[c.ID] = cur
				s.mu.Unlock()
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			s.log().Debug("logs: follow ended", "container", c.Name, "error", err)
		}
		// Wait longer when the stream is empty (a stopped container ends at once).
		if got {
			wait = 500 * time.Millisecond
		} else if wait < 5*time.Second {
			wait *= 2
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// entry is one log line as the server takes it.
type entry struct {
	Time      string `json:"_time"`
	Msg       string `json:"_msg"`
	Service   string `json:"service"`
	Release   string `json:"release"`
	Container string `json:"container"`
	Stream    string `json:"stream"`
	Level     string `json:"level"`
	Replica   string `json:"replica"`
}

type window struct {
	sec     int64
	count   int
	dropped int
	last    rawLine
}

// batcher builds batches from the lines of all readers.
type batcher struct {
	s       *Shipper
	buf     bytes.Buffer
	lines   int
	cursors map[string]Cursor
	first   time.Time
	win     map[string]*window // by service
}

// batchLoop collects lines and writes a batch when it is big enough or old enough.
func (s *Shipper) batchLoop(ctx context.Context) {
	b := &batcher{s: s, cursors: map[string]Cursor{}, win: map[string]*window{}}
	maxLines, maxBytes := s.FlushLines, s.FlushBytes
	if maxLines <= 0 {
		maxLines = 1000
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	every := orDur(s.FlushEvery, time.Second)
	t := time.NewTicker(every / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			b.flush()
			return
		case l := <-s.lines:
			b.add(l)
			if b.lines >= maxLines || b.buf.Len() >= maxBytes {
				b.flush()
			}
		case <-t.C:
			if b.lines > 0 && time.Since(b.first) >= every {
				b.flush()
			}
		}
	}
}

func (b *batcher) add(l rawLine) {
	s := b.s
	b.cursors[l.id] = l.cur // the position moves for every line, also for a line that is dropped
	if b.lines == 0 && b.buf.Len() == 0 {
		b.first = time.Now()
	}
	rate := s.RatePerSecond
	if rate <= 0 {
		rate = DefaultRatePerSecond
	}
	w := b.win[l.service]
	if w == nil {
		w = &window{}
		b.win[l.service] = w
	}
	// The window follows the time of the log line, so catching up after a restart is limited the same way.
	if sec := l.line.Time.Unix(); sec != w.sec {
		w.sec, w.count = sec, 0
	}
	w.count++
	if w.count > rate {
		w.dropped++
		w.last = l
		s.rateLimited.Add(1)
		return
	}
	_, _, masker := s.telemetry()
	b.put(l, l.line.Time, l.line.Stream, b.message(l, masker), "")
}

// message masks the secrets, then cuts the line.
func (b *batcher) message(l rawLine, masker *strings.Replacer) string {
	msg := l.line.Text
	if masker != nil {
		msg = masker.Replace(msg)
	}
	if len(msg) > MaxLineBytes {
		cut := MaxLineBytes
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + cutMarker
	} else if l.line.Truncated {
		msg += cutMarker
	}
	return msg
}

// put adds one line to the batch. An empty level means: guess it from the message.
func (b *batcher) put(l rawLine, at time.Time, stream, msg, level string) {
	if level == "" {
		level = guessLevel(msg)
	}
	e := entry{Time: at.UTC().Format(time.RFC3339Nano), Msg: msg, Service: l.service, Release: l.release,
		Container: l.name, Stream: stream, Level: level, Replica: l.replica}
	j, _ := json.Marshal(e)
	b.buf.Write(j)
	b.buf.WriteByte('\n')
	b.lines++
}

// flush writes the batch to the spool. The cursors move in the same step.
func (b *batcher) flush() {
	s := b.s
	if s.dead.Load() {
		return
	}
	// One extra line per service that lost lines to the rate limit.
	for _, w := range b.win {
		if w.dropped > 0 {
			b.put(w.last, w.last.line.Time, "stderr", strconv.Itoa(w.dropped)+" lines dropped", "warn")
			w.dropped = 0
		}
	}
	if b.lines == 0 && len(b.cursors) == 0 {
		return
	}
	if s.at("before-spool") {
		return
	}
	if _, err := s.sp.add(b.cursors, b.lines, b.buf.Bytes()); err != nil {
		s.log().Error("logs: cannot write the batch, will try again", "error", err)
		return // the lines stay in memory
	}
	b.buf.Reset()
	b.lines, b.cursors = 0, map[string]Cursor{}
	_, capBytes, _ := s.telemetry()
	if d, err := s.sp.trim(capBytes); err != nil {
		s.log().Error("logs: cannot trim the buffer", "error", err)
	} else if d > 0 {
		s.droppedBatches.Add(uint64(d))
		s.log().Warn("logs: the disk buffer is full, the oldest batches were dropped", "batches", d)
	}
	s.notify()
	s.at("spooled")
}
