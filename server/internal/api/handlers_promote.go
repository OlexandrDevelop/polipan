// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/promote"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/vars"
)

// PromoteRelease promotes a Release to the service of the same name in another environment of the project
// (F04). With dry_run it answers 200 with the plan and changes nothing. No build runs.
func (h *Handlers) PromoteRelease(w http.ResponseWriter, r *http.Request, releaseID gen.ReleaseId, params gen.PromoteReleaseParams) {
	h.promoteRequest(w, r, releaseID, false, params.IdempotencyKey)
}

// PromoteEnvironment promotes all services of an environment (project-wide). The services are deployed one after
// the other in the order of their dependencies. If one fails, the rest of the batch does not start (REQ-PROMO-008).
func (h *Handlers) PromoteEnvironment(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId, params gen.PromoteEnvironmentParams) {
	h.promoteRequest(w, r, environmentID, true, params.IdempotencyKey)
}

func (h *Handlers) promoteRequest(w http.ResponseWriter, r *http.Request, id string, batch bool, key *string) {
	idem, replayed := h.startIdempotent(w, r, key)
	if replayed {
		return
	}
	var body gen.PromoteRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	pl := &promote.Planner{DB: h.Store.Pool}
	opts := promote.Options{TargetEnvID: body.TargetEnvironmentId, CreateMissing: deref(body.CreateMissing), IncludeDatabases: deref(body.IncludeDatabases)}
	if body.AllowMissing != nil {
		opts.AllowMissing = *body.AllowMissing
	}
	if body.Strategy != nil {
		opts.Strategy = string(*body.Strategy)
	}
	dry := deref(body.DryRun)

	tgtEnv, err := pl.Env(ctx, opts.TargetEnvID)
	if errors.Is(err, promote.ErrNotFound) {
		invalid(w, r, "target_environment_id", "the target environment does not exist")
		return
	}
	if fail(w, r, err) {
		return
	}
	var plan *promote.Plan
	var srcEnvID string
	if batch {
		srcEnvID = id
		plan, _, err = pl.ForEnvironment(ctx, id, opts)
	} else {
		plan, _, err = pl.ForRelease(ctx, id, opts)
	}
	if h.promoteFailed(w, r, err) {
		return
	}
	// The permission is judged on the target (REQ-PROMO-007). A dry run only reads.
	if !dry && denyProtected(w, r, "service:promote", tgtEnv.Protected) {
		return
	}
	// The strategy of each item, from the deploy engine. A canary needs a domain on the target.
	strategies := make([]string, len(plan.Items))
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.SkippedReason != "" || it.TargetServiceID == "" {
			continue
		}
		dp, err := h.Orch.Plan(ctx, orchestrator.DeployParams{ServiceID: it.TargetServiceID, Trigger: "promotion", Prepared: it.Prepared, Strategy: opts.Strategy})
		switch {
		case errors.Is(err, orchestrator.ErrNoNode):
			it.Warnings = append(it.Warnings, orchestrator.Warning{Code: "node_unavailable", Message: "no node is ready to run " + it.Service})
		case err != nil:
			if h.promoteOrchFailed(w, r, err) {
				return
			}
		default:
			strategies[i] = dp.Strategy
			it.Warnings = append(it.Warnings, dp.Warnings...)
		}
	}
	if dry {
		WriteJSON(w, http.StatusOK, planJSONOf(plan, strategies))
		return
	}
	if body.PlanHash != nil && *body.PlanHash != plan.Hash {
		WriteError(w, r, http.StatusConflict, CodePlanChanged, "the target changed since the plan was made. Read the plan again",
			map[string]any{"plan_hash": plan.Hash})
		return
	}
	if names := plan.MissingNames(); len(names) > 0 {
		WriteError(w, r, http.StatusUnprocessableEntity, CodeMissingVariables,
			"the target does not have variables that the release uses. Set them, or pass allow_missing", map[string]any{"names": names})
		return
	}
	todo := 0
	for _, it := range plan.Items {
		if it.SkippedReason == "" {
			todo++
		}
	}
	if todo == 0 {
		WriteError(w, r, http.StatusConflict, CodeConflict, "nothing can be promoted: every service was skipped", map[string]any{"reason": "nothing_to_promote"})
		return
	}
	replan := func() (*promote.Plan, error) { p, _, err := pl.ForRelease(ctx, id, opts); return p, err }
	if batch {
		replan = func() (*promote.Plan, error) { p, _, err := pl.ForEnvironment(ctx, srcEnvID, opts); return p, err }
	}
	if !h.createPromotionTargets(w, r, tgtEnv, plan, replan) {
		return
	}

	batchID, prev := "", ""
	if batch {
		batchID = store.NewID("pbt")
	}
	p := PrincipalFrom(ctx)
	var deployments []gen.Deployment
	var depIDs []string
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.SkippedReason != "" {
			continue
		}
		depID, _, err := h.Orch.DeployWith(ctx, orchestrator.DeployParams{ServiceID: it.TargetServiceID, ActorID: p.User.ID, Trigger: "promotion",
			Strategy: opts.Strategy, Prepared: it.Prepared, BatchID: batchID, AfterDeploymentID: prev})
		if err != nil {
			if len(depIDs) > 0 {
				logging.From(ctx).Error("promotion batch: a deployment was not queued", "service", it.Service, "error", err, "queued", depIDs)
			}
			h.promoteOrchFailed(w, r, err)
			return
		}
		d, err := h.Store.GetDeployment(ctx, h.orgID(r), depID)
		if fail(w, r, err) {
			return
		}
		deployments = append(deployments, deploymentJSON(d))
		depIDs = append(depIDs, depID)
		prev = depID
	}
	action, target := "release.promote", id
	if batch {
		action = "environment.promote"
	}
	h.auditUser(r, h.orgID(r), tgtEnv.ProjectID, action, map[bool]string{false: "release", true: "environment"}[batch], target,
		map[string]any{"source": id, "target_environment_id": tgtEnv.ID, "plan_hash": plan.Hash, "deployments": depIDs, "batch_id": batchID})
	if batch {
		h.saveIdempotent(r, idem, http.StatusAccepted, deployments)
		WriteJSON(w, http.StatusAccepted, deployments)
		return
	}
	h.saveIdempotent(r, idem, http.StatusAccepted, deployments[0])
	w.Header().Set("Location", BasePath+"/deployments/"+depIDs[0])
	WriteJSON(w, http.StatusAccepted, deployments[0])
}

