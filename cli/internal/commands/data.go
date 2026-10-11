// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

func (a *app) dataCommands() []*cobra.Command {
	return []*cobra.Command{a.varCmd(), a.secretCmd(), a.gitCmd()}
}

func (a *app) domainCmd() *cobra.Command {
	var port int
	var prefix, redirect, service, to string
	var noHTTPS bool
	bf := &bodyFlags{}
	cols := []string{"id", "host", "path_prefix", "container_port", "https", "tls_status"}
	ls := a.listCmd("ls <service>", "List domains of a service", "listDomains", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		items, err := a.list("listDomains", ids(id))
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	fields := func(cmd *cobra.Command) map[string]any {
		fl := map[string]any{}
		if cmd.Flags().Changed("port") {
			fl["container_port"] = port
		}
		if prefix != "" {
			fl["path_prefix"] = prefix
		}
		if redirect != "" {
			fl["redirect_www"] = redirect
		}
		if noHTTPS {
			fl["https"] = false
		}
		return fl
	}
	add := a.cmd("add <service> <host>", "Add a domain to a service", "createDomain", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		fl := fields(cmd)
		fl["host"] = args[1]
		body, err := bf.build(a, fl)
		if err != nil {
			return err
		}
		r, err := a.call("createDomain", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	bf.add(add)
	add.Flags().IntVar(&port, "port", 0, "container port")
	add.Flags().StringVar(&prefix, "path-prefix", "", "path prefix, default /")
	add.Flags().StringVar(&redirect, "redirect-www", "", "to_www or to_apex")
	add.Flags().BoolVar(&noHTTPS, "no-https", false, "serve plain HTTP")
	bfu := &bodyFlags{}
	update := a.cmd("update <host>", "Change a domain", "updateDomain", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		d, err := a.domainObj(args[0], service)
		if err != nil {
			return err
		}
		body, err := bfu.build(a, fields(cmd))
		if err != nil {
			return err
		}
		r, err := a.call("updateDomain", apiops.Input{Path: []string{str(d, "id")}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	bfu.add(update)
	update.Flags().IntVar(&port, "port", 0, "container port")
	update.Flags().StringVar(&prefix, "path-prefix", "", "path prefix")
	update.Flags().StringVar(&redirect, "redirect-www", "", "to_www or to_apex")
	update.Flags().BoolVar(&noHTTPS, "no-https", false, "serve plain HTTP")
	update.Flags().StringVar(&service, "service", "", "service that holds the domain")
	rm := a.cmd("rm <host>", "Remove a domain", "deleteDomain", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Remove domain "+args[0], args[0]); err != nil {
			return err
		}
		d, err := a.domainObj(args[0], service)
		if err != nil {
			return err
		}
		if _, err := a.call("deleteDomain", ids(str(d, "id"))); err != nil {
			return err
		}
		return a.done("removed domain " + args[0])
	})
	rm.Flags().StringVar(&service, "service", "", "service that holds the domain")
	move := a.cmd("move <host> --to <service>", "Move a domain to another service", "moveDomain", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if to == "" {
			return usageErr("--to is needed")
		}
		d, err := a.domainObj(args[0], service)
		if err != nil {
			return err
		}
		sid, err := a.serviceID(to)
		if err != nil {
			return err
		}
		r, err := a.call("moveDomain", apiops.Input{Path: []string{str(d, "id")}, Body: jsonBody(map[string]any{"target_service_id": sid})})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	move.Flags().StringVar(&to, "to", "", "target service")
	move.Flags().StringVar(&service, "service", "", "service that holds the domain now")
	return group("domain", "Domains", ls, add, update, rm, move)
}

func (a *app) volumeCmd() *cobra.Command {
	var size int64
	var mode, kind, service string
	var delData bool
	cols := []string{"id", "name", "kind", "mount_path", "node_id", "access_mode", "usage_bytes"}
	ls := a.listCmd("ls <service>", "List volumes of a service", "listVolumes", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		items, err := a.list("listVolumes", ids(id))
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	add := a.cmd("add <service> <name> <path>", "Add a volume", "createVolume", exactArgs(3), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[1], "mount_path": args[2]}
		if kind != "" {
			body["kind"] = kind
		}
		if mode != "" {
			body["access_mode"] = mode
		}
		if size > 0 {
			body["size_limit_bytes"] = size
		}
		r, err := a.call("createVolume", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	add.Flags().StringVar(&kind, "kind", "", "volume, bind or tmpfs")
	add.Flags().StringVar(&mode, "access-mode", "", "single_writer or shared")
	add.Flags().Int64Var(&size, "size-limit", 0, "size limit in bytes")
	update := a.cmd("update <volume>", "Change mount path, size limit or access mode", "updateVolume", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		v, err := a.volumeObj(args[0], service)
		if err != nil {
			return err
		}
		body := map[string]any{}
		if mode != "" {
			body["access_mode"] = mode
		}
		if size > 0 {
			body["size_limit_bytes"] = size
		}
		if kind != "" {
			body["mount_path"] = kind
		}
		r, err := a.call("updateVolume", apiops.Input{Path: []string{str(v, "id")}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	update.Flags().StringVar(&service, "service", "", "service that holds the volume")
	update.Flags().StringVar(&mode, "access-mode", "", "single_writer or shared")
	update.Flags().Int64Var(&size, "size-limit", 0, "size limit in bytes")
	update.Flags().StringVar(&kind, "mount-path", "", "new mount path")
	rm := a.cmd("rm <volume>", "Unmount a volume (the data stays unless --delete-data)", "deleteVolume", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Remove volume "+args[0], args[0]); err != nil {
			return err
		}
		v, err := a.volumeObj(args[0], service)
		if err != nil {
			return err
		}
		q := url.Values{}
		if delData {
			q.Set("delete_data", "true")
		}
		if _, err := a.call("deleteVolume", apiops.Input{Path: []string{str(v, "id")}, Query: q}); err != nil {
			return err
		}
		return a.done("removed volume " + args[0])
	})
	rm.Flags().StringVar(&service, "service", "", "service that holds the volume (when the volume is given by name)")
	rm.Flags().BoolVar(&delData, "delete-data", false, "also delete the data")
	return group("volume", "Volumes", ls, add, update, rm)
}

// sameScope tells if an item has exactly this scope.
func sameScope(m map[string]any, q url.Values) bool {
	return str(m, "environment_id") == q.Get("environment_id") && str(m, "service_id") == q.Get("service_id")
}

func (a *app) varCmd() *cobra.Command {
	var service string
	var preview, build bool
	scoped := func(c *cobra.Command) { c.Flags().StringVar(&service, "service", "", "limit to one service") }
	ls := a.listCmd("ls", "List variables", "listVariables", noArgs, func(cmd *cobra.Command, _ []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return err
		}
		items, err := a.list("listVariables", apiops.Input{Path: []string{pid}, Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "value", "environment_id", "service_id", "preview", "build_time", "git_managed")
	})
	scoped(ls)
	upsert := func(pid string, q url.Values, name, value string) (string, error) {
		items, err := a.list("listVariables", apiops.Input{Path: []string{pid}, Query: q})
		if err != nil {
			return "", err
		}
		body := map[string]any{"name": name, "value": value, "preview": preview, "build_time": build}
		for k := range q {
			body[k] = q.Get(k)
		}
		for _, it := range items {
			m, _ := it.(map[string]any)
			if str(m, "name") == name && sameScope(m, q) {
				_, err := a.call("updateVariable", apiops.Input{Path: []string{str(m, "id")}, Body: jsonBody(body)})
				return "updated " + name, err
			}
		}
		_, err = a.call("createVariable", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		return "created " + name, err
	}
	set := a.cmd("set NAME=VALUE...", "Create or change variables (plain values, never secrets)", "listVariables,createVariable,updateVariable", minArgs(1), func(cmd *cobra.Command, args []string) error {
		pairs, err := kvPairs(args)
		if err != nil {
			return err
		}
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return err
		}
		for _, arg := range args {
			name, _, _ := cutKV(arg)
			msg, err := upsert(pid, q, name, pairs[name])
			if err != nil {
				return err
			}
			if err := a.done(msg); err != nil {
				return err
			}
		}
		return nil
	})
	scoped(set)
	set.Flags().BoolVar(&preview, "preview", false, "also use in preview environments")
	set.Flags().BoolVar(&build, "build", false, "use at build time")
	rm := a.cmd("rm NAME", "Delete a variable", "listVariables,deleteVariable", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return err
		}
		items, err := a.list("listVariables", apiops.Input{Path: []string{pid}, Query: q})
		if err != nil {
			return err
		}
		for _, it := range items {
			m, _ := it.(map[string]any)
			if str(m, "name") == args[0] && sameScope(m, q) {
				if err := a.confirm("Delete variable "+args[0], args[0]); err != nil {
					return err
				}
				if _, err := a.call("deleteVariable", ids(str(m, "id"))); err != nil {
					return err
				}
				return a.done("deleted " + args[0])
			}
		}
		return notFoundErr("variable %q not found in this scope", args[0])
	})
	scoped(rm)
	var secretFlag bool
	imp := a.cmd("import <file.env>", "Import variables from a .env file", "bulkSetVariables", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		f, err := os.Open(args[0])
		if err != nil {
			return notFoundErr("cannot read %s: %v", args[0], err)
		}
		defer func() { _ = f.Close() }()
		var items []any
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := cutKV(line)
			if !ok {
				return usageErr("line %q is not NAME=VALUE", k)
			}
			v = strings.Trim(v, `"'`)
			items = append(items, map[string]any{"name": strings.TrimSpace(k), "value": v, "secret": secretFlag, "preview": preview, "build_time": build})
		}
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return err
		}
		body := map[string]any{"items": items}
		for k := range q {
			body[k] = q.Get(k)
		}
		r, err := a.call("bulkSetVariables", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	scoped(imp)
	imp.Flags().BoolVar(&secretFlag, "secrets", false, "store the values as secrets (the file holds the values)")
	imp.Flags().BoolVar(&preview, "preview", false, "also use in preview environments")
	imp.Flags().BoolVar(&build, "build", false, "use at build time")
	return group("var", "Variables", ls, set, rm, imp)
}

func (a *app) secretCmd() *cobra.Command {
	var service string
	var preview, build bool
	scoped := func(c *cobra.Command) { c.Flags().StringVar(&service, "service", "", "limit to one service") }
	find := func(name string) (url.Values, string, map[string]any, error) {
		pid, err := a.projectID("")
		if err != nil {
			return nil, "", nil, err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return nil, "", nil, err
		}
		items, err := a.list("listSecrets", apiops.Input{Path: []string{pid}, Query: q})
		if err != nil {
			return nil, "", nil, err
		}
		for _, it := range items {
			m, _ := it.(map[string]any)
			if str(m, "name") == name && sameScope(m, q) {
				return q, pid, m, nil
			}
		}
		return q, pid, nil, nil
	}
	mustFind := func(name string) (map[string]any, error) {
		_, _, m, err := find(name)
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, notFoundErr("secret %q not found in this scope", name)
		}
		return m, nil
	}
	ls := a.listCmd("ls", "List secrets (names and versions, never values)", "listSecrets", noArgs, func(cmd *cobra.Command, _ []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		q, err := a.scopeQuery(service)
		if err != nil {
			return err
		}
		items, err := a.list("listSecrets", apiops.Input{Path: []string{pid}, Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "current_version", "environment_id", "service_id", "preview", "build_time", "updated_at")
	})
	scoped(ls)
	src := &secretSource{label: "secret value"}
	set := a.cmd("set NAME", "Create or change a secret. The value comes from a prompt, stdin or a file, never from arguments", "listSecrets,createSecret,setSecretValue", func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usageErr("secret set needs the NAME only. The value is read from stdin, --from-file or a prompt")
		}
		if strings.Contains(args[0], "=") {
			return usageErr("secret values are never read from arguments: use `secret set NAME` and give the value on stdin, with --from-file or at the prompt")
		}
		return nil
	}, func(cmd *cobra.Command, args []string) error {
		value, err := a.readSecret(src, true)
		if err != nil {
			return err
		}
		q, pid, cur, err := find(args[0])
		if err != nil {
			return err
		}
		if cur != nil {
			body := map[string]any{"value": value}
			if cmd.Flags().Changed("preview") {
				body["preview"] = preview
			}
			if cmd.Flags().Changed("build") {
				body["build_time"] = build
			}
			r, err := a.call("setSecretValue", apiops.Input{Path: []string{str(cur, "id")}, Body: jsonBody(body)})
			if err != nil {
				return err
			}
			return a.emitBody(r, "id", "name", "current_version", "updated_at")
		}
		body := map[string]any{"name": args[0], "value": value, "preview": preview, "build_time": build}
		for k := range q {
			body[k] = q.Get(k)
		}
		r, err := a.call("createSecret", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, "id", "name", "current_version", "updated_at")
	})
	src.file = ""
	set.Flags().StringVar(&src.file, "from-file", "", "read the value from a file")
	set.Flags().BoolVar(&src.stdin, "stdin", false, "read the value from standard input")
	scoped(set)
	set.Flags().BoolVar(&preview, "preview", false, "also use in preview environments")
	set.Flags().BoolVar(&build, "build", false, "use at build time")
	versions := a.listCmd("versions NAME", "List the versions of a secret", "listSecrets,listSecretVersions", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		m, err := mustFind(args[0])
		if err != nil {
			return err
		}
		items, err := a.list("listSecretVersions", ids(str(m, "id")))
		if err != nil {
			return err
		}
		return a.emit(items, "version", "created_by", "created_at", "destroyed", "used_by_releases")
	})
	scoped(versions)
	restore := a.cmd("restore NAME <version>", "Make an earlier version current (as a new version)", "listSecrets,restoreSecretVersion", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		m, err := mustFind(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("restoreSecretVersion", ids(str(m, "id"), args[1]))
		if err != nil {
			return err
		}
		return a.emitBody(r, "id", "name", "current_version", "updated_at")
	})
	scoped(restore)
	rm := a.cmd("rm NAME", "Delete a secret", "listSecrets,deleteSecret", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		m, err := mustFind(args[0])
		if err != nil {
			return err
		}
		if err := a.confirm("Delete secret "+args[0], args[0]); err != nil {
			return err
		}
		if _, err := a.call("deleteSecret", ids(str(m, "id"))); err != nil {
			return err
		}
		return a.done("deleted secret " + args[0])
	})
	scoped(rm)
	destroy := a.cmd("destroy NAME <version>", "Destroy an old version for good", "listSecrets,destroySecretVersion", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		m, err := mustFind(args[0])
		if err != nil {
			return err
		}
		if err := a.confirm("Destroy version "+args[1]+" of secret "+args[0], args[0]); err != nil {
			return err
		}
		if _, err := a.call("destroySecretVersion", ids(str(m, "id"), args[1])); err != nil {
			return err
		}
		return a.done("destroyed version " + args[1])
	})
	scoped(destroy)
	return group("secret", "Secrets (values are never printed)", ls, set, versions, restore, rm, destroy)
}

func (a *app) gitCmd() *cobra.Command {
	var provider, name, baseURL, appID, instID, q string
	tok := addSecretFlagsNone("token", "access token")
	key := addSecretFlagsNone("private-key", "private key")
	whs := addSecretFlagsNone("webhook-secret", "webhook secret")
	ls := a.listCmd("ls", "List Git connections", "listGitConnections", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listGitConnections", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "provider", "base_url", "webhook_url")
	})
	add := a.cmd("add <name> --provider P", "Add a Git connection", "createGitConnection", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if provider == "" {
			return usageErr("--provider is needed: github, gitlab, gitea or generic")
		}
		body := map[string]any{"provider": provider, "name": args[0]}
		if baseURL != "" {
			body["base_url"] = baseURL
		}
		if appID != "" {
			body["app_id"] = appID
		}
		if instID != "" {
			body["installation_id"] = instID
		}
		for field, s := range map[string]*secretSource{"token": tok, "private_key": key, "webhook_secret": whs} {
			if !s.set() && (field != "token" || provider == "github") {
				continue
			}
			v, err := a.readSecret(s, field != "webhook_secret")
			if err != nil {
				return err
			}
			if v != "" {
				body[field] = v
			}
		}
		r, err := a.call("createGitConnection", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		a.info("If a webhook secret is shown, set it at the Git provider now. It is shown once.")
		return a.emitBody(r)
	})
	add.Flags().StringVar(&provider, "provider", "", "github, gitlab, gitea or generic")
	add.Flags().StringVar(&baseURL, "base-url", "", "address of a self-hosted provider")
	add.Flags().StringVar(&appID, "app-id", "", "GitHub App id")
	add.Flags().StringVar(&instID, "installation-id", "", "GitHub App installation id")
	registerSecret(add, tok, "token")
	registerSecret(add, key, "private-key")
	registerSecret(add, whs, "webhook-secret")
	_ = name
	rm := a.cmd("rm <connection>", "Delete a Git connection", "deleteGitConnection", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete Git connection "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.gitConnID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteGitConnection", ids(id)); err != nil {
			return err
		}
		return a.done("deleted " + args[0])
	})
	repos := a.listCmd("repos <connection>", "List repositories reachable by a connection", "listGitRepos", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.gitConnID(args[0])
		if err != nil {
			return err
		}
		qv := url.Values{}
		if q != "" {
			qv.Set("q", q)
		}
		items, err := a.list("listGitRepos", apiops.Input{Path: []string{id}, Query: qv})
		if err != nil {
			return err
		}
		return a.emit(items, "full_name", "default_branch", "private")
	})
	repos.Flags().StringVar(&q, "search", "", "filter by text")
	return group("git", "Git connections", ls, add, rm, repos)
}

func addSecretFlagsNone(_ string, label string) *secretSource { return &secretSource{label: label} }

func registerSecret(cmd *cobra.Command, s *secretSource, prefix string) {
	cmd.Flags().StringVar(&s.file, prefix+"-file", "", "read the "+s.label+" from a file")
	cmd.Flags().BoolVar(&s.stdin, prefix+"-stdin", false, "read the "+s.label+" from standard input")
}
