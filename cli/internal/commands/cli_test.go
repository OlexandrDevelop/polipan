// SPDX-License-Identifier: Apache-2.0

package commands_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/polipan/polipan/api"
	"github.com/polipan/polipan/cli/internal/commands"
)

// browserOnly are the operations that no command can run: the provider calls them with a browser, or Git calls them.
var browserOnly = map[string]string{
	"receiveGitWebhook": "inbound webhook of a Git provider",
	"receiveDeployHook": "inbound deploy hook for CI systems (a plain HTTP call)",
	"revealSecret":      "shows a secret to a signed-in admin; an API token is refused, so the CLI cannot call it (REQ-SEC-002)",
}

func opts(t *testing.T, url string, out, errw *bytes.Buffer) commands.Options {
	t.Helper()
	no := false
	env := map[string]string{"POLIPAN_URL": url, "POLIPAN_TOKEN": "pp_test"}
	return commands.Options{Out: out, Err: errw, In: strings.NewReader(""), Env: func(k string) string { return env[k] },
		ConfigPath: filepath.Join(t.TempDir(), "config.yaml"), NoKeyring: true, Interactive: &no, PollInterval: 10 * time.Millisecond}
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, s := range c.Commands() {
		walk(s, fn)
	}
}

// REQ-CLI-100: every operationId of the API file is reachable from a command.
func TestREQ_CLI_100_EveryOperationHasACommand(t *testing.T) {
	var out, errw bytes.Buffer
	root := commands.NewRoot(opts(t, "http://127.0.0.1:1", &out, &errw))
	have := map[string]bool{}
	walk(root, func(c *cobra.Command) {
		for _, op := range strings.Split(c.Annotations["operations"], ",") {
			if op != "" {
				have[op] = true
			}
		}
	})
	var missing []string
	for _, m := range regexp.MustCompile(`(?m)^\s+operationId: (\w+)`).FindAllStringSubmatch(string(api.Spec), -1) {
		if _, skip := browserOnly[m[1]]; !skip && !have[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("operations without a command: %s", strings.Join(missing, ", "))
	}
}

// AC-CLI-02: every list command prints valid JSON with --output json.
func TestAC_CLI_02_ListCommandsPrintJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"x_1","name":"one"}],"next_cursor":null}`))
	}))
	defer srv.Close()
	var out, errw bytes.Buffer
	var paths [][]string
	walk(commands.NewRoot(opts(t, srv.URL, &out, &errw)), func(c *cobra.Command) {
		if c.Annotations["list"] == "true" {
			var p []string
			for x := c; x.HasParent(); x = x.Parent() {
				p = append([]string{strings.Fields(x.Use)[0]}, p...)
			}
			paths = append(paths, p)
		}
	})
	if len(paths) < 15 {
		t.Fatalf("only %d list commands", len(paths))
	}
	for _, p := range paths {
		var o, e bytes.Buffer
		args := append(append([]string{}, p...), "--output", "json", "--project", "prj_1")
		if strings.Join(p, " ") != "secret versions" { // the fake secret has no scope
			args = append(args, "--env", "env_1")
		}
		// A command that needs an argument gets an id that needs no lookup.
		code := commands.Execute(opts(t, srv.URL, &o, &e), withArgs(p, args))
		if code != 0 {
			t.Errorf("%s: exit %d: %s", strings.Join(p, " "), code, e.String())
			continue
		}
		if !json.Valid(o.Bytes()) {
			t.Errorf("%s: not valid JSON: %q", strings.Join(p, " "), o.String())
		}
	}
}

// withArgs adds the arguments a list command may need.
func withArgs(path, args []string) []string {
	need := map[string][]string{
		"domain ls": {"svc_1"}, "volume ls": {"svc_1"}, "release ls": {"svc_1"}, "deployment ls": {"svc_1"}, "service ls": {},
		"backup policy ls": {}, "backup ls": {"pol_1"}, "backup checks": {}, "git repos": {"git_1"}, "logs search": {"error"},
		"node orphans": {"nod_1"}, "secret versions": {"one"}, "sso mappings": {"sso_1"},
	}
	extra := need[strings.Join(path, " ")]
	if extra == nil {
		return args
	}
	return append(append(append([]string{}, path...), extra...), args[len(path):]...)
}

// AC-CLI-01: a deployment that fails ends the command with exit code 5 when --wait is given.
func TestAC_CLI_01_DeployWaitFailedExitCode5(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"dep_1","service_id":"svc_1","state":"queued"}`))
		case strings.HasSuffix(r.URL.Path, "/deployments/dep_1"):
			state := "building"
			if polls.Add(1) > 1 {
				state = "failed"
			}
			_, _ = w.Write([]byte(`{"id":"dep_1","service_id":"svc_1","state":"` + state + `","failure_reason":"health_check_failed"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
		}
	}))
	defer srv.Close()
	var out, errw bytes.Buffer
	code := commands.Execute(opts(t, srv.URL, &out, &errw), []string{"deploy", "svc_1", "--wait"})
	if code != 5 {
		t.Fatalf("exit code %d (want 5): out %q err %q", code, out.String(), errw.String())
	}
	// A good deployment gives 0.
	polls.Store(-100)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"id":"dep_2","service_id":"svc_1","state":"queued"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"dep_2","state":"live"}`))
	}))
	defer srv2.Close()
	out.Reset()
	errw.Reset()
	if code := commands.Execute(opts(t, srv2.URL, &out, &errw), []string{"deploy", "svc_1", "--wait"}); code != 0 {
		t.Fatalf("live deployment: exit code %d: %s", code, errw.String())
	}
}

// Exit codes: 2 for a usage error, 3 for not found, 4 for no permission.
func TestREQ_CLI_002_ExitCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/services/svc_404"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
		default:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"forbidden","message":"no"}`))
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		args []string
		want int
	}{{[]string{"service", "get"}, 2}, {[]string{"service", "get", "svc_404"}, 3}, {[]string{"service", "get", "svc_1"}, 4}, {[]string{"nope"}, 2}} {
		var out, errw bytes.Buffer
		if code := commands.Execute(opts(t, srv.URL, &out, &errw), tc.args); code != tc.want {
			t.Errorf("%v: exit %d (want %d): %s", tc.args, code, tc.want, errw.String())
		}
	}
}

// REQ-CLI-103: validate works offline with the embedded schema, and an unknown key gives line and field.
func TestREQ_CLI_103_ValidateOffline(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "polipan.yaml")
	if err := os.WriteFile(good, []byte("version: 1\nproject: blog\nservices:\n  web:\n    source:\n      image: { name: nginx }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("version: 1\nproject: blog\nservices:\n  web:\n    colour: red\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := commands.Execute(opts(t, "http://127.0.0.1:1", &out, &errw), []string{"validate", "-f", good}); code != 0 {
		t.Fatalf("good file: %d %s", code, errw.String())
	}
	out.Reset()
	errw.Reset()
	code := commands.Execute(opts(t, "http://127.0.0.1:1", &out, &errw), []string{"validate", "-f", bad})
	if code == 0 || !strings.Contains(out.String(), `"line": 5`) || !strings.Contains(out.String(), "services.web.colour") || !strings.Contains(errw.String(), "bad.yaml") {
		t.Fatalf("bad file: exit %d, out %q err %q", code, out.String(), errw.String())
	}
}