// promoteFailed maps an error of the planner. It returns true when it wrote an answer.
func (h *Handlers) promoteFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var notPromotable *promote.NotPromotableError
	var unresolved *vars.UnresolvedError
	var cycle *vars.CycleError
	switch {
	case err == nil:
		return false
	case errors.Is(err, promote.ErrNotFound):
		notFoundError(w, r)
	case errors.Is(err, promote.ErrSameEnvironment), errors.Is(err, promote.ErrOtherProject), errors.Is(err, promote.ErrPreviewTarget):
		invalid(w, r, "target_environment_id", err.Error())
	case errors.As(err, &notPromotable):
		WriteError(w, r, http.StatusConflict, CodeReleaseNotPromotable, notPromotable.Error(), nil)
	case errors.Is(err, promote.ErrDomainPortMismatch):
		WriteError(w, r, http.StatusConflict, CodeDomainPortMismatch, err.Error(), map[string]any{"warning": "domain_port_mismatch"})
	case errors.As(err, &unresolved):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeUnresolvedReference, "the target has variables that do not resolve: "+unresolved.Error(),
			map[string]any{"names": unresolved.Names})
	case errors.As(err, &cycle):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeReferenceCycle, cycle.Error(), nil)
	default:
		internalError(w, r, err)
	}
	return true
}

