// SPDX-License-Identifier: Apache-2.0

package api

import (
	"crypto/subtle"
	"errors"
	"github.com/polipan/polipan/server/internal/audit"
	"io"
	"net/http"

	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/store"
)

func unauthorizedHook(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "the webhook signature is missing or wrong", nil)
}

// ReceiveGitWebhook handles a push from a Git provider (REQ-APP-004, REQ-SEC-101).
// Order: find the webhook, check the signature (401, nothing queued), reject a replayed
// delivery id (409), then queue a deployment (trigger git_push, actor system) for each
// linked service whose repo and branch match and whose auto deploy is on.
// Unknown events answer 202 and are ignored.
func (h *Handlers) ReceiveGitWebhook(w http.ResponseWriter, r *http.Request, webhookID string) {
	ctx := r.Context()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w, r, DefaultMaxBodyBytes)
			return
		}
		invalid(w, r, "body", "cannot read the body")
		return
	}
	if h.Git == nil || h.Git.Keys == nil {
		internalError(w, r, errors.New("git is not set up on this server"))
		return
	}
	hook, err := h.Store.GetWebhook(ctx, webhookID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (hook.Kind != "git" || hook.SecretRef == "")) {
		unauthorizedHook(w, r) // the same answer as a bad signature: ids cannot be probed
		return
	}
	if fail(w, r, err) {
		return
	}
	secret, err := h.Git.Keys.Get(ctx, hook.SecretRef)
	if fail(w, r, err) {
		return
	}
	if git.Verify(hook.Provider, []byte(secret), r.Header, body) != nil {
		unauthorizedHook(w, r)
		return
	}
	d, err := git.Parse(hook.Provider, r.Header, body)
	if err != nil {
		invalid(w, r, "body", err.Error())
		return
	}
	replay, err := h.Store.SeenDelivery(ctx, hook.ID, d.ID, h.Clock.Now())
	if fail(w, r, err) {
		return
	}
	if replay {
		WriteError(w, r, http.StatusConflict, CodeConflict, "this delivery was received before", map[string]any{"reason": "replay"})
		return
	}
	queued := []string{}
	if d.Push == nil || d.Push.Deleted || d.Push.Branch == "" {
		WriteJSON(w, http.StatusAccepted, map[string]any{"event": d.Event, "deployments": queued})
		return
	}
	// A push to the repo and branch of a config_source starts a config sync (REQ-CAC-003).
	configSyncs := h.syncConfigPush(ctx, hook, d.Push)
	services, err := h.Store.GitServicesOf(ctx, hook)
	if fail(w, r, err) {
		return
	}
	log := logging.From(ctx)
	for _, s := range services {
		src, ok := git.SourceOf(s.Config)
		// The event is only acted on if the repo matches a linked service (10-security row 5).
		if !ok || git.RepoName(src.Repo) != d.Push.Repo || src.Branch != d.Push.Branch || !src.AutoDeployOn(s.EnvProtected) {
			continue
		}
		dctx := orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{Commit: d.Push.Commit, Branch: d.Push.Branch})
		id, err := h.Orch.Deploy(dctx, s.ID, "", "git_push")
		if err != nil {
			log.Warn("git push: deploy not queued", "service_id", s.ID, "error", err)
			continue
		}
		log.Info("git push: deployment queued", "service_id", s.ID, "deployment_id", id, "commit", d.Push.Commit,
			"author", d.Push.Author, "actor", "system:"+hook.ID)
		h.auditSystemDeploy(r, hook, s.ID, id, map[string]any{"trigger": "git_push", "commit": d.Push.Commit, "branch": d.Push.Branch,
			"author": d.Push.Author, "repo": d.Push.Repo})
		queued = append(queued, id)
	}
	out := map[string]any{"event": d.Event, "deployments": queued}
	if len(configSyncs) > 0 {
		out["config_syncs"] = configSyncs
	}
	WriteJSON(w, http.StatusAccepted, out)
}

// ReceiveDeployHook is the CI deploy hook: the header X-Polipan-Token must match the secret of
// the hook. It deploys the linked service (trigger api, actor system).
func (h *Handlers) ReceiveDeployHook(w http.ResponseWriter, r *http.Request, webhookID string) {
	ctx := r.Context()
	if h.Git == nil || h.Git.Keys == nil {
		internalError(w, r, errors.New("git is not set up on this server"))
		return
	}
	hook, err := h.Store.GetWebhook(ctx, webhookID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && hook.Kind != "deploy_hook") {
		unauthorizedHook(w, r)
		return
	}
	if fail(w, r, err) {
		return
	}
	secret, err := h.Git.Keys.Get(ctx, hook.SecretRef)
	if fail(w, r, err) {
		return
	}
	token := r.Header.Get("X-Polipan-Token")
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		unauthorizedHook(w, r)
		return
	}
	id, err := h.Orch.Deploy(orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{}), hook.ServiceID, "", "api")
	if h.orchestratorFailed(w, r, err) {
		return
	}
	h.auditSystemDeploy(r, hook, hook.ServiceID, id, map[string]any{"trigger": "deploy_hook"})
	WriteJSON(w, http.StatusAccepted, map[string]any{"deployments": []string{id}})
}

// auditSystemDeploy writes the audit event of a deployment that a trigger started: actor_type system, the id of
// the trigger as actor, and the commit author when there is one (REQ-AUDIT-007).
func (h *Handlers) auditSystemDeploy(r *http.Request, hook *store.Webhook, serviceID, deploymentID string, diff map[string]any) {
	ctx := r.Context()
	diff["service_id"] = serviceID
	projectID := ""
	if svc, err := h.Store.GetService(ctx, hook.OrgID, serviceID); err == nil {
		projectID = svc.ProjectID
	}
	err := audit.Write(ctx, h.Store.Pool, audit.Event{OrgID: hook.OrgID, ActorType: audit.ActorSystem, ActorID: "webhook:" + hook.ID,
		Action: "deployment.create", TargetType: "deployment", TargetID: deploymentID, ProjectID: projectID, Diff: diff})
	if err != nil {
		logging.From(ctx).Error("audit event of a webhook deploy not written", "error", err, "deployment_id", deploymentID)
	}
}
