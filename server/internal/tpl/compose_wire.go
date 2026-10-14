// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/polipan/polipan/server/internal/dbengine"
)

// The wiring step of a Compose import. It runs after all services are converted. It fills the variables that a
// person would set by hand: the secrets that the file needs and nobody gave, the hosts of the services of the same
// file, and the Laravel defaults. A generated value is only in ComposeResult.Inputs, never in a message or in text.

var markerRe = regexp.MustCompile(genMarkerPrefix + `(.+?)__`)

// unmark writes a marker as ${NAME}, so that a message names the variable and nothing else.
func unmark(s string) string {
	if !strings.Contains(s, genMarkerPrefix) {
		return s
	}
	return markerRe.ReplaceAllStringFunc(s, func(m string) string {
		return "${" + markerRe.FindStringSubmatch(m)[1] + "}"
	})
}

func unmarkProblems(ps Problems) Problems {
	out := make(Problems, len(ps))
	for i, p := range ps {
		p.Message, p.Path = unmark(p.Message), unmark(p.Path)
		out[i] = p
	}
	return out
}

func unmarkResult(res *ComposeResult) {
	for i := range res.Warnings {
		res.Warnings[i] = unmark(res.Warnings[i])
	}
	for i := range res.Losses {
		l := &res.Losses[i]
		l.Path, l.Lost, l.Replacement = unmark(l.Path), unmark(l.Lost), unmark(l.Replacement)
	}
}

// unmarkErr is unmarkProblems for the error of an import.
func unmarkErr(err error) error {
	var ps Problems
	var unsafe *UnsafeError
	switch {
	case errors.As(err, &unsafe):
		return &UnsafeError{Violations: unmarkProblems(unsafe.Violations)}
	case errors.As(err, &ps):
		return unmarkProblems(ps)
	}
	return err
}

// wholeInputRef tells if v is exactly one reference ${{inputs.NAME}}, and gives NAME.
func wholeInputRef(v string) (string, bool) {
	in, ok := strings.CutPrefix(v, "${{inputs.")
	if !ok || !strings.HasSuffix(v, "}}") || strings.Count(v, "${{") != 1 {
		return "", false
	}
	return strings.TrimSuffix(in, "}}"), true
}

// imageKind tells if an image is a database or a cache that other services connect to: postgres, mysql or redis.
func imageKind(img string) string {
	img, _, _ = strings.Cut(strings.ToLower(img), "@")
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		img = img[:i]
	}
	parts := strings.Split(img, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	if len(parts) > 1 && parts[0] == "library" {
		parts = parts[1:]
	}
	base := strings.Join(parts, "/")
	switch {
	case base == "postgres" || base == "postgis/postgis" || base == "bitnami/postgresql" || strings.HasPrefix(base, "timescale/timescaledb"):
		return "postgres"
	case base == "mysql" || base == "mariadb" || base == "bitnami/mysql" || base == "bitnami/mariadb":
		return "mysql"
	case base == "redis" || base == "valkey/valkey" || base == "bitnami/redis" || strings.HasPrefix(base, "redis/redis-stack"):
		return "redis"
	}
	return ""
}

var kindPort = map[string]int{"postgres": 5432, "mysql": 3306, "redis": 6379}

// hostKeyKinds are the variables that name the host of a database or a cache, and the kinds of service they can mean.
var hostKeyKinds = map[string][]string{
	"DB_HOST": {"postgres", "mysql"}, "DATABASE_HOST": {"postgres", "mysql"},
	"POSTGRES_HOST": {"postgres"}, "PGHOST": {"postgres"}, "MYSQL_HOST": {"mysql"}, "REDIS_HOST": {"redis"},
}

// sqlSource are the variables of a database service that hold what DB_PASSWORD, DB_USERNAME and DB_DATABASE need.
var sqlSource = map[string]map[string]string{
	"postgres": {"DB_PASSWORD": "POSTGRES_PASSWORD", "DB_USERNAME": "POSTGRES_USER", "DB_DATABASE": "POSTGRES_DB"},
	"mysql":    {"DB_PASSWORD": "MYSQL_PASSWORD", "DB_USERNAME": "MYSQL_USER", "DB_DATABASE": "MYSQL_DATABASE"},
}

var urlHostRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://(?:[^@/]*@)?)([^:/?#@]+)(.*)$`)

