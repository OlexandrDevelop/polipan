// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"strings"
	"testing"
)

// REQ-PREV-005: the host is one DNS label before the preview domain. A long service name is cut and gets a 4 character hash.
func TestREQ_PREV_005_HostIsOneLabel(t *testing.T) {
	if got := Host("pr-5", "web", "Preview.Example.com."); got != "pr-5-web.preview.example.com" {
		t.Fatalf("host: %s", got)
	}
	if got := Host("pr-5", "web", ""); got != "" {
		t.Fatalf("no preview domain gives no host: %q", got)
	}
	long := strings.Repeat("a-very-long-service-name", 4)
	l := Label("pr-12345", long)
	if len(l) > 63 || strings.Contains(l, ".") || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
		t.Fatalf("label %q (%d)", l, len(l))
	}
	if l == Label("pr-12345", long+"x") {
		t.Fatal("two long names give the same label")
	}
	if got := Label("pr-5", "My_Service.v2"); got != "pr-5-my-service-v2" {
		t.Fatalf("a service name with odd characters: %s", got)
	}
	if BranchNumber("feature/x") >= 0 || BranchSlug("Feature/X y") == BranchSlug("feature/x y") {
		t.Fatal("branch previews need a negative number and a slug that depends on the exact name")
	}
}

// REQ-PREV-004: the default strategy of a preview is recreate. The limits have the defaults of the glossary.
func TestREQ_PREV_Defaults(t *testing.T) {
	var c Config
	if c.DeployStrategy() != "recreate" || c.Max() != 10 || c.TTL().Hours() != 7*24 || c.Grace().Minutes() != 60 {
		t.Fatalf("defaults: %s %d %v %v", c.DeployStrategy(), c.Max(), c.TTL(), c.Grace())
	}
	zero := 0
	c = Config{Strategy: "blue_green", CleanupGraceMinutes: &zero}
	if c.DeployStrategy() != "blue_green" || c.Grace() != 0 {
		t.Fatal("settings are not used")
	}
}

func TestSeedCommandRunsAfterPreDeploy(t *testing.T) {
	got := withSeed([]any{"npm", "run", "migrate"}, []string{"npm", "run", "it's"})
	if len(got) != 3 || got[0] != "sh" || got[2] != `'npm' 'run' 'migrate' && 'npm' 'run' 'it'\''s'` {
		t.Fatalf("combined: %v", got)
	}
	if only := withSeed(nil, []string{"./seed", "--small"}); len(only) != 2 || only[0] != "./seed" {
		t.Fatalf("seed only: %v", only)
	}
}
