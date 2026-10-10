// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/ssrf"
)

// R5: a host with shell or ssh option characters is refused and never reaches GIT_SSH_COMMAND.
func TestCloneRefusesBadHost(t *testing.T) {
	bad := []string{
		"ssh://git@evil.com'$(touch${IFS}/tmp/x)'/o/r.git",
		"ssh://git@a;b/o/r.git",
		"ssh://git@a`id`/o/r.git",
		"git@a$(id):o/r.git",
		"git@-oProxyCommand=x:o/r.git",
		"ssh://-oProxyCommand=x@host/o/r.git",
		"git@host'x:o/r.git",
	}
	for _, u := range bad {
		_, err := CLI{}.Clone(context.Background(), CloneRequest{URL: u, Branch: "main", SSHKey: "k", Dir: t.TempDir()})
		if err == nil || !IsCloneError(err) {
			t.Errorf("%q: want a refusal, got %v", u, err)
		}
	}
	for _, h := range []string{"github.com", "git.example.org", "10.0.0.1", "::1", "my-host_1.local"} {
		if !validHost(h) {
			t.Errorf("host %q should be valid", h)
		}
	}
	for _, h := range []string{"", "a b", "a;b", "a'b", "a$b", "a`b", "-x", "a\nb", "a\"b", "a|b"} {
		if validHost(h) {
			t.Errorf("host %q should be refused", h)
		}
	}
}

func TestSSHCommandQuotes(t *testing.T) {
	c := sshCommand("/tmp/a b/key", "/tmp/a b/kh", "git.example.org", false)
	for _, want := range []string{"'/tmp/a b/key'", "'/tmp/a b/kh'", "HostKeyAlias='git.example.org'", "StrictHostKeyChecking=accept-new"} {
		if !strings.Contains(c, want) {
			t.Errorf("%q not in %q", want, c)
		}
	}
	if c := sshCommand("k", "h", "x", true); !strings.Contains(c, "StrictHostKeyChecking=yes") {
		t.Errorf("pinned command is not strict: %q", c)
	}
}

// R6: the key learned by ssh is read back as "type base64", and a pinned key makes the check strict.
func TestReadHostKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "known_hosts")
	if ReadHostKey(f) != "" {
		t.Fatal("a missing file has no key")
	}
	if err := os.WriteFile(f, []byte("# c\ngit.example.org ssh-ed25519 AAAAC3Nza comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadHostKey(f); got != "ssh-ed25519 AAAAC3Nza" {
		t.Fatalf("got %q", got)
	}
}

// R7: a token is never sent over http.
func TestCloneRefusesTokenOverHTTP(t *testing.T) {
	_, err := CLI{}.Clone(context.Background(), CloneRequest{URL: "http://git.example.org/o/r.git", Branch: "main",
		Username: "u", Password: "tok", Dir: t.TempDir(), Policy: ssrf.Policy{}})
	if err == nil || !strings.Contains(err.Error(), "http") || !IsCloneError(err) {
		t.Fatalf("want a refusal, got %v", err)
	}
}
