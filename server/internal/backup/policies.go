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
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// Retention is the keep rule of a policy (REQ-BKP-001).
type Retention struct {
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
}

// DefaultRetention is the glossary default.
var DefaultRetention = Retention{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}

// Default schedules.
const (
	DefaultCheckSchedule  = "0 3 * * 0"
	DefaultBackupSchedule = "0 2 * * *"
)

// Hooks are the commands that run in the container before and after a backup (REQ-BKP-011).
type Hooks struct {
	Pre  []string `json:"pre,omitempty"`
	Post []string `json:"post,omitempty"`
}

// Policy is a backup policy.
type Policy struct {
	ID                   string
	OrgID                string
	ServiceID            *string
	VolumeID             *string
	Schedule             string
	TargetIDs            []string
	Retention            Retention
	RepoPasswordSecretID string
	CheckSchedule        string
	CheckEnabled         bool
	CheckMode            string
	Hooks                Hooks
	Enabled              bool
	CreatedAt            time.Time
}

// PolicyInput is a create or an update. A nil field is not changed (on create: the default).
type PolicyInput struct {
	VolumeID      *string
	Schedule      *string
	TargetIDs     *[]string
	Retention     *map[string]int
	CheckSchedule *string
	CheckEnabled  *bool
	CheckMode     *string
	Enabled       *bool
	PreHook       *[]string
	PostHook      *[]string
}

// liveService keeps the policies of services that are not deleted, and the control-plane policy. The snapshots of a
// deleted service stay on the targets.
const liveService = `(service_id IS NULL OR EXISTS (SELECT 1 FROM services s WHERE s.id = service_id AND s.deleted_at IS NULL))`

const policyCols = `id, org_id, service_id, volume_id, schedule, target_ids, retention, repo_password_secret_id, restore_check_schedule,
	restore_check_enabled, restore_check_mode, hooks, enabled, created_at`

