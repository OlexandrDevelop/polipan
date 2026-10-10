// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// applyIngress loads the complete ingress config once and remembers it, so the next reconcile does not load it again.
func (e *Engine) applyIngress(ctx context.Context, spec *agentv1.IngressSpec) (string, error) {
	if e.Ingress == nil {
		return "", nil
	}
	if spec == nil {
		spec = &agentv1.IngressSpec{}
	}
	e.ingMu.Lock()
	defer e.ingMu.Unlock()
	if err := e.Ingress.Start(ctx); err != nil {
		return "", err
	}
	h := hashSpec(spec)
	hash, err := e.Ingress.Apply(ctx, spec)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.ingSpec, e.ingHash = h, hash
	e.mu.Unlock()
	return hash, nil
}

// runOneOff runs a command in a container from the image and returns the exit code and the output
// (pre_deploy and post_deploy, REQ-DEPLOY-109). The container does not carry the managed label,
// so the reconcile loop leaves it alone.
func (e *Engine) runOneOff(ctx context.Context, ro *agentv1.RunOneOff) ([]byte, string, string) {
	w := ro.Workload
	if w == nil {
		return nil, "internal_error", "workload is missing"
	}
	timeout := time.Duration(ro.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	e.mu.Lock()
	done := e.pulled[w.ImageRef]
	e.mu.Unlock()
	if !done {
		auth, err := e.pullAuth(w.RegistryAuthRef)
		if err != nil {
			return nil, codeFor(err, "image_pull_failed"), err.Error()
		}
		if _, err := e.Docker.PullImage(rctx, w.ImageRef, auth); err != nil {
			return nil, "image_pull_failed", err.Error()
		}
		e.mu.Lock()
		e.pulled[w.ImageRef] = true
		e.mu.Unlock()
	}
	mounts, err := e.prepareMounts(rctx, w)
	if err != nil {
		return nil, codeFor(err, "container_start_failed"), err.Error()
	}
	defer e.removeFiles(w.PlacementId)
	sp := e.containerSpec(w)
	sp.Mounts = mounts
	sp.RestartPolicy = "no"
	sp.Ports = nil
	sp.Aliases = nil
	sp.Labels = map[string]string{"polipan.oneoff": "true"}
	// The container gets a fixed address of its own and the firewall lets it reach its environment while it runs.
	if ip := e.oneOffIP(rctx); ip != "" {
		sp.IP = ip
		if r, ok := e.Firewall.(interface {
			Retain(ctx context.Context, env, ip string) func()
		}); ok {
			defer r.Retain(rctx, w.EnvironmentId, ip)()
		}
	}
	// A repeated command (same id) starts from a clean name.
	if old, err := e.Docker.Inspect(rctx, sp.Name); err == nil {
		_ = e.Docker.Remove(rctx, old.ID, true)
	}
	id, err := e.Docker.CreateAndStart(rctx, sp)
	if err != nil {
		return nil, codeFor(err, "container_start_failed"), err.Error()
	}
	defer func() {
		cctx, c2 := context.WithTimeout(context.Background(), 30*time.Second)
		defer c2()
		_ = e.Docker.Remove(cctx, id, true)
	}()
	for {
		ic, err := e.Docker.Inspect(rctx, id)
		if err == nil && (ic.State == "exited" || ic.State == "dead") {
			logs, _ := e.Docker.Logs(rctx, id, 2000)
			return jsonBytes(map[string]any{"exit_code": ic.ExitCode, "logs": e.scrub(logs)}), "", ""
		}
		if err != nil && !errors.Is(err, docker.ErrNotFound) && rctx.Err() == nil {
			return nil, codeFor(err, "docker_unavailable"), err.Error()
		}
		select {
		case <-rctx.Done():
			logs, _ := e.Docker.Logs(context.Background(), id, 2000)
			return jsonBytes(map[string]any{"exit_code": -1, "logs": e.scrub(logs)}), "deadline_exceeded", "the command did not end in time"
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// probeRoute sends requests to the local ingress for the host, and counts the answers that
// show a broken route: 5xx and connection errors (the post-switch check, REQ-DEPLOY-110).
// A TLS error of an https route is not counted: the certificate may not be ready yet.
func (e *Engine) probeRoute(ctx context.Context, pr *agentv1.ProbeRoute) ([]byte, string, string) {
	dur := time.Duration(pr.DurationSeconds) * time.Second
	if dur <= 0 {
		dur = time.Second
	}
	port := "80"
	if pr.Https {
		port = "443"
	}
	tr := &http.Transport{
		DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(c, "tcp", net.JoinHostPort("127.0.0.1", port))
		},
		TLSClientConfig:   &tls.Config{ServerName: pr.Host, InsecureSkipVerify: true}, //nolint:gosec // only to count answers of our own ingress
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := pr.Path
	if path == "" {
		path = "/"
	}
	scheme := "http"
	if pr.Https {
		scheme = "https"
	}
	total, failed := 0, 0
	end := time.Now().Add(dur)
	for time.Now().Before(end) && ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+pr.Host+path, nil)
		resp, err := client.Do(req)
		switch {
		case err != nil && pr.Https && isTLSError(err):
			// not counted
		case err != nil:
			total++
			failed++
		default:
			total++
			if resp.StatusCode >= 500 {
				failed++
			}
			_ = resp.Body.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	return jsonBytes(map[string]int{"total": total, "failed": failed}), "", ""
}

func isTLSError(err error) bool {
	var re tls.RecordHeaderError
	if errors.As(err, &re) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "tls:") || strings.Contains(s, "x509:") || strings.Contains(s, "remote error")
}

// oneOffIP picks a free address from .200 to .249 of the network of the node, or "" when there is none or the
// network is not a /24 that the agent knows. The server uses .10 to .127 for workloads, Docker the upper half.
func (e *Engine) oneOffIP(ctx context.Context) string {
	l, ok := e.Docker.(interface {
		NetworkIPs(ctx context.Context, name string) (map[string]bool, error)
	})
	if !ok || e.NetSubnet == nil || e.Network == "" || e.Network == "host" {
		return ""
	}
	base := e.NetSubnet.IP.To4()
	if ones, bits := e.NetSubnet.Mask.Size(); base == nil || ones != 24 || bits != 32 {
		return ""
	}
	used, err := l.NetworkIPs(ctx, e.Network)
	if err != nil {
		return ""
	}
	for last := 200; last <= 249; last++ {
		ip := net.IPv4(base[0], base[1], base[2], byte(last)).String()
		if !used[ip] {
			return ip
		}
	}
	return ""
}
