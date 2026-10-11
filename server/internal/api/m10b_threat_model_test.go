// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestM10b_ThreatModelRowsAreCovered walks the table of docs/spec/10-security.md section 6. Every row must be in
// docs/operations/threat-model-coverage.md with tests that exist, or with an accepted risk that is written in the
// draft release notes.
func TestM10b_ThreatModelRowsAreCovered(t *testing.T) {
	read := func(p string) string {
		raw, err := os.ReadFile(filepath.Join("../../..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	row := regexp.MustCompile(`(?m)^\| (\d+) \|`)
	spec := read("docs/spec/10-security.md")
	spec = spec[strings.Index(spec, "## 6. Threat model"):]
	spec = spec[:strings.Index(spec, "## Requirements")]
	var rows []string
	for _, m := range row.FindAllStringSubmatch(spec, -1) {
		rows = append(rows, m[1])
	}
	if len(rows) != 24 {
		t.Fatalf("the threat model has %d rows, expected 24", len(rows))
	}
	// All test function names of the repository.
	defined := map[string]bool{}
	fn := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)
	_ = filepath.WalkDir("../../..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".claude" || d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, "_test.go") {
			if raw, err := os.ReadFile(p); err == nil {
				for _, m := range fn.FindAllStringSubmatch(string(raw), -1) {
					defined[m[1]] = true
				}
			}
		}
		return nil
	})
	notes := read("docs/operations/release-notes-1.0.md")
	covered := map[string]bool{}
	for _, line := range strings.Split(read("docs/operations/threat-model-coverage.md"), "\n") {
		cols := strings.Split(line, "|")
		if len(cols) < 5 || !row.MatchString(line) {
			continue
		}
		id := strings.TrimSpace(cols[1])
		tests := 0
		for _, name := range strings.Split(cols[2], ",") {
			if name = strings.TrimSpace(name); name != "" {
				if !defined[name] {
					t.Errorf("row %s: the test %s does not exist", id, name)
				}
				tests++
			}
		}
		risks := 0
		for _, ar := range strings.Split(cols[3], ",") {
			if ar = strings.TrimSpace(ar); ar != "" {
				if !strings.Contains(notes, "| "+ar+" |") {
					t.Errorf("row %s: the accepted risk %s is not in the release notes", id, ar)
				}
				risks++
			}
		}
		if tests+risks == 0 {
			t.Errorf("row %s has no test and no accepted risk", id)
		}
		covered[id] = true
	}
	for _, id := range rows {
		if !covered[id] {
			t.Errorf("row %s of the threat model is not in the coverage table", id)
		}
	}
}
