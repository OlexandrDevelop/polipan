// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"crypto/rand"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns "<prefix>_<ULID>", for example nod_01J...
func NewID(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:])
	// 128 bits to 26 chars of base32 (the first char only has 3 bits).
	out := make([]byte, 26)
	var acc, bits uint
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = crockford[acc&31]
			idx--
			acc >>= 5
			bits -= 5
		}
	}
	if idx >= 0 {
		out[idx] = crockford[acc&31]
	}
	return prefix + "_" + string(out)
}
