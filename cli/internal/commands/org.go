// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"io"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

func (a *app) orgCommands() []*cobra.Command {
	return []*cobra.Command{a.orgCmd(), a.memberCmd(), a.teamCmd(), a.ssoCmd(), a.auditCmd()}
}

func (a *app) orgCmd() *cobra.Command {
	get := a.cmd("get", "Show the organization", "getOrg", noArgs, func(cmd *cobra.Command, _ []string) error {
		r, err := a.call("getOrg", ids())
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	set := a.cmd("set key=value...", "Change organization settings, for example name=Acme settings.require_2fa=true", "updateOrg", minArgs(1), func(cmd *cobra.Command, args []string) error {
		body := map[string]any{}
		for _, kv := range args {
			k, v, ok := cutKV(kv)
			if !ok {
				return usageErr("expected key=value, got %q", kv)
			}
			setPath(body, k, parseValue(v))
		}
		r, err := a.call("updateOrg", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm", "Delete the organization (soft delete, purged after 30 days)", "deleteOrg,getOrg", noArgs, func(cmd *cobra.Command, _ []string) error {
		v, err := a.getJSON("getOrg", ids())
		if err != nil {
			return err
		}
		m, _ := v.(map[string]any)
		slug := str(m, "slug")
		if err := a.confirm("Delete the organization", slug); err != nil {
			return err
		}
		if _, err := a.call("deleteOrg", apiops.Input{Query: url.Values{"confirm": {slug}}}); err != nil {
			return err
		}
		return a.done("organization deleted")
	})
	return group("org", "The organization", get, set, rm)
}

func cutKV(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], i > 0
		}
	}
	return "", "", false
}

func (a *app) memberCmd() *cobra.Command {
	var role string
	ls := a.listCmd("ls", "List members", "listMembers", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listMembers", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "email", "name", "role", "status", "source", "last_login_at")
	})
	invite := a.cmd("invite <email> --role R", "Invite a user. Prints the invite link", "inviteMember", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("inviteMember", apiops.Input{Body: jsonBody(map[string]any{"email": args[0], "role": role})})
		if err != nil {
			return err
		}
		return a.emitBody(r, "email", "role", "status", "invite_url")
	})
	invite.Flags().StringVar(&role, "role", "developer", "owner, admin, developer or viewer")
	setRole := a.cmd("role <email> <role>", "Change the role of a member", "updateMember", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.userID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("updateMember", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"role": args[1]})})
		if err != nil {
			return err
		}
		return a.emitBody(r, "email", "role", "status")
	})
	disable := a.cmd("disable <email>", "Disable a member", "updateMember", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.userID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("updateMember", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"disabled": true})})
		if err != nil {
			return err
		}
		return a.emitBody(r, "email", "role", "status")
	})
	rm := a.cmd("rm <email>", "Remove a member", "removeMember", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Remove member "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.userID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("removeMember", ids(id)); err != nil {
			return err
		}
		return a.done("removed " + args[0])
	})
	revoke := a.cmd("revoke-sessions <email>", "End all sessions of a member", "revokeMemberSessions", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.userID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("revokeMemberSessions", ids(id)); err != nil {
			return err
		}
		return a.done("sessions of " + args[0] + " revoked")
	})
	return group("member", "Members of the organization", ls, invite, setRole, disable, rm, revoke)
}

func (a *app) teamCmd() *cobra.Command {
	ls := a.listCmd("ls", "List teams", "listTeams", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listTeams", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "member_ids")
	})
	create := a.cmd("create <name>", "Create a team", "createTeam", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("createTeam", apiops.Input{Body: jsonBody(map[string]any{"name": args[0]})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rename := a.cmd("rename <team> <name>", "Rename a team", "updateTeam", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.teamID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("updateTeam", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"name": args[1]})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm <team>", "Delete a team", "deleteTeam", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete team "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.teamID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteTeam", ids(id)); err != nil {
			return err
		}
		return a.done("deleted team " + args[0])
	})
	add := a.cmd("add <team> <email>", "Add a user to a team", "addTeamMember", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		tid, err := a.teamID(args[0])
		if err != nil {
			return err
		}
		uid, err := a.userID(args[1])
		if err != nil {
			return err
		}
		if _, err := a.call("addTeamMember", ids(tid, uid)); err != nil {
			return err
		}
		return a.done("added " + args[1] + " to " + args[0])
	})
	remove := a.cmd("remove <team> <email>", "Remove a user from a team", "removeTeamMember", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		tid, err := a.teamID(args[0])
		if err != nil {
			return err
		}
		uid, err := a.userID(args[1])
		if err != nil {
			return err
		}
		if _, err := a.call("removeTeamMember", ids(tid, uid)); err != nil {
			return err
		}
		return a.done("removed " + args[1] + " from " + args[0])
	})
	return group("team", "Teams", ls, create, rename, rm, add, remove)
}

