// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// format returns table, json or yaml. The default is table on a terminal and json when piped.
func (a *app) format() (string, error) {
	switch a.fl.output {
	case "table", "json", "yaml":
		return a.fl.output, nil
	case "":
		if isTerminal(a.opts.Out) || (a.opts.Interactive != nil && *a.opts.Interactive) {
			return "table", nil
		}
		return "json", nil
	}
	return "", usageErr("unknown output format %q: use table, json or yaml", a.fl.output)
}

// emit prints a value in the chosen format. cols are "HEADER=path" or "path" and only matter for the table.
func (a *app) emit(v any, cols ...string) error {
	f, err := a.format()
	if err != nil {
		return err
	}
	w := a.out()
	switch f {
	case "json":
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	case "yaml":
		b, err := yaml.Marshal(plainNumbers(v))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	return a.table(v, cols)
}

// emitBody decodes an answer and prints it. An empty answer prints a short message.
func (a *app) emitBody(r *Response, cols ...string) error {
	v, err := decodeJSON(r.Body)
	if err != nil {
		return err
	}
	if v == nil {
		return a.done("ok")
	}
	return a.emit(v, cols...)
}

// done prints a short result of an action that has no answer body.
func (a *app) done(msg string) error {
	f, err := a.format()
	if err != nil {
		return err
	}
	if f == "table" {
		_, err = fmt.Fprintln(a.out(), msg)
		return err
	}
	return a.emit(map[string]any{"ok": true, "message": msg})
}

// info prints a hint to stderr. It never goes to stdout, so pipes stay clean.
func (a *app) info(format string, args ...any) {
	fmt.Fprintf(a.err(), format+"\n", args...)
}

func plainNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = plainNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = plainNumbers(e)
		}
	}
	return v
}

var preferred = []string{"id", "name", "slug", "email", "host", "kind", "type", "state", "status", "role"}

func (a *app) table(v any, cols []string) error {
	var rows []map[string]any
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				rows = append(rows, m)
			} else {
				rows = append(rows, map[string]any{"value": e})
			}
		}
		return a.rows(rows, cols, len(x) == 0)
	case map[string]any:
		if items, ok := x["items"].([]any); ok {
			return a.table(items, cols)
		}
		return a.fields(x, cols)
	case nil:
		return nil
	}
	_, err := fmt.Fprintln(a.out(), cell(v))
	return err
}

type column struct{ header, path string }

func parseCols(cols []string) []column {
	var out []column
	for _, c := range cols {
		h, p := c, c
		if i := strings.Index(c, "="); i >= 0 {
			h, p = c[:i], c[i+1:]
		} else if i := strings.LastIndex(c, "."); i >= 0 {
			h = c[i+1:]
		}
		out = append(out, column{strings.ToUpper(h), p})
	}
	return out
}

func autoCols(m map[string]any) []string {
	var out []string
	used := map[string]bool{}
	for _, k := range preferred {
		if v, ok := m[k]; ok && scalar(v) {
			out = append(out, k)
			used[k] = true
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= 6 {
			break
		}
		if !used[k] && scalar(m[k]) {
			out = append(out, k)
		}
	}
	return out
}

func scalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func (a *app) rows(rows []map[string]any, cols []string, empty bool) error {
	if empty || len(rows) == 0 {
		_, err := fmt.Fprintln(a.out(), "No results.")
		return err
	}
	if len(cols) == 0 {
		cols = autoCols(rows[0])
	}
	cs := parseCols(cols)
	tw := tabwriter.NewWriter(a.out(), 0, 8, 2, ' ', 0)
	heads := make([]string, len(cs))
	for i, c := range cs {
		heads[i] = c.header
	}
	fmt.Fprintln(tw, strings.Join(heads, "\t"))
	for _, r := range rows {
		cells := make([]string, len(cs))
		for i, c := range cs {
			cells[i] = a.colored(c.path, cell(lookupPath(r, c.path)))
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func (a *app) fields(m map[string]any, cols []string) error {
	var keys []string
	if len(cols) > 0 {
		keys = cols
	} else {
		seen := map[string]bool{}
		for _, k := range preferred {
			if _, ok := m[k]; ok {
				keys = append(keys, k)
				seen[k] = true
			}
		}
		rest := make([]string, 0, len(m))
		for k := range m {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		keys = append(keys, rest...)
	}
	tw := tabwriter.NewWriter(a.out(), 0, 8, 2, ' ', 0)
	for _, c := range parseCols(keys) {
		val := lookupPath(m, c.path)
		if val == nil && len(cols) == 0 {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\n", strings.ToLower(c.header), a.colored(c.path, cell(val)))
	}
	return tw.Flush()
}

func lookupPath(m map[string]any, path string) any {
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		if x == "" {
			return "-"
		}
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case float64:
		return fmt.Sprint(x)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	s := strings.TrimSpace(b.String())
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// colored adds colour to state words on a terminal, unless --no-color or NO_COLOR is set.
func (a *app) colored(path, s string) string {
	if a.fl.noColor || a.getenv("NO_COLOR") != "" || !isTerminal(a.opts.Out) {
		return s
	}
	if !strings.HasSuffix(path, "state") && !strings.HasSuffix(path, "status") && !strings.HasSuffix(path, "health") {
		return s
	}
	switch s {
	case "live", "running", "ready", "healthy", "succeeded", "passed", "ok", "verified", "issued":
		return "\x1b[32m" + s + "\x1b[0m"
	case "failed", "down", "unhealthy", "failing", "rolled_back", "firing":
		return "\x1b[31m" + s + "\x1b[0m"
	}
	return s
}
