// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/polipan/polipan/server/internal/store"
)

// kickCh wakes the loop. A buffer of one is enough: the loop reads all queued work in one tick.
var kickCh = make(chan struct{}, 1)

func (s *Service) kick() {
	select {
	case kickCh <- struct{}{}:
	default:
	}
}

// Run schedules and executes backups, restore checks and restores until ctx ends. It first ends the runs
// that a former server left running.
func (s *Service) Run(ctx context.Context) error {
	s.recover(ctx)
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		s.Tick1(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return nil
		case <-t.C:
		case <-kickCh:
		}
	}
}

// recover ends the runs, checks and restores that were running when the server stopped (no retry: the next run is fresh).
func (s *Service) recover(ctx context.Context) {
	if _, err := s.DB.Exec(ctx, `UPDATE backup_runs SET state = 'failed', error = 'server_restarted: the server restarted during the run', finished_at = now() WHERE state = 'running'`); err != nil {
		s.Log.Warn("backup recover runs", "error", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE restore_checks SET state = 'failed', finished_at = now(),
		checks = '[{"name":"server_restarted","ok":false,"detail":"the server restarted during the check"}]' WHERE state = 'running'`); err != nil {
		s.Log.Warn("backup recover checks", "error", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE backup_restores SET state = 'failed', error = 'server_restarted: the server restarted during the restore', finished_at = now() WHERE state = 'running'`); err != nil {
		s.Log.Warn("backup recover restores", "error", err)
	}
}

func (s *Service) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.working[id] {
		return false
	}
	s.working[id] = true
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.working, id)
	s.mu.Unlock()
}

// Tick1 does one round: the control-plane policy, the schedule, the queued runs and checks, the overdue alerts.
func (s *Service) Tick1(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := s.EnsureControlPlane(ctx); err != nil {
		s.Log.Warn("control-plane backup policy", "error", err)
	}
	s.schedule(ctx)
	s.dispatch(ctx)
	s.overdue(ctx)
}

// schedule queues the runs and checks that are due.
func (s *Service) schedule(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT `+policyCols+` FROM backup_policies WHERE enabled`)
	if err != nil {
		s.Log.Warn("backup schedule", "error", err)
		return
	}
	var list []Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			rows.Close()
			return
		}
		list = append(list, *p)
	}
	rows.Close()
	now := s.now()
	for i := range list {
		p := &list[i]
		if c, err := ParseCron(p.Schedule); err == nil {
			prev := c.Prev(now)
			var last *time.Time
			if err := s.DB.QueryRow(ctx, `SELECT max(created_at) FROM backup_runs WHERE policy_id = $1 AND trigger = 'schedule'`, p.ID).Scan(&last); err != nil {
				continue
			}
			base := p.CreatedAt
			if last != nil && last.After(base) {
				base = *last
			}
			var active int
			_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM backup_runs WHERE policy_id = $1 AND state IN ('queued','running')`, p.ID).Scan(&active)
			if !prev.IsZero() && prev.After(base) && active == 0 {
				if _, err := s.queueRun(ctx, p, "schedule"); err != nil {
					s.Log.Warn("backup run not queued", "policy", p.ID, "error", err)
				}
			}
		}
		if p.CheckEnabled {
			s.scheduleCheck(ctx, p, now)
		}
	}
}

