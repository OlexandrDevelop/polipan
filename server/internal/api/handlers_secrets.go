// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// provider is the secret provider, or nil.
func (h *Handlers) provider() secrets.SecretProvider {
	if h.Secrets == nil {
		return nil
	}
	return h.Secrets.Provider
}

// secretsReady writes 500 and returns false when the server has no secrets store.
func (h *Handlers) secretsReady(w http.ResponseWriter, r *http.Request) bool {
	if h.Secrets == nil {
		internalError(w, r, errors.New("the secrets store is not set up"))
		return false
	}
	return true
}

// secretJSON never has a value (REQ-SEC-002).
func secretJSON(x *store.Secret) gen.Secret {
	return gen.Secret{Id: &x.ID, Name: &x.Name, EnvironmentId: x.EnvironmentID, ServiceId: x.ServiceID, Preview: &x.Preview,
		BuildTime: &x.BuildTime, CurrentVersion: &x.CurrentVersion, GitManaged: &x.GitManaged, UpdatedBy: x.UpdatedBy, UpdatedAt: x.UpdatedAt}
}

// secretFailed maps the errors of the secret versions, then the common errors.
func secretFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, store.ErrVersionNotFound):
		notFoundError(w, r)
	case errors.Is(err, store.ErrVersionInUse):
		WriteError(w, r, http.StatusConflict, CodeConflict, "the version is current or used by a kept release", map[string]any{"reason": "version_in_use"})
	case errors.Is(err, secrets.ErrVersionDestroyed):
		WriteError(w, r, http.StatusConflict, CodeSecretVersionDestroyed, "the version was destroyed", nil)
	default:
		return configFailed(w, r, err)
	}
	return true
}

// ListSecrets lists the secrets of a project, never their values.
func (h *Handlers) ListSecrets(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId, params gen.ListSecretsParams) {
	if _, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListSecrets(r.Context(), h.orgID(r), projectID, params.EnvironmentId, params.ServiceId)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Secret, len(list))
	for i := range list {
		out[i] = secretJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateSecret makes a secret. The value is encrypted and never returned.
func (h *Handlers) CreateSecret(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.SecretWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Value == nil {
		invalid(w, r, "value", "value is required")
		return
	}
	if !checkNameValue(w, r, body.Name, *body.Value) || !h.secretsReady(w, r) {
		return
	}
	sc, ok := h.scopeFor(w, r, "secret:write", projectID, body.EnvironmentId, body.ServiceId)
	if !ok {
		return
	}
	x, err := h.Store.CreateSecret(r.Context(), h.provider(), h.orgID(r), projectID, sc,
		store.NewSecret{Name: body.Name, Value: *body.Value, Preview: body.Preview, BuildTime: body.BuildTime}, actor(r))
	if secretFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, secretJSON(x))
}

// SetSecretValue writes a new version.
func (h *Handlers) SetSecretValue(w http.ResponseWriter, r *http.Request, secretID gen.SecretId) {
	var body gen.SetSecretValueJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Value == nil {
		invalid(w, r, "value", "value is required")
		return
	}
	if !h.secretsReady(w, r) {
		return
	}
	cur, err := h.Store.GetSecret(r.Context(), h.orgID(r), secretID)
	if fail(w, r, err) {
		return
	}
	if !checkNameValue(w, r, cur.Name, *body.Value) || denyProtected(w, r, "secret:write", cur.EnvProtected) {
		return
	}
	x, err := h.Store.SetSecretValue(r.Context(), h.provider(), h.orgID(r), secretID, *body.Value, body.Preview, body.BuildTime, actor(r))
	if secretFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, secretJSON(x))
}

