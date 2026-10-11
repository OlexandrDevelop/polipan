// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/preview"
	"github.com/polipan/polipan/server/internal/store"
)

func (h *Handlers) orgID(r *http.Request) string { return PrincipalFrom(r.Context()).OrgID }

// fail maps a store error to an answer. It returns true when the error was handled.
func fail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		notFoundError(w, r)
	default:
		internalError(w, r, err)
	}
	return true
}

func projectJSON(p *store.Project) gen.Project {
	var settings gen.ProjectSettings
	_ = json.Unmarshal(p.Settings, &settings)
	state := gen.ProjectConfigSyncState(p.ConfigSyncState)
	out := gen.Project{Id: &p.ID, Name: &p.Name, Slug: &p.Slug, Settings: &settings, ConfigSyncState: &state, CreatedAt: &p.CreatedAt}
	if len(p.ConfigSource) > 0 && string(p.ConfigSource) != "null" {
		_ = json.Unmarshal(p.ConfigSource, &out.ConfigSource)
	}
	return out
}

func envJSON(e *store.Environment) gen.Environment {
	kind := gen.EnvironmentKind(e.Kind)
	out := gen.Environment{Id: &e.ID, ProjectId: &e.ProjectID, Name: &e.Name, Slug: &e.Slug, Kind: &kind,
		Protected: &e.Protected, BaseEnvironmentId: e.BaseEnvID}
	var settings map[string]any
	if json.Unmarshal(e.Settings, &settings) == nil {
		out.Settings = &settings
	}
	return out
}

