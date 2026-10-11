// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// GateSource gives the ingress numbers of a canary step from the metrics store. *obs.Backends implements it.
type GateSource interface {
	CanaryStats(ctx context.Context, serviceID, greenRelease, blueRelease string, window time.Duration) (obs.CanaryStats, error)
}

// minGateRequests is the number of green requests a gate needs to judge the error rate and the latency (08 section 6).
const minGateRequests = 20

// gateResult is the answer of one gate. It goes into the canary_gate event and into the deployment.
type gateResult struct {
	Step                int     `json:"step"`
	Passed              bool    `json:"passed"`
	Reason              string  `json:"reason,omitempty"`
	HealthOK            bool    `json:"health_ok"`
	GreenRequests       float64 `json:"green_requests"`
	Green5xxRate        float64 `json:"green_5xx_rate"`
	GreenP95Seconds     float64 `json:"green_p95_seconds"`
	BlueP95Seconds      float64 `json:"blue_p95_seconds"`
	InsufficientTraffic bool    `json:"insufficient_traffic,omitempty"`
	MetricsUnavailable  bool    `json:"metrics_unavailable,omitempty"`
	Extended            bool    `json:"extended,omitempty"`
	Promoted            bool    `json:"promoted,omitempty"`
}

// ---- manual control ----

// CanaryAction is promote, abort, pause or resume of a running canary (REQ-DEPLOY-006).
func (c *Control) CanaryAction(ctx context.Context, depID, action, actorID string) error {
	var state string
	var paused bool
	var pausedAt, startedAt *time.Time
	err := c.DB.QueryRow(ctx, `SELECT state, canary_paused, canary_paused_at, canary_step_started_at FROM deployments WHERE id = $1`, depID).Scan(&state, &paused, &pausedAt, &startedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != "shifting" {
		return orchestrator.ErrNotCanary
	}
	now := c.Clock.Now()
	switch action {
	case "pause":
		if paused {
			return nil
		}
		return c.canaryNote(ctx, depID, "the canary was paused by the user", `UPDATE deployments SET canary_paused = true, canary_paused_at = $2 WHERE id = $1`, now)
	case "resume":
		if !paused {
			return nil
		}
		// The time of the pause does not count for the step: the start moves forward by it.
		shifted := startedAt
		if startedAt != nil && pausedAt != nil {
			t := startedAt.Add(now.Sub(*pausedAt))
			shifted = &t
		}
		return c.canaryNote(ctx, depID, "the canary was resumed by the user",
			`UPDATE deployments SET canary_paused = false, canary_paused_at = NULL, canary_step_started_at = COALESCE($2, canary_step_started_at) WHERE id = $1`, shifted)
	case "promote", "abort":
		c.mu.Lock()
		ad := c.active[depID]
		c.mu.Unlock()
		if ad == nil || ad.cmds == nil {
			return orchestrator.ErrNotCanary
		}
		select {
		case ad.cmds <- action:
		default:
		}
		return nil
	}
	return fmt.Errorf("unknown canary action %q", action)
}

// canaryNote runs an update and writes a step of the deployment in one transaction.
func (c *Control) canaryNote(ctx context.Context, depID, msg, sql string, args ...any) error {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, sql, append([]any{depID}, args...)...); err != nil {
		return err
	}
	if err := c.addStep(ctx, tx, depID, "shifting", msg, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- the run ----

// canary shifts the traffic step by step (08 section 6). from is the index of the first step. A resumed run goes on in
// the step it was in, with the saved start time.
func (r *run) canary(ctx context.Context, from int, resumed bool) *deployErr {
	c, d := r.c, r.d
	c.lockCancel(r.ad) // traffic moves: the user aborts instead of cancelling
	steps := d.opts.CanarySteps
	if len(steps) == 0 {
		steps = []int{100}
	}
	for i := from; i < len(steps) && steps[i] < 100; i++ {
		p := steps[i]
		if !resumed || i != from {
			if e := r.shiftTo(ctx, p); e != nil {
				return e
			}
		}
		if e := r.waitStep(ctx, p); e != nil {
			return e
		}
	}
	// 100 percent: the normal switch. Blue gets no traffic, then it drains.
	if ctx.Err() != nil {
		return r.stopped(ctx)
	}
	if e := r.step(ctx, "switching", "the new version gets all the traffic", map[string]any{"canary_step": 100}); e != nil {
		return e
	}
	if err := r.commitSwitch(ctx); err != nil {
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		return r.revertCanary(ctx, ReasonSwitchFailed, "switch: "+err.Error())
	}
	r.committed = true
	return r.afterCommit(ctx, false)
}

// shiftTo sends p percent of the traffic to the new containers and waits for the ack of every ingress node.
func (r *run) shiftTo(ctx context.Context, p int) *deployErr {
	c, d := r.c, r.d
	if ctx.Err() != nil {
		return r.stopped(ctx)
	}
	if _, err := c.DB.Exec(ctx, `UPDATE deployments SET canary_step = $2, canary_step_started_at = $3, canary_extended = false, canary_gate = NULL,
		canary_routing = true, canary_paused = false, canary_paused_at = NULL, updated_at = now() WHERE id = $1`, d.id, p, c.Clock.Now()); err != nil {
		return fail(ReasonInternal, "database: %v", err)
	}
	if e := r.step(ctx, "shifting", fmt.Sprintf("%d%% of the traffic goes to the new version", p), map[string]any{"canary_step": p}); e != nil {
		return e
	}
	if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) { return c.allNodeIDs(ctx, tx) }); err != nil {
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		return r.revertCanary(ctx, ReasonSwitchFailed, "send the weights: "+err.Error())
	}
	if err := r.switchAcks(ctx, fmt.Sprintf("canary-%d", p)); err != nil {
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		return r.revertCanary(ctx, ReasonSwitchFailed, err.Error())
	}
	return nil
}

