// SPDX-License-Identifier: Apache-2.0

// Package build builds images of git services with BuildKit (REQ-APP-002, REQ-APP-005).
//
// BuildKit runs rootless in its own container, on its own Docker network (polipan-build),
// without the Docker socket (REQ-SEC-102, REQ-SEC-108). The server is the client: it runs
// buildctl against buildkitd over mTLS. The result is an OCI layout tar on the server side;
// the server pushes it to the registry (the build network cannot reach the registry).
//
// Builders:
//   - dockerfile: the dockerfile.v0 frontend.
//   - railpack: "railpack prepare" detects the language and writes a build plan, then the
//     railpack frontend builds it. A detection failure is a DetectError with the message.
//
// Build-time secrets are BuildKit secret mounts (--secret id=NAME,env=NAME, the value only in the
// environment of buildctl), never build args. Plain build-time variables are build args.
package build

import (
	"context"
	"errors"
	"time"
)

// Builders.
const (
	Dockerfile = "dockerfile"
	Railpack   = "railpack"
)

// Request is one build.
type Request struct {
	ContextDir string            // the checked-out source plus the build path
	Builder    string            // dockerfile or railpack
	Dockerfile string            // path relative to ContextDir (dockerfile builder)
	Secrets    map[string]string // build-time secrets: BuildKit secret mounts
	BuildArgs  map[string]string // plain build-time variables
	CacheKey   string            // per service: cache mounts are kept apart
	NoCache    bool              // clear the cache: build without it
	Timeout    time.Duration     // build_timeout
	Output     string            // file that receives the OCI layout tar
	Log        func(line string) // live build log, already split in lines (not masked yet)
}

// Builder builds an image. Tests use Fake.
type Builder interface {
	Build(ctx context.Context, req Request) error
}

// DetectError means Railpack could not find out how to build the app.
type DetectError struct{ Msg string }

func (e *DetectError) Error() string { return "railpack: " + e.Msg }

// ErrTimeout means the build ran longer than build_timeout.
var ErrTimeout = errors.New("build: build_timeout reached")
