// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

// body reads the request fields of a command: a file, flags and --set pairs.
func (a *app) send(op string, path []string, query url.Values, body map[string]any, cols ...string) error {
	in := apiops.Input{Path: path, Query: query}
	if body != nil {
		in.Body = jsonBody(body)
	}
	r, err := a.call(op, in)
	if err != nil {
		return err
	}
	return a.emitBody(r, cols...)
}

// readFileArg reads a file, or standard input for "-".
func (a *app) readFileArg(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(a.readStdin())
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, notFoundErr("cannot read %s: %v", name, err)
	}
	return b, nil
}

// ---- templates and compose ----

func (a *app) templateCommands() []*cobra.Command {
	var env string
	var sets []string
	var delVolumes bool
	ls := a.listCmd("ls", "List the templates", "listTemplates", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listTemplates", apiops.Input{})
		if err != nil {
			return err
		}
		return a.emit(items, "slug", "version", "name", "builtin", "description")
	})
	get := a.cmd("get <slug>", "Show a template and its inputs", "getTemplate", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.send("getTemplate", []string{args[0]}, nil, nil)
	})
	apply := a.cmd("apply <slug> --env E [--set k=v ...]", "Create the services of a template", "instantiateTemplate", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		eid, err := a.envID(env)
		if err != nil {
			return err
		}
		inputs := map[string]any{}
		kv, err := kvPairs(sets)
		if err != nil {
			return err
		}
		for k, v := range kv {
			inputs[k] = parseValue(v)
		}
		return a.send("instantiateTemplate", []string{args[0]}, nil, map[string]any{"environment_id": eid, "inputs": inputs})
	})
	apply.Flags().StringVar(&env, "env", "", "environment to create the services in")
	apply.Flags().StringArrayVar(&sets, "set", nil, "an input, k=v (the value is JSON when it can be)")
	add := a.cmd("add <file.yaml>", "Add a custom template", "createTemplate", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		raw, err := a.readFileArg(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("createTemplate", apiops.Input{ContentType: "application/yaml", Body: raw})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm <instance>", "Delete all services of a template instance", "deleteTemplateInstance", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete template instance "+args[0], args[0]); err != nil {
			return err
		}
		q := url.Values{"confirm": {args[0]}}
		if delVolumes {
			q.Set("delete_volumes", "true")
		}
		if _, err := a.call("deleteTemplateInstance", apiops.Input{Path: []string{args[0]}, Query: q}); err != nil {
			return err
		}
		return a.done("deleted instance " + args[0])
	})
	rm.Flags().BoolVar(&delVolumes, "delete-volumes", false, "also delete the volumes of the services")
	tpl := group("template", "Templates", ls, get, apply, add, rm)

	var dry bool
	imp := a.cmd("import <file> --env E", "Convert a Docker Compose file to services", "importCompose", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		eid, err := a.envID(env)
		if err != nil {
			return err
		}
		raw, err := a.readFileArg(args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if dry {
			q.Set("dry_run", "true")
		}
		r, err := a.call("importCompose", apiops.Input{Path: []string{eid}, Query: q, ContentType: "application/yaml", Body: raw})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	imp.Flags().StringVar(&env, "env", "", "environment to create the services in")
	imp.Flags().BoolVar(&dry, "dry-run", false, "show the result and create nothing")
	return []*cobra.Command{tpl, group("compose", "Docker Compose", imp)}
}

// ---- backups ----

func (a *app) backupCommands() []*cobra.Command {
	subs := []*cobra.Command{a.backupTargetCmd(), a.backupPolicyCmd()}
	return []*cobra.Command{group("backup", "Backups", append(subs, a.backupRunCmds()...)...)}
}

func (a *app) backupTargetCmd() *cobra.Command {
	bf := &bodyFlags{}
	var kind string
	cols := []string{"id", "name", "kind", "last_test_ok", "last_test_at"}
	ls := a.listCmd("ls", "List the backup targets", "listBackupTargets", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listBackupTargets", apiops.Input{})
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	add := a.cmd("add <name> --kind K", "Add a backup target. Credentials come from --credentials-file or --credentials-stdin", "createBackupTarget", exactArgs(1), nil)
	creds := addSecretFlags(add, "credentials", "credentials (a JSON object)")
	add.RunE = func(cmd *cobra.Command, args []string) error {
		a.ctx = cmd.Context()
		fl := map[string]any{"name": args[0]}
		if kind != "" {
			fl["kind"] = kind
		}
		body, err := bf.build(a, fl)
		if err != nil {
			return err
		}
		if creds.set() {
			v, err := a.readSecret(creds, true)
			if err != nil {
				return err
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(v), &obj); err != nil {
				return usageErr("the credentials must be a JSON object")
			}
			body["credentials"] = obj
		}
		return a.send("createBackupTarget", nil, nil, body, cols...)
	}
	bf.add(add)
	add.Flags().StringVar(&kind, "kind", "", "s3, b2, sftp, local, azure or gcs")
	bu := &bodyFlags{}
	update := a.cmd("update <target>", "Change a backup target", "updateBackupTarget", exactArgs(1), nil)
	ucreds := addSecretFlags(update, "credentials", "new credentials (a JSON object)")
	update.RunE = func(cmd *cobra.Command, args []string) error {
		a.ctx = cmd.Context()
		id, err := a.targetID(args[0])
		if err != nil {
			return err
		}
		body, err := bu.build(a, nil)
		if err != nil {
			return err
		}
		if ucreds.set() {
			v, err := a.readSecret(ucreds, true)
			if err != nil {
				return err
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(v), &obj); err != nil {
				return usageErr("the credentials must be a JSON object")
			}
			body["credentials"] = obj
		}
		return a.send("updateBackupTarget", []string{id}, nil, body, cols...)
	}
	bu.add(update)
	test := a.cmd("test <target>", "Write and read a small object on the target", "testBackupTarget", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.targetID(args[0])
		if err != nil {
			return err
		}
		return a.send("testBackupTarget", []string{id}, nil, nil)
	})
	rm := a.cmd("rm <target>", "Delete a backup target", "deleteBackupTarget", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete backup target "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.targetID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteBackupTarget", ids(id)); err != nil {
			return err
		}
		return a.done("deleted target " + args[0])
	})
	return group("target", "Backup targets", ls, add, update, test, rm)
}

