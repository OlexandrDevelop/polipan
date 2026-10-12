// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loss is a part of a Compose file that is not imported as it is, with what to use instead.
type Loss struct {
	Path        string `json:"path"`        // services.app.env_file, services.app.volumes[1]
	Key         string `json:"key"`         // the Compose key: env_file, volumes, build, ports, or another key
	Disposition string `json:"disposition"` // imported, replaced, ignored, dropped or manual
	Lost        string `json:"lost"`        // what is lost, in one sentence
	Replacement string `json:"replacement"` // a polipan.yaml part or one manual step
}

// The dispositions of a Loss.
const (
	LossImported = "imported" // the content was imported in another form (env_file values)
	LossReplaced = "replaced" // Polipan does this in its own way, nothing to do
	LossIgnored  = "ignored"  // not imported. The replacement says what to set
	LossDropped  = "dropped"  // removed because it is not allowed (a bind mount of a host folder, an unsafe service)
	LossManual   = "manual"   // a step that only the user can do
)

// composeExtra is what the replacement polipan.yaml adds to a service for the parts that were dropped.
type composeExtra struct {
	Volumes []Volume
	Files   []File
	Secrets []secretRef
}

type secretRef struct {
	Name      string `yaml:"name"`
	File      string `yaml:"file,omitempty"`
	BuildTime bool   `yaml:"build_time,omitempty"`
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var notEnvChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// secretName tells if a variable looks like a secret by its name.
var secretName = regexp.MustCompile(`(?i)(PASS|SECRET|TOKEN|PRIVATE|CREDENTIAL|(^|_)KEY($|_)|APIKEY|AUTH|SALT|DSN|CERT)`)

// IsSecretName tells if a variable with this name is kept as a secret when a Compose file is imported.
func IsSecretName(name string) bool { return secretName.MatchString(name) }

// composeEnvFile is one file of env_file.
type composeEnvFile struct {
	Name     string // as written in the Compose file
	RepoPath string // the path inside the repository, "" when the file cannot be read from it
	Required bool
}

// envFilesOf reads the env_file key of a service: a string, a list of strings, or a list of {path, required}.
func envFilesOf(v any, dir string) []composeEnvFile {
	var raw []composeEnvFile
	add := func(p string, req bool) {
		if p = strings.TrimSpace(p); p != "" {
			raw = append(raw, composeEnvFile{Name: p, Required: req})
		}
	}
	switch x := v.(type) {
	case string:
		add(x, true)
	case []any:
		for _, item := range x {
			switch it := item.(type) {
			case string:
				add(it, true)
			case map[string]any:
				req := true
				if r, ok := it["required"]; ok {
					req = truthy(r)
				}
				add(fmt.Sprint(it["path"]), req)
			}
		}
	}
	for i := range raw {
		raw[i].RepoPath = envFileRepoPath(raw[i].Name, dir)
	}
	return raw
}

// envFileRepoPath is the path of an env file inside the repository. An absolute path, a home path or a path that
// leaves the repository gives "": such a file is never read.
func envFileRepoPath(name, dir string) string {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") || strings.ContainsAny(name, "\x00\\$") {
		return ""
	}
	p := path.Clean(path.Join(dir, name))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// ComposeEnvFiles lists the env files that the services of a Compose file read, as paths inside the repository.
// dir is the folder of the Compose file in the repository. Files that are outside the repository are not listed.
func ComposeEnvFiles(data []byte, vars map[string]string, dir string) []string {
	text, _ := composeInterpolate(string(data), vars)
	var doc map[string]any
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	services, _ := doc["services"].(map[string]any)
	seen := map[string]bool{}
	var out []string
	for _, name := range sortedKeys(services) {
		svc, _ := services[name].(map[string]any)
		for _, f := range envFilesOf(svc["env_file"], dir) {
			if f.RepoPath != "" && !seen[f.RepoPath] {
				seen[f.RepoPath] = true
				out = append(out, f.RepoPath)
			}
		}
	}
	return out
}

// parseEnvFile reads NAME=value lines like Compose does. It returns the names in file order. Lines that are not
// valid are skipped and counted.
func parseEnvFile(data []byte) (map[string]string, []string, int) {
	values := map[string]string{}
	var order []string
	bad := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !envNameRe.MatchString(k) {
			bad++
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			v = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			v = v[1 : len(v)-1]
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		if _, dup := values[k]; !dup {
			order = append(order, k)
		}
		values[k] = v
	}
	return values, order, bad
}

// envFileLoss imports the env files of one service into env. The environment key of the service wins over the
// files, and a later file wins over an earlier one, like in Compose. The values are never put in a message.
func envFileLoss(where string, files []composeEnvFile, content map[string][]byte, fromRepo bool, env map[string]any) []Loss {
	var out []Loss
	fromFiles := map[string]string{}
	var order []string
	for _, f := range files {
		l := Loss{Path: where, Key: "env_file"}
		data, ok := content[f.RepoPath]
		switch {
		case f.RepoPath == "":
			l.Disposition = LossManual
			l.Lost = fmt.Sprintf("the variables of %s: the file is outside the repository, so it is not read", envFileLabel(f.Name))
			l.Replacement = "Add the variables in the Variables tab of the service after the import. Mark passwords and keys as secrets."
		case !fromRepo:
			l.Disposition = LossManual
			l.Lost = fmt.Sprintf("the variables of %s: a pasted Compose file has no files next to it", f.Name)
			l.Replacement = "Import the file from its Git repository so that " + f.Name + " is read, or add the variables in the Variables tab of the service."
		case !ok:
			l.Disposition = LossManual
			l.Lost = fmt.Sprintf("the variables of %s: the file is not in the repository (often it is in .gitignore), or it is a symbolic link, which is not read", f.Name)
			if !f.Required {
				l.Disposition = LossIgnored
				l.Lost = fmt.Sprintf("%s is not in the repository. It is optional (required: false)", f.Name)
			}
			l.Replacement = "Add the variables in the Variables tab of the service after the import, or commit a copy without secrets (for example .env.example) and name it in env_file."
		default:
			values, names, bad := parseEnvFile(data)
			for _, k := range names {
				if _, seen := fromFiles[k]; !seen {
					order = append(order, k)
				}
				fromFiles[k] = values[k]
			}
			l.Disposition = LossImported
			l.Lost = fmt.Sprintf("%s is not mounted: its %d variables are imported as variables of the service", f.Name, len(names))
			if bad > 0 {
				l.Lost += fmt.Sprintf(" (%d lines that are not NAME=value are skipped)", bad)
			}
			var secrets, plain []string
			for _, k := range names {
				if IsSecretName(k) {
					secrets = append(secrets, k)
				} else {
					plain = append(plain, k)
				}
			}
			l.Replacement = envFileReplacement(plain, secrets)
		}
		out = append(out, l)
	}
	for _, k := range order {
		if _, set := env[k]; !set {
			env[k] = fromFiles[k]
		}
	}
	return out
}

// envFileLabel names an env file without a host path.
func envFileLabel(name string) string {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") {
		return "a file of the host (" + path.Base(name) + ")"
	}
	return name
}

