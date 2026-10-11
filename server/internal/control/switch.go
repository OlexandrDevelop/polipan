// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// ingressTarget is a public route of the service on an ingress node.
type ingressTarget struct {
	node, host, path string
	https            bool
}

// ingressTargets lists the routes of the service on the nodes that serve them. A node that is not
// connected is skipped: it gets the new config from its desired state when it returns (08 section 5.3).
func (c *Control) ingressTargets(ctx context.Context, serviceID string) ([]ingressTarget, error) {
	rows, err := c.DB.Query(ctx, `SELECT n.id, d.host, d.path_prefix, d.https FROM nodes n
		JOIN domains d ON d.service_id = $1 AND (d.ingress_node_ids IS NULL OR n.id = ANY(d.ingress_node_ids))
		WHERE n.state IN ('ready','cordoned','draining') AND (d.ingress_node_ids IS NOT NULL OR n.labels->>'ingress' = 'true')
		ORDER BY n.id, d.host`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ingressTarget
	for rows.Next() {
		var t ingressTarget
		if err := rows.Scan(&t.node, &t.host, &t.path, &t.https); err != nil {
			return nil, err
		}
		if c.Sender != nil && c.Sender.Connected(t.node) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func distinctNodes(ts []ingressTarget) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t.node] {
			seen[t.node] = true
			out = append(out, t.node)
		}
	}
	sort.Strings(out)
	return out
}

// switchAcks sends the complete ingress to every ingress node and waits until all acked (REQ-DEPLOY-101, 102).
func (r *run) switchAcks(ctx context.Context, name string) error {
	c := r.c
	targets, err := c.ingressTargets(ctx, r.d.serviceID)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, node := range distinctNodes(targets) {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			e := func() error {
				ds, err := c.DesiredState(ctx, node)
				if err != nil {
					return err
				}
				ack, err := c.sendCmd(ctx, node, r.d.id, "switch-"+name+"-"+node, &pb.Command{Body: &pb.Command_SwitchTraffic{
					SwitchTraffic: &pb.SwitchTraffic{Ingress: ds.Ingress, DesiredStateVersion: ds.Version}}}, c.SwitchAckTimeout)
				if err != nil {
					return fmt.Errorf("node %s did not ack the switch: %w", node, err)
				}
				if !ack.GetOk() {
					return fmt.Errorf("node %s: %s: %s", node, ack.GetErrorCode(), ack.GetErrorMessage())
				}
				return nil
			}()
			if e != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = e
				}
				mu.Unlock()
			}
		}(node)
	}
	wg.Wait()
	return firstErr
}

