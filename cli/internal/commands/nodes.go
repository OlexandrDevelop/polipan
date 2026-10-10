// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

var nodeCols = []string{"id", "name", "state", "public_ip", "mesh_ip", "workload_count", "last_heartbeat_at"}

func (a *app) nodeCommands() []*cobra.Command {
	var labels []string
	var force, off bool
	var newName string
	add := a.cmd("add <name>", "Add a node. Prints the install command", "createNodeJoinToken", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"name": args[0]}
		if len(labels) > 0 {
			m, err := kvPairs(labels)
			if err != nil {
				return err
			}
			body["labels"] = m
		}
		r, err := a.call("createNodeJoinToken", apiops.Input{Body: jsonBody(body)})
		if err != nil {
			return err
		}
		v, _ := decodeJSON(r.Body)
		if f, _ := a.format(); f == "table" {
			if m, ok := v.(map[string]any); ok {
				a.info("Run this on the new node. The token is shown once.")
				_, err := a.out().Write([]byte(str(m, "install_command") + "\n"))
				return err
			}
		}
		return a.emit(v)
	})
	add.Flags().StringArrayVar(&labels, "label", nil, "label k=v (can be repeated)")

	ls := a.listCmd("ls", "List nodes", "listNodes", noArgs, func(cmd *cobra.Command, _ []string) error {
		items, err := a.list("listNodes", ids())
		if err != nil {
			return err
		}
		return a.emit(items, nodeCols...)
	})
	get := a.cmd("get <node>", "Show a node", "getNode", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("getNode", ids(id))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	label := a.cmd("label <node> k=v...", "Set labels on a node (k- removes a label)", "getNode,updateNode", minArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		v, err := a.getJSON("getNode", ids(id))
		if err != nil {
			return err
		}
		m, _ := v.(map[string]any)
		cur, _ := m["labels"].(map[string]any)
		out := map[string]any{}
		for k, e := range cur {
			out[k] = e
		}
		for _, kv := range args[1:] {
			if n := len(kv); n > 1 && kv[n-1] == '-' {
				delete(out, kv[:n-1])
				continue
			}
			k, val, ok := cutKV(kv)
			if !ok {
				return usageErr("expected key=value or key-, got %q", kv)
			}
			out[k] = val
		}
		r, err := a.call("updateNode", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"labels": out})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rename := a.cmd("rename <node> <new-name>", "Rename a node", "updateNode", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("updateNode", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"name": args[1]})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	_ = newName
	simple := func(use, short, op string, confirmIt bool) *cobra.Command {
		return a.cmd(use+" <node>", short, op, exactArgs(1), func(cmd *cobra.Command, args []string) error {
			if confirmIt {
				if err := a.confirm(short+" "+args[0], args[0]); err != nil {
					return err
				}
			}
			id, err := a.nodeID(args[0])
			if err != nil {
				return err
			}
			r, err := a.call(op, ids(id))
			if err != nil {
				return err
			}
			return a.emitBody(r, nodeCols...)
		})
	}
	maint := a.cmd("maintenance <node>", "Put a drained node in maintenance (--off to end it)", "setNodeMaintenance", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("setNodeMaintenance", apiops.Input{Path: []string{id}, Body: jsonBody(map[string]any{"enabled": !off})})
		if err != nil {
			return err
		}
		return a.emitBody(r, nodeCols...)
	})
	maint.Flags().BoolVar(&off, "off", false, "end the maintenance")
	check := a.cmd("check <node>", "Run the self check (Docker, Caddy, WireGuard) on a node", "runNodeSelfCheck", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		r, err := a.call("runNodeSelfCheck", ids(id))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	rm := a.cmd("rm <node>", "Delete a node", "deleteNode", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete node "+args[0], args[0]); err != nil {
			return err
		}
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if force {
			q.Set("force", "true")
		}
		if _, err := a.call("deleteNode", apiops.Input{Path: []string{id}, Query: q}); err != nil {
			return err
		}
		return a.done("deleted node " + args[0])
	})
	rm.Flags().BoolVar(&force, "force", false, "delete even when workloads run on the node")
	orphans := a.listCmd("orphans <node>", "List volumes on the node that no service uses", "listOrphanVolumes", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		items, err := a.list("listOrphanVolumes", ids(id))
		if err != nil {
			return err
		}
		return a.emit(items, "name", "size_bytes", "last_service")
	})
	orphanRm := a.cmd("orphan-rm <node> <volume>", "Delete an orphan volume for good", "deleteOrphanVolume", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		if err := a.confirm("Delete the data of volume "+args[1], args[1]); err != nil {
			return err
		}
		id, err := a.nodeID(args[0])
		if err != nil {
			return err
		}
		if _, err := a.call("deleteOrphanVolume", ids(id, args[1])); err != nil {
			return err
		}
		return a.done("deleted volume " + args[1])
	})
	node := group("node", "Nodes", add, ls, get, label, rename, simple("cordon", "Stop scheduling on", "cordonNode", false),
		simple("uncordon", "Allow scheduling on", "uncordonNode", false), simple("drain", "Drain", "drainNode", false), maint, check, rm, orphans, orphanRm)
	return []*cobra.Command{node}
}