func envFileReplacement(plain, secrets []string) string {
	var b strings.Builder
	if len(plain) > 0 {
		b.WriteString("env:\n")
		for _, k := range plain {
			fmt.Fprintf(&b, "  %s: <value from the file>\n", k)
		}
	}
	if len(secrets) > 0 {
		fmt.Fprintf(&b, "secrets: [%s]  # the values are imported as secrets\n", strings.Join(secrets, ", "))
	}
	if b.Len() == 0 {
		return "The file has no variables. Nothing to do."
	}
	return b.String()
}

// bindLoss tells what a dropped bind mount was and what replaces it in polipan.yaml. A host path that is not a
// folder of the repository is never written in the message.
func bindLoss(where string, item any, extra *composeExtra) Loss {
	source, target, readOnly := bindParts(item)
	l := Loss{Path: where, Key: "volumes", Disposition: LossDropped}
	label := "a folder of the host"
	if strings.HasPrefix(source, ".") {
		label = source
	}
	base := path.Base(target)
	isFile := strings.Contains(strings.TrimPrefix(base, "."), ".")
	secretish := strings.HasPrefix(target, "/run/secrets") || strings.Contains(strings.ToLower(source), "secret")
	switch {
	case secretish:
		name := strings.ToUpper(composeName.ReplaceAllString(strings.ToLower(strings.TrimSuffix(path.Base(source), path.Ext(source))), "_"))
		name = strings.Trim(strings.ReplaceAll(name, "-", "_"), "_")
		if name == "" || !envNameRe.MatchString(name) {
			name = "SECRET"
		}
		file := target
		if !isFile {
			file = path.Join(target, "<file name>")
		}
		name += "_FILE"
		extra.Secrets = append(extra.Secrets, secretRef{Name: name, File: file})
		l.Lost = fmt.Sprintf("%s mounted at %s (secret files)", label, target)
		l.Replacement = fmt.Sprintf("secrets:\n  - name: %s\n    file: %s\n# Set the secret %s to the content of the file. Make one secret for each file of the folder.", name, file, name)
	case isFile:
		extra.Files = append(extra.Files, File{Path: target, Content: "TODO: paste the content of " + label + " here\n"})
		l.Lost = fmt.Sprintf("%s mounted at %s (a config file)", label, target)
		l.Replacement = fmt.Sprintf("files:\n  - path: %s\n    content: |\n      <the content of the file>\n# A file is read-only and must not hold secrets. Or copy the file into the image in the Dockerfile.", target)
	default:
		name := strings.Trim(composeName.ReplaceAllString(strings.ToLower(base), "-"), "-")
		if name == "" {
			name = "data"
		}
		extra.Volumes = append(extra.Volumes, Volume{Name: name, Path: target})
		l.Lost = fmt.Sprintf("%s mounted at %s, and the files in it", label, target)
		l.Replacement = fmt.Sprintf("volumes:\n  - name: %s\n    path: %s\n# A new volume is empty: copy the files into it after the first deploy.", name, target)
		if readOnly {
			l.Replacement += " The folder was read-only: if it holds code or fixed data, copy it into the image in the Dockerfile instead."
		}
	}
	return l
}

