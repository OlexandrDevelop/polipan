// SPDX-License-Identifier: Apache-2.0

package build_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/build"
)

type call struct {
	name      string
	args, env []string
}

// REQ-SEC-107 / AC-APP-06: build-time secrets go to BuildKit as secret mounts with the value only
// in the environment of buildctl, never in the arguments (no build args, no process list leak).
func TestAC_APP_06_SecretsAreSecretMounts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []call
	bk := &build.BuildKit{Addr: "tcp://buildkitd:1234", CertDir: "/certs", Run: func(_ context.Context, name string, args, env []string, line func(string)) error {
		calls = append(calls, call{name, args, env})
		line("#1 done")
		return nil
	}}
	var logged []string
	err := bk.Build(context.Background(), build.Request{ContextDir: dir, Builder: build.Dockerfile, Dockerfile: "Dockerfile",
		Secrets: map[string]string{"NPM_TOKEN": "very-secret-value", "PATH": "/evil", "LD_PRELOAD": "/evil.so"}, BuildArgs: map[string]string{"MODE": "prod"},
		NoCache: true, Output: filepath.Join(dir, "out.tar"), Log: func(l string) { logged = append(logged, l) }})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(calls[0].args, " ")
	if strings.Contains(args, "very-secret-value") {
		t.Fatalf("secret value in the arguments: %s", args)
	}
	for _, want := range []string{"--secret id=NPM_TOKEN,env=PP_BUILD_SECRET_1", "--secret id=PATH,env=PP_BUILD_SECRET_2",
		"--secret id=LD_PRELOAD,env=PP_BUILD_SECRET_0", "--opt build-arg:MODE=prod", "--no-cache",
		"--output type=oci,dest=", "--frontend dockerfile.v0", "--tlscert /certs/client.crt"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	envs := strings.Join(calls[0].env, "\n")
	if !strings.Contains(envs, "PP_BUILD_SECRET_1=very-secret-value") || len(logged) != 1 {
		t.Fatalf("env or log: %v %v", calls[0].env, logged)
	}
	for _, e := range calls[0].env {
		if strings.HasPrefix(e, "LD_PRELOAD=") || e == "PATH=/evil" {
			t.Fatalf("user secret name set in the process env: %s", e)
		}
	}
}

// AC-APP-04: railpack prepare runs first; exit code 1 is a detection failure with the message.
func TestAC_APP_04_RailpackPrepare(t *testing.T) {
	dir := t.TempDir()
	var names []string
	bk := &build.BuildKit{Addr: "tcp://b:1", Run: func(_ context.Context, name string, args, _ []string, line func(string)) error {
		names = append(names, name)
		if name == "railpack" {
			line("✖ No start command could be found")
			return exec.Command("sh", "-c", "exit 1").Run()
		}
		return nil
	}}
	err := bk.Build(context.Background(), build.Request{ContextDir: dir, Builder: build.Railpack, Output: filepath.Join(dir, "o.tar")})
	de, ok := err.(*build.DetectError)
	if !ok || !strings.Contains(de.Msg, "No start command") || len(names) != 1 {
		t.Fatalf("err %v names %v", err, names)
	}
	bk.Run = func(_ context.Context, name string, args, _ []string, _ func(string)) error {
		names = append(names, name+" "+strings.Join(args, " "))
		return nil
	}
	names = nil
	if err := bk.Build(context.Background(), build.Request{ContextDir: dir, Builder: build.Railpack, CacheKey: "svc-1", Output: filepath.Join(dir, "o.tar")}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || !strings.HasPrefix(names[0], "railpack prepare") || !strings.Contains(names[1], "--frontend gateway.v0") ||
		!strings.Contains(names[1], "build-arg:cache-key=svc-1") {
		t.Fatalf("calls %v", names)
	}
}
