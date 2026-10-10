// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"

	pb "github.com/polipan/polipan/proto/agentv1"
)

var _ orchestrator.NodeManager = (*Control)(nil)

// NodeJoined gives every node the new peer (09 section 2, step 5). The gateway calls it after a join.
func (c *Control) NodeJoined(ctx context.Context, nodeID string) {
	if err := c.NodesChanged(ctx); err != nil {
		c.Log.Warn("new peer was not sent", "node", nodeID, "err", err)
	}
}

// NodesChanged makes a new desired state for every node (peers, routes and endpoints depend on all nodes).
func (c *Control) NodesChanged(ctx context.Context) error {
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) { return c.allNodeIDs(ctx, tx) })
}

// alert writes an alert event (delivery to channels is M8, the event is the record, like in the backup package).
func (c *Control) alert(ctx context.Context, orgID, rule, severity, state, objectID string, details map[string]any) {
	p := map[string]any{"rule": rule, "severity": severity, "state": state}
	for k, v := range details {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	if _, err := c.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'alert',$2,$3)`, orgID, objectID, b); err != nil {
		c.Log.Warn("alert event", "rule", rule, "err", err)
	}
}

func (c *Control) nodeEvent(ctx context.Context, orgID, nodeID string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	if _, err := c.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'node',$2,$3)`, orgID, nodeID, b); err != nil {
		c.Log.Warn("node event", "err", err)
	}
}

// auditNode writes the audit event of a node action. The actor comes from the request, or is the system.
func (c *Control) auditNode(ctx context.Context, orgID, nodeID, action string, diff map[string]any) {
	if err := audit.Write(ctx, c.DB, audit.Event{OrgID: orgID, Action: action, TargetType: "node", TargetID: nodeID, Diff: diff}); err != nil {
		c.Log.Warn("audit of a node action", "action", action, "err", err)
	}
}

// setNodeState changes the state when it is one of from. Else it gives ErrNodeState.
func (c *Control) setNodeState(ctx context.Context, orgID, nodeID string, from []string, to, action string) error {
	var before string
	err := c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, nodeID, orgID).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.ErrNotFound
	}
	if err != nil {
		return err
	}
	tag, err := c.DB.Exec(ctx, `UPDATE nodes SET state = $4, drain_note = CASE WHEN $4 = 'draining' THEN drain_note ELSE NULL END, updated_at = now()
		WHERE id = $1 AND org_id = $2 AND state = ANY($3)`, nodeID, orgID, from, to)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: the node is %s", orchestrator.ErrNodeState, before)
	}
	c.nodeEvent(ctx, orgID, nodeID, map[string]any{"kind": "state", "from": before, "to": to, "action": action})
	c.auditNode(ctx, orgID, nodeID, "node."+action, map[string]any{"from": before, "to": to})
	return c.NodesChanged(ctx)
}

// CordonNode stops scheduling on a ready node. Existing workloads stay (09 section 6).
func (c *Control) CordonNode(ctx context.Context, orgID, nodeID, actorID string) error {
	return c.setNodeState(ctx, orgID, nodeID, []string{"ready"}, "cordoned", "cordon")
}

// UncordonNode makes a cordoned node ready again.
func (c *Control) UncordonNode(ctx context.Context, orgID, nodeID, actorID string) error {
	return c.setNodeState(ctx, orgID, nodeID, []string{"cordoned"}, "ready", "uncordon")
}

// SetMaintenance puts a cordoned node, or a draining node that is empty, into maintenance. enabled=false makes it ready.
func (c *Control) SetMaintenance(ctx context.Context, orgID, nodeID string, enabled bool, actorID string) error {
	if !enabled {
		return c.setNodeState(ctx, orgID, nodeID, []string{"maintenance"}, "ready", "maintenance_off")
	}
	var left int
	if err := c.DB.QueryRow(ctx, `SELECT count(*) FROM placements WHERE node_id = $1 AND role IN ('live','candidate','draining')`, nodeID).Scan(&left); err != nil {
		return err
	}
	var state string
	if err := c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, nodeID, orgID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.ErrNotFound
	} else if err != nil {
		return err
	}
	if state == "draining" && left > 0 {
		return fmt.Errorf("%w: the node still has %d workloads", orchestrator.ErrNodeState, left)
	}
	return c.setNodeState(ctx, orgID, nodeID, []string{"cordoned", "draining"}, "maintenance", "maintenance_on")
}