// waitStep waits for the interval of the step, then runs the gate. promote skips the wait, abort rolls back.
func (r *run) waitStep(ctx context.Context, p int) *deployErr {
	c, d := r.c, r.d
	interval := time.Duration(d.opts.CanaryStepIntervalSeconds) * time.Second
	downSince := map[string]time.Time{}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return r.stopped(ctx)
		case cmd := <-r.ad.cmds:
			switch cmd {
			case "abort":
				return r.revertCanary(ctx, ReasonCancelled, "the canary was aborted by the user")
			case "promote":
				gr := gateResult{Step: p, Passed: true, Promoted: true, HealthOK: true}
				c.canaryGateEvent(ctx, d.id, gr)
				_ = c.setStateNote(ctx, d.id, fmt.Sprintf("step %d%% was promoted by the user", p))
				return nil
			}
		case <-tick.C:
		}
		var started *time.Time
		var paused, extended bool
		var pausedAt *time.Time
		if err := c.DB.QueryRow(ctx, `SELECT canary_step_started_at, canary_paused, canary_paused_at, canary_extended FROM deployments WHERE id = $1`, d.id).
			Scan(&started, &paused, &pausedAt, &extended); err != nil {
			if ctx.Err() != nil {
				return r.stopped(ctx)
			}
			return fail(ReasonInternal, "database: %v", err)
		}
		now := c.Clock.Now()
		if paused {
			if pausedAt != nil && now.Sub(*pausedAt) >= c.CanaryPauseMax {
				return r.revertCanary(ctx, ReasonCancelled, fmt.Sprintf("the canary was paused for more than %s", c.CanaryPauseMax))
			}
			continue
		}
		// A green node that is gone for node_down_after ends the canary (08 section 11).
		for _, g := range r.green {
			if c.Sender != nil && c.Sender.Connected(g.node) {
				delete(downSince, g.node)
				continue
			}
			if _, ok := downSince[g.node]; !ok {
				downSince[g.node] = now
			}
			if now.Sub(downSince[g.node]) >= c.Config.NodeDownAfter {
				return r.revertCanary(ctx, ReasonNodeUnavailable, fmt.Sprintf("node %s was not connected for %s", g.node, c.Config.NodeDownAfter))
			}
		}
		if started == nil {
			continue
		}
		total := interval
		if extended {
			total = 2 * interval
		}
		elapsed := now.Sub(*started)
		if elapsed < total {
			continue
		}
		gr := r.gate(ctx, p, elapsed)
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		gr.Extended = extended
		if gr.InsufficientTraffic && !extended && gr.Passed {
			// Too few requests: the gate passes on health only, and the step is extended once by the same time.
			if _, err := c.DB.Exec(ctx, `UPDATE deployments SET canary_extended = true WHERE id = $1`, d.id); err != nil {
				return fail(ReasonInternal, "database: %v", err)
			}
			gr.Extended = true
			c.canaryGateEvent(ctx, d.id, gr)
			continue
		}
		c.canaryGateEvent(ctx, d.id, gr)
		if !gr.Passed {
			return r.revertCanary(ctx, gr.Reason, gateDetail(gr, d.opts))
		}
		return nil
	}
}

