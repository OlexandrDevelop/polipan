// SPDX-License-Identifier: Apache-2.0

// Package mask hides secret values in text before it is stored (REQ-SEC-007, REQ-APP-007).
// It is a safety net, not a guarantee: only exact matches of the known values are found.
package mask

import (
	"sort"
	"strings"
)

// Replacement is what a secret value becomes.
const Replacement = "***"

// minLen is the shortest value that is masked. A one-letter value would hide too much text.
const minLen = 3

// Masker replaces known secret values.
type Masker struct {
	values []string
}

// New makes a Masker. Empty and very short values are ignored. Longer values are replaced first.
func New(values []string) *Masker {
	seen := map[string]bool{}
	var vs []string
	for _, v := range values {
		if len(v) < minLen || seen[v] {
			continue
		}
		seen[v] = true
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return &Masker{values: vs}
}

// Mask returns s with every known value replaced by ***.
func (m *Masker) Mask(s string) string {
	if m == nil {
		return s
	}
	for _, v := range m.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Replacement)
		}
	}
	return s
}

// Add adds more values. It is not safe to call while Mask runs in another goroutine.
func (m *Masker) Add(values ...string) {
	merged := New(append(append([]string{}, m.values...), values...))
	m.values = merged.values
}
