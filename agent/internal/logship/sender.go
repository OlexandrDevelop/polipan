// SPDX-License-Identifier: Apache-2.0

package logship

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/join"
)

// Sender sends one batch. It returns nil for a 2xx answer and a *StatusError for another HTTP status.
type Sender interface {
	Send(ctx context.Context, url, batchID string, body []byte) error
}

// StatusError is an HTTP answer that is not 2xx.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("logs: the server answered HTTP %d", e.Code) }

// HTTPSender posts batches over mTLS with the identity of the agent. The certificate files are read at each
// new connection, so a renewed certificate is used without a restart.
type HTTPSender struct {
	DataDir string

	once   sync.Once
	client *http.Client
	err    error
}

func (h *HTTPSender) init() {
	id, err := join.Load(h.DataDir)
	if err != nil {
		h.err = err
		return
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    id.CAPool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cur, err := join.Load(h.DataDir)
			if err != nil {
				return &id.Cert, nil
			}
			return &cur.Cert, nil
		},
	}
	h.client = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig: cfg, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second}}
}

// Send posts the batch with the header X-Polipan-Batch.
func (h *HTTPSender) Send(ctx context.Context, url, batchID string, body []byte) error {
	h.once.Do(h.init)
	if h.err != nil {
		// The identity may not be there yet. Try to read it again next time.
		err := h.err
		h.once, h.err = sync.Once{}, nil
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &StatusError{Code: 400}
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("X-Polipan-Batch", batchID)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return &StatusError{Code: resp.StatusCode}
}

// sendLoop sends the oldest batch again and again until the server takes it, then goes on with the next one.
func (s *Shipper) sendLoop(ctx context.Context) {
	back := orDur(s.BackoffMin, time.Second)
	lo, hi := back, orDur(s.BackoffMax, time.Minute)
	for ctx.Err() == nil {
		f, ok := s.sp.oldest()
		url, _, _ := s.telemetry()
		if !ok || url == "" {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(time.Second): // the url may have been set
			}
			continue
		}
		h, body, err := readBatch(f.path)
		if err != nil {
			s.log().Warn("logs: cannot read a batch, dropping it", "batch", f.n, "error", err)
			_ = s.sp.finish(f.n)
			continue
		}
		if s.at("before-send") {
			return
		}
		id := s.sp.epoch() + "." + fmt.Sprint(f.n)
		err = s.Sender.Send(ctx, url, id, body)
		if s.dead.Load() {
			return
		}
		var se *StatusError
		switch {
		case err == nil:
			s.shipped.Add(uint64(h.Lines))
		case asStatus(err, &se) && se.Code/100 == 4 && se.Code != 429:
			// A batch the server will never take must not block the lines behind it.
			s.rejected.Add(1)
			s.log().Warn("logs: the server refused a batch, dropping it", "batch", f.n, "status", se.Code)
		default:
			if ctx.Err() != nil {
				return
			}
			s.log().Warn("logs: cannot send, will try again", "error", err, "wait", back.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(back/2 + rand.N(back/2+1)):
			}
			back = min2(back*2, hi)
			continue
		}
		back = lo
		if s.at("after-send") {
			return
		}
		if err := s.sp.finish(f.n); err != nil {
			s.log().Error("logs: cannot save the cursors", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func min2(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func asStatus(err error, out **StatusError) bool {
	return errors.As(err, out)
}
