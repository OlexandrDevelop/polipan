// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/store"
)

// JobKind is the kind of the delivery jobs in the queue.
const JobKind = "alert.notify"

// DeliveryAttempts is the number of tries of one message to one channel (REQ-ALERT-008).
const DeliveryAttempts = 3

// DeliveryBackoffs are the waits after a failed try: 10 s, 60 s, 300 s (12-observability section 5).
var DeliveryBackoffs = []time.Duration{10 * time.Second, 60 * time.Second, 300 * time.Second}

// Engine makes alert instances from events and metrics and sends the messages.
type Engine struct {
	DB       *pgxpool.Pool
	Store    *Store
	Clock    clock.Clock
	Log      *slog.Logger
	Backends *obs.Backends // nil or empty: no metric rules
	// ExternalURL is the address of the panel, for the links in messages.
	ExternalURL string
	// RepeatDefault is the repeat interval of a new rule (alert_repeat_interval).
	RepeatDefault time.Duration
	// Client gives the HTTP client for an org (it checks the target address, REQ-SEC-104). Tests give a plain client.
	Client func(ctx context.Context, orgID string) *http.Client
	// Dialer gives the dialer for SMTP. Nil means a plain dialer.
	Dialer func(ctx context.Context, orgID string) *net.Dialer
	// TelegramAPI is the base of the bot API. Empty means https://api.telegram.org.
	TelegramAPI string
	// EventInterval and MetricInterval are how often the engine reads events and evaluates rules (1 s and 30 s).
	EventInterval  time.Duration
	MetricInterval time.Duration

	monitoringDown atomic.Bool
	lastAuditCheck map[string]time.Time
	batcher        *telegramBatcher
}

// MonitoringUnavailable tells that the last metric evaluation could not reach VictoriaMetrics.
func (e *Engine) MonitoringUnavailable() bool { return e.monitoringDown.Load() }

func (e *Engine) now() time.Time {
	if e.Clock != nil {
		return e.Clock.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) repeatDefault() int {
	if e.RepeatDefault > 0 {
		return int(e.RepeatDefault.Seconds())
	}
	return 4 * 3600
}

// DefaultRepeatSeconds is the repeat interval of a new rule, in seconds.
func (e *Engine) DefaultRepeatSeconds() int { return e.repeatDefault() }

// Run reads events and sends due messages every second, and evaluates the metric and database rules every 30 s.
// On start the engine continues from the saved event cursor and loads nothing else: the firing instances are in
// the database, so no message is sent twice after a restart (REQ-OBS-104).
func (e *Engine) Run(ctx context.Context) {
	ev := e.EventInterval
	if ev <= 0 {
		ev = time.Second
	}
	mi := e.MetricInterval
	if mi <= 0 {
		mi = 30 * time.Second
	}
	e.Tick(ctx, true)
	te, tm := time.NewTicker(ev), time.NewTicker(mi)
	defer te.Stop()
	defer tm.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-te.C:
			e.Tick(ctx, false)
		case <-tm.C:
			e.Tick(ctx, true)
		}
	}
}

// Tick runs one pass: events, then (with rules) the rule evaluation, then the messages that are due.
func (e *Engine) Tick(ctx context.Context, rules bool) {
	if rules {
		if err := e.EnsureDefaults(ctx); err != nil && ctx.Err() == nil {
			e.log().Warn("alert: default rules", "err", err)
		}
	}
	if _, err := e.ProcessEvents(ctx); err != nil && ctx.Err() == nil {
		e.log().Warn("alert: events", "err", err)
	}
	if rules {
		e.EvalRules(ctx)
	}
	if err := e.SendDue(ctx); err != nil && ctx.Err() == nil {
		e.log().Warn("alert: send", "err", err)
	}
}

// ---- default rules ----

type defaultRule struct {
	name, typ, severity string
	params              map[string]any
}

