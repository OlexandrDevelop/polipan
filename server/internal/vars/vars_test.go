// SPDX-License-Identifier: Apache-2.0

package vars

import (
	"errors"
	"reflect"
	"testing"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"A", "_x", "LOG_LEVEL", "a1"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", string(make([]byte, 129))} {
		if CheckName(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
	if err := CheckName("POLIPAN_NODE"); !errors.Is(err, ErrReserved) {
		t.Errorf("reserved: %v", err)
	}
	if CheckValue(string(make([]byte, MaxValueBytes+1))) == nil {
		t.Error("a value over 64 KiB passed")
	}
}

// AC-ENV-01 (unit): project, environment and service scopes, last wins; previews see only preview values.
func TestAC_ENV_01_EffectiveScopes(t *testing.T) {
	entries := []Entry{
		{Name: "A", Value: "1"},
		{Name: "A", Value: "2", EnvironmentID: "env1"},
		{Name: "B", Value: "p", Preview: true},
		{Name: "C", Value: "s", EnvironmentID: "env1", ServiceID: "svc1"},
		{Name: "A", Value: "other", EnvironmentID: "env2"},
	}
	v := Effective(entries, "env1", "svc1", false)
	if v["A"].Value != "2" || v["B"].Value != "p" || v["C"].Value != "s" || len(v) != 3 {
		t.Fatalf("view: %+v", v)
	}
	if v := Effective(entries, "env1", "svc2", false); v["A"].Value != "2" || len(v) != 2 {
		t.Fatalf("other service: %+v", v)
	}
	if v := Effective(entries, "env1", "svc1", true); len(v) != 1 || v["B"].Value != "p" {
		t.Fatalf("preview view: %+v", v)
	}
}

// AC-ENV-04 (unit): a cycle is found, a missing name is not a cycle but fails at resolve.
func TestAC_ENV_04_CyclesAndReferences(t *testing.T) {
	view := map[string]Entry{
		"A": {Name: "A", Value: "${{B}}"},
		"B": {Name: "B", Value: "x-${{ A }}"},
	}
	var cycle *CycleError
	if err := CheckCycles(view); !errors.As(err, &cycle) || len(cycle.Names) != 3 {
		t.Fatalf("cycle: %v", err)
	}
	view = map[string]Entry{
		"URL":  {Name: "URL", Value: "postgres://u:${{PW}}@${{HOST}}/db"},
		"HOST": {Name: "HOST", Value: "${{BASE}}.local"},
		"BASE": {Name: "BASE", Value: "db"},
		"PW":   {Name: "PW", Secret: true, ID: "sec_1", Version: 2},
		"SELF": {Name: "SELF", Value: "${{SELF_X}}"},
	}
	var unresolved *UnresolvedError
	if _, err := Resolve(view); !errors.As(err, &unresolved) || !reflect.DeepEqual(unresolved.Names, []string{"SELF_X"}) {
		t.Fatalf("unresolved: %v", err)
	}
	delete(view, "SELF")
	res, err := Resolve(view)
	if err != nil {
		t.Fatal(err)
	}
	if res.Plain["HOST"] != "db.local" || res.Templates["URL"] != "postgres://u:${{PW}}@db.local/db" || res.Secrets["PW"].Version != 2 {
		t.Fatalf("result: %+v", res)
	}
	if _, ok := res.Plain["URL"]; ok {
		t.Fatal("a value that uses a secret must not be in the plain snapshot")
	}
	got := Expand(res.Templates["URL"], func(n string) (string, bool) { return "s3cr${{x}}", n == "PW" })
	if got != "postgres://u:s3cr${{x}}@db.local/db" {
		t.Fatalf("expand: %s", got)
	}
	if _, err := Resolve(map[string]Entry{"D": {Name: "D", Value: "${{db.main.URL}}"}}); !errors.As(err, &unresolved) {
		t.Fatalf("database reference: %v", err)
	}
}

func TestParseDotenv(t *testing.T) {
	text := "# comment\nexport A=1\nB = two words # note\nC='lit ${{X}} \\n'\nD=\"line1\\nline2 \\\"q\\\"\"\nE=\"multi\nline\"\n\nF=\n"
	got, err := ParseDotenv(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []KV{{"A", "1"}, {"B", "two words"}, {"C", "lit ${{X}} \\n"}, {"D", "line1\nline2 \"q\""}, {"E", "multi\nline"}, {"F", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"NOEQUALS", "1A=x", "A=\"open"} {
		if _, err := ParseDotenv(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestParseJSON(t *testing.T) {
	got, err := ParseJSON([]byte(`{"PORT": 3000, "DEBUG": true, "NAME": "x", "EMPTY": null}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []KV{{"DEBUG", "true"}, {"EMPTY", ""}, {"NAME", "x"}, {"PORT", "3000"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{`[]`, `{"A": {"b": 1}}`, `nope`} {
		if _, err := ParseJSON([]byte(bad)); err == nil {
			t.Errorf("%s passed", bad)
		}
	}
}
