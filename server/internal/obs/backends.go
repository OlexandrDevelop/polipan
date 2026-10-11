// SPDX-License-Identifier: Apache-2.0

// Package obs is the observability side of the server: the clients of VictoriaMetrics and VictoriaLogs, the
// query layer for the API, the ingest of agent data and the platform metrics (docs/spec/12-observability.md).
package obs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnavailable means a store did not answer. The API shows monitoring_unavailable (REQ-ALERT edge case).
var ErrUnavailable = errors.New("obs: the monitoring store is not reachable")

// ErrBadQuery means the store refused the query (for example a bad regex). The API answers 422.
var ErrBadQuery = errors.New("obs: the query was refused")

// Backends are the two stores. An empty URL means that store is off.
type Backends struct {
	MetricsURL string
	LogsURL    string
	User       string
	Password   string
	HTTP       *http.Client
}

// NewBackends makes the clients. The client has no total timeout, because the log tail is a long request:
// every call sets its own context.
func NewBackends(metricsURL, logsURL, user, password string) *Backends {
	return &Backends{
		MetricsURL: strings.TrimRight(metricsURL, "/"), LogsURL: strings.TrimRight(logsURL, "/"),
		User: user, Password: password,
		HTTP: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}},
	}
}

// MetricsEnabled and LogsEnabled tell if the store is set up.
func (b *Backends) MetricsEnabled() bool { return b != nil && b.MetricsURL != "" }
func (b *Backends) LogsEnabled() bool    { return b != nil && b.LogsURL != "" }

func (b *Backends) do(ctx context.Context, method, base, path string, q url.Values, ctype string, body io.Reader) (*http.Response, error) {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if b.User != "" {
		req.SetBasicAuth(b.User, b.Password)
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resp, nil
}

// check turns a bad status into an error. The body is read (up to 1 KiB) for the message.
func check(resp *http.Response) error {
	if resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(b))
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrBadQuery, msg)
	default:
		return fmt.Errorf("%w: status %d: %s", ErrUnavailable, resp.StatusCode, msg)
	}
}

// MetricsUp and LogsUp are the health probes of /healthz.
func (b *Backends) MetricsUp(ctx context.Context) bool { return b.up(ctx, b.MetricsURL) }
func (b *Backends) LogsUp(ctx context.Context) bool    { return b.up(ctx, b.LogsURL) }

func (b *Backends) up(ctx context.Context, base string) bool {
	if b == nil || base == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := b.do(ctx, http.MethodGet, base, "/health", nil, "", nil)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode == http.StatusOK
}
