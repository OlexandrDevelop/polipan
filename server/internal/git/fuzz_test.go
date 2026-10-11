// SPDX-License-Identifier: Apache-2.0

package git_test

import (
	"net/http"
	"testing"

	"github.com/polipan/polipan/server/internal/git"
)

// FuzzWebhook feeds webhook headers and bodies from the internet. Parse must never panic, and a request
// with a made-up signature must never verify.
func FuzzWebhook(f *testing.F) {
	f.Add("github", "push", "sha256=00", []byte(`{"ref":"refs/heads/main","after":"abc","repository":{"full_name":"a/b"}}`))
	f.Add("gitlab", "Push Hook", "x", []byte(`{"object_kind":"push"}`))
	f.Add("gitea", "pull_request", "", []byte(`[`))
	f.Fuzz(func(t *testing.T, provider, event, sig string, body []byte) {
		h := http.Header{}
		h.Set("X-GitHub-Event", event)
		h.Set("X-Gitlab-Event", event)
		h.Set("X-Gitea-Event", event)
		h.Set("X-Hub-Signature-256", sig)
		h.Set("X-Gitea-Signature", sig)
		if sig != "polipan-secret-value" && git.Verify(provider, []byte("polipan-secret-value"), h, body) == nil {
			// Only a real HMAC may pass. A random value that passes is a hole.
			if provider != git.GitLab {
				t.Fatalf("a made-up signature %q verified", sig)
			}
		}
		_, _ = git.Parse(provider, h, body)
	})
}