// DeleteSecret deletes a secret. Versions used by kept releases stay until destroyed.
func (h *Handlers) DeleteSecret(w http.ResponseWriter, r *http.Request, secretID gen.SecretId) {
	cur, err := h.Store.GetSecret(r.Context(), h.orgID(r), secretID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "secret:write", cur.EnvProtected) {
		return
	}
	if secretFailed(w, r, h.Store.DeleteSecret(r.Context(), h.orgID(r), secretID, actor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListSecretVersions lists the versions (number, who, when, destroyed, use).
func (h *Handlers) ListSecretVersions(w http.ResponseWriter, r *http.Request, secretID gen.SecretId) {
	list, err := h.Store.ListSecretVersions(r.Context(), h.orgID(r), secretID)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.SecretVersion, len(list))
	for i, v := range list {
		out[i] = gen.SecretVersion{Version: ptr(v.Version), CreatedBy: v.CreatedBy, CreatedAt: ptr(v.CreatedAt),
			Destroyed: ptr(v.Destroyed), UsedByReleases: ptr(v.UsedByReleases)}
	}
	WriteJSON(w, http.StatusOK, out)
}

// RestoreSecretVersion writes an earlier version as the new current version.
func (h *Handlers) RestoreSecretVersion(w http.ResponseWriter, r *http.Request, secretID gen.SecretId, version gen.Version) {
	if !h.secretsReady(w, r) {
		return
	}
	cur, err := h.Store.GetSecret(r.Context(), h.orgID(r), secretID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "secret:write", cur.EnvProtected) {
		return
	}
	x, err := h.Store.RestoreSecretVersion(r.Context(), h.provider(), h.orgID(r), secretID, version, actor(r))
	if secretFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, secretJSON(x))
}

// DestroySecretVersion removes the ciphertext of an old version for good.
func (h *Handlers) DestroySecretVersion(w http.ResponseWriter, r *http.Request, secretID gen.SecretId, version gen.Version) {
	if !h.secretsReady(w, r) {
		return
	}
	if secretFailed(w, r, h.Store.DestroySecretVersion(r.Context(), h.provider(), h.orgID(r), secretID, version, actor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevealSecret returns the current value of a secret to an owner or an admin who is signed in with a browser session.
// It is the one place where a value leaves the store, so it is narrow: an API token is refused, the call is written
// to the audit log before the value is sent, and the answer is never cached.
func (h *Handlers) RevealSecret(w http.ResponseWriter, r *http.Request, secretID gen.SecretId) {
	if !h.secretsReady(w, r) {
		return
	}
	p := PrincipalFrom(r.Context())
	if p == nil || p.User == nil || p.Token != nil {
		WriteError(w, r, http.StatusForbidden, CodeForbidden, "a secret is shown only to a signed-in user, not to an API token",
			map[string]any{"permission": "secret:reveal", "reason": "session_required"})
		return
	}
	cur, err := h.Store.GetSecret(r.Context(), h.orgID(r), secretID)
	if fail(w, r, err) {
		return
	}
	value, err := h.provider().Get(r.Context(), h.Store.Pool, cur.ProjectID, cur.ID, cur.CurrentVersion)
	if secretFailed(w, r, err) {
		return
	}
	if err := secrets.WriteAudit(r.Context(), h.Store.Pool, secrets.AuditEvent{OrgID: cur.OrgID, ActorType: "user", ActorID: p.User.ID,
		Action: "secret.reveal", TargetType: "secret", TargetID: cur.ID, ProjectID: cur.ProjectID,
		Diff: map[string]any{"name": cur.Name, "version": cur.CurrentVersion},
		IP:   clientKey(r), RequestID: RequestIDFrom(r.Context())}); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"value": string(value), "version": cur.CurrentVersion})
}

// maskerFor returns a masker with the secret values of a service. On an error the lines are
// not shown as they are: the caller gets a masker that hides nothing only when there are no secrets.
func (h *Handlers) maskerFor(r *http.Request, serviceID string) (*mask.Masker, error) {
	if h.Secrets == nil {
		return mask.New(nil), nil
	}
	m, err := h.Secrets.Masker(r.Context(), serviceID)
	if err != nil {
		logging.From(r.Context()).Error("secret masker", "service_id", serviceID, "error", err)
	}
	return m, err
}