func scanPolicy(row pgx.Row) (*Policy, error) {
	var p Policy
	var ret, hooks []byte
	if err := row.Scan(&p.ID, &p.OrgID, &p.ServiceID, &p.VolumeID, &p.Schedule, &p.TargetIDs, &ret, &p.RepoPasswordSecretID, &p.CheckSchedule,
		&p.CheckEnabled, &p.CheckMode, &hooks, &p.Enabled, &p.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(ret, &p.Retention)
	_ = json.Unmarshal(hooks, &p.Hooks)
	return &p, nil
}

// GetPolicy reads a policy of the org.
func (s *Service) GetPolicy(ctx context.Context, orgID, id string) (*Policy, error) {
	return scanPolicy(s.DB.QueryRow(ctx, `SELECT `+policyCols+` FROM backup_policies WHERE id = $1 AND org_id = $2`, id, orgID))
}

// ListPolicies lists the policies of the org, or of one service.
func (s *Service) ListPolicies(ctx context.Context, orgID string, serviceID *string) ([]Policy, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+policyCols+` FROM backup_policies WHERE org_id = $1 AND ($2::text IS NULL OR service_id = $2) AND `+liveService+`
		ORDER BY (service_id IS NULL) DESC, created_at, id`, orgID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func checkCron(field, expr string) error {
	if _, err := ParseCron(expr); err != nil {
		return invalid(field, "%s: %v", field, err)
	}
	return nil
}

// retentionOf reads a retention from a request. An object with some keys keeps only those rules; a missing one is not used.
// No object at all gives the defaults.
func retentionOf(m *map[string]int) (Retention, error) {
	if m == nil {
		return DefaultRetention, nil
	}
	var r Retention
	for k, v := range *m {
		if v < 0 || v > 3650 {
			return r, invalid("retention."+k, "retention.%s must be from 0 to 3650", k)
		}
		switch k {
		case "keep_daily":
			r.KeepDaily = v
		case "keep_weekly":
			r.KeepWeekly = v
		case "keep_monthly":
			r.KeepMonthly = v
		default:
			return r, invalid("retention."+k, "retention.%s is not a rule (keep_daily, keep_weekly, keep_monthly)", k)
		}
	}
	if r.KeepDaily+r.KeepWeekly+r.KeepMonthly == 0 {
		return r, invalid("retention", "retention needs at least one rule above 0")
	}
	return r, nil
}

func checkHooks(field string, list *[]string) ([]string, error) {
	if list == nil {
		return nil, nil
	}
	if len(*list) > 10 {
		return nil, invalid(field, "%s can have up to 10 commands", field)
	}
	for _, h := range *list {
		if strings.TrimSpace(h) == "" || len(h) > 2000 {
			return nil, invalid(field, "%s: a command must have 1 to 2000 characters", field)
		}
	}
	return *list, nil
}

// engineOf tells what a policy backs up: the engine of the database, "volume", or "controlplane".
func (s *Service) engineOf(ctx context.Context, p *Policy) (string, error) {
	if p.ServiceID == nil {
		return "controlplane", nil
	}
	if p.VolumeID != nil {
		return "volume", nil
	}
	var typ string
	var engine *string
	if err := s.DB.QueryRow(ctx, `SELECT type, db_engine FROM services WHERE id = $1`, *p.ServiceID).Scan(&typ, &engine); err != nil {
		return "", notFound(err)
	}
	if engine == nil {
		return "", invalid("volume_id", "this service is not a database: give the volume_id to back up")
	}
	return *engine, nil
}

// CreatePolicy makes a policy for a service. A database is backed up by its dump. A volume_id backs up the files of that volume.
func (s *Service) CreatePolicy(ctx context.Context, orgID, serviceID string, in PolicyInput) (*Policy, error) {
	var typ string
	var engine *string
	err := s.DB.QueryRow(ctx, `SELECT s.type, s.db_engine FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		WHERE s.id = $1 AND p.org_id = $2 AND s.deleted_at IS NULL`, serviceID, orgID).Scan(&typ, &engine)
	if err != nil {
		return nil, notFound(err)
	}
	p := &Policy{OrgID: orgID, ServiceID: &serviceID, Schedule: DefaultBackupSchedule, Retention: DefaultRetention, CheckSchedule: DefaultCheckSchedule,
		CheckEnabled: true, CheckMode: "full", Enabled: true}
	if in.VolumeID != nil && *in.VolumeID != "" {
		var n int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM volumes WHERE id = $1 AND service_id = $2 AND kind = 'volume'`, *in.VolumeID, serviceID).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, invalid("volume_id", "volume_id must be a volume (not a bind or tmpfs) of this service")
		}
		p.VolumeID = in.VolumeID
	} else if engine == nil {
		return nil, invalid("volume_id", "an app has no database: give the volume_id to back up")
	}
	if err := s.applyInput(ctx, p, in); err != nil {
		return nil, err
	}
	if in.TargetIDs == nil || len(*in.TargetIDs) == 0 {
		return nil, invalid("target_ids", "at least one target is needed")
	}
	return s.insertPolicy(ctx, p)
}

// applyInput copies the fields of a request into the policy after checking them.
func (s *Service) applyInput(ctx context.Context, p *Policy, in PolicyInput) error {
	if in.Schedule != nil {
		if err := checkCron("schedule", *in.Schedule); err != nil {
			return err
		}
		p.Schedule = *in.Schedule
	}
	if in.CheckSchedule != nil {
		if err := checkCron("restore_check_schedule", *in.CheckSchedule); err != nil {
			return err
		}
		p.CheckSchedule = *in.CheckSchedule
	}
	if in.Retention != nil {
		r, err := retentionOf(in.Retention)
		if err != nil {
			return err
		}
		p.Retention = r
	}
	if in.CheckEnabled != nil {
		p.CheckEnabled = *in.CheckEnabled
	}
	if in.CheckMode != nil {
		if *in.CheckMode != "full" && *in.CheckMode != "sample" {
			return invalid("restore_check_mode", "restore_check_mode must be full or sample")
		}
		p.CheckMode = *in.CheckMode
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.PreHook != nil {
		h, err := checkHooks("pre_hook", in.PreHook)
		if err != nil {
			return err
		}
		p.Hooks.Pre = h
	}
	if in.PostHook != nil {
		h, err := checkHooks("post_hook", in.PostHook)
		if err != nil {
			return err
		}
		p.Hooks.Post = h
	}
	if in.TargetIDs != nil {
		seen := map[string]bool{}
		for _, id := range *in.TargetIDs {
			if seen[id] {
				return invalid("target_ids", "target %s is listed twice", id)
			}
			seen[id] = true
			if _, err := s.GetTarget(ctx, p.OrgID, id); err != nil {
				if errors.Is(err, ErrNotFound) {
					return invalid("target_ids", "target %s does not exist", id)
				}
				return err
			}
		}
		p.TargetIDs = *in.TargetIDs
	}
	return nil
}

func (s *Service) insertPolicy(ctx context.Context, p *Policy) (*Policy, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	p.ID = store.NewID("pol")
	password, err := dbengine.GeneratePassword(48)
	if err != nil {
		return nil, err
	}
	if p.RepoPasswordSecretID, err = s.putSecret(ctx, tx, p.OrgID, "backup-repo-"+p.ID, password); err != nil {
		return nil, err
	}
	ret, _ := json.Marshal(p.Retention)
	hooks, _ := json.Marshal(p.Hooks)
	if _, err := tx.Exec(ctx, `INSERT INTO backup_policies (id, org_id, service_id, volume_id, schedule, target_ids, retention, repo_password_secret_id,
		restore_check_schedule, restore_check_enabled, restore_check_mode, hooks, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.OrgID, p.ServiceID, p.VolumeID, p.Schedule, p.TargetIDs, ret, p.RepoPasswordSecretID, p.CheckSchedule, p.CheckEnabled, p.CheckMode, hooks, p.Enabled); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetPolicy(ctx, p.OrgID, p.ID)
}

// UpdatePolicy changes a policy. The service and the volume cannot change.
func (s *Service) UpdatePolicy(ctx context.Context, orgID, id string, in PolicyInput) (*Policy, error) {
	p, err := s.GetPolicy(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if in.VolumeID != nil && (p.VolumeID == nil || *in.VolumeID != *p.VolumeID) {
		return nil, invalid("volume_id", "the volume of a policy cannot change")
	}
	if in.TargetIDs != nil && len(*in.TargetIDs) == 0 {
		return nil, invalid("target_ids", "at least one target is needed")
	}
	if err := s.applyInput(ctx, p, in); err != nil {
		return nil, err
	}
	ret, _ := json.Marshal(p.Retention)
	hooks, _ := json.Marshal(p.Hooks)
	if _, err := s.DB.Exec(ctx, `UPDATE backup_policies SET schedule = $2, target_ids = $3, retention = $4, restore_check_schedule = $5,
		restore_check_enabled = $6, restore_check_mode = $7, hooks = $8, enabled = $9 WHERE id = $1`,
		id, p.Schedule, p.TargetIDs, ret, p.CheckSchedule, p.CheckEnabled, p.CheckMode, hooks, p.Enabled); err != nil {
		return nil, err
	}
	return s.GetPolicy(ctx, orgID, id)
}

// DeletePolicy removes a policy and its runs. With deleteSnapshots the snapshots on the targets are removed too (on the node
// that holds the data); otherwise they stay and can be read with restic (REQ-OSS-005). The control-plane policy cannot be deleted.
func (s *Service) DeletePolicy(ctx context.Context, orgID, id string, deleteSnapshots bool) error {
	p, err := s.GetPolicy(ctx, orgID, id)
	if err != nil {
		return err
	}
	if p.ServiceID == nil {
		return ErrControlPlane
	}
	if deleteSnapshots {
		if err := s.deleteSnapshots(ctx, p); err != nil {
			return err
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM backup_restores WHERE run_id IN (SELECT id FROM backup_runs WHERE policy_id = $1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM restore_checks WHERE policy_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM backup_policies WHERE id = $1`, id); err != nil {
		return err
	}
	// The secret row stays as a deleted row. An admin can read the password of kept snapshots with the
	// command "polipan-server admin backup-password" (REQ-OSS-006).
	if err := s.deleteSecret(ctx, tx, p.RepoPasswordSecretID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deleteSnapshots asks the node that holds the data to remove the snapshots of the policy from every target.
func (s *Service) deleteSnapshots(ctx context.Context, p *Policy) error {
	node, _, err := s.dataNode(ctx, p)
	if err != nil {
		return err
	}
	password, err := s.secretValue(ctx, p.RepoPasswordSecretID)
	if err != nil {
		return err
	}
	var specs []*pb.BackupTargetSpec
	for _, id := range p.TargetIDs {
		t, err := s.GetTarget(ctx, p.OrgID, id)
		if err != nil {
			return err
		}
		sp, err := s.spec(ctx, t, p.ID)
		if err != nil {
			return err
		}
		specs = append(specs, sp)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	ack, err := s.Sender.Send(cctx, node, &pb.Command{CommandId: gateway.NewID("cmd"),
		Body: &pb.Command_DeleteSnapshots{DeleteSnapshots: &pb.DeleteSnapshots{PolicyId: p.ID, Targets: specs, RepoPassword: password}}})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoNode, err)
	}
	if !ack.GetOk() {
		return fmt.Errorf("the snapshots were not removed: %s", ack.GetErrorMessage())
	}
	return nil
}

// ---- views ----

// PolicyView is a policy with its state.
type PolicyView struct {
	*Policy
	Engine      string
	Health      string // verified | unverified | failing
	CheckOver   bool
	LocalOnly   bool
	LastRun     *Run
	LastCheck   *Check
	ServiceName string
}

// View adds the engine, the health and the last run and check to a policy.
func (s *Service) View(ctx context.Context, p *Policy) (*PolicyView, error) {
	v := &PolicyView{Policy: p}
	var err error
	if v.Engine, err = s.engineOf(ctx, p); err != nil {
		v.Engine = "unknown"
	}
	if v.LastRun, err = s.lastRun(ctx, p.ID, ""); err != nil {
		return nil, err
	}
	if v.LastCheck, err = s.lastCheck(ctx, p.ID, ""); err != nil {
		return nil, err
	}
	succeeded, err := s.lastRun(ctx, p.ID, "succeeded")
	if err != nil {
		return nil, err
	}
	passed, err := s.lastCheck(ctx, p.ID, "passed")
	if err != nil {
		return nil, err
	}
	// Health (F10 states): verified = the last check passed and the last backup run succeeded.
	switch {
	case (v.LastRun != nil && v.LastRun.State == "failed") || (v.LastCheck != nil && v.LastCheck.State == "failed"):
		v.Health = "failing"
	case v.LastCheck != nil && v.LastCheck.State == "passed" && v.LastRun != nil && v.LastRun.State == "succeeded":
		v.Health = "verified"
	default:
		v.Health = "unverified"
	}
	_ = succeeded
	v.CheckOver = s.checkOverdue(p, passed)
	v.LocalOnly = true
	for _, id := range p.TargetIDs {
		var kind string
		if err := s.DB.QueryRow(ctx, `SELECT kind FROM backup_targets WHERE id = $1`, id).Scan(&kind); err == nil && kind != "local" {
			v.LocalOnly = false
		}
	}
	return v, nil
}

// checkOverdue is true when no restore check passed within 2 x the check interval (REQ-BKP-103).
func (s *Service) checkOverdue(p *Policy, lastPassed *Check) bool {
	if !p.CheckEnabled || !p.Enabled {
		return false
	}
	c, err := ParseCron(p.CheckSchedule)
	if err != nil {
		return false
	}
	now := s.now()
	interval := c.Interval(now)
	if interval == 0 {
		return false
	}
	since := p.CreatedAt
	if lastPassed != nil && lastPassed.FinishedAt != nil {
		since = *lastPassed.FinishedAt
	}
	return now.Sub(since) > 2*interval
}