// ListProjects lists the projects of the org.
func (h *Handlers) ListProjects(w http.ResponseWriter, r *http.Request, params gen.ListProjectsParams) {
	cur, ok := parseCursor(w, r, params.Cursor)
	if !ok {
		return
	}
	limit := pageLimit(params.Limit)
	list, err := h.Store.ListProjects(r.Context(), h.orgID(r), limit, cur)
	if fail(w, r, err) {
		return
	}
	out := page[gen.Project]{Items: []gen.Project{}}
	for i := range list {
		if i == limit {
			next := store.Cursor{Time: list[limit-1].CreatedAt, ID: list[limit-1].ID}.Encode()
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, projectJSON(&list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateProject makes a project. Unless default_environments is false it also makes production and staging.
func (h *Handlers) CreateProject(w http.ResponseWriter, r *http.Request) {
	var body gen.CreateProjectJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := validName(body.Name)
	if !ok {
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	slug := slugify(name)
	if body.Slug != nil {
		slug = *body.Slug
	}
	if !slugRe.MatchString(slug) {
		invalid(w, r, "slug", "slug must use lower case letters, digits and dashes (1 to 40 characters)")
		return
	}
	var envs []store.NewEnv
	if body.DefaultEnvironments == nil || *body.DefaultEnvironments {
		envs = []store.NewEnv{
			{Name: "Production", Slug: "production", Kind: "production", Protected: true},
			{Name: "Staging", Slug: "staging", Kind: "staging"},
		}
	}
	p, err := h.Store.CreateProject(r.Context(), h.orgID(r), name, slug, envs)
	if unique, _ := store.IsUnique(err); unique {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a project with this slug exists", map[string]any{"field": "slug"})
		return
	}
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, projectJSON(p))
}

// GetProject returns a project.
func (h *Handlers) GetProject(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	p, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, projectJSON(p))
}

// UpdateProject changes the name and the settings. Settings are merged by top-level key.
func (h *Handlers) UpdateProject(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.ProjectUpdate
	if !decodeJSON(w, r, &body) {
		return
	}
	var source map[string]any
	if body.ConfigSource != nil { // REQ-CAC-012: an admin sets the source of the config file
		if requireAdmin(w, r, "changing config_source") {
			return
		}
		var ok bool
		if source, ok = h.configSourceOf(w, r, *body.ConfigSource); !ok {
			return
		}
	}
	cur, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	var name *string
	if body.Name != nil {
		n, ok := validName(*body.Name)
		if !ok {
			invalid(w, r, "name", "name must have 1 to 100 characters")
			return
		}
		name = &n
	}
	var settings json.RawMessage
	if s := body.Settings; s != nil {
		if (s.Previews != nil && s.Previews.FromForks != nil) && requireAdmin(w, r, "changing previews.from_forks") {
			return
		}
		if (s.RetainReleases != nil && *s.RetainReleases < 1) || (s.BuildTimeoutMinutes != nil && *s.BuildTimeoutMinutes < 1) {
			invalid(w, r, "settings", "retain_releases and build_timeout_minutes must be 1 or more")
			return
		}
		merged := map[string]any{}
		_ = json.Unmarshal(cur.Settings, &merged)
		var patch map[string]any
		raw, _ := json.Marshal(s)
		_ = json.Unmarshal(raw, &patch)
		for k, v := range patch {
			merged[k] = v
		}
		settings, _ = json.Marshal(merged)
	}
	if body.ConfigSource != nil {
		var raw json.RawMessage
		if len(source) > 0 {
			raw, _ = json.Marshal(source)
		}
		if fail(w, r, h.Store.SetConfigSource(r.Context(), h.orgID(r), projectID, raw)) {
			return
		}
		h.auditUser(r, h.orgID(r), projectID, "project.config_source", "project", projectID, map[string]any{"config_source": source})
	}
	p, err := h.Store.UpdateProject(r.Context(), h.orgID(r), projectID, name, settings)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, projectJSON(p))
}

// DeleteProject soft deletes a project. confirm must be the slug.
func (h *Handlers) DeleteProject(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId, params gen.DeleteProjectParams) {
	p, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	if params.Confirm != p.Slug {
		invalid(w, r, "confirm", "confirm must be the slug of the project")
		return
	}
	ids, err := h.Store.SoftDeleteProject(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	h.servicesRemoved(r, ids)
	w.WriteHeader(http.StatusNoContent)
}

// servicesRemoved tells the orchestrator about deleted services. The rows are already gone,
// so an error is logged and the node gets the right state at its next change.
func (h *Handlers) servicesRemoved(r *http.Request, ids []string) {
	for _, id := range ids {
		if err := h.Orch.ServiceRemoved(r.Context(), id); err != nil {
			logging.From(r.Context()).Error("service removal failed", "service_id", id, "error", err)
		}
	}
}

// ListEnvironments lists the environments of a project.
func (h *Handlers) ListEnvironments(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	if _, err := h.Store.GetProject(r.Context(), h.orgID(r), projectID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListEnvironments(r.Context(), h.orgID(r), projectID)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Environment, len(list))
	for i := range list {
		out[i] = envJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateEnvironment adds an environment. A production environment is protected by default.
func (h *Handlers) CreateEnvironment(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.CreateEnvironmentJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := validName(body.Name)
	kind := string(body.Kind)
	switch {
	case !ok:
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	case kind != "production" && kind != "staging" && kind != "custom":
		invalid(w, r, "kind", "kind must be production, staging or custom")
		return
	case body.CloneFromEnvironmentId != nil:
		invalid(w, r, "clone_from_environment_id", "cloning an environment is not available in this version")
		return
	case body.Protected != nil && requireAdmin(w, r, "setting protected"):
		return
	}
	slug := slugify(name)
	if !slugRe.MatchString(slug) {
		invalid(w, r, "name", "name must have letters or digits")
		return
	}
	protected := kind == "production"
	if body.Protected != nil {
		protected = *body.Protected
	}
	e, err := h.Store.CreateEnvironment(r.Context(), h.orgID(r), projectID, store.NewEnv{Name: name, Slug: slug, Kind: kind, Protected: protected})
	if unique, _ := store.IsUnique(err); unique {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "an environment with this name exists in the project", map[string]any{"field": "name"})
		return
	}
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, envJSON(e))
}

// GetEnvironment returns an environment.
func (h *Handlers) GetEnvironment(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId) {
	e, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, envJSON(e))
}

// UpdateEnvironment changes name, protected and settings. Changing protected needs an admin.
func (h *Handlers) UpdateEnvironment(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId) {
	var body gen.UpdateEnvironmentJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	cur, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "env:update", cur.Protected) {
		return
	}
	if body.Protected != nil && requireAdmin(w, r, "changing protected") {
		return
	}
	var name *string
	if body.Name != nil {
		n, ok := validName(*body.Name)
		if !ok {
			invalid(w, r, "name", "name must have 1 to 100 characters")
			return
		}
		name = &n
	}
	var settings json.RawMessage
	if body.Settings != nil {
		settings, _ = json.Marshal(*body.Settings)
	}
	e, err := h.Store.UpdateEnvironment(r.Context(), h.orgID(r), environmentID, name, body.Protected, settings)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, envJSON(e))
}

