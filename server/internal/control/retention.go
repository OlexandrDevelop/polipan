// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
)

// prune keeps retain_stopped sets of stopped containers and retain_releases releases with their image (REQ-REL-006).
// Older stopped containers are removed. Older releases get pruned_at, and their image is removed from the nodes and the registry.
func (c *Control) prune(ctx context.Context, s *svcInfo, cfg serviceConfig) {
	if err := c.pruneStopped(ctx, s.id, cfg.retainStopped()); err != nil {
		c.Log.Warn("prune stopped containers", "service", s.id, "err", err)
	}
	var keep *int
	_ = c.DB.QueryRow(ctx, `SELECT NULLIF(settings->>'retain_releases','')::int FROM projects WHERE id = $1`, s.projectID).Scan(&keep)
	n := defaultRetainReleases
	if keep != nil && *keep > 0 {
		n = *keep
	}
	if err := c.pruneReleases(ctx, s, n); err != nil {
		c.Log.Warn("prune releases", "service", s.id, "err", err)
	}
}

// pruneStopped removes the kept stopped containers beyond keep sets, oldest release first.
func (c *Control) pruneStopped(ctx context.Context, serviceID string, keep int) error {
	rows, err := c.DB.Query(ctx, `SELECT p.id, p.node_id FROM placements p JOIN releases r ON r.id = p.release_id
		JOIN services s ON s.id = p.service_id
		WHERE p.service_id = $1 AND p.role = 'stopped' AND p.expires_at IS NULL AND r.id IS DISTINCT FROM s.live_release_id
		  AND p.release_id NOT IN (SELECT release_id FROM (SELECT DISTINCT p2.release_id, r2.number FROM placements p2 JOIN releases r2 ON r2.id = p2.release_id
		       WHERE p2.service_id = $1 AND p2.role = 'stopped' AND p2.expires_at IS NULL ORDER BY r2.number DESC LIMIT $2) k)`, serviceID, keep)
	if err != nil {
		return err
	}
	var old []plc
	for rows.Next() {
		var p plc
		if err := rows.Scan(&p.id, &p.node); err != nil {
			rows.Close()
			return err
		}
		old = append(old, p)
	}
	rows.Close()
	return c.deletePlacements(ctx, old)
}

func (c *Control) deletePlacements(ctx context.Context, list []plc) error {
	if len(list) == 0 {
		return nil
	}
	for _, p := range list {
		c.removeContainer(ctx, p.node, p.id)
	}
	return c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE id = ANY($1)`, ids(list)); err != nil {
			return nil, err
		}
		return nodesOf(list), nil
	})
}

// pruneReleases marks the releases beyond keep as pruned and removes their images.
func (c *Control) pruneReleases(ctx context.Context, s *svcInfo, keep int) error {
	// The newest keep releases keep their image. The live release is never pruned.
	rows, err := c.DB.Query(ctx, `SELECT id, image_ref FROM (
		SELECT r.id, r.image_ref, sv.live_release_id, row_number() OVER (ORDER BY r.number DESC) AS rn
		  FROM releases r JOIN services sv ON sv.id = r.service_id WHERE r.service_id = $1 AND r.pruned_at IS NULL) x
		WHERE rn > $2 AND id IS DISTINCT FROM live_release_id ORDER BY rn`, s.id, keep)
	if err != nil {
		return err
	}
	type rel struct{ id, ref string }
	var old []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.id, &r.ref); err != nil {
			rows.Close()
			return err
		}
		old = append(old, r)
	}
	rows.Close()
	if len(old) == 0 {
		return nil
	}
	var relIDs []string
	for _, r := range old {
		relIDs = append(relIDs, r.id)
	}
	stopped, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE release_id = ANY($1) AND role = 'stopped'`, relIDs)
	if err != nil {
		return err
	}
	if err := c.deletePlacements(ctx, stopped); err != nil {
		return err
	}
	if _, err := c.DB.Exec(ctx, `UPDATE releases SET pruned_at = now() WHERE id = ANY($1) AND pruned_at IS NULL`, relIDs); err != nil {
		return err
	}
	// Image GC only for images that no kept release uses: the same digest is in many releases (same commit deployed again).
	refs, err := queryStrings(ctx, c.DB, `SELECT DISTINCT image_ref FROM releases WHERE id = ANY($1)
		AND image_ref NOT IN (SELECT image_ref FROM releases WHERE pruned_at IS NULL)`, relIDs)
	if err != nil {
		return err
	}
	c.gcImages(ctx, refs)
	b, _ := json.Marshal(relIDs)
	c.Log.Info("releases pruned", "service", s.id, "releases", string(b))
	return nil
}

// sweepExpired removes the stopped containers of failed deployments after failed_green_keep.
func (c *Control) sweepExpired(ctx context.Context) {
	list, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE role = 'stopped' AND expires_at IS NOT NULL AND expires_at < $1`, c.Clock.Now())
	if err != nil || len(list) == 0 {
		return
	}
	if err := c.deletePlacements(ctx, list); err != nil && ctx.Err() == nil {
		c.Log.Warn("sweep failed containers", "err", err)
	}
}

// gcImages removes images from the nodes (a running image stays, Docker refuses) and the manifests from the registry.
func (c *Control) gcImages(ctx context.Context, refs []string) {
	if len(refs) == 0 {
		return
	}
	nodes, _ := queryStrings(ctx, c.DB, `SELECT id FROM nodes WHERE state = 'ready'`)
	for _, n := range nodes {
		if c.Sender == nil || !c.Sender.Connected(n) {
			continue
		}
		if _, err := c.sendCmd(ctx, n, gateway.NewID("gc"), "prune", &pb.Command{Body: &pb.Command_PruneImages{
			PruneImages: &pb.PruneImages{RemoveImageRefs: refs}}}, 2*60*1e9); err != nil {
			c.Log.Warn("image gc", "node", n, "err", err)
		}
	}
	if c.Build != nil && c.Build.Registry != nil {
		for _, ref := range refs {
			if err := c.Build.Registry.Delete(ctx, ref); err != nil {
				c.Log.Warn("registry gc", "ref", ref, "err", err)
			}
		}
	}
}

// RemoveServiceImages marks every release of the services as pruned and removes the images that no other release keeps.
// It is for the services of a deleted preview environment (REQ-PREV-007, orchestrator.ImageRemover).
func (c *Control) RemoveServiceImages(ctx context.Context, serviceIDs []string) error {
	if len(serviceIDs) == 0 {
		return nil
	}
	relIDs, err := queryStrings(ctx, c.DB, `SELECT id FROM releases WHERE service_id = ANY($1) AND pruned_at IS NULL`, serviceIDs)
	if err != nil || len(relIDs) == 0 {
		return err
	}
	if _, err := c.DB.Exec(ctx, `UPDATE releases SET pruned_at = now() WHERE id = ANY($1)`, relIDs); err != nil {
		return err
	}
	refs, err := queryStrings(ctx, c.DB, `SELECT DISTINCT image_ref FROM releases WHERE id = ANY($1)
		AND image_ref NOT IN (SELECT image_ref FROM releases WHERE pruned_at IS NULL)`, relIDs)
	if err != nil {
		return err
	}
	c.gcImages(ctx, refs)
	return nil
}
