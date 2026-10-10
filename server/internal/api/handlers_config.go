// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/build"
	"github.com/polipan/polipan/server/internal/cac"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/store"
)

// Codes of the config API.
const (
	CodePlanChanged    = "plan_changed"
	CodeInvalidConfig  = "invalid_config"
	CodeMissingSecret  = "missing_secret"
	CodeApplyFailed    = "apply_failed"
	maxConfigFileBytes = 1 << 20
)

// initConfig makes the config syncer: the engine, the apply and the Git side (WP6.2, WP6.3).
func (h *Handlers) initConfig(pool *pgxpool.Pool) {
	ap := &cac.Applier{Pool: pool, Store: h.Store, Backup: h.Backup}
	if h.Secrets != nil {
		ap.Secrets = h.Secrets.Provider
	}
	if h.Orch != nil {
		ap.Deploy = func(ctx context.Context, serviceID, actorID, trigger string) (string, error) {
			return h.Orch.Deploy(orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{}), serviceID, actorID, trigger)
		}
		ap.DomainsChanged = h.Orch.DomainsChanged
		ap.Removed = h.Orch.ServiceRemoved
	}
	h.Config = &cac.Syncer{Pool: pool, Store: h.Store, Applier: ap, Fetch: h.fetchConfig, Status: h.postConfigStatus,
		LinkURL: func(ctx context.Context, projectID string) string {
			if h.Settings.ExternalURL == "" {
				return ""
			}
			return strings.TrimRight(h.Settings.ExternalURL, "/") + "/"
		}}
}

func (h *Handlers) cloner() git.Cloner {
	if h.Git != nil && h.Git.Cloner != nil {
		return h.Git.Cloner
	}
	return git.CLI{}
}

// connectionProvider makes the provider of a Git connection of the org.
func (h *Handlers) connectionProvider(ctx context.Context, orgID, connectionID string) (git.Provider, error) {
	c, err := h.Store.GetGitConnection(ctx, orgID, connectionID)
	if err != nil {
		return nil, err
	}
	return h.providerOf(ctx, c)
}

// fetchConfig reads the config file of a project at a commit with a shallow clone (REQ-CAC-003).
func (h *Handlers) fetchConfig(ctx context.Context, orgID string, src cac.ConfigSource, commit string) ([]byte, error) {
	if h.Git == nil || h.Git.Keys == nil {
		return nil, errors.New("git is not set up on this server")
	}
	path, err := cac.FilePath(src.Path)
	if err != nil {
		return nil, err
	}
	req := git.CloneRequest{URL: src.Repo, Branch: src.Branch, Commit: commit, Policy: h.policy(ctx, orgID)}
	if src.ConnectionID != "" {
		prov, err := h.connectionProvider(ctx, orgID, src.ConnectionID)
		if err != nil {
			return nil, fmt.Errorf("git connection: %w", err)
		}
		req.URL = prov.CloneURL(src.Repo)
		if req.Username, req.Password, err = prov.CloneAuth(ctx); err != nil {
			return nil, fmt.Errorf("git connection: %w", err)
		}
	}
	dir, err := os.MkdirTemp("", "pp-config-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	req.Dir = filepath.Join(dir, "src")
	if err := os.MkdirAll(req.Dir, 0o700); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := h.cloner().Clone(cctx, req); err != nil {
		return nil, err
	}
	file, err := build.ResolveInside(req.Dir, path)
	if err != nil {
		return nil, fmt.Errorf("the file %s is not in the repository", path)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("the file %s is not in the repository", path)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxConfigFileBytes {
		return nil, fmt.Errorf("the file %s is larger than 1 MiB", path)
	}
	return raw, nil
}

// postConfigStatus posts the commit status of a sync, if the provider can (F08 edge cases). Errors are only logged.
func (h *Handlers) postConfigStatus(ctx context.Context, orgID string, src cac.ConfigSource, commit string, st git.Status) {
	if src.ConnectionID == "" || h.Git == nil || h.Git.Keys == nil {
		return
	}
	prov, err := h.connectionProvider(ctx, orgID, src.ConnectionID)
	if err != nil {
		return
	}
	poster, ok := prov.(git.StatusPoster)
	if !ok {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := poster.PostStatus(sctx, git.RepoName(src.Repo), commit, st); err != nil {
		logging.From(ctx).Warn("config sync: commit status not posted", "error", err)
	}
}

func (h *Handlers) configReady(w http.ResponseWriter, r *http.Request) bool {
	if h.Config == nil {
		internalError(w, r, errors.New("config as code is not set up on this server"))
		return false
	}
	return true
}

// ExportProjectConfig makes a polipan.yaml from the state of the project. Secrets are names only (REQ-CAC-009).
func (h *Handlers) ExportProjectConfig(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	if !h.configReady(w, r) {
		return
	}
	if !h.projectExists(w, r, projectID) {
		return
	}
	live, err := cac.Load(r.Context(), h.Config.Pool, h.orgID(r), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	raw, err := cac.Export(live)
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (h *Handlers) projectExists(w http.ResponseWriter, r *http.Request, projectID string) bool {
	_, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID)
	return !fail(w, r, err)
}

// planJSONOf is the plan as the API shows it.
func planOut(pl *cac.Plan) map[string]any {
	out := map[string]any{"plan_hash": pl.PlanHash, "valid": pl.Valid, "errors": pl.Errors, "changes": pl.Changes}
	if len(pl.Warnings) > 0 {
		out["warnings"] = pl.Warnings
	}
	return out
}

// PlanProjectConfig validates a file and returns the diff. It changes nothing (REQ-CAC-004).
func (h *Handlers) PlanProjectConfig(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	if !h.configReady(w, r) || !h.projectExists(w, r, projectID) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w, r, DefaultMaxBodyBytes)
			return
		}
		invalid(w, r, "body", "cannot read the body")
		return
	}
	pl, err := h.Config.BuildPlan(r.Context(), h.orgID(r), projectID, body)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, planOut(pl))
}

