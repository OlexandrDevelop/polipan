// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/polipan/polipan/server/internal/dbengine"
)

// Plan is what a template makes in one environment.
type Plan struct {
	Slug     string
	Version  string
	Inputs   map[string]any // inputs of the instance. A secret input is not here (REQ-TPL-002).
	Services []PlannedService
	Warnings []string
}

// PlannedService is one service to create.
type PlannedService struct {
	Name      string
	Type      string
	DBEngine  string
	DBVersion string
	Config    map[string]any
	Env       []dbengine.EnvEntry
	Volumes   []VolumeSpec
	Domains   []DomainSpec
	DependsOn []string
}

// VolumeSpec is a volume of a planned service.
type VolumeSpec struct {
	Name       string
	Kind       string
	MountPath  string
	AccessMode string
	SizeLimit  *int64
}

// DomainSpec is a domain of a planned service.
type DomainSpec struct {
	Host        string
	Port        int
	PathPrefix  string
	HTTPS       bool
	RedirectWWW string
}

// Options are the parts that Expand takes from the outside.
type Options struct {
	// ServiceIDs are the ids of the services to create, by name. They give the host names of ${{services.NAME.host}}.
	ServiceIDs map[string]string
	// NormalizeHost checks and normalizes a domain. Nil means a lower-case check only.
	NormalizeHost func(string) (string, error)
}

// ExpandError is an input or a value that does not fit (422 validation_failed).
type ExpandError struct{ Problems Problems }

func (e *ExpandError) Error() string { return e.Problems.Error() }

var refRe = regexp.MustCompile(`\$\{\{\s*(inputs|services)\.([A-Za-z0-9_-]+)(?:\.([A-Za-z_]+))?\s*\}\}`)

// Expand turns a manifest and the user inputs into a plan. It creates no row. Nothing is partly done.
func (m *Manifest) Expand(given map[string]any, opt Options) (*Plan, error) {
	var ps Problems
	add := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, a...)})
	}

	known := map[string]Input{}
	for _, in := range m.Inputs {
		known[in.Name] = in
	}
	for k := range given {
		if _, ok := known[k]; !ok {
			add("inputs."+k, "is not an input of this template")
		}
	}
	values := map[string]string{}
	secretInput := map[string]bool{}
	plan := &Plan{Slug: m.Slug, Version: m.Version, Inputs: map[string]any{}}
	for _, in := range m.Inputs {
		path := "inputs." + in.Name
		v, has := given[in.Name]
		if !has || v == nil || v == "" {
			v, has = in.Default, in.Default != nil
		}
		text, empty := "", !has
		if has {
			text = fmt.Sprint(v)
			if f, ok := v.(float64); ok && f == float64(int64(f)) {
				text = strconv.FormatInt(int64(f), 10)
			}
			empty = text == ""
		}
		if in.Type == "secret" {
			secretInput[in.Name] = true
			if empty && in.Generate {
				g, err := dbengine.GeneratePassword(dbengine.PasswordLength)
				if err != nil {
					return nil, err
				}
				text, empty = g, false
			}
		}
		if empty {
			if in.Required {
				add(path, "is required")
			}
			values[in.Name] = ""
			continue
		}
		switch in.Type {
		case "number":
			if _, err := strconv.ParseFloat(text, 64); err != nil {
				add(path, "must be a number")
			}
		case "bool":
			if text != "true" && text != "false" {
				add(path, "must be true or false")
			}
		case "domain":
			if opt.NormalizeHost != nil {
				h, err := opt.NormalizeHost(text)
				if err != nil {
					add(path, "%s", err.Error())
					continue
				}
				text = h
			} else {
				text = strings.ToLower(strings.TrimSpace(text))
			}
		}
		values[in.Name] = text
		if in.Type != "secret" {
			plan.Inputs[in.Name] = text
		}
	}
	if len(ps) > 0 {
		return nil, &ExpandError{ps}
	}

	// expand replaces ${{inputs.X}} and ${{services.NAME.host|port}}. It tells if a secret input was used.
	expand := func(path, s string) (string, bool) {
		usedSecret := false
		out := refRe.ReplaceAllStringFunc(s, func(match string) string {
			p := refRe.FindStringSubmatch(match)
			switch p[1] {
			case "inputs":
				if _, ok := known[p[2]]; !ok {
					add(path, "unknown input %s", p[2])
					return match
				}
				usedSecret = usedSecret || secretInput[p[2]]
				return values[p[2]]
			default:
				svc, ok := m.Services[p[2]]
				if !ok {
					add(path, "unknown service %s", p[2])
					return match
				}
				switch p[3] {
				case "host":
					if id := opt.ServiceIDs[p[2]]; id != "" {
						return dbengine.Alias(id)
					}
					add(path, "no id for service %s", p[2])
				case "port":
					if p := servicePort(svc); p != 0 {
						return strconv.Itoa(p)
					}
					add(path, "service %s has no port", p[2])
				default:
					add(path, "services.%s.%s is not available (use host or port)", p[2], p[3])
				}
				return match
			}
		})
		return out, usedSecret
	}

	for _, name := range m.order() {
		svc := m.Services[name]
		path := "services." + name
		ps2, err := m.planService(name, svc, path, expand, values, secretInput, add, func(w string) { plan.Warnings = append(plan.Warnings, w) })
		if err != nil {
			return nil, err
		}
		plan.Services = append(plan.Services, ps2)
	}
	if len(ps) > 0 {
		return nil, &ExpandError{ps}
	}
	return plan, nil
}

