// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/store"
)

// backupFailed maps the errors of the backup service to answers. It returns true when it wrote one.
func (h *Handlers) backupFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, backup.ErrNotFound):
		notFoundError(w, r)
	case errors.Is(err, backup.ErrInvalid):
		if field := backup.InvalidField(err); field != "" {
			invalid(w, r, field, err.Error())
		} else {
			WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		}
	case errors.Is(err, backup.ErrTargetInUse):
		WriteError(w, r, http.StatusConflict, CodeConflict, "a backup policy uses this target", map[string]any{"reason": "target_in_use"})
	case errors.Is(err, backup.ErrDeployRunning):
		WriteError(w, r, http.StatusConflict, CodeConflict, "a deployment of this service is running", map[string]any{"reason": "deploy_running"})
	case errors.Is(err, backup.ErrAlreadyRunning):
		WriteError(w, r, http.StatusConflict, CodeConflict, "a restore of this service is running", map[string]any{"reason": "restore_running"})
	case errors.Is(err, backup.ErrNoNode):
		WriteError(w, r, http.StatusConflict, CodeConflict, "no node can do this now", map[string]any{"reason": "node_unavailable"})
	case errors.Is(err, backup.ErrNotRestorable):
		WriteError(w, r, http.StatusConflict, CodeConflict, err.Error(), map[string]any{"reason": "not_restorable"})
	case errors.Is(err, backup.ErrControlPlane):
		WriteError(w, r, http.StatusConflict, CodeConflict, "the control-plane policy cannot be deleted", map[string]any{"reason": "control_plane"})
	case errors.Is(err, backup.ErrConflict):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, err.Error(), nil)
	default:
		internalError(w, r, err)
	}
	return true
}

func (h *Handlers) needBackup(w http.ResponseWriter, r *http.Request) bool {
	if h.Backup == nil {
		internalError(w, r, errors.New("the backup service is not set up"))
		return false
	}
	return true
}

func targetJSON(t *backup.Target) gen.BackupTarget {
	kind := gen.BackupTargetKind(t.Kind)
	cfg := t.Config
	return gen.BackupTarget{Id: &t.ID, Name: &t.Name, Kind: &kind, Config: &cfg, LastTestAt: t.LastTestAt, LastTestOk: t.LastTestOK}
}

