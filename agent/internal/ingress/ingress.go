// SPDX-License-Identifier: Apache-2.0

// Package ingress manages Caddy on the node: the container, the admin socket,
// the full JSON config built from IngressSpec, one atomic POST /load, and the
// TLS status of every domain (REQ-AGENT-007, REQ-DOM-002, REQ-DOM-003, REQ-DOM-010).
package ingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

const (
	// DefaultImage is the pinned Caddy 2.x image.
	DefaultImage = "caddy:2.10.2"
	// ContainerName is the name of the Caddy container.
	ContainerName = "polipan-caddy"
	// ErrCodeLoadFailed is the agent error code when POST /load fails.
	ErrCodeLoadFailed = "caddy_load_failed"
)

// Config is the Caddy setting of the agent.
type Config struct {
	DataDir string // the agent data dir; Caddy storage is DataDir/caddy, the admin socket is DataDir/caddy-admin/admin.sock
	Image   string // Caddy image, default DefaultImage
	Network string // docker network that Caddy joins to reach the upstreams
	// IP is the fixed address of Caddy in Network (the .2 of the mesh subnet of the node). The environment
	// firewall of the other nodes lets this address reach every container (REQ-SEC-106). Empty = Docker picks.
	IP string
	// ACMECAURL overrides the ACME directory (tests use a staging or local CA). Empty = from IngressSpec.
	ACMECAURL string
	// ACMETrustedRoots is an optional PEM file that trusts ACMECAURL. It is mounted read-only
	// into the container at the same path.
	ACMETrustedRoots string
	// DNS01Provider, DNS01Config and WildcardDomains turn on wildcard certificates by DNS-01 (REQ-DOM-007).
	DNS01Provider, DNS01Config, WildcardDomains string
}

// Manager owns the Caddy container and its config.
type Manager struct {
	Config Config
	Docker docker.Client

	mu       sync.Mutex
	lastHash string
	client   *http.Client
}

// LoadError is returned when Caddy refuses a config.
type LoadError struct{ Message string }

func (e *LoadError) Error() string { return ErrCodeLoadFailed + ": " + e.Message }

// Code returns the agent error code.
func (e *LoadError) Code() string { return ErrCodeLoadFailed }

func (m *Manager) image() string {
	if m.Config.Image != "" {
		return m.Config.Image
	}
	return DefaultImage
}

func (m *Manager) adminDir() string   { return filepath.Join(m.Config.DataDir, "caddy-admin") }
func (m *Manager) socketPath() string { return filepath.Join(m.adminDir(), "admin.sock") }
func (m *Manager) hashPath() string   { return filepath.Join(m.Config.DataDir, "caddy-config-hash") }

