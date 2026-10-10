// SPDX-License-Identifier: Apache-2.0

package api

import (
	"crypto/subtle"
	"errors"
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
		queued = append(queued, id)
	}
	WriteJSON(w, http.StatusAccepted, map[string]any{"event": d.Event, "deployments": queued})
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
	WriteJSON(w, http.StatusAccepted, map[string]any{"deployments": []string{id}})
}
