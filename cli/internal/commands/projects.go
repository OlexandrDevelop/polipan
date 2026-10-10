// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

func (a *app) projectCommands() []*cobra.Command {
	return []*cobra.Command{a.projectCmd(), a.envCmd(), a.previewCmd()}
}

func (a *app) projectCmd() *cobra.Command {
	var slug string
	var noEnvs bool
	cols := []string{"id", "name", "slug", "config_sync_state", "created_at"}
	ls := a.listCmd("ls", "List projects", "listProjects", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listProjects", ids())
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	create := a.cmd("create <name>", "Create a project", "createProject", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"name": args[0], "default_environments": !noEnvs}
		if slug != "" {
			body["slug"] = slug
		}
		r, err := a.call("createProject", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	create.Flags().StringVar(&slug, "slug", "", "slug (default: made from the name)")
	create.Flags().BoolVar(&noEnvs, "no-default-envs", false, "do not create production and staging")
	get := a.cmd("get [project]", "Show a project", "getProject", rangeArgs(0, 1), func(cmd *cobra.Command, args []string) error {
		ref := ""
		if len(args) > 0 {
			ref = args[0]
		}
		id, err := a.projectID(ref)
		if err != nil {
			return err
		}
		r, err := a.call("getProject", ids(id))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	set := a.cmd("set <project> key=value...", "Change project settings, for example settings.retain_releases=20", "updateProject", minArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.projectID(args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		for _, kv := range args[1:] {
			k, v, ok := cutKV(kv)
			if !ok {
				return usageErr("expected key=value, got %q", kv)
			}
			setPath(body, k, parseValue(v))
		}
		r, err := a.call("updateProject", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm <project>", "Delete a project (soft delete, purged after 30 days)", "deleteProject", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		m, err := a.projectObj(args[0])
		if err != nil {
			return err
		}
		slug := str(m, "slug")
		if slug == "" {
			slug = args[0]
		}
		if err := a.confirm("Delete project "+args[0], slug); err != nil {
			return err
		}
		if _, err := a.call("deleteProject", apiops.Input{Path: []string{str(m, "id")}, Query: url.Values{"confirm": {slug}}}); err != nil {
			return err
		}
		return a.done("deleted project " + args[0])
	})
	roles := a.listCmd("roles [project]", "List project role overrides", "listProjectRoles", rangeArgs(0, 1), func(cmd *cobra.Command, args []string) error {
		ref := ""
		if len(args) > 0 {
			ref = args[0]
		}
		id, err := a.projectID(ref)
		if err != nil {
			return err
		}
		items, err := a.list("listProjectRoles", ids(id))
		if err != nil {
			return err
		}
		return a.emit(items, "user_id", "team_id", "role")
	})
	role := a.cmd("role <project> <email|team> <admin|developer|viewer|none>", "Set the role of a user or team in a project", "listProjectRoles,replaceProjectRoles", exactArgs(3), func(cmd *cobra.Command, args []string) error {
		pid, err := a.projectID(args[0])
		if err != nil {
			return err
		}
		field, id := "user_id", ""
		if tid, terr := a.teamID(args[1]); terr == nil && !containsAt(args[1]) {
			field, id = "team_id", tid
		} else if id, err = a.userID(args[1]); err != nil {
			return err
		}
		items, err := a.list("listProjectRoles", ids(pid))
		if err != nil {
			return err
		}
		out := []any{}
		for _, it := range items {
			if m, _ := it.(map[string]any); str(m, field) != id {
				out = append(out, it)
			}
		}
		if args[2] != "none" {
			out = append(out, map[string]any{field: id, "role": args[2]})
		}
		r, err := a.call("replaceProjectRoles", apiops.Input{Path: []string{pid}, Body: jsonBody(out)})
		if err != nil {
			return err
		}
		return a.emitBody(r, "user_id", "team_id", "role")
	})
	return group("project", "Projects", ls, create, get, set, rm, roles, role)
}

func containsAt(s string) bool {
	for _, c := range s {
		if c == '@' {
			return true
		}
	}
	return false
}

func (a *app) envCmd() *cobra.Command {
	var kind, from string
	var off, delVol, protectedFlag bool
	cols := []string{"id", "name", "slug", "kind", "protected"}
	ls := a.listCmd("ls", "List environments of the project", "listEnvironments", noArgs, func(cmd *cobra.Command, _ []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		items, err := a.list("listEnvironments", ids(pid))
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	create := a.cmd("create <name>", "Create an environment", "createEnvironment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[0], "kind": kind}
		if from != "" {
			o, err := a.envObj(from, "")
			if err != nil {
				return err
			}
			body["clone_from_environment_id"] = str(o, "id")
		}
		if protectedFlag {
			body["protected"] = true
		}
		r, err := a.call("createEnvironment", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	create.Flags().StringVar(&kind, "kind", "custom", "production, staging or custom")
	create.Flags().StringVar(&from, "from", "", "clone from this environment")
	create.Flags().BoolVar(&protectedFlag, "protected", false, "make the environment protected")
	get := a.cmd("get <env>", "Show an environment", "getEnvironment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		o, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		r, err := a.call("getEnvironment", ids(str(o, "id")))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm <env>", "Delete an environment and its services", "deleteEnvironment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete environment "+args[0], args[0]); err != nil {
			return err
		}
		o, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		q := url.Values{}
		if delVol {
			q.Set("delete_volumes", "true")
		}
		if _, err := a.call("deleteEnvironment", apiops.Input{Path: []string{str(o, "id")}, Query: q}); err != nil {
			return err
		}
		return a.done("deleted environment " + args[0])
	})
	rm.Flags().BoolVar(&delVol, "delete-volumes", false, "also delete the volumes")
	protect := a.cmd("protect <env>", "Protect an environment (--off to remove the protection)", "updateEnvironment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		o, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		r, err := a.call("updateEnvironment", apiops.Input{Path: []string{str(o, "id")}, Body: jsonBody(map[string]any{"protected": !off})})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	protect.Flags().BoolVar(&off, "off", false, "remove the protection")
	rename := a.cmd("rename <env> <new-name>", "Rename an environment", "updateEnvironment", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		o, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		r, err := a.call("updateEnvironment", apiops.Input{Path: []string{str(o, "id")}, Body: jsonBody(map[string]any{"name": args[1]})})
		if err != nil {
			return err
		}
		return a.emitBody(r, cols...)
	})
	return group("env", "Environments", ls, create, get, rm, protect, rename)
}

func (a *app) previewCmd() *cobra.Command {
	var repo, branch, base string
	var pr int
	ls := a.listCmd("ls", "List preview environments", "listPreviews", noArgs, func(cmd *cobra.Command, _ []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		items, err := a.list("listPreviews", ids(pid))
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "preview.repo", "preview.pr_number", "preview.state", "preview.expires_at", "preview.url")
	})
	create := a.cmd("create --repo R --pr N", "Create a preview environment by hand", "createPreview", noArgs, func(cmd *cobra.Command, _ []string) error {
		if repo == "" || (pr == 0 && branch == "") {
			return usageErr("--repo and --pr (or --branch) are needed")
		}
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		body := map[string]any{"repo": repo}
		if pr > 0 {
			body["pr_number"] = pr
		}
		if branch != "" {
			body["branch"] = branch
		}
		if base != "" {
			o, err := a.envObj(base, "")
			if err != nil {
				return err
			}
			body["base_environment_id"] = str(o, "id")
		}
		r, err := a.call("createPreview", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	create.Flags().StringVar(&repo, "repo", "", "repository, for example org/app")
	create.Flags().IntVar(&pr, "pr", 0, "pull request number")
	create.Flags().StringVar(&branch, "branch", "", "branch (instead of a pull request)")
	create.Flags().StringVar(&base, "base", "", "base environment")
	rm := a.cmd("rm <env>", "Delete a preview environment", "deleteEnvironment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete preview "+args[0], args[0]); err != nil {
			return err
		}
		o, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		if _, err := a.call("deleteEnvironment", apiops.Input{Path: []string{str(o, "id")}, Query: url.Values{"delete_volumes": {"true"}}}); err != nil {
			return err
		}
		return a.done("deleted preview " + args[0])
	})
	return group("preview", "Preview environments", ls, create, rm)
}
