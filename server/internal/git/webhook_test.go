// SPDX-License-Identifier: Apache-2.0

package git_test

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/polipan/polipan/server/internal/git"
)

const pushGitHub = `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"full_name":"Acme/Shop"},"pusher":{"name":"ann"},"head_commit":{"author":{"name":"Ann Lee"}}}`
const pushGitLab = `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","checkout_sha":"0123456789abcdef0123456789abcdef01234567","user_name":"ann","project":{"path_with_namespace":"acme/shop"}}`

// AC-APP-03 (signature part): each provider's signature verifies with the right secret, and a
// wrong secret, a changed body or a missing header is refused.
func TestAC_APP_03_WebhookSignatures(t *testing.T) {
	secret := []byte("s3cret")
	for _, p := range []string{git.GitHub, git.GitLab, git.Gitea, git.Generic} {
		body := []byte(pushGitHub)
		if p == git.GitLab {
			body = []byte(pushGitLab)
		}
		h := git.SignatureHeaders(p, secret, body, "d-1")
		if err := git.Verify(p, secret, h, body); err != nil {
			t.Fatalf("%s: valid signature refused: %v", p, err)
		}
		if err := git.Verify(p, []byte("other"), h, body); err == nil {
			t.Fatalf("%s: wrong secret accepted", p)
		}
		if p != git.GitLab {
			if err := git.Verify(p, secret, h, append(body, ' ')); err == nil {
				t.Fatalf("%s: changed body accepted", p)
			}
		}
		if err := git.Verify(p, secret, http.Header{}, body); err == nil {
			t.Fatalf("%s: missing header accepted", p)
		}
		if err := git.Verify(p, nil, h, body); err == nil {
			t.Fatalf("%s: empty secret accepted", p)
		}
		d, err := git.Parse(p, h, body)
		if err != nil || d.ID != "d-1" || d.Event != "push" || d.Push == nil {
			t.Fatalf("%s: parse %+v %v", p, d, err)
		}
		if d.Push.Repo != "acme/shop" || d.Push.Branch != "main" || d.Push.Commit != "0123456789abcdef0123456789abcdef01234567" || d.Push.Deleted {
			t.Fatalf("%s: push %+v", p, d.Push)
		}
	}
	// A deleted branch and a delivery without an id (the body hash is the id).
	del := `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true,"repository":{"full_name":"acme/shop"}}`
	h := http.Header{"X-Github-Event": {"push"}}
	d, err := git.Parse(git.GitHub, h, []byte(del))
	if err != nil || !d.Push.Deleted || !strings.HasPrefix(d.ID, "sha256:") {
		t.Fatalf("deleted: %+v %v", d, err)
	}
}

// Fuzz: Verify and Parse never panic, and a body is only accepted with its own signature.
func FuzzAC_APP_03_Webhook(f *testing.F) {
	f.Add([]byte(pushGitHub), "github", "sha256=00")
	f.Add([]byte(pushGitLab), "gitlab", "x")
	f.Add([]byte(`{"ref":1}`), "gitea", "zz")
	f.Add([]byte(`not json`), "generic", "")
	f.Fuzz(func(t *testing.T, body []byte, provider, sig string) {
		secret := []byte("fuzz-secret")
		h := http.Header{}
		h.Set("X-Hub-Signature-256", sig)
		h.Set("X-Gitea-Signature", sig)
		h.Set("X-Gitlab-Token", sig)
		h.Set("X-Github-Event", "push")
		h.Set("X-Gitea-Event", "push")
		h.Set("X-Gitlab-Event", "Push Hook")
		if git.Verify(provider, secret, h, body) == nil && sig != "fuzz-secret" {
			t.Fatalf("forged signature %q accepted", sig)
		}
		_, _ = git.Parse(provider, h, body)
		good := git.SignatureHeaders(provider, secret, body, "id")
		if git.ValidProvider(provider) && git.Verify(provider, secret, good, body) != nil {
			t.Fatal("own signature refused")
		}
	})
}

func TestAC_APP_03_RepoName(t *testing.T) {
	for in, want := range map[string]string{
		"Acme/Shop": "acme/shop", "https://github.com/acme/shop.git": "acme/shop", "git@github.com:acme/shop.git": "acme/shop",
		"ssh://git@gitea.local:2222/acme/shop.git": "acme/shop", "https://gitlab.com/group/sub/app": "group/sub/app",
	} {
		if got := git.RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

// REQ-APP-011: a deploy key pair: OpenSSH private key and an authorized_keys public line.
func TestAC_APP_03_DeployKey(t *testing.T) {
	k, err := git.NewDeployKey("polipan-web")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(k.PrivatePEM))
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Public))
	if err != nil || string(pub.Marshal()) != string(signer.PublicKey().Marshal()) || !strings.HasSuffix(k.Public, " polipan-web") {
		t.Fatalf("public key %q: %v", k.Public, err)
	}
}

func TestAC_APP_03_SourceNormalize(t *testing.T) {
	s := git.Source{Repo: "https://git.example.com/acme/shop.git"}
	if err := s.Normalize(); err != nil || s.Branch != "main" || s.Builder != "railpack" || s.Path != "/" || s.Dockerfile != "Dockerfile" {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []git.Source{{Repo: "acme/shop"}, {Repo: "https://x/y", Branch: "--upload-pack=x"}, {Repo: "https://x/y", Path: "../../etc"},
		{Repo: "https://x/y", Builder: "nix"}} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v must fail", bad)
		}
	}
	on := true
	if !(&git.Source{}).AutoDeployOn(false) || (&git.Source{}).AutoDeployOn(true) || !(&git.Source{AutoDeploy: &on}).AutoDeployOn(true) {
		t.Fatal("auto deploy defaults")
	}
}
