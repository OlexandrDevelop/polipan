// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

// waitFlags are --wait, --follow and --timeout of the commands that start a deployment.
type waitFlags struct {
	wait, follow bool
	timeout      time.Duration
}

func (w *waitFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&w.wait, "wait", false, "wait for the deployment to end. Exit code 5 when it failed or rolled back, 6 on timeout")
	cmd.Flags().BoolVar(&w.follow, "follow", false, "print state changes and log lines until the deployment ends (implies --wait)")
	cmd.Flags().DurationVar(&w.timeout, "timeout", 30*time.Minute, "how long --wait and --follow wait")
}

func isFinal(state string) bool {
	switch state {
	case "live", "failed", "rolled_back", "cancelled", "superseded":
		return true
	}
	return false
}

// deploymentIDs finds the ids in an answer: one deployment, a list, or {deployments: [...]}.
func deploymentIDs(v any) []string {
	var out []string
	var add func(x any)
	add = func(x any) {
		switch t := x.(type) {
		case []any:
			for _, e := range t {
				add(e)
			}
		case map[string]any:
			if d, ok := t["deployments"]; ok {
				add(d)
				return
			}
			if _, ok := t["state"]; ok {
				if id := str(t, "id"); id != "" {
					out = append(out, id)
				}
			}
		}
	}
	add(v)
	return out
}

// finish prints the answer of a command that starts deployments. With --wait or --follow it waits for them.
func (a *app) finish(w *waitFlags, r *Response) error {
	v, err := decodeJSON(r.Body)
	if err != nil {
		return err
	}
	if v != nil {
		if err := a.emit(v, "id", "service_id", "state", "strategy", "trigger", "started_at"); err != nil {
			return err
		}
	}
	if !w.wait && !w.follow {
		return nil
	}
	code := 0
	for _, id := range deploymentIDs(v) {
		state, err := a.watchDeployment(id, w.follow, w.timeout)
		if err != nil {
			return err
		}
		if c := stateExit(state); c > code {
			code = c
		}
	}
	if code != 0 {
		return failErr(code, "deployment did not finish well")
	}
	return nil
}

func stateExit(state string) int {
	switch state {
	case "live":
		return ExitOK
	case "failed", "rolled_back":
		return ExitDeployFailed
	}
	return ExitError
}

// watchDeployment follows a deployment to a final state and returns that state.
func (a *app) watchDeployment(id string, follow bool, timeout time.Duration) (string, error) {
	parent := a.context()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	a.ctx = ctx
	defer func() { a.ctx = parent }()
	timedOut := func() error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failErr(ExitTimeout, "timeout: deployment %s did not finish in %s", id, timeout)
		}
		return nil
	}
	if follow {
		if err := a.followDeployment(ctx, id); err != nil {
			if te := timedOut(); te != nil {
				return "", te
			}
			a.info("event stream ended (%v): checking the state", err)
		}
	}
	for {
		v, err := a.getJSON("getDeployment", ids(id))
		if err != nil {
			if te := timedOut(); te != nil {
				return "", te
			}
			return "", err
		}
		m, _ := v.(map[string]any)
		state := str(m, "state")
		if isFinal(state) {
			msg := fmt.Sprintf("deployment %s: %s", id, state)
			if reason := str(m, "failure_reason"); reason != "" {
				msg += " (" + reason + ")"
			}
			a.info("%s", msg)
			return state, nil
		}
		select {
		case <-ctx.Done():
			return "", timedOut()
		case <-time.After(a.opts.PollInterval):
		}
	}
}

// followDeployment prints the events of the stream until it ends.
func (a *app) followDeployment(ctx context.Context, id string) error {
	resp, err := a.stream("streamDeploymentEvents", ids(id))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return readSSE(resp.Body, func(data string) {
		v, err := decodeJSON([]byte(data))
		m, ok := v.(map[string]any)
		if err != nil || !ok {
			_, _ = fmt.Fprintln(a.err(), data)
			return
		}
		d, _ := m["data"].(map[string]any)
		switch str(m, "type") {
		case "state":
			_, _ = fmt.Fprintf(a.err(), "state: %s\n", str(m, "state"))
		case "log":
			line := str(d, "line")
			if line == "" {
				line = str(d, "message")
			}
			_, _ = fmt.Fprintln(a.out(), line)
		default:
			_, _ = fmt.Fprintf(a.err(), "%s: %s\n", str(m, "type"), data)
		}
	})
}