// ApplyProjectConfig applies a plan (REQ-CAC-004, 006, 008, 011, 013, 014).
func (h *Handlers) ApplyProjectConfig(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId, params gen.ApplyProjectConfigParams) {
	if !h.configReady(w, r) || !h.projectExists(w, r, projectID) {
		return
	}
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	var body gen.ApplyProjectConfigJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	orgID := h.orgID(r)
	unlock, err := h.Config.Lock(ctx, projectID)
	if fail(w, r, err) {
		return
	}
	defer unlock()
	content := []byte(body.Config)
	pl, err := h.Config.BuildPlan(ctx, orgID, projectID, content)
	if fail(w, r, err) {
		return
	}
	if !pl.Valid {
		if names := pl.MissingSecrets(); len(names) > 0 {
			WriteError(w, r, http.StatusUnprocessableEntity, CodeMissingSecret, "secrets named in the file do not exist: "+strings.Join(names, ", "),
				map[string]any{"names": names, "errors": pl.Errors})
			return
		}
		WriteError(w, r, http.StatusUnprocessableEntity, CodeInvalidConfig, "the file is not valid", map[string]any{"errors": pl.Errors})
		return
	}
	if pl.PlanHash != body.PlanHash {
		WriteError(w, r, http.StatusConflict, CodePlanChanged, "the file or the project changed since the plan. Run plan again", map[string]any{"plan_hash": pl.PlanHash})
		return
	}
	prune := body.Prune != nil && *body.Prune
	pruneData := body.PruneData != nil && *body.PruneData
	if (prune || pruneData) && requireAdmin(w, r, "prune") {
		return
	}
	// A protected environment follows config:apply (P), and the changes of REQ-CAC-014 need an admin.
	if p := PrincipalFrom(ctx); p != nil && !auth.IsAdmin(p.Role) {
		for _, c := range pl.Changes {
			if c.Action != cac.ActionUnmanaged && (c.ProtectedEnvironment || c.RequiresAdmin) {
				WriteError(w, r, http.StatusForbidden, CodeForbidden, "this plan changes a protected environment or a setting that needs an admin",
					map[string]any{"permission": "config:apply", "resource": c.Resource, "name": c.Name})
				return
			}
		}
	}
	res, err := h.Config.Applier.Apply(ctx, pl, cac.Options{OrgID: orgID, ProjectID: projectID, Prune: prune, PruneData: pruneData, Actor: actor(r)})
	if err != nil {
		internalError(w, r, err)
		return
	}
	h.Config.SettleAfterApply(ctx, projectID, content, pl.PlanHash, res.Failed())
	h.auditUser(r, orgID, projectID, "config.apply", "project", projectID, map[string]any{"plan_hash": pl.PlanHash, "prune": prune,
		"prune_data": pruneData, "changes": len(res.Items), "failed": res.Failed()})
	if res.Failed() {
		WriteError(w, r, http.StatusConflict, CodeApplyFailed, "the apply stopped at a failed item. The items before it are applied", map[string]any{"items": res.Items})
		return
	}
	deployments := []gen.Deployment{}
	for _, depID := range res.Deployments {
		if d, err := h.Store.GetDeployment(ctx, orgID, depID); err == nil {
			deployments = append(deployments, deploymentJSON(d))
		}
	}
	out := map[string]any{"deployments": deployments, "items": res.Items}
	if len(res.DeployErrors) > 0 {
		out["deploy_errors"] = res.DeployErrors
	}
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	WriteJSON(w, http.StatusAccepted, out)
}

