// SPDX-License-Identifier: Apache-2.0

// Package git has the Git side of Polipan: webhook signatures and events, Git providers
// (GitHub App, GitLab, Gitea, generic), cloning with an SSRF-checked URL and deploy keys.
// Every external call is behind an interface with a fake (REQ-CORE-032).
package git

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Providers.
const (
	GitHub  = "github"
	GitLab  = "gitlab"
	Gitea   = "gitea"
	Generic = "generic"
)

// ValidProvider tells if p is a known provider.
func ValidProvider(p string) bool {
	return p == GitHub || p == GitLab || p == Gitea || p == Generic
}

// ErrBadSignature means the signature header is missing or wrong (answer 401).
var ErrBadSignature = errors.New("git: webhook signature is missing or wrong")

// Verify checks the signature of a webhook body (10-security, section 1):
// GitHub and generic: X-Hub-Signature-256 = "sha256=" + hex HMAC-SHA256.
// GitLab: X-Gitlab-Token equals the secret.
// Gitea and Forgejo: X-Gitea-Signature (or X-Forgejo-Signature) = hex HMAC-SHA256.
// An empty secret never verifies.
func Verify(provider string, secret []byte, h http.Header, body []byte) error {
	if len(secret) == 0 {
		return ErrBadSignature
	}
	switch provider {
	case GitLab:
		got := h.Get("X-Gitlab-Token")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), secret) != 1 {
			return ErrBadSignature
		}
		return nil
	case Gitea:
		got := h.Get("X-Gitea-Signature")
		if got == "" {
			got = h.Get("X-Forgejo-Signature")
		}
		return checkHMAC(secret, body, got)
	case GitHub, Generic:
		got, ok := strings.CutPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
		if !ok {
			return ErrBadSignature
		}
		return checkHMAC(secret, body, got)
	}
	return ErrBadSignature
}

func checkHMAC(secret, body []byte, gotHex string) error {
	got, err := hex.DecodeString(strings.TrimSpace(gotHex))
	if err != nil || len(got) != sha256.Size {
		return ErrBadSignature
	}
	if !hmac.Equal(got, Sign(secret, body)) {
		return ErrBadSignature
	}
	return nil
}

// Sign returns HMAC-SHA256(secret, body). Tests and the fake Git server use it.
func Sign(secret, body []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return m.Sum(nil)
}

// SignatureHeaders returns the headers a provider sends for body. Tests and the fake Git server use it.
func SignatureHeaders(provider string, secret, body []byte, deliveryID string) http.Header {
	h := http.Header{}
	sig := hex.EncodeToString(Sign(secret, body))
	switch provider {
	case GitLab:
		h.Set("X-Gitlab-Token", string(secret))
		h.Set("X-Gitlab-Event", "Push Hook")
		h.Set("X-Gitlab-Event-UUID", deliveryID)
	case Gitea:
		h.Set("X-Gitea-Signature", sig)
		h.Set("X-Gitea-Event", "push")
		h.Set("X-Gitea-Delivery", deliveryID)
	default:
		h.Set("X-Hub-Signature-256", "sha256="+sig)
		h.Set("X-GitHub-Event", "push")
		h.Set("X-GitHub-Delivery", deliveryID)
	}
	return h
}

// Delivery is one webhook call.
type Delivery struct {
	// ID is the delivery id of the provider. Without one it is the SHA-256 of the body,
	// so the same body cannot be replayed either.
	ID string
	// Event is push, pull_request, ping or the raw name of another event.
	Event string
	// Push is set for push events.
	Push *PushEvent
}

// PushEvent is the part of a push event Polipan uses.
type PushEvent struct {
	Repo    string // full name, lower case, for example acme/shop
	Branch  string // empty for a tag push
	Commit  string // the new head; empty when the branch was deleted
	Deleted bool
	Author  string // commit author or pusher, for the audit event
}

// Parse reads the event of a verified webhook. It must only be called after Verify.
func Parse(provider string, h http.Header, body []byte) (*Delivery, error) {
	d := &Delivery{}
	switch provider {
	case GitLab:
		d.ID = firstHeader(h, "X-Gitlab-Event-UUID", "X-Gitlab-Webhook-UUID", "Idempotency-Key")
		switch h.Get("X-Gitlab-Event") {
		case "Push Hook":
			d.Event = "push"
		case "Merge Request Hook":
			d.Event = "pull_request"
		default:
			d.Event = h.Get("X-Gitlab-Event")
		}
	case Gitea:
		d.ID = firstHeader(h, "X-Gitea-Delivery", "X-Forgejo-Delivery")
		d.Event = firstHeader(h, "X-Gitea-Event", "X-Forgejo-Event")
	default:
		d.ID = h.Get("X-GitHub-Delivery")
		d.Event = h.Get("X-GitHub-Event")
	}
	if d.ID == "" {
		sum := sha256.Sum256(body)
		d.ID = "sha256:" + hex.EncodeToString(sum[:])
	}
	if len(d.ID) > 200 {
		return nil, errors.New("git: delivery id is too long")
	}
	if d.Event != "push" {
		return d, nil
	}
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
		// GitHub, Gitea
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Pusher struct {
			Name     string `json:"name"`
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"pusher"`
		HeadCommit *struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"head_commit"`
		// GitLab
		CheckoutSHA *string `json:"checkout_sha"`
		UserName    string  `json:"user_name"`
		Project     struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("git: push event is not valid JSON: %w", err)
	}
	ev := &PushEvent{Repo: p.Repository.FullName, Commit: p.After, Deleted: p.Deleted}
	if provider == GitLab {
		ev.Repo = p.Project.PathWithNamespace
		ev.Author = p.UserName
		if p.CheckoutSHA == nil {
			ev.Deleted = true
		}
	} else {
		ev.Author = firstNonEmpty(p.Pusher.Name, p.Pusher.Login, p.Pusher.Username)
		if p.HeadCommit != nil && p.HeadCommit.Author.Name != "" {
			ev.Author = p.HeadCommit.Author.Name
		}
	}
	if strings.Trim(ev.Commit, "0") == "" {
		ev.Deleted, ev.Commit = true, ""
	}
	if ev.Commit != "" && !ValidCommit(ev.Commit) {
		return nil, errors.New("git: push event has a bad commit id")
	}
	ev.Branch, _ = strings.CutPrefix(p.Ref, "refs/heads/")
	if ev.Branch == p.Ref {
		ev.Branch = "" // a tag or another ref
	}
	ev.Repo = strings.ToLower(ev.Repo)
	if ev.Repo == "" {
		return nil, errors.New("git: push event has no repository")
	}
	d.Push = ev
	return d, nil
}

// ValidCommit tells if s looks like a commit id (7 to 64 hex characters).
func ValidCommit(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	_, err := hex.DecodeString(s + strings.Repeat("0", len(s)%2))
	return err == nil
}

// RepoName gives the "owner/name" form of a repo: a full name stays, a URL loses its scheme,
// host and ".git". The result is lower case. It is used to match a push to linked services.
func RepoName(repo string) string {
	r := strings.TrimSpace(repo)
	if strings.Contains(r, "://") {
		if u, err := url.Parse(r); err == nil {
			r = u.Path
		}
	} else if i := strings.Index(r, ":"); i > 0 && strings.Contains(r[:i], "@") {
		r = r[i+1:] // git@host:owner/name.git
	}
	r = strings.Trim(r, "/")
	r = strings.TrimSuffix(r, ".git")
	return strings.ToLower(r)
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
