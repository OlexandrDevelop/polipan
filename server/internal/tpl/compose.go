// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ComposeResult is a Compose file converted to services (REQ-TPL-006).
type ComposeResult struct {
	Manifest *Manifest // a manifest without inputs. Expand it to get the plan.
	Warnings []string
}

// supported keys of a Compose service, and the ones that are dropped without a warning.
var (
	composeKeys    = map[string]bool{"image": true, "command": true, "entrypoint": true, "environment": true, "ports": true, "volumes": true, "depends_on": true, "healthcheck": true, "restart": true, "deploy": true}
	composeQuiet   = map[string]bool{"container_name": true, "networks": true, "expose": true, "labels": true, "hostname": true, "platform": true, "stdin_open": true, "tty": true, "logging": true}
	composeTopKeep = map[string]bool{"services": true, "version": true, "name": true, "volumes": true, "networks": true, "configs": true, "secrets": true}
)

var composeName = regexp.MustCompile(`[^a-z0-9-]+`)

// ImportCompose converts a Compose file. A file with unsafe settings gives *UnsafeError. A build:
// key or a service without an image gives Problems. Keys that are not supported are warnings.
func ImportCompose(data []byte) (*ComposeResult, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{{Message: "the Compose file is not valid YAML: " + err.Error()}}
	}
	services, _ := doc["services"].(map[string]any)
	if len(services) == 0 {
		return nil, Problems{{Path: "services", Message: "the Compose file has no services"}}
	}
	if v := SafetyOfCompose(doc); len(v) > 0 {
		return nil, &UnsafeError{Violations: v}
	}
	res := &ComposeResult{Manifest: &Manifest{Slug: "compose-import", Version: "1.0.0", Name: "Compose import", Services: map[string]ServiceEntry{}}}
	var ps Problems
	for k := range doc {
		if !composeTopKeep[k] && !strings.HasPrefix(k, "x-") {
			res.Warnings = append(res.Warnings, fmt.Sprintf("top-level key %q is ignored", k))
		}
	}
	names := map[string]string{} // compose name -> service name
	for _, raw := range sortedKeys(services) {
		n := strings.Trim(composeName.ReplaceAllString(strings.ToLower(raw), "-"), "-")
		if n == "" || len(n) > 40 {
			ps = append(ps, Problem{Path: "services." + raw, Message: "the service name cannot be made a valid name (a-z, 0-9, -, up to 40 characters)"})
			continue
		}
		names[raw] = n
	}
	for _, raw := range sortedKeys(services) {
		svc, _ := services[raw].(map[string]any)
		path := "services." + raw
		if _, ok := svc["build"]; ok {
			ps = append(ps, Problem{Path: path + ".build", Message: "build: is not supported. Create a Git service for this code instead"})
			continue
		}
		name := names[raw]
		if name == "" {
			continue
		}
		entry, warns, perr := convertComposeService(path, name, svc, names)
		ps = append(ps, perr...)
		for _, w := range warns {
			res.Warnings = append(res.Warnings, w)
		}
		for k := range svc {
			if !composeKeys[k] && !composeQuiet[k] && k != "build" && k != "privileged" && k != "cap_add" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s.%s is not supported and is ignored", path, k))
			}
		}
		res.Manifest.Services[name] = entry
	}
	if len(ps) > 0 {
		return nil, ps
	}
	return res, nil
}