// ListBackupTargets lists the targets. Credentials are never returned.
func (h *Handlers) ListBackupTargets(w http.ResponseWriter, r *http.Request) {
	if !h.needBackup(w, r) {
		return
	}
	list, err := h.Backup.ListTargets(r.Context(), h.orgID(r))
	if h.backupFailed(w, r, err) {
		return
	}
	out := make([]gen.BackupTarget, len(list))
	for i := range list {
		out[i] = targetJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

func targetInput(b gen.BackupTargetWrite) backup.TargetInput {
	in := backup.TargetInput{Name: b.Name, Kind: string(b.Kind), Config: b.Config}
	if b.Credentials != nil {
		in.Credentials = *b.Credentials
	}
	return in
}

// CreateBackupTarget adds a target. The credentials are write-only and stored as a secret (REQ-BKP-004).
func (h *Handlers) CreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	if !h.needBackup(w, r) {
		return
	}
	var body gen.BackupTargetWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := h.Backup.CreateTarget(r.Context(), h.orgID(r), targetInput(body))
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup_target.create", "backup_target", t.ID, map[string]any{"name": t.Name, "kind": t.Kind, "credentials": "***"})
	WriteJSON(w, http.StatusCreated, targetJSON(t))
}

// UpdateBackupTarget changes a target. New credentials replace the old ones.
func (h *Handlers) UpdateBackupTarget(w http.ResponseWriter, r *http.Request, targetID gen.TargetId) {
	if !h.needBackup(w, r) {
		return
	}
	var body gen.BackupTargetWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	// A target that a policy of a protected environment (or the control-plane policy) uses needs an admin (R13).
	protected, err := h.Backup.TargetUsedByProtected(r.Context(), h.orgID(r), targetID)
	if h.backupFailed(w, r, err) {
		return
	}
	if denyProtected(w, r, "backup:manage", protected) {
		return
	}
	t, err := h.Backup.UpdateTarget(r.Context(), h.orgID(r), targetID, targetInput(body))
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup_target.update", "backup_target", t.ID, map[string]any{"name": t.Name, "credentials": credentialsChanged(body)})
	WriteJSON(w, http.StatusOK, targetJSON(t))
}

func credentialsChanged(b gen.BackupTargetWrite) string {
	if b.Credentials != nil {
		return "***"
	}
	return "unchanged"
}

// DeleteBackupTarget removes a target that no policy uses.
func (h *Handlers) DeleteBackupTarget(w http.ResponseWriter, r *http.Request, targetID gen.TargetId) {
	if !h.needBackup(w, r) {
		return
	}
	if h.backupFailed(w, r, h.Backup.DeleteTarget(r.Context(), h.orgID(r), targetID)) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup_target.delete", "backup_target", targetID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestBackupTarget writes and reads a small test object on the target.
func (h *Handlers) TestBackupTarget(w http.ResponseWriter, r *http.Request, targetID gen.TargetId) {
	if !h.needBackup(w, r) {
		return
	}
	ok, msg, err := h.Backup.TestTarget(r.Context(), h.orgID(r), targetID)
	if h.backupFailed(w, r, err) {
		return
	}
	out := map[string]any{"ok": ok}
	if msg != "" {
		out["error"] = msg
	}
	WriteJSON(w, http.StatusOK, out)
}

func (h *Handlers) runJSON(run *backup.Run) gen.BackupRun {
	state, trigger := gen.BackupRunState(run.State), gen.BackupRunTrigger(run.Trigger)
	out := gen.BackupRun{Id: &run.ID, PolicyId: &run.PolicyID, State: &state, Trigger: &trigger, Error: run.Error, NodeId: run.NodeID,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, DurationMs: run.DurationMS()}
	snaps := make([]struct {
		Error      *string `json:"error,omitempty"`
		Ok         *bool   `json:"ok,omitempty"`
		SizeBytes  *int    `json:"size_bytes,omitempty"`
		SnapshotId *string `json:"snapshot_id,omitempty"`
		TargetId   *string `json:"target_id,omitempty"`
	}, len(run.Snapshots))
	for i, s := range run.Snapshots {
		size := int(s.SizeBytes)
		snaps[i].TargetId, snaps[i].SnapshotId, snaps[i].SizeBytes, snaps[i].Ok = ptr(s.TargetID), ptr(s.SnapshotID), &size, ptr(s.OK)
		if s.Error != "" {
			snaps[i].Error = ptr(s.Error)
		}
	}
	out.Snapshots = &snaps
	var m map[string]interface{}
	if len(run.Manifest) > 0 && json.Unmarshal(run.Manifest, &m) == nil {
		out.Manifest = &m
	}
	return out
}

func checkJSON(c *backup.Check) gen.RestoreCheck {
	state, trigger := gen.RestoreCheckState(c.State), gen.RestoreCheckTrigger(c.Trigger)
	lines := make([]struct {
		Detail *string `json:"detail,omitempty"`
		Name   *string `json:"name,omitempty"`
		Ok     *bool   `json:"ok,omitempty"`
	}, len(c.Lines))
	for i, l := range c.Lines {
		lines[i].Name, lines[i].Ok, lines[i].Detail = ptr(l.Name), ptr(l.OK), ptr(l.Detail)
	}
	return gen.RestoreCheck{Id: &c.ID, PolicyId: &c.PolicyID, BackupRunId: &c.RunID, TargetId: &c.TargetID, NodeId: c.NodeID, State: &state, Trigger: &trigger,
		Checks: &lines, DurationMs: c.DurationMS, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt}
}

func (h *Handlers) policyJSON(v *backup.PolicyView) gen.BackupPolicy {
	health := gen.BackupPolicyHealth(v.Health)
	mode := gen.BackupPolicyRestoreCheckMode(v.CheckMode)
	out := gen.BackupPolicy{Id: &v.ID, ServiceId: v.ServiceID, VolumeId: v.VolumeID, Schedule: &v.Schedule, TargetIds: &v.TargetIDs,
		RestoreCheckSchedule: &v.CheckSchedule, RestoreCheckEnabled: &v.CheckEnabled, RestoreCheckMode: &mode, Enabled: &v.Enabled,
		Health: &health, ControlPlane: ptr(v.ServiceID == nil), Engine: &v.Engine, CheckOverdue: &v.CheckOver, LocalOnly: &v.LocalOnly}
	out.Retention = &struct {
		KeepDaily   *int `json:"keep_daily,omitempty"`
		KeepMonthly *int `json:"keep_monthly,omitempty"`
		KeepWeekly  *int `json:"keep_weekly,omitempty"`
	}{KeepDaily: &v.Retention.KeepDaily, KeepWeekly: &v.Retention.KeepWeekly, KeepMonthly: &v.Retention.KeepMonthly}
	pre, post := v.Hooks.Pre, v.Hooks.Post
	if pre == nil {
		pre = []string{}
	}
	if post == nil {
		post = []string{}
	}
	out.Hooks = &struct {
		Post *[]string `json:"post,omitempty"`
		Pre  *[]string `json:"pre,omitempty"`
	}{Post: &post, Pre: &pre}
	if v.LastRun != nil {
		r := h.runJSON(v.LastRun)
		out.LastRun = &r
	}
	if v.LastCheck != nil {
		c := checkJSON(v.LastCheck)
		out.LastRestoreCheck = &c
	}
	return out
}

func (h *Handlers) policyView(w http.ResponseWriter, r *http.Request, p *backup.Policy) (gen.BackupPolicy, bool) {
	v, err := h.Backup.View(r.Context(), p)
	if h.backupFailed(w, r, err) {
		return gen.BackupPolicy{}, false
	}
	return h.policyJSON(v), true
}

// policyProtected tells if the environment of the service of a policy is protected. The control-plane policy counts as protected.
func (h *Handlers) policyProtected(r *http.Request, p *backup.Policy) bool {
	if p.ServiceID == nil {
		return true
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), *p.ServiceID)
	if err != nil {
		return true
	}
	return s.EnvProtected
}

func policyInput(b gen.BackupPolicyWrite) backup.PolicyInput {
	in := backup.PolicyInput{VolumeID: b.VolumeId, Schedule: b.Schedule, TargetIDs: b.TargetIds, Retention: b.Retention,
		CheckSchedule: b.RestoreCheckSchedule, CheckEnabled: b.RestoreCheckEnabled, Enabled: b.Enabled, PreHook: b.PreHook, PostHook: b.PostHook}
	if b.RestoreCheckMode != nil {
		m := string(*b.RestoreCheckMode)
		in.CheckMode = &m
	}
	return in
}

func (h *Handlers) listPolicies(w http.ResponseWriter, r *http.Request, serviceID *string) {
	list, err := h.Backup.ListPolicies(r.Context(), h.orgID(r), serviceID)
	if h.backupFailed(w, r, err) {
		return
	}
	out := make([]gen.BackupPolicy, 0, len(list))
	for i := range list {
		j, ok := h.policyView(w, r, &list[i])
		if !ok {
			return
		}
		out = append(out, j)
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListAllBackupPolicies lists every policy of the org, with the control-plane policy first.
func (h *Handlers) ListAllBackupPolicies(w http.ResponseWriter, r *http.Request) {
	if !h.needBackup(w, r) {
		return
	}
	h.listPolicies(w, r, nil)
}

// ListBackupPolicies lists the policies of a service.
func (h *Handlers) ListBackupPolicies(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if !h.needBackup(w, r) {
		return
	}
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	h.listPolicies(w, r, &serviceID)
}

// CreateBackupPolicy makes a policy for a database or a volume of a service (REQ-BKP-001).
func (h *Handlers) CreateBackupPolicy(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if !h.needBackup(w, r) {
		return
	}
	var body gen.BackupPolicyWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "backup:manage", s.EnvProtected) {
		return
	}
	p, err := h.Backup.CreatePolicy(r.Context(), h.orgID(r), serviceID, policyInput(body))
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), s.ProjectID, "backup_policy.create", "backup_policy", p.ID, map[string]any{"service_id": serviceID, "targets": p.TargetIDs, "schedule": p.Schedule})
	j, ok := h.policyView(w, r, p)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusCreated, j)
}

