// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// Snapshot is the result of one target of a run (REQ-BKP-008).
type Snapshot struct {
	TargetID   string `json:"target_id"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

// Run is a backup run.
type Run struct {
	ID         string
	PolicyID   string
	State      string
	Trigger    string
	Snapshots  []Snapshot
	Error      *string
	Manifest   json.RawMessage
	NodeID     *string
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
}

// DurationMS is the time the run took.
func (r *Run) DurationMS() *int {
	if r.StartedAt == nil || r.FinishedAt == nil {
		return nil
	}
	d := int(r.FinishedAt.Sub(*r.StartedAt).Milliseconds())
	return &d
}

const runCols = `id, policy_id, state, trigger, snapshots, error, manifest, node_id, started_at, finished_at, created_at`

func scanRun(row pgx.Row) (*Run, error) {
	var r Run
	var snaps []byte
	if err := row.Scan(&r.ID, &r.PolicyID, &r.State, &r.Trigger, &snaps, &r.Error, &r.Manifest, &r.NodeID, &r.StartedAt, &r.FinishedAt, &r.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(snaps, &r.Snapshots)
	return &r, nil
}

// GetRun reads a run of the org.
func (s *Service) GetRun(ctx context.Context, orgID, id string) (*Run, error) {
	return scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM backup_runs WHERE id = $1 AND policy_id IN (SELECT id FROM backup_policies WHERE org_id = $2)`, id, orgID))
}

func (s *Service) lastRun(ctx context.Context, policyID, state string) (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM backup_runs WHERE policy_id = $1 AND ($2 = '' OR state = $2)
		AND state IN ('succeeded','failed','running','queued') ORDER BY created_at DESC, id DESC LIMIT 1`, policyID, state))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return r, err
}

// ListRuns lists the runs of a policy, newest first. It returns up to limit+1 rows.
func (s *Service) ListRuns(ctx context.Context, orgID, policyID string, limit int, after *store.Cursor) ([]Run, error) {
	if _, err := s.GetPolicy(ctx, orgID, policyID); err != nil {
		return nil, err
	}
	var t *time.Time
	var id string
	if after != nil {
		t, id = &after.Time, after.ID
	}
	rows, err := s.DB.Query(ctx, `SELECT `+runCols+` FROM backup_runs WHERE policy_id = $1 AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC LIMIT $4`, policyID, t, id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RunNow queues a manual run (REQ-BKP-006).
func (s *Service) RunNow(ctx context.Context, orgID, policyID string) (*Run, error) {
	p, err := s.GetPolicy(ctx, orgID, policyID)
	if err != nil {
		return nil, err
	}
	r, err := s.queueRun(ctx, p, "manual")
	if err != nil {
		return nil, err
	}
	go s.kick() // do not wait for the next tick
	return r, nil
}

func (s *Service) queueRun(ctx context.Context, p *Policy, trigger string) (*Run, error) {
	id := store.NewID("bkr")
	if _, err := s.DB.Exec(ctx, `INSERT INTO backup_runs (id, policy_id, state, trigger) VALUES ($1,$2,'queued',$3)`, id, p.ID, trigger); err != nil {
		return nil, err
	}
	s.event(ctx, p.OrgID, p.ID, map[string]any{"type": "run", "run_id": id, "state": "queued"})
	return scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM backup_runs WHERE id = $1`, id))
}