func gateDetail(g gateResult, o deployOptions) string {
	switch g.Reason {
	case ReasonGate5xx:
		return fmt.Sprintf("step %d%%: %.1f%% of %.0f requests of the new version failed (limit %.1f%%)", g.Step, g.Green5xxRate*100, g.GreenRequests, o.CanaryMax5xxRate*100)
	case ReasonGateLatency:
		return fmt.Sprintf("step %d%%: p95 latency of the new version is %.3f s, the old one has %.3f s (limit x%.2f)", g.Step, g.GreenP95Seconds, g.BlueP95Seconds, o.CanaryMaxP95Ratio)
	}
	return fmt.Sprintf("step %d%%: the new containers are not healthy", g.Step)
}

// gate judges a step (08 section 6, step 2.3).
func (r *run) gate(ctx context.Context, p int, window time.Duration) gateResult {
	c, d := r.c, r.d
	gr := gateResult{Step: p, Passed: true, HealthOK: true}
	var bad int
	if err := c.DB.QueryRow(ctx, `SELECT count(*) FROM placements WHERE id = ANY($1) AND health = 'unhealthy'`, ids(r.green)).Scan(&bad); err == nil && bad > 0 {
		gr.HealthOK, gr.Passed, gr.Reason = false, false, ReasonHealthFailed
		return gr
	}
	if c.Gates == nil || d.releaseID == nil {
		gr.InsufficientTraffic, gr.MetricsUnavailable = true, c.Gates == nil
		return gr
	}
	blue := ""
	if d.prevReleaseID != nil {
		blue = *d.prevReleaseID
	}
	st, err := c.Gates.CanaryStats(ctx, d.serviceID, *d.releaseID, blue, window)
	if err != nil {
		if ctx.Err() == nil {
			c.Log.Warn("canary gate: metrics", "deployment", d.id, "err", err)
		}
		gr.InsufficientTraffic, gr.MetricsUnavailable = true, true
		return gr
	}
	gr.GreenRequests = st.GreenRequests
	gr.GreenP95Seconds, gr.BlueP95Seconds = st.GreenP95, st.BlueP95
	if st.GreenRequests < minGateRequests {
		gr.InsufficientTraffic = true
		return gr
	}
	gr.Green5xxRate = st.Green5xx / st.GreenRequests
	if gr.Green5xxRate > d.opts.CanaryMax5xxRate {
		gr.Passed, gr.Reason = false, ReasonGate5xx
		return gr
	}
	if st.GreenP95Known && st.BlueP95Known && st.BlueP95 > 0 && st.GreenP95 > st.BlueP95*d.opts.CanaryMaxP95Ratio {
		gr.Passed, gr.Reason = false, ReasonGateLatency
	}
	return gr
}

// canaryGateEvent saves the result in the deployment and writes the canary_gate event (REQ-DEPLOY-105).
func (c *Control) canaryGateEvent(ctx context.Context, depID string, gr gateResult) {
	raw, _ := json.Marshal(gr)
	if _, err := c.DB.Exec(ctx, `UPDATE deployments SET canary_gate = $2 WHERE id = $1`, depID, raw); err != nil {
		c.Log.Warn("canary gate: save", "deployment", depID, "err", err)
	}
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	c.event(ctx, depID, map[string]any{"type": "canary_gate", "data": data})
	msg := fmt.Sprintf("gate of step %d%%: passed", gr.Step)
	if !gr.Passed {
		msg = fmt.Sprintf("gate of step %d%%: failed (%s)", gr.Step, gr.Reason)
	} else if gr.Extended && gr.InsufficientTraffic {
		msg = fmt.Sprintf("gate of step %d%%: too few requests, the step is extended", gr.Step)
	}
	if err := addLog(ctx, c.DB, depID, "deploy", msg); err != nil {
		c.Log.Warn("canary gate: log", "deployment", depID, "err", err)
	}
}

