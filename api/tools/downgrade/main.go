// SPDX-License-Identifier: Apache-2.0

// Command downgrade turns the OpenAPI 3.1 file into an OpenAPI 3.0.3 file.
// The Go code generator only reads 3.0. The spec uses only one 3.1 feature:
// type lists like [string, 'null']. This tool rewrites them to "nullable".
// Usage: downgrade <in.yaml> <out.yaml>
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: downgrade <in.yaml> <out.yaml>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "downgrade:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if err := walk(&doc); err != nil {
		return err
	}
	root := doc.Content[0]
	setScalar(root, "openapi", "3.0.3")
	if info := mapGet(root, "info"); info != nil {
		if lic := mapGet(info, "license"); lic != nil {
			mapDelete(lic, "identifier")
		}
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

func walk(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		if t := mapGet(n, "type"); t != nil && t.Kind == yaml.SequenceNode {
			var types []*yaml.Node
			nullable := false
			for _, item := range t.Content {
				if item.Value == "null" {
					nullable = true
					continue
				}
				types = append(types, item)
			}
			if len(types) != 1 {
				return fmt.Errorf("line %d: type list with %d non-null types is not supported", t.Line, len(types))
			}
			*t = *types[0]
			if nullable {
				n.Content = append(n.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "nullable"},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
			}
		}
	}
	for _, c := range n.Content {
		if err := walk(c); err != nil {
			return err
		}
	}
	return nil
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func mapDelete(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

func setScalar(n *yaml.Node, key, value string) {
	if v := mapGet(n, key); v != nil {
		v.Value = value
	}
}
