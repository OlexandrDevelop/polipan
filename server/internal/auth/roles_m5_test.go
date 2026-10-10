// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/auth"
)

// REQ-RBAC-101: the effective role in a project.
func TestEffectiveRole(t *testing.T) {
	for _, tc := range []struct {
		org       string
		overrides []string
		want      string
	}{
		{"owner", []string{"viewer"}, "owner"},
		{"admin", []string{"viewer"}, "admin"},
		{"developer", nil, "developer"},
		{"developer", []string{"viewer"}, "viewer"},
		{"viewer", []string{"developer"}, "developer"},
		{"viewer", []string{"viewer", "admin", "developer"}, "admin"},
		{"developer", []string{"bogus"}, "developer"},
	} {
		if got := auth.EffectiveRole(tc.org, tc.overrides); got != tc.want {
			t.Errorf("EffectiveRole(%s, %v) = %s, want %s", tc.org, tc.overrides, got, tc.want)
		}
	}
}

// REQ-AUTH-101: token scopes map to permissions.
func TestScopeAllows(t *testing.T) {
	for _, tc := range []struct {
		scopes []string
		perm   string
		want   bool
	}{
		{[]string{"read"}, "service:read", true},
		{[]string{"read"}, "logs:read", true},
		{[]string{"read"}, "service:deploy", false},
		{[]string{"deploy"}, "service:deploy", true},
		{[]string{"deploy"}, "service:restart", true},
		{[]string{"deploy"}, "service:read", true},
		{[]string{"deploy"}, "service:update", false},
		{[]string{"write"}, "service:update", true},
		{[]string{"write"}, "service:deploy", true},
		{[]string{"write"}, "secret:write", true},
		{[]string{"write"}, "member:manage", false},
		{[]string{"write"}, "secret:destroy", false},
		{[]string{"admin"}, "member:manage", true},
		{[]string{"read", "deploy"}, "service:rollback", true},
		{nil, "service:read", false},
		{[]string{"admin"}, "not:a-permission", false},
	} {
		if got := auth.ScopeAllows(tc.scopes, tc.perm); got != tc.want {
			t.Errorf("ScopeAllows(%v, %s) = %v, want %v", tc.scopes, tc.perm, got, tc.want)
		}
	}
}

// RFC 6238 test vector (SHA-1, secret "12345678901234567890", time 59 s): 94287082, so 287082 with 6 digits.
func TestTOTPVector(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(59, 0)
	if got := auth.TOTPCode(secret, now); got != "287082" {
		t.Fatalf("code %s", got)
	}
	if _, ok := auth.CheckTOTP(secret, "287082", now.Add(25*time.Second)); !ok {
		t.Fatal("a code of the step before is refused")
	}
	if _, ok := auth.CheckTOTP(secret, "287082", now.Add(3*time.Minute)); ok {
		t.Fatal("an old code is accepted")
	}
	plain, hashes, err := auth.NewRecoveryCodes()
	if err != nil || len(plain) != 10 || len(hashes) != 10 || auth.HashToken(auth.NormalizeRecoveryCode(plain[3])) != hashes[3] {
		t.Fatalf("recovery codes: %v %v", plain, err)
	}
}
