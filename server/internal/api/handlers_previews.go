// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/preview"
	"github.com/polipan/polipan/server/internal/store"
)

func (h *Handlers) previewsReady(w http.ResponseWriter, r *http.Request) bool {
	if h.Git == nil || h.Git.Previews == nil {
		internalError(w, r, errors.New("previews are not set up on this server"))
		return false
	}
	return true
}

func previewJSON(i preview.Info) map[string]any {
	p := map[string]any{"repo": i.Repo, "state": i.State, "urls": i.URLs, "from_fork": i.FromFork, "waiting_for_label": i.WaitingForLabel}
	if i.Number > 0 {
		p["pr_number"] = i.Number
	}
	if i.Branch != "" {
		p["branch"] = i.Branch
	}
	if len(i.URLs) > 0 {
		p["url"] = i.URLs[0]
	}
	if i.ExpiresAt != nil {
		p["expires_at"] = *i.ExpiresAt
	}
	if i.LastPushAt != nil {
		p["last_push_at"] = *i.LastPushAt
	}
	if i.FailureReason != "" {
		p["failure_reason"] = i.FailureReason
	}
	if i.HeadSHA != "" {
		p["head_sha"] = i.HeadSHA
	}
	return map[string]any{"id": i.ID, "project_id": i.ProjectID, "name": i.Slug, "slug": i.Slug, "kind": "preview", "protected": false,
		"base_environment_id": i.BaseEnvID, "preview": p}
}

// ListPreviews lists the preview environments of a project.
func (h *Handlers) ListPreviews(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	if !h.previewsReady(w, r) {
		return
	}
	if _, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID); fail(w, r, err) {
		return
	}
	list, err := h.Git.Previews.List(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	out := make([]map[string]any, len(list))
	for i := range list {
		out[i] = previewJSON(list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreatePreview makes a preview by hand for a PR number or a branch (REQ-PREV-014).
func (h *Handlers) CreatePreview(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body struct {
		Repo              string `json:"repo"`
		PRNumber          int    `json:"pr_number"`
		Branch            string `json:"branch"`
		BaseEnvironmentID string `json:"base_environment_id"`
	}
	if !decodeJSON(w, r, &body) || !h.previewsReady(w, r) {
		return
	}
	p, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	if body.Repo == "" {
		invalid(w, r, "repo", "repo is required")
		return
	}
	if body.PRNumber <= 0 && body.Branch == "" {
		invalid(w, r, "pr_number", "a PR number or a branch is needed")
		return
	}
	if body.BaseEnvironmentID != "" {
		if e, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), body.BaseEnvironmentID); err != nil || e.ProjectID != projectID || e.Kind == "preview" {
			invalid(w, r, "base_environment_id", "base_environment_id must be an environment of the project")
			return
		}
	}
	a := actor(r)
	info, err := h.Git.Previews.Create(r.Context(), preview.CreateParams{OrgID: h.orgID(r), ProjectID: projectID, Repo: body.Repo, PRNumber: body.PRNumber,
		Branch: body.Branch, BaseEnvID: body.BaseEnvironmentID, UserID: a.UserID, IP: a.IP, Req: a.RequestID})
	switch {
	case errors.Is(err, preview.ErrDisabled):
		invalid(w, r, "previews", "previews are off in this project: turn them on in the project settings")
		return
	case errors.Is(err, preview.ErrNoRepo):
		invalid(w, r, "repo", "no service of the base environment uses this repository")
		return
	case errors.Is(err, preview.ErrLimit):
		WriteError(w, r, http.StatusConflict, CodeConflict, "the project has the largest number of previews", map[string]any{"reason": "preview_limit"})
		return
	case errors.Is(err, preview.ErrExists):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "this pull request has a preview already", nil)
		return
	case err != nil:
		var used *store.DomainUsedError
		if errors.As(err, &used) {
			WriteError(w, r, http.StatusConflict, CodeDomainInUse, err.Error(), nil)
			return
		}
		internalError(w, r, err)
		return
	}
	_ = p
	w.Header().Set("Location", BasePath+"/environments/"+info.ID)
	WriteJSON(w, http.StatusAccepted, previewJSON(*info))
}

// updatePreview is the update of a preview environment. Its settings hold the state of the preview, so only
// extend_days (days to add to the TTL) can be set. A preview has no name change and is never protected (REQ-PREV-012).
func (h *Handlers) updatePreview(w http.ResponseWriter, r *http.Request, cur *store.Environment, name *string, protected *bool, settings *map[string]any) {
	if name != nil || protected != nil {
		invalid(w, r, "name", "a preview cannot be renamed or protected")
		return
	}
	if !h.previewsReady(w, r) {
		return
	}
	days := 0
	if settings != nil {
		for k, v := range *settings {
			f, ok := v.(float64)
			if k != "extend_days" || !ok || f < 1 || f > 365 || f != float64(int(f)) {
				invalid(w, r, "settings."+k, "only settings.extend_days (1 to 365) can be set on a preview")
				return
			}
			days = int(f)
		}
	}
	if days > 0 {
		if err := h.Git.Previews.Extend(r.Context(), h.orgID(r), cur.ID, days); err != nil {
			if errors.Is(err, preview.ErrNotFound) {
				notFoundError(w, r)
				return
			}
			internalError(w, r, err)
			return
		}
	}
	info, err := h.Git.Previews.Get(r.Context(), h.orgID(r), cur.ID)
	if errors.Is(err, preview.ErrNotFound) {
		notFoundError(w, r)
		return
	}
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, previewJSON(*info))
}
