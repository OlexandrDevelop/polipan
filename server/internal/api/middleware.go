// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/polipan/polipan/server/internal/logging"
)

type requestIDKey struct{}

// RequestHeader is the header that carries the request id.
const RequestHeader = "X-Request-ID"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// RequestIDFrom returns the request id of the context, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

// RequestID gives every request an id. A good id from the client is kept.
// The id is in the response header, in the context and in every log line of the request.
func RequestID(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestHeader)
			if !validRequestID.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set(RequestHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			ctx = logging.WithLogger(ctx, log.With("request_id", id))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// statusWriter remembers the status code for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps streaming responses (SSE) working.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Recover catches a panic at the request boundary and answers with an
// internal_error that has the request id (REQ-CORE-033).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, _ := w.(*statusWriter)
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value
					panic(rec)
				}
				logging.From(r.Context()).Error("panic in handler", "panic", rec, "stack", string(debug.Stack()))
				if sw != nil && sw.wrote {
					return
				}
				WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// AccessLog writes one log line per request. It wraps Recover so a panic is logged with its status.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logging.From(r.Context()).Info("request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// notImplementedAsJSON turns the plain 501 of gen.Unimplemented into the standard error object.
func notImplementedAsJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&notImplementedWriter{ResponseWriter: w, r: r}, r)
	})
}

type notImplementedWriter struct {
	http.ResponseWriter
	r *http.Request
}

func (n *notImplementedWriter) WriteHeader(code int) {
	if code == http.StatusNotImplemented {
		WriteError(n.ResponseWriter, n.r, code, CodeInternal, "this operation is not implemented in this version", nil)
		return
	}
	n.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer (streams, write deadlines).
func (n *notImplementedWriter) Unwrap() http.ResponseWriter { return n.ResponseWriter }

// Flush keeps streaming responses working.
func (n *notImplementedWriter) Flush() {
	if f, ok := n.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