// dataNode returns the node that holds the data of the policy and the container or volume to read. For the
// control-plane policy it returns no node: the run tries the nodes (the data is in a container that the agent finds).
func (s *Service) dataNode(ctx context.Context, p *Policy) (node string, source *pb.BackupSource, err error) {
	engine, err := s.engineOf(ctx, p)
	if err != nil {
		return "", nil, err
	}
	src := &pb.BackupSource{Engine: engine, PreHook: p.Hooks.Pre, PostHook: p.Hooks.Post}
	switch engine {
	case "controlplane":
		src.Container = "label:com.docker.compose.service=postgres"
		n, err := s.anyNode(ctx, p.OrgID, "")
		return n, src, err
	case "volume":
		var nodeID *string
		var vol string
		if err := s.DB.QueryRow(ctx, `SELECT node_id, id FROM volumes WHERE id = $1`, *p.VolumeID).Scan(&nodeID, &vol); err != nil {
			return "", nil, notFound(err)
		}
		if nodeID == nil {
			return "", nil, fmt.Errorf("the volume is not placed on a node yet")
		}
		src.VolumeName = store.VolumeDockerName(vol)
		return *nodeID, src, nil
	}
	var placement, nodeID string
	err = s.DB.QueryRow(ctx, `SELECT p.id, p.node_id FROM placements p WHERE p.service_id = $1 AND p.role = 'live' ORDER BY p.replica_index, p.id LIMIT 1`, *p.ServiceID).Scan(&placement, &nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("the service has no live container")
	}
	if err != nil {
		return "", nil, err
	}
	src.Container = control.ContainerName(placement)
	if engine == "redis" {
		var vol string
		if err := s.DB.QueryRow(ctx, `SELECT id FROM volumes WHERE service_id = $1 AND name = 'data'`, *p.ServiceID).Scan(&vol); err == nil {
			src.VolumeName = store.VolumeDockerName(vol)
		}
	}
	return nodeID, src, nil
}

