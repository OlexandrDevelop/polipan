// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"errors"
	"strings"

	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/store"
)

// Errors of the manual create. The API maps them to answers.
var (
	ErrDisabled = errors.New("preview: previews are off in this project")
	ErrNoRepo   = errors.New("preview: no service of the base environment uses this repository")
	ErrLimit    = store.ErrPreviewLimit
	ErrExists   = store.ErrPreviewExists
)

// Info is a preview for the API and the UI (REQ-PREV-014).
type Info struct {
	ID, ProjectID, Slug, Repo string
	Number                    int
	Branch                    string
	BaseEnvID                 string
	State                     string
	URLs                      []string
	ExpiresAt, LastPushAt     *string
	FromFork, WaitingForLabel bool
	FailureReason             string
	HeadSHA                   string
}

func (s *Service) info(ctx context.Context, e *envState) Info {
	i := Info{ID: e.ID, ProjectID: e.ProjectID, Slug: e.Slug, Repo: e.Repo, Number: e.Number, BaseEnvID: e.BaseEnvID, State: e.State,
		URLs: s.urls(ctx, e.ID), Branch: e.str("head_ref"), FromFork: e.boolean("preview_fork"), WaitingForLabel: e.boolean("waiting_for_label"),
		FailureReason: e.str("preview_failure"), HeadSHA: e.str("head_sha")}
	if i.URLs == nil {
		i.URLs = []string{}
	}
	if e.Expires != nil {
		t := e.Expires.UTC().Format("2006-01-02T15:04:05Z07:00")
		i.ExpiresAt = &t
	}
	if e.LastPush != nil {
		t := e.LastPush.UTC().Format("2006-01-02T15:04:05Z07:00")
		i.LastPushAt = &t
	}
	return i
}

// List lists the previews of a project that are not deleted.
func (s *Service) List(ctx context.Context, orgID, projectID string) ([]Info, error) {
	envs, err := s.list(ctx, `e.project_id = $1 AND p.org_id = $2 AND e.kind = 'preview' AND e.deleted_at IS NULL`, projectID, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(envs))
	for _, e := range envs {
		out = append(out, s.info(ctx, e))
	}
	return out, nil
}

// Get reads one preview.
func (s *Service) Get(ctx context.Context, orgID, envID string) (*Info, error) {
	envs, err := s.list(ctx, `e.id = $1 AND p.org_id = $2 AND e.kind = 'preview' AND e.deleted_at IS NULL`, envID, orgID)
	if err != nil {
		return nil, err
	}
	if len(envs) == 0 {
		return nil, ErrNotFound
	}
	i := s.info(ctx, envs[0])
	return &i, nil
}

// CreateParams is a manual create (REQ-PREV-014).
type CreateParams struct {
	OrgID, ProjectID string
	Repo             string
	PRNumber         int
	Branch           string
	BaseEnvID        string
	UserID, IP, Req  string
}

// Create makes a preview by hand for a PR number or a branch. The preview builds the branch head.
func (s *Service) Create(ctx context.Context, p CreateParams) (*Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.projectConfig(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	if p.BaseEnvID != "" {
		cfg.BaseEnvironmentID = p.BaseEnvID
	}
	baseID, err := s.baseEnvironment(ctx, p.ProjectID, cfg)
	if err != nil {
		return nil, errNoBase
	}
	repo := git.RepoName(p.Repo)
	svcs, err := s.baseServices(ctx, baseID)
	if err != nil {
		return nil, err
	}
	var tracked *baseService
	for i := range svcs {
		if tracks(svcs[i].Source, repo) {
			tracked = &svcs[i]
			break
		}
	}
	if tracked == nil {
		return nil, ErrNoRepo
	}
	slug, number, branch := Slug(p.PRNumber), p.PRNumber, strings.TrimSpace(p.Branch)
	if p.PRNumber <= 0 {
		if branch == "" {
			return nil, errors.New("preview: a PR number or a branch is needed")
		}
		slug, number = BranchSlug(branch), BranchNumber(branch)
	}
	if branch == "" {
		branch = tracked.Source.Branch
	}
	if existing, err := s.findEnv(ctx, p.ProjectID, repo, number); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, ErrExists
	}
	c := candidate{ProjectID: p.ProjectID, OrgID: p.OrgID}
	env, created, err := s.createEnv(ctx, c, cfg, baseID, tracked.Source.ConnectionID, slug, repo, number, "", branch, false, "", "",
		s.Clock.Now(), store.Actor{UserID: p.UserID, IP: p.IP, RequestID: p.Req})
	if err != nil {
		return nil, err
	}
	if err := s.deployAll(ctx, env, created, cfg, "", branch, false); err != nil {
		s.Log.Warn("preview: manual create", "environment", env.Slug, "error", err)
	}
	fresh, err := s.Get(ctx, p.OrgID, env.ID)
	return fresh, err
}