// DrainNode starts the drain: every workload moves away with a blue-green move (REQ-NODE-003). The drain goes on
// in the background. It ends with the state maintenance, or it stays draining and the node says what is left.
func (c *Control) DrainNode(ctx context.Context, orgID, nodeID, actorID string) error {
	if err := c.setNodeState(ctx, orgID, nodeID, []string{"ready", "cordoned"}, "draining", "drain"); err != nil {
		return err
	}
	c.startDrain(orgID, nodeID)
	return nil
}

// resumeDrains starts the drain of the nodes that were draining when the server stopped.
func (c *Control) resumeDrains(ctx context.Context) {
	rows, err := c.DB.Query(ctx, `SELECT org_id, id FROM nodes WHERE state = 'draining' AND deleted_at IS NULL`)
	if err != nil {
		return
	}
	type pair struct{ org, id string }
	var list []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.org, &p.id) == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	for _, p := range list {
		c.startDrain(p.org, p.id)
	}
}

func (c *Control) startDrain(orgID, nodeID string) {
	c.mu.Lock()
	if c.draining == nil {
		c.draining = map[string]bool{}
	}
	if c.draining[nodeID] {
		c.mu.Unlock()
		return
	}
	c.draining[nodeID] = true
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.draining, nodeID)
			c.mu.Unlock()
		}()
		c.drainLoop(c.runCtx, orgID, nodeID)
	}()
}

// drainRetry is the pause between two passes of a drain that left something.
const drainRetry = 20 * time.Second

func (c *Control) drainLoop(ctx context.Context, orgID, nodeID string) {
	for ctx.Err() == nil {
		var state string
		if err := c.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1`, nodeID).Scan(&state); err != nil || state != "draining" {
			return // the user changed the state, or the node is gone
		}
		left := c.drainPass(ctx, orgID, nodeID)
		if ctx.Err() != nil {
			return
		}
		if len(left) == 0 {
			if err := c.setNodeState(ctx, orgID, nodeID, []string{"draining"}, "maintenance", "drained"); err != nil {
				c.Log.Warn("drain: maintenance", "node", nodeID, "err", err)
			}
			return
		}
		note := strings.Join(left, "; ")
		_, _ = c.DB.Exec(ctx, `UPDATE nodes SET drain_note = $2, updated_at = now() WHERE id = $1`, nodeID, note)
		c.nodeEvent(ctx, orgID, nodeID, map[string]any{"kind": "drain", "left": left})
		select {
		case <-ctx.Done():
			return
		case <-time.After(drainRetry):
		}
	}
}

// drainPass moves every service off the node once. It returns what is left, one line per service.
func (c *Control) drainPass(ctx context.Context, orgID, nodeID string) []string {
	rows, err := c.DB.Query(ctx, `SELECT DISTINCT s.id, s.name FROM placements p JOIN services s ON s.id = p.service_id
		WHERE p.node_id = $1 AND p.role IN ('live','candidate','draining') AND s.deleted_at IS NULL ORDER BY s.name`, nodeID)
	if err != nil {
		return []string{"cannot read the workloads: " + err.Error()}
	}
	type svc struct{ id, name string }
	var list []svc
	for rows.Next() {
		var s svc
		if err := rows.Scan(&s.id, &s.name); err != nil {
			rows.Close()
			return []string{err.Error()}
		}
		list = append(list, s)
	}
	rows.Close()
	var left []string
	for _, s := range list {
		id, _, err := c.DeployWith(ctx, orchestrator.DeployParams{ServiceID: s.id, Trigger: "move", MoveFrom: nodeID, Cause: "drain", VolumeCopy: "backup"})
		if err != nil {
			left = append(left, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		state, detail, err := c.waitDeployment(ctx, id)
		if err != nil {
			return left
		}
		if state != "live" {
			left = append(left, fmt.Sprintf("%s: the move %s (%s)", s.name, state, detail))
		}
	}
	// Kept stopped containers of older releases are not workloads: they go with the drain.
	if stopped, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE node_id = $1 AND role = 'stopped'`, nodeID); err == nil && len(stopped) > 0 {
		for _, p := range stopped {
			c.removeContainer(ctx, p.node, p.id)
		}
		_, _ = c.DB.Exec(ctx, `DELETE FROM placements WHERE id = ANY($1)`, ids(stopped))
	}
	var remaining int
	if err := c.DB.QueryRow(ctx, `SELECT count(*) FROM placements WHERE node_id = $1 AND role IN ('live','candidate','draining')`, nodeID).Scan(&remaining); err == nil && remaining > 0 && len(left) == 0 {
		left = append(left, fmt.Sprintf("%d workloads are still on the node", remaining))
	}
	return left
}

