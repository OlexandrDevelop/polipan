// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// failoverRetry is the pause before a failed failover of a service is tried again.
const failoverRetry = 60 * time.Second

// Reasons that a failover is blocked (09 section 9). They are shown on the service and in the alert.
const (
	blockNoReachable = "the server cannot reach any other node, so it waits (it assumes its own network is the problem)"
	blockPolicyNone  = "failover is off for this service"
	blockDataLoss    = "the service has a local volume and allow_data_loss_failover is off"
	blockNoBackup    = "the service has a local volume and no successful backup"
)

// failoverTick starts the failover of every node that is down and still has workloads (09 section 9, rule 1).
// The work of one node runs once at a time in the background.
func (c *Control) failoverTick(ctx context.Context) {
	rows, err := c.DB.Query(ctx, `SELECT n.id, n.org_id FROM nodes n WHERE n.state = 'down'
		AND EXISTS (SELECT 1 FROM placements p WHERE p.node_id = n.id AND p.role IN ('live','candidate','draining'))`)
	if err != nil {
		c.Log.Warn("failover: read nodes", "err", err)
		return
	}
	type pair struct{ org, id string }
	var list []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.id, &p.org) == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	for _, p := range list {
		c.startFailover(p.org, p.id)
	}
	// A service that was blocked by a node that is back, or that has no placement on a down node any more, is healthy.
	_, _ = c.DB.Exec(ctx, `UPDATE services s SET failover_state = 'healthy', failover_reason = NULL
		WHERE s.failover_state IN ('blocked','moving')
		  AND NOT EXISTS (SELECT 1 FROM placements p JOIN nodes n ON n.id = p.node_id WHERE p.service_id = s.id AND n.state = 'down' AND p.role IN ('live','candidate','draining'))
		  AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = s.id AND d.trigger = 'failover' AND d.state IN ('queued',`+runningStates+`))
		  AND (s.failover_reason IS NULL OR s.failover_reason NOT LIKE 'the node % was deleted')`)
}

func (c *Control) startFailover(orgID, nodeID string) {
	c.mu.Lock()
	if c.failing == nil {
		c.failing = map[string]bool{}
	}
	if c.failing[nodeID] || c.runCtx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.failing[nodeID] = true
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.failing, nodeID)
			c.mu.Unlock()
		}()
		c.failoverNode(c.runCtx, orgID, nodeID)
	}()
}

type failSvc struct {
	id, name, policy, state, reason, projectID string
	allowLoss                                  bool
	at                                         *time.Time
}

// otherNodeReachable is the "no reachable node" guard (09 section 9, rule 7): at least one other node must be
// ready and connected. Without one, all nodes look down because the network of the server is down.
func (c *Control) otherNodeReachable(ctx context.Context, orgID, nodeID string) bool {
	ids, err := queryStrings(ctx, c.DB, `SELECT id FROM nodes WHERE org_id = $1 AND id <> $2 AND state = 'ready' AND deleted_at IS NULL ORDER BY id`, orgID, nodeID)
	if err != nil || c.Sender == nil {
		return false
	}
	for _, id := range ids {
		if c.Sender.Connected(id) {
			return true
		}
	}
	return false
}

// failoverNode moves the services of a failed node, in the order they were created.
func (c *Control) failoverNode(ctx context.Context, orgID, nodeID string) {
	list, err := c.failoverServices(ctx, nodeID)
	if err != nil || len(list) == 0 {
		return
	}
	// The guard comes first: when no other node is reachable nothing moves and the services say why.
	if !c.otherNodeReachable(ctx, orgID, nodeID) {
		for _, s := range list {
			c.blockFailover(ctx, orgID, nodeID, s, blockNoReachable)
		}
		return
	}
	for _, s := range list {
		if ctx.Err() != nil {
			return
		}
		c.failoverService(ctx, orgID, nodeID, s)
	}
}

