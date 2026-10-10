// SPDX-License-Identifier: Apache-2.0

package orchestrator

import "context"

// BuildRequest is what a deploy of a git service asks of the build: a commit (empty means
// the branch head), the branch of a push, and no_cache (clear the build cache).
type BuildRequest struct {
	Commit  string
	Branch  string
	NoCache bool
}

type buildReqKey struct{}

// WithBuildRequest puts a build request in ctx. Deploy stores it with the new deployment.
func WithBuildRequest(ctx context.Context, r BuildRequest) context.Context {
	return context.WithValue(ctx, buildReqKey{}, r)
}

// BuildRequestFrom returns the build request of ctx.
func BuildRequestFrom(ctx context.Context) (BuildRequest, bool) {
	r, ok := ctx.Value(buildReqKey{}).(BuildRequest)
	return r, ok
}
