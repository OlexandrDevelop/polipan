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
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// CheckLine is one line of a restore check result (REQ-BKP-102).
type CheckLine struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Check is a restore check.
type Check struct {
	ID         string
	PolicyID   string
	RunID      string
	TargetID   string
	NodeID     *string
	State      string
	Trigger    string
	Lines      []CheckLine
	DurationMS *int
	StartedAt  *time.Time
	FinishedAt *time.Time
	CreatedAt  time.Time
}

const checkCols = `id, policy_id, backup_run_id, target_id, node_id, state, trigger, checks, duration_ms, started_at, finished_at, created_at`

func scanCheck(row pgx.Row) (*Check, error) {
	var c Check
	var lines []byte
	if err := row.Scan(&c.ID, &c.PolicyID, &c.RunID, &c.TargetID, &c.NodeID, &c.State, &c.Trigger, &lines, &c.DurationMS, &c.StartedAt, &c.FinishedAt, &c.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(lines, &c.Lines)
	return &c, nil
}

func (s *Service) lastCheck(ctx context.Context, policyID, state string) (*Check, error) {
	c, err := scanCheck(s.DB.QueryRow(ctx, `SELECT `+checkCols+` FROM restore_checks WHERE policy_id = $1 AND ($2 = '' OR state = $2)
		ORDER BY created_at DESC, id DESC LIMIT 1`, policyID, state))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return c, err
}

// ListChecks lists the restore checks of the org, newest first. A policy filter is optional. It returns up to limit+1 rows.
func (s *Service) ListChecks(ctx context.Context, orgID string, policyID *string, limit int, after *store.Cursor) ([]Check, error) {
	var t *time.Time
	var id string
	if after != nil {
		t, id = &after.Time, after.ID
	}
	rows, err := s.DB.Query(ctx, `SELECT `+checkCols+` FROM restore_checks WHERE policy_id IN (SELECT id FROM backup_policies WHERE org_id = $1)
		AND ($2::text IS NULL OR policy_id = $2) AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC LIMIT $5`, orgID, policyID, t, id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// RunCheckNow queues a restore check of the latest successful run (REQ-BKP-106).
func (s *Service) RunCheckNow(ctx context.Context, orgID, policyID string) (*Check, error) {
	p, err := s.GetPolicy(ctx, orgID, policyID)
	if err != nil {
		return nil, err
	}
	c, err := s.queueCheck(ctx, p, "manual")
	if err != nil {
		return nil, err
	}
	go s.kick()
	return c, nil
}

// latestGoodRun is the newest run that succeeded.
func (s *Service) latestGoodRun(ctx context.Context, policyID string) (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(ctx, `SELECT `+runCols+` FROM backup_runs WHERE policy_id = $1 AND state = 'succeeded' ORDER BY created_at DESC, id DESC LIMIT 1`, policyID))
	return r, err
}

// queueCheck picks the snapshot to test: the latest run, and one of its targets. The targets take turns over time (REQ-BKP-100).
func (s *Service) queueCheck(ctx context.Context, p *Policy, trigger string) (*Check, error) {
	run, err := s.latestGoodRun(ctx, p.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: no backup run has succeeded yet", ErrConflict)
		}
		return nil, err
	}
	var good []Snapshot
	for _, sn := range run.Snapshots {
		if sn.OK {
			good = append(good, sn)
		}
	}
	if len(good) == 0 {
		return nil, fmt.Errorf("%w: the latest run has no snapshot", ErrConflict)
	}
	var done int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM restore_checks WHERE policy_id = $1`, p.ID).Scan(&done); err != nil {
		return nil, err
	}
	sn := good[done%len(good)]
	id := store.NewID("rck")
	if _, err := s.DB.Exec(ctx, `INSERT INTO restore_checks (id, policy_id, backup_run_id, target_id, state, trigger) VALUES ($1,$2,$3,$4,'queued',$5)`,
		id, p.ID, run.ID, sn.TargetID, trigger); err != nil {
		return nil, err
	}
	s.event(ctx, p.OrgID, p.ID, map[string]any{"type": "check", "check_id": id, "state": "queued"})
	return scanCheck(s.DB.QueryRow(ctx, `SELECT `+checkCols+` FROM restore_checks WHERE id = $1`, id))
}

// checkNode picks the node for a check: a connected node, another one than the data node when there is one (REQ-BKP-100).
func (s *Service) checkNode(ctx context.Context, p *Policy, dataNode string) (string, bool) {
	nodes := s.connectedNodes(ctx, p.OrgID, "")
	var fallback string
	for _, n := range nodes {
		if !s.Sender.Connected(n) {
			continue
		}
		if n != dataNode {
			return n, true
		}
		fallback = n
	}
	return fallback, fallback != ""
}

// imageOf returns the image for the temporary container of a database check.
func (s *Service) imageOf(ctx context.Context, p *Policy, engine string) (string, error) {
	if engine == "controlplane" {
		major, err := s.controlPlaneMajor(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("postgres:%d", major), nil
	}
	var image *string
	err := s.DB.QueryRow(ctx, `SELECT r.image_ref FROM services s JOIN releases r ON r.id = s.live_release_id WHERE s.id = $1`, *p.ServiceID).Scan(&image)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if image != nil && *image != "" {
		return *image, nil
	}
	var cfg struct {
		Image string `json:"image"`
	}
	var raw []byte
	if err := s.DB.QueryRow(ctx, `SELECT config FROM services WHERE id = $1`, *p.ServiceID).Scan(&raw); err != nil {
		return "", err
	}
	_ = json.Unmarshal(raw, &cfg)
	if cfg.Image == "" {
		return "", errors.New("the database has no image")
	}
	return cfg.Image, nil
}

func (s *Service) controlPlaneMajor(ctx context.Context) (int, error) {
	if s.DatabaseVersion != nil {
		return s.DatabaseVersion(ctx)
	}
	var v int
	err := s.DB.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&v)
	return v, err
}

// executeCheck runs a queued check and records the result (REQ-BKP-101).
func (s *Service) executeCheck(ctx context.Context, checkID string) {
	c, err := scanCheck(s.DB.QueryRow(ctx, `SELECT `+checkCols+` FROM restore_checks WHERE id = $1 AND state = 'queued'`, checkID))
	if err != nil {
		return
	}
	var orgID string
	if err := s.DB.QueryRow(ctx, `SELECT org_id FROM backup_policies WHERE id = $1`, c.PolicyID).Scan(&orgID); err != nil {
		return
	}
	p, err := s.GetPolicy(ctx, orgID, c.PolicyID)
	if err != nil {
		return
	}
	run, err := s.GetRun(ctx, orgID, c.RunID)
	if err != nil {
		return
	}
	engine, err := s.engineOf(ctx, p)
	if err != nil {
		s.failCheck(ctx, p, c, "", "policy", err.Error(), 0)
		return
	}
	dataNode := ""
	if run.NodeID != nil {
		dataNode = *run.NodeID
	}
	node, ok := s.checkNode(ctx, p, dataNode)
	if !ok {
		return // no node can run it now: it stays queued and is tried again (retried every 15 min, REQ edge case)
	}
	if res, err := s.DB.Exec(ctx, `UPDATE restore_checks SET state = 'running', node_id = $2, started_at = now() WHERE id = $1 AND state = 'queued'`, c.ID, node); err != nil || res.RowsAffected() == 0 {
		return
	}
	s.event(ctx, orgID, p.ID, map[string]any{"type": "check", "check_id": c.ID, "state": "running"})
	t, err := s.GetTarget(ctx, orgID, c.TargetID)
	if err != nil {
		s.failCheck(ctx, p, c, node, "target", err.Error(), 0)
		return
	}
	spec, err := s.spec(ctx, t, p.ID)
	if err != nil {
		s.failCheck(ctx, p, c, node, "target", err.Error(), 0)
		return
	}
	password, err := s.secretValue(ctx, p.RepoPasswordSecretID)
	if err != nil {
		s.failCheck(ctx, p, c, node, "password", err.Error(), 0)
		return
	}
	var snapshotID string
	for _, sn := range run.Snapshots {
		if sn.TargetID == c.TargetID {
			snapshotID = sn.SnapshotID
		}
	}
	cmd := &pb.RunRestoreCheck{PolicyId: p.ID, BackupRunId: run.ID, TargetId: c.TargetID, CheckId: c.ID, Source: &pb.BackupSource{Engine: engine},
		Target: spec, RepoPassword: password, SnapshotId: snapshotID, ManifestJson: run.Manifest, Mode: p.CheckMode}
	if engine != "volume" {
		if cmd.ImageRef, err = s.imageOf(ctx, p, engine); err != nil {
			s.failCheck(ctx, p, c, node, "image", err.Error(), 0)
			return
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.CheckTimeout)
	defer cancel()
	started := time.Now()
	ack, err := s.Sender.Send(cctx, node, &pb.Command{CommandId: gateway.NewID("cmd"), Body: &pb.Command_RunRestoreCheck{RunRestoreCheck: cmd}})
	elapsed := int(time.Since(started).Milliseconds())
	switch {
	case err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded):
		s.failCheck(ctx, p, c, node, "timeout", "the check did not end in "+s.CheckTimeout.String(), elapsed)
		return
	case err != nil:
		s.failCheck(ctx, p, c, node, "node_unavailable", err.Error(), elapsed)
		return
	case !ack.GetOk():
		name := ack.GetErrorCode()
		if name == "deadline_exceeded" {
			name = "timeout"
		}
		s.failCheck(ctx, p, c, node, name, ack.GetErrorMessage(), elapsed)
		return
	}
	var res struct {
		Passed     bool        `json:"passed"`
		Checks     []CheckLine `json:"checks"`
		DurationMS int         `json:"duration_ms"`
	}
	if err := json.Unmarshal(ack.GetResultJson(), &res); err != nil {
		s.failCheck(ctx, p, c, node, "result", "the agent gave a result that cannot be read", elapsed)
		return
	}
	state := "failed"
	if res.Passed {
		state = "passed"
	}
	lines, _ := json.Marshal(res.Checks)
	if _, err := s.DB.Exec(ctx, `UPDATE restore_checks SET state = $2, checks = $3, duration_ms = $4, finished_at = now() WHERE id = $1`, c.ID, state, lines, res.DurationMS); err != nil {
		s.Log.Error("restore check not saved", "check", c.ID, "error", err)
		return
	}
	s.event(ctx, orgID, p.ID, map[string]any{"type": "check", "check_id": c.ID, "state": state})
	if !res.Passed {
		var bad []string
		for _, l := range res.Checks {
			if !l.OK {
				bad = append(bad, l.Name+": "+l.Detail)
			}
		}
		s.alert(ctx, orgID, "restore_check_failed", "critical", p.ID, map[string]any{"check_id": c.ID, "failed": strings.Join(bad, "; "), "service_id": p.ServiceID})
	}
}

// failCheck ends a check as failed with one line that tells why, and raises the critical alert (REQ-BKP-103).
func (s *Service) failCheck(ctx context.Context, p *Policy, c *Check, node, name, detail string, elapsedMS int) {
	lines, _ := json.Marshal([]CheckLine{{Name: name, OK: false, Detail: detail}})
	var nodePtr *string
	if node != "" {
		nodePtr = &node
	}
	if _, err := s.DB.Exec(ctx, `UPDATE restore_checks SET state = 'failed', checks = $2, duration_ms = $3, node_id = COALESCE($4, node_id),
		started_at = COALESCE(started_at, now()), finished_at = now() WHERE id = $1`, c.ID, lines, elapsedMS, nodePtr); err != nil {
		s.Log.Error("restore check not saved", "check", c.ID, "error", err)
	}
	s.event(ctx, p.OrgID, p.ID, map[string]any{"type": "check", "check_id": c.ID, "state": "failed"})
	s.alert(ctx, p.OrgID, "restore_check_failed", "critical", p.ID, map[string]any{"check_id": c.ID, "failed": name + ": " + detail, "service_id": p.ServiceID})
}
