// SPDX-License-Identifier: Apache-2.0

// Package telemetry runs the parts of the node that send metrics and logs to the server (12-observability section 7):
// the metrics text of the agent, vmagent with a disk buffer, the Caddy access log counters and the log shipper.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/agent/internal/ingress"
	"github.com/polipan/polipan/agent/internal/join"
	"github.com/polipan/polipan/agent/internal/logship"
	"github.com/polipan/polipan/agent/internal/metrics"
	"github.com/polipan/polipan/proto/agentv1"
)

// VMAgentName is the name of the vmagent container.
const VMAgentName = "polipan-vmagent"

const vmagentListen = "127.0.0.1:8429"

// Docker is the Docker client with the log follower. *docker.HTTPClient is one.
type Docker interface {
	docker.Client
	FollowLogs(ctx context.Context, idOrName string, o docker.LogOptions, fn func(docker.LogLine) error) error
}

// Manager starts the metric and log senders and gives each one the telemetry spec of the desired state.
type Manager struct {
	Docker  Docker
	DataDir string
	NodeID  string
	Log     *slog.Logger
	// Collector, Access and Shipper are made by the caller, so the engine can feed them.
	Collector *metrics.Collector
	Access    *metrics.Access
	Shipper   *logship.Shipper
	// Secrets gives the secret values to mask in shipped logs.
	Secrets func() []string
	// MetricsAddr is where the metrics text is served. Default metrics.DefaultAddr.
	MetricsAddr string

	mu      sync.Mutex
	spec    *agentv1.TelemetrySpec
	wake    chan struct{}
	started bool
}

func (m *Manager) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// Update is called with every new desired state.
func (m *Manager) Update(ds *agentv1.DesiredState) {
	if ds == nil {
		return
	}
	if m.Access != nil && ds.GetIngress() != nil {
		m.Access.SetSpec(ds.GetIngress())
	}
	m.mu.Lock()
	m.spec = ds.GetTelemetry()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	m.mu.Unlock()
	if m.Shipper != nil {
		var secrets []string
		if m.Secrets != nil {
			secrets = m.Secrets()
		}
		m.Shipper.Update(ds.GetTelemetry(), secrets)
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run serves the metrics text, follows the Caddy log, runs the log shipper and keeps vmagent in line with the spec.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	m.started = true
	m.mu.Unlock()
	var wg sync.WaitGroup
	if m.Collector != nil {
		addr := m.MetricsAddr
		if addr == "" {
			addr = metrics.DefaultAddr
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Collector.Serve(ctx, addr); err != nil {
				m.log().Error("metrics endpoint stopped", "error", err)
			}
		}()
	}
	if m.Access != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Access.Follow(ctx, m.Docker, ingress.ContainerName)
		}()
	}
	if m.Shipper != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Shipper.Run(ctx); err != nil && ctx.Err() == nil {
				m.log().Error("log shipper stopped", "error", err)
			}
		}()
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		m.syncVMAgent(ctx)
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-m.wake:
		case <-tick.C: // also copies a renewed certificate
		}
	}
}

func (m *Manager) pkiDir() string  { return filepath.Join(m.DataDir, "vmagent", "pki") }
func (m *Manager) bufDir() string  { return filepath.Join(m.DataDir, "vmagent", "buffer") }
func (m *Manager) confDir() string { return filepath.Join(m.DataDir, "vmagent", "conf") }