var defaultRules = []defaultRule{
	{"Node down", "node_down", "critical", nil},
	{"Service unhealthy", "service_unhealthy", "critical", nil},
	{"Deployment failed", "deployment_failed", "warning", nil},
	{"Backup failed", "backup_failed", "critical", nil},
	{"Restore check failed", "restore_check_failed", "critical", nil},
	{"Certificate expiring", "cert_expiring", "warning", map[string]any{"days": 14}},
	{"Disk high", "disk_high", "warning", map[string]any{"threshold": 90, "for_seconds": 300}},
}

// EnsureDefaults makes the default rules of every org that lacks them (REQ-ALERT-005). The rules have no channel
// until the first channel is made: the UI shows a banner then.
func (e *Engine) EnsureDefaults(ctx context.Context) error {
	rows, err := e.DB.Query(ctx, `SELECT o.id FROM orgs o WHERE o.deleted_at IS NULL`)
	if err != nil {
		return err
	}
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		orgs = append(orgs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, org := range orgs {
		var has bool
		if err := e.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alert_rules WHERE org_id = $1 AND builtin)`, org).Scan(&has); err != nil {
			return err
		}
		if has {
			continue
		}
		var chans []string
		if err := e.DB.QueryRow(ctx, `SELECT COALESCE(array_agg(id ORDER BY created_at), '{}') FROM (SELECT id, created_at FROM alert_channels WHERE org_id = $1 ORDER BY created_at LIMIT 1) c`, org).Scan(&chans); err != nil {
			return err
		}
		for _, d := range defaultRules {
			params := d.params
			if params == nil {
				params = map[string]any{}
			}
			pb, _ := json.Marshal(params)
			if _, err := e.DB.Exec(ctx, `INSERT INTO alert_rules (id, org_id, name, type, scope, params, severity, channel_ids, repeat_interval_s, builtin)
				VALUES ($1,$2,$3,$4,'{}',$5,$6,$7,$8,true) ON CONFLICT (org_id, name) DO NOTHING`,
				store.NewID("alr"), org, d.name, d.typ, pb, d.severity, chans, e.repeatDefault()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- events ----

// eventRules maps the rule names of the alert events to rule types.
var eventRules = map[string]string{
	"node_suspect": "node_suspect", "node_down": "node_down", "service_unhealthy": "service_unhealthy",
	"deployment_failed": "deployment_failed", "deployment_rolled_back": "deployment_rolled_back", "deployment_live": "deployment_live",
	"backup_failed": "backup_failed", "restore_check_failed": "restore_check_failed", "restore_check_overdue": "restore_check_overdue",
	"service_failover": "failover_started", "failover_blocked": "failover_blocked",
}

// oneShot types have no end: one message, then the instance is closed.
var oneShot = map[string]bool{
	"deployment_failed": true, "deployment_rolled_back": true, "deployment_live": true, "backup_failed": true,
	"restore_check_failed": true, "restore_check_overdue": true, "failover_started": true, "failover_blocked": true,
}

// serviceLevel types are hidden while a node under the service is down (REQ-OBS-105).
var serviceLevel = map[string]bool{"service_unhealthy": true, "container_restart_loop": true, "memory_high": true, "log_match": true}

const eventCursor = "events"

type alertEvent struct {
	id      int64
	orgID   string
	object  string
	payload map[string]any
}

// ProcessEvents reads the alert events after the saved cursor, makes or closes instances, and saves the new cursor
// in the same transaction. It returns the number of events.
func (e *Engine) ProcessEvents(ctx context.Context) (int, error) {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var got bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('alert-events'))`).Scan(&got); err != nil || !got {
		return 0, err // another process reads the events
	}
	var cursor int64
	err = tx.QueryRow(ctx, `SELECT event_id FROM alert_cursor WHERE name = $1`, eventCursor).Scan(&cursor)
	if err == pgx.ErrNoRows {
		// First start: events from before the engine are old news. Start after the newest one.
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM events WHERE topic = 'alert'`).Scan(&cursor); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alert_cursor (name, event_id) VALUES ($1,$2)`, eventCursor, cursor); err != nil {
			return 0, err
		}
		return 0, tx.Commit(ctx)
	}
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id, org_id, object_id, payload FROM events WHERE topic = 'alert' AND id > $1 ORDER BY id LIMIT 200`, cursor)
	if err != nil {
		return 0, err
	}
	var evs []alertEvent
	for rows.Next() {
		var ev alertEvent
		var raw []byte
		if err := rows.Scan(&ev.id, &ev.orgID, &ev.object, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		_ = json.Unmarshal(raw, &ev.payload)
		evs = append(evs, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}
	for _, ev := range evs {
		if err := e.apply(ctx, tx, ev); err != nil {
			return 0, fmt.Errorf("event %d: %w", ev.id, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE alert_cursor SET event_id = $2 WHERE name = $1`, eventCursor, evs[len(evs)-1].id); err != nil {
		return 0, err
	}
	return len(evs), tx.Commit(ctx)
}

func detailStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// scopeOf finds the project, environment, service and node of an event.
func (e *Engine) scopeOf(ctx context.Context, q dbq, rtype, object string, d map[string]any) (map[string]string, error) {
	sc := map[string]string{}
	if n := detailStr(d, "node_id"); n != "" {
		sc["node_id"] = n
	} else if strings.HasPrefix(rtype, "node_") {
		sc["node_id"] = object
	}
	sid := detailStr(d, "service_id")
	if sid == "" && (rtype == "service_unhealthy" || rtype == "container_restart_loop") {
		sid = object
	}
	if sid != "" {
		var pid, eid string
		err := q.QueryRow(ctx, `SELECT e.project_id, e.id FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.id = $1`, sid).Scan(&pid, &eid)
		if err == nil {
			sc["service_id"], sc["project_id"], sc["environment_id"] = sid, pid, eid
		} else if err != pgx.ErrNoRows {
			return nil, err
		}
	}
	return sc, nil
}

func scopeMatches(rule, event map[string]string) bool {
	for k, v := range rule {
		if v != "" && event[k] != v {
			return false
		}
	}
	return true
}

// apply handles one event.
func (e *Engine) apply(ctx context.Context, tx pgx.Tx, ev alertEvent) error {
	name := detailStr(ev.payload, "rule")
	rtype, ok := eventRules[name]
	if !ok {
		return nil
	}
	state := detailStr(ev.payload, "state")
	if state == "" {
		state = "firing"
	}
	if strings.HasPrefix(rtype, "node_") && detailStr(ev.payload, "node_id") == "" {
		ev.payload = withKey(ev.payload, "node_id", ev.object) // the object of a node alert is the node
	}
	sc, err := e.scopeOf(ctx, tx, rtype, ev.object, ev.payload)
	if err != nil {
		return err
	}
	rules, err := e.rulesOfType(ctx, tx, ev.orgID, rtype)
	if err != nil {
		return err
	}
	fp := rtype + ":" + ev.object
	for _, r := range rules {
		if !scopeMatches(r.Scope, sc) {
			continue
		}
		if state == "resolved" {
			if err := e.resolve(ctx, tx, r.ID, fp, ev.payload); err != nil {
				return err
			}
			if rtype == "node_down" {
				if _, err := tx.Exec(ctx, `UPDATE alert_instances SET suppressed_by = NULL, updated_at = $2 WHERE suppressed_by = $1`, ev.object, e.now()); err != nil {
					return err
				}
			}
			continue
		}
		if err := e.fire(ctx, tx, r, fp, ev.payload, sc); err != nil {
			return err
		}
		if rtype == "node_down" {
			// The service alerts of the services on this node are hidden from now on (AC-ALERT-03).
			if _, err := tx.Exec(ctx, `UPDATE alert_instances i SET suppressed_by = $2, updated_at = $3
				FROM alert_rules ar WHERE ar.id = i.rule_id AND ar.org_id = $1 AND i.state = 'firing' AND ar.type = ANY($4)
				AND i.suppressed_by IS NULL AND (i.details->>'service_id') IN (SELECT service_id FROM placements WHERE node_id = $2)`,
				ev.orgID, ev.object, e.now(), serviceLevelTypes()); err != nil {
				return err
			}
		}
	}
	return nil
}

