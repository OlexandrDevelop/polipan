// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/vars"
)

// ReasonBatchStopped is the reason of a queued deployment of a promotion batch that did not start,
// because the deployment before it did not go live (REQ-PROMO-008).
const ReasonBatchStopped = "batch_stopped"

// usesStoredRelease tells if a deployment runs a Release that exists already: a rollback, a promotion, or
// a preview service that runs the Release of the base environment. No build runs for it (REQ-DEPLOY-103).
func usesStoredRelease(trigger string, hasRelease bool) bool {
	if !hasRelease {
		return false
	}
	switch trigger {
	case "rollback", "promotion", "pull_request":
		return true
	}
	return false
}

// checkPrepared reads the config of a prepared Release and checks it against the service.
// An image service cannot run an image of the built-in registry: only the build of one service may use it.
func (c *Control) checkPrepared(s *svcInfo, pr *orchestrator.PreparedRelease) (serviceConfig, error) {
	var cfg serviceConfig
	if err := json.Unmarshal(pr.Config, &cfg); err != nil {
		return cfg, err
	}
	if s.sourceType == "image" && c.Build != nil && c.Build.Registry.IsOwnImage(pr.ImageRef) {
		return cfg, orchestrator.ErrSourceMismatch
	}
	return cfg, nil
}

// storePrepared inserts the prepared Release of a service in the transaction of the deployment.
func (c *Control) storePrepared(ctx context.Context, tx pgx.Tx, s *svcInfo, pr *orchestrator.PreparedRelease, actorID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "rel:"+s.id); err != nil {
		return "", err
	}
	id := gateway.NewID("rel")
	src := map[string]any{"type": "image", "actor_id": actorID}
	for k, v := range pr.Source {
		src[k] = v
	}
	source, _ := json.Marshal(src)
	variables, _ := json.Marshal(pr.Variables)
	versions, _ := json.Marshal(pr.SecretVersions)
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source, created_by)
		 VALUES ($1, $2, (SELECT COALESCE(MAX(number),0)+1 FROM releases WHERE service_id = $2), $3, $4, $5, $6, $7, $8, $9)`,
		id, s.id, pr.ImageRef, pr.Digest, pr.Config, variables, versions, source, actor); err != nil {
		return "", err
	}
	snap := &vars.Snapshot{Scope: &vars.Scope{OrgID: s.orgID, ProjectID: s.projectID}, Secrets: map[string]vars.Entry{}}
	for _, u := range pr.SecretUses {
		snap.Secrets[u.Name] = vars.Entry{ID: u.ID, Name: u.Name, Version: u.Version, Secret: true}
	}
	if err := auditSecretUse(ctx, tx, snap, id, actorID); err != nil {
		return "", err
	}
	return id, nil
}

// settleDependents runs when a deployment reached a terminal state or was removed from the queue. The queued
// deployments of a promotion batch that wait for it start when it is live. In every other state they are
// cancelled, and so are the ones that wait for them (REQ-PROMO-008).
func (c *Control) settleDependents(ctx context.Context, depID, state string) {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	rows, err := c.DB.Query(ctx, `SELECT id, service_id FROM deployments WHERE after_deployment_id = $1 AND state = 'queued' ORDER BY created_at`, depID)
	if err != nil {
		c.Log.Warn("batch: read waiting deployments", "deployment", depID, "err", err)
		return
	}
	var waiting []plc
	for rows.Next() {
		var p plc
		if err := rows.Scan(&p.id, &p.node); err != nil { // node holds the service id here
			rows.Close()
			return
		}
		waiting = append(waiting, p)
	}
	rows.Close()
	for _, w := range waiting {
		if state == "live" {
			c.pump(w.node)
			continue
		}
		if c.cancelWaiting(ctx, w.id, depID, state) {
			c.settleDependents(ctx, w.id, "cancelled")
		}
	}
}

// cancelWaiting cancels a queued deployment of a batch. It returns false when it was not queued any more.
func (c *Control) cancelWaiting(ctx context.Context, id, before, beforeState string) bool {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE deployments SET state = 'cancelled', failure_reason = $2, failure_detail = $3, finished_at = now(), updated_at = now()
		WHERE id = $1 AND state = 'queued'`, id, ReasonBatchStopped, "the deployment "+before+" before it is "+beforeState+", so the batch stopped")
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	if err := c.addStep(ctx, tx, id, "cancelled", "the batch stopped: "+before+" is "+beforeState, map[string]any{"reason": ReasonBatchStopped}); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