// ---- config source and git managed fields ----

// configSourceOf checks the config_source of an updateProject body and returns it as JSON. A null value clears it.
func (h *Handlers) configSourceOf(w http.ResponseWriter, r *http.Request, in map[string]any) (map[string]any, bool) {
	if len(in) == 0 {
		return nil, true // an empty object clears the source
	}
	allowed := map[string]bool{"connection_id": true, "repo": true, "branch": true, "path": true}
	out := map[string]any{}
	for k, v := range in {
		s, isStr := v.(string)
		if !allowed[k] || !isStr {
			invalid(w, r, "config_source."+k, "config_source has the keys connection_id, repo, branch and path, all text")
			return nil, false
		}
		out[k] = strings.TrimSpace(s)
	}
	repo, _ := out["repo"].(string)
	branch, _ := out["branch"].(string)
	if repo == "" || branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n:~^?*[\\") {
		invalid(w, r, "config_source", "config_source needs a repo and a branch")
		return nil, false
	}
	if p, _ := out["path"].(string); p != "" {
		if _, err := cac.FilePath(p); err != nil {
			invalid(w, r, "config_source.path", err.Error())
			return nil, false
		}
	}
	if cid, _ := out["connection_id"].(string); cid != "" {
		if _, err := h.Store.GetGitConnection(r.Context(), h.orgID(r), cid); err != nil {
			invalid(w, r, "config_source.connection_id", "the Git connection does not exist")
			return nil, false
		}
	}
	return out, true
}

// denyManaged answers 409 managed_by_git when a service has the field from polipan.yaml (REQ-CAC-005).
func (h *Handlers) denyManaged(w http.ResponseWriter, r *http.Request, serviceID, field string) bool {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if err != nil {
		return false // the handler answers not found itself
	}
	for _, f := range s.GitManaged {
		if f == field {
			WriteError(w, r, http.StatusConflict, CodeManagedByGit, "this value is set by polipan.yaml. Change the file", map[string]any{"fields": []string{field}})
			return true
		}
	}
	return false
}

// valueAt reads a dotted path in a config.
func valueAt(cfg map[string]any, path string) any {
	var cur any = cfg
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return normJSON(cur)
}

