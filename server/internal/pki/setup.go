// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// SetupToken is the token of the one-time setup URL (REQ-AUTH-006). It comes from the
// master key, so it is the same after a restart and needs no storage. It is only
// accepted while no user exists.
func SetupToken(kek []byte) string {
	m := hmac.New(sha256.New, kek)
	m.Write([]byte("polipan setup token v1"))
	return hex.EncodeToString(m.Sum(nil)[:16])
}
