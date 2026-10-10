// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/tpl"
	"github.com/polipan/polipan/server/internal/vars"
)

// CodeTemplateUnsafe is the answer for a manifest or a Compose file with unsafe settings (REQ-TPL-005).
const CodeTemplateUnsafe = "template_unsafe"

// templateEntry is a manifest with its origin.
type templateEntry struct {
	m       *tpl.Manifest
	builtin bool
}

func templateJSON(e templateEntry) gen.Template {
	m := e.m
	inputs := make([]map[string]interface{}, len(m.Inputs))
	for i, in := range m.Inputs {
		inputs[i] = map[string]interface{}{"name": in.Name, "label": in.Label, "type": in.Type, "required": in.Required, "generate": in.Generate}
		if in.Default != nil {
			inputs[i]["default"] = in.Default
		}
	}
	services := []map[string]interface{}{}
	for _, name := range sortedNames(m.Services) {
		var entry map[string]interface{}
		b, _ := json.Marshal(m.Services[name])
		_ = json.Unmarshal(b, &entry)
		entry["name"] = name
		services = append(services, entry)
	}
	return gen.Template{Slug: &m.Slug, Version: &m.Version, Name: &m.Name, Description: &m.Description, Icon: &m.Icon,
		Builtin: &e.builtin, Inputs: &inputs, Services: &services}
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// findTemplate returns a built-in or a custom template. Built-in templates win.
func (h *Handlers) findTemplate(r *http.Request, slug string) (*templateEntry, error) {
	builtin, err := tpl.Builtin()
	if err != nil {
		return nil, err
	}
	for _, m := range builtin {
		if m.Slug == slug {
			return &templateEntry{m: m, builtin: true}, nil
		}
	}
	c, err := h.Store.GetCustomTemplate(r.Context(), h.orgID(r), slug)
	if err != nil {
		return nil, err
	}
	m, err := tpl.ParseManifest(c.Manifest)
	if err != nil {
		return nil, fmt.Errorf("custom template %s: %w", slug, err)
	}
	return &templateEntry{m: m}, nil
}

// ListTemplates lists the built-in and the custom templates (REQ-TPL-004).
func (h *Handlers) ListTemplates(w http.ResponseWriter, r *http.Request) {
	builtin, err := tpl.Builtin()
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := []gen.Template{}
	for _, m := range builtin {
		out = append(out, templateJSON(templateEntry{m: m, builtin: true}))
	}
	custom, err := h.Store.ListCustomTemplates(r.Context(), h.orgID(r))
	if fail(w, r, err) {
		return
	}
	for _, c := range custom {
		m, err := tpl.ParseManifest(c.Manifest)
		if err != nil {
			continue // a manifest that no longer passes the checks is not offered
		}
		out = append(out, templateJSON(templateEntry{m: m}))
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetTemplate returns one template with its inputs.
func (h *Handlers) GetTemplate(w http.ResponseWriter, r *http.Request, slug string) {
	e, err := h.findTemplate(r, slug)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, templateJSON(*e))
}

// templateFailed writes the answer for a manifest or Compose error. It returns true when it wrote one.
func templateFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var unsafe *tpl.UnsafeError
	var ps tpl.Problems
	var ee *tpl.ExpandError
	switch {
	case err == nil:
		return false
	case errors.As(err, &unsafe):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeTemplateUnsafe, unsafe.Error(), map[string]any{"violations": unsafe.Violations})
	case errors.As(err, &ee):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, ee.Error(), map[string]any{"problems": ee.Problems})
	case errors.As(err, &ps):
		WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed, ps.Error(), map[string]any{"problems": ps})
	default:
		return false
	}
	return true
}

// readBody reads the raw request body up to the size limit.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w, r, DefaultMaxBodyBytes)
		} else {
			invalid(w, r, "body", "the request body cannot be read")
		}
		return nil, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		invalid(w, r, "body", "request body is required")
		return nil, false
	}
	return data, true
}

// CreateTemplate adds a custom template. The manifest passes the safety validator and a trial expansion (REQ-TPL-005).
func (h *Handlers) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	data, ok := readBody(w, r)
	if !ok {
		return
	}
	m, err := tpl.ParseManifest(data)
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	if _, err := h.trialPlan(m); templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	if e, _ := h.findTemplateBuiltin(m.Slug); e != nil {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a built-in template has this slug", map[string]any{"field": "slug"})
		return
	}
	raw, _ := json.Marshal(m)
	t, err := h.Store.CreateCustomTemplate(r.Context(), h.orgID(r), m.Slug, m.Version, raw)
	if unique, _ := store.IsUnique(err); unique {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a template with this slug and version exists", map[string]any{"field": "version"})
		return
	}
	if fail(w, r, err) {
		return
	}
	h.auditUser(r, h.orgID(r), "", "template.create", "template", t.ID, map[string]any{"slug": m.Slug, "version": m.Version})
	WriteJSON(w, http.StatusCreated, templateJSON(templateEntry{m: m}))
}

