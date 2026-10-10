// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// idem is the state of one request with an Idempotency-Key (05-api, section 1).
type idem struct {
	actor, key string
	hash       []byte
}

// startIdempotent reads the body and checks the key. It returns replayed=true after it wrote the stored answer
// (or a 409 when the key was used for another request). The body is put back for the handler.
func (h *Handlers) startIdempotent(w http.ResponseWriter, r *http.Request, key *string) (id *idem, replayed bool) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if key == nil || *key == "" {
		return nil, false
	}
	p := PrincipalFrom(r.Context())
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), raw...))
	id = &idem{actor: p.User.ID, key: *key, hash: sum[:]}
	hash, status, body, found, err := h.Store.IdempotencyGet(r.Context(), id.actor, id.key)
	if err != nil {
		internalError(w, r, err)
		return nil, true
	}
	if !found {
		return id, false
	}
	if !bytes.Equal(hash, id.hash) {
		WriteError(w, r, http.StatusConflict, CodeIdempotencyKeyReuse, "this Idempotency-Key was used for a different request", nil)
		return nil, true
	}
	w.Header().Set("Idempotent-Replay", "true")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil, true
}

func (h *Handlers) saveIdempotent(r *http.Request, id *idem, status int, v any) {
	if id == nil {
		return
	}
	body, _ := json.Marshal(v)
	if err := h.Store.IdempotencyPut(r.Context(), id.actor, id.key, id.hash, status, body); err != nil {
		logging.From(r.Context()).Warn("idempotency key not stored", "error", err)
	}
}

// auditUser writes the audit record of a user action (REQ-AUDIT-002).
func (h *Handlers) auditUser(r *http.Request, orgID, projectID, action, targetType, targetID string, diff any) {
	p := PrincipalFrom(r.Context())
	err := secrets.WriteAudit(r.Context(), h.Store.Pool, secrets.AuditEvent{OrgID: orgID, ActorType: "user", ActorID: p.User.ID, Action: action,
		TargetType: targetType, TargetID: targetID, ProjectID: projectID, Diff: diff, IP: clientKey(r), RequestID: RequestIDFrom(r.Context())})
	if err != nil {
		logging.From(r.Context()).Warn("audit record not written", "error", err, "action", action)
	}
}

func paramsFromRequest(serviceID, actorID string, body gen.DeployRequest) orchestrator.DeployParams {
	p := orchestrator.DeployParams{ServiceID: serviceID, ActorID: actorID}
	if body.Strategy != nil {
		p.Strategy = string(*body.Strategy)
	}
	if o := body.StrategyOptions; o != nil {
		p.DrainSeconds = o.DrainSeconds
		p.AllowDowntimeOnLowCapacity = o.AllowDowntimeOnLowCapacity
		p.AllowOverlap = o.AllowOverlap
	}
	return p
}

// DeployService queues a deployment and answers 202 with it. With dry_run it answers 200 with the plan.
func (h *Handlers) DeployService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.DeployServiceParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	var body gen.DeployRequest
	if r.ContentLength != 0 && r.Body != nil {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:deploy", s.EnvProtected) {
		return
	}
	if body.Commit != nil && (s.SourceType != "git" || !git.ValidCommit(*body.Commit)) {
		invalid(w, r, "commit", "commit must be a commit id of a git service")
		return
	}
	p := paramsFromRequest(serviceID, PrincipalFrom(r.Context()).User.ID, body)
	if body.DryRun != nil && *body.DryRun {
		plan, err := h.Orch.Plan(r.Context(), p)
		if h.orchestratorFailed(w, r, err) {
			return
		}
		WriteJSON(w, http.StatusOK, planJSON(plan))
		return
	}
	depID, _, err := h.Orch.DeployWith(deployContext(r.Context(), s.SourceType, body), p)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	d, err := h.Store.GetDeployment(r.Context(), h.orgID(r), depID)
	if fail(w, r, err) {
		return
	}
	h.auditUser(r, s.OrgID, s.ProjectID, "deployment.create", "deployment", depID, map[string]any{"service_id": serviceID, "strategy": d.Strategy})
	out := deploymentJSON(d)
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	w.Header().Set("Location", BasePath+"/deployments/"+depID)
	WriteJSON(w, http.StatusAccepted, out)
}