func (c *Control) setStateNote(ctx context.Context, depID, msg string) error {
	return c.setState(ctx, depID, "shifting", msg, nil)
}

// revertCanary takes the traffic away from the new containers, waits for the acks, stops them and ends the
// deployment as rolled_back. Blue was never touched, so the old release serves all the time.
func (r *run) revertCanary(ctx context.Context, reason, detail string) *deployErr {
	c := r.c
	bg, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := c.DB.Exec(bg, `UPDATE deployments SET canary_routing = false, canary_paused = false WHERE id = $1`, r.d.id); err != nil {
		c.Log.Error("canary revert", "deployment", r.d.id, "err", err)
	}
	if err := c.change(bg, func(tx pgx.Tx) ([]string, error) { return c.allNodeIDs(bg, tx) }); err != nil {
		c.Log.Error("canary revert: weights", "deployment", r.d.id, "err", err)
	}
	if err := r.switchAcks(bg, "canary-revert"); err != nil {
		c.Log.Warn("canary revert: ack", "deployment", r.d.id, "err", err)
	}
	r.discardGreen(bg, false)
	r.reverted = true
	return &deployErr{reason: reason, detail: detail, state: "rolled_back"}
}

// resumeCanary goes on with a canary after a server restart.
func (r *run) resumeCanary(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	var step *int
	if err := c.DB.QueryRow(ctx, `SELECT canary_step FROM deployments WHERE id = $1`, d.id).Scan(&step); err != nil {
		return fail(ReasonInternal, "database: %v", err)
	}
	idx := 0
	for i, s := range d.opts.CanarySteps {
		if step != nil && s == *step {
			idx = i
		}
	}
	if len(r.green) == 0 {
		return &deployErr{reason: ReasonServerRestarted, detail: "the server restarted and the new containers are gone", state: "rolled_back"}
	}
	return r.canary(ctx, idx, true)
}

// ---- alert events of deployments (REQ-ALERT-009: in the same transaction as the state change) ----

// deploymentAlert writes the alert event of a failed or rolled back deployment, with the reason code and the last log
// lines (REQ-ALERT-007).
func (c *Control) deploymentAlert(ctx context.Context, q dbq, depID, rule, severity string) {
	var orgID, serviceID, reason, detail string
	var releaseID *string
	err := q.QueryRow(ctx, `SELECT p.org_id, d.service_id, COALESCE(d.failure_reason,''), COALESCE(d.failure_detail,''), d.release_id FROM deployments d
		JOIN environments e ON e.id = d.environment_id JOIN projects p ON p.id = e.project_id WHERE d.id = $1`, depID).Scan(&orgID, &serviceID, &reason, &detail, &releaseID)
	if err != nil {
		c.Log.Warn("deployment alert", "deployment", depID, "err", err)
		return
	}
	payload := map[string]any{"rule": rule, "state": "firing", "severity": severity, "service_id": serviceID, "deployment_id": depID,
		"reason": reason, "detail": detail, "last_lines": c.lastLogLines(ctx, q, depID, 10)}
	if releaseID != nil {
		payload["release_id"] = *releaseID
	}
	b, _ := json.Marshal(payload)
	if _, err := q.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'alert',$2,$3)`, orgID, depID, b); err != nil {
		c.Log.Warn("deployment alert", "deployment", depID, "err", err)
	}
}

func (c *Control) lastLogLines(ctx context.Context, q dbq, depID string, n int) []string {
	rows, err := q.Query(ctx, `SELECT line FROM deployment_logs WHERE deployment_id = $1 ORDER BY seq DESC LIMIT $2`, depID, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if rows.Scan(&l) == nil {
			out = append([]string{l}, out...)
		}
	}
	return out
}
