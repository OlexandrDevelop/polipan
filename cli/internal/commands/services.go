// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/net/websocket"

	"github.com/polipan/polipan/cli/internal/apiops"
)

var serviceCols = []string{"id", "name", "type", "status", "desired_state", "live_release_id"}

func (a *app) serviceCommands() []*cobra.Command {
	return []*cobra.Command{a.serviceCmd(), a.domainCmd(), a.volumeCmd()}
}

func (a *app) serviceCmd() *cobra.Command {
	var typ, image, repo, branch, conn, path, engine, dbVersion string
	var port int
	var delVol bool
	var node, volCopy, failover string
	var cmdStr string
	var replica int
	bf := &bodyFlags{}
	bfs := &bodyFlags{}
	wf := &waitFlags{}

	ls := a.listCmd("ls", "List services of the environment (or of all environments of the project)", "listServices,listEnvironments", noArgs, func(cmd *cobra.Command, _ []string) error {
		e, err := a.effective()
		if err != nil {
			return err
		}
		var envIDs []string
		if e.Env != "" {
			id, err := a.envID("")
			if err != nil {
				return err
			}
			envIDs = []string{id}
		} else {
			pid, err := a.projectID("")
			if err != nil {
				return err
			}
			envs, err := a.list("listEnvironments", ids(pid))
			if err != nil {
				return err
			}
			for _, it := range envs {
				m, _ := it.(map[string]any)
				envIDs = append(envIDs, str(m, "id"))
			}
		}
		all := []any{}
		for _, id := range envIDs {
			items, err := a.list("listServices", ids(id))
			if err != nil {
				return err
			}
			all = append(all, items...)
		}
		return a.emit(all, serviceCols...)
	})
	create := a.cmd("create <name>", "Create a service", "createService", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		eid, err := a.envID("")
		if err != nil {
			return err
		}
		fl := map[string]any{"name": args[0]}
		if typ != "" {
			fl["type"] = typ
		}
		src := map[string]any{}
		if image != "" {
			src["image"] = image
		}
		if repo != "" {
			src["repo"] = repo
		}
		if branch != "" {
			src["branch"] = branch
		}
		if path != "" {
			src["path"] = path
		}
		if conn != "" {
			cid, err := a.gitConnID(conn)
			if err != nil {
				return err
			}
			src["connection_id"] = cid
		}
		if len(src) > 0 {
			fl["source"] = src
		}
		if engine != "" {
			fl["db_engine"] = engine
		}
		if dbVersion != "" {
			fl["db_version"] = dbVersion
		}
		if port > 0 {
			fl["config.ports"] = []any{map[string]any{"container": port, "protocol": "tcp"}}
		}
		body, err := bf.build(a, fl)
		if err != nil {
			return err
		}
		r, err := a.call("createService", apiops.Input{Path: []string{eid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, serviceCols...)
	})
	bf.add(create)
	create.Flags().StringVar(&typ, "type", "app", "app, database, worker or cron")
	create.Flags().StringVar(&image, "image", "", "container image, for example nginx:1.27")
	create.Flags().StringVar(&repo, "repo", "", "Git repository")
	create.Flags().StringVar(&branch, "branch", "", "Git branch")
	create.Flags().StringVar(&conn, "connection", "", "Git connection")
	create.Flags().StringVar(&path, "source-path", "", "path in the repository")
	create.Flags().StringVar(&engine, "db-engine", "", "postgres, mysql, mariadb, mongo or redis")
	create.Flags().StringVar(&dbVersion, "db-version", "", "database version")
	create.Flags().IntVar(&port, "port", 0, "container port")

	get := a.cmd("get <service>", "Show a service", "getService", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("getService", ids(id))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	set := a.cmd("set <service> [key=value...]", "Change the config of a service, for example config.replicas=2", "updateService", minArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		fl := map[string]any{}
		for _, kv := range args[1:] {
			k, v, ok := cutKV(kv)
			if !ok {
				return usageErr("expected key=value, got %q", kv)
			}
			if k == "failover" || k == "allow_data_loss_failover" || k == "override_git" {
				fl[k] = parseValue(v)
			} else {
				fl["config."+k] = parseValue(v)
			}
		}
		if failover != "" {
			fl["failover"] = failover
		}
		body, err := bfs.build(a, fl)
		if err != nil {
			return err
		}
		r, err := a.call("updateService", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.emitBody(r, serviceCols...)
	})
	bfs.add(set)
	set.Flags().StringVar(&failover, "failover", "", "none or auto")
	rm := a.cmd("rm <service>", "Delete a service", "deleteService,getService", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		o, err := a.serviceObj(args[0])
		if err != nil {
			return err
		}
		name := str(o, "name")
		if err := a.confirm("Delete service "+name, name); err != nil {
			return err
		}
		q := url.Values{"confirm": {name}}
		if delVol {
			q.Set("delete_volumes", "true")
		}
		if _, err := a.call("deleteService", apiops.Input{Path: []string{str(o, "id")}, Query: q}); err != nil {
			return err
		}
		return a.done("deleted service " + name)
	})
	rm.Flags().BoolVar(&delVol, "delete-volumes", false, "also delete the volumes")
	action := func(use, short, op string) *cobra.Command {
		return a.cmd(use+" <service>", short, op, exactArgs(1), func(cmd *cobra.Command, args []string) error {
			id, err := a.serviceID(args[0])
			if err != nil {
				return err
			}
			if _, err := a.call(op, ids(id)); err != nil {
				return err
			}
			return a.done(use + " requested for " + args[0])
		})
	}
	move := a.cmd("move <service> --node N", "Move a service to another node", "moveService", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if node == "" {
			return usageErr("--node is needed")
		}
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		nid, err := a.nodeID(node)
		if err != nil {
			return err
		}
		body := map[string]any{"target_node_id": nid}
		if volCopy != "" {
			body["volume_copy"] = volCopy
		}
		r, err := a.call("moveService", apiops.Input{Path: []string{id}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.finish(wf, r)
	})
	move.Flags().StringVar(&node, "node", "", "target node")
	move.Flags().StringVar(&volCopy, "volume-copy", "", "backup or direct")
	wf.add(move)
	health := a.cmd("health-test <service>", "Run the health check once", "testHealthCheck", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("testHealthCheck", ids(id))
		if err != nil {
			return err
		}
		v, _ := decodeJSON(r.Body)
		if err := a.emit(v); err != nil {
			return err
		}
		if m, ok := v.(map[string]any); ok {
			if h, _ := m["healthy"].(bool); !h {
				return failErr(ExitError, "the health check failed")
			}
		}
		return nil
	})
	exec := a.cmd("exec <service> [-- command...]", "Open a terminal in a running container", "execService", minArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		c := "/bin/sh"
		if len(args) > 1 {
			c = strings.Join(args[1:], " ")
		}
		return a.execTerminal(id, c, replica)
	})
	exec.Flags().IntVar(&replica, "replica", 0, "replica index")
	_ = cmdStr
	key := a.cmd("deploy-key <service>", "Make an SSH deploy key and webhook for a Git service. Shown once", "createDeployKey", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("createDeployKey", ids(id))
		if err != nil {
			return err
		}
		a.info("The webhook secret and the key are shown once.")
		return a.emitBody(r)
	})
	return group("service", "Services", ls, create, get, set, rm, action("stop", "Stop a service", "stopService"),
		action("start", "Start a stopped service", "startService"), action("restart", "Restart the containers", "restartService"), move, health, exec, key)
}

