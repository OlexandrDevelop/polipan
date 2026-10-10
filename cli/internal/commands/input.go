// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// confirm asks the user to type the name of the thing, unless --yes is set.
// In a shell that is not interactive it fails with exit code 2.
func (a *app) confirm(action, name string) error {
	if a.fl.yes {
		return nil
	}
	if !a.interactive() {
		return usageErr("%s needs a confirmation: add --yes to run it in a shell that is not interactive", action)
	}
	fmt.Fprintf(a.err(), "%s. Type %q to confirm: ", action, name)
	line, err := a.readLine(true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != name {
		return failErr(ExitError, "not confirmed")
	}
	return nil
}

// confirmYN asks yes or no, unless --yes is set.
func (a *app) confirmYN(question string) error {
	if a.fl.yes {
		return nil
	}
	if !a.interactive() {
		return usageErr("%s needs a confirmation: add --yes to run it in a shell that is not interactive", question)
	}
	fmt.Fprintf(a.err(), "%s [y/N]: ", question)
	line, err := a.readLine(true)
	if err != nil {
		return err
	}
	if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
		return failErr(ExitError, "not confirmed")
	}
	return nil
}

func (a *app) readLine(echo bool) (string, error) {
	if !echo {
		if f, ok := a.opts.In.(*os.File); ok && isTerminal(f) {
			stty(f, "-echo")
			defer func() {
				stty(f, "echo")
				fmt.Fprintln(a.err())
			}()
		}
	}
	line, err := a.readStdin().ReadString('\n')
	if err != nil && line == "" {
		return "", failErr(ExitError, "no input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func stty(f *os.File, arg string) {
	c := exec.Command("stty", arg)
	c.Stdin = f
	_ = c.Run()
}

// secretSource holds the flags that give one secret value (REQ-CLI-102): a file, stdin or a prompt. Never an argument.
type secretSource struct {
	label string
	file  string
	stdin bool
}

// addSecretFlags adds --<prefix>file and --<prefix>stdin. For prefix "" the flags are --from-file and --stdin.
func addSecretFlags(cmd *cobra.Command, prefix, label string) *secretSource {
	s := &secretSource{label: label}
	if prefix == "" {
		cmd.Flags().StringVar(&s.file, "from-file", "", "read the "+label+" from a file")
		cmd.Flags().BoolVar(&s.stdin, "stdin", false, "read the "+label+" from standard input")
	} else {
		cmd.Flags().StringVar(&s.file, prefix+"-file", "", "read the "+label+" from a file")
		cmd.Flags().BoolVar(&s.stdin, prefix+"-stdin", false, "read the "+label+" from standard input")
	}
	return s
}

func (s *secretSource) set() bool { return s.file != "" || s.stdin }

// read returns the value. Without a flag it asks on a terminal, or reads standard input when it is piped.
func (a *app) readSecret(s *secretSource, required bool) (string, error) {
	switch {
	case s.file != "":
		b, err := os.ReadFile(s.file)
		if err != nil {
			return "", failErr(ExitError, "cannot read %s: %v", s.file, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	case s.stdin || !a.interactive():
		if !s.stdin && !required {
			return "", nil
		}
		b, err := io.ReadAll(a.readStdin())
		if err != nil {
			return "", err
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" && required {
			return "", usageErr("no %s on standard input", s.label)
		}
		return v, nil
	}
	if !required {
		return "", nil
	}
	return a.prompt(s.label)
}

// prompt asks for a secret without echo. It fails when no terminal is there.
func (a *app) prompt(label string) (string, error) {
	if !a.interactive() {
		return "", usageErr("the %s is needed: use the file or stdin flag", label)
	}
	fmt.Fprintf(a.err(), "%s: ", label)
	v, err := a.readLine(false)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", usageErr("empty %s", label)
	}
	return v, nil
}

// secretKeys are keys that a --set flag may not carry: values of secrets never come from arguments.
var secretKeys = map[string]bool{
	"value": true, "secret": true, "token": true, "password": true, "private_key": true,
	"client_secret": true, "webhook_secret": true, "credentials": true, "new_password": true, "current_password": true,
}

// bodyFlags give a request body from a file and from --set key=value pairs.
type bodyFlags struct {
	file string
	sets []string
}

func (b *bodyFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&b.file, "file", "f", "", "read the request fields from a JSON or YAML file (- for stdin)")
	cmd.Flags().StringArrayVar(&b.sets, "set", nil, "set a field, for example --set config.replicas=2 (the value is JSON when it can be)")
}

// build merges: the file, then the fields from flags, then the --set pairs.
func (b *bodyFlags) build(a *app, fromFlags map[string]any) (map[string]any, error) {
	body := map[string]any{}
	if b.file != "" {
		var raw []byte
		var err error
		if b.file == "-" {
			raw, err = io.ReadAll(a.readStdin())
		} else {
			raw, err = os.ReadFile(b.file)
		}
		if err != nil {
			return nil, failErr(ExitError, "cannot read %s: %v", b.file, err)
		}
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, usageErr("%s is not valid JSON or YAML: %v", b.file, err)
		}
		m, ok := normalize(doc).(map[string]any)
		if !ok {
			return nil, usageErr("%s must hold one object", b.file)
		}
		body = m
	}
	for k, v := range fromFlags {
		setPath(body, k, v)
	}
	for _, kv := range b.sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, usageErr("--set needs key=value, got %q", kv)
		}
		parts := strings.Split(k, ".")
		if secretKeys[parts[len(parts)-1]] || secretKeys[parts[0]] {
			return nil, usageErr("the field %q holds a secret: give it with a file or stdin, not with --set", k)
		}
		setPath(body, k, parseValue(v))
	}
	return body, nil
}

// normalize turns YAML maps into maps with string keys.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case map[any]any:
		m := map[string]any{}
		for k, e := range x {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
		return x
	}
	return v
}

func parseValue(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

func setPath(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// parseDuration reads Go durations and the units d (day) and w (week).
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if strings.HasSuffix(s, suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, suffix), 64)
			if err != nil {
				return 0, usageErr("not a duration: %q", s)
			}
			return time.Duration(n * float64(unit)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, usageErr("not a duration: %q (examples: 30s, 2h, 7d)", s)
	}
	return d, nil
}

// since turns "1h" into an RFC 3339 time that far in the past.
func sinceTime(s string) (string, error) {
	d, err := parseDuration(s)
	if err != nil || d == 0 {
		return "", err
	}
	return time.Now().Add(-d).UTC().Format(time.RFC3339), nil
}

// kvPairs reads k=v arguments.
func kvPairs(args []string) (map[string]string, error) {
	m := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, usageErr("expected key=value, got %q", a)
		}
		m[k] = v
	}
	return m, nil
}

func itoa(n int) string { return strconv.Itoa(n) }
