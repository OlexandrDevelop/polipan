// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewSessionValue returns a random 256-bit value for the session cookie.
func NewSessionValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random session value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is the SHA-256 of a token, in hex. Only this hash is stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionID is the database id of a session: the hash of the cookie value.
func SessionID(cookieValue string) string { return HashToken(cookieValue) }

// CSRFToken is the CSRF token of a session. It comes from the cookie value, so the
// server needs to store only its hash, and a logged-in client can read it again
// (login answer and GET /me). A page of another site cannot read it.
func CSRFToken(cookieValue string) string {
	sum := sha256.Sum256([]byte("pp-csrf:" + cookieValue))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
