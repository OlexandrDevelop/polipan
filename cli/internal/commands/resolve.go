// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"net/url"
	"strings"

	"github.com/polipan/polipan/cli/internal/apiops"
)

// pick finds one item by id or by one of the name keys.
func pick(items []any, ref, kind string, keys ...string) (map[string]any, error) {
	var found []map[string]any
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := m["id"].(string); s == ref {
			return m, nil
		}
		for _, k := range keys {
			if s, _ := m[k].(string); s == ref {
				found = append(found, m)
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return nil, notFoundErr("%s %q not found", kind, ref)
	case 1:
		return found[0], nil
	}
	return nil, usageErr("%s %q is not unique: use the id", kind, ref)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// byRef resolves a name or id with a list call. A ref with the id prefix is used as it is.
func (a *app) byRef(ref, prefix, kind, opID string, in apiops.Input, keys ...string) (map[string]any, error) {
	if prefix != "" && strings.HasPrefix(ref, prefix) {
		return map[string]any{"id": ref}, nil
	}
	items, err := a.list(opID, in)
	if err != nil {
		return nil, err
	}
	return pick(items, ref, kind, keys...)
}

func (a *app) projectRef(ref string) (string, error) {
	if ref != "" {
		return ref, nil
	}
	e, err := a.effective()
	if err != nil {
		return "", err
	}
	if e.Project == "" {
		return "", usageErr("no project set: use --project or POLIPAN_PROJECT")
	}
	return e.Project, nil
}

// projectObj resolves a project by id, name or slug. An empty ref uses --project.
func (a *app) projectObj(ref string) (map[string]any, error) {
	ref, err := a.projectRef(ref)
	if err != nil {
		return nil, err
	}
	return a.byRef(ref, "prj_", "project", "listProjects", apiops.Input{}, "slug", "name")
}

func (a *app) projectID(ref string) (string, error) {
	m, err := a.projectObj(ref)
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) envObj(ref, project string) (map[string]any, error) {
	if strings.HasPrefix(ref, "env_") {
		return map[string]any{"id": ref}, nil
	}
	pid, err := a.projectID(project)
	if err != nil {
		return nil, err
	}
	return a.byRef(ref, "env_", "environment", "listEnvironments", ids(pid), "slug", "name")
}

// envID resolves an environment of the project in --project. An empty ref uses --env.
func (a *app) envID(ref string) (string, error) {
	if ref == "" {
		e, err := a.effective()
		if err != nil {
			return "", err
		}
		ref = e.Env
	}
	if ref == "" {
		return "", usageErr("no environment set: use --env or POLIPAN_ENV")
	}
	m, err := a.envObj(ref, "")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

// serviceID resolves "svc_id", "[project/]env/service", or a service name with --project and --env.
func (a *app) serviceID(ref string) (string, error) {
	if strings.HasPrefix(ref, "svc_") {
		return ref, nil
	}
	e, err := a.effective()
	if err != nil {
		return "", err
	}
	parts := strings.Split(ref, "/")
	project, env, name := e.Project, e.Env, ""
	switch len(parts) {
	case 1:
		name = parts[0]
	case 2:
		env, name = parts[0], parts[1]
	case 3:
		project, env, name = parts[0], parts[1], parts[2]
	default:
		return "", usageErr("a service is written [project/]env/service, got %q", ref)
	}
	if project == "" {
		return "", usageErr("no project set for service %q: use --project or project/env/service", ref)
	}
	if env == "" {
		return "", usageErr("no environment set for service %q: use --env or env/service", ref)
	}
	eo, err := a.envObj(env, project)
	if err != nil {
		return "", err
	}
	m, err := a.byRef(name, "svc_", "service", "listServices", ids(str(eo, "id")), "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) serviceObj(ref string) (map[string]any, error) {
	id, err := a.serviceID(ref)
	if err != nil {
		return nil, err
	}
	v, err := a.getJSON("getService", ids(id))
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

func (a *app) nodeID(ref string) (string, error) {
	m, err := a.byRef(ref, "nod_", "node", "listNodes", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) userID(ref string) (string, error) {
	m, err := a.byRef(ref, "usr_", "member", "listMembers", apiops.Input{}, "email", "name")
	if err != nil {
		return "", err
	}
	if id := str(m, "user_id"); id != "" {
		return id, nil
	}
	return str(m, "id"), nil
}

func (a *app) teamID(ref string) (string, error) {
	m, err := a.byRef(ref, "team_", "team", "listTeams", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) ssoID(ref string) (string, error) {
	m, err := a.byRef(ref, "sso_", "SSO provider", "listSsoProviders", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) targetID(ref string) (string, error) {
	m, err := a.byRef(ref, "bkp_", "backup target", "listBackupTargets", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) channelID(ref string) (string, error) {
	m, err := a.byRef(ref, "chn_", "alert channel", "listAlertChannels", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) ruleID(ref string) (string, error) {
	m, err := a.byRef(ref, "alr_", "alert rule", "listAlertRules", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

func (a *app) gitConnID(ref string) (string, error) {
	m, err := a.byRef(ref, "git_", "Git connection", "listGitConnections", apiops.Input{}, "name")
	if err != nil {
		return "", err
	}
	return str(m, "id"), nil
}

// domainObj finds a domain by id or host. For a host it searches the service in --service, or all services of --env.
func (a *app) domainObj(ref, service string) (map[string]any, error) {
	if strings.HasPrefix(ref, "dom_") {
		return map[string]any{"id": ref}, nil
	}
	var svcIDs []string
	if service != "" {
		id, err := a.serviceID(service)
		if err != nil {
			return nil, err
		}
		svcIDs = []string{id}
	} else {
		eid, err := a.envID("")
		if err != nil {
			return nil, err
		}
		items, err := a.list("listServices", ids(eid))
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				svcIDs = append(svcIDs, str(m, "id"))
			}
		}
	}
	for _, sid := range svcIDs {
		items, err := a.list("listDomains", ids(sid))
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && str(m, "host") == ref {
				return m, nil
			}
		}
	}
	return nil, notFoundErr("domain %q not found", ref)
}

// volumeObj finds a volume by id or name in a service.
func (a *app) volumeObj(ref, service string) (map[string]any, error) {
	if strings.HasPrefix(ref, "vol_") {
		return map[string]any{"id": ref}, nil
	}
	if service == "" {
		return nil, usageErr("give the service with --service to find volume %q by name", ref)
	}
	sid, err := a.serviceID(service)
	if err != nil {
		return nil, err
	}
	items, err := a.list("listVolumes", ids(sid))
	if err != nil {
		return nil, err
	}
	return pick(items, ref, "volume", "name")
}

// scopeQuery gives the environment_id and service_id query values for variables and secrets.
func (a *app) scopeQuery(service string) (url.Values, error) {
	q := url.Values{}
	e, err := a.effective()
	if err != nil {
		return nil, err
	}
	if e.Env != "" {
		eid, err := a.envID(e.Env)
		if err != nil {
			return nil, err
		}
		q.Set("environment_id", eid)
	}
	if service != "" {
		sid, err := a.serviceID(service)
		if err != nil {
			return nil, err
		}
		q.Set("service_id", sid)
	}
	return q, nil
}