func (h *Handlers) loadPolicy(w http.ResponseWriter, r *http.Request, id string) (*backup.Policy, bool) {
	if !h.needBackup(w, r) {
		return nil, false
	}
	p, err := h.Backup.GetPolicy(r.Context(), h.orgID(r), id)
	if h.backupFailed(w, r, err) {
		return nil, false
	}
	return p, true
}

// GetBackupPolicy returns a policy with its health, its last run and its last restore check.
func (h *Handlers) GetBackupPolicy(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId) {
	p, ok := h.loadPolicy(w, r, policyID)
	if !ok {
		return
	}
	if j, ok := h.policyView(w, r, p); ok {
		WriteJSON(w, http.StatusOK, j)
	}
}

// UpdateBackupPolicy changes a policy.
func (h *Handlers) UpdateBackupPolicy(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId) {
	var body gen.BackupPolicyWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	p, ok := h.loadPolicy(w, r, policyID)
	if !ok {
		return
	}
	if denyProtected(w, r, "backup:manage", h.policyProtected(r, p)) {
		return
	}
	updated, err := h.Backup.UpdatePolicy(r.Context(), h.orgID(r), policyID, policyInput(body))
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup_policy.update", "backup_policy", policyID, map[string]any{"schedule": updated.Schedule, "targets": updated.TargetIDs})
	if j, ok := h.policyView(w, r, updated); ok {
		WriteJSON(w, http.StatusOK, j)
	}
}

