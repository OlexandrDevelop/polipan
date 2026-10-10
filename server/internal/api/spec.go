// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// BasePath is the base path of the public API.
const BasePath = "/api/v1"

// Operation is one operation of the OpenAPI file.
type Operation struct {
	Method      string // upper case, for example GET
	Path        string // path in the file, for example /services/{service_id}
	OperationID string
	Summary     string
	Permission  string // value of x-permission, empty when missing
}

// FullPath is the path with the base path.
func (o Operation) FullPath() string { return BasePath + o.Path }

var httpMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true, "options": true, "trace": true}

// ParseOperations reads all operations of an OpenAPI file, sorted by path and method.
func ParseOperations(spec []byte) ([]Operation, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	var ops []Operation
	for path, item := range doc.Paths {
		for method, node := range item {
			if !httpMethods[method] {
				continue
			}
			var o struct {
				OperationID string `yaml:"operationId"`
				Summary     string `yaml:"summary"`
				Permission  string `yaml:"x-permission"`
			}
			if err := node.Decode(&o); err != nil {
				return nil, fmt.Errorf("openapi: %s %s: %w", method, path, err)
			}
			ops = append(ops, Operation{
				Method: strings.ToUpper(method), Path: path,
				OperationID: o.OperationID, Summary: o.Summary, Permission: o.Permission,
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops, nil
}
