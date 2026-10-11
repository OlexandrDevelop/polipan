// SPDX-License-Identifier: Apache-2.0

package cac

import "testing"

// FuzzParse feeds polipan.yaml content from a repository, which is untrusted. It must never panic.
func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nproject: shop\nenvironments:\n  production:\n    services:\n      web:\n        image: nginx:1.27\n"))
	f.Add([]byte("a: &a [*a]\n"))
	f.Add([]byte("version: 1\nunknown: [1, 2\n"))
	f.Add([]byte("\x00\xff{{{"))
	f.Fuzz(func(t *testing.T, data []byte) {
		Parse(data)
	})
}