// bindParts reads the source, the target and the read-only flag of a Compose volume.
func bindParts(item any) (source, target string, readOnly bool) {
	switch x := item.(type) {
	case string:
		parts := strings.Split(x, ":")
		if len(parts) >= 2 {
			source, target = parts[0], parts[1]
		}
		if len(parts) >= 3 {
			readOnly = strings.Contains(parts[2], "ro")
		}
	case map[string]any:
		source, target = fmt.Sprint(x["source"]), fmt.Sprint(x["target"])
		readOnly = truthy(x["read_only"])
	}
	return source, target, readOnly
}

// keyReplacements are the replacements of the service keys that are not imported.
var keyReplacements = map[string]struct{ disposition, text string }{
	"container_name":    {LossReplaced, "Polipan names the containers. Other services reach this one by its service name."},
	"hostname":          {LossReplaced, "Other services reach this one by its service name."},
	"networks":          {LossReplaced, "All services of an environment share one private network. Nothing to set."},
	"links":             {LossReplaced, "All services of an environment share one private network. Use the service name as the host."},
	"expose":            {LossReplaced, "Ports are open to the other services of the environment. Add `ports: [<port>]` for a domain or a health check."},
	"labels":            {LossReplaced, "Polipan sets its own labels. Use a domain (`domains:`) instead of proxy labels."},
	"logging":           {LossReplaced, "Polipan collects the logs. See the Logs tab of the service."},
	"platform":          {LossIgnored, "The image of the node's platform is pulled. Use an image that has it."},
	"stdin_open":        {LossIgnored, "Not needed for a service that runs all the time."},
	"tty":               {LossIgnored, "Not needed for a service that runs all the time."},
	"working_dir":       {LossIgnored, "Set WORKDIR in the Dockerfile, or use a full path in the command."},
	"user":              {LossIgnored, "Set USER in the Dockerfile."},
	"extra_hosts":       {LossIgnored, "Use the service name of another service as the host, or a DNS name."},
	"dns":               {LossIgnored, "The node's DNS is used."},
	"stop_grace_period": {LossIgnored, "stop_grace_period: <duration, for example 30s>"},
	"stop_signal":       {LossIgnored, "Set STOPSIGNAL in the Dockerfile."},
	"secrets":           {LossIgnored, "secrets:\n  - name: <NAME>\n    file: /run/secrets/<name>\n# Set the value of the secret in the Secrets tab."},
	"configs":           {LossIgnored, "files:\n  - path: <path in the container>\n    content: |\n      <the content>"},
	"tmpfs":             {LossIgnored, "volumes:\n  - name: tmp\n    path: <path>\n    kind: tmpfs"},
	"mem_limit":         {LossIgnored, "resources:\n  memory: <size, for example 512Mi>"},
	"cpus":              {LossIgnored, "resources:\n  cpu: <cores, for example 0.5>"},
	"init":              {LossIgnored, "Use an image with an init process (for example tini) as the entrypoint."},
	"profiles":          {LossIgnored, "Every service of the file is imported. Delete the services that you do not need."},
	"pull_policy":       {LossIgnored, "The image is pulled on each deploy."},
}