func (a *app) ssoCmd() *cobra.Command {
	var kind, issuer, clientID, defRole string
	var enabled bool
	cs := &secretSource{label: "client secret"}
	bf := &bodyFlags{}
	ls := a.listCmd("ls", "List SSO providers", "listSsoProviders", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listSsoProviders", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "kind", "issuer_url", "default_role", "enabled")
	})
	loginProv := a.listCmd("login-providers", "List the SSO providers on the login page (no login needed)", "listLoginProviders", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listLoginProviders", ids())
		if err != nil {
			return err
		}
		return a.emit(items, "id", "name", "kind")
	})
	body := func(cmd *cobra.Command, name string) (map[string]any, error) {
		fl := map[string]any{}
		if name != "" {
			fl["name"] = name
		}
		if cmd.Flags().Changed("kind") {
			fl["kind"] = kind
		}
		if issuer != "" {
			fl["issuer_url"] = issuer
		}
		if clientID != "" {
			fl["client_id"] = clientID
		}
		if defRole != "" {
			fl["default_role"] = defRole
		}
		if cmd.Flags().Changed("enabled") {
			fl["enabled"] = enabled
		}
		b, err := bf.build(a, fl)
		if err != nil {
			return nil, err
		}
		if cs.set() {
			v, err := a.readSecret(cs, true)
			if err != nil {
				return nil, err
			}
			b["client_secret"] = v
		}
		return b, nil
	}
	flags := func(c *cobra.Command) {
		bf.add(c)
		c.Flags().StringVar(&kind, "kind", "oidc", "oidc, google or github")
		c.Flags().StringVar(&issuer, "issuer-url", "", "OIDC issuer address")
		c.Flags().StringVar(&clientID, "client-id", "", "client id")
		c.Flags().StringVar(&defRole, "default-role", "", "role of new users")
		c.Flags().BoolVar(&enabled, "enabled", true, "turn the provider on or off")
		c.Flags().StringVar(&cs.file, "client-secret-file", "", "read the client secret from a file")
		c.Flags().BoolVar(&cs.stdin, "client-secret-stdin", false, "read the client secret from standard input")
	}
	add := a.cmd("add <name>", "Add an SSO provider", "createSsoProvider", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		b, err := body(cmd, args[0])
		if err != nil {
			return err
		}
		r, err := a.call("createSsoProvider", apiops.Input{Body: jsonBody(b)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	flags(add)
	update := a.cmd("update <provider>", "Change an SSO provider", "updateSsoProvider", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.ssoID(args[0])
		if err != nil {
			return err
		}
		b, err := body(cmd, "")
		if err != nil {
			return err
		}
		r, err := a.call("updateSsoProvider", apiops.Input{Path: []string{id}, Body: jsonBody(b)})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	flags(update)
	rm := a.cmd("rm <provider>", "Delete an SSO provider", "deleteSsoProvider", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete SSO provider "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.ssoID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteSsoProvider", ids(id)); err != nil {
			return err
		}
		return a.done("deleted " + args[0])
	})
	mappings := a.listCmd("mappings <provider>", "List group to role mappings", "listGroupMappings", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.ssoID(args[0])
		if err != nil {
			return err
		}
		items, err := a.list("listGroupMappings", ids(id))
		if err != nil {
			return err
		}
		return a.emit(items, "group_value", "role", "project_id")
	})
	var mapProject string
	mapCmd := a.cmd("map <provider> <group> <role>", "Map an identity provider group to a role", "listGroupMappings,replaceGroupMappings", exactArgs(3), func(cmd *cobra.Command, args []string) error {
		return a.changeMappings(args[0], args[1], args[2], mapProject, false)
	})
	mapCmd.Flags().StringVar(&mapProject, "project", "", "limit the role to one project")
	unmap := a.cmd("unmap <provider> <group>", "Remove the mapping of a group", "listGroupMappings,replaceGroupMappings", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		return a.changeMappings(args[0], args[1], "", "", true)
	})
	return group("sso", "SSO providers", ls, loginProv, add, update, rm, mappings, mapCmd, unmap)
}

