// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

var b64 = base64.RawStdEncoding

// hashPassword makes an argon2id hash with a new salt.
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword compares in constant time.
func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked when the user does not exist, so the time is the same.
var dummyHash, _ = hashPassword("not-a-real-password")

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func sign(secret []byte, purpose, value string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

func signedEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// csrfToken is the form value for a csrf cookie value.
func csrfToken(secret []byte, cookie string) string { return sign(secret, "csrf", cookie) }

func csrfValid(secret []byte, cookie, formValue string) bool {
	return cookie != "" && formValue != "" && signedEqual(csrfToken(secret, cookie), formValue)
}

// The time trap: a form must be at least minAge old and at most maxAge old.
const (
	trapMinAge = 3 * time.Second
	trapMaxAge = 2 * time.Hour
)

func trapToken(secret []byte, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + "." + sign(secret, "trap", ts)
}

func trapValid(secret []byte, value string, now time.Time) bool {
	ts, mac, ok := strings.Cut(value, ".")
	if !ok || !signedEqual(sign(secret, "trap", ts), mac) {
		return false
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(unix, 0))
	return age >= trapMinAge && age <= trapMaxAge
}

// limiter keeps the times of the last events in memory, per key.
type limiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	now    func() time.Time
}

func newLimiter() *limiter { return &limiter{events: map[string][]time.Time{}, now: time.Now} }

// allow records an event and says whether it is under the limit in the window.
func (l *limiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.events[key][:0]
	for _, t := range l.events[key] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	if len(l.events) > 20000 {
		for k, v := range l.events {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > 24*time.Hour {
				delete(l.events, k)
			}
		}
	}
	return true
}
