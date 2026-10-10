// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"

	"github.com/polipan/polipan/api"
)

// Parsed is a file that passed the schema. Lines keeps the place of every key for later checks.
type Parsed struct {
	File *File
	// Doc is the file as plain maps and lists.
	Doc map[string]any
	// Raw is the text of the file.
	Raw   []byte
	lines *yaml.Node
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

// compiled returns the compiled polipan.schema.json. The server and the CLI use the same file (REQ-CAC-100).
func compiled() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(api.ConfigSchema)))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		if err := c.AddResource("polipan.schema.json", doc); err != nil {
			schemaErr = err
			return
		}
		schema, schemaErr = c.Compile("polipan.schema.json")
	})
	return schema, schemaErr
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// Parse reads a polipan.yaml: YAML with line numbers, then the JSON schema, then the checks that need no live
// state (07 section 6). Every issue has a line and a path. Unknown keys are errors (REQ-CAC-007).
func Parse(content []byte) (*Parsed, []Issue) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		line := 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		return nil, []Issue{{Line: line, Path: "", Message: "the file is not valid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, []Issue{{Line: 1, Message: "the file is empty"}}
	}
	var doc any
	if err := root.Decode(&doc); err != nil {
		line := 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		return nil, []Issue{{Line: line, Message: strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, []Issue{{Line: 1, Message: "keys must be text: " + err.Error()}}
	}
	p := &Parsed{Raw: content, lines: &root}
	if issues := p.validateSchema(raw); len(issues) > 0 {
		return nil, issues
	}
	var plain map[string]any
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, []Issue{{Line: 1, Message: err.Error()}}
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, []Issue{{Line: 1, Message: "the file cannot be read: " + err.Error()}}
	}
	p.File, p.Doc = &f, plain
	if issues := p.checkStatic(); len(issues) > 0 {
		return nil, issues
	}
	return p, nil
}

func (p *Parsed) validateSchema(raw []byte) []Issue {
	s, err := compiled()
	if err != nil {
		return []Issue{{Line: 1, Message: "the schema cannot be loaded: " + err.Error()}}
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return []Issue{{Line: 1, Message: err.Error()}}
	}
	verr := s.Validate(inst)
	if verr == nil {
		return nil
	}
	ve, ok := verr.(*jsonschema.ValidationError)
	if !ok {
		return []Issue{{Line: 1, Message: verr.Error()}}
	}
	pr := message.NewPrinter(language.English)
	seen := map[string]bool{}
	var out []Issue
	add := func(path []string, msg string) {
		i := Issue{Line: p.lineOf(path), Path: strings.Join(path, "."), Message: msg}
		if key := i.Path + "|" + i.Message; !seen[key] {
			seen[key] = true
			out = append(out, i)
		}
	}
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}
		if ap, ok := e.ErrorKind.(*kind.AdditionalProperties); ok {
			for _, name := range ap.Properties {
				add(append(append([]string{}, e.InstanceLocation...), name), fmt.Sprintf("unknown key %q", name))
			}
			return
		}
		add(e.InstanceLocation, e.ErrorKind.LocalizedString(pr))
	}
	walk(ve)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// lineOf finds the line of a path in the YAML. A path that is not found gives the line of the nearest parent.
// A key gives the line of the key.
func (p *Parsed) lineOf(path []string) int {
	n := p.lines
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	line := n.Line
	for _, seg := range path {
		l, next := step(n, seg)
		if next == nil {
			break
		}
		line, n = l, next
	}
	return max(line, 1)
}

// step goes one segment down. It returns the line of the key (or of the list item) and the value node.
func step(n *yaml.Node, seg string) (int, *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				return n.Content[i].Line, n.Content[i+1]
			}
		}
	case yaml.SequenceNode:
		if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(n.Content) {
			return n.Content[i].Line, n.Content[i]
		}
	}
	return 0, nil
}
