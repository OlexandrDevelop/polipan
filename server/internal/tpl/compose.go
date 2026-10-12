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
	Manifest *Manifest // a manifest. Expand it with Inputs to get the plan.
	Warnings []string
	// Losses are the parts of the file that are not imported as they are, each with its replacement.
	Losses []Loss
	// Inputs are the values of the secret inputs of Manifest: the variables that look like secrets. They are
	// stored as secrets and are never in a message.
	Inputs map[string]any
	// ReplacementYAML is the import as a polipan.yaml, with the replacements of the dropped parts. No secret value is in it.
	ReplacementYAML string
}

// supported keys of a Compose service, and the ones that are dropped without a warning.
var (
	composeKeys    = map[string]bool{"image": true, "command": true, "entrypoint": true, "environment": true, "ports": true, "volumes": true, "depends_on": true, "healthcheck": true, "restart": true, "deploy": true}
	composeQuiet   = map[string]bool{"container_name": true, "networks": true, "expose": true, "labels": true, "hostname": true, "platform": true, "stdin_open": true, "tty": true, "logging": true}
	composeTopKeep = map[string]bool{"services": true, "version": true, "name": true, "volumes": true, "networks": true, "configs": true, "secrets": true}
)

var composeName = regexp.MustCompile(`[^a-z0-9-]+`)

// hostPathRe finds the host path in the message of a dropped bind mount, so that a warning does not show it.
var hostPathRe = regexp.MustCompile(`bind mount [/~]\S*`)

// ComposeRepo tells where a Compose file was read from. A service with build: is then built from the same
// repository, from the folder that build.context names.
type ComposeRepo struct {
	URL          string // the repository, a full URL, or owner/name with a Git connection
	Branch       string
	ConnectionID string
	Dir          string // the folder of the Compose file inside the repository ("" is the root)
}

// ComposeOptions are the extra settings of an import.
type ComposeOptions struct {
	// Repo is set when the file comes from a Git repository.
	Repo *ComposeRepo
	// SkipUnsafe drops what is not allowed (a service that mounts the Docker socket or is privileged, a bind
	// mount of a folder of the host) and says it in a warning. Without it the whole file is refused.
	SkipUnsafe bool
	// Vars are the values of ${NAME} in the file. A name that is not here is empty, with a warning.
	Vars map[string]string
	// EnvFiles are the env files of the services read from the repository, by path inside it (ComposeEnvFiles).
	// A file that is not here is not in the repository.
	EnvFiles map[string][]byte
	// Project is the project slug for the replacement polipan.yaml.
	Project string
}

// ImportCompose converts a Compose file. A file with unsafe settings gives *UnsafeError. A build:
// key or a service without an image gives Problems. Keys that are not supported are warnings.
func ImportCompose(data []byte) (*ComposeResult, error) {
	return ImportComposeWith(data, ComposeOptions{})
}

