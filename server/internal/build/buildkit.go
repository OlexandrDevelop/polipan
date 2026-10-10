// SPDX-License-Identifier: Apache-2.0

package build

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/server/internal/pki"
)

// DefaultRailpackFrontend is the railpack BuildKit frontend. Keep it on the version of the
// railpack binary in the server image.
const DefaultRailpackFrontend = "ghcr.io/railwayapp/railpack-frontend:v0.40.1"

// BuildKit is the Builder that runs buildctl against a remote buildkitd.
type BuildKit struct {
	Addr             string // tcp://buildkitd:1234
	CertDir          string // ca.crt, client.crt, client.key (see WriteCerts)
	Buildctl         string // default "buildctl"
	RailpackBin      string // default "railpack"
	RailpackFrontend string // default DefaultRailpackFrontend
	// Run starts a command and streams its output lines. Nil means os/exec. Tests set it.
	Run func(ctx context.Context, name string, args, env []string, line func(string)) error
}

// FromEnv reads POLIPAN_BUILDKIT_ADDR and POLIPAN_BUILDKIT_CERTS. An empty address gives nil.
func FromEnv(getenv func(string) string) *BuildKit {
	addr := getenv("POLIPAN_BUILDKIT_ADDR")
	if addr == "" {
		return nil
	}
	dir := getenv("POLIPAN_BUILDKIT_CERTS")
	if dir == "" {
		dir = "/var/lib/polipan/buildkit-certs"
	}
	return &BuildKit{Addr: addr, CertDir: dir, RailpackFrontend: getenv("POLIPAN_RAILPACK_FRONTEND")}
}

// Build implements Builder.
func (b *BuildKit) Build(ctx context.Context, req Request) error {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	args, env, err := b.args(ctx, req)
	if err != nil {
		return err
	}
	err = b.run(ctx, b.buildctl(), args, env, req.Log)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	if err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	return nil
}

func (b *BuildKit) buildctl() string {
	if b.Buildctl != "" {
		return b.Buildctl
	}
	return "buildctl"
}

// args makes the buildctl arguments and environment. Secret values are only in env.
func (b *BuildKit) args(ctx context.Context, req Request) (args, env []string, err error) {
	args = []string{"--addr", b.Addr}
	if b.CertDir != "" {
		args = append(args,
			"--tlscacert", filepath.Join(b.CertDir, "ca.crt"),
			"--tlscert", filepath.Join(b.CertDir, "client.crt"),
			"--tlskey", filepath.Join(b.CertDir, "client.key"),
			"--tlsservername", "buildkitd")
	}
	args = append(args, "build", "--progress", "plain", "--local", "context="+req.ContextDir,
		"--output", "type=oci,dest="+req.Output)
	if req.NoCache {
		args = append(args, "--no-cache")
	}
	env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
	names := sortedKeys(req.Secrets)
	for i, n := range names {
		// The env name is fixed, so a user secret name can not change the process env.
		envName := fmt.Sprintf("PP_BUILD_SECRET_%d", i)
		args = append(args, "--secret", "id="+n+",env="+envName)
		env = append(env, envName+"="+req.Secrets[n])
	}
	for _, k := range sortedKeys(req.BuildArgs) {
		args = append(args, "--opt", "build-arg:"+k+"="+req.BuildArgs[k])
	}

	switch req.Builder {
	case Dockerfile, "":
		file := req.Dockerfile
		if file == "" {
			file = "Dockerfile"
		}
		full := filepath.Join(req.ContextDir, filepath.Clean("/"+file))
		if _, err := os.Stat(full); err != nil {
			return nil, nil, &DetectError{Msg: "no " + file + " in the build path"}
		}
		args = append(args, "--frontend", "dockerfile.v0",
			"--local", "dockerfile="+filepath.Dir(full), "--opt", "filename="+filepath.Base(full))
	case Railpack:
		planDir := filepath.Join(filepath.Dir(req.Output), "railpack")
		if err := os.MkdirAll(planDir, 0o700); err != nil {
			return nil, nil, err
		}
		if err := b.prepare(ctx, req, planDir); err != nil {
			return nil, nil, err
		}
		front := b.RailpackFrontend
		if front == "" {
			front = DefaultRailpackFrontend
		}
		args = append(args, "--frontend", "gateway.v0", "--opt", "source="+front,
			"--local", "dockerfile="+planDir, "--opt", "filename=railpack-plan.json")
		if req.CacheKey != "" {
			args = append(args, "--opt", "build-arg:cache-key="+req.CacheKey)
		}
		if len(names) > 0 {
			args = append(args, "--opt", "build-arg:secrets-hash="+secretsHash(req.Secrets))
		}
	default:
		return nil, nil, fmt.Errorf("build: unknown builder %q", req.Builder)
	}
	return args, env, nil
}

