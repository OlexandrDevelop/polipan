// SPDX-License-Identifier: Apache-2.0

// Package api holds the OpenAPI file. It is the source of truth for the public API.
package api

import _ "embed"

// Spec is the content of openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte

// ConfigSchema is the content of polipan.schema.json, the schema of polipan.yaml.
// The server and the CLI validate with the same file (REQ-CAC-100).
// It is a copy of docs/spec/schemas/polipan.schema.json (make api-sync).
//
//go:embed polipan.schema.json
var ConfigSchema []byte
