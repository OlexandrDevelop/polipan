// SPDX-License-Identifier: Apache-2.0

// Package repo_test has the checks of the repository itself: license, governance files, release pipeline and
// the rule that a default install calls no host of the project (F11, AC-OSS-01 to AC-OSS-06).
package repo_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func run(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, out)
	}
}

// AC-OSS-01: LICENSE is Apache-2.0 and every source file has an SPDX header.
func TestAC_OSS_01_LicenseAndSPDXHeaders(t *testing.T) {
	lic := read(t, "LICENSE")
	if !strings.Contains(lic, "Apache License") || !strings.Contains(lic, "Version 2.0, January 2004") {
		t.Fatal("LICENSE is not Apache-2.0")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is needed to list the files")
	}
	run(t, "test/ci/check-spdx.sh")
}

// AC-OSS-02: the license job of CI runs the license checks, and their allow lists have no copyleft license.
// A dependency with another license makes go-licenses and the web check fail.
func TestAC_OSS_02_LicenseJobRefusesCopyleft(t *testing.T) {
	goScript := read(t, "test/ci/check-licenses-go.sh")
	allow := regexp.MustCompile(`--allowed_licenses=\S+`).FindString(goScript)
	if allow == "" {
		t.Fatal("check-licenses-go.sh has no allow list")
	}
	for _, bad := range []string{"GPL", "AGPL", "SSPL", "BUSL"} {
		if strings.Contains(strings.ToUpper(allow), bad) {
			t.Fatalf("the Go license check allows %s", bad)
		}
	}
	// The web check refuses these licenses by name.
	web := read(t, "test/ci/check-licenses-web.sh")
	if !strings.Contains(web, "GPL|AGPL|SSPL|BUSL") || !strings.Contains(web, "exit 1") {
		t.Fatal("the web license check does not refuse copyleft licenses")
	}
	if !strings.Contains(read(t, "Makefile"), "sh test/ci/check-licenses-go.sh") || !strings.Contains(read(t, ".github/workflows/ci.yml"), "make gate8") {
		t.Fatal("the license job does not run the checks")
	}
}

// AC-OSS-03: the release workflow signs the artifacts (cosign keyless), makes checksums and an SBOM, and the
// installer checks the checksums. The scripts are run by `make release-test`.
func TestAC_OSS_03_ReleasePipelineSignsAndPublishesSBOM(t *testing.T) {
	wf := read(t, ".github/workflows/release.yml")
	for _, want := range []string{"cosign", "sbom", "sha256sum", "id-token: write"} {
		if !strings.Contains(strings.ToLower(wf), strings.ToLower(want)) {
			t.Errorf("release.yml has no %q", want)
		}
	}
	if inst := read(t, "deploy/install.sh"); !strings.Contains(inst, "sha256sum") && !strings.Contains(inst, "shasum") {
		t.Error("install.sh does not check checksums")
	}
	if _, err := os.Stat(filepath.Join(root, "test/release/test.sh")); err != nil {
		t.Error("the release script test is missing")
	}
}

// AC-OSS-04: no source of the three binaries has the address of a host of the project (a fresh install makes
// no request home). The only address is the download of install.sh, which the user starts by hand.
func TestAC_OSS_04_NoRequestToAProjectHost(t *testing.T) {
	url := regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)
	var found []string
	for _, dir := range []string{"server", "agent", "cli"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".gen.go") {
				return nil
			}
			raw, _ := os.ReadFile(p)
			for _, m := range url.FindAllStringSubmatch(string(raw), -1) {
				if h := strings.ToLower(m[1]); strings.Contains(h, "polipan") && !strings.HasSuffix(h, ".internal") && !strings.HasSuffix(h, ".test") {
					found = append(found, p+": "+m[0])
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Fatalf("addresses of a project host in the code: %v", found)
	}
	// There is no update check or telemetry setting in the server config.
	cfg := strings.ToLower(read(t, "server/internal/config/config.go"))
	for _, bad := range []string{"update_check", "telemetry_url", "analytics"} {
		if strings.Contains(cfg, bad) {
			t.Fatalf("config has %q", bad)
		}
	}
}

// AC-OSS-06: the governance files exist and are not empty.
func TestAC_OSS_06_GovernanceFilesExist(t *testing.T) {
	for _, f := range []string{"CONTRIBUTING.md", "CODE_OF_CONDUCT.md", "GOVERNANCE.md", "SECURITY.md", "LICENSE",
		".github/PULL_REQUEST_TEMPLATE.md"} {
		if s := read(t, f); strings.TrimSpace(s) == "" {
			t.Errorf("%s is empty", f)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".github/ISSUE_TEMPLATE")); err != nil || len(entries) == 0 {
		t.Error("no issue templates")
	}
}

// T-382: install.sh is started as "curl | sh". The shell reads a pipe in blocks, and a command that keeps stdin open
// (docker compose exec) ate the rest of the script, so the updater was written half. These rules stop that.
func TestInstallScriptIsSafeToPipe(t *testing.T) {
	inst := read(t, "deploy/install.sh")
	lines := strings.Split(strings.TrimRight(inst, "\n"), "\n")
	if last := lines[len(lines)-1]; last != `main "$@"` {
		t.Fatalf("the last line of install.sh must be main \"$@\", it is %q", last)
	}
	if !strings.Contains(inst, "\nmain() {\n# The commands inside must not read the script from the pipe (docker compose exec would eat it).\nexec </dev/null\n") {
		t.Error("the first command of main must be exec </dev/null")
	}
	for _, want := range []string{"\nstamped_server_digest=\"", "\nstamped_version=\"", "\nregistry=\""} {
		i := strings.Index(inst, want)
		if i < 0 || i > strings.Index(inst, "\nmain() {\n") {
			t.Errorf("%q must stay at the top level, before main", strings.TrimSpace(want))
		}
	}
	if regexp.MustCompile(`systemctl enable[^\n]*polipan-update[^\n]*(>/dev/null|\|\| say)`).MatchString(inst) {
		t.Error("the update units must not be enabled with a hidden error")
	}
	if !strings.Contains(inst, "\ncheck_updater\n") {
		t.Error("install.sh does not call check_updater")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is needed")
	}
	syntax := func(body string) error {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(body)
		return cmd.Run()
	}
	if err := syntax(inst); err != nil {
		t.Fatalf("sh -n install.sh: %v", err)
	}
	// A cut copy (inside the updater heredoc) must not parse, so it runs nothing.
	if err := syntax(inst[:strings.Index(inst, "UPDATER\n")+400]); err == nil {
		t.Error("a cut copy of install.sh passes sh -n")
	}
}
