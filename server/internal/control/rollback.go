// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
)

// releaseRow is an older release that a rollback goes back to.
type releaseRow struct {
	id, imageRef, digest string
	number               int
	cfg                  serviceConfig
}

// checkRollback checks a rollback before anything is queued or started (08 section 9, REQ-REL-008, REQ-REL-012).
func (c *Control) checkRollback(ctx context.Context, s *svcInfo, p orchestrator.DeployParams) (*releaseRow, error) {
	var rel releaseRow
	var raw []byte
	var pruned any
	err := c.DB.QueryRow(ctx, `SELECT id, image_ref, image_digest, number, config_snapshot, pruned_at FROM releases WHERE id = $1 AND service_id = $2`,
		p.RollbackReleaseID, s.id).Scan(&rel.id, &rel.imageRef, &rel.digest, &rel.number, &raw, &pruned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, orchestrator.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if s.liveRelease != nil && *s.liveRelease == rel.id {
		return nil, orchestrator.ErrAlreadyLive
	}
	if pruned != nil {
		return nil, orchestrator.ErrImagePruned
	}
	if err := json.Unmarshal(raw, &rel.cfg); err != nil {
		return nil, err
	}
	// A destroyed secret version stops the rollback before anything starts.
	refs, err := secrets.ReleaseRefs(ctx, c.DB, rel.id)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		var destroyed bool
		if err := c.DB.QueryRow(ctx, `SELECT destroyed_at IS NOT NULL FROM secret_versions WHERE secret_id = $1 AND version = $2`,
			ref.SecretID, ref.Version).Scan(&destroyed); err != nil || destroyed {
			return nil, orchestrator.ErrSecretDestroyed
		}
	}
	// A domain that points to a port the old release does not expose needs force (edge case of F02).
	if !p.Force && rel.cfg.Port != 0 {
		var mismatch bool
		if err := c.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE service_id = $1 AND container_port <> $2)`, s.id, rel.cfg.Port).Scan(&mismatch); err != nil {
			return nil, err
		}
		if mismatch {
			return nil, orchestrator.ErrDomainPortMismatch
		}
	}
	return &rel, nil
}

// prepareRollback is the start of a rollback. No build runs and no release is made (REQ-DEPLOY-103).
// When the kept stopped containers of the release still exist, they are used (fast rollback, REQ-REL-004).
func (r *run) prepareRollback(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	var raw []byte
	if err := c.DB.QueryRow(ctx, `SELECT image_ref, config_snapshot FROM releases WHERE id = $1`, *d.releaseID).Scan(&r.imageRef, &raw); err != nil {
		return fail(ReasonInvalidConfig, "release not found: %v", err)
	}
	if err := json.Unmarshal(raw, &r.cfg); err != nil {
		return fail(ReasonInvalidConfig, "release config: %v", err)
	}
	r.releaseID = *d.releaseID
	// Same rule as a normal deploy: an image service never pulls images of the built-in registry.
	if r.svc.sourceType == "image" && c.Build != nil && c.Build.Registry.IsOwnImage(r.imageRef) {
		return fail(ReasonInvalidConfig, "an image service cannot use an image of the built-in registry")
	}
	kept, err := c.placementList(ctx, `SELECT p.id, p.node_id FROM placements p JOIN nodes n ON n.id = p.node_id
		WHERE p.service_id = $1 AND p.release_id = $2 AND p.role = 'stopped' AND p.expires_at IS NULL AND p.container_id IS NOT NULL
		  AND n.state = 'ready' ORDER BY p.replica_index, p.id`, d.serviceID, r.releaseID)
	if err != nil {
		return fail(ReasonInternal, "read placements: %v", err)
	}
	if len(kept) > 0 && c.Sender != nil && c.Sender.Connected(kept[0].node) {
		r.fast, r.green, r.node = true, kept[:1], kept[0].node
		return r.checkCapacity(ctx)
	}
	node, err := r.pickNodeFor(ctx)
	if err != nil {
		return fail(ReasonNodeUnavailable, "no ready node (or the node of the volumes is not ready)")
	}
	r.node = node
	if c.Sender == nil || !c.Sender.Connected(node) {
		return fail(ReasonNodeUnavailable, "node %s is not connected", node)
	}
	if e := r.checkCapacity(ctx); e != nil {
		return e
	}
	if _, err := c.DB.Exec(ctx, `UPDATE volumes SET node_id = $2 WHERE service_id = $1 AND node_id IS NULL AND kind <> 'tmpfs'`, d.serviceID, node); err != nil {
		return fail(ReasonInvalidConfig, "pin volumes: %v", err)
	}
	if _, _, e := r.pull(ctx, r.imageRef); e != nil {
		return e
	}
	return nil
}
