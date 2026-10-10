// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/secrets"
)

// Volume is a volume of a service.
type Volume struct {
	ID             string
	ServiceID      string
	Name           string
	Kind           string
	MountPath      string
	NodeID         *string
	SizeLimitBytes *int64
	AccessMode     string
}

// DockerName is the name of the Docker volume on the node (Mount.source of a volume mount).
func (v Volume) DockerName() string { return VolumeDockerName(v.ID) }

// VolumeDockerName is the Docker volume name of a volume id.
func VolumeDockerName(id string) string { return "pp-" + strings.ToLower(id) }

const volCols = `v.id, v.service_id, v.name, v.kind, v.mount_path, v.node_id, v.size_limit_bytes, v.access_mode`

func scanVolume(row pgx.Row) (*Volume, error) {
	var v Volume
	if err := row.Scan(&v.ID, &v.ServiceID, &v.Name, &v.Kind, &v.MountPath, &v.NodeID, &v.SizeLimitBytes, &v.AccessMode); err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// ListVolumes lists the volumes of a service (the caller checks the service).
func (s *Store) ListVolumes(ctx context.Context, serviceID string) ([]Volume, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+volCols+` FROM volumes v WHERE v.service_id = $1 ORDER BY v.name, v.id`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Volume{}
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetVolume reads a volume of a service of the org that is not deleted.
func (s *Store) GetVolume(ctx context.Context, orgID, id string) (*Volume, error) {
	return scanVolume(s.Pool.QueryRow(ctx, `SELECT `+volCols+` FROM volumes v
		JOIN services s ON s.id = v.service_id JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		WHERE v.id = $1 AND p.org_id = $2`+svcWhere, id, orgID))
}

func volumeDiff(v *Volume) map[string]any {
	return map[string]any{"name": v.Name, "kind": v.Kind, "mount_path": v.MountPath, "size_limit_bytes": v.SizeLimitBytes, "access_mode": v.AccessMode}
}

// CreateVolume adds a volume. A volume (not tmpfs) is pinned to the node where the service runs now;
// if it runs nowhere, the first deployment pins it (REQ-VOL-003). A used name is ErrNameConflict.
func (s *Store) CreateVolume(ctx context.Context, svc *Service, v Volume, a Actor) (*Volume, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	v.ID = NewID("vol")
	if v.Kind != "tmpfs" {
		err := tx.QueryRow(ctx, `SELECT node_id FROM placements WHERE service_id = $1 ORDER BY (role = 'live') DESC, created_at DESC LIMIT 1`, svc.ID).Scan(&v.NodeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO volumes (id, service_id, name, kind, mount_path, node_id, size_limit_bytes, access_mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, v.ID, svc.ID, v.Name, v.Kind, v.MountPath, v.NodeID, v.SizeLimitBytes, v.AccessMode); err != nil {
		if u, _ := IsUnique(err); u {
			return nil, ErrNameConflict
		}
		return nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(svc.OrgID, svc.ProjectID, "volume.create", "volume", v.ID, map[string]any{"after": volumeDiff(&v)})); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetVolume(ctx, svc.OrgID, v.ID)
}

// UpdateVolume stores the mount path, the size limit and the access mode.
func (s *Store) UpdateVolume(ctx context.Context, svc *Service, before *Volume, v Volume, a Actor) (*Volume, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE volumes SET mount_path = $2, size_limit_bytes = $3, access_mode = $4 WHERE id = $1`,
		before.ID, v.MountPath, v.SizeLimitBytes, v.AccessMode); err != nil {
		return nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(svc.OrgID, svc.ProjectID, "volume.update", "volume", before.ID,
		map[string]any{"before": volumeDiff(before), "after": volumeDiff(&v)})); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetVolume(ctx, svc.OrgID, before.ID)
}

// DeleteVolume removes a volume from the service. The next deployment no longer mounts it.
func (s *Store) DeleteVolume(ctx context.Context, orgID, projectID string, v *Volume, deleteData bool, a Actor) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM volumes WHERE id = $1`, v.ID); err != nil {
		return err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "volume.delete", "volume", v.ID,
		map[string]any{"before": volumeDiff(v), "delete_data": deleteData})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// OrphanVolume is a volume of a deleted service that still has data on a node (REQ-VOL-004).
type OrphanVolume struct {
	ID          string
	Name        string // Docker volume name or bind sub path on the node
	LastService string
}

// orphanName is the name an orphan has on its node.
const orphanName = `CASE WHEN v.kind = 'bind' THEN v.service_id || '/' || v.name ELSE 'pp-' || lower(v.id) END`

// ListOrphanVolumes lists the volumes on a node of the org whose service was deleted.
func (s *Store) ListOrphanVolumes(ctx context.Context, orgID, nodeID string) ([]OrphanVolume, error) {
	rows, err := s.Pool.Query(ctx, `SELECT v.id, `+orphanName+`, split_part(s.name, '~deleted~', 1)
		FROM volumes v JOIN services s ON s.id = v.service_id JOIN nodes n ON n.id = v.node_id
		WHERE v.node_id = $1 AND n.org_id = $2 AND v.kind <> 'tmpfs' AND s.deleted_at IS NOT NULL
		ORDER BY v.id`, nodeID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrphanVolume{}
	for rows.Next() {
		var o OrphanVolume
		if err := rows.Scan(&o.ID, &o.Name, &o.LastService); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteOrphanVolume removes the record of an orphan volume of a node. It returns ErrNotFound when there is none.
func (s *Store) DeleteOrphanVolume(ctx context.Context, orgID, nodeID, name string, a Actor) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id, projectID string
	err = tx.QueryRow(ctx, `DELETE FROM volumes v USING services s, environments e, nodes n
		WHERE s.id = v.service_id AND e.id = s.environment_id AND n.id = v.node_id
		  AND v.node_id = $1 AND n.org_id = $2 AND s.deleted_at IS NOT NULL AND v.kind <> 'tmpfs' AND `+orphanName+` = $3
		RETURNING v.id, e.project_id`, nodeID, orgID, name).Scan(&id, &projectID)
	if err != nil {
		return notFound(err)
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "volume.delete_orphan", "volume", id,
		map[string]any{"before": map[string]any{"node_id": nodeID, "name": name}})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
