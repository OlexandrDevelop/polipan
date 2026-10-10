// SPDX-License-Identifier: Apache-2.0

// Package clock gives time to code that has timers or thresholds.
// Tests use Fake so they never sleep (REQ-CORE-031).
package clock

import (
	"sync"
	"time"
)

// Clock tells the current time.
type Clock interface {
	Now() time.Time
}

// Real is the system clock.
type Real struct{}

// Now returns the current time in UTC.
func (Real) Now() time.Time { return time.Now().UTC() }

// Fake is a clock that only moves when the test says so.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake creates a fake clock that starts at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake time forward.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