func (h *Handlers) findTemplateBuiltin(slug string) (*tpl.Manifest, error) {
	list, err := tpl.Builtin()
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if m.Slug == slug {
			return m, nil
		}
	}
	return nil, nil
}

// trialPlan expands a manifest with sample inputs to check that every service config is valid.
func (h *Handlers) trialPlan(m *tpl.Manifest) (*tpl.Plan, error) {
	inputs := map[string]any{}
	for _, in := range m.Inputs {
		switch in.Type {
		case "domain":
			inputs[in.Name] = "app.example.com"
		case "number":
			inputs[in.Name] = 1
		case "bool":
			inputs[in.Name] = "true"
		default:
			inputs[in.Name] = "sample"
		}
	}
	ids := map[string]string{}
	for name := range m.Services {
		ids[name] = store.NewID("svc")
	}
	plan, err := m.Expand(inputs, tpl.Options{ServiceIDs: ids, NormalizeHost: normalizeHost})
	if err != nil {
		return nil, err
	}
	_, err = h.serviceInputs(plan, ids)
	return plan, err
}

// serviceInputs checks every planned service like a service made by hand, and returns the store input.
// A problem is a 422 and nothing is created (AC-TPL-04).
func (h *Handlers) serviceInputs(plan *tpl.Plan, ids map[string]string) ([]store.ServiceInput, error) {
	var ps tpl.Problems
	var out []store.ServiceInput
	for _, s := range plan.Services {
		path := "services." + s.Name
		raw, err := checkImageConfig(jsonRoundTrip(s.Config))
		if err != nil {
			ps = append(ps, tpl.Problem{Path: path, Message: err.Error()})
			continue
		}
		if err := h.Settings.panelPortClash(raw); err != nil {
			ps = append(ps, tpl.Problem{Path: path, Message: err.Error()})
		}
		in := store.ServiceInput{ID: ids[s.Name], Name: s.Name, Type: s.Type, DBEngine: s.DBEngine, DBVersion: s.DBVersion, Config: raw}
		seen := map[string]bool{}
		for _, v := range s.Volumes {
			switch {
			case !volumeNameRe.MatchString(v.Name):
				ps = append(ps, tpl.Problem{Path: path + ".volumes", Message: fmt.Sprintf("volume name %q is not valid", v.Name)})
			case !validMountPath(v.MountPath):
				ps = append(ps, tpl.Problem{Path: path + ".volumes", Message: fmt.Sprintf("path %q must be a clean absolute path", v.MountPath)})
			case seen[v.Name] || seen[v.MountPath]:
				ps = append(ps, tpl.Problem{Path: path + ".volumes", Message: fmt.Sprintf("volume %s is used twice", v.Name)})
			}
			seen[v.Name], seen[v.MountPath] = true, true
			in.Volumes = append(in.Volumes, store.Volume{Name: v.Name, Kind: v.Kind, MountPath: v.MountPath, AccessMode: v.AccessMode, SizeLimitBytes: v.SizeLimit})
		}
		for _, d := range s.Domains {
			host, err := normalizeHost(d.Host)
			if err != nil {
				ps = append(ps, tpl.Problem{Path: path + ".domains", Message: err.Error()})
				continue
			}
			if !validPathPrefix(d.PathPrefix) || d.Port < 1 || d.Port > 65535 {
				ps = append(ps, tpl.Problem{Path: path + ".domains", Message: "path_prefix or port is not valid"})
				continue
			}
			dom := store.Domain{Host: host, PathPrefix: d.PathPrefix, ContainerPort: d.Port, HTTPS: d.HTTPS}
			if d.RedirectWWW != "" {
				v := d.RedirectWWW
				dom.RedirectWWW = &v
			}
			in.Domains = append(in.Domains, dom)
		}
		in.Env = nil
		for _, e := range s.Env {
			if e.Secret && e.Value == "" {
				continue // an optional secret input that was left empty
			}
			if err := vars.CheckName(e.Name); err != nil {
				ps = append(ps, tpl.Problem{Path: path + ".env", Message: fmt.Sprintf("%s: %s", e.Name, err.Error())})
			}
			in.Env = append(in.Env, e)
		}
		out = append(out, in)
	}
	if len(ps) > 0 {
		return nil, ps
	}
	return out, nil
}