// DeleteEnvironment soft deletes an environment and its services. M1 has no volumes, so delete_volumes does nothing.
func (h *Handlers) DeleteEnvironment(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId, _ gen.DeleteEnvironmentParams) {
	cur, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	if cur.Kind == "preview" && h.Git != nil && h.Git.Previews != nil {
		// A preview is deleted with its containers, volumes, domains and images (REQ-PREV-007, REQ-PREV-014).
		if err := h.Git.Previews.Delete(r.Context(), h.orgID(r), environmentID, PrincipalFrom(r.Context()).User.ID); err != nil {
			if errors.Is(err, preview.ErrNotFound) {
				notFoundError(w, r)
				return
			}
			WriteError(w, r, http.StatusConflict, CodeConflict, "the preview was not deleted: "+err.Error(), map[string]any{"reason": "preview_delete_failed"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if denyProtected(w, r, "env:delete", cur.Protected) {
		return
	}
	ids, err := h.Store.SoftDeleteEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	h.servicesRemoved(r, ids)
	w.WriteHeader(http.StatusNoContent)
}

func serviceJSON(s *store.Service) gen.Service {
	var cfg gen.ServiceConfig
	_ = json.Unmarshal(s.Config, &cfg)
	typ, src, desired, failover, status := gen.ServiceType(s.Type), gen.ServiceSourceType(s.SourceType),
		gen.ServiceDesiredState(s.DesiredState), gen.ServiceFailover(s.Failover), gen.ServiceStatus(s.Status)
	managed := s.GitManaged
	if managed == nil {
		managed = []string{}
	}
	pl := make([]gen.Placement, len(s.Placements))
	for i, p := range s.Placements {
		color, role, health := gen.PlacementColor(p.Color), gen.PlacementRole(p.Role), gen.PlacementHealth(p.Health)
		pl[i] = gen.Placement{Id: ptr(p.ID), NodeId: ptr(p.NodeID), ReleaseId: ptr(p.ReleaseID), ReplicaIndex: ptr(p.ReplicaIndex),
			Color: &color, Role: &role, Weight: ptr(p.Weight), Health: &health}
	}
	out := gen.Service{Id: &s.ID, EnvironmentId: &s.EnvironmentID, Name: &s.Name, Type: &typ, SourceType: &src, Config: &cfg,
		GitManagedFields: &managed, Status: &status, DesiredState: &desired, Failover: &failover,
		AllowDataLossFailover: &s.AllowDataLoss, LiveReleaseId: s.LiveReleaseID, Placements: &pl, PendingChanges: ptr(false)}
	if s.DBEngine != nil {
		e := gen.ServiceDbEngine(*s.DBEngine)
		out.DbEngine = &e
	}
	return out
}

// ListServices lists the services of an environment.
func (h *Handlers) ListServices(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId) {
	if _, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID); fail(w, r, err) {
		return
	}
	list, err := h.Store.ListServices(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Service, len(list))
	for i := range list {
		out[i] = h.decorateService(r.Context(), serviceJSON(&list[i]), &list[i])
		out[i].PendingChanges = ptr(h.pendingChanges(r.Context(), &list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateService makes an image service. Other types and git sources come in later milestones.
func (h *Handlers) CreateService(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId) {
	var body gen.ServiceCreate
	if !decodeJSON(w, r, &body) {
		return
	}
	env, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:create", env.Protected) {
		return
	}
	switch {
	case !serviceNameRe.MatchString(body.Name):
		invalid(w, r, "name", "name must use lower case letters, digits and dashes (1 to 40 characters)")
		return
	case body.Type != "app" && body.Type != "database":
		invalid(w, r, "type", "only app and database services can be created in this version")
		return
	case body.Type == "app" && (body.DbEngine != nil || body.DbVersion != nil):
		invalid(w, r, "db_engine", "db_engine is only for a database service")
		return
	}
	if body.Type == "database" {
		h.createDatabase(w, r, env, body)
		return
	}
	var cfg, src map[string]any
	if body.Config != nil {
		cfg = *body.Config
	}
	if body.Source != nil {
		src = *body.Source
	}
	sourceType := "image"
	var raw json.RawMessage
	if _, isGit := gitSourceOf(cfg, src); isGit { // git source: handlers_git.go
		sourceType = "git"
		if raw, err = h.buildGitConfig(r, env.Protected, cfg, src, nil); err != nil {
			writeGitConfigError(w, r, err)
			return
		}
	} else if raw, err = buildImageConfig(cfg, src); err != nil {
		invalid(w, r, "config", err.Error())
		return
	}
	if err := h.Settings.panelPortClash(raw); err != nil {
		invalid(w, r, "config", err.Error())
		return
	}
	s, err := h.Store.CreateService(r.Context(), h.orgID(r), store.NewService{
		EnvironmentID: environmentID, Name: body.Name, Type: "app", SourceType: sourceType, Config: raw})
	if unique, _ := store.IsUnique(err); unique {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a service with this name exists in the environment", map[string]any{"field": "name"})
		return
	}
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, h.decorateService(r.Context(), serviceJSON(s), s))
}

// GetService returns a service with its placements.
func (h *Handlers) GetService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	out := h.decorateService(r.Context(), serviceJSON(s), s)
	out.PendingChanges = ptr(h.pendingChanges(r.Context(), s))
	WriteJSON(w, http.StatusOK, out)
}

// UpdateService replaces the config and changes failover. The config is used from the next deployment.
func (h *Handlers) UpdateService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	var body gen.ServiceUpdate
	if !decodeJSON(w, r, &body) {
		return
	}
	cur, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:update", cur.EnvProtected) {
		return
	}
	var cfg json.RawMessage
	var dbWarnings []gen.Warning
	if body.BackupSkipped != nil {
		if cur.Type != "database" {
			invalid(w, r, "backup_skipped", "only a database service has a backup suggestion")
			return
		}
		if fail(w, r, h.Store.SetBackupSkipped(r.Context(), h.orgID(r), serviceID, *body.BackupSkipped)) {
			return
		}
	}
	if body.Config != nil && cur.Type == "database" {
		merged := map[string]any{}
		_ = json.Unmarshal(cur.Config, &merged)
		for k, v := range *body.Config {
			merged[k] = v
		}
		var ok bool
		if dbWarnings, ok = h.checkDatabaseUpdate(w, r, cur, merged); !ok {
			return
		}
		raw, err := checkImageConfig(merged)
		if err != nil {
			invalid(w, r, "config", err.Error())
			return
		}
		cfg = raw
	} else if body.Config != nil && cur.SourceType == "git" { // handlers_git.go
		current, _ := git.SourceOf(cur.Config)
		if cfg, err = h.buildGitConfig(r, cur.EnvProtected, map[string]any(*body.Config), nil, current); err != nil {
			writeGitConfigError(w, r, err)
			return
		}
	} else if body.Config != nil {
		cfg, err = checkImageConfig(map[string]any(*body.Config))
		if err != nil {
			invalid(w, r, "config", err.Error())
			return
		}
	}
	if err := h.Settings.panelPortClash(cfg); err != nil {
		invalid(w, r, "config", err.Error())
		return
	}
	var failover *string
	if body.Failover != nil {
		failover = ptr(string(*body.Failover))
	}
	// A field that polipan.yaml sets is read only (REQ-CAC-005).
	var newCfg map[string]any
	if cfg != nil {
		_ = json.Unmarshal(cfg, &newCfg)
	} else {
		_ = json.Unmarshal(cur.Config, &newCfg)
	}
	drift, stop := h.guardManagedUpdate(w, r, cur, newCfg, failover, body.AllowDataLossFailover, body.OverrideGit != nil && *body.OverrideGit)
	if stop {
		return
	}
	s, err := h.Store.UpdateService(r.Context(), h.orgID(r), serviceID, cfg, failover, body.AllowDataLossFailover)
	if fail(w, r, err) {
		return
	}
	if len(drift) > 0 {
		h.recordDrift(r, cur, drift)
	}
	out := h.decorateService(r.Context(), serviceJSON(s), s)
	if len(dbWarnings) > 0 {
		out.Warnings = &dbWarnings
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteService soft deletes a service. confirm must be its name.
func (h *Handlers) DeleteService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.DeleteServiceParams) {
	cur, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:delete", cur.EnvProtected) {
		return
	}
	if params.Confirm != cur.Name {
		invalid(w, r, "confirm", "confirm must be the name of the service")
		return
	}
	if params.DeleteVolumes != nil && *params.DeleteVolumes && !h.removeData(w, r, []string{serviceID}) {
		return
	}
	if fail(w, r, h.Store.SoftDeleteService(r.Context(), h.orgID(r), serviceID)) {
		return
	}
	h.servicesRemoved(r, []string{serviceID})
	w.WriteHeader(http.StatusNoContent)
}

func deploymentJSON(d *store.Deployment) gen.Deployment {
	state, trigger, strategy := gen.DeploymentState(d.State), gen.DeploymentTrigger(d.Trigger), gen.Strategy(d.Strategy)
	out := gen.Deployment{Id: &d.ID, ServiceId: &d.ServiceID, EnvironmentId: &d.EnvironmentID, ReleaseId: d.ReleaseID,
		PreviousReleaseId: d.PrevReleaseID, Trigger: &trigger, Strategy: &strategy, State: &state, CanaryStep: d.CanaryStep,
		FailureReason: d.FailureReason, FailureDetail: d.FailureDetail, ActorId: d.ActorID, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt}
	var opts gen.StrategyOptions
	if json.Unmarshal(d.StrategyOptions, &opts) == nil {
		out.StrategyOptions = &opts
		if d.Strategy == "canary" {
			out.CanarySteps = opts.CanarySteps
		}
	}
	if d.Strategy == "canary" {
		out.CanaryPaused = &d.CanaryPaused
		var gate map[string]any
		if len(d.CanaryGate) > 0 && json.Unmarshal(d.CanaryGate, &gate) == nil && gate != nil {
			out.CanaryGate = &gate
		}
	}
	var warnings []gen.Warning
	if json.Unmarshal(d.Warnings, &warnings) == nil && len(warnings) > 0 {
		out.Warnings = &warnings
	}
	if d.Steps != nil {
		steps := make([]struct {
			Message *string    `json:"message,omitempty"`
			State   *string    `json:"state,omitempty"`
			Time    *time.Time `json:"time,omitempty"`
		}, len(d.Steps))
		for i, s := range d.Steps {
			steps[i].Message, steps[i].State, steps[i].Time = s.Message, ptr(s.State), ptr(s.CreatedAt)
		}
		out.Steps = &steps
	}
	return out
}

// StopService stops the containers. The service, its release and its domains stay (AC-APP-05).
func (h *Handlers) StopService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	h.setRunning(w, r, serviceID, false)
}

// StartService starts a stopped service.
func (h *Handlers) StartService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	h.setRunning(w, r, serviceID, true)
}