// DeleteBackupPolicy deletes a policy. delete_snapshots=true also removes the snapshots from the targets.
func (h *Handlers) DeleteBackupPolicy(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId, params gen.DeleteBackupPolicyParams) {
	p, ok := h.loadPolicy(w, r, policyID)
	if !ok {
		return
	}
	if denyProtected(w, r, "backup:manage", h.policyProtected(r, p)) {
		return
	}
	del := params.DeleteSnapshots != nil && *params.DeleteSnapshots
	if h.backupFailed(w, r, h.Backup.DeletePolicy(r.Context(), h.orgID(r), policyID, del)) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup_policy.delete", "backup_policy", policyID, map[string]any{"delete_snapshots": del})
	w.WriteHeader(http.StatusNoContent)
}

// RunBackup queues a manual run (REQ-BKP-006).
func (h *Handlers) RunBackup(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId, params gen.RunBackupParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	p, ok := h.loadPolicy(w, r, policyID)
	if !ok {
		return
	}
	if denyProtected(w, r, "backup:manage", h.policyProtected(r, p)) {
		return
	}
	run, err := h.Backup.RunNow(r.Context(), h.orgID(r), policyID)
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup.run", "backup_run", run.ID, map[string]any{"policy_id": policyID})
	out := h.runJSON(run)
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	WriteJSON(w, http.StatusAccepted, out)
}