func (a *app) backupPolicyCmd() *cobra.Command {
	var service string
	var targets []string
	var delSnaps bool
	cols := []string{"id", "service_id", "schedule", "target_ids", "health", "enabled"}
	ls := a.listCmd("ls [service]", "List backup policies (of one service, or all)", "listBackupPolicies,listAllBackupPolicies", rangeArgs(0, 1), func(cmd *cobra.Command, args []string) error {
		op, in := "listAllBackupPolicies", apiops.Input{}
		if len(args) == 1 {
			id, err := a.serviceID(args[0])
			if err != nil {
				return err
			}
			op, in = "listBackupPolicies", ids(id)
		}
		items, err := a.list(op, in)
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	get := a.cmd("get <policy>", "Show a backup policy with its health", "getBackupPolicy", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.send("getBackupPolicy", []string{args[0]}, nil, nil)
	})
	bf := &bodyFlags{}
	targetIDs := func() ([]string, error) {
		var out []string
		for _, t := range targets {
			id, err := a.targetID(t)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, nil
	}
	add := a.cmd("add <service> --target T [--schedule CRON]", "Create a backup policy", "createBackupPolicy", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		sid, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		fl := map[string]any{}
		if len(targets) > 0 {
			t, err := targetIDs()
			if err != nil {
				return err
			}
			fl["target_ids"] = t
		}
		body, err := bf.build(a, fl)
		if err != nil {
			return err
		}
		return a.send("createBackupPolicy", []string{sid}, nil, body, cols...)
	})
	bf.add(add)
	add.Flags().StringArrayVar(&targets, "target", nil, "backup target (repeat for more)")
	_ = service
	bu := &bodyFlags{}
	update := a.cmd("update <policy>", "Change a backup policy", "updateBackupPolicy", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		fl := map[string]any{}
		if len(targets) > 0 {
			t, err := targetIDs()
			if err != nil {
				return err
			}
			fl["target_ids"] = t
		}
		body, err := bu.build(a, fl)
		if err != nil {
			return err
		}
		return a.send("updateBackupPolicy", []string{args[0]}, nil, body, cols...)
	})
	bu.add(update)
	update.Flags().StringArrayVar(&targets, "target", nil, "backup target (repeat for more)")
	rm := a.cmd("rm <policy>", "Delete a backup policy", "deleteBackupPolicy", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete backup policy "+args[0], args[0]); err != nil {
			return err
		}
		q := url.Values{}
		if delSnaps {
			q.Set("delete_snapshots", "true")
		}
		if _, err := a.call("deleteBackupPolicy", apiops.Input{Path: []string{args[0]}, Query: q}); err != nil {
			return err
		}
		return a.done("deleted policy " + args[0])
	})
	rm.Flags().BoolVar(&delSnaps, "delete-snapshots", false, "also delete the snapshots on the targets")
	return group("policy", "Backup policies", ls, get, add, update, rm)
}

