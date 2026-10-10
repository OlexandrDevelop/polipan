// SPDX-License-Identifier: Apache-2.0

// Package vars has the rules of variables and secrets (C05): names, scopes,
// ${{NAME}} references, cycle checks, resolution and the .env / JSON bulk parsers.
// Only names and plain values are handled here. Secret values never pass through this package.
package vars

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Limits of REQ-ENV-002.
const (
	MaxNameLen    = 128
	MaxValueBytes = 64 * 1024
)

// ReservedPrefix is the prefix of the names that Polipan sets (REQ-ENV-010).
const ReservedPrefix = "POLIPAN_"

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// refRe finds ${{NAME}} and ${{db.name.FIELD}} references. Spaces inside the braces are allowed.
var refRe = regexp.MustCompile(`\$\{\{\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*\}\}`)

// Errors of the checks.
var (
	ErrBadName  = errors.New("name must match [A-Za-z_][A-Za-z0-9_]* and have at most 128 characters")
	ErrReserved = errors.New("names that start with POLIPAN_ are set by Polipan")
	ErrTooLong  = errors.New("value is longer than 64 KiB")
)

// CheckName checks a variable or secret name (REQ-ENV-002, REQ-ENV-010).
func CheckName(name string) error {
	if len(name) > MaxNameLen || !nameRe.MatchString(name) {
		return ErrBadName
	}
	if strings.HasPrefix(strings.ToUpper(name), ReservedPrefix) {
		return ErrReserved
	}
	return nil
}

// CheckValue checks the size of a value.
func CheckValue(value string) error {
	if len(value) > MaxValueBytes {
		return ErrTooLong
	}
	return nil
}