// order returns the service names: depends_on first, then by name.
func (m *Manifest) order() []string {
	var out []string
	done := map[string]bool{}
	var visit func(string, int)
	visit = func(n string, depth int) {
		if done[n] || depth > len(m.Services) {
			return
		}
		done[n] = true
		deps := append([]string{}, m.Services[n].DependsOn...)
		sort.Strings(deps)
		for _, d := range deps {
			visit(d, depth+1)
		}
		out = append(out, n)
	}
	for _, n := range sortedKeys(m.Services) {
		visit(n, 0)
	}
	return out
}

func servicePort(s ServiceEntry) int {
	if s.DB != nil {
		if e := dbengine.Get(s.DB.Engine); e != nil {
			return e.Port
		}
	}
	if len(s.Ports) > 0 {
		return s.Ports[0].Container
	}
	return 0
}

type expandFn func(path, s string) (string, bool)

func (m *Manifest) planService(name string, s ServiceEntry, path string, expand expandFn, values map[string]string, secretInput map[string]bool,
	add func(path, format string, a ...any), warn func(string)) (PlannedService, error) {
	out := PlannedService{Name: name, Type: s.Type, DependsOn: s.DependsOn}
	if out.Type == "" {
		out.Type = "app"
	}
	cfg := map[string]any{}
	if out.Type == "database" {
		eng := dbengine.Get(s.DB.Engine)
		if eng == nil {
			add(path+".db.engine", "unknown engine %s", s.DB.Engine)
			return out, nil
		}
		spec, err := eng.NewService(s.DB.Version, "")
		if err != nil {
			add(path+".db.version", "%s", err.Error())
			return out, nil
		}
		cfg, out.DBEngine, out.DBVersion = spec.Config, eng.Name, spec.Version
		out.Env = append(out.Env, spec.Env...)
		out.Volumes = append(out.Volumes, VolumeSpec{Name: "data", Kind: "volume", MountPath: spec.DataPath, AccessMode: "single_writer"})
		if s.DB.ExposeHostPort != 0 {
			cfg["host_port"] = s.DB.ExposeHostPort
		}
	} else {
		img, _ := expand(path+".source.image.name", s.Source.Image.Name)
		cfg["image"] = img
		cfg["restart_policy"] = "always"
	}
	if len(s.Command) > 0 {
		cfg["command"] = strs(s.Command)
	}
	if len(s.Entrypoint) > 0 {
		cfg["entrypoint"] = strs(s.Entrypoint)
	}
	if s.Restart != "" {
		cfg["restart_policy"] = s.Restart
	}
	if len(s.Ports) > 0 {
		cfg["port"] = s.Ports[0].Container
		if s.Ports[0].Host != 0 {
			cfg["host_port"] = s.Ports[0].Host
		}
		if len(s.Ports) > 1 {
			warn(path + ".ports: only the first port is used, a service has one port in this version")
		}
	}
	if s.StopGrace != "" {
		secs, err := Seconds(s.StopGrace)
		if err != nil {
			add(path+".stop_grace_period", "%s", err.Error())
		}
		cfg["stop_grace_period"] = secs
	}
	if len(s.Files) > 0 {
		var files []any
		for i, f := range s.Files {
			fpath := fmt.Sprintf("%s.files[%d]", path, i)
			content, usedSecret := expand(fpath, f.Content)
			if usedSecret {
				// A file is stored in the plain config. A secret input must not go there (REQ-TPL-002).
				add(fpath, "a secret input cannot be used in the content of a file: use it in env")
			}
			files = append(files, map[string]any{"path": f.Path, "content": content})
		}
		cfg["files"] = files
	}
	if s.Healthcheck != nil {
		h, err := healthConfig(s.Healthcheck)
		if err != nil {
			add(path+".healthcheck", "%s", err.Error())
		} else {
			cfg["health"] = h
		}
	}
	if s.Resources != nil {
		r, err := resourcesConfig(s.Resources)
		if err != nil {
			add(path+".resources", "%s", err.Error())
		} else if len(r) > 0 {
			cfg["resources"] = r
		}
	}
	if s.Deploy != nil {
		if err := deployConfig(cfg, s.Deploy); err != nil {
			add(path+".deploy", "%s", err.Error())
		}
	}
	if out.Type == "database" {
		cfg["strategy"] = "recreate" // forced (REQ-DB-002)
	}

	for _, key := range sortedKeys(s.Env) {
		v := fmt.Sprint(s.Env[key])
		if obj, ok := s.Env[key].(map[string]any); ok {
			v = fmt.Sprint(obj["value"])
		}
		text, secret := expand(path+".env."+key, v)
		out.Env = appendEnv(out.Env, key, text, secret)
	}
	for _, sec := range s.Secrets {
		in := strings.ToLower(sec)
		if !secretInput[in] {
			add(path+".secrets", "secret %s needs a secret input named %s", sec, in)
			continue
		}
		out.Env = appendEnv(out.Env, sec, values[in], true)
	}
	for i, v := range s.Volumes {
		kind := v.Kind
		if kind == "" {
			kind = "volume"
		}
		mode := v.AccessMode
		if mode == "" {
			mode = "single_writer"
		}
		spec := VolumeSpec{Name: v.Name, Kind: kind, MountPath: v.Path, AccessMode: mode}
		if v.SizeLimit != "" {
			n, err := Size(v.SizeLimit)
			if err != nil {
				add(fmt.Sprintf("%s.volumes[%d].size_limit", path, i), "%s", err.Error())
			}
			spec.SizeLimit = &n
		}
		out.Volumes = append(out.Volumes, spec)
	}
	for i, d := range s.Domains {
		host, _ := expand(fmt.Sprintf("%s.domains[%d].host", path, i), d.Host)
		if host == "" {
			continue // an optional domain input that was left empty: no domain
		}
		spec := DomainSpec{Host: host, Port: d.Port, PathPrefix: d.PathPrefix, HTTPS: d.HTTPS == nil || *d.HTTPS, RedirectWWW: d.RedirectWWW}
		if spec.PathPrefix == "" {
			spec.PathPrefix = "/"
		}
		if spec.Port == 0 {
			spec.Port = servicePort(s)
		}
		if spec.Port == 0 {
			add(fmt.Sprintf("%s.domains[%d].port", path, i), "is required when the service has no port")
		}
		out.Domains = append(out.Domains, spec)
	}
	out.Config = cfg
	return out, nil
}

// appendEnv adds a variable, or replaces the one with the same name.
func appendEnv(list []dbengine.EnvEntry, name, value string, secret bool) []dbengine.EnvEntry {
	for i := range list {
		if list[i].Name == name {
			list[i] = dbengine.EnvEntry{Name: name, Value: value, Secret: secret}
			return list
		}
	}
	return append(list, dbengine.EnvEntry{Name: name, Value: value, Secret: secret})
}

func strs(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
