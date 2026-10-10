// SPDX-License-Identifier: Apache-2.0

// Package commands is the command tree of the polipan CLI. It talks to the server only through
// the generated API client (REQ-CORE-009).
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/cli/internal/version"
)

func (o *Options) fill() {
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.Version == "" {
		o.Version = version.Version
	}
	if o.Commit == "" {
		o.Commit = version.Commit
	}
	if o.PollInterval == 0 {
		o.PollInterval = 2 * time.Second
	}
}

// Execute runs the CLI and returns the exit code.
func Execute(opts Options, args []string) int {
	opts.fill()
	root := NewRoot(opts)
	root.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintln(opts.Err, "polipan:", err)
	}
	return ExitCode(err)
}

// NewRoot builds the whole command tree.
func NewRoot(opts Options) *cobra.Command {
	opts.fill()
	a := &app{opts: opts}
	root := &cobra.Command{
		Use:           "polipan",
		Short:         "Command line client of Polipan",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return cmd.Help()
		},
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			a.checkServerVersion(cmd)
		},
	}
	root.SetOut(opts.Out)
	root.SetErr(opts.Err)
	root.SetIn(opts.In)
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr("%v", err) })

	pf := root.PersistentFlags()
	pf.StringVar(&a.fl.context, "context", "", "name of the context to use (env POLIPAN_CONTEXT)")
	pf.StringVar(&a.fl.url, "url", "", "address of the server, for example https://panel.example.com (env POLIPAN_URL)")
	pf.StringVar(&a.fl.url, "server", "", "same as --url")
	_ = pf.MarkHidden("server")
	pf.StringVar(&a.fl.project, "project", "", "project name, slug or id (env POLIPAN_PROJECT)")
	pf.StringVar(&a.fl.env, "env", "", "environment name, slug or id (env POLIPAN_ENV)")
	pf.StringVarP(&a.fl.output, "output", "o", "", "output format: table, json or yaml (default table on a terminal, json when piped)")
	pf.BoolVar(&a.fl.noColor, "no-color", false, "do not use colors")
	pf.BoolVarP(&a.fl.yes, "yes", "y", false, "do not ask for confirmation")
	pf.BoolVar(&a.fl.debug, "debug", false, "log requests to stderr, without secrets")

	root.AddCommand(
		a.versionCmd(),
		a.completionCmd(root),
		a.healthCmd(),
	)
	root.AddCommand(a.authCommands()...)
	root.AddCommand(a.orgCommands()...)
	root.AddCommand(a.nodeCommands()...)
	root.AddCommand(a.projectCommands()...)
	root.AddCommand(a.serviceCommands()...)
	root.AddCommand(a.deployCommands()...)
	root.AddCommand(a.releaseCommands()...)
	root.AddCommand(a.configCommands()...)
	root.AddCommand(a.dataCommands()...)
	root.AddCommand(a.obsCommands()...)
	root.AddCommand(a.templateCommands()...)
	root.AddCommand(a.backupCommands()...)
	root.AddCommand(a.alertCommands()...)
	return root
}

func (a *app) completionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion bash|zsh|fish",
		Short:     "Print the shell completion script",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			w := a.out()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(w, true)
			case "zsh":
				return root.GenZshCompletion(w)
			case "fish":
				return root.GenFishCompletion(w, true)
			}
			return usageErr("unknown shell %q: use bash, zsh or fish", args[0])
		},
	}
}

func (a *app) healthCmd() *cobra.Command {
	return a.cmd("health", "Show the health of the server", "getHealth", noArgs, func(cmd *cobra.Command, _ []string) error {
		r, err := a.call("getHealth", ids())
		if err != nil {
			return err
		}
		return a.emitBody(r)
	})
}

func (a *app) versionCmd() *cobra.Command {
	return a.cmd("version", "Print the client version, and the server version when a server is set", "getVersion", noArgs, func(cmd *cobra.Command, _ []string) error {
		client := map[string]any{"version": a.opts.Version, "commit": a.opts.Commit}
		out := map[string]any{"client": client}
		e, err := a.effective()
		if err != nil {
			return err
		}
		var serverLine string
		if e.URL != "" {
			r, cerr := a.call("getVersion", ids())
			switch {
			case cerr == nil:
				v, _ := decodeJSON(r.Body)
				out["server"] = v
				if m, ok := v.(map[string]any); ok {
					serverLine = fmt.Sprintf("server %s (%s), api %s", str(m, "version"), str(m, "commit"), str(m, "api"))
					if w := versionWarning(a.opts.Version, str(m, "version")); w != "" {
						a.info("warning: %s", w)
					}
				}
			case a.fl.url != "":
				return cerr
			default:
				a.info("server: %v", cerr)
			}
		}
		if f, ferr := a.format(); ferr == nil && f == "table" {
			fmt.Fprintf(a.out(), "polipan %s (%s)\n", a.opts.Version, a.opts.Commit)
			if serverLine != "" {
				fmt.Fprintln(a.out(), serverLine)
			}
			return nil
		}
		return a.emit(out)
	})
}

// versionWarning returns a text when the client is older than the server by more than one minor version (REQ-CLI-104).
func versionWarning(client, server string) string {
	cm, cn, ok1 := majorMinor(client)
	sm, sn, ok2 := majorMinor(server)
	if !ok1 || !ok2 {
		return ""
	}
	if sm > cm || (sm == cm && sn > cn+1) {
		return fmt.Sprintf("this CLI (%s) is older than the server (%s): please update polipan", client, server)
	}
	return ""
}

func majorMinor(v string) (int, int, bool) {
	v = strings.TrimPrefix(v, "v")
	var major, minor int
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// checkServerVersion warns, at most once a day, when the CLI is too old for the server. It never fails the command.
func (a *app) checkServerVersion(cmd *cobra.Command) {
	if a.opts.Version == "dev" || a.getenv("POLIPAN_NO_UPDATE_CHECK") != "" || a.gc == nil {
		return
	}
	switch cmd.Name() {
	case "version", "completion", "login", "logout":
		return
	}
	path, err := a.configPath()
	if err != nil {
		return
	}
	stamp := path + ".version-check"
	if st, err := os.Stat(stamp); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		return
	}
	r, err := a.call("getVersion", ids())
	if err != nil {
		return
	}
	_ = os.WriteFile(stamp, nil, 0o600)
	v, _ := decodeJSON(r.Body)
	if m, ok := v.(map[string]any); ok {
		if w := versionWarning(a.opts.Version, str(m, "version")); w != "" {
			a.info("warning: %s", w)
		}
	}
}

var _ io.Writer = os.Stdout