// backupRunCmds are run, ls, restore, restore-status and check.
func (a *app) backupRunCmds() []*cobra.Command {
	run := a.cmd("run <policy>", "Run a backup now", "runBackup", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.send("runBackup", []string{args[0]}, nil, nil)
	})
	ls := a.listCmd("ls <policy>", "List the backup runs of a policy", "listBackupRuns", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		items, err := a.list("listBackupRuns", ids(args[0]))
		if err != nil {
			return err
		}
		return a.emit(items, "id", "state", "started_at", "finished_at", "error")
	})
	var to, newName, target string
	restore := a.cmd("restore <run> --to <service>|--new <name>", "Restore a snapshot into the service, or into a new service", "restoreBackup", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if (to == "") == (newName == "") {
			return usageErr("give --to <service> (in place) or --new <name> (a copy), not both")
		}
		body := map[string]any{"mode": "in_place"}
		if newName != "" {
			body["mode"], body["new_service_name"] = "new_service", newName
		} else {
			sid, err := a.serviceID(to)
			if err != nil {
				return err
			}
			if err := a.confirm("Restore into the live service "+to+" (its data is replaced)", to); err != nil {
				return err
			}
			body["confirm"] = to
			_ = sid
		}
		if target != "" {
			id, err := a.targetID(target)
			if err != nil {
				return err
			}
			body["target_id"] = id
		}
		return a.send("restoreBackup", []string{args[0]}, nil, body)
	})
	restore.Flags().StringVar(&to, "to", "", "restore into this service (in place)")
	restore.Flags().StringVar(&newName, "new", "", "restore into a new service with this name")
	restore.Flags().StringVar(&target, "target", "", "the backup target that holds the snapshot")
	status := a.cmd("restore-status <id>", "Show a restore job", "getBackupRestore", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.send("getBackupRestore", []string{args[0]}, nil, nil)
	})
	var policy string
	checkRun := a.cmd("check <policy>", "Start a restore check now", "runRestoreCheck", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		return a.send("runRestoreCheck", []string{args[0]}, nil, nil)
	})
	checkLs := a.listCmd("checks", "List restore checks", "listRestoreChecks", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		if policy != "" {
			q.Set("policy_id", policy)
		}
		items, err := a.list("listRestoreChecks", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "policy_id", "state", "trigger", "duration_ms", "started_at")
	})
	checkLs.Flags().StringVar(&policy, "policy", "", "only this policy")
	return []*cobra.Command{run, ls, restore, status, checkRun, checkLs}
}

// ---- alerts ----

func (a *app) alertCommands() []*cobra.Command {
	return []*cobra.Command{a.alertCmd()}
}