func (c *Control) failoverServices(ctx context.Context, nodeID string) ([]failSvc, error) {
	rows, err := c.DB.Query(ctx, `SELECT DISTINCT s.id, s.name, s.failover, s.allow_data_loss_failover, s.failover_state, COALESCE(s.failover_reason,''),
		   s.failover_at, e.project_id, s.created_at
		   FROM placements p JOIN services s ON s.id = p.service_id JOIN environments e ON e.id = s.environment_id
		  WHERE p.node_id = $1 AND p.role IN ('live','candidate','draining') AND s.deleted_at IS NULL AND s.desired_state = 'running'
		  ORDER BY s.created_at, s.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []failSvc
	for rows.Next() {
		var s failSvc
		var created time.Time
		if err := rows.Scan(&s.id, &s.name, &s.policy, &s.allowLoss, &s.state, &s.reason, &s.at, &s.projectID, &created); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// blockFailover records that a service stays where it is, once per reason: an audit event, an alert, and the
// reason on the service (UI-41, AC-NODE-05).
func (c *Control) blockFailover(ctx context.Context, orgID, nodeID string, s failSvc, reason string) {
	tag, err := c.DB.Exec(ctx, `UPDATE services SET failover_state = 'blocked', failover_reason = $2, failover_at = now()
		WHERE id = $1 AND (failover_state <> 'blocked' OR failover_reason IS DISTINCT FROM $2)`, s.id, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	c.Log.Warn("failover blocked", "service", s.id, "node", nodeID, "reason", reason)
	if err := audit.Write(ctx, c.DB, audit.Event{OrgID: orgID, ActorType: audit.ActorSystem, Action: "service.failover_blocked", TargetType: "service",
		TargetID: s.id, ProjectID: s.projectID, Diff: map[string]any{"node_id": nodeID, "reason": reason}}); err != nil {
		c.Log.Warn("audit of a blocked failover", "service", s.id, "err", err)
	}
	c.alert(ctx, orgID, "failover_blocked", "critical", "firing", s.id, map[string]any{"service_id": s.id, "node_id": nodeID, "reason": reason})
}

// failoverService decides about one service and, when it may move, runs the failover deployment.
func (c *Control) failoverService(ctx context.Context, orgID, nodeID string, s failSvc) {
	// One failover deployment at a time for a service.
	var active bool
	if err := c.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments WHERE service_id = $1 AND trigger = 'failover' AND state IN ('queued',`+runningStates+`))`, s.id).Scan(&active); err != nil || active {
		return
	}
	if s.policy != "auto" {
		c.blockFailover(ctx, orgID, nodeID, s, blockPolicyNone)
		return
	}
	// A failed failover is tried again after a pause.
	if s.state == "blocked" && s.at != nil && s.reason != "" && len(s.reason) > 15 && s.reason[:15] == "failover failed" && time.Since(*s.at) < failoverRetry {
		return
	}
	local, err := c.hasLocalVolume(ctx, c.DB, s.id)
	if err != nil {
		return
	}
	var lostSince *time.Time
	if local {
		if !s.allowLoss {
			c.blockFailover(ctx, orgID, nodeID, s, blockDataLoss)
			return
		}
		if c.Mover == nil {
			c.blockFailover(ctx, orgID, nodeID, s, blockNoBackup)
			return
		}
		when, ok, err := c.Mover.LatestBackup(ctx, s.id)
		if err != nil || !ok {
			c.blockFailover(ctx, orgID, nodeID, s, blockNoBackup)
			return
		}
		lostSince = &when
	}
	// Last look before anything moves: the node must still be down (split-brain guard).
	var state string
	if err := c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1`, nodeID).Scan(&state); err != nil || state != "down" {
		return
	}
	_, _ = c.DB.Exec(ctx, `UPDATE services SET failover_state = 'moving', failover_reason = NULL, failover_at = now(),
		failover_data_lost_since = $2 WHERE id = $1`, s.id, lostSince)
	extra := map[string]any{"node_id": nodeID, "service_id": s.id}
	if lostSince != nil {
		extra["data_lost_since"] = lostSince.UTC().Format(time.RFC3339)
		extra["message"] = "data since " + lostSince.UTC().Format(time.RFC3339) + " is lost"
	}
	if err := audit.Write(ctx, c.DB, audit.Event{OrgID: orgID, ActorType: audit.ActorSystem, Action: "service.failover_start", TargetType: "service",
		TargetID: s.id, ProjectID: s.projectID, Diff: extra}); err != nil {
		c.Log.Warn("audit of a failover", "service", s.id, "err", err)
	}
	c.alert(ctx, orgID, "service_failover", "warning", "firing", s.id, extra)
	id, _, err := c.DeployWith(ctx, orchestrator.DeployParams{ServiceID: s.id, Trigger: "failover", FailoverFrom: []string{nodeID}, Cause: "failover"})
	if err != nil {
		c.blockFailover(ctx, orgID, nodeID, s, fmt.Sprintf("failover failed: %v", err))
		return
	}
	state, detail, err := c.waitDeployment(ctx, id)
	if err != nil {
		return
	}
	if state == "live" {
		c.alert(ctx, orgID, "service_failover", "warning", "resolved", s.id, extra)
		return
	}
	// The node may have come back while the deployment waited: then nothing was moved and the service is fine.
	if errNode := c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1`, nodeID).Scan(&state); errNode == nil && state != "down" {
		_, _ = c.DB.Exec(ctx, `UPDATE services SET failover_state = 'healthy', failover_reason = NULL WHERE id = $1`, s.id)
		return
	}
	c.blockFailover(ctx, orgID, nodeID, s, "failover failed: "+detail)
}

// ensureNodeStillDown is the check inside the failover deployment: when a failed node came back, the deployment
// stops and nothing moves (REQ-AGENT-009 guards the old node, this guards the new copy).
func (r *run) ensureNodeStillDown(ctx context.Context) *deployErr {
	for _, n := range r.d.opts.FailoverFrom {
		var state string
		err := r.c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1`, n).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fail(ReasonInternal, "database: %v", err)
		}
		if state != "down" {
			return fail(ReasonNodeUnavailable, "node %s is back: nothing is moved", n)
		}
	}
	return nil
}