// changedManaged lists the git managed fields of a service that an update changes.
func changedManaged(cur *store.Service, newCfg map[string]any, curCfg map[string]any, failover *string, allowLoss *bool) []string {
	var out []string
	for _, f := range cur.GitManaged {
		switch {
		case strings.HasPrefix(f, "config."):
			path := strings.TrimPrefix(f, "config.")
			if !reflect.DeepEqual(valueAt(curCfg, path), valueAt(newCfg, path)) {
				out = append(out, f)
			}
		case f == "failover":
			if failover != nil && *failover != cur.Failover {
				out = append(out, f)
			}
		case f == "allow_data_loss_failover":
			if allowLoss != nil && *allowLoss != cur.AllowDataLoss {
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// guardManagedUpdate refuses the change of a git managed field of a service (REQ-CAC-005). With override_git and an
// admin the change goes on and a drift record is made. It returns true when it wrote an answer.
func (h *Handlers) guardManagedUpdate(w http.ResponseWriter, r *http.Request, cur *store.Service, newCfg map[string]any, failover *string,
	allowLoss *bool, override bool) (drift []string, stop bool) {
	if len(cur.GitManaged) == 0 {
		return nil, false
	}
	var curCfg map[string]any
	_ = jsonUnmarshal(cur.Config, &curCfg)
	changed := changedManaged(cur, newCfg, curCfg, failover, allowLoss)
	if len(changed) == 0 {
		return nil, false
	}
	if !override {
		WriteError(w, r, http.StatusConflict, CodeManagedByGit, "these fields are set by polipan.yaml. Change the file, or use override_git as an admin",
			map[string]any{"fields": changed})
		return nil, true
	}
	if requireAdmin(w, r, "override_git") {
		return nil, true
	}
	return changed, false
}

// recordDrift marks the project drifted for the fields that an admin changed by hand.
func (h *Handlers) recordDrift(r *http.Request, cur *store.Service, fields []string) {
	if h.Config == nil {
		return
	}
	actorID := ""
	if p := PrincipalFrom(r.Context()); p != nil && p.User != nil {
		actorID = p.User.ID
	}
	for _, f := range fields {
		if err := h.Config.MarkDrift(r.Context(), cur.ProjectID, "service", cur.ID, f, actorID); err != nil {
			logging.From(r.Context()).Warn("drift record not written", "error", err)
		}
	}
	h.auditUser(r, cur.OrgID, cur.ProjectID, "config.drift", "service", cur.ID, map[string]any{"fields": fields})
}

// ---- push trigger ----

// syncConfigPush starts the config sync of every project whose config_source is the repo and the branch of a push
// (REQ-CAC-003, REQ-CAC-013). It runs in the background: the webhook answers at once. It returns the project ids.
func (h *Handlers) syncConfigPush(ctx context.Context, hook *store.Webhook, p *git.PushEvent) []string {
	if h.Config == nil || p == nil || p.Branch == "" || p.Commit == "" {
		return nil
	}
	rows, err := h.Config.Pool.Query(ctx, `SELECT id, config_source FROM projects WHERE org_id = $1 AND deleted_at IS NULL AND config_source IS NOT NULL
		AND config_source->>'branch' = $2`, hook.OrgID, p.Branch)
	if err != nil {
		logging.From(ctx).Warn("config sync: projects not listed", "error", err)
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			continue
		}
		src, ok := cac.ParseSource(raw)
		if !ok || git.RepoName(src.Repo) != p.Repo {
			continue
		}
		if hook.ConnectionID != "" && src.ConnectionID != hook.ConnectionID {
			continue
		}
		if hook.ConnectionID == "" && !h.hookBelongsTo(ctx, hook, id) {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		pid := id
		bg := context.WithoutCancel(ctx)
		bg = audit.WithRequest(bg, &audit.Request{ActorType: audit.ActorSystem, ActorID: "webhook:" + hook.ID, RequestID: RequestIDFrom(ctx)})
		h.syncWG.Add(1)
		go func() {
			defer h.syncWG.Done()
			if _, _, err := h.Config.SyncPush(bg, cac.Push{OrgID: hook.OrgID, ProjectID: pid, Commit: p.Commit, Author: p.Author}); err != nil {
				logging.From(bg).Warn("config sync failed", "project_id", pid, "error", err)
			}
		}()
	}
	return ids
}

// hookBelongsTo tells if the service of a service webhook is in the project.
func (h *Handlers) hookBelongsTo(ctx context.Context, hook *store.Webhook, projectID string) bool {
	if hook.ServiceID == "" {
		return false
	}
	var n int
	_ = h.Config.Pool.QueryRow(ctx, `SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id WHERE s.id = $1 AND e.project_id = $2`,
		hook.ServiceID, projectID).Scan(&n)
	return n > 0
}

// WaitConfigSyncs waits for the syncs that webhooks started. Tests use it.
func (h *Handlers) WaitConfigSyncs() { h.syncWG.Wait() }

func jsonUnmarshal(raw []byte, dst any) error { return json.Unmarshal(raw, dst) }

// normJSON makes a value comparable: it goes through JSON, so 5 and 5.0 are the same.
func normJSON(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}
