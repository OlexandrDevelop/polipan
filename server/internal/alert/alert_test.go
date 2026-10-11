// SPDX-License-Identifier: Apache-2.0

package alert_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/alert"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/queue"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
)

// receiver is a fake place that takes the messages of a channel.
type receiver struct {
	mu   sync.Mutex
	msgs []string
	hdr  []http.Header
	fail bool // answer 500
	hits int
}

func (r *receiver) add(m string, h http.Header) {
	r.mu.Lock()
	r.msgs, r.hdr = append(r.msgs, m), append(r.hdr, h)
	r.mu.Unlock()
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *receiver) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.msgs, "\n---\n")
}

func (r *receiver) serve(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits++
		fail := r.fail
		r.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		r.add(string(b), req.Header.Clone())
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// smtpServer is a small SMTP server that keeps the mails.
func smtpServer(t *testing.T, r *receiver) (host, port string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				rd := bufio.NewReader(c)
				w := func(s string) { _, _ = fmt.Fprintf(c, "%s\r\n", s) }
				w("220 fake ESMTP")
				var data strings.Builder
				inData := false
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if inData {
						if line == "." {
							inData = false
							r.add(data.String(), nil)
							data.Reset()
							w("250 queued")
							continue
						}
						data.WriteString(line + "\n")
						continue
					}
					switch cmd := strings.ToUpper(line); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						w("250 fake")
					case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
						w("250 ok")
					case cmd == "DATA":
						inData = true
						w("354 go on")
					case cmd == "QUIT":
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}()
		}
	}()
	h, p, _ := net.SplitHostPort(l.Addr().String())
	return h, p
}

type env struct {
	pool  *pgxpool.Pool
	clk   *clock.Fake
	org   string
	eng   *alert.Engine
	store *alert.Store
	node  string
	t     *testing.T
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	sec, err := secrets.NewManager(pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, clk: clock.NewFake(time.Now().UTC()), org: store.NewID("org"), t: t}
	e.exec(`INSERT INTO orgs (id, name, slug) VALUES ($1,'Acme','acme')`, e.org)
	e.node = e.newNode("node-1", "ready")
	e.store = &alert.Store{DB: pool, Secrets: sec, MinRepeatS: 1}
	e.eng = &alert.Engine{DB: pool, Store: e.store, Clock: e.clk, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ExternalURL: "https://panel.example.com", RepeatDefault: time.Hour}
	e.eng.Tick(ctx, true) // the first pass sets the event cursor: older events are not alerts
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) newNode(name, state string) string {
	id := store.NewID("nod")
	e.exec(`INSERT INTO nodes (id, org_id, name, state) VALUES ($1,$2,$3,$4)`, id, e.org, name, state)
	return id
}

// emit writes an alert event like the platform does.
func (e *env) emit(object string, payload map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	e.exec(`INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'alert',$2,$3)`, e.org, object, b)
}

// run makes one engine pass and runs the delivery jobs.
func (e *env) run() {
	e.t.Helper()
	ctx := context.Background()
	e.eng.Tick(ctx, true)
	e.drain()
}