// keyLoss is the loss of a service key that is not imported.
func keyLoss(where, key string) Loss {
	l := Loss{Path: where + "." + key, Key: key, Disposition: LossIgnored, Lost: "the setting " + key}
	if r, ok := keyReplacements[key]; ok {
		l.Disposition, l.Replacement = r.disposition, r.text
		return l
	}
	l.Replacement = "Polipan has no setting for " + key + ". If the app needs it, set it in the Dockerfile or in the image."
	return l
}

// buildLoss tells what replaces build: in a pasted file.
func buildLoss(where string, b any, image string) Loss {
	snippet := fmt.Sprintf("source:\n  git:\n    repo: <owner/name or https URL>\n    branch: main\n    path: %s\n    builder: dockerfile", buildPath(b))
	if image != "" {
		return Loss{Path: where, Key: "build", Disposition: LossIgnored,
			Lost:        "the build of the code: the image " + image + " is pulled instead",
			Replacement: snippet + "\n# Or import the file from its Git repository: build: is then built from it."}
	}
	return Loss{Path: where, Key: "build", Disposition: LossManual, Lost: "the build of the code",
		Replacement: snippet + "\n# Or import the file from its Git repository: build: is then built from it."}
}

// buildPath is the folder of a build: key as a path of a Git service ("/" is the root of the repository).
func buildPath(b any) string {
	ctx := "."
	switch v := b.(type) {
	case string:
		ctx = v
	case map[string]any:
		if c, ok := v["context"]; ok {
			ctx = fmt.Sprint(c)
		}
	}
	return path.Clean("/" + ctx)
}

// buildArgLoss tells that a build argument looks like a secret and is imported as a build-time secret. Its value is
// never in the message.
func buildArgLoss(where, name string) Loss {
	return Loss{Path: where, Key: "build", Disposition: LossManual,
		Lost: "the build argument " + name + " looks like a secret: it is imported as a build-time secret, not as a build argument. " +
			"A Dockerfile line ARG " + name + " gets no value now: change the Dockerfile before the first build",
		Replacement: fmt.Sprintf("# In the Dockerfile, remove ARG %s and read the secret in the RUN step that needs it:\n"+
			"RUN --mount=type=secret,id=%s,env=%s <your command>\n"+
			"# or, with an older BuildKit: RUN --mount=type=secret,id=%s %s=\"$(cat /run/secrets/%s)\" <your command>\n"+
			"# polipan.yaml:\nsecrets:\n  - name: %s\n    build_time: true\n# The value is in the Secrets tab. Change it there.", name, name, name, name, name, name, name)}
}

