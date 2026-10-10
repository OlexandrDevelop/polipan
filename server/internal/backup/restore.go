// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// Restore is a restore of a snapshot (REQ-BKP-007).
type Restore struct {
	ID         string
	RunID      string
	TargetID   string
	Mode       string
	ServiceID  *string
	State      string
	Error      *string
	CreatedBy  *string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// RestoreInput is a restore request.
type RestoreInput struct {
	TargetID       string
	Mode           string // in_place | new_service
	NewServiceName string
	Confirm        string // in_place: the name of the service
	ActorID        string
}

const restoreCols = `id, run_id, target_id, mode, service_id, state, error, created_by, created_at, finished_at`

func scanRestore(row pgx.Row) (*Restore, error) {
	var r Restore
	if err := row.Scan(&r.ID, &r.RunID, &r.TargetID, &r.Mode, &r.ServiceID, &r.State, &r.Error, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// GetRestore reads a restore of the org.
func (s *Service) GetRestore(ctx context.Context, orgID, id string) (*Restore, error) {
	return scanRestore(s.DB.QueryRow(ctx, `SELECT `+restoreCols+` FROM backup_restores WHERE id = $1 AND run_id IN
		(SELECT r.id FROM backup_runs r JOIN backup_policies p ON p.id = r.policy_id WHERE p.org_id = $2)`, id, orgID))
}

var serviceNameRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// StartRestore checks a restore request, makes the new service when the mode needs one, and queues the job.
func (s *Service) StartRestore(ctx context.Context, orgID, runID string, in RestoreInput) (*Restore, error) {
	run, err := s.GetRun(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	p, err := s.GetPolicy(ctx, orgID, run.PolicyID)
	if err != nil {
		return nil, err
	}
	var snap *Snapshot
	for i := range run.Snapshots {
		if run.Snapshots[i].TargetID == in.TargetID && run.Snapshots[i].OK {
			snap = &run.Snapshots[i]
		}
	}
	if snap == nil {
		return nil, invalid("target_id", "the run has no snapshot on this target")
	}
	engine, err := s.engineOf(ctx, p)
	if err != nil {
		return nil, err
	}
	if engine == "controlplane" {
		return nil, fmt.Errorf("%w: the control-plane database is restored by hand, see docs/operations/backups.md", ErrNotRestorable)
	}
	var svcName, envID string
	if err := s.DB.QueryRow(ctx, `SELECT name, environment_id FROM services WHERE id = $1 AND deleted_at IS NULL`, *p.ServiceID).Scan(&svcName, &envID); err != nil {
		return nil, fmt.Errorf("%w: the service of this policy does not exist any more", ErrNotRestorable)
	}
	restoreID := store.NewID("brs")
	var created *string
	if in.ActorID != "" {
		created = &in.ActorID
	}
	switch in.Mode {
	case "in_place":
		if in.Confirm != svcName {
			return nil, invalid("confirm", "confirm must be the name of the service (%s)", svcName)
		}
		ok, err := s.stable(ctx, p)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDeployRunning
		}
		var busy bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_restores WHERE service_id = $1 AND state IN ('queued','running'))`, *p.ServiceID).Scan(&busy); err != nil {
			return nil, err
		}
		if busy {
			return nil, ErrAlreadyRunning
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO backup_restores (id, run_id, target_id, mode, service_id, state, created_by) VALUES ($1,$2,$3,'in_place',$4,'queued',$5)`,
			restoreID, runID, in.TargetID, *p.ServiceID, created); err != nil {
			return nil, err
		}
	case "new_service":
		if engine == "volume" {
			return nil, invalid("mode", "a volume of an app can only be restored in place. A clone is for databases")
		}
		name := in.NewServiceName
		if name == "" {
			name = svcName + "-restore"
		}
		if !serviceNameRe.MatchString(name) {
			return nil, invalid("new_service_name", "new_service_name must use lower case letters, digits and dashes (1 to 40 characters)")
		}
		newID, err := s.createClone(ctx, orgID, *p.ServiceID, envID, name, in.ActorID)
		if err != nil {
			return nil, err
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO backup_restores (id, run_id, target_id, mode, service_id, state, created_by) VALUES ($1,$2,$3,'new_service',$4,'queued',$5)`,
			restoreID, runID, in.TargetID, newID, created); err != nil {
			return nil, err
		}
	default:
		return nil, invalid("mode", "mode must be in_place or new_service")
	}
	s.event(ctx, orgID, p.ID, map[string]any{"type": "restore", "restore_id": restoreID, "state": "queued"})
	go s.kick()
	return scanRestore(s.DB.QueryRow(ctx, `SELECT `+restoreCols+` FROM backup_restores WHERE id = $1`, restoreID))
}

// createClone makes a new database service like the source one, with its own new password (the original is not changed).
func (s *Service) createClone(ctx context.Context, orgID, sourceID, envID, name, actorID string) (string, error) {
	var engineName, version *string
	var cfgRaw []byte
	if err := s.DB.QueryRow(ctx, `SELECT db_engine, db_version, config FROM services WHERE id = $1`, sourceID).Scan(&engineName, &version, &cfgRaw); err != nil {
		return "", notFound(err)
	}
	eng := dbengine.Get(deref(engineName))
	if eng == nil {
		return "", invalid("mode", "only a database can be restored into a new service")
	}
	var cfg struct {
		Image     string         `json:"image"`
		Resources map[string]any `json:"resources"`
	}
	_ = json.Unmarshal(cfgRaw, &cfg)
	tag := eng.TagOf(cfg.Image)
	spec, err := eng.NewService(tag, "")
	if err != nil {
		return "", err
	}
	if cfg.Resources != nil {
		spec.Config["resources"] = cfg.Resources
	}
	raw, _ := json.Marshal(spec.Config)
	if s.Secrets == nil {
		return "", errors.New("the secrets store is not set up")
	}
	ids, err := s.store.CreateServices(ctx, s.Secrets.Provider, orgID, envID, []store.ServiceInput{{
		Name: name, Type: "database", DBEngine: eng.Name, DBVersion: spec.Version, Config: raw, Env: spec.Env,
		Volumes: []store.Volume{{Name: "data", Kind: "volume", MountPath: spec.DataPath, AccessMode: "single_writer"}}}}, nil, store.Actor{UserID: actorID})
	if errors.Is(err, store.ErrServiceNameUsed) {
		return "", fmt.Errorf("%w: a service with this name exists in the environment", ErrConflict)
	}
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) finishRestore(ctx context.Context, orgID, policyID, id, state, msg string) {
	var errMsg *string
	if msg != "" {
		errMsg = &msg
	}
	if _, err := s.DB.Exec(ctx, `UPDATE backup_restores SET state = $2, error = $3, finished_at = now() WHERE id = $1`, id, state, errMsg); err != nil {
		s.Log.Error("restore not saved", "restore", id, "error", err)
	}
	s.event(ctx, orgID, policyID, map[string]any{"type": "restore", "restore_id": id, "state": state})
}

// executeRestore does the restore: a safety backup first (in place), then the agent loads the snapshot.
func (s *Service) executeRestore(ctx context.Context, id string) {
	var runID, mode string
	var serviceID *string
	if err := s.DB.QueryRow(ctx, `UPDATE backup_restores SET state = 'running' WHERE id = $1 AND state = 'queued' RETURNING run_id, mode, service_id`, id).Scan(&runID, &mode, &serviceID); err != nil {
		return
	}
	r, err := scanRestore(s.DB.QueryRow(ctx, `SELECT `+restoreCols+` FROM backup_restores WHERE id = $1`, id))
	if err != nil {
		return
	}
	var orgID, policyID string
	if err := s.DB.QueryRow(ctx, `SELECT p.org_id, p.id FROM backup_runs r JOIN backup_policies p ON p.id = r.policy_id WHERE r.id = $1`, runID).Scan(&orgID, &policyID); err != nil {
		s.finishRestore(ctx, "", "", id, "failed", err.Error())
		return
	}
	fail := func(format string, a ...any) {
		s.finishRestore(ctx, orgID, policyID, id, "failed", fmt.Sprintf(format, a...))
	}
	p, err := s.GetPolicy(ctx, orgID, policyID)
	if err != nil {
		fail("%v", err)
		return
	}
	run, err := s.GetRun(ctx, orgID, runID)
	if err != nil {
		fail("%v", err)
		return
	}
	var snapshotID string
	for _, sn := range run.Snapshots {
		if sn.TargetID == r.TargetID {
			snapshotID = sn.SnapshotID
		}
	}
	engine, err := s.engineOf(ctx, p)
	if err != nil {
		fail("%v", err)
		return
	}
	target := &Policy{ID: p.ID, OrgID: orgID, ServiceID: serviceID, VolumeID: p.VolumeID, Hooks: Hooks{}}
	if mode == "in_place" {
		// The safety backup comes first (REQ-BKP-007): the data of now is a snapshot too.
		safety, err := s.queueRun(ctx, p, "safety")
		if err != nil {
			fail("the safety backup was not made: %v", err)
			return
		}
		s.executeRun(ctx, safety.ID) // returns at once when the loop took the run first: then we wait for it below
		if done, err := s.waitRun(ctx, orgID, safety.ID); err != nil || done.State != "succeeded" {
			msg := "the safety backup failed"
			if err == nil && done.Error != nil {
				msg += ": " + *done.Error
			}
			fail("%s. Nothing was restored", msg)
			return
		}
	} else if err := s.deployAndWait(ctx, *serviceID, r.CreatedBy); err != nil {
		fail("the new service did not start: %v", err)
		return
	}
	node, src, err := s.dataNode(ctx, target)
	if err != nil {
		fail("%v", err)
		return
	}
	if engine == "volume" || engine == "redis" {
		// The agent stops the container while it writes the files and starts it again.
		if src.Container == "" {
			if c, err := s.liveContainer(ctx, *p.ServiceID); err == nil {
				src.Container = c
			}
		}
	}
	if s.Sender == nil || !s.Sender.Connected(node) {
		fail("node_unavailable: the node that holds the data is not connected")
		return
	}
	password, err := s.secretValue(ctx, p.RepoPasswordSecretID)
	if err != nil {
		fail("%v", err)
		return
	}
	t, err := s.GetTarget(ctx, orgID, r.TargetID)
	if err != nil {
		fail("%v", err)
		return
	}
	spec, err := s.spec(ctx, t, p.ID)
	if err != nil {
		fail("%v", err)
		return
	}
	src.PreHook, src.PostHook = nil, nil
	cctx, cancel := context.WithTimeout(ctx, s.RunTimeout)
	defer cancel()
	ack, err := s.Sender.Send(cctx, node, &pb.Command{CommandId: gateway.NewID("cmd"), Body: &pb.Command_RestoreSnapshot{RestoreSnapshot: &pb.RestoreSnapshot{
		RestoreId: id, Source: src, Target: spec, RepoPassword: password, SnapshotId: snapshotID, StopContainer: engine == "volume" || engine == "redis"}}})
	switch {
	case err != nil:
		fail("node_unavailable: %v", err)
	case !ack.GetOk():
		fail("%s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
	default:
		s.finishRestore(ctx, orgID, policyID, id, "succeeded", "")
	}
}

// liveContainer returns the container name of the live placement of a service.
func (s *Service) liveContainer(ctx context.Context, serviceID string) (string, error) {
	var placement string
	if err := s.DB.QueryRow(ctx, `SELECT id FROM placements WHERE service_id = $1 AND role = 'live' ORDER BY replica_index, id LIMIT 1`, serviceID).Scan(&placement); err != nil {
		return "", err
	}
	return "pp-" + placement, nil
}

// deployAndWait deploys the new service and waits until its deployment is live (10 minutes at most).
func (s *Service) deployAndWait(ctx context.Context, serviceID string, actor *string) error {
	if s.Orch == nil {
		return errors.New("no deploy engine")
	}
	by := ""
	if actor != nil {
		by = *actor
	}
	depID, err := s.Orch.Deploy(ctx, serviceID, by, "api")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var state string
		var reason *string
		if err := s.DB.QueryRow(ctx, `SELECT state, failure_reason FROM deployments WHERE id = $1`, depID).Scan(&state, &reason); err != nil {
			return err
		}
		switch state {
		case "live":
			return nil
		case "failed", "cancelled", "rolled_back":
			if reason != nil {
				return fmt.Errorf("the deployment %s: %s", state, *reason)
			}
			return fmt.Errorf("the deployment %s", state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("the deployment did not end in 10 minutes")
}

// waitRun waits until a run has ended (the loop may run it in another goroutine).
func (s *Service) waitRun(ctx context.Context, orgID, id string) (*Run, error) {
	deadline := time.Now().Add(s.RunTimeout + time.Minute)
	for {
		r, err := s.GetRun(ctx, orgID, id)
		if err != nil || r.State == "succeeded" || r.State == "failed" {
			return r, err
		}
		if time.Now().After(deadline) {
			return r, errors.New("the run did not end in time")
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
