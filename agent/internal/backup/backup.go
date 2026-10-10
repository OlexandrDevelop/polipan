// SPDX-License-Identifier: Apache-2.0

// Package backup runs the backup jobs of the agent (F10): per-engine dumps piped into restic,
// retention, restore, restore checks in a temporary container and the test of a target.
// The agent runs restic. The database tools run inside the container of the database.
package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
)

// Docker is the part of the Docker client that the backup code uses.
type Docker interface {
	Exec(ctx context.Context, idOrName string, opts docker.ExecOptions) (docker.ExecConn, error)
	Inspect(ctx context.Context, idOrName string) (*docker.Container, error)
	List(ctx context.Context, labels map[string]string) ([]docker.Container, error)
	CreateAndStart(ctx context.Context, spec docker.ContainerSpec) (string, error)
	Remove(ctx context.Context, id string, force bool) error
	Logs(ctx context.Context, idOrName string, tail int) (string, error)
	VolumeCreate(ctx context.Context, name string, labels map[string]string) (*docker.Volume, error)
	VolumeInspect(ctx context.Context, name string) (*docker.Volume, error)
	VolumeRemove(ctx context.Context, name string) error
	PullImage(ctx context.Context, ref, registryAuth string) (string, error)
}

// Runner runs backup jobs.
type Runner struct {
	Docker Docker
	// Restic is the restic binary. Default "restic".
	Restic string
	// TmpDir is where temporary folders go (restore checks, key files). Default the system temp dir.
	TmpDir string
	// PullAuth returns the registry auth header value for a registry_auth_ref. Optional.
	PullAuth func(ref string) (string, error)
	Log      *slog.Logger
	// Scrub hides secrets in a message. Optional.
	Scrub func(string) string
}

// Error codes of the jobs. They are returned in the ack of the command.
const (
	CodeContainerNotFound = "container_not_found"
	CodeDumpFailed        = "dump_failed"
	CodeBackupFailed      = "backup_failed"
	CodeRestoreFailed     = "restore_failed"
	CodeTargetFailed      = "target_test_failed"
	CodeHookFailed        = "hook_failed"
	CodeInternal          = "internal_error"
)

// Error is a job error with a code.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func fail(code, format string, a ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Runner) resticBin() string {
	if r.Restic != "" {
		return r.Restic
	}
	return "restic"
}

func (r *Runner) tmp() string {
	if r.TmpDir != "" {
		_ = os.MkdirAll(r.TmpDir, 0o700)
		return r.TmpDir
	}
	return os.TempDir()
}

func (r *Runner) scrub(s string) string {
	if r.Scrub != nil {
		return r.Scrub(s)
	}
	return s
}

// resolve returns the name of the container. "label:key=value" picks the first running container with the label.
func (r *Runner) resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", fail(CodeContainerNotFound, "no container is given")
	}
	if spec, ok := strings.CutPrefix(ref, "label:"); ok {
		labels := map[string]string{}
		for _, pair := range strings.Split(spec, ",") {
			k, v, _ := strings.Cut(pair, "=")
			labels[k] = v
		}
		list, err := r.Docker.List(ctx, labels)
		if err != nil {
			return "", err
		}
		for _, c := range list {
			if c.State == "running" {
				return c.ID, nil
			}
		}
		return "", fail(CodeContainerNotFound, "no running container has the label %s", spec)
	}
	c, err := r.Docker.Inspect(ctx, ref)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			return "", fail(CodeContainerNotFound, "container %s does not exist on this node", ref)
		}
		return "", err
	}
	if c.State != "running" {
		return "", fail(CodeContainerNotFound, "container %s is not running (state %s)", ref, c.State)
	}
	return c.ID, nil
}

// limited keeps the first n bytes that are written.
type limited struct {
	buf bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limited) String() string { return strings.TrimSpace(l.buf.String()) }

// execResult is the end of a command in a container.
type execResult struct {
	Stdout, Stderr string
	Code           int
}

// execShell runs "sh -c script" in the container, with optional stdin. The script uses the environment of the container.
func (r *Runner) execShell(ctx context.Context, container, script string, stdin io.Reader) (execResult, error) {
	var stderr limited
	stderr.max = 8 << 10
	conn, err := r.Docker.Exec(ctx, container, docker.ExecOptions{Cmd: []string{"sh", "-c", script}, Stderr: &stderr})
	if err != nil {
		return execResult{}, err
	}
	defer func() { _ = conn.Close() }()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(conn, stdin)
			_ = conn.CloseWrite()
		}()
	} else {
		_ = conn.CloseWrite()
	}
	var out limited
	out.max = 1 << 20
	if _, err := io.Copy(&out, conn); err != nil && ctx.Err() == nil {
		return execResult{}, err
	}
	code, err := conn.Wait(ctx)
	if err != nil {
		return execResult{}, err
	}
	return execResult{Stdout: out.String(), Stderr: stderr.String(), Code: code}, nil
}

// execOK runs a script and returns its output. A non-zero exit code is an error with the stderr text.
func (r *Runner) execOK(ctx context.Context, container, script string) (string, error) {
	res, err := r.execShell(ctx, container, script, nil)
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("exit code %d: %s", res.Code, firstLine(res.Stderr+" "+res.Stdout))
	}
	return res.Stdout, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func since(t time.Time) int64 { return time.Since(t).Milliseconds() }
