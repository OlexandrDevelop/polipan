// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

var _ gateway.HealthHook = (*Control)(nil)

// HealthChanged is called by the gateway when the health of a placement changed (REQ-HC-007, REQ-HC-008).
// A live replica that turned unhealthy leaves the ingress upstreams, and comes back when it is healthy again.
// When all live replicas are unhealthy, Caddy keeps routing to them and a critical alert is written.
func (c *Control) HealthChanged(ctx context.Context, nodeID, placementID, from, to string) {
	var serviceID, role, orgID string
	err := c.DB.QueryRow(ctx, `SELECT p.service_id, p.role, pr.org_id FROM placements p
		JOIN services s ON s.id = p.service_id JOIN environments e ON e.id = s.environment_id JOIN projects pr ON pr.id = e.project_id
		WHERE p.id = $1`, placementID).Scan(&serviceID, &role, &orgID)
	if err != nil || role != "live" {
		return
	}
	if (from == "unhealthy") != (to == "unhealthy") {
		if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) { return c.allNodeIDs(ctx, tx) }); err != nil {
			c.Log.Warn("health: new ingress", "service", serviceID, "err", err)
		}
	}
	var live, unhealthy int
	_ = c.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE health = 'unhealthy') FROM placements WHERE service_id = $1 AND role = 'live'`,
		serviceID).Scan(&live, &unhealthy)
	rule := map[string]any{"rule": "service_unhealthy", "service_id": serviceID, "placement_id": placementID}
	switch {
	case to == "unhealthy" && unhealthy == live:
		rule["state"], rule["severity"] = "firing", "critical"
	case to == "unhealthy":
		rule["state"], rule["severity"] = "firing", "warning"
	case from == "unhealthy":
		rule["state"], rule["severity"] = "resolved", "info"
	default:
		return
	}
	b, _ := json.Marshal(rule)
	if _, err := c.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'alert',$2,$3)`, orgID, serviceID, b); err != nil {
		c.Log.Warn("health: alert event", "service", serviceID, "err", err)
	}
}

// TestHealth runs the health check of a live container once (POST health-check:test).
func (c *Control) TestHealth(ctx context.Context, serviceID string) (orchestrator.HealthResult, error) {
	var placementID, nodeID string
	err := c.DB.QueryRow(ctx, `SELECT p.id, p.node_id FROM placements p JOIN services s ON s.id = p.service_id
		WHERE p.service_id = $1 AND s.deleted_at IS NULL AND p.role = 'live' ORDER BY p.replica_index, p.id LIMIT 1`, serviceID).Scan(&placementID, &nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.HealthResult{}, orchestrator.ErrNoLiveRelease
	}
	if err != nil {
		return orchestrator.HealthResult{}, err
	}
	if c.Sender == nil || !c.Sender.Connected(nodeID) {
		return orchestrator.HealthResult{}, orchestrator.ErrNoNode
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ack, err := c.Sender.Send(cctx, nodeID, &pb.Command{Body: &pb.Command_ProbeHealth{ProbeHealth: &pb.ProbeHealth{PlacementId: placementID}}})
	if err != nil {
		return orchestrator.HealthResult{}, err
	}
	var res orchestrator.HealthResult
	if !ack.GetOk() {
		res.Error = ack.GetErrorMessage()
		return res, nil
	}
	_ = json.Unmarshal(ack.GetResultJson(), &res)
	return res, nil
}

// Restart restarts the live containers of the service: stop with the grace period, then the desired state starts them again.
// No release is made (restartService).
func (c *Control) Restart(ctx context.Context, serviceID string) error {
	s, err := c.loadService(ctx, c.DB, serviceID)
	if err != nil {
		return err
	}
	live, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1 AND role = 'live'`, serviceID)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return orchestrator.ErrNoLiveRelease
	}
	for _, p := range live {
		if c.Sender == nil || !c.Sender.Connected(p.node) {
			return orchestrator.ErrNoNode
		}
	}
	for _, p := range live {
		cctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.stopGraceSeconds()+30)*time.Second)
		_, err := c.Sender.Send(cctx, p.node, &pb.Command{Body: &pb.Command_StopWorkload{
			StopWorkload: &pb.StopWorkload{PlacementId: p.id, GraceSeconds: uint32(s.cfg.stopGraceSeconds())}}})
		cancel()
		if err != nil {
			return err
		}
	}
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) { return nodesOf(live), nil })
}