func (h *Handlers) setRunning(w http.ResponseWriter, r *http.Request, serviceID string, running bool) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:update", s.EnvProtected) {
		return
	}
	if h.orchestratorFailed(w, r, h.Orch.SetRunning(r.Context(), serviceID, running)) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ListServiceDeployments lists the deployments of a service, newest first.
func (h *Handlers) ListServiceDeployments(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.ListServiceDeploymentsParams) {
	cur, ok := parseCursor(w, r, params.Cursor)
	if !ok {
		return
	}
	if _, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID); fail(w, r, err) {
		return
	}
	limit := pageLimit(params.Limit)
	list, err := h.Store.ListDeployments(r.Context(), h.orgID(r), serviceID, limit, cur)
	if fail(w, r, err) {
		return
	}
	out := page[gen.Deployment]{Items: []gen.Deployment{}}
	for i := range list {
		if i == limit {
			next := store.Cursor{Time: list[limit-1].CreatedAt, ID: list[limit-1].ID}.Encode()
			out.NextCursor = &next
			break
		}
		out.Items = append(out.Items, deploymentJSON(&list[i]))
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetDeployment returns a deployment with its steps.
func (h *Handlers) GetDeployment(w http.ResponseWriter, r *http.Request, deploymentID gen.DeploymentId) {
	d, err := h.Store.GetDeployment(r.Context(), h.orgID(r), deploymentID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, deploymentJSON(d))
}

func pageLimitLogs(l *gen.Limit) int {
	if l == nil {
		return 200
	}
	return pageLimit(l)
}
