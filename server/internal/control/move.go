// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// DataMover copies the data of the volumes of a service to another node through a backup target (09 section 7).
// The backup package implements it. Without it a service with a local volume cannot move.
type DataMover interface {
	// SnapshotVolumes makes a fresh backup of every volume of the service and waits for it.
	SnapshotVolumes(ctx context.Context, serviceID string) error
	// LatestBackup tells when the last successful backup of the volumes finished.
	LatestBackup(ctx context.Context, serviceID string) (time.Time, bool, error)
	// RestoreVolumes loads the latest successful snapshot of every volume of the service into the volumes on the node.
	RestoreVolumes(ctx context.Context, serviceID, nodeID string) error
}

// copyData is the data copy of a move or a failover (09 sections 7 and 9). The service is stopped already (move), or
// its node is gone (failover). A move makes a fresh snapshot, a failover uses the last good one.
func (r *run) copyData(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if c.Mover == nil {
		return fail(ReasonInvalidConfig, "the data cannot be copied: the backup service is not available")
	}
	target := r.nodes[0]
	if d.trigger == "move" {
		if e := r.step(ctx, "starting", "the service is stopped: a backup of its volumes is made", nil); e != nil {
			return e
		}
		if err := c.Mover.SnapshotVolumes(ctx, d.serviceID); err != nil {
			return fail(ReasonContainerStartFailed, "backup of the volumes: %v", err)
		}
	} else {
		when, ok, err := c.Mover.LatestBackup(ctx, d.serviceID)
		if err != nil || !ok {
			return fail(ReasonContainerStartFailed, "the service has no successful backup")
		}
		if _, err := c.DB.Exec(ctx, `UPDATE services SET failover_data_lost_since = $2 WHERE id = $1`, d.serviceID, when); err != nil {
			return fail(ReasonInternal, "database: %v", err)
		}
		msg := "data since " + when.UTC().Format(time.RFC3339) + " is lost"
		c.event(ctx, d.id, map[string]any{"type": "log", "data": map[string]any{"stream": "deploy", "line": msg}})
		if e := r.step(ctx, "starting", "the last backup is restored on the new node: "+msg, map[string]any{"data_lost_since": when}); e != nil {
			return e
		}
	}
	if err := c.Mover.RestoreVolumes(ctx, d.serviceID, target); err != nil {
		return fail(ReasonContainerStartFailed, "restore of the volumes on %s: %v", target, err)
	}
	return nil
}

// recordMove writes the audit event and the platform event of a finished move or failover (UI-41).
func (r *run) recordMove(ctx context.Context) {
	c, d := r.c, r.d
	if d.trigger != "move" && d.trigger != "failover" {
		return
	}
	from := map[string]bool{}
	for _, p := range r.blue {
		from[p.node] = true
	}
	for _, p := range r.dead {
		from[p.node] = true
	}
	var src []string
	for n := range from {
		src = append(src, n)
	}
	cause := "manual"
	if d.opts.Cause != "" {
		cause = d.opts.Cause
	}
	if d.trigger == "failover" {
		cause = "failover"
	}
	diff := map[string]any{"deployment_id": d.id, "from_nodes": src, "to_nodes": distinct(r.nodes), "cause": cause, "data_copy": r.dataCopy}
	ev := audit.Event{OrgID: r.svc.orgID, ActorType: audit.ActorSystem, Action: "service.move", TargetType: "service", TargetID: d.serviceID,
		ProjectID: r.svc.projectID, Diff: diff}
	if d.trigger == "failover" {
		ev.Action = "service.failover"
	} else if d.actorID != "" {
		ev.ActorType, ev.ActorID = audit.ActorUser, d.actorID
	}
	if err := audit.Write(ctx, c.DB, ev); err != nil {
		c.Log.Warn("audit of a move", "deployment", d.id, "err", err)
	}
	payload, _ := json.Marshal(map[string]any{"type": d.trigger, "state": "done", "deployment_id": d.id, "from_nodes": src, "to_nodes": distinct(r.nodes), "cause": cause})
	if _, err := c.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'service',$2,$3)`, r.svc.orgID, d.serviceID, payload); err != nil {
		c.Log.Warn("event of a move", "deployment", d.id, "err", err)
	}
}

// MoveService moves the replicas of a service to another node (REQ-NODE-006). A stateless service goes with
// blue-green and no downtime. A service with a local volume goes with a data copy and a short downtime.
func (c *Control) MoveService(ctx context.Context, serviceID, targetNodeID, volumeCopy, actorID string) (string, []orchestrator.Warning, error) {
	var state, orgID string
	err := c.DB.QueryRow(ctx, `SELECT n.state, n.org_id FROM nodes n JOIN projects p ON p.org_id = n.org_id JOIN environments e ON e.project_id = p.id
		JOIN services s ON s.environment_id = e.id WHERE n.id = $1 AND s.id = $2 AND s.deleted_at IS NULL`, targetNodeID, serviceID).Scan(&state, &orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, orchestrator.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if state != "ready" {
		return "", nil, fmt.Errorf("%w: the target node is %s", orchestrator.ErrNodeState, state)
	}
	placed, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1 AND role IN ('live','draining') ORDER BY node_id, id`, serviceID)
	if err != nil {
		return "", nil, err
	}
	if len(placed) == 0 {
		return "", nil, orchestrator.ErrNoLiveRelease
	}
	moves := false
	for _, p := range placed {
		moves = moves || p.node != targetNodeID
	}
	if !moves {
		return "", nil, fmt.Errorf("%w: the service runs on this node already", orchestrator.ErrNodeState)
	}
	// A service with replicas on several nodes needs the node to leave: the first node of its replicas that is not the target.
	from := ""
	for _, p := range placed {
		if p.node != targetNodeID {
			from = p.node
			break
		}
	}
	local, err := c.hasLocalVolume(ctx, c.DB, serviceID)
	if err != nil {
		return "", nil, err
	}
	if local {
		if volumeCopy == "" {
			volumeCopy = "backup"
		}
		if c.Mover == nil {
			return "", nil, orchestrator.ErrStrategyUnavailable
		}
	}
	return c.DeployWith(ctx, orchestrator.DeployParams{ServiceID: serviceID, ActorID: actorID, Trigger: "move",
		TargetNodeID: targetNodeID, MoveFrom: from, VolumeCopy: volumeCopy})
}

// waitDeployment waits until a deployment is in a terminal state. It returns the state and the failure detail.
func (c *Control) waitDeployment(ctx context.Context, id string) (state, detail string, err error) {
	for {
		var reason, d *string
		if err := c.DB.QueryRow(ctx, `SELECT state, failure_reason, failure_detail FROM deployments WHERE id = $1`, id).Scan(&state, &reason, &d); err != nil {
			return "", "", err
		}
		if terminalStates[state] {
			if reason != nil {
				detail = *reason
				if d != nil {
					detail += ": " + *d
				}
			}
			return state, detail, nil
		}
		select {
		case <-ctx.Done():
			return state, "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
