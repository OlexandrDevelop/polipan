// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// kv is an ordered map, so that the exported file reads well: version and project first.
type kv struct {
	k string
	v any
}

type omap []kv

func (m *omap) set(k string, v any) { *m = append(*m, kv{k, v}) }

func (m omap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range m {
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.k}
		val := &yaml.Node{}
		if err := val.Encode(e.v); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, key, val)
	}
	return n, nil
}

func num(v any) (int64, bool) {
	switch t := norm(v).(type) {
	case float64:
		return int64(t), true
	}
	return 0, false
}

func seconds(v any) string {
	n, ok := num(v)
	if !ok {
		return ""
	}
	switch {
	case n%86400 == 0 && n > 0:
		return fmt.Sprintf("%dd", n/86400)
	case n%3600 == 0 && n > 0:
		return fmt.Sprintf("%dh", n/3600)
	case n%60 == 0 && n > 0:
		return fmt.Sprintf("%dm", n/60)
	}
	return fmt.Sprintf("%ds", n)
}

func memory(n int64) string {
	switch {
	case n%(1<<30) == 0:
		return fmt.Sprintf("%dGi", n>>30)
	case n%(1<<20) == 0:
		return fmt.Sprintf("%dMi", n>>20)
	case n%(1<<10) == 0:
		return fmt.Sprintf("%dKi", n>>10)
	}
	return fmt.Sprint(n)
}

