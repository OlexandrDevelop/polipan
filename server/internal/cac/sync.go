// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/store"
)

// Sync states (config_syncs.state).
const (
	SyncQueued      = "queued"
	SyncApplied     = "applied"
	SyncPlanPending = "plan_pending"
	SyncInvalid     = "invalid"
	SyncFailed      = "failed"
	SyncSuperseded  = "superseded"
	SyncApproved    = "approved"
)

// Config sync states of a project (projects.config_sync_state).
const (
	ProjectInSync      = "in_sync"
	ProjectDrifted     = "drifted"
	ProjectPlanPending = "plan_pending"
	ProjectApplyFailed = "apply_failed"
)

// DefaultPath is the file name when config_source has no path.
const DefaultPath = "polipan.yaml"

// ConfigSource is projects.config_source: where the config file is (REQ-CAC-003).
type ConfigSource struct {
	ConnectionID string `json:"connection_id"`
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
}

// ParseSource reads config_source. ok is false when the project has none.
func ParseSource(raw []byte) (ConfigSource, bool) {
	var s ConfigSource
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s.Repo == "" {
		return s, false
	}
	if s.Path == "" {
		s.Path = DefaultPath
	}
	return s, true
}

// Syncer runs the syncs of a project: one after the other, in the order of the pushes (F08 edge cases).
type Syncer struct {
	Pool    *pgxpool.Pool
	Store   *store.Store
	Applier *Applier
	// Fetch reads the config file of the source at a commit (empty: the head of the branch).
	Fetch func(ctx context.Context, orgID string, src ConfigSource, commit string) ([]byte, error)
	// Status posts a commit status. Best effort: it may be nil, and its errors are ignored.
	Status func(ctx context.Context, orgID string, src ConfigSource, commit string, st git.Status)
	// LinkURL is the address of the config screen of a project, for the commit status.
	LinkURL func(ctx context.Context, projectID string) string
}

// Push is a push to the tracked branch.
type Push struct {
	OrgID, ProjectID string
	Commit, Author   string
}