func (a *app) changeMappings(provider, group, role, project string, remove bool) error {
	id, err := a.ssoID(provider)
	if err != nil {
		return err
	}
	items, err := a.list("listGroupMappings", ids(id))
	if err != nil {
		return err
	}
	out := []any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if str(m, "group_value") != group {
			out = append(out, it)
		}
	}
	if !remove {
		m := map[string]any{"group_value": group, "role": role}
		if project != "" {
			pid, err := a.projectID(project)
			if err != nil {
				return err
			}
			m["project_id"] = pid
		}
		out = append(out, m)
	}
	r, err := a.call("replaceGroupMappings", apiops.Input{Path: []string{id}, Body: jsonBody(out)})
	if err != nil {
		return err
	}
	return a.emitBody(r, "group_value", "role", "project_id")
}

func (a *app) auditCmd() *cobra.Command {
	var actor, action, since, target, targetType string
	var limit int
	query := func() (url.Values, error) {
		q := url.Values{}
		if actor != "" {
			q.Set("actor_id", actor)
		}
		if action != "" {
			q.Set("action", action)
		}
		if target != "" {
			q.Set("target_id", target)
		}
		if targetType != "" {
			q.Set("target_type", targetType)
		}
		if a.fl.project != "" {
			pid, err := a.projectID("")
			if err != nil {
				return nil, err
			}
			q.Set("project_id", pid)
		}
		if since != "" {
			s, err := sinceTime(since)
			if err != nil {
				return nil, err
			}
			q.Set("from", s)
		}
		return q, nil
	}
	ls := a.listCmd("ls", "Search the audit log", "listAuditEvents", noArgs, func(cmd *cobra.Command, _ []string) error {
		q, err := query()
		if err != nil {
			return err
		}
		if limit > 0 {
			q.Set("limit", itoa(limit))
		}
		items, err := a.list("listAuditEvents", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "seq", "created_at", "actor_type", "actor_id", "action", "target_type", "target_id")
	})
	ls.Flags().StringVar(&actor, "actor", "", "actor id")
	ls.Flags().StringVar(&action, "action", "", "action name")
	ls.Flags().StringVar(&since, "since", "", "only events newer than this, for example 7d")
	ls.Flags().StringVar(&target, "target", "", "target id")
	ls.Flags().StringVar(&targetType, "target-type", "", "target type")
	ls.Flags().IntVar(&limit, "limit", 0, "read only one page of this size")
	var expSince string
	var outFile string
	export := a.cmd("export", "Export audit events as JSON lines", "exportAuditEvents", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		if expSince != "" {
			s, err := sinceTime(expSince)
			if err != nil {
				return err
			}
			q.Set("from", s)
		}
		resp, err := a.stream("exportAuditEvents", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		var w io.Writer = a.out()
		if outFile != "" {
			f, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			w = f
		}
		_, err = io.Copy(w, resp.Body)
		return err
	})
	export.Flags().StringVar(&expSince, "since", "", "only events newer than this, for example 30d")
	export.Flags().StringVar(&outFile, "to-file", "", "write to a file instead of standard output")
	verify := a.cmd("verify", "Check the hash chain of the audit log", "verifyAuditChain", noArgs, func(cmd *cobra.Command, _ []string) error {
		r, err := a.call("verifyAuditChain", ids())
		if err != nil {
			return err
		}
		v, _ := decodeJSON(r.Body)
		if err := a.emit(v); err != nil {
			return err
		}
		if m, ok := v.(map[string]any); ok {
			if ok, _ := m["ok"].(bool); !ok {
				return failErr(ExitError, "the audit chain is broken")
			}
		}
		return nil
	})
	return group("audit", "Audit log", ls, export, verify)
}
