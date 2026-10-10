// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Commit status states. A provider maps them to its own words.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
)

// Status is a commit status that tells a user what a sync did (REQ-CAC-003, F08 edge cases).
type Status struct {
	State       string // pending, success or failure
	Description string
	Context     string
	TargetURL   string
}

// StatusPoster is a provider that can post a commit status. GitHub, GitLab and Gitea can. A generic provider cannot.
type StatusPoster interface {
	PostStatus(ctx context.Context, repo, commit string, s Status) error
}

func trimDescription(s string) string {
	if len(s) > 140 {
		return s[:137] + "..."
	}
	return s
}

func postJSON(ctx context.Context, client *http.Client, u string, hdr http.Header, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var out json.RawMessage
	if err := doJSON(client, req, hdr, &out); err != nil && !strings.Contains(err.Error(), "unexpected end of JSON") {
		return err
	}
	return nil
}

func (g *githubApp) PostStatus(ctx context.Context, repo, commit string, s Status) error {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return err
	}
	u := g.api + "/repos/" + strings.Trim(repo, "/") + "/statuses/" + url.PathEscape(commit)
	return postJSON(ctx, g.client, u, http.Header{"Authorization": {"Bearer " + tok}},
		map[string]string{"state": s.State, "description": trimDescription(s.Description), "context": s.Context, "target_url": s.TargetURL})
}

func (g *gitlab) PostStatus(ctx context.Context, repo, commit string, s Status) error {
	state := map[string]string{StatusPending: "pending", StatusSuccess: "success", StatusFailure: "failed"}[s.State]
	u := g.base + "/api/v4/projects/" + url.PathEscape(strings.Trim(repo, "/")) + "/statuses/" + url.PathEscape(commit)
	return postJSON(ctx, g.client, u, http.Header{"Private-Token": {g.token}},
		map[string]string{"state": state, "description": trimDescription(s.Description), "name": s.Context, "target_url": s.TargetURL})
}

func (g *gitea) PostStatus(ctx context.Context, repo, commit string, s Status) error {
	u := g.base + "/api/v1/repos/" + strings.Trim(repo, "/") + "/statuses/" + url.PathEscape(commit)
	return postJSON(ctx, g.client, u, http.Header{"Authorization": {"token " + g.token}},
		map[string]string{"state": s.State, "description": trimDescription(s.Description), "context": s.Context, "target_url": s.TargetURL})
}
