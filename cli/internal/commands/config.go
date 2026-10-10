// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/polipan/polipan/cli/internal/apiops"
	"github.com/polipan/polipan/cli/internal/schema"
)

var configFileNames = []string{"polipan.yaml", ".polipan.yaml", "polipan.yml"}

// readConfigFile reads the file of -f, or the first default name that exists. "-" is stdin.
func (a *app) readConfigFile(path string) (string, []byte, error) {
	if path == "-" {
		b, err := io.ReadAll(a.readStdin())
		return "stdin", b, err
	}
	if path == "" {
		for _, n := range configFileNames {
			if _, err := os.Stat(n); err == nil {
				path = n
				break
			}
		}
		if path == "" {
			return "", nil, usageErr("no polipan.yaml found: use -f FILE")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, notFoundErr("cannot read %s: %v", path, err)
	}
	return path, b, nil
}

// configProject returns the project id: --project, else the project key of the file.
func (a *app) configProject(data []byte) (string, error) {
	ref := a.fl.project
	if ref == "" {
		var f struct {
			Project string `yaml:"project"`
		}
		_ = yaml.Unmarshal(data, &f)
		ref = f.Project
	}
	if ref == "" {
		e, _ := a.effective()
		ref = e.Project
	}
	if ref == "" {
		return "", usageErr("no project: set --project or the project key in the file")
	}
	return a.projectID(ref)
}

func (a *app) configCommands() []*cobra.Command {
	var file string
	var prune, pruneData bool
	wf := &waitFlags{}
	validate := a.cmd("validate", "Check polipan.yaml offline against the JSON Schema", "", noArgs, func(cmd *cobra.Command, _ []string) error {
		name, data, err := a.readConfigFile(file)
		if err != nil {
			return err
		}
		problems, err := schema.Validate(data)
		if err != nil {
			return err
		}
		if len(problems) == 0 {
			return a.done(name + ": valid")
		}
		return a.reportProblems(name, problems)
	})
	validate.Flags().StringVarP(&file, "file", "f", "", "config file (default polipan.yaml)")

	plan := a.cmd("plan", "Show what apply would change. Changes nothing", "planProjectConfig", noArgs, func(cmd *cobra.Command, _ []string) error {
		name, data, err := a.readConfigFile(file)
		if err != nil {
			return err
		}
		v, err := a.planConfig(name, data)
		if err != nil {
			return err
		}
		return a.printPlan(v)
	})
	plan.Flags().StringVarP(&file, "file", "f", "", "config file (default polipan.yaml)")

	apply := a.cmd("apply", "Apply polipan.yaml to the project", "planProjectConfig,applyProjectConfig", noArgs, func(cmd *cobra.Command, _ []string) error {
		if !a.fl.yes && !a.interactive() {
			return usageErr("apply needs --yes in a shell that is not interactive")
		}
		name, data, err := a.readConfigFile(file)
		if err != nil {
			return err
		}
		v, err := a.planConfig(name, data)
		if err != nil {
			return err
		}
		if err := a.printPlan(v); err != nil {
			return err
		}
		m, _ := v.(map[string]any)
		changes, _ := m["changes"].([]any)
		if countActions(changes, "create", "update", "delete") == 0 {
			return nil
		}
		if err := a.confirmYN("Apply this plan?"); err != nil {
			return err
		}
		pid, err := a.configProject(data)
		if err != nil {
			return err
		}
		body := map[string]any{"config": string(data), "plan_hash": str(m, "plan_hash")}
		if prune {
			body["prune"] = true
		}
		if pruneData {
			body["prune_data"] = true
		}
		r, err := a.call("applyProjectConfig", apiops.Input{Path: []string{pid}, Body: jsonBody(body)})
		if err != nil {
			return err
		}
		return a.finish(wf, r)
	})
	apply.Flags().StringVarP(&file, "file", "f", "", "config file (default polipan.yaml)")
	apply.Flags().BoolVar(&prune, "prune", false, "delete resources that are not in the file (needs admin)")
	apply.Flags().BoolVar(&pruneData, "prune-data", false, "also delete volumes and databases")
	wf.add(apply)

	export := a.cmd("export", "Print the project as polipan.yaml (secret names only)", "exportProjectConfig", noArgs, func(cmd *cobra.Command, _ []string) error {
		pid, err := a.projectID("")
		if err != nil {
			return err
		}
		r, err := a.call("exportProjectConfig", ids(pid))
		if err != nil {
			return err
		}
		_, err = a.out().Write(r.Body)
		return err
	})
	return []*cobra.Command{plan, apply, export, validate}
}

func (a *app) planConfig(name string, data []byte) (any, error) {
	pid, err := a.configProject(data)
	if err != nil {
		return nil, err
	}
	r, err := a.call("planProjectConfig", apiops.Input{Path: []string{pid}, ContentType: "application/yaml", Body: data})
	if err != nil {
		if ae, ok := err.(*apiError); ok && ae.Status == 422 {
			return nil, failErr(ExitError, "%s: %s", name, ae.Message)
		}
		return nil, err
	}
	v, err := decodeJSON(r.Body)
	if err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok {
		if valid, ok := m["valid"].(bool); ok && !valid {
			var ps []schema.Problem
			errs, _ := m["errors"].([]any)
			for _, e := range errs {
				em, _ := e.(map[string]any)
				line := 0
				if n, ok := em["line"].(interface{ Int64() (int64, error) }); ok {
					l, _ := n.Int64()
					line = int(l)
				}
				ps = append(ps, schema.Problem{Line: line, Path: str(em, "path"), Message: str(em, "message")})
			}
			return nil, a.reportProblems(name, ps)
		}
	}
	return v, nil
}

// reportProblems prints "file:line: field: message" lines and returns an error (exit 1).
func (a *app) reportProblems(name string, ps []schema.Problem) error {
	f, _ := a.format()
	if f != "table" {
		_ = a.emit(ps)
	} else {
		for _, p := range ps {
			loc := fmt.Sprintf("%s:%d", name, p.Line)
			if p.Path != "" {
				fmt.Fprintf(a.err(), "%s: %s: %s\n", loc, p.Path, p.Message)
			} else {
				fmt.Fprintf(a.err(), "%s: %s\n", loc, p.Message)
			}
		}
	}
	return failErr(ExitError, "%s is not valid (%d problem(s))", name, len(ps))
}

func countActions(changes []any, actions ...string) int {
	n := 0
	for _, c := range changes {
		m, _ := c.(map[string]any)
		for _, a := range actions {
			if str(m, "action") == a {
				n++
			}
		}
	}
	return n
}

// printPlan writes the plan. In table mode it is the stable text of docs/spec/06-cli.md section 4.
func (a *app) printPlan(v any) error {
	if f, _ := a.format(); f != "table" {
		return a.emit(v)
	}
	m, _ := v.(map[string]any)
	changes, _ := m["changes"].([]any)
	w := a.out()
	sym := map[string]string{"create": "+", "update": "~", "delete": "-", "unmanaged": "?"}
	for _, c := range changes {
		cm, _ := c.(map[string]any)
		act := str(cm, "action")
		fmt.Fprintf(w, "%s %s %s/%s\n", sym[act], act, str(cm, "resource"), str(cm, "name"))
		fields, _ := cm["fields"].([]any)
		for _, f := range fields {
			fm, _ := f.(map[string]any)
			fmt.Fprintf(w, "    %s: %s -> %s\n", str(fm, "path"), cell(fm["before"]), cell(fm["after"]))
		}
	}
	if len(changes) == 0 {
		fmt.Fprintln(w, "No changes.")
	}
	fmt.Fprintf(w, "\nPlan: %d to create, %d to update, %d to delete, %d unmanaged.\n",
		countActions(changes, "create"), countActions(changes, "update"), countActions(changes, "delete"), countActions(changes, "unmanaged"))
	fmt.Fprintf(w, "Plan hash: %s\n", strings.TrimSpace(str(m, "plan_hash")))
	return nil
}
