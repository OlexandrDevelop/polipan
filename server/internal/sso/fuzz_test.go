// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"testing"
	"time"
)

// FuzzVerifyIDToken feeds ID tokens from an identity provider or an attacker. It must never panic, and a
// token that nobody signed with the key must never be accepted.
func FuzzVerifyIDToken(f *testing.F) {
	f.Add("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.")
	f.Add("eyJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJodHRwczovL2lkcCIsInN1YiI6IngifQ.AAAA")
	f.Add("a.b.c")
	f.Add("..")
	f.Fuzz(func(t *testing.T, raw string) {
		keys := []jwk{{Kty: "EC", Kid: "k", Crv: "P-256", X: "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU", Y: "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}}
		want := tokenCheck{Issuers: []string{"https://idp"}, Audience: "client", Nonce: "n", Now: time.Unix(1_700_000_000, 0)}
		if _, err := verifyIDToken(raw, keys, want); err == nil {
			t.Fatalf("an unsigned token was accepted: %q", raw)
		}
	})
}