func (a *app) alertCmd() *cobra.Command {
	var firing bool
	var mute, reason string
	chCols := []string{"id", "name", "kind", "enabled", "state"}
	ruleCols := []string{"id", "name", "type", "severity", "enabled", "muted_until"}
	chLs := a.listCmd("ls", "List alert channels", "listAlertChannels", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listAlertChannels", apiops.Input{})
		if err != nil {
			return err
		}
		return a.emit(items, chCols...)
	})
	cbf := &bodyFlags{}
	var kind string
	chAdd := a.cmd("add <name> --kind K", "Create an alert channel. The secret (webhook URL or token) comes from --secret-file or --secret-stdin", "createAlertChannel", exactArgs(1), nil)
	chSecret := addSecretFlags(chAdd, "secret", "channel secret")
	chAdd.RunE = func(cmd *cobra.Command, args []string) error {
		a.ctx = cmd.Context()
		fl := map[string]any{"name": args[0]}
		if kind != "" {
			fl["kind"] = kind
		}
		body, err := cbf.build(a, fl)
		if err != nil {
			return err
		}
		if chSecret.set() {
			v, err := a.readSecret(chSecret, true)
			if err != nil {
				return err
			}
			body["secret"] = v
		}
		return a.send("createAlertChannel", nil, nil, body, chCols...)
	}
	cbf.add(chAdd)
	chAdd.Flags().StringVar(&kind, "kind", "", "email, webhook, slack, discord, telegram or ntfy")
	cuf := &bodyFlags{}
	chUpdate := a.cmd("update <channel>", "Change an alert channel", "updateAlertChannel", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.channelID(args[0])
		if err != nil {
			return err
		}
		body, err := cuf.build(a, nil)
		if err != nil {
			return err
		}
		return a.send("updateAlertChannel", []string{id}, nil, body, chCols...)
	})
	cuf.add(chUpdate)
	chTest := a.cmd("test <channel>", "Send a test message", "testAlertChannel", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.channelID(args[0])
		if err != nil {
			return err
		}
		return a.send("testAlertChannel", []string{id}, nil, nil)
	})
	chRm := a.cmd("rm <channel>", "Delete an alert channel", "deleteAlertChannel", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete alert channel "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.channelID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteAlertChannel", ids(id)); err != nil {
			return err
		}
		return a.done("deleted channel " + args[0])
	})
	channel := group("channel", "Alert channels", chLs, chAdd, chUpdate, chTest, chRm)

	ruleLs := a.listCmd("ls", "List alert rules", "listAlertRules", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listAlertRules", apiops.Input{})
		if err != nil {
			return err
		}
		return a.emit(items, ruleCols...)
	})
	rbf := &bodyFlags{}
	var typ, severity string
	var channels []string
	chanIDs := func() ([]string, error) {
		var out []string
		for _, c := range channels {
			id, err := a.channelID(c)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, nil
	}
	ruleAdd := a.cmd("add <name> --type T --severity S --channel C", "Create an alert rule", "createAlertRule", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		fl := map[string]any{"name": args[0]}
		if typ != "" {
			fl["type"] = typ
		}
		if severity != "" {
			fl["severity"] = severity
		}
		if len(channels) > 0 {
			c, err := chanIDs()
			if err != nil {
				return err
			}
			fl["channel_ids"] = c
		}
		body, err := rbf.build(a, fl)
		if err != nil {
			return err
		}
		return a.send("createAlertRule", nil, nil, body, ruleCols...)
	})
	rbf.add(ruleAdd)
	ruleAdd.Flags().StringVar(&typ, "type", "", "rule type, for example node_down or deploy_failed")
	ruleAdd.Flags().StringVar(&severity, "severity", "", "info, warning or critical")
	ruleAdd.Flags().StringArrayVar(&channels, "channel", nil, "alert channel (repeat for more)")
	ruf := &bodyFlags{}
	ruleUpdate := a.cmd("update <rule>", "Change an alert rule", "updateAlertRule", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.ruleID(args[0])
		if err != nil {
			return err
		}
		body, err := ruf.build(a, nil)
		if err != nil {
			return err
		}
		return a.send("updateAlertRule", []string{id}, nil, body, ruleCols...)
	})
	ruf.add(ruleUpdate)
	ruleRm := a.cmd("rm <rule>", "Delete an alert rule", "deleteAlertRule", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete alert rule "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.ruleID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteAlertRule", ids(id)); err != nil {
			return err
		}
		return a.done("deleted rule " + args[0])
	})
	ruleMute := a.cmd("mute <rule> --for 2h --reason TEXT", "Mute a rule for a while", "muteAlertRule", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		d, err := parseDuration(mute)
		if err != nil {
			return err
		}
		if d <= 0 || strings.TrimSpace(reason) == "" {
			return usageErr("--for and --reason are needed")
		}
		id, err := a.ruleID(args[0])
		if err != nil {
			return err
		}
		return a.send("muteAlertRule", []string{id}, nil, map[string]any{"until": time.Now().Add(d).UTC().Format(time.RFC3339), "reason": reason})
	})
	ruleMute.Flags().StringVar(&mute, "for", "", "how long, for example 2h or 1d")
	ruleMute.Flags().StringVar(&reason, "reason", "", "why (it is written to the audit log)")
	rule := group("rule", "Alert rules", ruleLs, ruleAdd, ruleUpdate, ruleRm, ruleMute)

	var state string
	ls := a.listCmd("ls [--firing]", "List firing and recently resolved alerts", "listAlertInstances", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		switch {
		case firing:
			q.Set("state", "firing")
		case state != "":
			q.Set("state", state)
		}
		items, err := a.list("listAlertInstances", apiops.Input{Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "rule_id", "state", "first_fired_at", "resolved_at")
	})
	ls.Flags().BoolVar(&firing, "firing", false, "only alerts that fire now")
	ls.Flags().StringVar(&state, "state", "", "firing or resolved")
	root := group("alert", "Alerts", channel, rule)
	root.AddCommand(ls)
	return root
}

// ---- single sign-on ----

// ssoLoginCmd prints the address that starts the browser login of a provider. The two SSO operations are browser
// redirects: the CLI cannot follow them, it hands the address to the user.
func (a *app) ssoLoginCmd() *cobra.Command {
	return a.cmd("sso-login <provider>", "Print the address that starts the browser login with an identity provider", "startSsoLogin,finishSsoLogin", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.ssoID(args[0])
		if err != nil {
			return err
		}
		e, err := a.effective()
		if err != nil {
			return err
		}
		return a.done(strings.TrimRight(e.URL, "/") + "/api/v1/auth/sso/" + id + "/start")
	})
}
