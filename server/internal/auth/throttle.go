// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sync"
	"time"

	"github.com/polipan/polipan/server/internal/shared/clock"
)

// Login throttle (10-security, section 1): 5 failed tries in 15 minutes for one
// account, or for one IP, start a lock of 15 minutes.
const (
	MaxFailures    = 5
	FailureWindow  = 15 * time.Minute
	LockDuration   = 15 * time.Minute
	throttleMaxKey = 50000
)

type throttleEntry struct {
	failures    []time.Time
	lockedUntil time.Time
}

// Throttle counts failed logins per key. A key is "acct:<email>" or "ip:<address>".
// It lives in memory, so a restart of the server clears it.
type Throttle struct {
	mu      sync.Mutex
	clock   clock.Clock
	entries map[string]*throttleEntry
}

// NewThrottle creates a login throttle.
func NewThrottle(c clock.Clock) *Throttle {
	return &Throttle{clock: c, entries: map[string]*throttleEntry{}}
}

// Locked tells if any of the keys is locked, and for how long.
func (t *Throttle) Locked(keys ...string) (bool, time.Duration) {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		if e, ok := t.entries[k]; ok && e.lockedUntil.After(now) {
			if d := e.lockedUntil.Sub(now); d > wait {
				wait = d
			}
		}
	}
	return wait > 0, wait
}

// Fail records a failed login for every key. The fifth failure in the window locks the key.
func (t *Throttle) Fail(keys ...string) {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) >= throttleMaxKey {
		t.collect(now)
	}
	for _, k := range keys {
		e := t.entries[k]
		if e == nil {
			e = &throttleEntry{}
			t.entries[k] = e
		}
		kept := e.failures[:0]
		for _, f := range e.failures {
			if now.Sub(f) < FailureWindow {
				kept = append(kept, f)
			}
		}
		e.failures = append(kept, now)
		if len(e.failures) >= MaxFailures {
			e.lockedUntil = now.Add(LockDuration)
			e.failures = nil
		}
	}
}

// Reset forgets the failures of a key after a good login.
func (t *Throttle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// collect removes entries that have no lock and no recent failure.
func (t *Throttle) collect(now time.Time) {
	for k, e := range t.entries {
		if e.lockedUntil.After(now) {
			continue
		}
		recent := false
		for _, f := range e.failures {
			if now.Sub(f) < FailureWindow {
				recent = true
			}
		}
		if !recent {
			delete(t.entries, k)
		}
	}
}
