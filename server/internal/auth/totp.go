// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 uses HMAC-SHA1, authenticator apps need it
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30
	totpDigits = 6
	// RecoveryCodeCount is the number of one-time recovery codes (10-security, section 1).
	RecoveryCodeCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret.
func NewTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("random totp secret: %w", err)
	}
	return b, nil
}

// TOTPURI is the otpauth URI for an authenticator app.
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{"secret": {b32.EncodeToString(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%0*d", totpDigits, n)
}

// TOTPStep is the time step of a moment.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// CheckTOTP checks a code for the steps around now (one step of clock drift is allowed).
// It returns the step of the code. The caller must refuse a step that is not newer than the last used one.
func CheckTOTP(secret []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	found := int64(-1)
	for _, step := range []int64{cur - 1, cur, cur + 1} {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			found = step
		}
	}
	return found, found >= 0
}

// NewRecoveryCodes makes the plain recovery codes ("xxxxx-xxxxx") and their hashes.
func NewRecoveryCodes() (plain, hashes []string, err error) {
	for i := 0; i < RecoveryCodeCount; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, fmt.Errorf("random recovery code: %w", err)
		}
		s := strings.ToLower(b32.EncodeToString(b))[:10]
		code := s[:5] + "-" + s[5:]
		plain = append(plain, code)
		hashes = append(hashes, HashToken(NormalizeRecoveryCode(code)))
	}
	return plain, hashes, nil
}

// NormalizeRecoveryCode lowercases a code and removes the dash and spaces.
func NormalizeRecoveryCode(code string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(code)))
}

// Seal encrypts a small secret with the key (AES-256-GCM). The nonce is in front of the result.
func Seal(key, plain []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plain, []byte(aad)), nil
}

// Open decrypts the result of Seal.
func Open(key, data []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("sealed value is too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(aad))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("the key must have 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// TOTPCode is the code that an authenticator app shows at a moment. Tests use it.
func TOTPCode(secret []byte, t time.Time) string { return totpCode(secret, TOTPStep(t)) }