// syncVMAgent makes the vmagent container match the spec: running with the right image and settings, or removed.
func (m *Manager) syncVMAgent(ctx context.Context) {
	m.mu.Lock()
	spec := m.spec
	m.mu.Unlock()
	url := spec.GetMetricsWriteUrl()
	c, err := m.Docker.Inspect(ctx, VMAgentName)
	if err != nil && !errors.Is(err, docker.ErrNotFound) {
		m.log().Warn("telemetry: inspect vmagent", "error", err)
		return
	}
	exists := err == nil
	if url == "" || spec.GetVmagentImage() == "" {
		if exists {
			if err := m.Docker.Remove(ctx, c.ID, true); err != nil {
				m.log().Warn("telemetry: remove vmagent", "error", err)
			}
		}
		return
	}
	certChanged, err := m.copyPKI()
	if err != nil {
		m.log().Warn("telemetry: copy the node certificate", "error", err)
		return
	}
	wantCmd := m.args(spec)
	confChanged, err := m.writeScrapeConfig(spec)
	if err != nil {
		m.log().Warn("telemetry: scrape config", "error", err)
		return
	}
	if exists {
		same := c.Image == spec.GetVmagentImage() && c.Labels["polipan.vmagent-args"] == joinArgs(wantCmd)
		switch {
		case same && (c.State == "running" || c.State == "restarting"):
			_ = certChanged // vmagent reads the certificate files again by itself
			_ = confChanged // and reloads the scrape config on SIGHUP, which the 15 s config check below does
			return
		case same:
			if err := m.Docker.Start(ctx, c.ID); err != nil {
				m.log().Warn("telemetry: start vmagent", "error", err)
			}
			return
		}
		if err := m.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
			m.log().Warn("telemetry: remove old vmagent", "error", err)
			return
		}
	}
	if _, err := m.Docker.PullImage(ctx, spec.GetVmagentImage(), ""); err != nil {
		m.log().Warn("telemetry: pull vmagent", "image", spec.GetVmagentImage(), "error", err)
		// A local image may exist: try to start anyway.
	}
	for _, d := range []string{m.bufDir(), m.confDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			m.log().Warn("telemetry: folder", "error", err)
			return
		}
	}
	_, err = m.Docker.CreateAndStart(ctx, docker.ContainerSpec{
		Name:   VMAgentName,
		Image:  spec.GetVmagentImage(),
		Cmd:    wantCmd,
		Labels: map[string]string{"polipan.component": "vmagent", "polipan.vmagent-args": joinArgs(wantCmd)},
		// The host network: vmagent scrapes the loopback address of the agent.
		Network:       "host",
		Binds:         []string{m.pkiDir() + ":/pki:ro", m.bufDir() + ":/buffer", m.confDir() + ":/conf:ro"},
		RestartPolicy: "always",
	})
	if err != nil {
		m.log().Warn("telemetry: start vmagent", "error", err)
	}
}

func joinArgs(a []string) string {
	s := ""
	for _, x := range a {
		s += x + "\n"
	}
	return s
}

func (m *Manager) args(spec *agentv1.TelemetrySpec) []string {
	mib := int64(spec.GetBufferMib())
	if mib <= 0 {
		mib = logship.DefaultBufferMiB
	}
	iv := spec.GetScrapeIntervalSeconds()
	if iv == 0 {
		iv = 15
	}
	_ = iv // the interval is in the scrape config
	return []string{
		"-promscrape.config=/conf/scrape.yml",
		"-remoteWrite.url=" + spec.GetMetricsWriteUrl(),
		"-remoteWrite.tlsCertFile=/pki/node.crt",
		"-remoteWrite.tlsKeyFile=/pki/node.key",
		"-remoteWrite.tlsCAFile=/pki/ca.crt",
		"-remoteWrite.tmpDataPath=/buffer",
		"-remoteWrite.maxDiskUsagePerURL=" + strconv.FormatInt(mib<<20, 10),
		"-httpListenAddr=" + vmagentListen,
		"-promscrape.config.strictParse=false",
	}
}

// writeScrapeConfig writes the scrape config. It reports if the file changed.
func (m *Manager) writeScrapeConfig(spec *agentv1.TelemetrySpec) (bool, error) {
	iv := spec.GetScrapeIntervalSeconds()
	if iv == 0 {
		iv = 15
	}
	addr := m.MetricsAddr
	if addr == "" {
		addr = metrics.DefaultAddr
	}
	text := fmt.Sprintf("global:\n  scrape_interval: %ds\nscrape_configs:\n  - job_name: polipan-agent\n    static_configs:\n      - targets: [%q]\n", iv, addr)
	if err := os.MkdirAll(m.confDir(), 0o700); err != nil {
		return false, err
	}
	return writeIfChanged(filepath.Join(m.confDir(), "scrape.yml"), []byte(text), 0o644)
}

// copyPKI copies the node certificate, its key and the CA into a folder that only vmagent sees.
func (m *Manager) copyPKI() (bool, error) {
	if err := os.MkdirAll(m.pkiDir(), 0o755); err != nil {
		return false, err
	}
	cert, key, ca := join.Files(m.DataDir)
	changed := false
	for src, dst := range map[string]string{cert: "node.crt", key: "node.key", ca: "ca.crt"} {
		b, err := os.ReadFile(src)
		if err != nil {
			return false, err
		}
		// vmagent runs as another user in its image, so the files are readable for it. The folder is the node's own.
		ch, err := writeIfChanged(filepath.Join(m.pkiDir(), dst), b, 0o644)
		if err != nil {
			return false, err
		}
		changed = changed || ch
	}
	return changed, nil
}

func writeIfChanged(path string, b []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(b) {
		return false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
