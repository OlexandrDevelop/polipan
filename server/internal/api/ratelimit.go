// SPDX-License-Identifier: Apache-2.0

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"strconv"
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

// clientKey is the bearer token (hashed) or, without one, the client IP.
func clientKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		sum := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		return "tok:" + hex.EncodeToString(sum[:8])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

// Middleware answers 429 with Retry-After when the client is over the limit.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.Allow(clientKey(r))
		if !ok {
			secs := int(math.Ceil(wait.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded", map[string]any{"retry_after_seconds": secs})
			return
		}
		next.ServeHTTP(w, r)
	})
}
