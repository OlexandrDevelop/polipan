// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// This file lets the control side move the data of a volume to another node through a backup target
// (09 section 7 "Move a service" and section 9 "Failover"). The control package uses it as its DataMover.

// volumePolicies returns the enabled volume policies of a service, with the id of the volume of each.
func (s *Service) volumePolicies(ctx context.Context, serviceID string) ([]Policy, error) {
	ids, err := queryIDs(ctx, s.DB, `SELECT id FROM backup_policies WHERE service_id = $1 AND volume_id IS NOT NULL AND enabled ORDER BY id`, serviceID)
	if err != nil {
		return nil, err
	}
	var out []Policy
	for _, id := range ids {
		var org string
		if err := s.DB.QueryRow(ctx, `SELECT org_id FROM backup_policies WHERE id = $1`, id).Scan(&org); err != nil {
			return nil, err
		}
		p, err := s.GetPolicy(ctx, org, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// SnapshotVolumes makes a fresh backup of every volume of the service and waits for it. The service is stopped.
func (s *Service) SnapshotVolumes(ctx context.Context, serviceID string) error {
	list, err := s.volumePolicies(ctx, serviceID)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("the service has no backup policy for its volumes: make one, then move the service")
	}
	for i := range list {
		p := &list[i]
		run, err := s.queueRun(ctx, p, "safety")
		if err != nil {
			return err
		}
		s.executeRun(ctx, run.ID) // returns at once when the loop took the run first: then waitRun waits
		done, err := s.waitRun(ctx, p.OrgID, run.ID)
		if err != nil {
			return err
		}
		if done.State != "succeeded" {
			msg := "the backup failed"
			if done.Error != nil {
				msg += ": " + *done.Error
			}
			return errors.New(msg)
		}
	}
	return nil
}

// latestGood returns the last run of a policy that succeeded, and its first good snapshot.
func (s *Service) latestGood(ctx context.Context, p *Policy) (*Run, *Snapshot, error) {
	r, err := s.latestGoodRun(ctx, p.ID)
	if errors.Is(err, ErrNotFound) || (err == nil && r == nil) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for i := range r.Snapshots {
		if r.Snapshots[i].OK {
			return r, &r.Snapshots[i], nil
		}
	}
	return nil, nil, nil
}

// LatestBackup tells when the oldest of the last successful backups of the volumes of the service finished.
// Every local volume needs a volume policy with a successful run, else the answer is false.
func (s *Service) LatestBackup(ctx context.Context, serviceID string) (time.Time, bool, error) {
	list, err := s.volumePolicies(ctx, serviceID)
	if err != nil {
		return time.Time{}, false, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id FROM volumes WHERE service_id = $1 AND kind <> 'tmpfs'`, serviceID)
	if err != nil {
		return time.Time{}, false, err
	}
	var vols []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return time.Time{}, false, err
		}
		vols = append(vols, id)
	}
	rows.Close()
	if len(vols) == 0 {
		return time.Time{}, false, nil
	}
	var oldest time.Time
	for _, vol := range vols {
		var when *time.Time
		for i := range list {
			if list[i].VolumeID == nil || *list[i].VolumeID != vol {
				continue
			}
			r, sn, err := s.latestGood(ctx, &list[i])
			if err != nil {
				return time.Time{}, false, err
			}
			if r != nil && sn != nil && r.FinishedAt != nil && (when == nil || r.FinishedAt.After(*when)) {
				when = r.FinishedAt
			}
		}
		if when == nil {
			return time.Time{}, false, nil
		}
		if oldest.IsZero() || when.Before(oldest) {
			oldest = *when
		}
	}
	return oldest, true, nil
}

// RestoreVolumes loads the latest good snapshot of every volume of the service into the volume on the node.
// The container does not exist there yet, so the agent only writes the files.
func (s *Service) RestoreVolumes(ctx context.Context, serviceID, nodeID string) error {
	list, err := s.volumePolicies(ctx, serviceID)
	if err != nil {
		return err
	}
	if s.Sender == nil || !s.Sender.Connected(nodeID) {
		return fmt.Errorf("node %s is not connected", nodeID)
	}
	restored := 0
	for i := range list {
		p := &list[i]
		run, sn, err := s.latestGood(ctx, p)
		if err != nil {
			return err
		}
		if run == nil || sn == nil {
			return fmt.Errorf("policy %s has no successful backup", p.ID)
		}
		t, err := s.GetTarget(ctx, p.OrgID, sn.TargetID)
		if err != nil {
			return err
		}
		spec, err := s.spec(ctx, t, p.ID)
		if err != nil {
			return err
		}
		password, err := s.secretValue(ctx, p.RepoPasswordSecretID)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, s.RunTimeout)
		ack, err := s.Sender.Send(cctx, nodeID, &pb.Command{CommandId: gateway.NewID("cmd"), Body: &pb.Command_RestoreSnapshot{RestoreSnapshot: &pb.RestoreSnapshot{
			RestoreId: store.NewID("brs"), ServiceId: serviceID, Target: spec, RepoPassword: password, SnapshotId: sn.SnapshotID,
			Source: &pb.BackupSource{Engine: "volume", VolumeName: store.VolumeDockerName(*p.VolumeID)}}}})
		cancel()
		if err != nil {
			return err
		}
		if !ack.GetOk() {
			return fmt.Errorf("%s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
		}
		restored++
	}
	if restored == 0 {
		return errors.New("the service has no backup policy for its volumes")
	}
	return nil
}