// Start makes sure the Caddy container runs (with the last saved config, if any). It is safe to call again.
func (m *Manager) Start(ctx context.Context) error {
	c, err := m.Docker.Inspect(ctx, ContainerName)
	switch {
	case err == nil && m.Config.IP != "" && c.IP != "" && c.IP != m.Config.IP:
		// An older Caddy container with another address: make it again. The data and the config stay in the data dir.
		if err := m.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return fmt.Errorf("ingress: remove caddy: %w", err)
		}
	case err == nil:
		if c.State == "running" || c.State == "restarting" {
			return nil
		}
		if err := m.Docker.Start(ctx, c.ID); err != nil {
			return fmt.Errorf("ingress: start caddy: %w", err)
		}
		return nil
	case !errors.Is(err, docker.ErrNotFound):
		return fmt.Errorf("ingress: inspect caddy: %w", err)
	}

	dirs := map[string]os.FileMode{
		filepath.Join(m.Config.DataDir, "caddy"):        0o700,
		filepath.Join(m.Config.DataDir, "caddy-config"): 0o700,
		m.adminDir(): 0o700,
	}
	for d, mode := range dirs {
		if err := os.MkdirAll(d, mode); err != nil {
			return fmt.Errorf("ingress: %w", err)
		}
		if err := os.Chmod(d, mode); err != nil {
			return fmt.Errorf("ingress: %w", err)
		}
	}
	initial := filepath.Join(m.Config.DataDir, "caddy-config", "initial.json")
	if err := os.WriteFile(initial, initialConfig(), 0o600); err != nil {
		return fmt.Errorf("ingress: %w", err)
	}
	// A new container: the saved hash may not match what Caddy runs, so forget it.
	_ = os.Remove(m.hashPath())
	m.mu.Lock()
	m.lastHash = ""
	m.mu.Unlock()

	binds := []string{
		filepath.Join(m.Config.DataDir, "caddy") + ":/data",
		filepath.Join(m.Config.DataDir, "caddy-config") + ":/config",
		m.adminDir() + ":/run/caddy-admin",
	}
	if m.Config.ACMETrustedRoots != "" {
		binds = append(binds, m.Config.ACMETrustedRoots+":"+m.Config.ACMETrustedRoots+":ro")
	}
	_, err = m.Docker.CreateAndStart(ctx, docker.ContainerSpec{
		Name:  ContainerName,
		Image: m.image(),
		// --resume: use the autosaved config when there is one, so Caddy keeps its last config.
		Cmd:           []string{"caddy", "run", "--config", "/config/initial.json", "--resume"},
		Labels:        map[string]string{"polipan.component": "caddy"},
		Network:       m.Config.Network,
		IP:            m.Config.IP,
		Binds:         binds,
		RestartPolicy: "always",
		Ports: []docker.PortBinding{
			{HostPort: 80, ContainerPort: 80, Proto: "tcp"},
			{HostPort: 443, ContainerPort: 443, Proto: "tcp"},
			{HostPort: 443, ContainerPort: 443, Proto: "udp"},
		},
	})
	if err != nil {
		return fmt.Errorf("ingress: create caddy: %w", err)
	}
	return nil
}

func (m *Manager) httpClient() *http.Client {
	if m.client == nil {
		sock := m.socketPath()
		m.client = &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
		}
	}
	return m.client
}

// Apply builds the full Caddy JSON from spec and sends one POST /load.
// If the JSON hash is the same as the last applied one, nothing is sent.
// It returns the hash of the applied config.
func (m *Manager) Apply(ctx context.Context, spec *agentv1.IngressSpec) (hash string, err error) {
	dns, err := ParseDNS01(m.Config.DNS01Provider, m.Config.DNS01Config, m.Config.WildcardDomains)
	if err != nil {
		return "", err
	}
	body, err := BuildConfig(spec, Options{
		ACMECAURL:        m.Config.ACMECAURL,
		ACMETrustedRoots: m.Config.ACMETrustedRoots,
		DNS01:            dns,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	hash = hex.EncodeToString(sum[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHash == "" {
		if b, rerr := os.ReadFile(m.hashPath()); rerr == nil {
			m.lastHash = strings.TrimSpace(string(b))
		}
	}
	if m.lastHash == hash {
		return hash, nil
	}

	// A new Caddy container needs a moment before the admin socket answers.
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, "http://caddy-admin/load", bytes.NewReader(body))
		if rerr != nil {
			return "", rerr
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = m.httpClient().Do(req)
		if err == nil {
			break
		}
		if attempt >= 40 || ctx.Err() != nil {
			return "", &LoadError{Message: err.Error()}
		}
		select {
		case <-ctx.Done():
			return "", &LoadError{Message: err.Error()}
		case <-time.After(500 * time.Millisecond):
		}
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return "", &LoadError{Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, msg)}
	}
	m.lastHash = hash
	if err := os.WriteFile(m.hashPath(), []byte(hash), 0o600); err != nil {
		return hash, fmt.Errorf("ingress: save hash: %w", err)
	}
	return hash, nil
}

// Version returns the Caddy version, or "" when Caddy is not running.
// It is the tag of the image of the container (for example caddy:2.10.2 gives 2.10.2).
func (m *Manager) Version(ctx context.Context) string {
	c, err := m.Docker.Inspect(ctx, ContainerName)
	if err != nil || c.State != "running" {
		return ""
	}
	img := c.Image
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	slash := strings.LastIndex(img, "/")
	colon := strings.LastIndex(img, ":")
	if colon < 0 || colon < slash {
		return ""
	}
	return img[colon+1:]
}
