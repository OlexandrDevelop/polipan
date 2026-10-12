// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/queue"
)

// Message kinds.
const (
	KindFiring   = "firing"
	KindRepeat   = "repeat"
	KindResolved = "resolved"
)

type notifyJob struct {
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
	ChannelID  string `json:"channel_id"`
	N          int    `json:"n"`
}

// SendDue finds the instances that need a message (a new one, a reminder, a resolve) and puts one job per channel
// in the queue. The counters and the jobs are saved in one transaction, so a restart cannot send a message twice and
// cannot lose one.
func (e *Engine) SendDue(ctx context.Context) error {
	now := e.now()
	rows, err := e.DB.Query(ctx, `SELECT i.id FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id
		WHERE (i.state = 'firing' AND i.suppressed_by IS NULL
		       AND (i.notify_count = 0 OR i.last_notified_at + make_interval(secs => r.repeat_interval_s) <= $1))
		   OR (i.state = 'resolved' AND NOT i.resolved_notified AND i.notify_count > 0)
		ORDER BY i.updated_at LIMIT 200`, now)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := e.notifyOne(ctx, id, now); err != nil {
			e.log().Warn("alert: notify", "instance", id, "err", err)
		}
	}
	return nil
}

func (e *Engine) notifyOne(ctx context.Context, id string, now time.Time) error {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state, ruleID string
	var count, repeat int
	var last, mutedUntil *time.Time
	var sup *string
	var resolvedNotified, enabled, absorbed bool
	var chans []string
	var details []byte
	err = tx.QueryRow(ctx, `SELECT i.state, i.rule_id, i.notify_count, i.last_notified_at, i.suppressed_by, i.resolved_notified, COALESCE(i.details,'{}'::jsonb),
		i.absorbed, r.repeat_interval_s, r.muted_until, r.enabled, r.channel_ids
		FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.id = $1 FOR UPDATE OF i SKIP LOCKED`, id).
		Scan(&state, &ruleID, &count, &last, &sup, &resolvedNotified, &details, &absorbed, &repeat, &mutedUntil, &enabled, &chans)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	kind := ""
	switch {
	case state == "firing" && sup == nil && count == 0:
		kind = KindFiring
	case state == "firing" && sup == nil && last != nil && !last.Add(time.Duration(repeat)*time.Second).After(now):
		kind = KindRepeat
	case state == "resolved" && !resolvedNotified && count > 0:
		kind = KindResolved
	}
	if kind == "" {
		return nil
	}
	// One incident, one message: the root alert tells about the related ones (12-observability section 5.1).
	inc, err := incidentOf(ctx, tx, id)
	if err != nil {
		return err
	}
	if inc != nil && kind == KindFiring && inc.Open && now.Before(inc.OpenedAt.Add(e.incidentHold())) {
		return nil // wait a little: a cause that fires later still leads the message
	}
	silent := false
	switch {
	case kind == KindFiring && inc != nil && inc.RootID != id && inc.Members > 1:
		// Only when the message of the root reaches every channel of this alert. Else this alert sends its own.
		if silent, err = e.rootCovers(ctx, tx, inc.RootID, chans, now); err != nil {
			return err
		}
		if silent {
			if _, err := tx.Exec(ctx, `UPDATE alert_instances SET absorbed = true WHERE id = $1`, id); err != nil {
				return err
			}
		}
	case kind == KindRepeat && absorbed && inc != nil && inc.RootID != id:
		// The reminder of the root alert covers it, while the root can still send one.
		if silent, err = e.rootCovers(ctx, tx, inc.RootID, chans, now); err != nil {
			return err
		}
	case kind == KindResolved && absorbed:
		silent = true
	}
	muted := mutedUntil != nil && mutedUntil.After(now)
	if kind == KindResolved && (muted || !enabled) {
		// The message of this rule was held back: do not tell about the end of it later.
		_, err := tx.Exec(ctx, `UPDATE alert_instances SET resolved_notified = true WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if muted || !enabled { // the instance stays firing and records it, but no message goes out (REQ-ALERT-006)
		return nil
	}
	var live []string
	if !silent {
		if live, err = e.liveChannels(ctx, tx, chans); err != nil {
			return err
		}
	}
	if silent && kind == KindResolved {
		_, err = tx.Exec(ctx, `UPDATE alert_instances SET resolved_notified = true WHERE id = $1`, id)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if len(live) == 0 && !silent {
		if kind == KindResolved {
			_, err = tx.Exec(ctx, `UPDATE alert_instances SET resolved_notified = true WHERE id = $1`, id)
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return nil // no channel yet: it goes out when the first channel is set on the rule
	}
	n := count
	if kind != KindResolved {
		n = count + 1
	}
	for _, ch := range live {
		payload, _ := json.Marshal(notifyJob{InstanceID: id, Kind: kind, ChannelID: ch, N: n})
		if _, err := tx.Exec(ctx, `INSERT INTO jobs (kind, payload, run_at, unique_key, max_attempts) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (unique_key) WHERE state IN ('queued','running') DO NOTHING`,
			JobKind, payload, now, fmt.Sprintf("alert:%s:%s:%d:%s", id, kind, n, ch), DeliveryAttempts); err != nil {
			return err
		}
	}
	switch kind {
	case KindResolved:
		_, err = tx.Exec(ctx, `UPDATE alert_instances SET resolved_notified = true WHERE id = $1`, id)
	default:
		var d map[string]any
		_ = json.Unmarshal(details, &d)
		if one, _ := d["one_shot"].(bool); one {
			_, err = tx.Exec(ctx, `UPDATE alert_instances SET last_notified_at = $2, notify_count = $3, state = 'resolved', resolved_at = $2,
				resolved_notified = true, updated_at = $2 WHERE id = $1`, id, now, n)
		} else {
			_, err = tx.Exec(ctx, `UPDATE alert_instances SET last_notified_at = $2, notify_count = $3 WHERE id = $1`, id, now, n)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// liveChannels keeps the channels that exist and are enabled.
func (e *Engine) liveChannels(ctx context.Context, q dbq, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM alert_channels WHERE id = ANY($1) AND enabled ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Handler is the queue handler of the delivery jobs. A failed try returns an error, so the queue tries again with
// the back-off of DeliveryBackoffs. After the last try the channel is marked failing (REQ-ALERT-008).
func (e *Engine) Handler() queue.Handler {
	return func(ctx context.Context, job queue.Job) error {
		var p notifyJob
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return nil // a job nobody can read is dropped
		}
		var orgID string
		err := e.DB.QueryRow(ctx, `SELECT r.org_id FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.id = $1`, p.InstanceID).Scan(&orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the rule was deleted
		}
		if err != nil {
			return err
		}
		ch, err := e.Store.GetChannel(ctx, orgID, p.ChannelID)
		if errors.Is(err, ErrNotFound) || (err == nil && !ch.Enabled) {
			return nil
		}
		if err != nil {
			return err
		}
		msg, err := e.buildMessage(ctx, p.InstanceID, p.Kind)
		if err != nil {
			return err
		}
		sendErr := e.deliver(ctx, ch, msg)
		if sendErr == nil {
			_, _ = e.DB.Exec(ctx, `UPDATE alert_channels SET state = 'ok', last_error = NULL, last_ok_at = $2 WHERE id = $1`, ch.ID, e.now())
			return nil
		}
		e.log().Warn("alert: delivery failed", "channel", ch.ID, "kind", ch.Kind, "attempt", job.Attempts, "err", sendErr)
		if job.Attempts >= job.MaxAttempts {
			_, _ = e.DB.Exec(ctx, `UPDATE alert_channels SET state = 'failing', last_error = $2 WHERE id = $1`, ch.ID, truncate(sendErr.Error(), 500))
		}
		return sendErr
	}
}

// SendTest sends a test message to a channel and returns the error of the channel, if any (REQ-ALERT-001).
func (e *Engine) SendTest(ctx context.Context, orgID, channelID string) error {
	ch, err := e.Store.GetChannel(ctx, orgID, channelID)
	if err != nil {
		return err
	}
	msg := &Message{Kind: KindFiring, Test: true, Severity: "info", RuleName: "Test message", RuleType: "test",
		What: "This is a test message from Polipan.", Where: map[string]string{}, Since: e.now(), Link: strings.TrimRight(e.ExternalURL, "/")}
	sendErr := e.deliver(ctx, ch, msg)
	if sendErr == nil {
		_, _ = e.DB.Exec(ctx, `UPDATE alert_channels SET state = 'ok', last_error = NULL, last_ok_at = $2 WHERE id = $1`, ch.ID, e.now())
		return nil
	}
	_, _ = e.DB.Exec(ctx, `UPDATE alert_channels SET state = 'failing', last_error = $2 WHERE id = $1`, ch.ID, truncate(sendErr.Error(), 500))
	return sendErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// rootCovers tells if the root alert of an incident can carry the message of another alert: the root is firing and
// not hidden by a down node, its rule is enabled and not muted, and its live channels include every live channel of
// the other alert. A root that resolved during the hold, or that sends nowhere, covers nothing.
func (e *Engine) rootCovers(ctx context.Context, q dbq, rootID string, memberChans []string, now time.Time) (bool, error) {
	var state string
	var sup *string
	var enabled bool
	var mutedUntil *time.Time
	var rootChans []string
	err := q.QueryRow(ctx, `SELECT i.state, i.suppressed_by, r.enabled, r.muted_until, r.channel_ids
		FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.id = $1`, rootID).
		Scan(&state, &sup, &enabled, &mutedUntil, &rootChans)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != "firing" || sup != nil || !enabled || (mutedUntil != nil && mutedUntil.After(now)) {
		return false, nil
	}
	rootLive, err := e.liveChannels(ctx, q, rootChans)
	if err != nil || len(rootLive) == 0 {
		return false, err
	}
	memberLive, err := e.liveChannels(ctx, q, memberChans)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, c := range rootLive {
		have[c] = true
	}
	for _, c := range memberLive {
		if !have[c] {
			return false, nil
		}
	}
	return true, nil
}