func planJSON(p orchestrator.Plan) gen.DeployPlan {
	strategy := gen.Strategy(p.Strategy)
	downtime := gen.DeployPlanExpectedDowntime(p.ExpectedDowntime)
	nodes := p.Nodes
	if nodes == nil {
		nodes = []string{}
	}
	out := gen.DeployPlan{Strategy: &strategy, ExpectedDowntime: &downtime, Nodes: &nodes, Forced: &p.Forced}
	warnings := make([]gen.Warning, 0, len(p.Warnings))
	for _, w := range p.Warnings {
		warnings = append(warnings, gen.Warning{Code: ptr(w.Code), Message: ptr(w.Message)})
	}
	out.Warnings = &warnings
	return out
}

// RollbackService queues a rollback to an older release. No build runs (REQ-REL-003).
func (h *Handlers) RollbackService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.RollbackServiceParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	var body gen.RollbackServiceJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:rollback", s.EnvProtected) { // REQ-REL-010
		return
	}
	p := orchestrator.DeployParams{ServiceID: serviceID, ActorID: PrincipalFrom(r.Context()).User.ID, RollbackReleaseID: body.ReleaseId}
	if body.Strategy != nil {
		p.Strategy = string(*body.Strategy)
	}
	p.Force = body.Force != nil && *body.Force
	depID, _, err := h.Orch.DeployWith(r.Context(), p)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	d, err := h.Store.GetDeployment(r.Context(), h.orgID(r), depID)
	if fail(w, r, err) {
		return
	}
	h.auditUser(r, s.OrgID, s.ProjectID, "deployment.rollback", "deployment", depID, map[string]any{"service_id": serviceID, "release_id": body.ReleaseId})
	out := deploymentJSON(d)
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	w.Header().Set("Location", BasePath+"/deployments/"+depID)
	WriteJSON(w, http.StatusAccepted, out)
}

// CancelDeployment cancels a deployment up to health_checking.
func (h *Handlers) CancelDeployment(w http.ResponseWriter, r *http.Request, deploymentID gen.DeploymentId) {
	d, err := h.Store.GetDeployment(r.Context(), h.orgID(r), deploymentID)
	if fail(w, r, err) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), d.ServiceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "deployment:cancel", s.EnvProtected) {
		return
	}
	if h.orchestratorFailed(w, r, h.Orch.Cancel(r.Context(), deploymentID, PrincipalFrom(r.Context()).User.ID)) {
		return
	}
	h.auditUser(r, s.OrgID, s.ProjectID, "deployment.cancel", "deployment", deploymentID, map[string]any{"service_id": d.ServiceID})
	d, err = h.Store.GetDeployment(r.Context(), h.orgID(r), deploymentID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, deploymentJSON(d))
}

// TestHealthCheck runs the health check of the live container once.
func (h *Handlers) TestHealthCheck(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	res, err := h.Orch.TestHealth(r.Context(), serviceID)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	masker, err := h.maskerFor(r, serviceID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"healthy": res.Healthy, "status": res.Status,
		"body_excerpt": masker.Mask(res.BodyExcerpt), "duration_ms": res.DurationMS, "error": masker.Mask(res.Error)})
}

// ---- releases ----