// execTerminal connects a WebSocket terminal. Text frames are control, binary frames are terminal bytes.
func (a *app) execTerminal(serviceID, command string, replica int) error {
	e, err := a.effective()
	if err != nil {
		return err
	}
	if e.URL == "" {
		return usageErr("no server set: use --url or POLIPAN_URL")
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return usageErr("the server address is not valid")
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	q := url.Values{"command": {command}}
	if replica > 0 {
		q.Set("replica", itoa(replica))
	}
	loc := scheme + "://" + u.Host + strings.TrimRight(u.Path, "/") + "/api/v1/services/" + serviceID + "/exec?" + q.Encode()
	cfg, err := websocket.NewConfig(loc, e.URL)
	if err != nil {
		return err
	}
	if e.Token != "" {
		cfg.Header.Set("Authorization", "Bearer "+e.Token)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return failErr(ExitError, "cannot open the terminal: %v", err)
	}
	defer func() { _ = ws.Close() }()
	raw := isTerminal(a.opts.In)
	if raw {
		if f, ok := a.opts.In.(*os.File); ok {
			stty(f, "raw")
			stty(f, "-echo")
			defer stty(f, "sane")
		}
	}
	go func() {
		buf := make([]byte, 4096)
		in := a.readStdin()
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_ = websocket.Message.Send(ws, buf[:n])
			}
			if err != nil {
				_ = websocket.Message.Send(ws, `{"type":"eof"}`)
				return
			}
		}
	}()
	for {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return nil
		}
		if len(msg) > 0 && msg[0] == '{' {
			v, _ := decodeJSON(msg)
			if m, ok := v.(map[string]any); ok && str(m, "type") == "exit" {
				if code, ok := m["code"].(interface{ Int64() (int64, error) }); ok {
					if n, _ := code.Int64(); n != 0 {
						return failErr(int(n), "the command ended with code %d", n)
					}
				}
				return nil
			}
		}
		_, _ = a.out().Write(msg)
	}
}

var _ = exec.Command
var _ = bufio.NewReader
var _ io.Reader