// References returns the names referenced in a value, in order, without repeats.
func References(value string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(value, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Expand replaces every reference with lookup(name). A name that lookup does not know stays as it is.
// The replaced text is not scanned again.
func Expand(value string, lookup func(name string) (string, bool)) string {
	return refRe.ReplaceAllStringFunc(value, func(m string) string {
		name := refRe.FindStringSubmatch(m)[1]
		if v, ok := lookup(name); ok {
			return v
		}
		return m
	})
}

// Entry is one variable or secret row. For a secret, Value is empty.
type Entry struct {
	ID            string
	Name          string
	Value         string
	Secret        bool
	Version       int // secret: the current version
	EnvironmentID string
	ServiceID     string
	Preview       bool
	BuildTime     bool
}

// level is the scope rank: project 0, environment 1, service 2.
func (e Entry) level() int {
	switch {
	case e.ServiceID != "":
		return 2
	case e.EnvironmentID != "":
		return 1
	}
	return 0
}

// Effective picks the entries that a service sees, last wins: project, environment, service (REQ-ENV-001).
// envID or serviceID may be empty to get the view of a wider scope.
// In a preview environment only entries with preview = true are used (REQ-SEC-006).
func Effective(entries []Entry, envID, serviceID string, preview bool) map[string]Entry {
	out := map[string]Entry{}
	rank := map[string]int{}
	for _, e := range entries {
		if e.EnvironmentID != "" && e.EnvironmentID != envID {
			continue
		}
		if e.ServiceID != "" && e.ServiceID != serviceID {
			continue
		}
		if preview && !e.Preview {
			continue
		}
		l := e.level()
		if r, ok := rank[e.Name]; ok && r > l {
			continue
		}
		rank[e.Name] = l
		out[e.Name] = e
	}
	return out
}

// CycleError is a reference cycle (422 reference_cycle).
type CycleError struct{ Names []string }

func (e *CycleError) Error() string {
	return "reference cycle: " + strings.Join(e.Names, " -> ")
}

// UnresolvedError lists the names that are referenced but do not exist (422 unresolved_reference).
type UnresolvedError struct{ Names []string }

func (e *UnresolvedError) Error() string {
	return "unresolved references: " + strings.Join(e.Names, ", ")
}

// CheckCycles finds a reference cycle between the plain variables of one view.
// Missing names are not an error here: they fail later, at Release time.
func CheckCycles(view map[string]Entry) error {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(name string) error
	visit = func(name string) error {
		e, ok := view[name]
		if !ok || e.Secret {
			return nil
		}
		switch color[name] {
		case grey:
			i := len(stack) - 1
			for i > 0 && stack[i] != name {
				i--
			}
			return &CycleError{Names: append(append([]string{}, stack[i:]...), name)}
		case black:
			return nil
		}
		color[name] = grey
		stack = append(stack, name)
		for _, ref := range References(e.Value) {
			if err := visit(ref); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		color[name] = black
		return nil
	}
	for _, name := range sortedNames(view) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// Result is a resolved view. It never holds a secret value.
type Result struct {
	// Plain are the variables with their final value (the variable_snapshot).
	Plain map[string]string
	// Secrets are the secrets of the view by name.
	Secrets map[string]Entry
	// Templates are variables whose value uses a secret. The plain references are already
	// replaced, the secret references stay as ${{NAME}} and are filled in when the workload is built.
	Templates map[string]string
	// BuildTime lists the names marked build_time (for the build step).
	BuildTime map[string]bool
}

// Resolve replaces the references of the view. Missing names (and database references,
// which come with M4) give an UnresolvedError. A cycle gives a CycleError.
func Resolve(view map[string]Entry) (*Result, error) {
	if err := CheckCycles(view); err != nil {
		return nil, err
	}
	res := &Result{Plain: map[string]string{}, Secrets: map[string]Entry{}, Templates: map[string]string{}, BuildTime: map[string]bool{}}
	missing := map[string]bool{}
	type done struct {
		value      string
		usesSecret bool
	}
	memo := map[string]done{}
	var resolve func(name string) done
	resolve = func(name string) done {
		if d, ok := memo[name]; ok {
			return d
		}
		e := view[name]
		if e.Secret {
			return done{"${{" + name + "}}", true}
		}
		uses := false
		v := Expand(e.Value, func(ref string) (string, bool) {
			if _, ok := view[ref]; !ok {
				missing[ref] = true
				return "", false
			}
			d := resolve(ref)
			uses = uses || d.usesSecret
			return d.value, true
		})
		d := done{v, uses}
		memo[name] = d
		return d
	}
	for _, name := range sortedNames(view) {
		e := view[name]
		if e.BuildTime {
			res.BuildTime[name] = true
		}
		if e.Secret {
			res.Secrets[name] = e
			continue
		}
		d := resolve(name)
		if d.usesSecret {
			res.Templates[name] = d.value
		} else {
			res.Plain[name] = d.value
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, &UnresolvedError{Names: names}
	}
	return res, nil
}

func sortedNames(view map[string]Entry) []string {
	names := make([]string, 0, len(view))
	for n := range view {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// KV is one item of a bulk import.
type KV struct {
	Name  string
	Value string
}

// ParseDotenv reads .env text: NAME=value lines, # comments, an optional "export ",
// single quotes (literal) and double quotes (with \n, \", \\ escapes, may span lines).
func ParseDotenv(text string) ([]KV, error) {
	var out []KV
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !nameRe.MatchString(name) {
			return nil, fmt.Errorf("line %d: expected NAME=value", i+1)
		}
		value = strings.TrimLeft(value, " \t")
		switch {
		case strings.HasPrefix(value, `"`):
			// A double-quoted value may go over more lines.
			buf := value[1:]
			for !closedQuote(buf) {
				if i+1 >= len(lines) {
					return nil, fmt.Errorf("line %d: missing closing quote", i+1)
				}
				i++
				buf += "\n" + lines[i]
			}
			end := closingIndex(buf)
			value = unescape(buf[:end])
		case strings.HasPrefix(value, `'`):
			end := strings.Index(value[1:], `'`)
			if end < 0 {
				return nil, fmt.Errorf("line %d: missing closing quote", i+1)
			}
			value = value[1 : end+1]
		default:
			if j := strings.Index(value, " #"); j >= 0 {
				value = value[:j]
			}
			value = strings.TrimSpace(value)
		}
		out = append(out, KV{Name: name, Value: value})
	}
	return out, nil
}

// closingIndex returns the index of the first double quote that is not escaped, or -1.
func closingIndex(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func closedQuote(s string) bool { return closingIndex(s) >= 0 }

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ParseJSON reads a JSON object of names to values. Numbers and booleans become text.
func ParseJSON(data []byte) ([]KV, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keep 3000 as "3000", not "3e+03"
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("not a JSON object: %v", err)
	}
	out := make([]KV, 0, len(m))
	for k, v := range m {
		var s string
		switch t := v.(type) {
		case string:
			s = t
		case nil:
			s = ""
		case map[string]any, []any:
			return nil, fmt.Errorf("%s: value must be text, a number or a boolean", k)
		default:
			s = fmt.Sprint(t)
		}
		out = append(out, KV{Name: k, Value: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