// SyncPush reads the file at the commit, queues it and runs the queue of the project. It returns the state of the sync row.
// A push applies by itself only what REQ-CAC-013 and REQ-CAC-014 allow.
func (s *Syncer) SyncPush(ctx context.Context, p Push) (id, state string, err error) {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(config_source,'null'::jsonb) FROM projects WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		p.ProjectID, p.OrgID).Scan(&raw); err != nil {
		return "", "", err
	}
	src, ok := ParseSource(raw)
	if !ok {
		return "", "", errors.New("the project has no config_source")
	}
	s.status(ctx, p.OrgID, p.ProjectID, src, p.Commit, git.StatusPending, "Polipan is reading "+src.Path)
	content, ferr := s.Fetch(ctx, p.OrgID, src, p.Commit)
	id = store.NewID("cfs")
	if ferr != nil {
		msg := "cannot read " + src.Path + ": " + ferr.Error()
		errs, _ := json.Marshal([]Issue{{Line: 0, Path: src.Path, Message: msg}})
		_, err := s.Pool.Exec(ctx, `INSERT INTO config_syncs (id, project_id, trigger, commit_sha, author, content, state, errors, finished_at)
			VALUES ($1,$2,'push',NULLIF($3,''),NULLIF($4,''),'',$5,$6::jsonb, now())`, id, p.ProjectID, p.Commit, p.Author, SyncInvalid, string(errs))
		s.status(ctx, p.OrgID, p.ProjectID, src, p.Commit, git.StatusFailure, msg)
		return id, SyncInvalid, err
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO config_syncs (id, project_id, trigger, commit_sha, author, content)
		VALUES ($1,$2,'push',NULLIF($3,''),NULLIF($4,''),$5)`, id, p.ProjectID, p.Commit, p.Author, string(content)); err != nil {
		return "", "", err
	}
	if err := s.Drain(ctx, p.OrgID, p.ProjectID); err != nil {
		return id, "", err
	}
	err = s.Pool.QueryRow(ctx, `SELECT state FROM config_syncs WHERE id = $1`, id).Scan(&state)
	return id, state, err
}

// Lock takes the lock of a project: one sync or apply runs for a project at a time. The caller must call unlock.
func (s *Syncer) Lock(ctx context.Context, projectID string) (unlock func(), err error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('cac:' || $1::text, 0))`, projectID); err != nil {
		conn.Release()
		return nil, err
	}
	return func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('cac:' || $1::text, 0))`, projectID)
		conn.Release()
	}, nil
}

// Drain runs the queued rows of a project in order. Only one drain runs for a project at a time.
func (s *Syncer) Drain(ctx context.Context, orgID, projectID string) error {
	unlock, err := s.Lock(ctx, projectID)
	if err != nil {
		return err
	}
	defer unlock()
	conn := s.Pool
	for {
		var id, content, commit, author string
		err := conn.QueryRow(ctx, `SELECT id, content, COALESCE(commit_sha,''), COALESCE(author,'') FROM config_syncs
			WHERE project_id = $1 AND state = 'queued' ORDER BY seq LIMIT 1`, projectID).Scan(&id, &content, &commit, &author)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.run(ctx, orgID, projectID, id, []byte(content), commit, author); err != nil {
			_, _ = conn.Exec(ctx, `UPDATE config_syncs SET state = 'failed', result = $2::jsonb, finished_at = now() WHERE id = $1`,
				id, mustJSON(map[string]string{"error": err.Error()}))
			_, _ = conn.Exec(ctx, `UPDATE projects SET config_sync_state = 'apply_failed' WHERE id = $1`, projectID)
		}
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// BuildPlan loads the live state and plans a file. It is the same call for the plan API and for a sync (REQ-CAC-003).
func (s *Syncer) BuildPlan(ctx context.Context, orgID, projectID string, content []byte) (*Plan, error) {
	live, err := Load(ctx, s.Pool, orgID, projectID)
	if err != nil {
		return nil, err
	}
	bc := BuildContext{}
	if src, ok := ParseSource(mustRaw(live.ConfigSource)); ok {
		bc.DefaultConnectionID = src.ConnectionID
	}
	return NewPlan(content, live, bc), nil
}

func mustRaw(m map[string]any) []byte {
	raw, _ := json.Marshal(m)
	return raw
}

// NeedsAdmin tells if a push may not apply a plan by itself: it touches a protected environment or has one of the
// changes of REQ-CAC-014 (REQ-CAC-013).
func NeedsAdmin(pl *Plan) bool {
	for _, c := range pl.Changes {
		if c.Action == ActionUnmanaged {
			continue
		}
		if c.ProtectedEnvironment || c.RequiresAdmin {
			return true
		}
	}
	return false
}

func (s *Syncer) run(ctx context.Context, orgID, projectID, id string, content []byte, commit, author string) error {
	sctx := audit.WithRequest(ctx, &audit.Request{ActorType: audit.ActorSystem, ActorID: "git-sync:" + shortCommit(commit)})
	var raw []byte
	_ = s.Pool.QueryRow(ctx, `SELECT COALESCE(config_source,'null'::jsonb) FROM projects WHERE id = $1`, projectID).Scan(&raw)
	src, _ := ParseSource(raw)
	pl, err := s.BuildPlan(ctx, orgID, projectID, content)
	if err != nil {
		return err
	}
	finish := func(state, projectState string, result any, errs any) error {
		_, err := s.Pool.Exec(ctx, `UPDATE config_syncs SET state = $2, plan_hash = $3, plan = $4::jsonb, result = $5::jsonb, errors = $6::jsonb, finished_at = now() WHERE id = $1`,
			id, state, pl.PlanHash, mustJSON(pl.Changes), nullStr(result), nullStr(errs))
		if err != nil {
			return err
		}
		if projectState != "" {
			_, err = s.Pool.Exec(ctx, `UPDATE projects SET config_sync_state = $2, config_synced_commit = CASE WHEN $3 <> '' AND $2 IN ('in_sync','drifted') THEN $3 ELSE config_synced_commit END WHERE id = $1`,
				projectID, projectState, commit)
		}
		return err
	}
	if !pl.Valid {
		if err := finish(SyncInvalid, "", nil, pl.Errors); err != nil {
			return err
		}
		s.status(ctx, orgID, projectID, src, commit, git.StatusFailure, firstIssue(pl.Errors))
		s.audit(sctx, orgID, projectID, id, "config.sync", map[string]any{"state": SyncInvalid, "commit": commit, "author": author, "errors": len(pl.Errors)})
		return nil
	}
	if NeedsAdmin(pl) {
		// An older pending plan is replaced by this one.
		_, _ = s.Pool.Exec(ctx, `UPDATE config_syncs SET state = 'superseded' WHERE project_id = $1 AND state = 'plan_pending' AND id <> $2`, projectID, id)
		if err := finish(SyncPlanPending, ProjectPlanPending, nil, nil); err != nil {
			return err
		}
		s.status(ctx, orgID, projectID, src, commit, git.StatusPending, "Waiting for an admin to apply the plan")
		s.audit(sctx, orgID, projectID, id, "config.sync", map[string]any{"state": SyncPlanPending, "commit": commit, "author": author})
		return nil
	}
	if !pl.HasChanges() {
		if err := finish(SyncApplied, s.settledState(ctx, projectID), map[string]any{"items": []any{}}, nil); err != nil {
			return err
		}
		s.status(ctx, orgID, projectID, src, commit, git.StatusSuccess, "Nothing to change")
		return nil
	}
	res, err := s.Applier.Apply(sctx, pl, Options{OrgID: orgID, ProjectID: projectID})
	if err != nil {
		return err
	}
	if res.Failed() {
		if err := finish(SyncFailed, ProjectApplyFailed, res, nil); err != nil {
			return err
		}
		s.status(ctx, orgID, projectID, src, commit, git.StatusFailure, "The apply failed. Open the config screen of the project")
	} else {
		if err := finish(SyncApplied, s.settledState(ctx, projectID), res, nil); err != nil {
			return err
		}
		s.status(ctx, orgID, projectID, src, commit, git.StatusSuccess, fmt.Sprintf("Applied %d changes", len(res.Items)))
	}
	s.audit(sctx, orgID, projectID, id, "config.sync", map[string]any{"state": SyncApplied, "commit": commit, "author": author, "changes": len(res.Items)})
	return nil
}

func nullStr(v any) any {
	if v == nil {
		return nil
	}
	return mustJSON(v)
}

func firstIssue(errs []Issue) string {
	if len(errs) == 0 {
		return "the file is not valid"
	}
	return "polipan.yaml: " + errs[0].String()
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// settledState is the state of a project after a good apply: drifted while a drift record is open.
func (s *Syncer) settledState(ctx context.Context, projectID string) string {
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM config_drift WHERE project_id = $1 AND resolved_at IS NULL`, projectID).Scan(&n)
	if n > 0 {
		return ProjectDrifted
	}
	return ProjectInSync
}

