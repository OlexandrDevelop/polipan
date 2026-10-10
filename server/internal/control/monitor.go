// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
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
		tag, err := c.DB.Exec(ctx, `UPDATE nodes SET state = $3, updated_at = $4 WHERE id = $1 AND state = $2`, r.id, r.state, next, now)
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
	}
	return nil
}