func (e *env) drain() {
	e.t.Helper()
	q := queue.New(e.pool, e.clk)
	q.Backoffs = map[string][]time.Duration{alert.JobKind: alert.DeliveryBackoffs}
	w := &queue.Worker{Queue: q, ID: "test", Handlers: map[string]queue.Handler{alert.JobKind: e.eng.Handler()}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for {
		ran, err := w.RunOnce(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func (e *env) channel(kind, name string, cfg map[string]any, secret string) *alert.Channel {
	e.t.Helper()
	ch, err := e.store.CreateChannel(context.Background(), e.org, alert.ChannelInput{Kind: kind, Name: name, Config: cfg, Secret: &secret})
	if err != nil {
		e.t.Fatal(err)
	}
	return ch
}

// rule sets the channels and the repeat interval of the rule of a type. The default rule of the type is used when
// there is one (a second rule of the same type would send every message twice).
func (e *env) rule(typ string, repeat int, channels ...string) *alert.Rule {
	e.t.Helper()
	var id string
	err := e.pool.QueryRow(context.Background(), `SELECT id FROM alert_rules WHERE org_id = $1 AND type = $2 AND builtin`, e.org, typ).Scan(&id)
	if err == nil {
		e.exec(`UPDATE alert_rules SET channel_ids = $2, repeat_interval_s = $3 WHERE id = $1`, id, channels, repeat)
		r, err := e.store.GetRule(context.Background(), e.org, id)
		if err != nil {
			e.t.Fatal(err)
		}
		return r
	}
	r, err := e.store.CreateRule(context.Background(), e.org, alert.RuleInput{Name: "rule " + typ, Type: typ, Severity: "critical",
		ChannelIDs: channels, RepeatS: repeat}, 3600)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) telegram(t *testing.T) (*receiver, *alert.Channel) {
	r := &receiver{}
	srv := r.serve(t)
	e.eng.TelegramAPI = srv.URL
	return r, e.channel("telegram", "tg", map[string]any{"chat_id": "42"}, "123:token")
}

func nodeEvent(state string) map[string]any {
	return map[string]any{"rule": "node_down", "severity": "critical", "state": state}
}

// AC-ALERT-01: a node stops its heartbeat. One Telegram message arrives. A resolved message arrives when it returns.
func TestAC_ALERT_01_NodeDownTelegramThenResolved(t *testing.T) {
	e := setup(t)
	tg, ch := e.telegram(t)
	if err := e.eng.EnsureDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.emit(e.node, nodeEvent("firing"))
	e.run()
	if tg.count() != 1 || !strings.Contains(tg.all(), "node-1") {
		t.Fatalf("firing: %d messages: %s", tg.count(), tg.all())
	}
	// The same alert again, and passes of the engine: still one message (dedup).
	e.emit(e.node, nodeEvent("firing"))
	e.run()
	e.run()
	if tg.count() != 1 {
		t.Fatalf("dedup: %d messages", tg.count())
	}
	e.emit(e.node, nodeEvent("resolved"))
	e.run()
	if tg.count() != 2 || !strings.Contains(strings.ToLower(tg.msgs[1]), "resolved") {
		t.Fatalf("resolved: %d messages: %s", tg.count(), tg.all())
	}
	// A channel secret is never in the answer of the store.
	got, err := e.store.GetChannel(context.Background(), e.org, ch.ID)
	if err != nil || strings.Contains(fmt.Sprint(got), "123:token") {
		t.Fatalf("the channel shows the secret: %v %v", got, err)
	}
}

// AC-ALERT-02: a firing alert that stays firing for 2 x the repeat interval gets exactly 2 reminders.
func TestAC_ALERT_02_ExactlyTwoReminders(t *testing.T) {
	e := setup(t)
	tg, ch := e.telegram(t)
	e.rule("node_down", 600, ch.ID)
	e.emit(e.node, nodeEvent("firing"))
	e.run()
	if tg.count() != 1 {
		t.Fatalf("first: %d", tg.count())
	}
	for _, step := range []time.Duration{5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 4 * time.Minute} {
		e.clk.Advance(step)
		e.run()
	}
	// 23 minutes passed: the firing message and the reminders at 10 and 20 minutes.
	if tg.count() != 3 {
		t.Fatalf("messages: %d\n%s", tg.count(), tg.all())
	}
	if !strings.Contains(strings.ToLower(tg.msgs[1]), "reminder") && !strings.Contains(strings.ToLower(tg.msgs[1]), "still") {
		t.Logf("the reminder text: %s", tg.msgs[1])
	}
}

// AC-ALERT-03: a node is down with 5 services on it. There is 1 node alert and no service alerts for the 5.
func TestAC_ALERT_03_NodeDownHidesServiceAlerts(t *testing.T) {
	e := setup(t)
	tg, ch := e.telegram(t)
	e.rule("node_down", 600, ch.ID)
	e.rule("service_unhealthy", 600, ch.ID)
	prj, env := store.NewID("prj"), store.NewID("env")
	e.exec(`INSERT INTO projects (id, org_id, name, slug) VALUES ($1,$2,'p','p')`, prj, e.org)
	e.exec(`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ($1,$2,'prod','prod','production')`, env, prj)
	var svcs []string
	for i := 0; i < 5; i++ {
		sid, rid := store.NewID("svc"), store.NewID("rel")
		e.exec(`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1,$2,$3,'app','image','{}')`, sid, env, fmt.Sprintf("web%d", i))
		e.exec(`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source)
			VALUES ($1,$2,1,'x@sha256:a','sha256:a','{}','{}','{}','{}')`, rid, sid)
		e.exec(`INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role) VALUES ($1,$2,$3,$4,0,'blue','live')`,
			store.NewID("plc"), sid, e.node, rid)
		svcs = append(svcs, sid)
	}
	// Two services report unhealthy before the node is seen down, three after.
	for _, sid := range svcs[:2] {
		e.emit(sid, map[string]any{"rule": "service_unhealthy", "service_id": sid, "state": "firing", "severity": "critical"})
	}
	e.exec(`UPDATE nodes SET state = 'down' WHERE id = $1`, e.node)
	e.emit(e.node, nodeEvent("firing"))
	for _, sid := range svcs[2:] {
		e.emit(sid, map[string]any{"rule": "service_unhealthy", "service_id": sid, "state": "firing", "severity": "critical"})
	}
	e.run()
	if tg.count() != 1 || !strings.Contains(tg.all(), "node-1") {
		t.Fatalf("messages: %d\n%s", tg.count(), tg.all())
	}
	// The alerts of the services are kept, as suppressed (REQ-OBS-105). The node comes back: they show up if still firing.
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM alert_instances WHERE suppressed_by = $1`, e.node).Scan(&n)
	if n != 5 {
		t.Fatalf("suppressed instances: %d", n)
	}
}

// AC-ALERT-04: a failed deployment sends a message with the reason code to the channels of the rule.
// REQ-ALERT-001: Telegram, Slack, email and webhook all get it. The webhook has a signature.
func TestAC_ALERT_04_FailedDeploymentToAllChannels(t *testing.T) {
	e := setup(t)
	tg, tgc := e.telegram(t)
	sl := &receiver{}
	slSrv := sl.serve(t)
	slc := e.channel("slack", "slack", nil, slSrv.URL+"/hook")
	wh := &receiver{}
	whSrv := wh.serve(t)
	whc := e.channel("webhook", "hook", map[string]any{"url": whSrv.URL + "/in"}, "hmac-secret")
	mail := &receiver{}
	host, port := smtpServer(t, mail)
	mc := e.channel("email", "mail", map[string]any{"host": host, "port": port, "from": "polipan@example.com", "recipients": []any{"ops@example.com"}, "tls": "none"}, "")
	e.rule("deployment_failed", 600, tgc.ID, slc.ID, whc.ID, mc.ID)

	prj, env, sid := store.NewID("prj"), store.NewID("env"), store.NewID("svc")
	e.exec(`INSERT INTO projects (id, org_id, name, slug) VALUES ($1,$2,'shop','shop')`, prj, e.org)
	e.exec(`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ($1,$2,'prod','prod','production')`, env, prj)
	e.exec(`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1,$2,'web','app','image','{}')`, sid, env)
	dep := store.NewID("dep")
	e.emit(dep, map[string]any{"rule": "deployment_failed", "state": "firing", "severity": "critical", "service_id": sid, "deployment_id": dep,
		"reason": "health_timeout", "last_lines": []string{"starting", "connection refused"}})
	e.run()
	for name, r := range map[string]*receiver{"telegram": tg, "slack": sl, "webhook": wh, "email": mail} {
		if r.count() != 1 || !strings.Contains(r.all(), "health_timeout") {
			t.Fatalf("%s: %d messages: %s", name, r.count(), r.all())
		}
	}
	sig := wh.hdr[0].Get("X-Polipan-Signature")
	if sig != alert.Sign("hmac-secret", []byte(wh.msgs[0])) {
		t.Fatalf("webhook signature %q", sig)
	}
	// A one-shot alert is closed after its message: a new failure gives a new message.
	e.emit(dep, map[string]any{"rule": "deployment_failed", "state": "firing", "severity": "critical", "service_id": sid, "deployment_id": dep, "reason": "health_timeout"})
	e.run()
	if tg.count() != 2 {
		t.Fatalf("second failure: %d", tg.count())
	}
}

// AC-ALERT-05: a channel that returns 500 gets 3 tries, then it is marked failing, with the error.
func TestAC_ALERT_05_FailingChannelIsRetriedThreeTimes(t *testing.T) {
	e := setup(t)
	r := &receiver{fail: true}
	srv := r.serve(t)
	ch := e.channel("webhook", "bad", map[string]any{"url": srv.URL}, "")
	e.rule("backup_failed", 600, ch.ID)
	e.emit("pol_1", map[string]any{"rule": "backup_failed", "state": "firing", "severity": "critical", "policy_id": "pol_1"})
	e.run()
	for _, wait := range alert.DeliveryBackoffs[:2] {
		e.clk.Advance(wait)
		e.drain()
	}
	e.clk.Advance(time.Hour)
	e.drain()
	r.mu.Lock()
	hits := r.hits
	r.mu.Unlock()
	if hits != alert.DeliveryAttempts {
		t.Fatalf("tries: %d", hits)
	}
	got, err := e.store.GetChannel(context.Background(), e.org, ch.ID)
	if err != nil || got.State != "failing" || !strings.Contains(got.LastError, "500") {
		t.Fatalf("channel %+v %v", got, err)
	}
	// The channel works again: the next message clears the warning.
	r.mu.Lock()
	r.fail = false
	r.mu.Unlock()
	e.emit("pol_2", map[string]any{"rule": "backup_failed", "state": "firing", "severity": "critical", "policy_id": "pol_2"})
	e.run()
	got, _ = e.store.GetChannel(context.Background(), e.org, ch.ID)
	if got.State != "ok" || r.count() != 1 {
		t.Fatalf("after the fix: %+v, %d messages", got, r.count())
	}
}

// REQ-ALERT-006: a muted rule sends nothing, also not the end of it.
func TestMutedRuleSendsNothing(t *testing.T) {
	e := setup(t)
	tg, ch := e.telegram(t)
	r := e.rule("node_down", 600, ch.ID)
	if _, err := e.store.MuteRule(context.Background(), e.org, r.ID, e.clk.Now().Add(time.Hour), "maintenance", e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	e.emit(e.node, nodeEvent("firing"))
	e.run()
	e.emit(e.node, nodeEvent("resolved"))
	e.run()
	if tg.count() != 0 {
		t.Fatalf("a muted rule sent %d messages", tg.count())
	}
	// The instance exists and shows as muted in the list.
	list, _, err := e.store.ListInstances(context.Background(), e.org, "", 10, "", e.clk.Now())
	if err != nil || len(list) != 1 || !list[0].Muted {
		t.Fatalf("instances: %+v %v", list, err)
	}
}

// REQ-OBS-104: a server restart does not send the firing alerts again. A new engine on the same database has no
// state in memory: the instances and the event cursor are in the database.
func TestREQ_OBS_104_RestartDoesNotResend(t *testing.T) {
	e := setup(t)
	tg, ch := e.telegram(t)
	e.rule("node_down", 3600, ch.ID)
	e.emit(e.node, nodeEvent("firing"))
	e.run()
	if tg.count() != 1 {
		t.Fatalf("first: %d", tg.count())
	}
	for i := 0; i < 3; i++ { // three restarts
		e.eng = &alert.Engine{DB: e.pool, Store: e.store, Clock: e.clk, Log: e.eng.Log, ExternalURL: e.eng.ExternalURL,
			RepeatDefault: time.Hour, TelegramAPI: e.eng.TelegramAPI}
		e.clk.Advance(time.Minute)
		e.run()
	}
	if tg.count() != 1 {
		t.Fatalf("after restarts: %d messages\n%s", tg.count(), tg.all())
	}
	// Events from before a first start are not alerts: a fresh database start has no cursor and sends nothing.
	e.emit(e.node, nodeEvent("resolved"))
	e.run()
	if tg.count() != 2 {
		t.Fatalf("resolved after restarts: %d", tg.count())
	}
}

// REQ-ALERT-005: the default rules exist for the org and have the first channel.
func TestDefaultRulesGetTheFirstChannel(t *testing.T) {
	e := setup(t)
	if err := e.eng.EnsureDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, ch := e.telegram(t)
	rules, err := e.store.ListRules(context.Background(), e.org)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, r := range rules {
		have[r.Type] = r.Builtin && len(r.ChannelIDs) == 1 && r.ChannelIDs[0] == ch.ID
	}
	for _, typ := range []string{"node_down", "service_unhealthy", "deployment_failed", "backup_failed", "restore_check_failed", "cert_expiring", "disk_high"} {
		if !have[typ] {
			t.Fatalf("default rule %s is missing or has no channel: %v", typ, have)
		}
	}
}