func (s *Syncer) status(ctx context.Context, orgID, projectID string, src ConfigSource, commit, state, desc string) {
	if s.Status == nil || commit == "" {
		return
	}
	target := ""
	if s.LinkURL != nil {
		target = s.LinkURL(ctx, projectID)
	}
	s.Status(ctx, orgID, src, commit, git.Status{State: state, Description: desc, Context: "polipan/config", TargetURL: target})
}

func (s *Syncer) audit(ctx context.Context, orgID, projectID, id, action string, diff map[string]any) {
	_ = audit.Write(ctx, s.Pool, audit.Event{OrgID: orgID, ProjectID: projectID, Action: action, TargetType: "config_sync", TargetID: id, Diff: diff})
}

// SettleAfterApply is called after a user applied a file: pending syncs of this project are closed. A pending sync with
// the same content is approved, the others are superseded. The project state is recomputed.
func (s *Syncer) SettleAfterApply(ctx context.Context, projectID string, content []byte, planHash string, failed bool) {
	if failed {
		_, _ = s.Pool.Exec(ctx, `UPDATE projects SET config_sync_state = 'apply_failed' WHERE id = $1`, projectID)
		return
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE config_syncs SET state = CASE WHEN content = $2 THEN 'approved' ELSE 'superseded' END, finished_at = now()
		WHERE project_id = $1 AND state = 'plan_pending'`, projectID, string(content))
	_, _ = s.Pool.Exec(ctx, `UPDATE projects SET config_sync_state = $2 WHERE id = $1`, projectID, s.settledState(ctx, projectID))
}

// MarkDrift records a hand change of a git managed field made with override_git (REQ-CAC-005).
func (s *Syncer) MarkDrift(ctx context.Context, projectID, targetType, targetID, field, actorID string) error {
	if _, err := s.Pool.Exec(ctx, `INSERT INTO config_drift (id, project_id, target_type, target_id, field, actor_id) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))`,
		store.NewID("cdr"), projectID, targetType, targetID, field, actorID); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE projects SET config_sync_state = 'drifted' WHERE id = $1 AND config_sync_state = 'in_sync'`, projectID)
	return err
}

// FilePath cleans the config path: no "..", no leading slash.
func FilePath(p string) (string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "" {
		return DefaultPath, nil
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" || strings.ContainsAny(part, "\x00\\") {
			return "", errors.New("path must be a relative path inside the repository")
		}
	}
	return p, nil
}
