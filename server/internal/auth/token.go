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

// APITokenPrefix starts every API token. The token is "pp_" and 40 random characters.
const APITokenPrefix = "pp_"

// NewAPIToken returns a new API token. Show it once, store only HashToken of it.
func NewAPIToken() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 40)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random api token: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return APITokenPrefix + string(b), nil
}

// NewOneTimeToken returns a random value for invitation and password reset links.
func NewOneTimeToken() (string, error) { return NewSessionValue() }