// commitSwitch makes the new containers live and the old ones draining, in one change of the desired state.
func (r *run) commitSwitch(ctx context.Context) error {
	c, d := r.c, r.d
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'live', updated_at = now() WHERE id = ANY($1)`, ids(r.green)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE deployments SET canary_routing = false WHERE id = $1`, d.id); err != nil {
			return nil, err
		}
		if d.strategy != "recreate" {
			if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'draining', updated_at = now() WHERE id = ANY($1)`, ids(r.blue)); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE services SET live_release_id = $2, desired_state = 'running', updated_at = now() WHERE id = $1`, d.serviceID, *d.releaseID); err != nil {
			return nil, err
		}
		if len(r.dead) > 0 {
			// The replicas of a failed node are gone for good. When the node returns, its desired state has no
			// workload of them, so reconcile removes the old containers (split-brain guard, REQ-NODE-012).
			if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE id = ANY($1)`, ids(r.dead)); err != nil {
				return nil, err
			}
		}
		if r.dataCopy {
			if _, err := tx.Exec(ctx, `UPDATE volumes SET node_id = $2 WHERE service_id = $1 AND kind <> 'tmpfs'`, d.serviceID, r.node); err != nil {
				return nil, err
			}
		}
		if d.trigger == "failover" {
			if _, err := tx.Exec(ctx, `UPDATE services SET failover_state = 'moved', failover_reason = NULL, failover_at = now() WHERE id = $1`, d.serviceID); err != nil {
				return nil, err
			}
		}
		return c.allNodeIDs(ctx, tx)
	})
}

func (r *run) stopped(ctx context.Context) *deployErr {
	return fail(ReasonInternal, "stopped: %v", ctx.Err())
}

// switchBlueGreen is the switching state of 08 section 5: all ingress nodes ack before blue is touched.
func (r *run) switchBlueGreen(ctx context.Context) *deployErr {
	r.c.lockCancel(r.ad)
	if ctx.Err() != nil { // a cancel that came before the lock
		return r.stopped(ctx)
	}
	if e := r.step(ctx, "switching", "traffic goes to the new containers", nil); e != nil {
		return e
	}
	if err := r.commitSwitch(ctx); err != nil {
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		return &deployErr{reason: ReasonSwitchFailed, detail: "switch: " + err.Error(), state: "rolled_back"}
	}
	r.committed = true
	return r.afterCommit(ctx, false)
}

// switchRecreate is switching and draining of a recreate: the new containers are the route already.
func (r *run) switchRecreate(ctx context.Context) *deployErr {
	r.c.lockCancel(r.ad)
	if ctx.Err() != nil {
		return r.stopped(ctx)
	}
	if e := r.step(ctx, "switching", "the new container is live", nil); e != nil {
		return e
	}
	if err := r.commitSwitch(ctx); err != nil {
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		return &deployErr{reason: ReasonSwitchFailed, detail: "switch: " + err.Error(), state: "rolled_back"}
	}
	r.committed = true
	return r.afterCommit(ctx, false)
}

// afterCommit waits for the acks, runs the post-switch check, drains and stops blue, and finishes. skipAck is
// set when a restarted server resumes in draining.
func (r *run) afterCommit(ctx context.Context, skipAck bool) *deployErr {
	c, d := r.c, r.d
	if !skipAck {
		if err := r.switchAcks(ctx, "forward"); err != nil {
			if ctx.Err() != nil {
				return r.stopped(ctx)
			}
			return r.revert(ctx, ReasonSwitchFailed, err.Error())
		}
		if e := r.step(ctx, "draining", fmt.Sprintf("draining the old containers for %d s", d.opts.DrainSeconds), map[string]any{"drain_seconds": d.opts.DrainSeconds}); e != nil {
			return e
		}
	}
	start := time.Now()
	if d.strategy != "recreate" && c.PostSwitchCheck > 0 {
		total, failed, err := r.probeRoutes(ctx, c.PostSwitchCheck)
		if ctx.Err() != nil {
			return r.stopped(ctx)
		}
		if err != nil {
			c.Log.Warn("post-switch check", "deployment", d.id, "err", err)
		}
		if total > 0 && float64(failed)/float64(total) > c.Max5xxRate {
			return r.revert(ctx, ReasonGate5xx, fmt.Sprintf("post-switch check: %d of %d requests failed", failed, total))
		}
	}
	if d.strategy != "recreate" && len(r.blue) > 0 {
		if wait := time.Duration(d.opts.DrainSeconds)*time.Second - time.Since(start); wait > 0 {
			select {
			case <-ctx.Done():
				return r.stopped(ctx)
			case <-time.After(wait):
			}
		}
		r.stopBlue(ctx)
	} else if d.strategy == "recreate" && d.trigger == "move" && len(r.blue) > 0 {
		r.stopBlue(ctx) // the old container was stopped at the start. A moved container is removed, not kept.
	}
	return r.finalize(ctx)
}

// probeRoutes sends requests to the public routes on all ingress nodes for the duration (REQ-DEPLOY-110).
func (r *run) probeRoutes(ctx context.Context, dur time.Duration) (total, failed int, err error) {
	c := r.c
	targets, err := c.ingressTargets(ctx, r.d.serviceID)
	if err != nil || len(targets) == 0 {
		return 0, 0, err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, t := range targets {
		wg.Add(1)
		go func(t ingressTarget) {
			defer wg.Done()
			ack, e := c.sendCmd(ctx, t.node, r.d.id, "probe-"+t.node+"-"+t.host+t.path, &pb.Command{Body: &pb.Command_ProbeRoute{
				ProbeRoute: &pb.ProbeRoute{Host: t.host, Path: t.path, Https: t.https, DurationSeconds: uint32((dur + time.Second - 1) / time.Second)}}}, dur+30*time.Second)
			if e != nil || !ack.GetOk() {
				return // the node did not probe: no result
			}
			var res struct{ Total, Failed int }
			if json.Unmarshal(ack.GetResultJson(), &res) != nil {
				return
			}
			mu.Lock()
			total += res.Total
			failed += res.Failed
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return total, failed, nil
}

// stopBlue stops the old containers with the stop grace period and keeps them (REQ-DEPLOY-009).
func (r *run) stopBlue(ctx context.Context) {
	c := r.c
	if r.d.trigger == "move" {
		// A moved replica is not kept: its container goes (the volume data stays on the old node, see orphan volumes).
		for _, b := range r.blue {
			c.removeContainer(ctx, b.node, b.id)
		}
		if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
			if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE id = ANY($1)`, ids(r.blue)); err != nil {
				return nil, err
			}
			return c.allNodeIDs(ctx, tx)
		}); err != nil {
			c.Log.Warn("remove the moved containers", "deployment", r.d.id, "err", err)
		}
		return
	}
	for _, b := range r.blue {
		r.stopOne(ctx, b)
	}
	if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'stopped', expires_at = NULL, updated_at = now() WHERE id = ANY($1)`, ids(r.blue)); err != nil {
			return nil, err
		}
		return nodesOf(r.blue), nil
	}); err != nil {
		c.Log.Warn("stop the old containers", "deployment", r.d.id, "err", err)
	}
}

// revert sends traffic back to blue, then stops green (state rolled_back).
func (r *run) revert(ctx context.Context, reason, detail string) *deployErr {
	c := r.c
	bg, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := c.change(bg, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(bg, `UPDATE placements SET role = 'live', updated_at = now() WHERE id = ANY($1)`, ids(r.blue)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(bg, `UPDATE services SET live_release_id = $2, updated_at = now() WHERE id = $1`, r.d.serviceID, r.d.prevReleaseID); err != nil {
			return nil, err
		}
		return c.allNodeIDs(bg, tx)
	})
	if err != nil {
		c.Log.Error("revert the switch", "deployment", r.d.id, "err", err)
	}
	if err := r.switchAcks(bg, "revert"); err != nil {
		c.Log.Warn("revert the switch: ack", "deployment", r.d.id, "err", err)
	}
	r.discardGreen(bg, false)
	r.reverted = true
	return &deployErr{reason: reason, detail: detail, state: "rolled_back"}
}

// finalize marks the deployment live, prunes old releases and runs post_deploy (a failure only warns).
func (r *run) finalize(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if e := r.step(ctx, "live", "deployment is live", map[string]any{"release_id": *d.releaseID}); e != nil {
		return e
	}
	c.settleDependents(ctx, d.id, "live")
	c.prune(ctx, r.svc, r.cfg)
	r.recordMove(ctx)
	if len(r.cfg.PostDeploy) > 0 && d.trigger != "move" && d.trigger != "failover" {
		if e := r.oneOff(ctx, "post_deploy", r.cfg.PostDeploy, "post_deploy_failed"); e != nil {
			warn, _ := json.Marshal([]map[string]string{{"code": "post_deploy_failed", "message": e.detail}})
			_, _ = c.DB.Exec(ctx, `UPDATE deployments SET warnings = warnings || $2::jsonb WHERE id = $1`, d.id, warn)
			c.Log.Warn("post_deploy failed", "deployment", d.id, "detail", e.detail)
		}
	}
	return nil
}
