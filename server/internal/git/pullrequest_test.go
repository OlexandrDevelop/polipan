// SPDX-License-Identifier: Apache-2.0

package git

import (
	"net/http"
	"testing"
)

const prSHA = "0123456789abcdef0123456789abcdef01234567"

func parsePR(t *testing.T, provider, event string, body string) *PullRequest {
	t.Helper()
	h := http.Header{}
	switch provider {
	case GitLab:
		h.Set("X-Gitlab-Event", event)
	case Gitea:
		h.Set("X-Gitea-Event", event)
	default:
		h.Set("X-GitHub-Event", event)
	}
	d, err := Parse(provider, h, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return d.PR
}

// REQ-PREV-001: the PR events of GitHub, Gitea and GitLab give the same actions.
func TestREQ_PREV_001_PullRequestEvents(t *testing.T) {
	gh := func(action string, extra string) string {
		return `{"action":"` + action + `","number":5,` + extra + `"pull_request":{"number":5,"title":"T","html_url":"https://x/5","merged":false,"user":{"login":"ann"},
			"head":{"sha":"` + prSHA + `","ref":"feature","repo":{"full_name":"Acme/Shop"}},"base":{"ref":"main","repo":{"full_name":"Acme/Shop"}},
			"labels":[{"name":"polipan-preview"}]},"repository":{"full_name":"Acme/Shop"}}`
	}
	for _, c := range []struct{ provider, event, action, want string }{
		{GitHub, "pull_request", "opened", PROpened}, {GitHub, "pull_request", "reopened", PRReopened}, {GitHub, "pull_request", "synchronize", PRSynchronize},
		{GitHub, "pull_request", "closed", PRClosed}, {GitHub, "pull_request", "labeled", PRLabels}, {GitHub, "pull_request", "unlabeled", PRLabels},
		{Gitea, "pull_request", "synchronized", PRSynchronize}, {Gitea, "pull_request", "label_updated", PRLabels}, {Gitea, "pull_request", "opened", PROpened},
	} {
		pr := parsePR(t, c.provider, c.event, gh(c.action, ""))
		if pr == nil || pr.Action != c.want || pr.Repo != "acme/shop" || pr.Number != 5 || pr.HeadSHA != prSHA || pr.HeadRef != "feature" || pr.IsFork() || !pr.HasLabel("POLIPAN-preview") {
			t.Errorf("%s %s: %+v", c.provider, c.action, pr)
		}
	}
	if pr := parsePR(t, GitHub, "pull_request", gh("edited", "")); pr != nil {
		t.Errorf("an edit is not an event of previews: %+v", pr)
	}
	// A PR from a fork (REQ-PREV-009), and a deleted fork.
	fork := `{"action":"opened","number":6,"pull_request":{"number":6,"head":{"sha":"` + prSHA + `","ref":"x","repo":{"full_name":"mallory/shop"}},"base":{"ref":"main","repo":{"full_name":"acme/shop"}}},"repository":{"full_name":"acme/shop"}}`
	if pr := parsePR(t, GitHub, "pull_request", fork); pr == nil || !pr.IsFork() || pr.HeadRepo != "mallory/shop" {
		t.Errorf("fork: %+v", pr)
	}
	gone := `{"action":"opened","number":6,"pull_request":{"number":6,"head":{"sha":"` + prSHA + `","ref":"x","repo":null},"base":{"ref":"main","repo":{"full_name":"acme/shop"}}},"repository":{"full_name":"acme/shop"}}`
	if pr := parsePR(t, GitHub, "pull_request", gone); pr == nil || !pr.IsFork() {
		t.Errorf("a deleted fork must count as a fork: %+v", pr)
	}
	// A bad commit id is an error, so nothing is built from it.
	bad := `{"action":"opened","number":6,"pull_request":{"number":6,"head":{"sha":"--upload-pack=x","ref":"x","repo":{"full_name":"acme/shop"}},"base":{"ref":"main","repo":{"full_name":"acme/shop"}}},"repository":{"full_name":"acme/shop"}}`
	if _, err := Parse(GitHub, http.Header{"X-Github-Event": {"pull_request"}}, []byte(bad)); err == nil {
		t.Error("a bad commit id was accepted")
	}

	mr := func(action, state string, extra string) string {
		return `{"user":{"username":"ann"},"project":{"path_with_namespace":"Acme/Shop"},"object_attributes":{"iid":7,"title":"T","action":"` + action + `","state":"` + state + `",
			"source_branch":"feature","target_branch":"main","url":"https://x/7","source_project_id":1,"target_project_id":1,"last_commit":{"id":"` + prSHA + `"}` + extra + `},
			"labels":[{"title":"polipan-preview"}],"changes":{}}`
	}
	for _, c := range []struct{ action, state, extra, want string }{
		{"open", "opened", "", PROpened}, {"reopen", "opened", "", PRReopened}, {"close", "closed", "", PRClosed}, {"merge", "merged", "", PRClosed},
		{"update", "opened", `,"oldrev":"` + prSHA + `"`, PRSynchronize},
	} {
		pr := parsePR(t, GitLab, "Merge Request Hook", mr(c.action, c.state, c.extra))
		if pr == nil || pr.Action != c.want || pr.Repo != "acme/shop" || pr.Number != 7 || pr.IsFork() || pr.HeadRef != "feature" {
			t.Errorf("gitlab %s: %+v", c.action, pr)
		}
	}
	if pr := parsePR(t, GitLab, "Merge Request Hook", mr("update", "opened", "")); pr != nil {
		t.Errorf("a title edit is not an event of previews: %+v", pr)
	}
	if pr := parsePR(t, GitLab, "Merge Request Hook", mr("merge", "merged", "")); pr == nil || !pr.Merged {
		t.Errorf("a merge: %+v", pr)
	}
	forkMR := `{"project":{"path_with_namespace":"acme/shop"},"object_attributes":{"iid":8,"action":"open","source_branch":"x","target_branch":"main","source_project_id":2,"target_project_id":1,"source":{"path_with_namespace":"mallory/shop"},"last_commit":{"id":"` + prSHA + `"}}}`
	if pr := parsePR(t, GitLab, "Merge Request Hook", forkMR); pr == nil || !pr.IsFork() {
		t.Errorf("gitlab fork: %+v", pr)
	}
}
