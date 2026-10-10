// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/vars"
)

// Codes of 05-api section 2 used by variables and secrets.
const (
	CodeManagedByGit           = "managed_by_git"
	CodeReferenceCycle         = "reference_cycle"
	CodeUnresolvedReference    = "unresolved_reference"
	CodeSecretVersionDestroyed = "secret_version_destroyed"
)

// actor is the user of the request, for the audit record.
func actor(r *http.Request) store.Actor {
	a := store.Actor{RequestID: RequestIDFrom(r.Context())}
	if p := PrincipalFrom(r.Context()); p != nil && p.User != nil {
		a.UserID = p.User.ID
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		a.IP = host
	}
	return a
}

// configFailed maps the errors of variables, secrets and volumes. It returns true when it wrote an answer.
func configFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var cycle *vars.CycleError
	var unresolved *vars.UnresolvedError
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNameConflict):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a variable or a secret with this name exists in this scope", map[string]any{"field": "name"})
	case errors.Is(err, store.ErrBadScope):
		invalid(w, r, "scope", "the environment or the service is not in this project")
	case errors.Is(err, store.ErrManagedByGit):
		WriteError(w, r, http.StatusConflict, CodeManagedByGit, "this value is set by polipan.yaml", nil)
	case errors.As(err, &cycle):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeReferenceCycle, cycle.Error(), map[string]any{"names": cycle.Names})
	case errors.As(err, &unresolved):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeUnresolvedReference, unresolved.Error(), map[string]any{"names": unresolved.Names})
	default:
		return fail(w, r, err)
	}
	return true
}

// checkNameValue validates a name and a value. It writes 422 and returns false when they are bad.
func checkNameValue(w http.ResponseWriter, r *http.Request, name, value string) bool {
	if err := vars.CheckName(name); err != nil {
		invalid(w, r, "name", err.Error())
		return false
	}
	if err := vars.CheckValue(value); err != nil {
		invalid(w, r, "value", err.Error())
		return false
	}
	return true
}

// scopeFor checks the scope of a write and the protected environment rule.
func (h *Handlers) scopeFor(w http.ResponseWriter, r *http.Request, perm, projectID string, envID, serviceID *string) (*store.Scope, bool) {
	sc, err := h.Store.ResolveScope(r.Context(), h.orgID(r), projectID, envID, serviceID)
	if configFailed(w, r, err) {
		return nil, false
	}
	if denyProtected(w, r, perm, sc.Protected) {
		return nil, false
	}
	return sc, true
}

func variableJSON(v *store.Variable) gen.Variable {
	return gen.Variable{Id: &v.ID, Name: &v.Name, Value: &v.Value, EnvironmentId: v.EnvironmentID, ServiceId: v.ServiceID,
		Preview: &v.Preview, BuildTime: &v.BuildTime, GitManaged: &v.GitManaged}
}

// ListVariables lists the variables of a project.
func (h *Handlers) ListVariables(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId, params gen.ListVariablesParams) {
	if _, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListVariables(r.Context(), h.orgID(r), projectID, params.EnvironmentId, params.ServiceId)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Variable, len(list))
	for i := range list {
		out[i] = variableJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateVariable makes a variable. Nothing restarts: the next deployment uses it (REQ-ENV-003).
func (h *Handlers) CreateVariable(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.VariableWrite
	if !decodeJSON(w, r, &body) || !checkNameValue(w, r, body.Name, body.Value) {
		return
	}
	sc, ok := h.scopeFor(w, r, "variable:write", projectID, body.EnvironmentId, body.ServiceId)
	if !ok {
		return
	}
	v, err := h.Store.CreateVariable(r.Context(), h.orgID(r), projectID, sc,
		store.NewVariable{Name: body.Name, Value: body.Value, Preview: body.Preview, BuildTime: body.BuildTime}, actor(r))
	if configFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, variableJSON(v))
}

// BulkSetVariables creates or changes many variables and secrets (REQ-ENV-006). The client
// parses .env text or a JSON object into items (see vars.ParseDotenv and vars.ParseJSON).
func (h *Handlers) BulkSetVariables(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.BulkSetVariablesJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Items) == 0 || len(body.Items) > 1000 {
		invalid(w, r, "items", "items must have 1 to 1000 entries")
		return
	}
	seen := map[string]bool{}
	items := make([]store.BulkItem, 0, len(body.Items))
	for _, it := range body.Items {
		if !checkNameValue(w, r, it.Name, it.Value) {
			return
		}
		if seen[it.Name] {
			invalid(w, r, "items", "the name "+it.Name+" is in the list twice")
			return
		}
		seen[it.Name] = true
		secret := it.Secret != nil && *it.Secret
		items = append(items, store.BulkItem{Name: it.Name, Value: it.Value, Secret: secret, Preview: it.Preview, BuildTime: it.BuildTime})
	}
	perm := "variable:write"
	for _, it := range items {
		if it.Secret {
			perm = "secret:write"
		}
	}
	if perm == "secret:write" && !h.secretsReady(w, r) {
		return
	}
	sc, ok := h.scopeFor(w, r, perm, projectID, body.EnvironmentId, body.ServiceId)
	if !ok {
		return
	}
	created, updated, err := h.Store.BulkSet(r.Context(), h.provider(), h.orgID(r), projectID, sc, items, actor(r))
	if configFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, map[string]int{"created": created, "updated": updated})
}

// UpdateVariable changes the name, value and flags of a variable. The scope cannot change.
func (h *Handlers) UpdateVariable(w http.ResponseWriter, r *http.Request, variableID gen.VariableId) {
	var body gen.VariableWrite
	if !decodeJSON(w, r, &body) || !checkNameValue(w, r, body.Name, body.Value) {
		return
	}
	cur, err := h.Store.GetVariable(r.Context(), h.orgID(r), variableID)
	if fail(w, r, err) {
		return
	}
	if !sameID(body.EnvironmentId, cur.EnvironmentID) || !sameID(body.ServiceId, cur.ServiceID) {
		invalid(w, r, "scope", "the scope of a variable cannot change, delete it and create a new one")
		return
	}
	if denyProtected(w, r, "variable:write", cur.EnvProtected) {
		return
	}
	v, err := h.Store.UpdateVariable(r.Context(), h.orgID(r), variableID,
		store.NewVariable{Name: body.Name, Value: body.Value, Preview: body.Preview, BuildTime: body.BuildTime}, actor(r))
	if configFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, variableJSON(v))
}

// sameID is true when the body does not set the id or sets the current one.
func sameID(body, cur *string) bool {
	if body == nil || *body == "" {
		return true
	}
	return cur != nil && *cur == *body
}

// DeleteVariable removes a variable.
func (h *Handlers) DeleteVariable(w http.ResponseWriter, r *http.Request, variableID gen.VariableId) {
	cur, err := h.Store.GetVariable(r.Context(), h.orgID(r), variableID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "variable:write", cur.EnvProtected) {
		return
	}
	if configFailed(w, r, h.Store.DeleteVariable(r.Context(), h.orgID(r), variableID, actor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pendingChanges tells if the live release of a service differs from its config, variables,
// secrets and volumes now (REQ-ENV-003). An error is logged and gives false.
func (h *Handlers) pendingChanges(ctx context.Context, s *store.Service) bool {
	pending, err := vars.Pending(ctx, h.Store.Pool, s.ID, s.LiveReleaseID)
	if err != nil {
		logging.From(ctx).Warn("pending changes", "service_id", s.ID, "error", err)
		return false
	}
	return pending
}
