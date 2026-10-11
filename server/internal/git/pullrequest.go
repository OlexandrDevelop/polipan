// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Actions of a pull request event, the same for every provider (REQ-PREV-001).
const (
	PROpened      = "opened"
	PRReopened    = "reopened"
	PRSynchronize = "synchronize" // a new push to the branch of the PR
	PRClosed      = "closed"      // closed or merged (see Merged)
	PRLabels      = "labels"      // the labels changed
)

// PullRequest is the part of a pull request (or merge request) event that previews use.
type PullRequest struct {
	Action  string
	Repo    string // the repository the PR is made against (lower case full name)
	Number  int
	Title   string
	URL     string
	HeadSHA string
	HeadRef string // the branch of the PR
	BaseRef string
	// HeadRepo is the repository of the branch. It is not Repo for a PR from a fork.
	HeadRepo string
	Merged   bool
	Labels   []string
	Author   string
}

// IsFork tells if the branch of the PR is in another repository (REQ-PREV-009).
func (p *PullRequest) IsFork() bool { return p.HeadRepo == "" || p.HeadRepo != p.Repo }

// HasLabel tells if the PR has the label.
func (p *PullRequest) HasLabel(name string) bool {
	for _, l := range p.Labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}

// parsePullRequest reads a pull request event. An event that previews do not use gives nil and no error.
func parsePullRequest(provider string, body []byte) (*PullRequest, error) {
	if provider == GitLab {
		return parseMergeRequest(body)
	}
	var p struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest *struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			Merged  bool   `json:"merged"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo *struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("git: pull request event is not valid JSON: %w", err)
	}
	if p.PullRequest == nil {
		return nil, errors.New("git: pull request event has no pull_request")
	}
	pr := &PullRequest{Number: p.PullRequest.Number, Title: p.PullRequest.Title, URL: p.PullRequest.HTMLURL, Merged: p.PullRequest.Merged,
		HeadSHA: p.PullRequest.Head.SHA, HeadRef: p.PullRequest.Head.Ref, BaseRef: p.PullRequest.Base.Ref, Author: p.PullRequest.User.Login,
		Repo: strings.ToLower(firstNonEmpty(p.PullRequest.Base.Repo.FullName, p.Repository.FullName))}
	if pr.Number == 0 {
		pr.Number = p.Number
	}
	if p.PullRequest.Head.Repo != nil {
		pr.HeadRepo = strings.ToLower(p.PullRequest.Head.Repo.FullName)
	}
	for _, l := range p.PullRequest.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	switch p.Action {
	case "opened":
		pr.Action = PROpened
	case "reopened":
		pr.Action = PRReopened
	case "synchronize", "synchronized":
		pr.Action = PRSynchronize
	case "closed":
		pr.Action = PRClosed
	case "labeled", "unlabeled", "label_updated", "label_cleared":
		pr.Action = PRLabels
	default:
		return nil, nil
	}
	return pr, checkPR(pr)
}

func parseMergeRequest(body []byte) (*PullRequest, error) {
	var p struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
		Attrs struct {
			IID          int    `json:"iid"`
			Title        string `json:"title"`
			Action       string `json:"action"`
			State        string `json:"state"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			URL          string `json:"url"`
			OldRev       string `json:"oldrev"`
			SourceID     int    `json:"source_project_id"`
			TargetID     int    `json:"target_project_id"`
			Source       struct {
				PathWithNamespace string `json:"path_with_namespace"`
			} `json:"source"`
			LastCommit struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
		Labels []struct {
			Title string `json:"title"`
		} `json:"labels"`
		Changes struct {
			Labels json.RawMessage `json:"labels"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("git: merge request event is not valid JSON: %w", err)
	}
	a := p.Attrs
	pr := &PullRequest{Number: a.IID, Title: a.Title, URL: a.URL, HeadSHA: a.LastCommit.ID, HeadRef: a.SourceBranch, BaseRef: a.TargetBranch,
		Repo: strings.ToLower(p.Project.PathWithNamespace), Author: p.User.Username, Merged: a.State == "merged" || a.Action == "merge"}
	pr.HeadRepo = pr.Repo
	if a.SourceID != a.TargetID {
		pr.HeadRepo = strings.ToLower(firstNonEmpty(a.Source.PathWithNamespace, "fork"))
	}
	for _, l := range p.Labels {
		pr.Labels = append(pr.Labels, l.Title)
	}
	switch a.Action {
	case "open":
		pr.Action = PROpened
	case "reopen":
		pr.Action = PRReopened
	case "close", "merge":
		pr.Action = PRClosed
	case "update":
		switch {
		case a.OldRev != "":
			pr.Action = PRSynchronize
		case len(p.Changes.Labels) > 0:
			pr.Action = PRLabels
		default:
			return nil, nil // a title or description edit
		}
	default:
		return nil, nil
	}
	return pr, checkPR(pr)
}

func checkPR(pr *PullRequest) error {
	switch {
	case pr.Repo == "" || pr.Number <= 0:
		return errors.New("git: pull request event has no repository or number")
	case pr.Action != PRClosed && pr.Action != PRLabels && !ValidCommit(pr.HeadSHA):
		return errors.New("git: pull request event has a bad commit id")
	case pr.HeadSHA != "" && !ValidCommit(pr.HeadSHA):
		return errors.New("git: pull request event has a bad commit id")
	case pr.HeadRef != "" && (!branchRe.MatchString(pr.HeadRef) || strings.HasPrefix(pr.HeadRef, "-") || strings.Contains(pr.HeadRef, "..")):
		pr.HeadRef = "" // an odd branch name is never passed on to a git command
	}
	return nil
}

// ---- comments and labels ----

// Commenter is a provider that can write the comment of a preview on a pull request and take a label away.
// GitHub, GitLab and Gitea can. A generic provider cannot.
type Commenter interface {
	// UpsertComment writes the comment. With an empty id it makes a new one, else it changes that one (and
	// makes a new one when it does not exist any more). It returns the id of the comment.
	UpsertComment(ctx context.Context, repo string, number int, id, body string) (string, error)
	// RemoveLabel takes a label off the pull request. A label that is not there is not an error.
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
}

func sendJSON(ctx context.Context, client *http.Client, method, u string, hdr http.Header, in, out any) (int, error) {
	var rd *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("git: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("git: %s %s: status %d", method, req.URL.Path, resp.StatusCode)
	}
	if out != nil && buf.Len() > 0 {
		return resp.StatusCode, json.Unmarshal(buf.Bytes(), out)
	}
	return resp.StatusCode, nil
}

// upsert is the shared flow: change the comment with the id, or make a new one when it is gone.
func upsert(ctx context.Context, id string, update func(id string) (int, string, error), create func() (string, error)) (string, error) {
	if id != "" {
		status, newID, err := update(id)
		if err == nil {
			return newID, nil
		}
		if status != http.StatusNotFound {
			return "", err
		}
	}
	return create()
}

func (g *githubApp) hdr(ctx context.Context) (http.Header, error) {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	return http.Header{"Authorization": {"Bearer " + tok}, "X-Github-Api-Version": {"2022-11-28"}}, nil
}

func (g *githubApp) UpsertComment(ctx context.Context, repo string, number int, id, body string) (string, error) {
	hdr, err := g.hdr(ctx)
	if err != nil {
		return "", err
	}
	repo = strings.Trim(repo, "/")
	return upsert(ctx, id,
		func(id string) (int, string, error) {
			st, err := sendJSON(ctx, g.client, http.MethodPatch, g.api+"/repos/"+repo+"/issues/comments/"+url.PathEscape(id), hdr, map[string]string{"body": body}, nil)
			return st, id, err
		},
		func() (string, error) {
			var out struct {
				ID int64 `json:"id"`
			}
			_, err := sendJSON(ctx, g.client, http.MethodPost, g.api+"/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments", hdr, map[string]string{"body": body}, &out)
			return strconv.FormatInt(out.ID, 10), err
		})
}

func (g *githubApp) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	hdr, err := g.hdr(ctx)
	if err != nil {
		return err
	}
	st, err := sendJSON(ctx, g.client, http.MethodDelete, g.api+"/repos/"+strings.Trim(repo, "/")+"/issues/"+strconv.Itoa(number)+"/labels/"+url.PathEscape(label), hdr, nil, nil)
	if st == http.StatusNotFound {
		return nil
	}
	return err
}

func (g *gitlab) UpsertComment(ctx context.Context, repo string, number int, id, body string) (string, error) {
	hdr := http.Header{"Private-Token": {g.token}}
	base := g.base + "/api/v4/projects/" + url.PathEscape(strings.Trim(repo, "/")) + "/merge_requests/" + strconv.Itoa(number) + "/notes"
	return upsert(ctx, id,
		func(id string) (int, string, error) {
			st, err := sendJSON(ctx, g.client, http.MethodPut, base+"/"+url.PathEscape(id), hdr, map[string]string{"body": body}, nil)
			return st, id, err
		},
		func() (string, error) {
			var out struct {
				ID int64 `json:"id"`
			}
			_, err := sendJSON(ctx, g.client, http.MethodPost, base, hdr, map[string]string{"body": body}, &out)
			return strconv.FormatInt(out.ID, 10), err
		})
}

func (g *gitlab) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	u := g.base + "/api/v4/projects/" + url.PathEscape(strings.Trim(repo, "/")) + "/merge_requests/" + strconv.Itoa(number)
	_, err := sendJSON(ctx, g.client, http.MethodPut, u, http.Header{"Private-Token": {g.token}}, map[string]string{"remove_labels": label}, nil)
	return err
}

func (g *gitea) UpsertComment(ctx context.Context, repo string, number int, id, body string) (string, error) {
	hdr := http.Header{"Authorization": {"token " + g.token}}
	repo = strings.Trim(repo, "/")
	return upsert(ctx, id,
		func(id string) (int, string, error) {
			st, err := sendJSON(ctx, g.client, http.MethodPatch, g.base+"/api/v1/repos/"+repo+"/issues/comments/"+url.PathEscape(id), hdr, map[string]string{"body": body}, nil)
			return st, id, err
		},
		func() (string, error) {
			var out struct {
				ID int64 `json:"id"`
			}
			_, err := sendJSON(ctx, g.client, http.MethodPost, g.base+"/api/v1/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments", hdr, map[string]string{"body": body}, &out)
			return strconv.FormatInt(out.ID, 10), err
		})
}

// RemoveLabel of Gitea needs the id of the label, so the labels of the pull request are read first.
func (g *gitea) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	hdr := http.Header{"Authorization": {"token " + g.token}}
	issue := g.base + "/api/v1/repos/" + strings.Trim(repo, "/") + "/issues/" + strconv.Itoa(number) + "/labels"
	var labels []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := getJSON(ctx, g.client, issue, hdr, &labels); err != nil {
		return err
	}
	for _, l := range labels {
		if strings.EqualFold(l.Name, label) {
			_, err := sendJSON(ctx, g.client, http.MethodDelete, issue+"/"+strconv.FormatInt(l.ID, 10), hdr, nil, nil)
			return err
		}
	}
	return nil
}
