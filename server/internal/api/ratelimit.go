// SPDX-License-Identifier: Apache-2.0

package api

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/server/internal/shared/clock"
)

// RateLimiter is a token bucket per key (api_rate_limit: requests per minute).
type RateLimiter struct {
	mu       sync.Mutex
	clock    clock.Clock
	perMin   int
	buckets  map[string]*bucket
	lastGC   time.Time
	gcAfter  time.Duration
	maxKeys  int
	capacity float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter creates a limiter that allows perMinute requests per key per minute.
func NewRateLimiter(c clock.Clock, perMinute int) *RateLimiter {
	return &RateLimiter{
		clock:    c,
		perMin:   perMinute,
		buckets:  map[string]*bucket{},
		lastGC:   c.Now(),
		gcAfter:  time.Minute,
		maxKeys:  10000,
		capacity: float64(perMinute),
	}
}

// Allow takes one token for the key. When there is none it returns the time to wait.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.collect(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	perSecond := float64(l.perMin) / 60
	b.tokens = math.Min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
	return false, wait
}

// collect removes full buckets once a minute so the map stays small.
func (l *RateLimiter) collect(now time.Time) {
	if now.Sub(l.lastGC) < l.gcAfter && len(l.buckets) < l.maxKeys {
		return
	}
	l.lastGC = now
	perSecond := float64(l.perMin) / 60
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*perSecond >= l.capacity {
			delete(l.buckets, k)
		}
	}
}

// clientKey is the client IP (RemoteAddr). X-Forwarded-For is read only from a trusted proxy,
// see TrustedProxies, because any client can send it.
// The key of a logged-in user (see Handlers.guard) is used in addition, after the session is valid.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// userKey is the extra key for a validated user.
func userKey(userID string) string { return "usr:" + userID }

// Check takes a token for the key and writes the 429 answer when there is none.
// It returns true when the request may go on.
func (l *RateLimiter) Check(w http.ResponseWriter, r *http.Request, key string) bool {
	ok, wait := l.Allow(key)
	if ok {
		return true
	}
	w.Header().Set("Retry-After", retryAfter(wait))
	WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded",
		map[string]any{"retry_after_seconds": int((wait + time.Second - 1) / time.Second)})
	return false
}

// Middleware answers 429 with Retry-After when the client IP is over the limit.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.Check(w, r, clientKey(r)) {
			next.ServeHTTP(w, r)
		}
	})
}

// TrustedProxies sets r.RemoteAddr to the real client when the request comes from a trusted
// proxy (the Caddy of the node). The last X-Forwarded-For entry is used: it is the address
// that the proxy saw. A request from any other address keeps its RemoteAddr, and its
// X-Forwarded-For is ignored.
func TrustedProxies(cidrs []string) func(http.Handler) http.Handler {
	var nets []*net.IPNet
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			nets = append(nets, n)
		}
	}
	return func(next http.Handler) http.Handler {
		if len(nets) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			peer := net.ParseIP(host)
			for _, n := range nets {
				if peer == nil || !n.Contains(peer) {
					continue
				}
				parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
					r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
				}
				break
			}
			next.ServeHTTP(w, r)
		})
	}
}
