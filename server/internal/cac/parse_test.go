// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtures = "../../../test/fixtures/config/"

// REQ-CAC-103: the three examples of 07 section 8 validate.
func TestREQ_CAC_103_ExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob(fixtures + "example-*.yaml")
	if len(files) != 3 {
		t.Fatalf("found %d example fixtures", len(files))
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if _, issues := Parse(raw); len(issues) > 0 {
			t.Errorf("%s: %v", f, issues)
		}
	}
}

// AC-CAC-05 and REQ-CAC-103: an unknown key fails with the line and the field.
func TestAC_CAC_05_UnknownKeyHasLineAndField(t *testing.T) {
	raw, _ := os.ReadFile(fixtures + "unknown-key.yaml")
	_, issues := Parse(raw)
	if len(issues) != 1 {
		t.Fatalf("issues: %v", issues)
	}
	i := issues[0]
	if i.Line != 9 || i.Path != "services.web.not_a_real_key" || !strings.Contains(i.Message, "not_a_real_key") {
		t.Fatalf("issue: %+v", i)
	}
}

func TestSemanticChecks(t *testing.T) {
	cases := map[string]string{
		"depends_on": "version: 1\nproject: a\nservices:\n  web:\n    source: {image: {name: x}}\n    depends_on: [nope]\n",
		"not exposed": "version: 1\nproject: a\nservices:\n  web:\n    source: {image: {name: x}}\n    ports: [80]\n    domains: [{host: a.example.com, port: 81}]\n",
		"volume": "version: 1\nproject: a\nservices:\n  web:\n    source: {image: {name: x}}\n    volumes: [{name: d, path: /d}]\n    deploy: {strategy: blue_green}\n",
		"twice": "version: 1\nproject: a\nservices:\n  a:\n    source: {image: {name: x}}\n    ports: [80]\n    domains: [{host: a.example.com}]\n  b:\n    source: {image: {name: x}}\n    ports: [80]\n    domains: [{host: a.example.com}]\n",
		"override": "version: 1\nproject: a\nservices:\n  a:\n    source: {image: {name: x}}\nenvironments:\n  production:\n    services:\n      ghost: {replicas: 1}\n",
	}
	for name, doc := range cases {
		_, issues := Parse([]byte(doc))
		if len(issues) == 0 || issues[0].Line == 0 {
			t.Errorf("%s: issues %v", name, issues)
		}
	}
}
