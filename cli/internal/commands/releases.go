// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/apiops"
)

func (a *app) releaseCommands() []*cobra.Command {
	var limit int
	ls := a.listCmd("ls <service>", "List releases of a service", "listReleases", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, err := a.serviceID(args[0])
		if err != nil {
			return err
		}
		q := url.Values{}
		if limit > 0 {
			q.Set("limit", itoa(limit))
		}
		items, err := a.list("listReleases", apiops.Input{Path: []string{id}, Query: q})
		if err != nil {
			return err
		}
		return a.emit(items, "id", "number", "status", "image_ref", "source.commit_sha", "note", "created_at")
	})
	ls.Flags().IntVar(&limit, "limit", 0, "read only one page of this size")
	get := a.cmd("get <id>", "Show a release", "getRelease", exactArgs(1), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("getRelease", ids(args[0]))
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	diff := a.cmd("diff <a> <b>", "Compare two releases", "diffReleases", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("diffReleases", ids(args[0], args[1]))
		if err != nil {
			return err
		}
		v, err := decodeJSON(r.Body)
		if err != nil {
			return err
		}
		if f, _ := a.format(); f == "table" {
			m, _ := v.(map[string]any)
			changes, _ := m["changes"].([]any)
			if len(changes) == 0 {
				_, _ = fmt.Fprintln(a.out(), "No differences.")
			}
			for _, c := range changes {
				cm, _ := c.(map[string]any)
				_, _ = fmt.Fprintf(a.out(), "%s: %s -> %s\n", str(cm, "path"), cell(cm["before"]), cell(cm["after"]))
			}
			return nil
		}
		return a.emit(v)
	})
	note := a.cmd("note <id> <text>", "Set the note of a release", "annotateRelease", exactArgs(2), func(cmd *cobra.Command, args []string) error {
		r, err := a.call("annotateRelease", apiops.Input{Path: []string{args[0]}, Body: jsonBody(map[string]any{"note": args[1]})})
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
	return []*cobra.Command{group("release", "Releases", ls, get, diff, note)}
}