// InstantiateTemplate creates the services of a template in one transaction (REQ-TPL-003).
func (h *Handlers) InstantiateTemplate(w http.ResponseWriter, r *http.Request, slug string, params gen.InstantiateTemplateParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	var body struct {
		EnvironmentID string         `json:"environment_id"`
		Inputs        map[string]any `json:"inputs"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	e, err := h.findTemplate(r, slug)
	if fail(w, r, err) {
		return
	}
	env, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), body.EnvironmentID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:create", env.Protected) {
		return
	}
	if h.Secrets == nil {
		internalError(w, r, errors.New("the secrets store is not set up"))
		return
	}
	ids := map[string]string{}
	for name := range e.m.Services {
		ids[name] = store.NewID("svc")
	}
	plan, err := e.m.Expand(body.Inputs, tpl.Options{ServiceIDs: ids, NormalizeHost: normalizeHost})
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	in, err := h.serviceInputs(plan, ids)
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	for _, s := range in {
		for _, d := range s.Domains {
			if h.refusePanelHost(w, r, d.Host) {
				return
			}
		}
	}
	inputsJSON, _ := json.Marshal(plan.Inputs)
	created, err := h.Store.CreateServices(r.Context(), h.Secrets.Provider, h.orgID(r), env.ID, in,
		&store.InstanceInput{TemplateID: plan.Slug, Version: plan.Version, Inputs: inputsJSON}, actor(r))
	if h.groupFailed(w, r, err) {
		return
	}
	out := make([]gen.Service, 0, len(created))
	for _, sid := range created {
		s, err := h.Store.GetService(r.Context(), h.orgID(r), sid)
		if fail(w, r, err) {
			return
		}
		out = append(out, h.decorateService(r.Context(), serviceJSON(s), s))
	}
	h.saveIdempotent(r, id, http.StatusCreated, out)
	WriteJSON(w, http.StatusCreated, out)
}

// ImportCompose converts a Docker Compose file (REQ-TPL-006). With dry_run the answer is the result only.
// Without it the services are created in the environment, in one transaction.
func (h *Handlers) ImportCompose(w http.ResponseWriter, r *http.Request, environmentID gen.EnvironmentId, params gen.ImportComposeParams) {
	env, err := h.Store.GetEnvironment(r.Context(), h.orgID(r), environmentID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:create", env.Protected) {
		return
	}
	data, ok := readBody(w, r)
	if !ok {
		return
	}
	res, err := tpl.ImportCompose(data)
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	ids := map[string]string{}
	for name := range res.Manifest.Services {
		ids[name] = store.NewID("svc")
	}
	plan, err := res.Manifest.Expand(nil, tpl.Options{ServiceIDs: ids, NormalizeHost: normalizeHost})
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	in, err := h.serviceInputs(plan, ids)
	if templateFailed(w, r, err) || fail(w, r, err) {
		return
	}
	warnings := append(res.Warnings, plan.Warnings...)
	if warnings == nil {
		warnings = []string{}
	}
	if params.DryRun != nil && *params.DryRun {
		list := make([]gen.ServiceCreate, 0, len(in))
		for _, s := range in {
			var cfg gen.ServiceConfig
			_ = json.Unmarshal(s.Config, &cfg)
			list = append(list, gen.ServiceCreate{Name: s.Name, Type: gen.ServiceCreateType(s.Type), Config: &cfg})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"services": list, "warnings": warnings})
		return
	}
	if h.Secrets == nil {
		internalError(w, r, errors.New("the secrets store is not set up"))
		return
	}
	created, err := h.Store.CreateServices(r.Context(), h.Secrets.Provider, h.orgID(r), env.ID, in, nil, actor(r))
	if h.groupFailed(w, r, err) {
		return
	}
	list := make([]gen.Service, 0, len(created))
	for _, sid := range created {
		s, err := h.Store.GetService(r.Context(), h.orgID(r), sid)
		if fail(w, r, err) {
			return
		}
		list = append(list, h.decorateService(r.Context(), serviceJSON(s), s))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"services": list, "warnings": warnings})
}

// DeleteTemplateInstance deletes all services of an instance as a group (REQ-TPL-008). Volumes go only with delete_volumes=true.
func (h *Handlers) DeleteTemplateInstance(w http.ResponseWriter, r *http.Request, instanceID string, params gen.DeleteTemplateInstanceParams) {
	ti, err := h.Store.GetTemplateInstance(r.Context(), h.orgID(r), instanceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:delete", ti.Protected) {
		return
	}
	if params.Confirm != ti.TemplateID {
		invalid(w, r, "confirm", "confirm must be the slug of the template ("+ti.TemplateID+")")
		return
	}
	if params.DeleteVolumes != nil && *params.DeleteVolumes {
		if !h.removeData(w, r, ti.ServiceIDs) {
			return
		}
	}
	ids, err := h.Store.DeleteTemplateInstance(r.Context(), h.orgID(r), instanceID)
	if fail(w, r, err) {
		return
	}
	h.servicesRemoved(r, ids)
	h.auditUser(r, ti.OrgID, ti.ProjectID, "template_instance.delete", "template_instance", instanceID,
		map[string]any{"template": ti.TemplateID, "services": ids, "delete_volumes": params.DeleteVolumes != nil && *params.DeleteVolumes})
	w.WriteHeader(http.StatusNoContent)
}

// removeData removes the data of services on their nodes before they are deleted. It writes the answer and returns false on a failure.
func (h *Handlers) removeData(w http.ResponseWriter, r *http.Request, serviceIDs []string) bool {
	remover, ok := h.Orch.(orchestrator.DataRemover)
	if !ok {
		return true
	}
	for _, id := range serviceIDs {
		if err := remover.RemoveServiceData(r.Context(), id); err != nil {
			WriteError(w, r, http.StatusConflict, CodeConflict, "the data was not removed, nothing was deleted: "+err.Error(), map[string]any{"reason": "node_unavailable"})
			return false
		}
	}
	return true
}