func serviceLevelTypes() []string {
	var out []string
	for t := range serviceLevel {
		out = append(out, t)
	}
	return out
}

func (e *Engine) rulesOfType(ctx context.Context, q dbq, orgID, rtype string) ([]*Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE org_id = $1 AND type = $2 AND enabled`, orgID, rtype)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// suppressedBy gives the node that hides an alert of a service: a node under the service that is down.
func (e *Engine) suppressedBy(ctx context.Context, q dbq, rtype, serviceID string) (*string, error) {
	if !serviceLevel[rtype] || serviceID == "" {
		return nil, nil
	}
	var node string
	err := q.QueryRow(ctx, `SELECT n.id FROM placements p JOIN nodes n ON n.id = p.node_id
		WHERE p.service_id = $1 AND n.state = 'down' ORDER BY n.id LIMIT 1`, serviceID).Scan(&node)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// fire makes the instance firing (a new one, or one that was resolved). A firing instance keeps its time and its
// message counters: this is the dedup (REQ-ALERT-004).
func (e *Engine) fire(ctx context.Context, tx dbq, r *Rule, fp string, details map[string]any, sc map[string]string) error {
	sup, err := e.suppressedBy(ctx, tx, r.Type, sc["service_id"])
	if err != nil {
		return err
	}
	if oneShot[r.Type] {
		details = withKey(details, "one_shot", true)
	}
	now := e.now()
	raw, _ := json.Marshal(details)
	_, err = tx.Exec(ctx, `INSERT INTO alert_instances (id, rule_id, fingerprint, state, first_fired_at, details, suppressed_by, updated_at)
		VALUES ($1,$2,$3,'firing',$4,$5,$6,$4)
		ON CONFLICT (rule_id, fingerprint) DO UPDATE SET
		  first_fired_at = CASE WHEN alert_instances.state = 'firing' THEN alert_instances.first_fired_at ELSE EXCLUDED.first_fired_at END,
		  last_notified_at = CASE WHEN alert_instances.state = 'firing' THEN alert_instances.last_notified_at ELSE NULL END,
		  notify_count = CASE WHEN alert_instances.state = 'firing' THEN alert_instances.notify_count ELSE 0 END,
		  resolved_notified = CASE WHEN alert_instances.state = 'firing' THEN alert_instances.resolved_notified ELSE false END,
		  state = 'firing', resolved_at = NULL, details = EXCLUDED.details,
		  suppressed_by = EXCLUDED.suppressed_by, updated_at = EXCLUDED.updated_at`,
		store.NewID("ali"), r.ID, fp, now, raw, sup)
	return err
}

func withKey(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

// resolve closes a firing instance. It keeps the last details and adds the ones of the resolving event.
func (e *Engine) resolve(ctx context.Context, tx dbq, ruleID, fp string, details map[string]any) error {
	now := e.now()
	_, err := tx.Exec(ctx, `UPDATE alert_instances SET state = 'resolved', resolved_at = $3, updated_at = $3,
		details = COALESCE(details, '{}'::jsonb) || $4::jsonb
		WHERE rule_id = $1 AND fingerprint = $2 AND state = 'firing'`, ruleID, fp, now, mustJSON(resolveDetails(details)))
	return err
}

// resolveDetails keeps only the extra facts of a resolving event, not its state or rule fields.
func resolveDetails(d map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range d {
		switch k {
		case "state", "rule", "severity", "one_shot":
		default:
			out[k] = v
		}
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