// topLevelLosses tells what the top-level volumes, networks, configs and secrets of a file had that is not
// imported. A named volume without settings is not a loss: the service gets it. A host path is never written.
func topLevelLosses(doc map[string]any) []Loss {
	var out []Loss
	if v, ok := doc["name"]; ok && fmt.Sprint(v) != "" {
		out = append(out, Loss{Path: "name", Key: "name", Disposition: LossReplaced, Lost: "the project name of the file",
			Replacement: "The services go to the project and environment where you import them."})
	}
	if _, ok := doc["version"]; ok {
		out = append(out, Loss{Path: "version", Key: "version", Disposition: LossReplaced, Lost: "the version of the file format",
			Replacement: "Not needed. Nothing to set."})
	}
	vols, _ := doc["volumes"].(map[string]any)
	for _, name := range sortedKeys(vols) {
		def, _ := vols[name].(map[string]any)
		keys := sortedKeys(def)
		if len(keys) == 0 || (len(keys) == 1 && keys[0] == "name" && fmt.Sprint(def["name"]) == name) {
			continue // the service gets this volume as it is
		}
		l := Loss{Path: "volumes." + name, Key: "volumes", Disposition: LossIgnored,
			Lost:        fmt.Sprintf("the settings %s of the volume %s: Polipan makes a local volume on the node", strings.Join(keys, ", "), name),
			Replacement: fmt.Sprintf("volumes:\n  - name: %s\n    path: <path in the container>\n    size_limit: <size, for example 10Gi>  # optional", name)}
		if n, ok := def["name"]; ok && fmt.Sprint(n) != "" && fmt.Sprint(n) != name {
			// name: is the Docker name of the volume. Polipan does not use it: the volume gets the key as its name.
			l.Lost += fmt.Sprintf(". The Docker name %s is not used: the volume is named %s, and the data of %s is not in it", fmt.Sprint(n), name, fmt.Sprint(n))
			l.Replacement += "\n# A new volume is empty: copy the data of the old Docker volume into it after the first deploy."
			l.Disposition = LossManual
		}
		if truthy(def["external"]) {
			l.Disposition = LossManual
			l.Lost = "the external volume " + name + " and its data: Polipan makes a new empty volume"
			l.Replacement += "\n# Copy the data of the old volume into the new one after the first deploy."
		}
		out = append(out, l)
	}
	nets, _ := doc["networks"].(map[string]any)
	for _, name := range sortedKeys(nets) {
		out = append(out, Loss{Path: "networks." + name, Key: "networks", Disposition: LossReplaced, Lost: "the network " + name,
			Replacement: "All services of an environment share one private network. Use the service name as the host."})
	}
	cfgs, _ := doc["configs"].(map[string]any)
	for _, name := range sortedKeys(cfgs) {
		def, _ := cfgs[name].(map[string]any)
		from := "its content"
		if f, ok := def["file"].(string); ok {
			from = envFileLabel(f)
		}
		out = append(out, Loss{Path: "configs." + name, Key: "configs", Disposition: LossIgnored, Lost: "the config " + name + " (" + from + ")",
			Replacement: "files:\n  - path: <path in the container>\n    content: |\n      <the content>\n# Put it on each service that uses the config. A file must not hold secrets."})
	}
	secs, _ := doc["secrets"].(map[string]any)
	for _, name := range sortedKeys(secs) {
		n := strings.Trim(strings.ToUpper(notEnvChar.ReplaceAllString(name, "_")), "_")
		if n == "" || !envNameRe.MatchString(n) {
			n = "SECRET"
		}
		out = append(out, Loss{Path: "secrets." + name, Key: "secrets", Disposition: LossManual, Lost: "the secret " + name + ": its value is not read",
			Replacement: fmt.Sprintf("secrets:\n  - name: %s\n    file: /run/secrets/%s  # or leave out file: to get it as a variable\n# Set the value in the Secrets tab.", n, name)})
	}
	return out
}

// The replacement polipan.yaml. The fields are in the order of the file format (07-config-as-code).
type yamlFile struct {
	Version  int                     `yaml:"version"`
	Project  string                  `yaml:"project"`
	Services map[string]*yamlService `yaml:"services"`
}