// DeleteNode removes a node (REQ-NODE-008). The certificate is revoked and the stream is closed. A node with
// workloads needs force: its replicas are lost. Services with failover auto move to other nodes, the others are
// stopped with a reason. The row stays as a tombstone until no placement points to it.
func (c *Control) DeleteNode(ctx context.Context, orgID, nodeID string, force bool, actorID string) error {
	var name, serial string
	err := c.DB.QueryRow(ctx, `SELECT name, COALESCE(cert_serial,'') FROM nodes WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, nodeID, orgID).Scan(&name, &serial)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.ErrNotFound
	}
	if err != nil {
		return err
	}
	var live int
	if err := c.DB.QueryRow(ctx, `SELECT count(*) FROM placements WHERE node_id = $1 AND role IN ('live','candidate','draining')`, nodeID).Scan(&live); err != nil {
		return err
	}
	if live > 0 && !force {
		return orchestrator.ErrNodeNotEmpty
	}
	err = c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if serial != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO revoked_certs (serial, node_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, serial, nodeID); err != nil {
				return nil, err
			}
		}
		// Services that cannot fail over stop with a visible reason. The ones with failover auto wait for the failover.
		if _, err := tx.Exec(ctx, `UPDATE services SET desired_state = 'stopped', failover_state = 'blocked',
			failover_reason = 'the node ' || $2 || ' was deleted', failover_at = now(), updated_at = now()
			WHERE failover <> 'auto' AND deleted_at IS NULL AND id IN (SELECT service_id FROM placements WHERE node_id = $1 AND role IN ('live','candidate','draining'))`, nodeID, name); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE node_id = $1 AND (role = 'stopped' OR service_id IN
			(SELECT id FROM services WHERE failover <> 'auto' OR deleted_at IS NOT NULL))`, nodeID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET deleted_at = now(), state = 'down', down_at = COALESCE(down_at, now()),
			name = name || '.deleted.' || right(id, 8), mesh_subnet = NULL, mesh_ip = NULL, wireguard_public_key = NULL, updated_at = now()
			WHERE id = $1`, nodeID); err != nil {
			return nil, err
		}
		if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "node.delete", TargetType: "node", TargetID: nodeID,
			Diff: map[string]any{"name": name, "force": force, "workloads": live}}); err != nil {
			return nil, err
		}
		return c.allNodeIDs(ctx, tx)
	})
	if err != nil {
		return err
	}
	if d, ok := c.Sender.(interface{ Disconnect(nodeID string) }); ok {
		d.Disconnect(nodeID)
	}
	c.nodeEvent(ctx, orgID, nodeID, map[string]any{"kind": "deleted", "name": name})
	return nil
}

// SelfCheck asks the agent of the node to test Docker, Caddy and WireGuard (REQ-NODE-011).
func (c *Control) SelfCheck(ctx context.Context, orgID, nodeID string) (orchestrator.SelfCheckResult, error) {
	var res orchestrator.SelfCheckResult
	var one string
	if err := c.DB.QueryRow(ctx, `SELECT id FROM nodes WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, nodeID, orgID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		return res, orchestrator.ErrNotFound
	} else if err != nil {
		return res, err
	}
	if c.Sender == nil || !c.Sender.Connected(nodeID) {
		return res, orchestrator.ErrNodeOffline
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ack, err := c.Sender.Send(cctx, nodeID, &pb.Command{CommandId: gateway.NewID("cmd"), Body: &pb.Command_SelfCheck{SelfCheck: &pb.SelfCheck{}}})
	if err != nil {
		return res, err
	}
	if !ack.GetOk() {
		return res, fmt.Errorf("self-check: %s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
	}
	if err := json.Unmarshal(ack.GetResultJson(), &res); err != nil {
		return res, err
	}
	return res, nil
}
