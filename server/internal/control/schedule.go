// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/sched"
)

// schedNodes reads the nodes of an org with the load that the scheduler needs: the limits of the placed workloads
// and how many replicas of the service run on each node. keep are the placements that stay where they are, so the
// placements that this deployment replaces do not count as replicas (09 section 7).
func (c *Control) schedNodes(ctx context.Context, q dbq, orgID string, keep []string) ([]sched.Node, error) {
	if keep == nil {
		keep = []string{}
	}
	rows, err := q.Query(ctx,
		`SELECT n.id, n.name, n.state, n.labels, n.capacity,
		        COALESCE(SUM((r.config_snapshot->'resources'->>'cpu_millis')::numeric) FILTER (WHERE p.role IN ('live','candidate','draining')), 0),
		        COALESCE(SUM((r.config_snapshot->'resources'->>'memory_bytes')::numeric) FILTER (WHERE p.role IN ('live','candidate','draining')), 0),
		        COUNT(p.id) FILTER (WHERE p.id = ANY($2))
		   FROM nodes n LEFT JOIN placements p ON p.node_id = n.id LEFT JOIN releases r ON r.id = p.release_id
		  WHERE n.org_id = $1
		  GROUP BY n.id ORDER BY n.name, n.id`, orgID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sched.Node
	for rows.Next() {
		var n sched.Node
		var labels, capRaw []byte
		var usedCPU, usedMem float64
		if err := rows.Scan(&n.ID, &n.Name, &n.State, &labels, &capRaw, &usedCPU, &usedMem, &n.Replicas); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(labels, &n.Labels)
		var capa struct {
			CPU uint64 `json:"cpu_millis"`
			Mem uint64 `json:"memory_bytes"`
		}
		if len(capRaw) > 0 && json.Unmarshal(capRaw, &capa) == nil {
			n.CPUMillis, n.MemoryBytes = capa.CPU, capa.Mem
		}
		n.UsedCPUMillis, n.UsedMemoryBytes = uint64(usedCPU), uint64(usedMem)
		out = append(out, n)
	}
	return out, rows.Err()
}

// schedRequest makes the scheduler request of a service config (REQ-NODE-004). The label and name rules come from
// config.placement, the limits from config.resources.
func schedRequest(cfg serviceConfig, replicas int) sched.Request {
	req := sched.Request{Replicas: replicas, Names: cfg.Placement.Nodes, Labels: cfg.Placement.Labels, NoSpread: !cfg.spread()}
	if cfg.Resources != nil {
		req.CPUMillis, req.MemoryBytes = uint64(cfg.Resources.CPUMillis), cfg.Resources.MemoryBytes
	}
	return req
}

// volumeNode is the node of the local volumes of the service, or "" when it has none or they are not placed yet.
func (c *Control) volumeNode(ctx context.Context, q dbq, serviceID string) (string, error) {
	var node *string
	err := q.QueryRow(ctx, `SELECT node_id FROM volumes WHERE service_id = $1 AND kind <> 'tmpfs' AND node_id IS NOT NULL ORDER BY name LIMIT 1`, serviceID).Scan(&node)
	if errors.Is(err, pgx.ErrNoRows) || node == nil {
		return "", nil
	}
	return *node, err
}

// hasLocalVolume tells if the service has a volume on a node (not tmpfs).
func (c *Control) hasLocalVolume(ctx context.Context, q dbq, serviceID string) (bool, error) {
	var has bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM volumes WHERE service_id = $1 AND kind <> 'tmpfs')`, serviceID).Scan(&has)
	return has, err
}

// nodeError makes the deploy error of a scheduler answer: the message lists the missing capacity or labels.
func nodeError(err error) *deployErr {
	var nf *sched.NoFitError
	if errors.As(err, &nf) {
		return fail(ReasonNodeUnavailable, "%s", nf.Message)
	}
	return fail(ReasonNodeUnavailable, "scheduler: %v", err)
}

// schedule picks the nodes of the new replicas of a deployment (09 section 7). The first pass checks the capacity.
// When no node has room, a second pass ignores it: checkCapacity then decides between a recreate and a failure,
// as before M7.
func (r *run) schedule(ctx context.Context) ([]string, *deployErr) {
	c, d := r.c, r.d
	count := r.cfg.replicas()
	keepIDs := ids(r.keep)
	req := schedRequest(r.cfg, count)
	req.Exclude = r.exclude
	switch d.trigger {
	case "move":
		count = len(r.blue)
		if count == 0 {
			return nil, fail(ReasonNodeUnavailable, "the service has no container on the node to move")
		}
		req.Replicas = count
		if d.targetNode != "" {
			req.OnlyIDs = []string{d.targetNode}
			// The move gives the replicas to the chosen node, whatever the spread rule says.
			req.NoSpread = true
		}
	case "failover":
		count = len(r.dead)
		if count == 0 {
			return nil, fail(ReasonNodeUnavailable, "the service has no container on the failed node")
		}
		req.Replicas = count
	default:
		if vn, err := c.volumeNode(ctx, c.DB, d.serviceID); err != nil {
			return nil, fail(ReasonInternal, "volumes: %v", err)
		} else if vn != "" {
			req.VolumeNode = vn
		}
	}
	if r.dataCopy {
		req.VolumeNode = "" // the data goes with the service
	}
	// A normal deploy keeps the nodes of blue when it can (08 section 4.2).
	if d.trigger != "move" && d.trigger != "failover" && len(r.blue) == count && count > 0 {
		if nodes, ok := r.sameNodes(ctx, req); ok {
			return nodes, nil
		}
	}
	nodes, err := c.schedNodes(ctx, c.DB, r.svc.orgID, keepIDs)
	if err != nil {
		return nil, fail(ReasonInternal, "read nodes: %v", err)
	}
	res, err := sched.Schedule(nodes, req)
	if err != nil {
		req.IgnoreCapacity = true
		var again *sched.Result
		if again, _ = sched.Schedule(nodes, req); again == nil {
			return nil, nodeError(err)
		}
		res = again
	}
	for _, w := range res.Warnings {
		c.event(ctx, d.id, map[string]any{"type": "log", "data": map[string]any{"stream": "deploy", "line": "scheduler: " + w}})
	}
	return res.NodeIDs, nil
}

// sameNodes returns the nodes of the blue replicas when each of them can take its replica again.
func (r *run) sameNodes(ctx context.Context, req sched.Request) ([]string, bool) {
	c := r.c
	nodes, err := c.schedNodes(ctx, c.DB, r.svc.orgID, ids(r.keep))
	if err != nil {
		return nil, false
	}
	byID := map[string]sched.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	req.Replicas = 1
	out := make([]string, 0, len(r.blue))
	for _, b := range r.blue {
		n, ok := byID[b.node]
		if !ok {
			return nil, false
		}
		// The old container keeps its room until the switch: blue and green must fit together (REQ-DEPLOY-010).
		if why := sched.Reject(n, req); why != "" {
			return nil, false
		}
		out = append(out, b.node)
		n.UsedCPUMillis += req.CPUMillis
		n.UsedMemoryBytes += req.MemoryBytes
		byID[b.node] = n
	}
	return out, true
}

// allocIP gives a placement a fixed address in the mesh subnet of its node: the first free one from .10 up to .250.
// The caller holds the lock of the node row, so two placements never get the same address.
func allocIP(ctx context.Context, q dbq, nodeID string) (string, error) {
	var subnet string
	if err := q.QueryRow(ctx, `SELECT host(network(mesh_subnet)) FROM nodes WHERE id = $1 AND mesh_subnet IS NOT NULL FOR UPDATE`, nodeID).Scan(&subnet); err != nil {
		return "", nil // a node without a mesh subnet: Docker gives the address
	}
	rows, err := q.Query(ctx, `SELECT host(ip) FROM placements WHERE node_id = $1 AND ip IS NOT NULL`, nodeID)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return "", err
		}
		used[s] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	var a, b, cc, dd int
	if _, err := fmt.Sscanf(subnet, "%d.%d.%d.%d", &a, &b, &cc, &dd); err != nil {
		return "", fmt.Errorf("mesh subnet %q: %w", subnet, err)
	}
	for last := 10; last <= 250; last++ {
		ip := fmt.Sprintf("%d.%d.%d.%d", a, b, cc, last)
		if !used[ip] {
			return ip, nil
		}
	}
	return "", errors.New("no free address in the subnet of the node")
}
