// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"
)

// UpdateNode changes the name or the labels of a node. A nil value is not changed.
func (s *Store) UpdateNode(ctx context.Context, orgID, id string, name *string, labels map[string]string) (*Node, error) {
	var lb any
	if labels != nil {
		raw, _ := json.Marshal(labels)
		lb = raw
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE nodes SET name = COALESCE($3, name), labels = COALESCE($4::jsonb, labels), updated_at = now()
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID, name, lb)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetNode(ctx, orgID, id)
}

// NodeWorkload is a placement on a node, for the node page.
type NodeWorkload struct {
	PlacementID   string
	ServiceID     string
	ServiceName   string
	EnvironmentID string
	Role          string
	Health        string
}

// NodeWorkloads lists the placements on a node (the node page, UI-40).
func (s *Store) NodeWorkloads(ctx context.Context, nodeID string) ([]NodeWorkload, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.service_id, s.name, s.environment_id, p.role, p.health
		FROM placements p JOIN services s ON s.id = p.service_id WHERE p.node_id = $1 AND s.deleted_at IS NULL ORDER BY s.name, p.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeWorkload{}
	for rows.Next() {
		var w NodeWorkload
		if err := rows.Scan(&w.PlacementID, &w.ServiceID, &w.ServiceName, &w.EnvironmentID, &w.Role, &w.Health); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// FailoverInfo is the failover state of a service and the time of its last backup.
type FailoverInfo struct {
	State         string
	Reason        string
	DataLostSince *time.Time
	At            *time.Time
	LastBackupAt  *time.Time
	HasVolume     bool
}

// GetFailoverInfo reads the failover state of a service (REQ-NODE-007).
func (s *Store) GetFailoverInfo(ctx context.Context, serviceID string) (*FailoverInfo, error) {
	var f FailoverInfo
	err := s.Pool.QueryRow(ctx, `SELECT failover_state, COALESCE(failover_reason,''), failover_data_lost_since, failover_at,
		(SELECT max(r.finished_at) FROM backup_runs r JOIN backup_policies p ON p.id = r.policy_id WHERE p.service_id = s.id AND r.state = 'succeeded'),
		EXISTS (SELECT 1 FROM volumes v WHERE v.service_id = s.id AND v.kind <> 'tmpfs')
		FROM services s WHERE s.id = $1`, serviceID).Scan(&f.State, &f.Reason, &f.DataLostSince, &f.At, &f.LastBackupAt, &f.HasVolume)
	if err != nil {
		return nil, notFound(err)
	}
	return &f, nil
}