// prepare runs "railpack prepare". Exit code 1 is a detection failure with a message.
func (b *BuildKit) prepare(ctx context.Context, req Request, planDir string) error {
	bin := b.RailpackBin
	if bin == "" {
		bin = "railpack"
	}
	info := filepath.Join(planDir, "railpack-info.json")
	args := []string{"prepare", req.ContextDir, "--plan-out", filepath.Join(planDir, "railpack-plan.json"), "--info-out", info}
	// The plan needs the names of the secrets, not the values.
	for _, n := range sortedKeys(req.Secrets) {
		args = append(args, "--env", n+"=secret")
	}
	for _, k := range sortedKeys(req.BuildArgs) {
		args = append(args, "--env", k+"="+req.BuildArgs[k])
	}
	var lines []string
	err := b.run(ctx, bin, args, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}, func(l string) {
		lines = append(lines, l)
		if req.Log != nil {
			req.Log(l)
		}
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return &DetectError{Msg: detectMessage(info, lines)}
	}
	return fmt.Errorf("railpack prepare: %w", err)
}

// detectMessage reads the error logs of railpack-info.json, or the last output lines.
func detectMessage(infoFile string, lines []string) string {
	var info struct {
		Logs []struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		} `json:"logs"`
	}
	if raw, err := os.ReadFile(infoFile); err == nil && json.Unmarshal(raw, &info) == nil {
		var msgs []string
		for _, l := range info.Logs {
			if l.Level == "error" {
				msgs = append(msgs, l.Msg)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return "could not detect how to build the app"
}

func (b *BuildKit) run(ctx context.Context, name string, args, env []string, line func(string)) error {
	if line == nil {
		line = func(string) {}
	}
	if b.Run != nil {
		return b.Run(ctx, name, args, env, line)
	}
	return RunCommand(ctx, name, args, env, line)
}

// RunCommand runs a command and gives every output line (stdout and stderr) to line.
func RunCommand(ctx context.Context, name string, args, env []string, line func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Run()
	_ = pw.Close()
	wg.Wait()
	return err
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// secretsHash changes when a secret value changes, so railpack layers that use it are rebuilt.
func secretsHash(secrets map[string]string) string {
	h := sha256.New()
	for _, k := range sortedKeys(secrets) {
		fmt.Fprintf(h, "%s=%s\n", k, secrets[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// WriteCerts writes the mTLS files of buildkitd and of the buildctl client into dir:
// ca.crt, tls.crt and tls.key (server, name "buildkitd"), client.crt and client.key.
// Files that exist and are valid for 30 more days are kept.
func WriteCerts(dir string, ca *pki.CA, hosts []string) error {
	if ca == nil {
		return errors.New("build: no CA")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if validFor(filepath.Join(dir, "tls.crt"), 30*24*time.Hour) && validFor(filepath.Join(dir, "client.crt"), 30*24*time.Hour) {
		return nil
	}
	write := func(name string, data []byte) error {
		// buildkitd runs as another user and reads the files from a volume only it and the server mount.
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	if err := write("ca.crt", ca.CertPEM()); err != nil {
		return err
	}
	srv, err := ca.ServerCert(append([]string{"buildkitd"}, hosts...))
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(srv.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return err
	}
	if err := write("tls.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate[0]})); err != nil {
		return err
	}
	if err := write("tls.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "polipan-buildctl"}}, key)
	if err != nil {
		return err
	}
	cl, err := ca.SignAgentCert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), "polipan-buildctl", 365*24*time.Hour)
	if err != nil {
		return err
	}
	ckDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := write("client.crt", cl.PEM); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ckDER}), 0o600)
}

func validFor(certFile string, d time.Duration) bool {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return false
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return false
	}
	c, err := x509.ParseCertificate(b.Bytes)
	return err == nil && time.Until(c.NotAfter) > d
}
