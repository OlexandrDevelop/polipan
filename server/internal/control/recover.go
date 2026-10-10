// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"

	"github.com/polipan/polipan/server/internal/orchestrator"
)

// recoverDeployments finishes the deployments that a former server run left active (REQ-DEPLOY-107).
// Before the switch the new containers are discarded and the deployment fails (server_restarted).
// After the switch is committed, the deployment goes on: drain, stop blue, live. A switch that was not
// committed is rolled back. Queued deployments start when their service is free.
func (c *Control) recoverDeployments(ctx context.Context) error {
	ids, err := queryStrings(ctx, c.DB, `SELECT id FROM deployments WHERE state IN (`+runningStates+`)`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c.mu.Lock()
		mine := c.active[id] != nil
		c.mu.Unlock()
		if !mine {
			c.startResume(id)
		}
	}
	queued, err := queryStrings(ctx, c.DB, `SELECT DISTINCT service_id FROM deployments WHERE state = 'queued'`)
	if err != nil {
		return err
	}
	for _, s := range queued {
		c.pump(s)
	}
	return nil
}

func (c *Control) startResume(id string) {
	rctx, cancel := context.WithCancelCause(c.runCtx)
	ad := &activeDep{cancel: cancel, locked: true}
	c.mu.Lock()
	c.active[id] = ad
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel(nil)
		var serviceID string
		defer func() {
			c.mu.Lock()
			delete(c.active, id)
			c.mu.Unlock()
			if serviceID != "" {
				c.pump(serviceID)
			}
		}()
		d, err := c.loadDeployment(rctx, id)
		if err != nil {
			c.Log.Error("recover: load deployment", "deployment", id, "err", err)
			return
		}
		serviceID = d.serviceID
		c.Log.Info("recovering deployment", "deployment", id, "state", d.state)
		c.runDeployment(rctx, ad, d, true)
	}()
}

// resume continues a deployment that a former server started.
func (r *run) resume(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if e := r.load(ctx); e != nil {
		return e
	}
	if d.releaseID != nil {
		var raw []byte
		var image string
		if err := c.DB.QueryRow(ctx, `SELECT config_snapshot, image_ref FROM releases WHERE id = $1`, *d.releaseID).Scan(&raw, &image); err == nil {
			var cfg serviceConfig
			if json.Unmarshal(raw, &cfg) == nil {
				r.cfg = cfg
			}
			r.releaseID, r.imageRef = *d.releaseID, image
		}
	}
	committed := d.releaseID != nil && r.svc.liveRelease != nil && *r.svc.liveRelease == *d.releaseID
	// Not switched yet. A recreate may have stopped the old containers: they are started again (also in switching).
	if !committed && d.state != "draining" && d.strategy == "recreate" && d.prevReleaseID != nil {
		old, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1 AND release_id = $2
			AND deployment_id IS DISTINCT FROM $3`, d.serviceID, *d.prevReleaseID, d.id)
		if err == nil && len(old) > 0 {
			r.blue, r.blueStopped = old, true
		}
	}
	switch {
	case d.state == "draining" || (d.state == "switching" && committed):
		// The old containers are in the database as draining (blue_green) or stopped (recreate).
		r.committed = true
		return r.afterCommit(ctx, d.state == "draining")
	case d.state == "switching":
		// Nothing was switched: blue still serves. The new containers are removed.
		return &deployErr{reason: ReasonServerRestarted, detail: "the server restarted before the switch was saved", state: "rolled_back"}
	}
	return &deployErr{reason: ReasonServerRestarted, detail: "the server restarted while the deployment was running", state: "failed"}
}

var _ = orchestrator.ErrNotFound
