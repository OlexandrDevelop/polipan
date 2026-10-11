// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/promote"
	"github.com/polipan/polipan/server/internal/store"
)

// Result is what a PR event did in one project.
type Result struct {
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment,omitempty"`
	// Action is created, updated, reopened, closing, waiting_for_label, limit_reached, failed or ignored.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type candidate struct {
	ProjectID, OrgID string
}

// candidates finds the projects of the org that have a git service that this webhook can see.
func (s *Service) candidates(ctx context.Context, hook *store.Webhook) ([]candidate, error) {
	// A service that has a plain repository URL (no connection) is also linked to the connection of the same server:
	// the URL starts with the base URL of the connection.
	var base string
	if hook.ConnectionID != "" {
		_ = s.Pool.QueryRow(ctx, `SELECT COALESCE(base_url, '') FROM git_connections WHERE id = $1`, hook.ConnectionID).Scan(&base)
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT p.id, p.org_id FROM services sv
		JOIN environments e ON e.id = sv.environment_id AND e.deleted_at IS NULL AND e.kind <> 'preview'
		JOIN projects p ON p.id = e.project_id AND p.deleted_at IS NULL
		WHERE sv.deleted_at IS NULL AND sv.source_type = 'git' AND p.org_id = $1
		  AND (($2 <> '' AND sv.config->'git'->>'connection_id' = $2) OR sv.id = $3
		       OR ($4 <> '' AND COALESCE(sv.config->'git'->>'connection_id', '') = '' AND starts_with(sv.config->'git'->>'repo', $4 || '/')))
		  AND COALESCE(p.settings->'previews'->>'enabled', 'false') = 'true'
		ORDER BY p.id`, hook.OrgID, hook.ConnectionID, hook.ServiceID, strings.TrimRight(base, "/"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ProjectID, &c.OrgID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// baseService is a service of the base environment.
type baseService struct {
	ID, Name, SourceType string
	Source               *git.Source
}

func (s *Service) baseServices(ctx context.Context, envID string) ([]baseService, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, source_type, config FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY name`, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []baseService
	for rows.Next() {
		var b baseService
		var raw []byte
		if err := rows.Scan(&b.ID, &b.Name, &b.SourceType, &raw); err != nil {
			return nil, err
		}
		if src, ok := git.SourceOf(raw); ok && b.SourceType == "git" {
			b.Source = src
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// connectionFor finds the Git connection that posts the status and the comment of a service: the one of the service, or
// the connection of the same server as the repository URL.
func (s *Service) connectionFor(ctx context.Context, orgID string, src *git.Source) string {
	if src == nil {
		return ""
	}
	if src.ConnectionID != "" {
		return src.ConnectionID
	}
	var id string
	_ = s.Pool.QueryRow(ctx, `SELECT id FROM git_connections WHERE org_id = $1 AND COALESCE(base_url, '') <> '' AND starts_with($2, rtrim(base_url, '/') || '/')
		ORDER BY id LIMIT 1`, orgID, src.Repo).Scan(&id)
	return id
}

func tracks(src *git.Source, repo string) bool {
	return src != nil && git.RepoName(src.Repo) == repo
}

// HandlePR acts on a pull request event of a verified webhook (REQ-PREV-001). Every project with previews that
// builds the repository of the PR gets its answer.
func (s *Service) HandlePR(ctx context.Context, hook *store.Webhook, pr *git.PullRequest) []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	cands, err := s.candidates(ctx, hook)
	if err != nil {
		s.Log.Error("preview: find projects", "error", err)
		return nil
	}
	var out []Result
	for _, c := range cands {
		r := s.handleProject(ctx, c, pr)
		if r.Action != "ignored" || r.Reason != "" {
			s.Log.Info("preview: pull request event", "project", c.ProjectID, "repo", pr.Repo, "number", pr.Number, "action", pr.Action, "result", r.Action, "reason", r.Reason)
		}
		out = append(out, r)
	}
	return out
}

func (s *Service) findEnv(ctx context.Context, projectID, repo string, number int) (*envState, error) {
	e, err := scanEnv(s.Pool.QueryRow(ctx, `SELECT `+envCols+` FROM environments e JOIN projects p ON p.id = e.project_id
		WHERE e.project_id = $1 AND e.kind = 'preview' AND e.preview_repo = $2 AND e.preview_pr_number = $3 AND e.deleted_at IS NULL`, projectID, repo, number))
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return e, nil
}

func (s *Service) handleProject(ctx context.Context, c candidate, pr *git.PullRequest) Result {
	res := Result{ProjectID: c.ProjectID, Action: "ignored"}
	cfg, err := s.projectConfig(ctx, c.ProjectID)
	if err != nil || !cfg.Enabled {
		return res
	}
	baseID, err := s.baseEnvironment(ctx, c.ProjectID, cfg)
	if err != nil {
		res.Reason = "no_base_environment"
		return res
	}
	svcs, err := s.baseServices(ctx, baseID)
	if err != nil {
		res.Action, res.Reason = "failed", err.Error()
		return res
	}
	var trackedSvc *baseService
	for i := range svcs {
		if tracks(svcs[i].Source, pr.Repo) {
			trackedSvc = &svcs[i]
			break
		}
	}
	if trackedSvc == nil {
		res.Reason = "no_service_uses_the_repository"
		return res
	}
	env, err := s.findEnv(ctx, c.ProjectID, pr.Repo, pr.Number)
	if err != nil {
		res.Action, res.Reason = "failed", err.Error()
		return res
	}
	if env != nil {
		res.Environment = env.Slug
	}
	fork := pr.IsFork()
	conn := s.connectionFor(ctx, c.OrgID, trackedSvc.Source)

	if pr.Action == git.PRClosed {
		if fork && cfg.FromForks {
			// The label does not stay on a closed PR: a push to a closed PR sends no event, so the label could not be
			// told from an approval of the new commit when the PR is reopened.
			s.removeLabel(ctx, c.OrgID, conn, pr)
		}
		if env == nil || env.State == StateClosing {
			return res
		}
		if err := s.markClosing(ctx, env, cfg); err != nil {
			res.Action, res.Reason = "failed", err.Error()
			return res
		}
		res.Action = "closing"
		return res
	}
	if fork && !cfg.FromForks { // REQ-PREV-009: no environment
		res.Reason = "fork_previews_are_off"
		return res
	}
	if fork {
		// A fork commit is built only after a maintainer approved that commit with the label. The labels stay on a PR
		// when it is closed or pushed to, so only the commit that was built (approved_sha) is known to be approved.
		approved := env != nil && pr.HeadSHA == env.str("approved_sha")
		if pr.Action != git.PRLabels && !approved {
			// A new push, or a reopened PR with a commit that nobody approved: the label is taken away and nothing is built.
			removed := s.removeLabel(ctx, c.OrgID, conn, pr)
			if env != nil {
				// label_absent: the label is known to be off. When the removal failed, the label may still be there, and
				// a later event with the label does not approve the new commit: a maintainer must take it off and add it again.
				_ = s.setSettings(ctx, env.ID, map[string]any{"waiting_for_label": true, "label_absent": removed, "head_sha": pr.HeadSHA})
				env.Settings["waiting_for_label"] = true
				s.report(ctx, env, "pending", "A maintainer must add the label "+LabelName+" to build the new commit", nil)
			}
			res.Action = "waiting_for_label"
			return res
		}
		if pr.Action == git.PRLabels {
			if !pr.HasLabel(LabelName) {
				if env != nil {
					_ = s.setSettings(ctx, env.ID, map[string]any{"label_absent": true})
				}
				res.Action, res.Reason = "waiting_for_label", "the label "+LabelName+" is missing"
				return res
			}
			if env != nil && !approved && !env.boolean("label_absent") {
				res.Action, res.Reason = "waiting_for_label", "the label was not removed since the last push: remove it and add it again"
				return res
			}
		}
	}
	branch := pr.HeadRef
	if fork {
		branch = pr.BaseRef // the commit is fetched by its id from the base repository
	}
	if env == nil {
		if pr.Action == git.PRLabels && !fork {
			return res
		}
		return s.create(ctx, c, cfg, baseID, conn, pr, branch, fork)
	}
	switch {
	case env.State == StateClosing:
		// The PR was reopened within the grace time: back to active, no cleanup (F03 States).
		if err := s.setSettings(ctx, env.ID, map[string]any{"preview_state": StateActive, "close_at": ""}); err != nil {
			res.Action, res.Reason = "failed", err.Error()
			return res
		}
		res.Action = "reopened"
		if pr.HeadSHA == env.str("head_sha") {
			return res
		}
	case pr.Action == git.PRLabels && pr.HeadSHA == env.str("head_sha") && !env.boolean("waiting_for_label"):
		return res
	case pr.Action != git.PRSynchronize && pr.Action != git.PRLabels && pr.HeadSHA == env.str("head_sha"):
		return res // opened or reopened again with the same commit
	}
	if err := s.push(ctx, env, cfg, pr, branch); err != nil {
		res.Action, res.Reason = "failed", err.Error()
		return res
	}
	if res.Action == "ignored" {
		res.Action = "updated"
	}
	return res
}

// create makes the environment of a new PR and starts the deployments (REQ-PREV-003).
func (s *Service) create(ctx context.Context, c candidate, cfg Config, baseID, conn string, pr *git.PullRequest, branch string, fork bool) Result {
	res := Result{ProjectID: c.ProjectID, Action: "failed", Environment: Slug(pr.Number)}
	now := s.Clock.Now()
	env, svcs, err := s.createEnv(ctx, c, cfg, baseID, conn, Slug(pr.Number), pr.Repo, pr.Number, pr.HeadSHA, branch, fork, pr.Title, pr.URL, now, store.Actor{})
	switch {
	case errors.Is(err, store.ErrPreviewLimit):
		// REQ-PREV-008: no environment, a failure status with the reason.
		s.postStatus(ctx, c.OrgID, conn, pr.Repo, pr.HeadSHA, git.Status{State: git.StatusFailure, Context: StatusContext,
			Description: "Preview limit reached: " + itoa(cfg.Max()) + " active previews. Close a pull request or delete a preview."})
		res.Action, res.Reason = "limit_reached", "preview_max_active"
		return res
	case err != nil:
		res.Reason = err.Error()
		s.Log.Error("preview: create", "project", c.ProjectID, "pr", pr.Number, "error", err)
		if hostErr := (*store.DomainUsedError)(nil); errors.As(err, &hostErr) {
			s.postStatus(ctx, c.OrgID, conn, pr.Repo, pr.HeadSHA, git.Status{State: git.StatusFailure, Context: StatusContext,
				Description: "The host " + hostErr.Host + " is in use by another service"})
		}
		return res
	}
	res.Environment, res.Action = env.Slug, "created"
	s.report(ctx, env, "pending", "Building the preview", nil)
	if err := s.deployAll(ctx, env, svcs, cfg, pr.HeadSHA, branch, false); err != nil {
		res.Action, res.Reason = "failed", err.Error()
	}
	return res
}

// createEnv is the environment and the copy of the services (also used by the manual create).
func (s *Service) createEnv(ctx context.Context, c candidate, cfg Config, baseID, conn, slug, repo string, number int, sha, branch string, fork bool,
	title, prURL string, now time.Time, actor store.Actor) (*envState, []store.PreviewService, error) {
	domain := s.previewDomain(ctx, c.OrgID)
	settings := map[string]any{"preview_state": StateCreating, "connection_id": conn, "head_sha": sha, "head_ref": branch,
		"pr_title": title, "pr_url": prURL, "preview_pending": map[string]any{}, "waiting_for_label": false, "approved_sha": sha}
	in := store.PreviewCreate{OrgID: c.OrgID, ProjectID: c.ProjectID, BaseEnvID: baseID, Slug: slug, Repo: repo, Number: number, Fork: fork,
		Settings: settings, TTL: cfg.TTL(), Now: now, MaxActive: cfg.Max(), Actor: actor,
		HostFor: func(service string) string { return Host(slug, service, domain) },
		Tracks: func(_, srcType string, sc map[string]any) bool {
			return srcType == "git" && tracks(sourceOfMap(sc), repo)
		},
		Mutate: func(_, srcType string, sc map[string]any) map[string]any {
			return mutateConfig(sc, srcType, cfg, repo, branch)
		},
	}
	id, svcs, err := s.Store.CreatePreviewEnvironment(ctx, s.Secrets.Provider, in)
	if err != nil {
		return nil, nil, err
	}
	env, err := scanEnv(s.Pool.QueryRow(ctx, `SELECT `+envCols+` FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = $1`, id))
	return env, svcs, err
}

// mutateConfig makes the config of a copied service (REQ-PREV-003, REQ-PREV-013): one replica, no failover, no fixed
// host port, no backup, the strategy of the previews, the node selector, and no deploy by push for a git service.
func mutateConfig(cfg map[string]any, srcType string, pc Config, repo, branch string) map[string]any {
	for _, k := range []string{"domains", "failover", "host_port", "backup", "canary", "placement"} {
		delete(cfg, k)
	}
	cfg["replicas"] = 1
	cfg["strategy"] = pc.DeployStrategy()
	if len(pc.NodeSelector) > 0 {
		cfg["placement"] = map[string]any{"labels": pc.NodeSelector}
	}
	if g, ok := cfg["git"].(map[string]any); ok {
		g["auto_deploy"] = false // the PR event deploys, not a push
		if src := sourceOfMap(cfg); tracks(src, repo) {
			if branch != "" {
				g["branch"] = branch
			}
			if len(pc.SeedCommand) > 0 {
				cfg["pre_deploy"] = withSeed(cfg["pre_deploy"], pc.SeedCommand)
			}
		}
	}
	return cfg
}

// withSeed adds the seed command after the pre_deploy command of the base. It runs once: after the first deployment
// that went live, the pre_deploy command of the base is restored (afterFirstSuccess).
func withSeed(pre any, seed []string) []any {
	var parts []string
	if l, ok := pre.([]any); ok {
		var argv []string
		for _, a := range l {
			if str, ok := a.(string); ok {
				argv = append(argv, shellQuote(str))
			}
		}
		if len(argv) > 0 {
			parts = append(parts, strings.Join(argv, " "))
		}
	}
	var sq []string
	for _, a := range seed {
		sq = append(sq, shellQuote(a))
	}
	parts = append(parts, strings.Join(sq, " "))
	if len(parts) == 1 && len(seed) > 0 && pre == nil {
		out := make([]any, len(seed))
		for i, a := range seed {
			out[i] = a
		}
		return out
	}
	return []any{"sh", "-c", strings.Join(parts, " && ")}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// afterFirstSuccess takes the seed command away from the pre_deploy of the preview services. The seed runs once.
func (s *Service) afterFirstSuccess(ctx context.Context, env *envState) {
	if env.boolean("seed_applied") {
		return
	}
	cfg, err := s.projectConfig(ctx, env.ProjectID)
	if err != nil || len(cfg.SeedCommand) == 0 {
		return
	}
	_, err = s.Pool.Exec(ctx, `UPDATE services s SET config = CASE WHEN b.config ? 'pre_deploy' THEN jsonb_set(s.config, '{pre_deploy}', b.config->'pre_deploy')
		ELSE s.config - 'pre_deploy' END, updated_at = now()
		FROM services b WHERE s.environment_id = $1 AND b.environment_id = $2 AND b.name = s.name AND b.deleted_at IS NULL
		AND s.deleted_at IS NULL AND s.source_type = 'git'`, env.ID, env.BaseEnvID)
	if err != nil {
		s.Log.Warn("preview: seed command not removed", "environment", env.Slug, "error", err)
		return
	}
	env.Settings["seed_applied"] = true
	_ = s.setSettings(ctx, env.ID, map[string]any{"seed_applied": true})
}

func sourceOfMap(cfg map[string]any) *git.Source {
	raw, err := jsonMarshal(cfg)
	if err != nil {
		return nil
	}
	src, _ := git.SourceOf(raw)
	return src
}

// push builds the new commit of a PR in the existing preview (REQ-PREV-004). It extends the TTL.
func (s *Service) push(ctx context.Context, env *envState, cfg Config, pr *git.PullRequest, branch string) error {
	now := s.Clock.Now()
	kv := map[string]any{"head_sha": pr.HeadSHA, "head_ref": branch, "waiting_for_label": false, "label_absent": false, "approved_sha": pr.HeadSHA, "preview_failure": "", "close_at": ""}
	if env.State == StateFailed || env.State == StateClosing {
		kv["preview_state"] = StateCreating
	}
	if err := s.setSettings(ctx, env.ID, kv); err != nil {
		return err
	}
	var expires *time.Time
	if ttl := cfg.TTL(); ttl > 0 {
		t := now.Add(ttl)
		expires = &t
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE environments SET preview_last_push_at = $2, preview_expires_at = $3 WHERE id = $1`, env.ID, now, expires); err != nil {
		return err
	}
	env.Settings["head_sha"], env.Settings["head_ref"] = pr.HeadSHA, branch
	s.report(ctx, env, "pending", "Building the new commit", nil)
	svcs, err := s.envServices(ctx, env.ID, pr.Repo)
	if err != nil {
		return err
	}
	return s.deployAll(ctx, env, svcs, cfg, pr.HeadSHA, branch, true)
}

// envServices lists the services of a preview with the flag that says if they build the commit of the PR.
func (s *Service) envServices(ctx context.Context, envID, repo string) ([]store.PreviewService, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, type, source_type, config FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY name`, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.PreviewService
	for rows.Next() {
		var p store.PreviewService
		var raw []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.SourceType, &raw); err != nil {
			return nil, err
		}
		if src, ok := git.SourceOf(raw); ok && p.SourceType == "git" {
			p.Tracks = tracks(src, repo)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// deployAll queues the deployments of a preview. Databases come first. A service that builds the PR gets a build of
// the commit. The other services run the live Release of the base environment, with the same digest, when it has one
// (REQ-PREV-003). onlyTracked is set for a new push: only the services of the PR are built again.
func (s *Service) deployAll(ctx context.Context, env *envState, svcs []store.PreviewService, cfg Config, sha, branch string, onlyTracked bool) error {
	sort.SliceStable(svcs, func(i, j int) bool { return (svcs[i].Type == "database") && (svcs[j].Type != "database") })
	pending := env.pending()
	var firstErr error
	for _, sv := range svcs {
		if onlyTracked && !sv.Tracks {
			continue
		}
		id, err := s.queue(ctx, env, sv, cfg, sha, branch)
		if err != nil {
			s.Log.Warn("preview: deployment not queued", "environment", env.Slug, "service", sv.Name, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", sv.Name, err)
			}
			continue
		}
		pending[sv.ID] = id
	}
	kv := map[string]any{"preview_pending": pending}
	if firstErr != nil && len(pending) == 0 {
		kv["preview_state"], kv["preview_failure"], kv["preview_failure_public"] = StateFailed, reasonOf(firstErr), publicReason(firstErr)
	}
	if err := s.setSettings(ctx, env.ID, kv); err != nil {
		return err
	}
	if firstErr != nil {
		if len(pending) == 0 {
			env.Settings["preview_failure"], env.Settings["preview_failure_public"] = reasonOf(firstErr), publicReason(firstErr)
			s.report(ctx, env, "failure", "The preview failed: "+publicReason(firstErr), nil)
		}
		return firstErr
	}
	return nil
}

// publicReason is the reason on the PR: a code, no detail of the platform.
func publicReason(err error) string {
	if errors.Is(err, orchestrator.ErrNoNode) {
		return "node_unavailable"
	}
	return "deploy_not_started"
}

func reasonOf(err error) string {
	if errors.Is(err, orchestrator.ErrNoNode) {
		return "node_unavailable: no node is ready to run the preview"
	}
	return err.Error()
}

// queue starts the deployment of one service.
func (s *Service) queue(ctx context.Context, env *envState, sv store.PreviewService, cfg Config, sha, branch string) (string, error) {
	p := orchestrator.DeployParams{ServiceID: sv.ID, Trigger: "pull_request", Strategy: cfg.DeployStrategy()}
	dctx := ctx
	switch {
	case sv.Tracks:
		dctx = orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{Commit: sha, Branch: branch})
	case sv.Type != "database" && sv.BaseLiveRelease != nil:
		// The live Release of the base service: the same digest, no build (REQ-PREV-003).
		pl := &promote.Planner{DB: s.Pool}
		plan, _, err := pl.ForRelease(ctx, *sv.BaseLiveRelease, promote.Options{TargetEnvID: env.ID, AllowPreview: true, KeepMissing: true})
		if err == nil && len(plan.Items) == 1 && plan.Items[0].SkippedReason == "" {
			p.Prepared = plan.Items[0].Prepared
		} else if err != nil {
			s.Log.Warn("preview: the live release of the base service is not used", "service", sv.Name, "error", err)
		}
	}
	if sv.Type == "database" {
		p.Strategy = "recreate"
	}
	id, _, err := s.Orch.DeployWith(dctx, p)
	return id, err
}

// markClosing puts a preview in the state closing. The environment is deleted after the grace time (REQ-PREV-007).
func (s *Service) markClosing(ctx context.Context, env *envState, cfg Config) error {
	closeAt := s.Clock.Now().Add(cfg.Grace()).UTC().Format(time.RFC3339)
	return s.setSettings(ctx, env.ID, map[string]any{"preview_state": StateClosing, "close_at": closeAt})
}

// removeLabel takes the label off a fork PR. It returns true only when the provider confirmed it.
func (s *Service) removeLabel(ctx context.Context, orgID, conn string, pr *git.PullRequest) bool {
	if s.Provider == nil || conn == "" {
		return false
	}
	prov, err := s.Provider(ctx, orgID, conn)
	if err != nil {
		s.Log.Warn("preview: provider", "error", err)
		return false
	}
	c, ok := prov.(git.Commenter)
	if !ok {
		return false
	}
	if err := c.RemoveLabel(ctx, pr.Repo, pr.Number, LabelName); err != nil {
		s.Log.Warn("preview: label not removed", "repo", pr.Repo, "number", pr.Number, "error", err)
		return false
	}
	return true
}

func (s *Service) postStatus(ctx context.Context, orgID, conn, repo, sha string, st git.Status) {
	if s.Provider == nil || conn == "" || sha == "" {
		return
	}
	prov, err := s.Provider(ctx, orgID, conn)
	if err != nil {
		s.Log.Warn("preview: provider", "error", err)
		return
	}
	if p, ok := prov.(git.StatusPoster); ok {
		if err := p.PostStatus(ctx, repo, sha, st); err != nil {
			s.Log.Warn("preview: status not posted", "repo", repo, "error", err)
		}
	}
}