// scheduleCheck queues a check after the first successful backup and then on the check schedule (REQ-BKP-100).
func (s *Service) scheduleCheck(ctx context.Context, p *Policy, now time.Time) {
	var active int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM restore_checks WHERE policy_id = $1 AND state IN ('queued','running')`, p.ID).Scan(&active)
	if active > 0 {
		return
	}
	var good int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM backup_runs WHERE policy_id = $1 AND state = 'succeeded'`, p.ID).Scan(&good)
	if good == 0 {
		return
	}
	var lastCheck *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT max(created_at) FROM restore_checks WHERE policy_id = $1`, p.ID).Scan(&lastCheck); err != nil {
		return
	}
	if lastCheck == nil {
		if _, err := s.queueCheck(ctx, p, "first_backup"); err != nil {
			s.Log.Warn("restore check not queued", "policy", p.ID, "error", err)
		}
		return
	}
	c, err := ParseCron(p.CheckSchedule)
	if err != nil {
		return
	}
	if prev := c.Prev(now); !prev.IsZero() && prev.After(*lastCheck) {
		if _, err := s.queueCheck(ctx, p, "schedule"); err != nil {
			s.Log.Warn("restore check not queued", "policy", p.ID, "error", err)
		}
	}
}

// dispatch starts the queued runs, checks and restores in the background.
func (s *Service) dispatch(ctx context.Context) {
	runs, _ := queryIDs(ctx, s.DB, `SELECT r.id FROM backup_runs r WHERE r.state = 'queued' ORDER BY r.created_at LIMIT 20`)
	for _, id := range runs {
		var orgID, policyID string
		if err := s.DB.QueryRow(ctx, `SELECT p.org_id, p.id FROM backup_runs r JOIN backup_policies p ON p.id = r.policy_id WHERE r.id = $1`, id).Scan(&orgID, &policyID); err != nil {
			continue
		}
		p, err := s.GetPolicy(ctx, orgID, policyID)
		if err != nil {
			continue
		}
		var created time.Time
		_ = s.DB.QueryRow(ctx, `SELECT created_at FROM backup_runs WHERE id = $1`, id).Scan(&created)
		if ok, _ := s.stable(ctx, p); !ok && s.now().Sub(created) < s.StableWait {
			continue // wait for the deployment to end
		}
		if !s.claim(id) {
			continue
		}
		s.wg.Add(1)
		go func(id string) {
			defer s.wg.Done()
			defer s.release(id)
			s.executeRun(ctx, id)
		}(id)
	}
	checks, _ := queryIDs(ctx, s.DB, `SELECT id FROM restore_checks WHERE state = 'queued' ORDER BY created_at LIMIT 20`)
	for _, id := range checks {
		if !s.claim(id) {
			continue
		}
		s.wg.Add(1)
		go func(id string) {
			defer s.wg.Done()
			defer s.release(id)
			s.executeCheck(ctx, id)
		}(id)
	}
	restores, _ := queryIDs(ctx, s.DB, `SELECT id FROM backup_restores WHERE state = 'queued' ORDER BY created_at LIMIT 20`)
	for _, id := range restores {
		if !s.claim(id) {
			continue
		}
		s.wg.Add(1)
		go func(id string) {
			defer s.wg.Done()
			defer s.release(id)
			s.executeRestore(ctx, id)
		}(id)
	}
}

func queryIDs(ctx context.Context, q dbq, sql string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// overdue raises the alert restore_check_overdue once a day for a policy without a passed check in 2 x its check interval (REQ-BKP-103).
func (s *Service) overdue(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT `+policyCols+` FROM backup_policies WHERE enabled AND restore_check_enabled`)
	if err != nil {
		return
	}
	var list []Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			rows.Close()
			return
		}
		list = append(list, *p)
	}
	rows.Close()
	for i := range list {
		p := &list[i]
		passed, err := s.lastCheck(ctx, p.ID, "passed")
		if err != nil || !s.checkOverdue(p, passed) {
			continue
		}
		var recent int
		_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'alert' AND object_id = $1 AND payload->>'rule' = 'restore_check_overdue'
			AND created_at > now() - interval '24 hours'`, p.ID).Scan(&recent)
		if recent == 0 {
			s.alert(ctx, p.OrgID, "restore_check_overdue", "warning", p.ID, map[string]any{"service_id": p.ServiceID})
		}
	}
}

// ---- control-plane database policy (REQ-BKP-010) ----

// ControlPlaneTargetName is the local target that the built-in policy uses until the admin adds a remote one.
const ControlPlaneTargetName = "control-plane-local"

// EnsureControlPlane makes the built-in policy of the control-plane database for every org that has none:
// daily, default retention, a local target on the node of the control plane.
func (s *Service) EnsureControlPlane(ctx context.Context) error {
	ids, err := queryIDs(ctx, s.DB, `SELECT o.id FROM orgs o WHERE o.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM backup_policies p WHERE p.org_id = o.id AND p.service_id IS NULL)`)
	if err != nil {
		return err
	}
	for _, orgID := range ids {
		t, err := s.GetTargetByName(ctx, orgID, ControlPlaneTargetName)
		if err == ErrNotFound {
			t, err = s.CreateTarget(ctx, orgID, TargetInput{Name: ControlPlaneTargetName, Kind: "local",
				Config: map[string]any{"path": s.LocalRoot + "/control-plane"}, Credentials: map[string]string{}})
		}
		if err != nil {
			return err
		}
		p := &Policy{OrgID: orgID, Schedule: "0 3 * * *", TargetIDs: []string{t.ID}, Retention: DefaultRetention, CheckSchedule: DefaultCheckSchedule,
			CheckEnabled: true, CheckMode: "full", Enabled: true}
		if _, err := s.insertPolicy(ctx, p); err != nil {
			if u, _ := store.IsUnique(err); u {
				continue // made by another process
			}
			return err
		}
	}
	return nil
}

// GetTargetByName reads a target of the org by its name.
func (s *Service) GetTargetByName(ctx context.Context, orgID, name string) (*Target, error) {
	return scanTarget(s.DB.QueryRow(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE org_id = $1 AND name = $2`, orgID, name))
}

var _ = json.Marshal