// readSSE reads a text/event-stream and calls fn with the data of each event.
func readSSE(r io.Reader, fn func(data string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				fn(strings.Join(data, "\n"))
				data = nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if len(data) > 0 {
		fn(strings.Join(data, "\n"))
	}
	return sc.Err()
}

func (a *app) deployCommands() []*cobra.Command {
	return []*cobra.Command{a.deployCmd(), a.deploymentCmd(), a.rollbackCmd(), a.promoteCmd()}
}

func (a *app) deployCmd() *cobra.Command {
	var commit, strategy, steps string
	var drain int
	var dry, noCache bool
	wf := &waitFlags{}
	c := a.cmd("deploy <service>", "Deploy a service", "deployService", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		if commit != "" {
			body["commit"] = commit
		}
		if strategy != "" {
			body["strategy"] = strategy
		}
		opts := map[string]any{}
		if cmd.Flags().Changed("drain") {
			opts["drain_seconds"] = drain
		}
		if steps != "" {
			var st []any
			for _, p := range strings.Split(steps, ",") {
				var n int
				if _, err := fmt.Sscan(p, &n); err != nil {
					return usageErr("--steps needs numbers, for example 10,25,50,100")
				}
				st = append(st, n)
			}
			opts["canary_steps"] = st
		}
		if len(opts) > 0 {
			body["strategy_options"] = opts
		}
		if dry {
			body["dry_run"] = true
		}
		if noCache {
			body["no_cache"] = true
		}
		r, err := a.call("deployService", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		if r.Status == 200 || dry {
			return a.emitBody(r)
		}
		return a.finish(wf, r)
	})
	c.Flags().StringVar(&commit, "commit", "", "Git commit to build")
	c.Flags().StringVar(&strategy, "strategy", "", "recreate, blue_green or canary")
	c.Flags().IntVar(&drain, "drain", 30, "drain time in seconds")
	c.Flags().StringVar(&steps, "steps", "", "canary steps in percent, for example 10,25,50,100")
	c.Flags().BoolVar(&dry, "dry-run", false, "show the plan only")
	c.Flags().BoolVar(&noCache, "no-cache", false, "build without cache")
	wf.add(c)
	return c
}

func (a *app) deploymentCmd() *cobra.Command {
	var limit int
	cols := []string{"id", "service_id", "state", "strategy", "trigger", "started_at", "finished_at"}
	ls := a.listCmd("ls <service>", "List deployments of a service", "listServiceDeployments", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if limit > 0 {
			q.Set("limit", itoa(limit))
		}
		items, err := a.list("listServiceDeployments", apiops.Input{Path: []string{id}, Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, cols...)
	})
	ls.Flags().IntVar(&limit, "limit", 0, "read only one page of this size")
	get := a.cmd("get <id>", "Show a deployment with its steps", "getDeployment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("getDeployment", ids(args[0]))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	act := func(use, short, op string) *cobra.Command {
		return a.cmd(use+" <id>", short, op, exactArgs(1), func(cmd *cobra.Command, args []string) error {
			r, err := a.call(op, ids(args[0]))
			if err != nil {
				return err
			}
			return a.emitBody(r, cols...)
		})
	}
	var after int
	logs := a.cmd("logs <id>", "Show build and pre_deploy log lines", "getDeploymentLogs", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		q := url.Values{}
		if after > 0 {
			q.Set("after_seq", itoa(after))
		}
		items, err := a.list("getDeploymentLogs", apiops.Input{Path: []string{args[0]}, Query: q})
		if err != nil {
			return err
		}
		if f, _ := a.format(); f == "table" {
			for _, it := range items {
				m, _ := it.(map[string]any)
				_, _ = fmt.Fprintln(a.out(), str(m, "line"))
			}
			return nil
		}
		return a.emit(items)
	})
	logs.Flags().IntVar(&after, "after-seq", 0, "only lines after this sequence number")
	watch := a.cmd("watch <id>", "Follow a deployment until it ends", "streamDeploymentEvents,getDeployment", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		state, err := a.watchDeployment(args[0], true, 30*time.Minute)
		if err != nil {
			return err
		}
		if c := stateExit(state); c != 0 {
			return failErr(c, "deployment ended as %s", state)
		}
		return nil
	})
	return group("deployment", "Deployments", ls, get, act("cancel", "Cancel a deployment", "cancelDeployment"),
		act("promote", "Skip the wait of the current canary step", "promoteCanary"), act("abort", "Abort a canary", "abortCanary"),
		act("pause", "Pause a canary", "pauseCanary"), act("resume", "Resume a canary", "resumeCanary"), logs, watch)
}