// targetSpecs builds the agent targets of a policy.
func (s *Service) targetSpecs(ctx context.Context, p *Policy) ([]*pb.BackupTargetSpec, error) {
	var out []*pb.BackupTargetSpec
	for _, id := range p.TargetIDs {
		t, err := s.GetTarget(ctx, p.OrgID, id)
		if err != nil {
			return nil, err
		}
		sp, err := s.spec(ctx, t, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}

// failRun ends a run as failed and raises the alert backup_failed (REQ-BKP-009, AC-BKP-02).
func (s *Service) failRun(ctx context.Context, p *Policy, runID, node, msg string, snaps []Snapshot) {
	snapJSON, _ := json.Marshal(snaps)
	var nodePtr *string
	if node != "" {
		nodePtr = &node
	}
	if _, err := s.DB.Exec(ctx, `UPDATE backup_runs SET state = 'failed', error = $2, snapshots = $3, node_id = COALESCE($4, node_id),
		started_at = COALESCE(started_at, now()), finished_at = now() WHERE id = $1`, runID, msg, snapJSON, nodePtr); err != nil {
		s.Log.Error("backup run not saved", "run", runID, "error", err)
	}
	s.event(ctx, p.OrgID, p.ID, map[string]any{"type": "run", "run_id": runID, "state": "failed"})
	s.alert(ctx, p.OrgID, "backup_failed", "critical", p.ID, map[string]any{"run_id": runID, "error": msg, "service_id": p.ServiceID})
}

// stable tells if the service has no deployment that is still running (REQ edge case: wait for a stable placement).
func (s *Service) stable(ctx context.Context, p *Policy) (bool, error) {
	if p.ServiceID == nil {
		return true, nil
	}
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM deployments WHERE service_id = $1 AND state IN ('queued','building','image_ready','starting','health_checking','shifting','switching','draining')
		AND created_at > now() - make_interval(secs => $2)`, *p.ServiceID, s.StableWait.Seconds()).Scan(&n)
	return n == 0, err
}

// executeRun runs one backup on the node and records the result. The run must be queued.
func (s *Service) executeRun(ctx context.Context, runID string) {
	var policyID string
	var trigger string
	if err := s.DB.QueryRow(ctx, `UPDATE backup_runs SET state = 'running', started_at = now() WHERE id = $1 AND state = 'queued' RETURNING policy_id, trigger`, runID).Scan(&policyID, &trigger); err != nil {
		return // taken by another process
	}
	var orgID string
	if err := s.DB.QueryRow(ctx, `SELECT org_id FROM backup_policies WHERE id = $1`, policyID).Scan(&orgID); err != nil {
		s.Log.Error("backup run: policy", "run", runID, "error", err)
		return
	}
	p, err := s.GetPolicy(ctx, orgID, policyID)
	if err != nil {
		return
	}
	s.event(ctx, orgID, p.ID, map[string]any{"type": "run", "run_id": runID, "state": "running"})
	node, src, err := s.dataNode(ctx, p)
	if err != nil {
		code := "node_unavailable"
		if !errors.Is(err, ErrNoNode) {
			code = "no_data"
		}
		s.failRun(ctx, p, runID, "", code+": "+err.Error(), nil)
		return
	}
	if s.Sender == nil || !s.Sender.Connected(node) {
		s.failRun(ctx, p, runID, node, "node_unavailable: the node that holds the data is not connected", nil)
		return
	}
	password, err := s.secretValue(ctx, p.RepoPasswordSecretID)
	if err != nil {
		s.failRun(ctx, p, runID, node, "the repository password cannot be read: "+err.Error(), nil)
		return
	}
	specs, err := s.targetSpecs(ctx, p)
	if err != nil {
		s.failRun(ctx, p, runID, node, "a target cannot be read: "+err.Error(), nil)
		return
	}
	job := &pb.RunBackup{PolicyId: p.ID, RunId: runID, Source: src, Targets: specs, RepoPassword: password, Tag: trigger,
		KeepDaily: uint32(p.Retention.KeepDaily), KeepWeekly: uint32(p.Retention.KeepWeekly), KeepMonthly: uint32(p.Retention.KeepMonthly)}
	nodes := []string{node}
	if p.ServiceID == nil { // control plane: any node that has the container
		nodes = s.connectedNodes(ctx, p.OrgID, node)
	}
	var ack *pb.CommandAck
	for _, n := range nodes {
		cctx, cancel := context.WithTimeout(ctx, s.RunTimeout)
		ack, err = s.Sender.Send(cctx, n, &pb.Command{CommandId: gateway.NewID("cmd"), Body: &pb.Command_RunBackup{RunBackup: job}})
		cancel()
		node = n
		if err == nil && (ack.GetOk() || ack.GetErrorCode() != "container_not_found") {
			break
		}
	}
	switch {
	case err != nil:
		s.failRun(ctx, p, runID, node, "node_unavailable: "+err.Error(), nil)
		return
	case !ack.GetOk():
		s.failRun(ctx, p, runID, node, ack.GetErrorCode()+": "+ack.GetErrorMessage(), nil)
		return
	}
	var res struct {
		Snapshots []Snapshot      `json:"snapshots"`
		Manifest  json.RawMessage `json:"manifest"`
	}
	if err := json.Unmarshal(ack.GetResultJson(), &res); err != nil {
		s.failRun(ctx, p, runID, node, "the agent gave a result that cannot be read", nil)
		return
	}
	var failed []string
	for _, sn := range res.Snapshots {
		if !sn.OK {
			failed = append(failed, fmt.Sprintf("%s: %s", sn.TargetID, sn.Error))
		}
	}
	if len(failed) > 0 { // the run is failed when any target failed. The others keep their snapshot.
		s.failRun(ctx, p, runID, node, "target failed: "+strings.Join(failed, "; "), res.Snapshots)
		if _, err := s.DB.Exec(ctx, `UPDATE backup_runs SET manifest = $2 WHERE id = $1`, runID, nullJSON(res.Manifest)); err != nil {
			s.Log.Warn("backup manifest", "error", err)
		}
		return
	}
	snapJSON, _ := json.Marshal(res.Snapshots)
	if _, err := s.DB.Exec(ctx, `UPDATE backup_runs SET state = 'succeeded', snapshots = $2, manifest = $3, node_id = $4, finished_at = now() WHERE id = $1`,
		runID, snapJSON, nullJSON(res.Manifest), node); err != nil {
		s.Log.Error("backup run not saved", "run", runID, "error", err)
		return
	}
	s.event(ctx, orgID, p.ID, map[string]any{"type": "run", "run_id": runID, "state": "succeeded"})
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return []byte(raw)
}

// connectedNodes returns the ready nodes with an open stream, first is first.
func (s *Service) connectedNodes(ctx context.Context, orgID, first string) []string {
	rows, err := s.DB.Query(ctx, `SELECT id FROM nodes WHERE org_id = $1 AND state = 'ready' ORDER BY (id = $2) DESC, created_at, id`, orgID, first)
	if err != nil {
		return []string{first}
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && s.Sender != nil && s.Sender.Connected(id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return []string{first}
	}
	return out
}
