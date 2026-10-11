// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"strconv"
	"strings"
	"testing"
)

// FuzzTextFilter feeds the search text of a user. The answer must be LogsQL that holds only quoted strings,
// so the text can never add a filter, a pipe or another stream.
func FuzzTextFilter(f *testing.F) {
	f.Add("error timeout")
	f.Add(`"two words" x`)
	f.Add(`/err(or)?/`)
	f.Add(`" } | delete * {"`)
	f.Fuzz(func(t *testing.T, text string) {
		q, err := textFilter(text)
		if err != nil {
			return
		}
		rest := strings.TrimPrefix(q, "~")
		for rest != "" {
			lit, err := strconv.QuotedPrefix(rest)
			if err != nil {
				t.Fatalf("%q gave %q: not only quoted strings", text, q)
			}
			rest = strings.TrimLeft(rest[len(lit):], " ")
		}
	})
}