// createPromotionTargets creates the services that are missing in the target (create_missing, REQ-PROMO-006).
// The service gets the config of the source without the env-specific fields: no domain, one replica, no auto deploy.
// Then the plan is made again, so the new Release is made for the new service.
func (h *Handlers) createPromotionTargets(w http.ResponseWriter, r *http.Request, tgtEnv *promote.EnvInfo, plan *promote.Plan,
	replan func() (*promote.Plan, error)) bool {
	ctx := r.Context()
	created := false
	for _, it := range plan.Items {
		if !it.Create || it.SkippedReason != "" {
			continue
		}
		var typ, srcType string
		var cfg json.RawMessage
		if err := h.Store.Pool.QueryRow(ctx, `SELECT type, source_type, config FROM services WHERE id = $1`, it.SourceServiceID).Scan(&typ, &srcType, &cfg); fail(w, r, err) {
			return false
		}
		newCfg, err := promote.NewServiceConfig(cfg)
		if fail(w, r, err) {
			return false
		}
		svc, err := h.Store.CreateService(ctx, h.orgID(r), store.NewService{EnvironmentID: tgtEnv.ID, Name: it.Service, Type: typ, SourceType: srcType, Config: newCfg})
		if err != nil {
			if u, _ := store.IsUnique(err); u {
				WriteError(w, r, http.StatusConflict, CodePlanChanged, "the service "+it.Service+" was created since the plan was made", nil)
				return false
			}
			fail(w, r, err)
			return false
		}
		vols, err := h.Store.ListVolumes(ctx, it.SourceServiceID)
		if fail(w, r, err) {
			return false
		}
		for _, v := range vols {
			v.NodeID = nil // a new volume of the target: its own data and node (REQ-PROMO-002)
			if _, err := h.Store.CreateVolume(ctx, svc, v, actor(r)); fail(w, r, err) {
				return false
			}
		}
		h.auditUser(r, svc.OrgID, svc.ProjectID, "service.create", "service", svc.ID, map[string]any{"after": map[string]any{"name": svc.Name, "created_by": "promotion"}})
		created = true
	}
	if !created {
		return true
	}
	p2, err := replan()
	if h.promoteFailed(w, r, err) {
		return false
	}
	if len(p2.Items) != len(plan.Items) {
		internalError(w, r, errors.New("the plan changed while the services were created"))
		return false
	}
	// The prepared Releases of the new plan replace the old ones. The hash of the first plan stays: it was checked.
	for i := range plan.Items {
		plan.Items[i] = p2.Items[i]
	}
	return true
}

func planJSONOf(plan *promote.Plan, strategies []string) map[string]any {
	items := make([]map[string]any, 0, len(plan.Items))
	for i, it := range plan.Items {
		m := map[string]any{"service": it.Service}
		if it.SkippedReason != "" {
			m["skipped_reason"] = it.SkippedReason
			items = append(items, m)
			continue
		}
		changes := make([]map[string]any, 0, len(it.Changes))
		for _, c := range it.Changes {
			changes = append(changes, map[string]any{"path": c.Path, "before": c.Before, "after": c.After})
		}
		m["source_release_id"], m["source_release_number"], m["image_digest"] = it.SourceReleaseID, it.SourceReleaseNumber, it.ImageDigest
		m["changes"] = changes
		m["variables_added"], m["variables_removed"] = nonNil(it.VariablesAdded), nonNil(it.VariablesRemoved)
		m["missing_variables"], m["allowed_missing"] = nonNil(it.Missing), nonNil(it.Allowed)
		m["create"] = it.Create
		if it.TargetServiceID != "" {
			m["target_service_id"] = it.TargetServiceID
		}
		if it.TargetReleaseID != "" {
			m["target_release_id"] = it.TargetReleaseID
		}
		if strategies[i] != "" {
			m["strategy"] = strategies[i]
		}
		ws := make([]gen.Warning, 0, len(it.Warnings))
		for _, w := range it.Warnings {
			ws = append(ws, gen.Warning{Code: ptr(w.Code), Message: ptr(w.Message)})
		}
		m["warnings"] = ws
		items = append(items, m)
	}
	ws := make([]gen.Warning, 0, len(plan.Warnings))
	for _, w := range plan.Warnings {
		ws = append(ws, gen.Warning{Code: ptr(w.Code), Message: ptr(w.Message)})
	}
	return map[string]any{"plan_hash": plan.Hash, "items": items, "warnings": ws}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// promoteOrchFailed is orchestratorFailed with the error of a promotion.
func (h *Handlers) promoteOrchFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, orchestrator.ErrSourceMismatch) {
		invalid(w, r, "target_environment_id", "the target service is an image service and cannot run an image built by Polipan. Make it a git service")
		return true
	}
	return h.orchestratorFailed(w, r, err)
}