func hostRef(service string) string { return "${{services." + service + ".host}}" }

func isLocalHost(v string) bool {
	switch strings.ToLower(v) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func generateSecret(name string) (string, error) {
	if name == "APP_KEY" { // the Laravel format
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return "base64:" + base64.StdEncoding.EncodeToString(b), nil
	}
	return dbengine.GeneratePassword(dbengine.PasswordLength)
}

// wireCompose does the wiring step. names is the compose name -> service name map.
func wireCompose(res *ComposeResult, ip interpolated, names map[string]string, fromExample map[string]map[string]bool, secretVars map[string][]string) Problems {
	m := res.Manifest
	svcNames := sortedKeys(m.Services)
	gen := map[string]bool{}
	for _, n := range ip.Generated {
		gen[n] = true
	}
	kinds := map[string]string{}
	for _, n := range svcNames {
		if e := m.Services[n]; e.Source != nil && e.Source.Image != nil {
			if k := imageKind(e.Source.Image.Name); k != "" {
				kinds[n] = k
			}
		}
	}
	ofKind := func(ks ...string) []string {
		var out []string
		for _, n := range svcNames {
			for _, k := range ks {
				if kinds[n] == k {
					out = append(out, n)
				}
			}
		}
		return out
	}
	loss := func(svc, key, lost, repl string) {
		res.Losses = append(res.Losses, Loss{Path: "services." + svc + ".environment." + key, Key: "environment", Disposition: LossReplaced, Lost: lost, Replacement: repl})
	}

	// 1. The variables of an example file take the value of the compose variable of the same name (so the app and
	// the database share one password), or the value of the database service.
	for _, n := range svcNames {
		env := m.Services[n].Env
		sql := ofKind("postgres", "mysql")
		for _, k := range sortedKeys(env) {
			if !fromExample[n][k] {
				continue
			}
			if rv := ip.Resolved[k]; rv != "" {
				env[k] = rv
				continue
			}
			if len(sql) != 1 || sql[0] == n {
				continue
			}
			// The database service says which user, database and password it was made with. The example file has
			// the values of a local setup (DB_USERNAME=root), which do not exist in that database.
			if from := sqlSource[kinds[sql[0]]][k]; from != "" {
				dbEnv := m.Services[sql[0]].Env
				v := ""
				if raw, ok := dbEnv[from]; ok && raw != nil {
					v = fmt.Sprint(raw)
				}
				if v == "" && kinds[sql[0]] == "postgres" {
					// The postgres image defaults: the user is postgres, the database has the name of the user.
					switch from {
					case "POSTGRES_USER":
						v = "postgres"
					case "POSTGRES_DB":
						v = "postgres"
						if raw, ok := dbEnv["POSTGRES_USER"]; ok && raw != nil && fmt.Sprint(raw) != "" {
							v = fmt.Sprint(raw)
						}
					}
				}
				if v != "" {
					env[k] = v
				}
			}
		}
	}

	// 2. Hosts of the services of the same file.
	for _, n := range svcNames {
		env := m.Services[n].Env
		for _, k := range sortedKeys(env) {
			v := fmt.Sprint(env[k])
			up := strings.ToUpper(k)
			switch {
			case strings.HasSuffix(up, "_HOST") || up == "PGHOST":
				if target := names[v]; target != "" && v != "" {
					env[k] = hostRef(target)
					loss(n, k, k+" of "+n+" is the name of the service "+target+", which is not a host name in Polipan", k+": ${{services."+target+".host}}")
					continue
				}
				kk, ok := hostKeyKinds[up]
				if !ok || !isLocalHost(v) {
					continue
				}
				found := ofKind(kk...)
				if len(found) != 1 || found[0] == n {
					continue
				}
				env[k] = hostRef(found[0])
				loss(n, k, k+" of "+n+" points to the service "+found[0], k+": ${{services."+found[0]+".host}}")
				portKey := k[:len(k)-4] + "PORT"
				if _, has := env[portKey]; has {
					env[portKey] = strconv.Itoa(kindPort[kinds[found[0]]])
				}
			case strings.HasSuffix(up, "_URL") || strings.HasSuffix(up, "_URI"):
				g := urlHostRe.FindStringSubmatch(v)
				if g == nil || names[g[2]] == "" {
					continue
				}
				user := strings.TrimSuffix(g[1][strings.Index(g[1], "://")+3:], "@")
				if strings.Contains(user, ":") && !strings.Contains(user, genMarkerPrefix) {
					// A password in the text must stay out of the plain variable and the value is not expanded
					// inside a secret, so the host cannot be filled in.
					res.Losses = append(res.Losses, Loss{Path: "services." + n + ".environment." + k, Key: "environment", Disposition: LossManual,
						Lost:        "the host of " + k + " is the name of the service " + names[g[2]] + ", which is not a host name in Polipan",
						Replacement: "Set the host of " + k + " to ${{services." + names[g[2]] + ".host}} in the Variables tab of the service."})
					continue
				}
				env[k] = g[1] + hostRef(names[g[2]]) + g[3]
				loss(n, k, "the host of "+k+" of "+n+" is the name of the service "+names[g[2]]+", which is not a host name in Polipan", "the host of "+k+": ${{services."+names[g[2]]+".host}}")
			}
		}
	}

	// 3. The Laravel profile.
	for _, n := range svcNames {
		env := m.Services[n].Env
		_, hasKey := env["APP_KEY"]
		conn, hasConn := env["DB_CONNECTION"]
		if !hasKey || !hasConn {
			continue
		}
		if fmt.Sprint(env["APP_KEY"]) == "" {
			env["APP_KEY"] = genMarker("APP_KEY")
			gen["APP_KEY"] = true
		}
		if c := fmt.Sprint(conn); c == "" || c == "sqlite" {
			if sql := ofKind("postgres", "mysql"); len(sql) == 1 {
				to := map[string]string{"postgres": "pgsql", "mysql": "mysql"}[kinds[sql[0]]]
				env["DB_CONNECTION"] = to
				loss(n, "DB_CONNECTION", "DB_CONNECTION of "+n+" is "+to+", because the file has the database "+sql[0]+" (the Laravel default is SQLite)", "DB_CONNECTION: "+to)
			}
		}
	}

	// 4. Markers become secrets.
	var ps Problems
	used := map[string]map[string]bool{} // generated name -> the services that use it
	use := func(name, svc string) {
		if used[name] == nil {
			used[name] = map[string]bool{}
		}
		used[name][svc] = true
	}
	addSecret := func(svc, key string) {
		for _, k := range secretVars[svc] {
			if k == key {
				return
			}
		}
		secretVars[svc] = append(secretVars[svc], key)
	}
	genList := sortedKeys(anyBoolMap(gen))
	inputName := func(name string) string { return "g_" + strings.ToLower(name) }
	for _, n := range svcNames {
		e := m.Services[n]
		path := "services." + n
		for _, k := range sortedKeys(e.Env) {
			v := fmt.Sprint(e.Env[k])
			if !strings.Contains(v, genMarkerPrefix) {
				continue
			}
			for _, g := range genList {
				if strings.Contains(v, genMarker(g)) {
					v = strings.ReplaceAll(v, genMarker(g), "${{inputs."+inputName(g)+"}}")
					use(g, n)
				}
			}
			e.Env[k] = v
			addSecret(n, k)
		}
		// A command, an entrypoint or a health check reads the secret from the environment of the shell.
		passed := map[string]bool{}
		e.Command = shellMarkers(e.Command, genList, passed, false)
		e.Entrypoint = shellMarkers(e.Entrypoint, genList, passed, true)
		if hc, ok := e.Healthcheck["command"].([]any); ok && hasMarker(anyStrings(hc)) {
			out := shellMarkers(anyStrings(hc), genList, passed, false)
			h := map[string]any{}
			for k, v := range e.Healthcheck {
				h[k] = v
			}
			h["command"] = stringsToAny(out)
			e.Healthcheck = h
		}
		for _, g := range sortedKeys(anyBoolMap(passed)) {
			ref := "${{inputs." + inputName(g) + "}}"
			if e.Env == nil {
				e.Env = map[string]any{}
			}
			if cur := fmt.Sprint(e.Env[g]); e.Env[g] != nil && cur != "" && cur != ref {
				ps = append(ps, Problem{Path: path + ".environment." + g, Message: "the generated secret " + g + " is used in a command, but the variable " + g + " has another value. Remove one of them"})
				continue
			}
			e.Env[g] = ref
			use(g, n)
			addSecret(n, g)
		}
		// Anywhere else a secret must not go: the file would hold the value.
		if where, g := markerElsewhere(e, genList); where != "" {
			ps = append(ps, Problem{Path: path + "." + where, Message: "the generated secret " + g + " is used in " + where + ", which cannot hold a secret. Use it in a variable, or in a command (sh -c)"})
		}
		m.Services[n] = e
	}
	if len(ps) > 0 {
		return ps
	}

	// The values: one input for each name, shared by all the services.
	for _, g := range genList {
		if len(used[g]) == 0 {
			continue
		}
		v, err := generateSecret(g)
		if err != nil {
			return Problems{{Message: "a secret could not be generated: " + err.Error()}}
		}
		in := inputName(g)
		dup := false
		for _, x := range m.Inputs {
			dup = dup || x.Name == in
		}
		if !dup {
			m.Inputs = append(m.Inputs, Input{Name: in, Type: "secret"})
		}
		if res.Inputs == nil {
			res.Inputs = map[string]any{}
		}
		res.Inputs[in] = v
		var who []string
		for s := range used[g] {
			who = append(who, s)
		}
		sort.Strings(who)
		res.Losses = append(res.Losses, Loss{Path: "services." + who[0] + ".environment." + g, Key: "environment", Disposition: LossGenerated,
			Lost:        "the secret " + g + " is generated and stored as a secret of " + strings.Join(who, ", "),
			Replacement: "Nothing to do. The same value is used by all of these services. You can see it in the Secrets tab."})
	}
	return nil
}

func anyStrings(in []any) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = fmt.Sprint(x)
	}
	return out
}

