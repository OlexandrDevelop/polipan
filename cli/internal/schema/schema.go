// SPDX-License-Identifier: Apache-2.0

// Package schema checks a polipan.yaml file offline with the embedded JSON Schema (REQ-CLI-103).
// The embedded file is a copy of docs/spec/schemas/polipan.schema.json. A test keeps both equal.
package schema

//go:generate cp ../../../docs/spec/schemas/polipan.schema.json polipan.schema.json

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// Raw is the embedded schema file.
//
//go:embed polipan.schema.json
var Raw []byte

// Problem is one validation error with the place in the file.
type Problem struct {
	Line    int    `json:"line"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

var (
	once     sync.Once
	compiled *jsonschema.Schema
	compErr  error
)

func compile() (*jsonschema.Schema, error) {
	once.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(Raw)))
		if err != nil {
			compErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err := c.AddResource("polipan.schema.json", doc); err != nil {
			compErr = err
			return
		}
		compiled, compErr = c.Compile("polipan.schema.json")
	})
	return compiled, compErr
}

// Validate checks the YAML text. It returns the problems, sorted by line. A YAML syntax error is one problem.
func Validate(data []byte) ([]Problem, error) {
	s, err := compile()
	if err != nil {
		return nil, fmt.Errorf("embedded schema: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		line := 1
		if m := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(err.Error()); m != nil {
			fmt.Sscan(m[1], &line)
		}
		return []Problem{{Line: line, Message: "invalid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}}, nil
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return []Problem{{Line: 1, Message: "the file is empty"}}, nil
	}
	var value any
	if err := root.Content[0].Decode(&value); err != nil {
		return []Problem{{Line: 1, Message: "invalid YAML: " + err.Error()}}, nil
	}
	value = jsonValue(value)
	verr := s.Validate(value)
	if verr == nil {
		return nil, nil
	}
	ve, ok := verr.(*jsonschema.ValidationError)
	if !ok {
		return nil, verr
	}
	pr := message.NewPrinter(language.English)
	var out []Problem
	seen := map[string]bool{}
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		loc := e.InstanceLocation
		if ap, ok := e.ErrorKind.(*kind.AdditionalProperties); ok {
			for _, p := range ap.Properties {
				full := append(append([]string{}, loc...), p)
				add(&out, seen, root.Content[0], full, "unknown key "+fmt.Sprintf("%q", p), true)
			}
			return
		}
		add(&out, seen, root.Content[0], loc, e.ErrorKind.LocalizedString(pr), false)
	}
	walk(ve)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

func add(out *[]Problem, seen map[string]bool, root *yaml.Node, loc []string, msg string, keyLine bool) {
	path := strings.Join(loc, ".")
	key := path + "|" + msg
	if seen[key] {
		return
	}
	seen[key] = true
	if path == "" {
		path = "(root)"
	}
	*out = append(*out, Problem{Line: lineOf(root, loc, keyLine), Path: path, Message: msg})
}

// lineOf finds the line of a path in the YAML tree. When the key is missing it gives the line of the nearest parent.
func lineOf(n *yaml.Node, loc []string, keyLine bool) int {
	line := n.Line
	for i, part := range loc {
		next := child(n, part)
		if next == nil {
			return line
		}
		if i == len(loc)-1 && keyLine {
			return next.key.Line
		}
		n = next.val
		line = next.key.Line
		if n.Kind == yaml.SequenceNode {
			line = n.Line
		}
	}
	return line
}

type pair struct{ key, val *yaml.Node }

func child(n *yaml.Node, part string) *pair {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == part {
				return &pair{n.Content[i], n.Content[i+1]}
			}
		}
	case yaml.SequenceNode:
		var idx int
		if _, err := fmt.Sscan(part, &idx); err == nil && idx >= 0 && idx < len(n.Content) {
			return &pair{n.Content[idx], n.Content[idx]}
		}
	}
	return nil
}

// jsonValue turns YAML decoded values into types the validator accepts.
func jsonValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonValue(e)
		}
		return x
	case map[any]any:
		m := map[string]any{}
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonValue(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonValue(e)
		}
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case uint64:
		return float64(x)
	default:
		return v
	}
}
