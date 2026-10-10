// SPDX-License-Identifier: Apache-2.0

package git

import (
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
)

// Source is the git source of a service, stored as services.config.git (REQ-APP-001).
type Source struct {
	ConnectionID    string `json:"connection_id,omitempty"`
	Repo            string `json:"repo"`                        // owner/name (with a connection) or a full URL
	Branch          string `json:"branch"`                      // default main
	Path            string `json:"path"`                        // build path, default /
	Builder         string `json:"builder"`                     // dockerfile or railpack (default railpack)
	Dockerfile      string `json:"dockerfile"`                  // default Dockerfile
	Provider        string `json:"provider,omitempty"`          // webhook signature type without a connection
	AutoDeploy      *bool  `json:"auto_deploy,omitempty"`       // nil: on for non-protected environments
	DeployKeyPublic string `json:"deploy_key_public,omitempty"` // shown to the user
	DeployKeyRef    string `json:"deploy_key_ref,omitempty"`    // id of the private key in the key store
}

// SourceOf reads config.git of a service config. ok is false when there is none.
func SourceOf(config []byte) (*Source, bool) {
	var c struct {
		Git *Source `json:"git"`
	}
	if json.Unmarshal(config, &c) != nil || c.Git == nil {
		return nil, false
	}
	return c.Git, true
}

// AutoDeployOn tells if a push deploys the service (REQ-APP-004).
func (s *Source) AutoDeployOn(protected bool) bool {
	if s.AutoDeploy != nil {
		return *s.AutoDeploy
	}
	return !protected
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// Normalize fills the defaults and checks the fields.
func (s *Source) Normalize() error {
	s.Repo = strings.TrimSpace(s.Repo)
	if s.Branch == "" {
		s.Branch = "main"
	}
	if s.Path == "" {
		s.Path = "/"
	}
	if s.Builder == "" {
		s.Builder = "railpack"
	}
	if s.Dockerfile == "" {
		s.Dockerfile = "Dockerfile"
	}
	switch {
	case s.Repo == "" || len(s.Repo) > 300:
		return errors.New("git.repo is required")
	case s.ConnectionID == "" && !strings.Contains(s.Repo, "://") && !strings.Contains(s.Repo, "@"):
		return errors.New("git.repo must be a full URL when there is no connection")
	case !branchRe.MatchString(s.Branch) || strings.HasPrefix(s.Branch, "-") || strings.Contains(s.Branch, ".."):
		return errors.New("git.branch is not a valid branch name")
	case s.Builder != "dockerfile" && s.Builder != "railpack":
		return errors.New("git.builder must be dockerfile or railpack")
	case s.Provider != "" && !ValidProvider(s.Provider):
		return errors.New("git.provider must be github, gitlab, gitea or generic")
	}
	for _, p := range []*string{&s.Path, &s.Dockerfile} {
		clean := path.Clean("/" + *p)
		if strings.Contains(*p, "..") || len(*p) > 300 {
			return errors.New("git.path and git.dockerfile must stay inside the repository")
		}
		if p == &s.Path {
			*p = clean
		}
	}
	return nil
}