// exportService turns a live service into the shape of the file (the reverse of desireService).
func exportService(ls *LiveService, secretNames []SecretRef, projectSlug string) omap {
	var out omap
	cfg := norm(ls.Config).(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	extra, _ := cfg["extra"].(map[string]any)
	if ls.Type == "database" {
		out.set("type", "database")
		db := omap{}
		db.set("engine", ls.DBEngine)
		out.set("db", db)
	}
	if g, ok := cfg["git"].(map[string]any); ok {
		src := omap{}
		git := omap{}
		for _, k := range []string{"repo", "branch", "path", "builder", "dockerfile"} {
			if v, ok := g[k]; ok {
				git.set(k, v)
			}
		}
		src.set("git", git)
		out.set("source", src)
		if v, ok := g["auto_deploy"]; ok {
			out.set("auto_deploy", v)
		}
	} else if img, ok := cfg["image"].(string); ok && ls.Type != "database" {
		out.set("source", omap{{"image", omap{{"name", img}}}})
	}
	if p, ok := cfg["port"]; ok {
		ports := []any{p}
		if more, ok := extra["ports"].([]any); ok && len(more) > 1 {
			ports = nil
			for _, x := range more {
				if m, ok := x.(map[string]any); ok {
					ports = append(ports, m["container"])
				}
			}
		}
		out.set("ports", ports)
	}
	for _, k := range []string{"command", "entrypoint", "restart_policy"} {
		if v, ok := cfg[k]; ok {
			out.set(k, v)
		}
	}
	if v, ok := cfg["stop_grace_period"]; ok {
		out.set("stop_grace_period", seconds(v))
	}
	if res, ok := cfg["resources"].(map[string]any); ok {
		r := omap{}
		if n, ok := num(res["cpu_millis"]); ok {
			r.set("cpu", fmt.Sprintf("%dm", n))
		}
		if n, ok := num(res["memory_bytes"]); ok {
			r.set("memory", memory(n))
		}
		if len(r) > 0 {
			out.set("resources", r)
		}
	}
	if h, ok := cfg["health"].(map[string]any); ok {
		hc := omap{}
		for _, k := range []string{"type", "path", "port"} {
			if v, ok := h[k]; ok {
				hc.set(k, v)
			}
		}
		if v, ok := h["host"]; ok {
			hc.set("host_header", v)
		}
		if v, ok := h["command"]; ok {
			hc.set("command", v)
		}
		for _, k := range []string{"interval", "timeout", "start_period", "deploy_timeout"} {
			if v, ok := h[k]; ok {
				hc.set(k, seconds(v))
			}
		}
		for _, k := range []string{"healthy_threshold", "unhealthy_threshold", "restart_on_unhealthy"} {
			if v, ok := h[k]; ok {
				hc.set(k, v)
			}
		}
		out.set("healthcheck", hc)
	}
	deploy := omap{}
	if v, ok := cfg["strategy"]; ok && ls.Type != "database" {
		deploy.set("strategy", v)
	}
	if o, ok := cfg["strategy_options"].(map[string]any); ok {
		for _, k := range []string{"drain_seconds", "allow_overlap", "allow_downtime_on_low_capacity"} {
			if v, ok := o[k]; ok {
				deploy.set(k, v)
			}
		}
	}
	for _, k := range []string{"retain_stopped", "pre_deploy", "post_deploy"} {
		if v, ok := cfg[k]; ok {
			deploy.set(k, v)
		}
	}
	if v, ok := extra["canary"]; ok {
		deploy.set("canary", v)
	}
	if len(deploy) > 0 {
		out.set("deploy", deploy)
	}
	for _, k := range []string{"workdir", "replicas", "placement", "depends_on", "cron"} {
		if v, ok := extra[k]; ok {
			out.set(k, v)
		}
	}
	if ls.Failover != "" && ls.Failover != "none" {
		out.set("failover", ls.Failover)
	}
	if ls.AllowDataLoss {
		out.set("allow_data_loss_failover", true)
	}
	// Plain variables of the service.
	if len(ls.Vars) > 0 {
		env := omap{}
		for _, name := range sortedKeys(ls.Vars) {
			v := ls.Vars[name]
			if v.Preview || v.BuildTime {
				o := omap{{"value", v.Value}}
				if v.Preview {
					o.set("preview", true)
				}
				if v.BuildTime {
					o.set("build_time", true)
				}
				env.set(name, o)
				continue
			}
			env.set(name, v.Value)
		}
		out.set("env", env)
	}
	if len(secretNames) > 0 {
		out.set("secrets", secretList(secretNames))
	}
	if len(ls.Domains) > 0 {
		var list []any
		for _, d := range ls.Domains {
			o := omap{{"host", d.Host}}
			if d.Port != 0 {
				o.set("port", d.Port)
			}
			if d.PathPrefix != "" && d.PathPrefix != "/" {
				o.set("path_prefix", d.PathPrefix)
			}
			if !d.HTTPS {
				o.set("https", false)
			}
			if d.RedirectWWW != "" {
				o.set("redirect_www", d.RedirectWWW)
			}
			if n, ok := num(d.Options["max_body_bytes"]); ok {
				o.set("max_body", memory(n))
			}
			if v, ok := d.Options["ip_allow"]; ok {
				o.set("ip_allow", v)
			}
			if v, ok := d.Options["basic_auth_secret"]; ok {
				o.set("basic_auth_secret", v)
			}
			list = append(list, o)
		}
		out.set("domains", list)
	}
	if ls.Type != "database" && len(ls.Volumes) > 0 {
		var list []any
		for _, v := range ls.Volumes {
			o := omap{{"name", v.Name}, {"path", v.Path}}
			if v.Kind != "" && v.Kind != "volume" {
				o.set("kind", v.Kind)
			}
			if v.AccessMode != "" && v.AccessMode != "single_writer" {
				o.set("access_mode", v.AccessMode)
			}
			if v.SizeLimit > 0 {
				o.set("size_limit", memory(v.SizeLimit))
			}
			list = append(list, o)
		}
		out.set("volumes", list)
	}
	_ = projectSlug
	return out
}

func secretList(refs []SecretRef) []any {
	var out []any
	for _, r := range refs {
		if !r.Preview && !r.BuildTime && r.File == "" {
			out = append(out, r.Name)
			continue
		}
		o := omap{{"name", r.Name}}
		if r.Preview {
			o.set("preview", true)
		}
		if r.BuildTime {
			o.set("build_time", true)
		}
		if r.File != "" {
			o.set("file", r.File)
		}
		out = append(out, o)
	}
	return out
}

// Export makes a polipan.yaml from the live state of a project (REQ-CAC-009). Secrets are names only: the value of a
// secret is never read. A service that exists in more than one environment is written once under services, with
// enabled: false where it is missing. Differences between environments are not written (07 section 3).
func Export(live *Live) ([]byte, error) {
	var root omap
	root.set("version", 1)
	root.set("project", live.Slug)

	// Environments, preview environments excluded.
	var slugs []string
	for slug, e := range live.Envs {
		if e.Kind != "preview" {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	envs := omap{}
	for _, slug := range slugs {
		e := live.Envs[slug]
		o := omap{{"kind", e.Kind}, {"protected", e.Protected}}
		if v, ok := e.Settings["auto_deploy"].(bool); ok {
			o.set("auto_deploy", v)
		}
		if len(e.Vars) > 0 {
			o.set("variables", varMap(e.Vars))
		}
		if names := scopeSecrets(live, e.ID, ""); len(names) > 0 {
			o.set("secrets", secretList(names))
		}
		envs.set(slug, o)
	}
	// Services: the first environment that has one, production first.
	order := append([]string{}, slugs...)
	sort.SliceStable(order, func(i, j int) bool {
		return live.Envs[order[i]].Kind == "production" && live.Envs[order[j]].Kind != "production"
	})
	exported := map[string]omap{}
	var names []string
	for _, slug := range order {
		e := live.Envs[slug]
		for _, name := range sortedKeys(e.Services) {
			if _, ok := exported[name]; ok {
				continue
			}
			ls := e.Services[name]
			exported[name] = exportService(ls, scopeSecrets(live, e.ID, ls.ID), live.Slug)
			names = append(names, name)
		}
	}
	sort.Strings(names)
	// Environments that lack a service turn it off.
	for _, slug := range slugs {
		var off omap
		for _, name := range names {
			if live.Envs[slug].Services[name] == nil {
				off.set(name, omap{{"enabled", false}})
			}
		}
		if len(off) == 0 {
			continue
		}
		for i := range envs {
			if envs[i].k == slug {
				o := envs[i].v.(omap)
				o.set("services", off)
				envs[i].v = o
			}
		}
	}
	if len(envs) > 0 {
		root.set("environments", envs)
	}
	svcs := omap{}
	for _, name := range names {
		svcs.set(name, exported[name])
	}
	root.set("services", svcs)
	if len(live.ProjectVars) > 0 {
		root.set("variables", varMap(live.ProjectVars))
	}
	if names := scopeSecrets(live, "", ""); len(names) > 0 {
		root.set("secrets", secretList(names))
	}
	if pv, ok := live.Settings["previews"].(map[string]any); ok && len(pv) > 0 {
		root.set("previews", exportPreviews(live, pv))
	}
	raw, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Exported by Polipan. Secrets are names only: set the values with the UI, polipan secret set or Terraform.\n"), raw...), nil
}

func varMap(vars map[string]LiveVar) omap {
	var out omap
	for _, k := range sortedKeys(vars) {
		out.set(k, vars[k].Value)
	}
	return out
}

// scopeSecrets lists the names of the secrets of one scope. envID and serviceID empty: the project scope.
func scopeSecrets(live *Live, envID, serviceID string) []SecretRef {
	var out []SecretRef
	for _, s := range live.Secrets {
		if s.EnvID == envID && s.ServiceID == serviceID {
			out = append(out, SecretRef{Name: s.Name, Preview: s.Preview, BuildTime: s.BuildTime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func exportPreviews(live *Live, pv map[string]any) omap {
	var out omap
	for _, k := range []string{"enabled", "max_active", "from_forks", "node_selector", "strategy", "seed_command"} {
		if v, ok := pv[k]; ok {
			out.set(k, v)
		}
	}
	if id, ok := pv["base_environment_id"].(string); ok {
		for slug, e := range live.Envs {
			if e.ID == id {
				out.set("base_environment", slug)
			}
		}
	}
	if d, ok := num(pv["ttl_days"]); ok {
		out.set("ttl", fmt.Sprintf("%dd", d))
	}
	return out
}
