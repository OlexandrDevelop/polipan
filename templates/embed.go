// SPDX-License-Identifier: Apache-2.0

// Package templates has the built-in templates of Polipan (REQ-TPL-004). The manifests are
// embedded in the server binary.
package templates

import "embed"

// FS holds one manifest per template: <slug>.yaml.
//
//go:embed *.yaml
var FS embed.FS
