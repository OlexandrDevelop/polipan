// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

var terminal = map[string]bool{"live": true, "failed": true, "rolled_back": true, "cancelled": true, "superseded": true}

// Tick does one pass: it reports the deployments that finished, deletes the previews whose TTL ended, and deletes the
// closed previews after the grace time.
func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.list(ctx, `e.kind = 'preview' AND e.deleted_at IS NULL`)
	if err != nil {
		s.Log.Error("preview: read the previews", "error", err)
		return
	}
	now := s.Clock.Now()
	for _, env := range list {
		if ctx.Err() != nil {
			return
		}
		switch {
		case env.State == StateClosing:
			if t, err := time.Parse(time.RFC3339, env.str("close_at")); err == nil && !now.Before(t) {
				if err := s.deleteLocked(ctx, env, "pr_closed", ""); err != nil {
					s.Log.Warn("preview: delete after the PR closed", "environment", env.Slug, "error", err)
				}
			}
		case env.Expires != nil && !now.Before(*env.Expires):
			s.expireOne(ctx, env)
		case len(env.pending()) > 0:
			s.checkPending(ctx, env)
		}
	}
}

func (s *Service) list(ctx context.Context, where string, args ...any) ([]*envState, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+envCols+` FROM environments e JOIN projects p ON p.id = e.project_id AND p.deleted_at IS NULL
		WHERE `+where+` ORDER BY e.created_at, e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*envState
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// expireOne deletes a preview that had no push for preview_ttl and tells the PR (REQ-PREV-008).
func (s *Service) expireOne(ctx context.Context, env *envState) {
	cfg, _ := s.projectConfig(ctx, env.ProjectID)
	days := intOr(cfg.TTLDays, DefaultTTLDays)
	s.comment(ctx, env, fmt.Sprintf("The preview was deleted: there was no push for %d days.", days))
	s.postStatus(ctx, env.OrgID, env.str("connection_id"), env.Repo, env.str("head_sha"), git.Status{State: git.StatusFailure, Context: StatusContext,
		Description: fmt.Sprintf("The preview was deleted after %d days without a push", days)})
	if err := s.deleteLocked(ctx, env, "ttl", ""); err != nil {
		s.Log.Warn("preview: delete after the TTL", "environment", env.Slug, "error", err)
	}
}

// checkPending looks at the deployments of a preview. When all ended, it sets the state and tells the PR.
func (s *Service) checkPending(ctx context.Context, env *envState) {
	pending := env.pending()
	var failed, publicText []string
	allLive := true
	for svc, dep := range pending {
		var state string
		var reason, detail *string
		var name string
		err := s.Pool.QueryRow(ctx, `SELECT d.state, d.failure_reason, d.failure_detail, s.name FROM deployments d JOIN services s ON s.id = d.service_id
			WHERE d.id = $1 AND d.service_id = $2`, dep, svc).Scan(&state, &reason, &detail, &name)
		if err != nil {
			s.Log.Warn("preview: read a deployment", "deployment", dep, "error", err)
			return
		}
		if !terminal[state] {
			return // still running
		}
		if state == "superseded" {
			continue
		}
		if state != "live" {
			allLive = false
			msg := name + ": " + state
			if reason != nil {
				msg = name + ": " + *reason
				if detail != nil && *detail != "" {
					msg += " (" + trimDetail(*detail) + ")"
				}
			}
			failed = append(failed, msg)
			// The text on the PR is public: only the service and the reason code, no detail of the node or the build.
			short := name + ": " + state
			if reason != nil {
				short = name + ": " + *reason
			}
			publicText = append(publicText, short)
		}
	}
	kv := map[string]any{"preview_pending": map[string]any{}}
	if allLive {
		kv["preview_state"], kv["preview_failure"] = StateActive, ""
	} else {
		kv["preview_state"], kv["preview_failure"] = StateFailed, strings.Join(failed, "; ")
		kv["preview_failure_public"] = strings.Join(publicText, "; ")
	}
	if err := s.setSettings(ctx, env.ID, kv); err != nil {
		s.Log.Error("preview: set the state", "environment", env.Slug, "error", err)
		return
	}
	for k, v := range kv {
		env.Settings[k] = v
	}
	if allLive {
		s.afterFirstSuccess(ctx, env)
		urls := s.urls(ctx, env.ID)
		s.report(ctx, env, git.StatusSuccess, "The preview is ready", urls)
		return
	}
	s.report(ctx, env, git.StatusFailure, "The preview failed: "+strings.Join(publicText, "; "), nil)
}

func trimDetail(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// urls are the URLs of the services of a preview.
func (s *Service) urls(ctx context.Context, envID string) []string {
	rows, err := s.Pool.Query(ctx, `SELECT d.host, d.https FROM domains d JOIN services sv ON sv.id = d.service_id
		WHERE sv.environment_id = $1 AND sv.deleted_at IS NULL ORDER BY sv.name, d.host`, envID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var host string
		var https bool
		if err := rows.Scan(&host, &https); err != nil {
			return out
		}
		scheme := "https://"
		if !https {
			scheme = "http://"
		}
		out = append(out, scheme+host)
	}
	return out
}

// report posts the commit status, and for a result also the comment (REQ-PREV-006). The comment is one comment that
// is changed at each report.
func (s *Service) report(ctx context.Context, env *envState, state, text string, urls []string) {
	conn := env.str("connection_id")
	if conn == "" || env.Number <= 0 || s.Provider == nil {
		return
	}
	target := s.ExternalURL
	if len(urls) > 0 {
		target = urls[0]
	}
	s.postStatus(ctx, env.OrgID, conn, env.Repo, env.str("head_sha"), git.Status{State: state, Description: text, Context: StatusContext, TargetURL: target})
	if state == git.StatusPending {
		return
	}
	var b strings.Builder
	b.WriteString(CommentMarker + "\n### Polipan preview\n\n")
	switch state {
	case git.StatusSuccess:
		b.WriteString("The preview is ready.\n\n")
		if len(urls) == 0 {
			b.WriteString("No URL: the organization has no preview domain.\n\n")
		}
		for _, u := range urls {
			b.WriteString("- " + u + "\n")
		}
		b.WriteString("\n")
	default:
		b.WriteString("The preview failed.\n\n```\n" + env.str("preview_failure_public") + "\n```\n\nThe details are in Polipan.\n\n")
	}
	if sha := env.str("head_sha"); len(sha) >= 7 {
		b.WriteString("Commit: `" + sha[:7] + "`\n")
	}
	s.comment(ctx, env, b.String())
}

// comment writes or changes the one comment of the preview on the PR.
func (s *Service) comment(ctx context.Context, env *envState, body string) {
	conn := env.str("connection_id")
	if conn == "" || env.Number <= 0 || s.Provider == nil {
		return
	}
	prov, err := s.Provider(ctx, env.OrgID, conn)
	if err != nil {
		s.Log.Warn("preview: provider", "error", err)
		return
	}
	c, ok := prov.(git.Commenter)
	if !ok {
		return
	}
	if !strings.HasPrefix(body, CommentMarker) {
		body = CommentMarker + "\n### Polipan preview\n\n" + body + "\n"
	}
	id, err := c.UpsertComment(ctx, env.Repo, env.Number, env.str("comment_id"), body)
	if err != nil {
		s.Log.Warn("preview: comment not written", "repo", env.Repo, "number", env.Number, "error", err)
		return
	}
	if id != env.str("comment_id") {
		env.Settings["comment_id"] = id
		_ = s.setSettings(ctx, env.ID, map[string]any{"comment_id": id})
	}
}

// ---- delete ----

var errBusy = errors.New("preview: a deployment of the preview still runs")

// Delete deletes a preview now: its containers, volumes, domains and images (REQ-PREV-007, REQ-PREV-014).
// userID is the user who asked, empty for the system.
func (s *Service) Delete(ctx context.Context, orgID, envID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	envs, err := s.list(ctx, `e.id = $1 AND p.org_id = $2 AND e.kind = 'preview' AND e.deleted_at IS NULL`, envID, orgID)
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		return errStore404
	}
	return s.deleteLocked(ctx, envs[0], "manual", userID)
}

func (s *Service) deleteLocked(ctx context.Context, env *envState, reason, userID string) error {
	ids, err := queryStrings(ctx, s.Pool, `SELECT id FROM services WHERE environment_id = $1 AND deleted_at IS NULL`, env.ID)
	if err != nil {
		return err
	}
	// Stop the deployments that run. A deployment cannot be cancelled in its last states: the next pass tries again.
	active, err := queryStrings(ctx, s.Pool, `SELECT id FROM deployments WHERE environment_id = $1 AND state IN
		('queued','building','image_ready','starting','health_checking','shifting','switching','draining')`, env.ID)
	if err != nil {
		return err
	}
	for _, d := range active {
		if err := s.Orch.Cancel(ctx, d, ""); err != nil && !errors.Is(err, orchestrator.ErrNotCancellable) && !errors.Is(err, orchestrator.ErrNotFound) {
			s.Log.Warn("preview: cancel a deployment", "deployment", d, "error", err)
		}
	}
	if left, _ := queryStrings(ctx, s.Pool, `SELECT id FROM deployments WHERE environment_id = $1 AND state IN
		('building','image_ready','starting','health_checking','shifting','switching','draining')`, env.ID); len(left) > 0 {
		return errBusy
	}
	// The containers and the volumes first. A node that is not connected keeps the preview: the data would stay behind.
	if remover, ok := s.Orch.(orchestrator.DataRemover); ok {
		for _, id := range ids {
			if err := remover.RemoveServiceData(ctx, id); err != nil {
				return fmt.Errorf("remove the data of a service: %w", err)
			}
		}
	}
	if _, err := s.Store.SoftDeleteEnvironment(ctx, env.OrgID, env.ID); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Orch.ServiceRemoved(ctx, id); err != nil {
			s.Log.Error("preview: service removal failed", "service", id, "error", err)
		}
	}
	if ir, ok := s.Orch.(orchestrator.ImageRemover); ok {
		if err := ir.RemoveServiceImages(ctx, ids); err != nil {
			s.Log.Warn("preview: images not removed", "environment", env.Slug, "error", err)
		}
	}
	if s.Secrets != nil {
		if err := s.Store.PurgePreviewEntries(ctx, s.Secrets.Provider, env.ID); err != nil {
			s.Log.Warn("preview: variables and secrets not removed", "environment", env.Slug, "error", err)
		}
	}
	_ = s.setSettings(ctx, env.ID, map[string]any{"preview_state": StateDeleted, "preview_pending": map[string]any{}})
	ev := audit.Event{OrgID: env.OrgID, ActorType: audit.ActorSystem, ActorID: "preview", Action: "preview.deleted", TargetType: "environment", TargetID: env.ID,
		ProjectID: env.ProjectID, Diff: map[string]any{"reason": reason, "repo": env.Repo, "pr_number": env.Number, "environment": env.Slug}}
	if userID != "" {
		ev.ActorType, ev.ActorID = audit.ActorUser, userID
	}
	if err := audit.Write(ctx, s.Pool, ev); err != nil {
		s.Log.Error("preview: audit event not written", "error", err)
	}
	s.Log.Info("preview deleted", "environment", env.Slug, "project", env.ProjectID, "reason", reason)
	return nil
}