// ListBackupRuns lists the runs of a policy, newest first (REQ-BKP-008).
func (h *Handlers) ListBackupRuns(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId, params gen.ListBackupRunsParams) {
	if !h.needBackup(w, r) {
		return
	}
	cur, ok := parseCursor(w, r, params.Cursor)
	if !ok {
		return
	}
	limit := pageLimit(params.Limit)
	list, err := h.Backup.ListRuns(r.Context(), h.orgID(r), policyID, limit, cur)
	if h.backupFailed(w, r, err) {
		return
	}
	out := page[gen.BackupRun]{Items: []gen.BackupRun{}}
	for i := range list {
		if i == limit {
			next := store.Cursor{Time: list[limit-1].CreatedAt, ID: list[limit-1].ID}.Encode()
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, h.runJSON(&list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}

// RestoreBackup restores a snapshot into the service or into a new service (REQ-BKP-007).
func (h *Handlers) RestoreBackup(w http.ResponseWriter, r *http.Request, runID string, params gen.RestoreBackupParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	if !h.needBackup(w, r) {
		return
	}
	var body gen.RestoreBackupJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	in := backup.RestoreInput{TargetID: body.TargetId, Mode: string(body.Mode), ActorID: actor(r).UserID}
	if body.NewServiceName != nil {
		in.NewServiceName = *body.NewServiceName
	}
	if body.Confirm != nil {
		in.Confirm = *body.Confirm
	}
	run, err := h.Backup.GetRun(r.Context(), h.orgID(r), runID)
	if h.backupFailed(w, r, err) {
		return
	}
	rs, err := h.Backup.StartRestore(r.Context(), h.orgID(r), runID, in)
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup.restore", "backup_restore", rs.ID, map[string]any{"run_id": run.ID, "mode": in.Mode, "target_id": in.TargetID})
	out := map[string]any{"job_id": rs.ID}
	if rs.ServiceID != nil {
		out["service_id"] = *rs.ServiceID
	}
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	WriteJSON(w, http.StatusAccepted, out)
}

// GetBackupRestore returns a restore job.
func (h *Handlers) GetBackupRestore(w http.ResponseWriter, r *http.Request, restoreID string) {
	if !h.needBackup(w, r) {
		return
	}
	rs, err := h.Backup.GetRestore(r.Context(), h.orgID(r), restoreID)
	if h.backupFailed(w, r, err) {
		return
	}
	mode, state := gen.BackupRestoreMode(rs.Mode), gen.BackupRestoreState(rs.State)
	WriteJSON(w, http.StatusOK, gen.BackupRestore{Id: &rs.ID, RunId: &rs.RunID, TargetId: &rs.TargetID, Mode: &mode, ServiceId: rs.ServiceID, State: &state,
		Error: rs.Error, CreatedAt: &rs.CreatedAt, FinishedAt: rs.FinishedAt})
}

// RunRestoreCheck starts a restore check now (REQ-BKP-106).
func (h *Handlers) RunRestoreCheck(w http.ResponseWriter, r *http.Request, policyID gen.PolicyId, params gen.RunRestoreCheckParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	p, ok := h.loadPolicy(w, r, policyID)
	if !ok {
		return
	}
	if denyProtected(w, r, "backup:manage", h.policyProtected(r, p)) {
		return
	}
	c, err := h.Backup.RunCheckNow(r.Context(), h.orgID(r), policyID)
	if h.backupFailed(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "backup.restore_check", "restore_check", c.ID, map[string]any{"policy_id": policyID})
	out := checkJSON(c)
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	WriteJSON(w, http.StatusAccepted, out)
}

// ListRestoreChecks lists the restore checks, newest first. policy_id filters.
func (h *Handlers) ListRestoreChecks(w http.ResponseWriter, r *http.Request, params gen.ListRestoreChecksParams) {
	if !h.needBackup(w, r) {
		return
	}
	cur, ok := parseCursor(w, r, params.Cursor)
	if !ok {
		return
	}
	limit := pageLimit(params.Limit)
	list, err := h.Backup.ListChecks(r.Context(), h.orgID(r), params.PolicyId, limit, cur)
	if h.backupFailed(w, r, err) {
		return
	}
	out := page[gen.RestoreCheck]{Items: []gen.RestoreCheck{}}
	for i := range list {
		if i == limit {
			next := store.Cursor{Time: list[limit-1].CreatedAt, ID: list[limit-1].ID}.Encode()
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, checkJSON(&list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}
