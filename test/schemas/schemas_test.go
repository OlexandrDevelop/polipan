// SPDX-License-Identifier: Apache-2.0

// Package schemas holds the schema checks of CI gate 5 (docs/spec/13-testing-and-quality.md).
package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	polipanSchema  = "../../docs/spec/schemas/polipan.schema.json"
	templateSchema = "../../docs/spec/schemas/template.schema.json"
	configDoc      = "../../docs/spec/07-config-as-code.md"
	unknownKeyFile = "../fixtures/config/unknown-key.yaml"
)

// compile loads a schema file. Compiling checks it against the JSON Schema 2020-12 meta-schema.
func compile(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	var head struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil || head.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("%s: $schema must be draft 2020-12, got %q", path, head.Schema)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(path, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(path)
	if err != nil {
		t.Fatalf("%s is not a valid JSON Schema 2020-12: %v", path, err)
	}
	return s
}

// yamlToValue turns YAML text into a value the validator accepts (JSON types).
func yamlToValue(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Gate 5: both schema files are valid JSON Schema 2020-12.
func TestGate5_SchemasAreValid(t *testing.T) {
	compile(t, polipanSchema)
	compile(t, templateSchema)
}

// Gate 5: the examples of 07-config-as-code.md, section 8, validate.
func TestGate5_ConfigExamplesValidate(t *testing.T) {
	schema := compile(t, polipanSchema)
	raw, err := os.ReadFile(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	_, examples, ok := strings.Cut(string(raw), "## 8. Examples")
	if !ok {
		t.Fatal("section 8 not found")
	}
	if i := strings.Index(examples, "\n## "); i >= 0 {
		examples = examples[:i]
	}
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(examples, -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d examples, want at least 3", len(blocks))
	}
	for i, b := range blocks {
		if err := schema.Validate(yamlToValue(t, b[1])); err != nil {
			t.Errorf("example %d does not validate:\n%v", i+1, err)
		}
	}
}

// Gate 5: a file with an unknown key fails.
func TestGate5_UnknownKeyFails(t *testing.T) {
	schema := compile(t, polipanSchema)
	raw, err := os.ReadFile(unknownKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(yamlToValue(t, string(raw)))
	if err == nil {
		t.Fatal("the fixture with an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "not_a_real_key") {
		t.Fatalf("the error does not name the unknown key: %v", err)
	}
	// The same file without the key is fine, so the failure comes from the key only.
	fixed := strings.ReplaceAll(string(raw), "    not_a_real_key: true\n", "")
	if err := schema.Validate(yamlToValue(t, fixed)); err != nil {
		t.Fatalf("the fixture without the unknown key is invalid: %v", err)
	}
}

// Gate 5 and AC-TPL: the 13 built-in templates in /templates validate against the template schema,
// and every service entry validates against the service shape of polipan.schema.json.
func TestGate5_BuiltinTemplatesValidate(t *testing.T) {
	tplSchema := compile(t, templateSchema)
	cfgSchema := compile(t, polipanSchema)
	files, err := filepath.Glob("../../templates/*.yaml")
	if err != nil || len(files) != 13 {
		t.Fatalf("templates: %d files, %v", len(files), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := tplSchema.Validate(yamlToValue(t, string(raw))); err != nil {
			t.Errorf("%s does not match the template schema: %v", f, err)
		}
		var doc struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		wrapped, _ := yaml.Marshal(map[string]any{"version": 1, "project": "template-check", "services": doc.Services})
		if err := cfgSchema.Validate(yamlToValue(t, string(wrapped))); err != nil {
			t.Errorf("%s: a service does not match polipan.schema.json: %v", f, err)
		}
	}
}
