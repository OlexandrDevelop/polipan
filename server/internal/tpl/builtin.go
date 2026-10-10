// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"fmt"
	"io/fs"
	"sort"

	"github.com/polipan/polipan/templates"
)

// Builtin returns the built-in templates, sorted by slug. A manifest that does not pass the
// checks is a bug of the build, so it is an error (the tests catch it).
func Builtin() ([]*Manifest, error) {
	files, err := fs.Glob(templates.FS, "*.yaml")
	if err != nil {
		return nil, err
	}
	var out []*Manifest
	for _, f := range files {
		raw, err := fs.ReadFile(templates.FS, f)
		if err != nil {
			return nil, err
		}
		m, err := ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", f, err)
		}
		if f != m.Slug+".yaml" {
			return nil, fmt.Errorf("template %s: the file name must be %s.yaml", f, m.Slug)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}