func (a *app) rollbackCmd() *cobra.Command {
	var strategy string
	var force bool
	wf := &waitFlags{}
	c := a.cmd("rollback <service> [release]", "Roll back to the previous release, or to a given release", "rollbackService,listReleases", rangeArgs(1, 2), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		var rel string
		if len(args) == 2 {
			rel, err = a.releaseID(id, args[1])
		} else {
			rel, err = a.previousRelease(id)
		}
		if err != nil {
			return err
		}
		body := map[string]any{"release_id": rel}
		if strategy != "" {
			body["strategy"] = strategy
		}
		if force {
			body["force"] = true
		}
		r, err := a.call("rollbackService", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.finish(wf, r)
	})
	c.Flags().StringVar(&strategy, "strategy", "", "recreate, blue_green or canary")
	c.Flags().BoolVar(&force, "force", false, "roll back even when a check says no")
	wf.add(c)
	return c
}

// releaseID resolves a release number (like 12 or #12) or an id of a service.
func (a *app) releaseID(serviceID, ref string) (string, error) {
	if strings.HasPrefix(ref, "rel_") {
		return ref, nil
	}
	items, err := a.list("listReleases", ids(serviceID))
	if err != nil {
		return "", err
	}
	want := strings.TrimPrefix(ref, "#")
	for _, it := range items {
		m, _ := it.(map[string]any)
		if fmt.Sprint(m["number"]) == want {
			return str(m, "id"), nil
		}
	}
	return "", notFoundErr("release %q not found", ref)
}

// previousRelease finds the release before the live one.
func (a *app) previousRelease(serviceID string) (string, error) {
	items, err := a.list("listReleases", ids(serviceID))
	if err != nil {
		return "", err
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if str(m, "status") == "previous" {
			return str(m, "id"), nil
		}
	}
	return "", notFoundErr("no previous release to roll back to")
}

func (a *app) promoteCmd() *cobra.Command {
	var service, strategy string
	var dry, createMissing, includeDB bool
	var allow []string
	wf := &waitFlags{}
	c := a.cmd("promote <from-env> <to-env>", "Promote the live releases of one environment to another", "promoteRelease,promoteEnvironment,listServices", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		from, err := a.envObj(args[0], "")
		if err != nil {
			return err
		}
		to, err := a.envObj(args[1], "")
		if err != nil {
			return err
		}
		body := map[string]any{"target_environment_id": str(to, "id")}
		if dry {
			body["dry_run"] = true
		}
		if len(allow) > 0 {
			body["allow_missing"] = allow
		}
		if createMissing {
			body["create_missing"] = true
		}
		if includeDB {
			body["include_databases"] = true
		}
		if strategy != "" {
			body["strategy"] = strategy
		}
		var r *Response
		if service == "" {
			r, err = a.call("promoteEnvironment", apiops.Input{Path: []string{str(from, "id")}, Body: jsonBody(body)})
		} else {
			sid, serr := a.serviceID(args[0] + "/" + service)
			if serr != nil {
				return serr
			}
			var rel string
			svc, serr := a.getJSON("getService", ids(sid))
			if serr != nil {
				return serr
			}
			sm, _ := svc.(map[string]any)
			rel = str(sm, "live_release_id")
			if rel == "" {
				return failErr(ExitError, "service %s has no live release", service)
			}
			r, err = a.call("promoteRelease", apiops.Input{Path: []string{rel}, Body: jsonBody(body)})
		}
		if err != nil {
			return err
		}
		if r.Status == 200 {
			return a.emitBody(r)
		}
		return a.finish(wf, r)
	})
	c.Flags().StringVar(&service, "service", "", "promote only this service")
	c.Flags().StringVar(&strategy, "strategy", "", "recreate, blue_green or canary")
	c.Flags().BoolVar(&dry, "dry-run", false, "show the plan only")
	c.Flags().StringSliceVar(&allow, "allow-missing", nil, "variable names that may be missing in the target")
	c.Flags().BoolVar(&createMissing, "create-missing", false, "create services that are missing in the target")
	c.Flags().BoolVar(&includeDB, "include-databases", false, "also promote database services")
	wf.add(c)
	return c
}