func releaseJSON(rel *store.Release) gen.Release {
	out := gen.Release{Id: &rel.ID, ServiceId: &rel.ServiceID, Number: &rel.Number, ImageDigest: &rel.ImageDigest, ImageRef: &rel.ImageRef,
		Status: ptr(gen.ReleaseStatus(rel.Status)), FastRollbackPossible: &rel.FastRollbackPossible, CreatedBy: rel.CreatedBy, CreatedAt: &rel.CreatedAt}
	if rel.Note != "" {
		out.Note = &rel.Note
	}
	var cfg gen.ServiceConfig
	if json.Unmarshal(rel.ConfigSnapshot, &cfg) == nil {
		// The snapshot holds ids of secrets and volumes for the engine. The names and versions are in secret_versions.
		delete(cfg, "secret_refs")
		out.ConfigSnapshot = &cfg
	}
	var vars map[string]string
	if json.Unmarshal(rel.Variables, &vars) == nil {
		out.Variables = &vars
	}
	var versions map[string]int
	if json.Unmarshal(rel.SecretVersions, &versions) == nil {
		out.SecretVersions = &versions
	}
	var src struct {
		Type                  string `json:"type"`
		CommitSha             string `json:"commit_sha"`
		Branch                string `json:"branch"`
		PrNumber              int    `json:"pr_number"`
		PromotedFromReleaseID string `json:"promoted_from_release_id"`
		RollbackOfReleaseID   string `json:"rollback_of_release_id"`
	}
	if json.Unmarshal(rel.Source, &src) == nil {
		out.Source = &struct {
			Branch                *string `json:"branch,omitempty"`
			CommitSha             *string `json:"commit_sha,omitempty"`
			PrNumber              *int    `json:"pr_number,omitempty"`
			PromotedFromReleaseId *string `json:"promoted_from_release_id,omitempty"`
			RollbackOfReleaseId   *string `json:"rollback_of_release_id,omitempty"`
			Type                  *string `json:"type,omitempty"`
		}{Branch: strPtr(src.Branch), CommitSha: strPtr(src.CommitSha), PrNumber: intPtr(src.PrNumber),
			PromotedFromReleaseId: strPtr(src.PromotedFromReleaseID), RollbackOfReleaseId: strPtr(src.RollbackOfReleaseID), Type: strPtr(src.Type)}
	}
	return out
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// ListReleases lists the releases of a service, newest first.
func (h *Handlers) ListReleases(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.ListReleasesParams) {
	cur, ok := parseCursor(w, r, params.Cursor)
	if !ok {
		return
	}
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	limit := pageLimit(params.Limit)
	list, err := h.Store.ListReleases(r.Context(), h.orgID(r), serviceID, limit, cur)
	if fail(w, r, err) {
		return
	}
	out := page[gen.Release]{Items: []gen.Release{}}
	for i := range list {
		if i == limit {
			next := store.Cursor{Time: list[limit-1].CreatedAt, ID: list[limit-1].ID}.Encode()
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, releaseJSON(&list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetRelease returns one release.
func (h *Handlers) GetRelease(w http.ResponseWriter, r *http.Request, releaseID gen.ReleaseId) {
	rel, err := h.Store.GetRelease(r.Context(), h.orgID(r), releaseID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, releaseJSON(rel))
}

// AnnotateRelease sets the note of a release. It is audited (REQ-REL-002).
func (h *Handlers) AnnotateRelease(w http.ResponseWriter, r *http.Request, releaseID gen.ReleaseId) {
	var body struct {
		Note string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Note) > 2000 {
		invalid(w, r, "note", "the note can have 2000 characters at most")
		return
	}
	rel, err := h.Store.GetRelease(r.Context(), h.orgID(r), releaseID)
	if fail(w, r, err) {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), rel.ServiceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:update", s.EnvProtected) {
		return
	}
	rel, err = h.Store.SetReleaseNote(r.Context(), h.orgID(r), releaseID, body.Note, PrincipalFrom(r.Context()).User.ID)
	if fail(w, r, err) {
		return
	}
	h.auditUser(r, s.OrgID, s.ProjectID, "release.annotate", "release", releaseID, map[string]any{"service_id": rel.ServiceID})
	WriteJSON(w, http.StatusOK, releaseJSON(rel))
}

// DiffReleases compares two releases: image, commit, config, variables (plain values) and secret versions (REQ-REL-009).
func (h *Handlers) DiffReleases(w http.ResponseWriter, r *http.Request, releaseID gen.ReleaseId, otherReleaseID string) {
	a, err := h.Store.GetRelease(r.Context(), h.orgID(r), releaseID)
	if fail(w, r, err) {
		return
	}
	b, err := h.Store.GetRelease(r.Context(), h.orgID(r), otherReleaseID)
	if fail(w, r, err) {
		return
	}
	changes := []gen.FieldChange{}
	add := func(path string, before, after any) {
		bj, _ := json.Marshal(before)
		aj, _ := json.Marshal(after)
		if !bytes.Equal(bj, aj) {
			changes = append(changes, gen.FieldChange{Path: ptr(path), Before: &before, After: &after})
		}
	}
	add("image_digest", a.ImageDigest, b.ImageDigest)
	commit := func(rel *store.Release) any {
		var s struct {
			C string `json:"commit_sha"`
		}
		_ = json.Unmarshal(rel.Source, &s)
		if s.C == "" {
			return nil
		}
		return s.C
	}
	add("commit", commit(a), commit(b))
	diffMaps := func(prefix string, x, y json.RawMessage, skip string) {
		var xm, ym map[string]any
		_ = json.Unmarshal(x, &xm)
		_ = json.Unmarshal(y, &ym)
		keys := map[string]bool{}
		for k := range xm {
			keys[k] = true
		}
		for k := range ym {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			if k != skip {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			add(prefix+"."+k, xm[k], ym[k])
		}
	}
	diffMaps("config", a.ConfigSnapshot, b.ConfigSnapshot, "secret_refs")
	diffMaps("variables", a.Variables, b.Variables, "")
	diffMaps("secrets", a.SecretVersions, b.SecretVersions, "")
	WriteJSON(w, http.StatusOK, map[string]any{"changes": changes})
}
