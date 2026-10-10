// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// CheckNodes does one round of the node state monitor:
// ready -> suspect after NodeSuspectAfter, ready/suspect -> down after NodeDownAfter, both without heartbeat.
// The way back to ready is done by the gateway on Hello or heartbeat.
func (c *Control) CheckNodes(ctx context.Context) error {
	type row struct {
		id, org, state string
		last           time.Time
	}
	rows, err := c.DB.Query(ctx,
		`SELECT id, org_id, state, last_heartbeat_at FROM nodes
		  WHERE state IN ('ready','suspect') AND last_heartbeat_at IS NOT NULL`)
	if err != nil {
		return err
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.org, &r.state, &r.last); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := c.Clock.Now()
	for _, r := range list {
		last := r.last
		if last.Before(c.startedAt) {
			last = c.startedAt
		}
		age := now.Sub(last)
		next := r.state
		switch {
		case age > c.Config.NodeDownAfter:
			next = "down"
		case age > c.Config.NodeSuspectAfter && r.state == "ready":
			next = "suspect"
		}
		if next == r.state {
			continue
		}
		tag, err := c.DB.Exec(ctx, `UPDATE nodes SET state = $3, updated_at = $4, down_at = CASE WHEN $3 = 'down' THEN $4 ELSE down_at END
			WHERE id = $1 AND state = $2`, r.id, r.state, next, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue // the node changed in the meantime
		}
		payload, _ := json.Marshal(map[string]any{"kind": "state", "from": r.state, "to": next})
		if _, err := c.DB.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1,'node',$2,$3)`, r.org, r.id, payload); err != nil {
			return err
		}
		c.Log.Warn("node state changed", "node", r.id, "from", r.state, "to", next)
		switch next {
		case "suspect":
			c.alert(ctx, r.org, "node_suspect", "warning", "firing", r.id, map[string]any{"node_id": r.id})
		case "down":
			c.alert(ctx, r.org, "node_down", "critical", "firing", r.id, map[string]any{"node_id": r.id})
		}
	}
	return c.watchDownSet(ctx)
}

// watchDownSet sends new routes to all nodes when a node enters or leaves the state down: the ingress nodes
// drop a node that is down as upstream and take it back when it returns (09 section 6). It also resolves the
// node_suspect and node_down alerts of nodes that came back.
func (c *Control) watchDownSet(ctx context.Context) error {
	rows, err := c.DB.Query(ctx, `SELECT id, org_id FROM nodes WHERE state = 'down' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	cur := map[string]string{}
	var key []string
	for rows.Next() {
		var id, org string
		if err := rows.Scan(&id, &org); err != nil {
			rows.Close()
			return err
		}
		cur[id] = org
		key = append(key, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	joined := strings.Join(key, ",")
	c.monMu.Lock()
	changed := c.downInit && joined != c.lastDown
	prev := c.lastDown
	c.lastDown, c.downInit = joined, true
	c.monMu.Unlock()
	if !changed {
		return nil
	}
	for _, id := range strings.Split(prev, ",") {
		if _, still := cur[id]; id != "" && !still {
			var org string
			if c.DB.QueryRow(ctx, `SELECT org_id FROM nodes WHERE id = $1`, id).Scan(&org) == nil {
				c.alert(ctx, org, "node_down", "critical", "resolved", id, map[string]any{"node_id": id})
			}
		}
	}
	return c.NodesChanged(ctx)
}