func convertComposeService(path, name string, svc map[string]any, names map[string]string) (ServiceEntry, []string, Problems) {
	var warns []string
	var ps Problems
	e := ServiceEntry{Type: "app"}
	img, _ := svc["image"].(string)
	if img == "" {
		ps = append(ps, Problem{Path: path + ".image", Message: "is required (build: is not supported)"})
	}
	e.Source = &Source{Image: &struct {
		Name string `yaml:"name" json:"name"`
	}{Name: img}}
	e.Command = composeCommand(svc["command"])
	e.Entrypoint = composeCommand(svc["entrypoint"])
	switch env := svc["environment"].(type) {
	case map[string]any:
		e.Env = map[string]any{}
		for k, v := range env {
			if v == nil {
				v = ""
			}
			e.Env[k] = fmt.Sprint(v)
		}
	case []any:
		e.Env = map[string]any{}
		for _, item := range env {
			k, v, _ := strings.Cut(fmt.Sprint(item), "=")
			e.Env[k] = v
		}
	}
	for i, p := range asList(svc["ports"]) {
		port, host, ok := composePort(p)
		if !ok {
			ps = append(ps, Problem{Path: fmt.Sprintf("%s.ports[%d]", path, i), Message: fmt.Sprintf("%v is not a valid port", p)})
			continue
		}
		if host != 0 {
			warns = append(warns, fmt.Sprintf("%s.ports: the host port %d is ignored. Use a domain, or set a host port on the service", path, host))
		}
		e.Ports = append(e.Ports, Port{Container: port})
	}
	for i, v := range asList(svc["volumes"]) {
		vol, ok, warn := composeVolume(name, v)
		if warn != "" {
			warns = append(warns, fmt.Sprintf("%s.volumes[%d]: %s", path, i, warn))
		}
		if ok {
			e.Volumes = append(e.Volumes, vol)
		}
	}
	switch d := svc["depends_on"].(type) {
	case []any:
		for _, x := range d {
			if n := names[fmt.Sprint(x)]; n != "" {
				e.DependsOn = append(e.DependsOn, n)
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(d) {
			if n := names[k]; n != "" {
				e.DependsOn = append(e.DependsOn, n)
			}
		}
	}
	if r, _ := svc["restart"].(string); r != "" {
		switch r {
		case "always", "unless-stopped":
			e.Restart = "always"
		case "on-failure":
			e.Restart = "on-failure"
		case "no":
			e.Restart = "no"
		default:
			if strings.HasPrefix(r, "on-failure") {
				e.Restart = "on-failure"
			} else {
				warns = append(warns, fmt.Sprintf("%s.restart %q is ignored", path, r))
			}
		}
	}
	if hc, ok := svc["healthcheck"].(map[string]any); ok {
		h, w := composeHealth(hc)
		e.Healthcheck = h
		if w != "" {
			warns = append(warns, path+".healthcheck: "+w)
		}
	}
	if d, ok := svc["deploy"].(map[string]any); ok {
		for k := range d {
			if k != "resources" {
				warns = append(warns, fmt.Sprintf("%s.deploy.%s is ignored", path, k))
			}
		}
		if res, ok := d["resources"].(map[string]any); ok {
			if lim, ok := res["limits"].(map[string]any); ok {
				r := map[string]any{}
				if c, ok := lim["cpus"]; ok {
					r["cpu"] = fmt.Sprint(c)
				}
				if m, ok := lim["memory"]; ok {
					if b, err := composeBytes(fmt.Sprint(m)); err == nil {
						r["memory"] = fmt.Sprintf("%dKi", b/1024)
					} else {
						warns = append(warns, fmt.Sprintf("%s.deploy.resources.limits.memory: %v", path, err))
					}
				}
				if len(r) > 0 {
					e.Resources = r
				}
			}
			if _, ok := res["reservations"]; ok {
				warns = append(warns, path+".deploy.resources.reservations is ignored")
			}
		}
	}
	return e, warns, ps
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// composeCommand reads a command: a list stays a list, a string is run by a shell.
func composeCommand(v any) []string {
	switch c := v.(type) {
	case []any:
		out := make([]string, len(c))
		for i, x := range c {
			out[i] = fmt.Sprint(x)
		}
		return out
	case string:
		if c != "" {
			return []string{"sh", "-c", c}
		}
	}
	return nil
}

// composePort reads "8080:80", "80", "127.0.0.1:8080:80/tcp" or {target, published}.
func composePort(v any) (container, host int, ok bool) {
	switch p := v.(type) {
	case int:
		return p, 0, p > 0 && p < 65536
	case map[string]any:
		t, _ := strconv.Atoi(fmt.Sprint(p["target"]))
		h, _ := strconv.Atoi(fmt.Sprint(p["published"]))
		return t, h, t > 0 && t < 65536
	case string:
		s, _, _ := strings.Cut(p, "/")
		parts := strings.Split(s, ":")
		last := parts[len(parts)-1]
		c, err := strconv.Atoi(last)
		if err != nil || c < 1 || c > 65535 {
			return 0, 0, false
		}
		if len(parts) >= 2 {
			h, _ := strconv.Atoi(parts[len(parts)-2])
			return c, h, true
		}
		return c, 0, true
	}
	return 0, 0, false
}

// composeVolume converts a named volume. A bind mount inside the binds folder of the service becomes a bind volume.
func composeVolume(service string, v any) (Volume, bool, string) {
	var source, target string
	switch x := v.(type) {
	case string:
		parts := strings.Split(x, ":")
		if len(parts) < 2 {
			return Volume{}, false, "an anonymous volume is ignored"
		}
		source, target = parts[0], parts[1]
	case map[string]any:
		source, target = fmt.Sprint(x["source"]), fmt.Sprint(x["target"])
		if t := fmt.Sprint(x["type"]); t == "tmpfs" {
			return Volume{Name: composeName.ReplaceAllString(strings.ToLower(path.Base(target)), "-"), Path: target, Kind: "tmpfs"}, true, ""
		}
	}
	if target == "" || !strings.HasPrefix(target, "/") {
		return Volume{}, false, "the target path is not valid"
	}
	if strings.HasPrefix(source, "/") {
		root := BindsRoot + "/" + service + "/"
		return Volume{Name: strings.TrimPrefix(path.Clean(source), root), Path: target, Kind: "bind"}, true, ""
	}
	name := strings.Trim(composeName.ReplaceAllString(strings.ToLower(source), "-"), "-")
	if name == "" {
		return Volume{}, false, "the volume name is not valid"
	}
	return Volume{Name: name, Path: target}, true, ""
}

var composeDur = regexp.MustCompile(`^[0-9]+(ms|s|m|h)`)

func composeSeconds(v any) (int, bool) {
	s := fmt.Sprint(v)
	if s == "" || !composeDur.MatchString(s) {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, true
}

func composeHealth(hc map[string]any) (map[string]any, string) {
	var cmd []string
	switch t := hc["test"].(type) {
	case string:
		cmd = []string{"sh", "-c", t}
	case []any:
		for _, x := range t {
			cmd = append(cmd, fmt.Sprint(x))
		}
		switch {
		case len(cmd) > 0 && cmd[0] == "NONE":
			return nil, "disabled"
		case len(cmd) > 0 && cmd[0] == "CMD-SHELL":
			cmd = []string{"sh", "-c", strings.Join(cmd[1:], " ")}
		case len(cmd) > 0 && cmd[0] == "CMD":
			cmd = cmd[1:]
		}
	}
	if len(cmd) == 0 {
		return nil, "no test command, ignored"
	}
	out := map[string]any{"type": "command", "command": stringsToAny(cmd)}
	for k, to := range map[string]string{"interval": "interval", "timeout": "timeout", "start_period": "start_period"} {
		if v, ok := hc[k]; ok {
			if s, ok := composeSeconds(v); ok {
				out[to] = fmt.Sprintf("%ds", s)
			}
		}
	}
	if r, ok := hc["retries"]; ok {
		if n, _ := strconv.Atoi(fmt.Sprint(r)); n > 0 {
			out["unhealthy_threshold"] = n
		}
	}
	return out, ""
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

var bytesRe = regexp.MustCompile(`^([0-9]+)\s*([kKmMgG]?)[bB]?$`)

// composeBytes reads 512m, 1g, 64M, 1024.
func composeBytes(s string) (int64, error) {
	m := bytesRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q is not a size like 512m or 1g", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch strings.ToLower(m[2]) {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	}
	return n, nil
}

// MarshalYAML writes the manifest of an import, for dry runs and for saving it as a template.
func (m *Manifest) MarshalYAML() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	type plain Manifest
	if err := enc.Encode((*plain)(m)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
