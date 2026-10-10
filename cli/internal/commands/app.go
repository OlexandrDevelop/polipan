// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/cli/internal/apiops"
	"github.com/polipan/polipan/cli/internal/config"
)

// Options sets the streams and the environment of one CLI run. Tests use it. Zero values mean the real ones.
type Options struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Env reads an environment variable. Default os.Getenv.
	Env func(string) string
	// ConfigPath is the config file. Default ~/.config/polipan/config.yaml.
	ConfigPath string
	// Keyring stores tokens. Nil means the system keyring when present, else the file.
	Keyring config.Keyring
	// NoKeyring forces the file.
	NoKeyring bool
	// HTTPClient replaces the transport (tests).
	HTTPClient *http.Client
	// Interactive overrides the terminal check.
	Interactive *bool
	// PollInterval is the wait between polls of a deployment. Default 2s.
	PollInterval time.Duration
	// Version is the client version.
	Version, Commit string
}

type globalFlags struct {
	context, url, project, env, output string
	noColor, yes, debug                bool
}

type app struct {
	opts    Options
	fl      globalFlags
	ctx     context.Context
	cfgPath string
	cfg     *config.File
	eff     *config.Effective
	gc      *gen.Client
	stdin   *bufio.Reader
}

func (a *app) getenv(k string) string {
	if a.opts.Env != nil {
		return a.opts.Env(k)
	}
	return os.Getenv(k)
}

func (a *app) out() io.Writer { return a.opts.Out }
func (a *app) err() io.Writer { return a.opts.Err }

func (a *app) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *app) keyring() config.Keyring {
	if a.opts.NoKeyring || a.getenv("POLIPAN_NO_KEYRING") != "" {
		return nil
	}
	if a.opts.Keyring != nil {
		return a.opts.Keyring
	}
	return config.SystemKeyring()
}

func (a *app) configPath() (string, error) {
	if a.cfgPath != "" {
		return a.cfgPath, nil
	}
	if a.opts.ConfigPath != "" {
		a.cfgPath = a.opts.ConfigPath
	} else {
		p, err := config.DefaultPath(a.getenv)
		if err != nil {
			return "", err
		}
		a.cfgPath = p
	}
	return a.cfgPath, nil
}

func (a *app) config() (*config.File, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	p, err := a.configPath()
	if err != nil {
		return nil, err
	}
	f, err := config.Load(p)
	if err != nil {
		return nil, err
	}
	a.cfg = f
	return f, nil
}

// effective gives the settings of this run (flags, env, file).
func (a *app) effective() (*config.Effective, error) {
	if a.eff != nil {
		return a.eff, nil
	}
	f, err := a.config()
	if err != nil {
		return nil, err
	}
	e := config.Resolve(f, a.keyring(), config.Flags{Context: a.fl.context, URL: a.fl.url, Project: a.fl.project, Env: a.fl.env}, a.getenv)
	a.eff = &e
	return a.eff, nil
}

// transport adds the token and the user agent, and logs requests without secrets (--debug).
type transport struct {
	base  http.RoundTripper
	token string
	debug io.Writer
	agent string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if t.token != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	r.Header.Set("User-Agent", t.agent)
	start := time.Now()
	resp, err := t.base.RoundTrip(r)
	if t.debug != nil {
		// Only the method, the path and the names of the query keys. No header, no body, no values.
		keys := make([]string, 0)
		for k := range r.URL.Query() {
			keys = append(keys, k)
		}
		q := ""
		if len(keys) > 0 {
			q = " ?" + strings.Join(keys, ",")
		}
		if err != nil {
			fmt.Fprintf(t.debug, "debug: %s %s%s failed: %v\n", r.Method, r.URL.Path, q, err)
		} else {
			fmt.Fprintf(t.debug, "debug: %s %s%s -> %d (%s)\n", r.Method, r.URL.Path, q, resp.StatusCode, time.Since(start).Round(time.Millisecond))
		}
	}
	return resp, err
}

func (a *app) httpClient(token string) *http.Client {
	base := http.RoundTripper(http.DefaultTransport)
	if a.opts.HTTPClient != nil && a.opts.HTTPClient.Transport != nil {
		base = a.opts.HTTPClient.Transport
	}
	t := &transport{base: base, token: token, agent: "polipan-cli/" + a.opts.Version}
	if a.fl.debug {
		t.debug = a.err()
	}
	return &http.Client{Transport: t}
}