var composeVar = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?-)([^}]*))?\}`)

// composeInterpolate fills in ${NAME}, ${NAME:-default} and ${NAME-default}. $$ is a dollar sign.
// It returns the names that had no value and no default.
func composeInterpolate(text string, vars map[string]string) (string, []string) {
	unset := map[string]bool{}
	out := composeVar.ReplaceAllStringFunc(text, func(m string) string {
		if m == "$$" {
			return "$"
		}
		g := composeVar.FindStringSubmatch(m)
		name, op, def := g[1], g[2], g[3]
		if v, ok := vars[name]; ok && (v != "" || op != ":-") {
			return v
		}
		if op != "" {
			return def
		}
		unset[name] = true
		return ""
	})
	return out, sortedKeys(anyBoolMap(unset))
}

func anyBoolMap(in map[string]bool) map[string]any {
	out := make(map[string]any, len(in))
	for k := range in {
		out[k] = true
	}
	return out
}

// ImportComposeWith is ImportCompose with options (a file from a Git repository, skipping what is unsafe).
func ImportComposeWith(data []byte, opts ComposeOptions) (*ComposeResult, error) {
	text, unset := composeInterpolate(string(data), opts.Vars)
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, Problems{{Message: "the Compose file is not valid YAML: " + err.Error()}}
	}
	services, _ := doc["services"].(map[string]any)
	if len(services) == 0 {
		return nil, Problems{{Path: "services", Message: "the Compose file has no services"}}
	}
	res := &ComposeResult{Manifest: &Manifest{Slug: "compose-import", Version: "1.0.0", Name: "Compose import", Services: map[string]ServiceEntry{}}}
	if len(unset) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("these variables of the file have no value and are empty: %s. Set them in the variables of the service", strings.Join(unset, ", ")))
	}
	extras := map[string]*composeExtra{} // by compose name
	if opts.SkipUnsafe {
		dropUnsafe(doc, res, extras)
		services, _ = doc["services"].(map[string]any)
		if len(services) == 0 {
			return nil, Problems{{Path: "services", Message: "no service is left after the unsafe ones were skipped"}}
		}
	}
	if v := SafetyOfCompose(doc); len(v) > 0 {
		return nil, &UnsafeError{Violations: v}
	}
	var ps Problems
	for k := range doc {
		if !composeTopKeep[k] && !strings.HasPrefix(k, "x-") {
			res.Warnings = append(res.Warnings, fmt.Sprintf("top-level key %q is ignored", k))
			res.Losses = append(res.Losses, Loss{Path: k, Key: k, Disposition: LossIgnored, Lost: "the top-level setting " + k,
				Replacement: "Polipan has no setting for " + k + ". Remove it, or set what it does on each service."})
		}
	}
	res.Losses = append(res.Losses, topLevelLosses(doc)...)
	names := map[string]string{}        // compose name -> service name
	secretVars := map[string][]string{} // service name -> the names of its secret variables
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
		var git *GitSource
		if b, ok := svc["build"]; ok {
			if opts.Repo == nil {
				img, _ := svc["image"].(string)
				loss := buildLoss(path+".build", b, img)
				res.Losses = append(res.Losses, loss)
				if img == "" {
					ps = append(ps, Problem{Path: path + ".build", Message: "build: is not supported. Import the file from a Git repository, add image:, or create a Git service for this code (path " + buildPath(b) + ", builder dockerfile)"})
					continue
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s.build is not supported: the image %s is pulled instead", path, img))
			} else {
				g, perr := composeBuild(path+".build", b, opts.Repo)
				if len(perr) > 0 {
					ps = append(ps, perr...)
					continue
				}
				git = g
				for _, k := range sortedKeys(g.BuildArgs) {
					if IsSecretName(k) {
						res.Losses = append(res.Losses, buildArgLoss(path+".build.args."+k, k))
					}
				}
			}
		}
		name := names[raw]
		if name == "" {
			continue
		}
		entry, warns, losses, perr := convertComposeService(path, name, svc, names, git)
		ps = append(ps, perr...)
		res.Warnings = append(res.Warnings, warns...)
		res.Losses = append(res.Losses, losses...)
		for _, k := range sortedKeys(svc) {
			switch {
			case k == "env_file":
				if entry.Env == nil {
					entry.Env = map[string]any{}
				}
				res.Losses = append(res.Losses, envFileLoss(path+".env_file", envFilesOf(svc[k], repoDir(opts.Repo)), opts.EnvFiles, opts.Repo != nil, entry.Env)...)
				if len(entry.Env) == 0 {
					entry.Env = nil
				}
			case !composeKeys[k] && !composeQuiet[k] && k != "build" && k != "privileged" && k != "cap_add":
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s.%s is not supported and is ignored", path, k))
				res.Losses = append(res.Losses, keyLoss(path, k))
			case composeQuiet[k]:
				res.Losses = append(res.Losses, keyLoss(path, k))
			}
		}
		if names := secretEnv(name, &entry, res); len(names) > 0 {
			secretVars[name] = names
		}
		if git != nil && len(entry.Ports) == 0 && svc["restart"] == nil && svc["entrypoint"] != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s looks like a command that runs once (it has an entrypoint, no port and no restart). Delete the service if it should not run all the time", path))
		}
		res.Manifest.Services[name] = entry
	}
	if len(ps) > 0 {
		return nil, ps
	}
	byName := map[string]*composeExtra{}
	for raw, x := range extras {
		if n := names[raw]; n != "" {
			byName[n] = x
		}
	}
	y, err := replacementYAML(res.Manifest, secretVars, byName, opts.Project)
	if err != nil {
		return nil, err
	}
	res.ReplacementYAML = y
	sortLosses(res.Losses)
	return res, nil
}

func repoDir(r *ComposeRepo) string {
	if r == nil {
		return ""
	}
	return r.Dir
}

// secretEnv keeps the variables that look like secrets (IsSecretName) as secret inputs of the import: the
// variable reads the input, and the value goes to res.Inputs. It returns the names of these variables.
func secretEnv(service string, e *ServiceEntry, res *ComposeResult) []string {
	var out []string
	for _, k := range sortedKeys(e.Env) {
		v := fmt.Sprint(e.Env[k])
		if v == "" || !IsSecretName(k) {
			continue
		}
		// The number keeps the name unique: API_KEY and api_key are two variables, and the name is lower case.
		in := fmt.Sprintf("s%d_%s__%s", len(res.Manifest.Inputs)+1, strings.ReplaceAll(service, "-", "_"), strings.ToLower(k))
		res.Manifest.Inputs = append(res.Manifest.Inputs, Input{Name: in, Type: "secret"})
		if res.Inputs == nil {
			res.Inputs = map[string]any{}
		}
		res.Inputs[in] = v
		e.Env[k] = "${{inputs." + in + "}}"
		out = append(out, k)
	}
	return out
}

// composeBuild reads the build: key of a service of a file that is in a repository. The context is a folder of
// the same repository and the Dockerfile is in it.
func composeBuild(where string, b any, repo *ComposeRepo) (*GitSource, Problems) {
	ctx, dockerfile := ".", ""
	args := map[string]string{}
	switch v := b.(type) {
	case string:
		ctx = v
	case map[string]any:
		if c, ok := v["context"]; ok {
			ctx = fmt.Sprint(c)
		}
		if d, ok := v["dockerfile"]; ok {
			dockerfile = fmt.Sprint(d)
		}
		switch a := v["args"].(type) {
		case map[string]any:
			for k, x := range a {
				if x == nil {
					x = ""
				}
				args[k] = fmt.Sprint(x)
			}
		case []any:
			for _, item := range a {
				k, x, _ := strings.Cut(fmt.Sprint(item), "=")
				args[k] = x
			}
		}
	default:
		return nil, Problems{{Path: where, Message: "is not valid"}}
	}
	if strings.Contains(ctx, "://") || strings.HasPrefix(ctx, "git@") || strings.HasPrefix(ctx, "/") {
		return nil, Problems{{Path: where + ".context", Message: "must be a folder of the repository (for example ./app)"}}
	}
	joined := path.Join(repo.Dir, ctx)
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return nil, Problems{{Path: where + ".context", Message: "must stay inside the repository"}}
	}
	if strings.HasPrefix(dockerfile, "/") || strings.Contains(dockerfile, "..") {
		return nil, Problems{{Path: where + ".dockerfile", Message: "must be a file inside the build folder"}}
	}
	g := &GitSource{Repo: repo.URL, Branch: repo.Branch, ConnectionID: repo.ConnectionID, Path: "/" + strings.TrimPrefix(joined, "./"), Dockerfile: dockerfile}
	if joined == "." {
		g.Path = "/"
	}
	if len(args) > 0 {
		g.BuildArgs = args
	}
	return g, nil
}

// dropUnsafe removes the services that mount the Docker socket or are privileged, and the bind mounts of host
// folders. Each one is told in a warning and in a loss with its replacement.
func dropUnsafe(doc map[string]any, res *ComposeResult, extras map[string]*composeExtra) {
	warns := &res.Warnings
	services, _ := doc["services"].(map[string]any)
	volIndex := regexp.MustCompile(`\.volumes\[(\d+)\]$`)
	for _, name := range sortedKeys(services) {
		svc, _ := services[name].(map[string]any)
		vs := serviceViolations("services."+name, name, svc)
		if len(vs) == 0 {
			continue
		}
		skipService, reason := false, ""
		dropVolume := map[int]string{}
		for _, v := range vs {
			if m := volIndex.FindStringSubmatch(v.Path); m != nil && strings.HasPrefix(v.Message, "bind mount ") {
				i, _ := strconv.Atoi(m[1])
				dropVolume[i] = v.Message
				continue
			}
			skipService, reason = true, v.Message
		}
		if skipService {
			delete(services, name)
			*warns = append(*warns, fmt.Sprintf("service %s is skipped: %s", name, reason))
			res.Losses = append(res.Losses, Loss{Path: "services." + name, Key: "services", Disposition: LossDropped,
				Lost:        "the whole service: " + reason,
				Replacement: "Polipan does not run services with access to the host (privileged, the Docker socket, host network, devices). Run this tool on the host outside Polipan, or use a version of it that needs no such access."})
			continue
		}
		var kept []any
		for i, item := range asList(svc["volumes"]) {
			if msg, drop := dropVolume[i]; drop {
				*warns = append(*warns, fmt.Sprintf("services.%s.volumes[%d] is skipped: %s", name, i, hostPathRe.ReplaceAllString(msg, "bind mount of a folder of the host")))
				if extras[name] == nil {
					extras[name] = &composeExtra{}
				}
				res.Losses = append(res.Losses, bindLoss(fmt.Sprintf("services.%s.volumes[%d]", name, i), item, extras[name]))
				continue
			}
			kept = append(kept, item)
		}
		svc["volumes"] = kept
	}
}

func convertComposeService(path, name string, svc map[string]any, names map[string]string, git *GitSource) (ServiceEntry, []string, []Loss, Problems) {
	var warns []string
	var losses []Loss
	var ps Problems
	e := ServiceEntry{Type: "app"}
	img, _ := svc["image"].(string)
	if git != nil {
		e.Source = &Source{Git: git}
	} else {
		if img == "" {
			ps = append(ps, Problem{Path: path + ".image", Message: "is required (build: is not supported)"})
		}
		e.Source = &Source{Image: &struct {
			Name string `yaml:"name" json:"name"`
		}{Name: img}}
	}
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
			losses = append(losses, Loss{Path: fmt.Sprintf("%s.ports[%d]", path, i), Key: "ports", Disposition: LossIgnored,
				Lost:        fmt.Sprintf("the host port %d (the container port %d is kept)", host, port),
				Replacement: fmt.Sprintf("domains:\n  - host: <your domain>\n    port: %d\n# Or, to open the port on the node:\nports:\n  - container: %d\n    host: %d", port, port, host)})
		}
		e.Ports = append(e.Ports, Port{Container: port})
	}
	for i, v := range asList(svc["volumes"]) {
		vol, ok, warn := composeVolume(name, v)
		if warn != "" {
			warns = append(warns, fmt.Sprintf("%s.volumes[%d]: %s", path, i, warn))
			losses = append(losses, Loss{Path: fmt.Sprintf("%s.volumes[%d]", path, i), Key: "volumes", Disposition: LossIgnored, Lost: warn,
				Replacement: "volumes:\n  - name: <name>\n    path: <path in the container>"})
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
				losses = append(losses, Loss{Path: path + ".restart", Key: "restart", Disposition: LossIgnored, Lost: "the restart policy " + r,
					Replacement: "restart_policy: always  # or on-failure, or no"})
			}
		}
	}
	if hc, ok := svc["healthcheck"].(map[string]any); ok {
		h, w := composeHealth(hc)
		e.Healthcheck = h
		if w != "" {
			warns = append(warns, path+".healthcheck: "+w)
			losses = append(losses, Loss{Path: path + ".healthcheck", Key: "healthcheck", Disposition: LossIgnored, Lost: "the health check (" + w + ")",
				Replacement: "healthcheck:\n  type: http  # or tcp, or command\n  path: /\n  port: <port>"})
		}
	}
	if d, ok := svc["deploy"].(map[string]any); ok {
		for k := range d {
			if k != "resources" {
				warns = append(warns, fmt.Sprintf("%s.deploy.%s is ignored", path, k))
				repl := "Polipan has no setting for deploy." + k + "."
				switch k {
				case "replicas":
					repl = fmt.Sprintf("replicas: %v", d[k])
				case "restart_policy":
					repl = "restart_policy: always  # or on-failure, or no"
				case "update_config", "rollback_config":
					repl = "deploy:\n  strategy: blue_green  # or recreate, or canary"
				}
				losses = append(losses, Loss{Path: path + ".deploy." + k, Key: "deploy", Disposition: LossIgnored, Lost: "the setting deploy." + k, Replacement: repl})
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
				losses = append(losses, Loss{Path: path + ".deploy.resources.reservations", Key: "deploy", Disposition: LossIgnored,
					Lost: "the reserved CPU and memory", Replacement: "Polipan sets limits only:\nresources:\n  cpu: <cores>\n  memory: <size, for example 512Mi>"})
			}
		}
	}
	return e, warns, losses, ps
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
