// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"errors"
	"strings"
)

// Errors of the access rules. The handler maps each one to a short reason code.
var (
	ErrDomain       = errors.New("sso: the email domain is not allowed")
	ErrHostedDomain = errors.New("sso: the Google hosted domain (hd) is not allowed")
	ErrUnverified   = errors.New("sso: the email is not verified")
	ErrUnsafeJIT    = errors.New("sso: user creation needs allowed_domains on Google and GitHub when a default role is set")
)

// NormalizeDomains lowercases the list, removes a leading "@" and drops empty and duplicate entries.
func NormalizeDomains(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, d := range list {
		d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "@")
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// UnsafeJIT is true for a provider that would give a default role to anybody with a Google or GitHub
// account: it creates users, it has a default role, and it limits no domain. Saving such a provider is
// refused, and a login with one never creates a user.
func UnsafeJIT(kind string, allowedDomains []string, autoCreate bool, defaultRole string) bool {
	return autoCreate && defaultRole != "" && len(allowedDomains) == 0 && (kind == KindGoogle || kind == KindGitHub)
}

// CheckIdentity applies the rules every login must pass: a verified email, an allowed domain and,
// for Google with a domain list, a matching hd claim. Everybody has a Google account, so a mail address
// at an allowed domain is not enough: hd says that the account belongs to the Workspace of the company.
func CheckIdentity(kind string, allowedDomains []string, id *Identity) error {
	if id.Email == "" || !id.EmailVerified {
		return ErrUnverified
	}
	allowed := NormalizeDomains(allowedDomains)
	if len(allowed) == 0 {
		return nil
	}
	_, domain, ok := strings.Cut(id.Email, "@")
	if !ok || !contains(allowed, strings.ToLower(domain)) {
		return ErrDomain
	}
	if kind == KindGoogle && !contains(allowed, strings.ToLower(id.HostedDomain)) {
		return ErrHostedDomain
	}
	return nil
}