// client returns the generated API client for the current context.
func (a *app) client() (*gen.Client, error) {
	if a.gc != nil {
		return a.gc, nil
	}
	e, err := a.effective()
	if err != nil {
		return nil, err
	}
	if e.URL == "" {
		return nil, usageErr("no server set: run polipan login --url URL, or set POLIPAN_URL")
	}
	c, err := gen.NewClient(e.URL+"/api/v1", gen.WithHTTPClient(a.httpClient(e.Token)))
	if err != nil {
		return nil, usageErr("the server address is not valid: %v", err)
	}
	a.gc = c
	return c, nil
}

// Response is a finished call with its body.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// call runs one operation and reads the answer. A status of 400 or more is an error.
func (a *app) call(opID string, in apiops.Input) (*Response, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.context(), 60*time.Second)
	defer cancel()
	resp, err := apiops.Do(ctx, c, opID, in)
	if err != nil {
		return nil, a.netErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, a.netErr(err)
	}
	if resp.StatusCode >= 400 {
		return nil, parseAPIError(resp.StatusCode, body)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

func (a *app) netErr(err error) error {
	if a.context().Err() != nil {
		return failErr(ExitError, "stopped")
	}
	e, _ := a.effective()
	where := ""
	if e != nil {
		where = " " + e.URL
	}
	return failErr(ExitError, "cannot reach the server%s: %v", where, err)
}

// stream opens a long answer (SSE). The caller closes the body.
func (a *app) stream(opID string, in apiops.Input) (*http.Response, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	resp, err := apiops.Do(a.context(), c, opID, in)
	if err != nil {
		return nil, a.netErr(err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, parseAPIError(resp.StatusCode, body)
	}
	return resp, nil
}

// getJSON runs an operation and decodes the body into a generic value.
func (a *app) getJSON(opID string, in apiops.Input) (any, error) {
	r, err := a.call(opID, in)
	if err != nil {
		return nil, err
	}
	return decodeJSON(r.Body)
}

func decodeJSON(b []byte) (any, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, failErr(ExitError, "the server answer is not JSON: %v", err)
	}
	return v, nil
}

// list reads a list operation. It follows next_cursor, unless the call has a limit (then it reads one page).
// It accepts a plain array or an object with items.
func (a *app) list(opID string, in apiops.Input) ([]any, error) {
	var all []any
	single := in.Query.Get("limit") != ""
	q := url.Values{}
	for k, v := range in.Query {
		q[k] = v
	}
	in.Query = q
	for page := 0; page < 1000; page++ {
		v, err := a.getJSON(opID, in)
		if err != nil {
			return nil, err
		}
		switch x := v.(type) {
		case []any:
			return append(all, x...), nil
		case map[string]any:
			items, _ := x["items"].([]any)
			all = append(all, items...)
			next, _ := x["next_cursor"].(string)
			if next == "" || single {
				return all, nil
			}
			in.Query.Set("cursor", next)
		default:
			return all, nil
		}
	}
	return all, nil
}

func (a *app) readStdin() *bufio.Reader {
	if a.stdin == nil {
		in := a.opts.In
		if in == nil {
			in = strings.NewReader("")
		}
		a.stdin = bufio.NewReader(in)
	}
	return a.stdin
}

func isTerminal(x any) bool {
	f, ok := x.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// interactive is true when the user can answer questions.
func (a *app) interactive() bool {
	if a.opts.Interactive != nil {
		return *a.opts.Interactive
	}
	return isTerminal(a.opts.In) && isTerminal(a.opts.Err)
}

// cmd makes a command that carries its operation ids (REQ-CLI-100).
func (a *app) cmd(use, short, ops string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.ctx = cmd.Context()
			return run(cmd, args)
		},
	}
	c.Annotations = map[string]string{}
	if ops != "" {
		c.Annotations[operationsAnnotation] = ops
	}
	return c
}

// listCmd is a command that lists things. The tests call every one of them with --output json (AC-CLI-02).
func (a *app) listCmd(use, short, ops string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	c := a.cmd(use, short, ops, args, run)
	c.Annotations["list"] = "true"
	return c
}

// group makes a command that only holds sub commands.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return cmd.Help()
		},
	}
	c.AddCommand(subs...)
	return c
}

// Argument checks that give exit code 2.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageErr("%s needs %d argument(s), got %d. Usage: %s", cmd.CommandPath(), n, len(args), cmd.UseLine())
		}
		return nil
	}
}

func rangeArgs(min, max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return usageErr("%s needs %d to %d argument(s), got %d. Usage: %s", cmd.CommandPath(), min, max, len(args), cmd.UseLine())
		}
		return nil
	}
}

func minArgs(min int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min {
			return usageErr("%s needs at least %d argument(s). Usage: %s", cmd.CommandPath(), min, cmd.UseLine())
		}
		return nil
	}
}

var noArgs = exactArgs(0)

func ids(path ...string) apiops.Input { return apiops.Input{Path: path} }
