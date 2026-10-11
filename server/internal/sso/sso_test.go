// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"encoding/base64"
	"errors"
	"testing"
)

func b64enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// AC-SSO-01: the access rules of a provider.
func TestAC_SSO_01_CheckIdentity(t *testing.T) {
	ok := &Identity{Email: "a@example.com", EmailVerified: true, HostedDomain: "example.com"}
	cases := []struct {
		name    string
		kind    string
		domains []string
		id      *Identity
		want    error
	}{
		{"open provider", KindOIDC, nil, ok, nil},
		{"allowed domain", KindOIDC, []string{"Example.com"}, ok, nil},
		{"other domain", KindOIDC, []string{"corp.io"}, ok, ErrDomain},
		{"google with hd", KindGoogle, []string{"example.com"}, ok, nil},
		{"google without hd", KindGoogle, []string{"example.com"}, &Identity{Email: "a@example.com", EmailVerified: true}, ErrHostedDomain},
		{"google other hd", KindGoogle, []string{"example.com"}, &Identity{Email: "a@example.com", EmailVerified: true, HostedDomain: "evil.com"}, ErrHostedDomain},
		{"unverified", KindOIDC, nil, &Identity{Email: "a@example.com"}, ErrUnverified},
		{"no email", KindOIDC, nil, &Identity{EmailVerified: true}, ErrUnverified},
		{"suffix trick", KindOIDC, []string{"example.com"}, &Identity{Email: "a@badexample.com", EmailVerified: true}, ErrDomain},
	}
	for _, c := range cases {
		if err := CheckIdentity(c.kind, c.domains, c.id); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// The unsafe provider: everybody with a Google or GitHub account would get the default role.
func TestUnsafeJIT(t *testing.T) {
	if !UnsafeJIT(KindGoogle, nil, true, "viewer") || !UnsafeJIT(KindGitHub, []string{}, true, "developer") {
		t.Error("an open Google or GitHub provider with a default role must be unsafe")
	}
	for _, safe := range []bool{
		UnsafeJIT(KindGoogle, []string{"example.com"}, true, "viewer"),
		UnsafeJIT(KindGoogle, nil, false, "viewer"),
		UnsafeJIT(KindGoogle, nil, true, ""),
		UnsafeJIT(KindOIDC, nil, true, "viewer"),
	} {
		if safe {
			t.Error("a safe provider was called unsafe")
		}
	}
}

func TestVerifyIDTokenRefusesWeakTokens(t *testing.T) {
	// "none" and HMAC tokens must never pass, whatever the claims say.
	for _, alg := range []string{"none", "HS256"} {
		raw := b64enc(`{"alg":"`+alg+`"}`) + "." + b64enc(`{"iss":"i","aud":"c","sub":"s","exp":99999999999,"nonce":"n"}`) + "."
		_, err := verifyIDToken(raw, nil, tokenCheck{Issuers: []string{"i"}, Audience: "c", Nonce: "n"})
		if err == nil {
			t.Errorf("alg %s was accepted", alg)
		}
	}
	if _, err := verifyIDToken("a.b", nil, tokenCheck{}); err == nil {
		t.Error("a token with two parts was accepted")
	}
}
