// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// FakeProvider is a Provider for tests. It also records the statuses, the comments and the removed labels
// of the previews (StatusPoster and Commenter).
type FakeProvider struct {
	Repos    []Repo
	User     string
	Password string
	Err      error

	mu       sync.Mutex
	Statuses []FakeStatus
	Comments []FakeComment
	Removed  []string // "repo#number:label"
	// RemoveErr makes RemoveLabel fail.
	RemoveErr error
}

// FakeStatus is a commit status that the fake provider got.
type FakeStatus struct {
	Repo, Commit string
	Status
}

// FakeComment is the last text of a comment that the fake provider got.
type FakeComment struct {
	Repo   string
	Number int
	ID     string
	Body   string
	Writes int
}

// PostStatus implements StatusPoster.
func (f *FakeProvider) PostStatus(_ context.Context, repo, commit string, s Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Statuses = append(f.Statuses, FakeStatus{Repo: repo, Commit: commit, Status: s})
	return nil
}

// UpsertComment implements Commenter.
func (f *FakeProvider) UpsertComment(_ context.Context, repo string, number int, id, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.Comments {
		c := &f.Comments[i]
		if c.Repo == repo && c.Number == number && (id == "" || c.ID == id) {
			c.Body, c.Writes = body, c.Writes+1
			return c.ID, nil
		}
	}
	c := FakeComment{Repo: repo, Number: number, ID: "c" + strconv.Itoa(len(f.Comments)+1), Body: body, Writes: 1}
	f.Comments = append(f.Comments, c)
	return c.ID, nil
}

// RemoveLabel implements Commenter.
func (f *FakeProvider) RemoveLabel(_ context.Context, repo string, number int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.Removed = append(f.Removed, repo+"#"+strconv.Itoa(number)+":"+label)
	return nil
}

// Snapshot returns copies of what the fake provider got.
func (f *FakeProvider) Snapshot() (statuses []FakeStatus, comments []FakeComment, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeStatus(nil), f.Statuses...), append([]FakeComment(nil), f.Comments...), append([]string(nil), f.Removed...)
}

// ListRepos implements Provider.
func (f *FakeProvider) ListRepos(_ context.Context, q string) ([]Repo, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return filterRepos(f.Repos, q), nil
}

// CloneAuth implements Provider.
func (f *FakeProvider) CloneAuth(context.Context) (string, string, error) {
	return f.User, f.Password, f.Err
}

// CloneURL implements Provider.
func (f *FakeProvider) CloneURL(repo string) string { return cloneURL("https://git.example.com", repo) }

// FakeCloner is a Cloner for tests: it writes Files into the folder and returns Commit.
type FakeCloner struct {
	mu     sync.Mutex
	Files  map[string]string // path -> content
	Commit string            // returned when the request has no commit
	Err    error
	// HostKey is the SSH host key of the fake server. A clone with another pinned key fails;
	// a clone without one reports this key with OnHostKey.
	HostKey string
	Calls   []CloneRequest
}

// Clone implements Cloner.
func (f *FakeCloner) Clone(_ context.Context, req CloneRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, req)
	if f.Err != nil {
		return "", f.Err
	}
	if f.HostKey != "" && req.HostKey != "" && req.HostKey != f.HostKey {
		return "", &CloneError{"host key verification failed"}
	}
	if f.HostKey != "" && req.HostKey == "" && req.OnHostKey != nil {
		defer req.OnHostKey(f.HostKey)
	}
	for name, content := range f.Files {
		p := filepath.Join(req.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	if req.Commit != "" {
		return req.Commit, nil
	}
	return f.Commit, nil
}
