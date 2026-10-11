// SPDX-License-Identifier: Apache-2.0

package tpl_test

import (
	"testing"

	"github.com/polipan/polipan/server/internal/tpl"
)

// FuzzImportCompose feeds a Compose file of a user. It must never panic.
func FuzzImportCompose(f *testing.F) {
	f.Add([]byte("services:\n  web:\n    image: nginx\n    ports: ['80:80']\n"))
	f.Add([]byte("services:\n  web:\n    image: x\n    privileged: true\n    volumes: ['/var/run/docker.sock:/s']\n"))
	f.Add([]byte("services: {a: {build: .}}"))
	f.Add([]byte("services: [1]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = tpl.ImportCompose(data)
	})
}

// FuzzParseManifest feeds a template manifest. It must never panic.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte("id: demo\nname: Demo\nversion: 1\nservices: []\n"))
	f.Add([]byte("services:\n  - name: a\n    config:\n      privileged: true\n"))
	f.Add([]byte("{\"id\": null}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = tpl.ParseManifest(data)
	})
}
