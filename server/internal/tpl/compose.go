// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"bytes"
	"encoding/json"
	"errors"
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
	// EnvExamples maps the path of an env file that is not in the repository to the path of an example file that is
	// (.env.example, .env.sample, .env.dist). The content of the example is in EnvFiles under its own path.
	EnvExamples map[string]string
	// BindFiles are the files that services bind mount, read from the repository by path (ComposeBindSources).
	BindFiles map[string][]byte
	// BindDirs are small folders that services bind mount: the path of the folder, then the files by path inside it.
	BindDirs map[string]map[string][]byte
	// BindSkipped maps a bind source that is in the repository but was not read to the reason.
	BindSkipped map[string]string
	// Project is the project slug for the replacement polipan.yaml.
	Project string
}

// ImportCompose converts a Compose file. A file with unsafe settings gives *UnsafeError. A build:
// key or a service without an image gives Problems. Keys that are not supported are warnings.
func ImportCompose(data []byte) (*ComposeResult, error) {
	return ImportComposeWith(data, ComposeOptions{})
}

var composeVar = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}`)

// genMarkerPrefix starts the marker that stands for a generated secret in the text of a file. The value is never
// in the text: a later step swaps the marker for a reference to a secret input.
const genMarkerPrefix = "__polipan_gen_"

func genMarker(name string) string { return genMarkerPrefix + name + "__" }

// genPassName and genNames are the variables that are generated when nobody gives a value.
var genPassName = regexp.MustCompile(`(?i)PASS`)

var genNames = map[string]bool{"APP_KEY": true, "SECRET_KEY": true, "SECRET_KEY_BASE": true, "JWT_SECRET": true,
	"APP_SECRET": true, "SESSION_SECRET": true, "NEXTAUTH_SECRET": true}

// generatable tells if a variable without a value gets a generated secret.
func generatable(name string) bool { return genNames[name] || genPassName.MatchString(name) }

// interpolated is the result of composeInterpolateGen.
type interpolated struct {
	Text      string
	Unset     []string          // names with no value that cannot be generated
	Generated []string          // names that got a marker
	Resolved  map[string]string // the value of each variable that the file read (a marker for a generated one)
}

// composeInterpolate fills in ${NAME}, ${NAME:-default}, ${NAME-default} and ${NAME:?message}. $$ is a dollar sign.
// It returns the names that had no value and no default.
func composeInterpolate(text string, vars map[string]string) (string, []string) {
	r := composeInterpolateGen(text, vars, false)
	return r.Text, r.Unset
}

// composeInterpolateGen is composeInterpolate. With gen, a variable that has no value and can be generated gets a
// marker instead of an empty text.
func composeInterpolateGen(text string, vars map[string]string, gen bool) interpolated {
	unset, generated := map[string]bool{}, map[string]bool{}
	resolved := map[string]string{}
	out := composeVar.ReplaceAllStringFunc(text, func(m string) string {
		if m == "$$" {
			return "$"
		}
		g := composeVar.FindStringSubmatch(m)
		name, op, def := g[1], g[2], g[3]
		v, set := vars[name]
		switch op {
		case "-":
			if set {
				resolved[name] = v
				return v
			}
			return def
		case ":-":
			if set && v != "" {
				resolved[name] = v
				return v
			}
			return def
		case "+":
			if set {
				return def
			}
			return ""
		case ":+":
			if set && v != "" {
				return def
			}
			return ""
		case ":?":
			if set && v != "" {
				resolved[name] = v
				return v
			}
		default: // "" and "?"
			if set {
				resolved[name] = v
				return v
			}
		}
		if gen && generatable(name) {
			generated[name] = true
			resolved[name] = genMarker(name)
			return genMarker(name)
		}
		unset[name] = true
		return ""
	})
	return interpolated{Text: out, Unset: sortedKeys(anyBoolMap(unset)), Generated: sortedKeys(anyBoolMap(generated)), Resolved: resolved}
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
	res, err := importCompose(data, opts)
	if err != nil {
		return nil, unmarkErr(redactSecrets(err, rawSecrets(data, opts)))
	}
	return res, nil
}

// rawSecrets lists the secret values that the file gives: the secret variables of environment: and of the env
// files, and the secret build arguments (IsSecretName, checkedSecret). It reads the file again, as it is, so it
// also works when the import stops early with a problem.
func rawSecrets(data []byte, opts ComposeOptions) []secretValue {
	text, _ := composeInterpolate(string(data), opts.Vars)
	var doc map[string]any
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	var out []secretValue
	add := func(svc, k string, v any) {
		if v == nil {
			return
		}
		if s := fmt.Sprint(v); IsSecretName(k) && checkedSecret(s) {
			out = append(out, secretValue{svc, k, s})
		}
	}
	addList := func(svc string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				add(svc, k, val)
			}
		case []any:
			for _, item := range x {
				k, val, _ := strings.Cut(fmt.Sprint(item), "=")
				add(svc, k, val)
			}
		}
	}
	services, _ := doc["services"].(map[string]any)
	for _, name := range sortedKeys(services) {
		svc, _ := services[name].(map[string]any)
		addList(name, svc["environment"])
		if b, ok := svc["build"].(map[string]any); ok {
			addList(name, b["args"])
		}
		for _, f := range envFilesOf(svc["env_file"], repoDir(opts.Repo)) {
			content, ok := opts.EnvFiles[f.RepoPath]
			if !ok {
				content, ok = opts.EnvFiles[opts.EnvExamples[f.RepoPath]]
			}
			if ok && opts.Repo != nil {
				values, _, _ := parseEnvFile(content)
				for k, v := range values {
					add(name, k, v)
				}
			}
		}
	}
	return out
}

// redactSecrets replaces the secret values in the messages of an import error (a bad port, a bad size, an unsafe
// setting can quote a part of the file). The message names the secret instead of its value.
func redactSecrets(err error, secrets []secretValue) error {
	if len(secrets) == 0 {
		return err
	}
	fix := func(ps Problems) Problems {
		out := make(Problems, len(ps))
		for i, p := range ps {
			for _, s := range secrets {
				p.Message = strings.ReplaceAll(p.Message, s.value, "<the value of the secret "+s.name+">")
				p.Path = strings.ReplaceAll(p.Path, s.value, "<secret "+s.name+">")
			}
			out[i] = p
		}
		return out
	}
	var ps Problems
	var unsafe *UnsafeError
	switch {
	case errors.As(err, &unsafe):
		return &UnsafeError{Violations: fix(unsafe.Violations)}
	case errors.As(err, &ps):
		return fix(ps)
	}
	msg := err.Error()
	for _, s := range secrets {
		if strings.Contains(msg, s.value) {
			return errors.New("the Compose file cannot be imported (the error message is hidden: it has the value of a secret)")
		}
	}
	return err
}

// importCompose is ImportComposeWith without the redaction of secret values in its errors.
func importCompose(data []byte, opts ComposeOptions) (*ComposeResult, error) {
	ip := composeInterpolateGen(string(data), opts.Vars, true)
	text, unset := ip.Text, ip.Unset
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
		dropUnsafe(doc, res, extras, bindInput{fromRepo: opts.Repo != nil, dir: repoDir(opts.Repo), opts: &opts})
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
	names := map[string]string{}                // compose name -> service name
	secretVars := map[string][]string{}         // service name -> the names of its secret variables
	fromExample := map[string]map[string]bool{} // service name -> the variables that came from an example env file
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
		if where, g := markerInKeys(svc, ip.Generated, "image", "ports", "volumes", "build", "deploy", "restart", "depends_on"); where != "" {
			ps = append(ps, Problem{Path: path + "." + where, Message: "the generated secret " + g + " is used in " + where + ", which cannot hold a secret. Use it in a variable, or in a command (sh -c)"})
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
				ls, ex := envFileLoss(path+".env_file", envFilesOf(svc[k], repoDir(opts.Repo)), opts.EnvFiles, opts.EnvExamples, opts.Repo != nil, entry.Env)
				res.Losses = append(res.Losses, ls...)
				for _, key := range ex {
					if fromExample[name] == nil {
						fromExample[name] = map[string]bool{}
					}
					fromExample[name][key] = true
				}
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
		if git != nil && len(entry.Ports) == 0 && svc["restart"] == nil && svc["entrypoint"] != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s looks like a command that runs once (it has an entrypoint, no port and no restart). Delete the service if it should not run all the time", path))
		}
		mergeImported(path, &entry, extras[raw], res)
		res.Manifest.Services[name] = entry
	}
	if len(ps) == 0 {
		ps = wireCompose(res, ip, names, fromExample, secretVars)
	}
	if len(ps) == 0 {
		for _, name := range sortedKeys(res.Manifest.Services) {
			e := res.Manifest.Services[name]
			if more := secretEnv(name, &e, res); len(more) > 0 {
				secretVars[name] = append(secretVars[name], more...)
			}
			res.Manifest.Services[name] = e
		}
		ps = secretReuse(res, secretVars)
	}
	if len(ps) > 0 {
		return nil, unmarkProblems(ps)
	}
	unmarkResult(res)
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

// mergeImported adds the files that were read from the repository to the files of the service. A path that another
// file or a volume of the service already uses is not imported.
func mergeImported(where string, entry *ServiceEntry, x *composeExtra, res *ComposeResult) {
	if x == nil {
		return
	}
	used := map[string]bool{}
	for _, f := range entry.Files {
		used[f.Path] = true
	}
	for _, v := range entry.Volumes {
		used[v.Path] = true
	}
	for _, f := range x.Imported {
		if used[f.Path] {
			res.Losses = append(res.Losses, Loss{Path: where + ".volumes", Key: "volumes", Disposition: LossDropped,
				Lost:        "the file " + f.Path + " from the repository: another mount of the service uses this path",
				Replacement: "Add the file in the Files tab of the service, with a path that no other mount uses."})
			continue
		}
		used[f.Path] = true
		entry.Files = append(entry.Files, f)
	}
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
	return secretEnvIf(service, e, res, func(k, _ string) bool { return IsSecretName(k) })
}

// secretEnvIf is secretEnv for the variables that pick chooses.
func secretEnvIf(service string, e *ServiceEntry, res *ComposeResult, pick func(name, value string) bool) []string {
	var out []string
	for _, k := range sortedKeys(e.Env) {
		v := fmt.Sprint(e.Env[k])
		if v == "" || strings.Contains(v, "${{") || !pick(k, v) {
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

// secretValue is a value of a secret of the import, and where it comes from.
type secretValue struct{ service, name, value string }

// checkedSecret tells if a secret value is looked for in the other settings. A very short value or a yes/no word
// (AUTH_ENABLED=true) would be found in many places where it is not a secret.
func checkedSecret(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "null", "none":
		return false
	}
	return len(v) >= 6
}

// secretReuse keeps the secret values of the import out of the plain settings (SEC-1). A YAML alias, or the same
// text, can put the value of a secret variable into a plain variable, a command, a health check, a file or another
// setting. A plain variable with a secret value in it becomes a secret too. Any other setting with a secret value
// refuses the import: the message names the service and the secret, never the value.
func secretReuse(res *ComposeResult, secretVars map[string][]string) Problems {
	var secrets []secretValue
	for _, svc := range sortedKeys(res.Manifest.Services) {
		e := res.Manifest.Services[svc]
		for _, k := range sortedKeys(e.Env) {
			if in, ok := wholeInputRef(fmt.Sprint(e.Env[k])); ok {
				if v := fmt.Sprint(res.Inputs[in]); checkedSecret(v) {
					secrets = append(secrets, secretValue{svc, k, v})
				}
			}
		}
		if e.Source != nil && e.Source.Git != nil {
			for _, k := range sortedKeys(e.Source.Git.BuildArgs) {
				if v := e.Source.Git.BuildArgs[k]; IsSecretName(k) && checkedSecret(v) {
					secrets = append(secrets, secretValue{svc, k, v})
				}
			}
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	has := func(text string) (secretValue, bool) {
		for _, s := range secrets {
			if strings.Contains(text, s.value) {
				return s, true
			}
			// The value as it is written inside a JSON string (quotes, backslashes and the like are escaped).
			if q, err := json.Marshal(s.value); err == nil && strings.Contains(text, string(q[1:len(q)-1])) {
				return s, true
			}
		}
		return secretValue{}, false
	}
	var ps Problems
	for _, svc := range sortedKeys(res.Manifest.Services) {
		e := res.Manifest.Services[svc]
		// A plain variable that holds a secret value (DATABASE_URL with the password in it) is kept as a secret.
		if names := secretEnvIf(svc, &e, res, func(_, v string) bool { _, found := has(v); return found }); len(names) > 0 {
			secretVars[svc] = append(secretVars[svc], names...)
			for _, n := range names {
				res.Losses = append(res.Losses, Loss{Path: "services." + svc + ".environment." + n, Key: "environment", Disposition: LossImported,
					Lost:        "the variable " + n + " holds the value of a secret, so it is imported as a secret",
					Replacement: "secrets: [" + n + "]  # the value is in the Secrets tab"})
			}
		}
		res.Manifest.Services[svc] = e
		// All other settings, without the variables (all safe now) and without the build arguments that are secrets.
		plain := e
		plain.Env = nil
		if e.Source != nil && e.Source.Git != nil {
			g := *e.Source.Git
			g.BuildArgs = map[string]string{}
			for k, v := range e.Source.Git.BuildArgs {
				if !IsSecretName(k) {
					g.BuildArgs[k] = v
				}
			}
			plain.Source = &Source{Image: e.Source.Image, Git: &g}
		}
		raw, err := json.Marshal(plain)
		if err != nil {
			ps = append(ps, Problem{Path: "services." + svc, Message: err.Error()})
			continue
		}
		if s, found := has(string(raw)); found {
			ps = append(ps, Problem{Path: "services." + svc, Message: fmt.Sprintf(
				"a plain setting (command, entrypoint, health check, build argument, file or another one) has the value of the secret %s of service %s. "+
					"Polipan keeps secrets out of plain settings. Write $%s in a shell command (sh -c) so that the container reads the secret variable, then import again",
				s.name, s.service, s.name)})
		}
	}
	// The messages of the answer quote parts of the file (a restart policy, a port, a size).
	var texts []string
	texts = append(texts, res.Warnings...)
	for _, l := range res.Losses {
		texts = append(texts, l.Lost, l.Replacement)
	}
	if s, found := has(strings.Join(texts, "\n")); found {
		ps = append(ps, Problem{Path: "services." + s.service, Message: fmt.Sprintf(
			"a setting that is not imported has the value of the secret %s of service %s. Remove the value from that setting, then import again", s.name, s.service)})
	}
	return ps
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
func dropUnsafe(doc map[string]any, res *ComposeResult, extras map[string]*composeExtra, in bindInput) {
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
				res.Losses = append(res.Losses, bindLoss(fmt.Sprintf("services.%s.volumes[%d]", name, i), item, extras[name], in))
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

// markerInKeys gives the first of the keys of a service that holds the marker of a generated secret, and the name
// of the variable.
func markerInKeys(svc map[string]any, generated []string, keys ...string) (string, string) {
	for _, k := range keys {
		if v, ok := svc[k]; ok {
			if b, err := json.Marshal(v); err == nil && strings.Contains(string(b), genMarkerPrefix) {
				for _, g := range generated {
					if strings.Contains(string(b), genMarker(g)) {
						return k, g
					}
				}
				return k, "of the file"
			}
		}
	}
	return "", ""
}
