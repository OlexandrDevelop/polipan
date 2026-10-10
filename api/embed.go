// SPDX-License-Identifier: Apache-2.0

// Package api holds the OpenAPI file. It is the source of truth for the public API.
package api

import _ "embed"

// Spec is the content of openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
