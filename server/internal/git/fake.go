// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// FakeProvider is a Provider for tests.
type FakeProvider struct {
	Repos    []Repo
	User     string
	Password string
	Err      error
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
