// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxBodyBytes is the size limit of a request body (REQ-SEC-105).
const DefaultMaxBodyBytes int64 = 1 << 20

// contentSecurityPolicy has no unsafe-inline and no unsafe-eval (REQ-AUTH-005).
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// SecurityHeaders sets the security headers on every response, also on web files and errors.
// hsts adds Strict-Transport-Security, so use it only when the panel is served over https.
func SecurityHeaders(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("X-Frame-Options", "DENY")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit limits the size of request bodies. A body that is too large gets 413 in the error model.
// A client that sends a Content-Length over the limit is refused at once. For other bodies the
// reader fails when the limit is passed, and decodeJSON answers 413.
func BodyLimit(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				writeTooLarge(w, r, max)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeTooLarge(w http.ResponseWriter, r *http.Request, max int64) {
	// The list of error codes is closed, so this is validation_failed with a reason.
	w.Header().Set("Connection", "close")
	WriteError(w, r, http.StatusRequestEntityTooLarge, CodeValidationFailed, "request body is too large",
		map[string]any{"reason": "body_too_large", "max_bytes": max})
}

func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// HTTP server timeouts (REQ-SEC-105). Streams clear the write deadline with ClearWriteDeadline.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 120 * time.Second
)

// NewHTTPServer makes the http.Server of the panel with the timeouts above.
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}
}

// ClearWriteDeadline removes the write timeout of one response. A streaming handler
// (SSE, logs follow) calls it before the first write, or the server closes the stream
// after WriteTimeout. It works through the response wrappers of this package.
func ClearWriteDeadline(w http.ResponseWriter) error {
	return http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

// retryAfter formats a wait time for the Retry-After header (whole seconds, at least 1).
func retryAfter(d time.Duration) string {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// hostOnly returns the host of an address like "example.com:8443".
func hostOnly(hostport string) string {
	if strings.HasPrefix(hostport, "[") {
		if i := strings.Index(hostport, "]"); i > 0 {
			return hostport[:i+1]
		}
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 && !strings.Contains(hostport[:i], ":") {
		return hostport[:i]
	}
	return hostport
}