type yamlService struct {
	Type        string         `yaml:"type,omitempty"`
	Source      *yamlSource    `yaml:"source,omitempty"`
	Ports       []Port         `yaml:"ports,omitempty"`
	Command     []string       `yaml:"command,omitempty"`
	Entrypoint  []string       `yaml:"entrypoint,omitempty"`
	Env         map[string]any `yaml:"env,omitempty"`
	Secrets     []any          `yaml:"secrets,omitempty"`
	Volumes     []Volume       `yaml:"volumes,omitempty"`
	Files       []File         `yaml:"files,omitempty"`
	Healthcheck map[string]any `yaml:"healthcheck,omitempty"`
	Resources   map[string]any `yaml:"resources,omitempty"`
	Restart     string         `yaml:"restart_policy,omitempty"`
	DependsOn   []string       `yaml:"depends_on,omitempty"`
}

type yamlSource struct {
	Image *struct {
		Name string `yaml:"name" json:"name"`
	} `yaml:"image,omitempty"`
	Git *yamlGit `yaml:"git,omitempty"`
}

type yamlGit struct {
	Repo       string            `yaml:"repo"`
	Branch     string            `yaml:"branch,omitempty"`
	Path       string            `yaml:"path,omitempty"`
	Builder    string            `yaml:"builder"`
	Dockerfile string            `yaml:"dockerfile,omitempty"`
	BuildArgs  map[string]string `yaml:"build_args,omitempty"`
}

// replacementYAML writes the import as a polipan.yaml, with the replacements of the dropped parts. A secret is
// only named: its value is never in the file.
func replacementYAML(m *Manifest, secretEnv map[string][]string, extras map[string]*composeExtra, project string) (string, error) {
	if project == "" {
		project = "<project slug>"
	}
	f := yamlFile{Version: 1, Project: project, Services: map[string]*yamlService{}}
	for name, s := range m.Services {
		ys := &yamlService{Ports: s.Ports, Command: s.Command, Entrypoint: s.Entrypoint, Volumes: s.Volumes, Files: s.Files,
			Healthcheck: s.Healthcheck, Resources: s.Resources, Restart: s.Restart, DependsOn: s.DependsOn}
		if s.Type != "app" {
			ys.Type = s.Type
		}
		if s.Source != nil {
			ys.Source = &yamlSource{Image: s.Source.Image}
			if g := s.Source.Git; g != nil {
				ys.Source.Git = &yamlGit{Repo: g.Repo, Branch: g.Branch, Path: g.Path, Builder: "dockerfile", Dockerfile: g.Dockerfile}
				// A build argument that looks like a secret is a build-time secret: only its name is in the file.
				for _, k := range sortedKeys(g.BuildArgs) {
					if IsSecretName(k) {
						ys.Secrets = append(ys.Secrets, secretRef{Name: k, BuildTime: true})
						continue
					}
					if ys.Source.Git.BuildArgs == nil {
						ys.Source.Git.BuildArgs = map[string]string{}
					}
					ys.Source.Git.BuildArgs[k] = g.BuildArgs[k]
				}
			}
		}
		secret := map[string]bool{}
		for _, k := range secretEnv[name] {
			secret[k] = true
			ys.Secrets = append(ys.Secrets, k)
		}
		for k, v := range s.Env {
			if secret[k] {
				continue
			}
			if ys.Env == nil {
				ys.Env = map[string]any{}
			}
			ys.Env[k] = v
		}
		if x := extras[name]; x != nil {
			ys.Volumes = append(ys.Volumes, x.Volumes...)
			ys.Files = append(ys.Files, x.Files...)
			for _, sr := range x.Secrets {
				ys.Secrets = append(ys.Secrets, sr)
			}
		}
		f.Services[name] = ys
	}
	var b bytes.Buffer
	b.WriteString("# polipan.yaml made from the Compose file. Check the lines with TODO or <...> before you apply it.\n")
	b.WriteString("# Secrets are only named here. Set their values in the Secrets tab or with `polipan secret set`.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return "", err
	}
	return b.String(), nil
}

// sortLosses keeps the losses in the order of the file: by path.
func sortLosses(l []Loss) {
	sort.SliceStable(l, func(i, j int) bool { return l[i].Path < l[j].Path })
}
