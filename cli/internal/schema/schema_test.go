// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The embedded schema must equal the docs file (REQ-CLI-103).
func TestEmbeddedSchemaMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../../docs/spec/schemas/polipan.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, Raw) {
		t.Fatal("cli/internal/schema/polipan.schema.json differs from docs/spec/schemas/polipan.schema.json: run go generate ./cli/internal/schema")
	}
}

func TestValidFileHasNoProblem(t *testing.T) {
	file := "version: 1\nproject: blog\nservices:\n  web:\n    source:\n      image: {name: \"nginx:1.27\"}\n    ports: [80]\n"
	problems, err := Validate([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %+v", problems)
	}
}

// AC-CAC-05: an unknown key gives an error with the line and the field.
func TestUnknownKeyHasLineAndField(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/config/unknown-key.yaml")
	if err != nil {
		t.Fatal(err)
	}
	problems, err := Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 {
		t.Fatalf("want 1 problem, got %+v", problems)
	}
	want := 0
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "not_a_real_key") {
			want = i + 1
		}
	}
	p := problems[0]
	if p.Line != want || !strings.Contains(p.Path, "not_a_real_key") {
		t.Fatalf("want line %d and field not_a_real_key, got %+v", want, p)
	}
}

func TestSyntaxError(t *testing.T) {
	problems, err := Validate([]byte("version: 1\nservices: [\n"))
	if err != nil || len(problems) != 1 || problems[0].Line == 0 {
		t.Fatalf("want one problem with a line, got %+v, %v", problems, err)
	}
}