func hasMarker(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, genMarkerPrefix) {
			return true
		}
	}
	return false
}

// shellMarkers writes the markers of a command as ${NAME}, so that the shell reads the secret from the environment.
// A command that is already sh -c keeps its form. Another command is wrapped in sh -c. keepArgs is for an
// entrypoint: the command that follows is passed on. It puts the names of the markers in passed.
func shellMarkers(cmd []string, genList []string, passed map[string]bool, keepArgs bool) []string {
	if !hasMarker(cmd) {
		return cmd
	}
	shell := func(s string) string {
		for _, g := range genList {
			if strings.Contains(s, genMarker(g)) {
				s = strings.ReplaceAll(s, genMarker(g), "${"+g+"}")
				passed[g] = true
			}
		}
		return s
	}
	if len(cmd) >= 3 && (cmd[0] == "sh" || cmd[0] == "/bin/sh" || cmd[0] == "bash" || cmd[0] == "/bin/bash") && cmd[1] == "-c" {
		out := append([]string{}, cmd...)
		for i := 2; i < len(out); i++ {
			out[i] = shell(out[i])
		}
		return out
	}
	var parts []string
	for _, a := range cmd {
		if strings.Contains(a, genMarkerPrefix) {
			q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(a)
			for _, g := range genList {
				q = strings.ReplaceAll(q, genMarker(g), `${`+g+`}`)
				if strings.Contains(a, genMarker(g)) {
					passed[g] = true
				}
			}
			parts = append(parts, `"`+q+`"`)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	script := "exec " + strings.Join(parts, " ")
	if keepArgs {
		return []string{"sh", "-c", script + ` "$@"`, "sh"}
	}
	return []string{"sh", "-c", script}
}

// markerElsewhere finds a marker in a setting of a service that cannot hold a secret. It gives the setting and the
// name of the variable.
func markerElsewhere(e ServiceEntry, genList []string) (string, string) {
	plain := e
	plain.Env, plain.Command, plain.Entrypoint = nil, nil, nil
	if hc := e.Healthcheck; hc != nil {
		h := map[string]any{}
		for k, v := range hc {
			if k != "command" {
				h[k] = v
			}
		}
		plain.Healthcheck = h
	}
	raw, err := json.Marshal(plain)
	if err != nil {
		return "", ""
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return "", ""
	}
	for _, k := range sortedKeys(top) {
		if s := string(top[k]); strings.Contains(s, genMarkerPrefix) {
			for _, g := range genList {
				if strings.Contains(s, genMarker(g)) {
					return k, g
				}
			}
			return k, "of the file"
		}
	}
	return "", ""
}
