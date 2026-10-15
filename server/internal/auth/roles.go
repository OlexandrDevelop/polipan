// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sort"
	"strings"
)

// Roles of an organization (10-security, section 3).
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleViewer    = "viewer"
)

// matrix is the permission matrix of docs/spec/10-security.md. The four letters are
// owner, admin, developer and viewer. Y = allowed, - = denied, P = allowed for
// developers only on an environment that is not protected.
var matrix = map[string]string{
	"org:read":          "YYYY",
	"org:update":        "YY--",
	"org:delete":        "Y---",
	"member:read":       "YYYY",
	"member:manage":     "YY--",
	"team:manage":       "YY--",
	"sso:manage":        "YY--",
	"token:own":         "YYYY",
	"token:manage":      "YY--",
	"audit:read":        "YY--",
	"audit:export":      "YY--",
	"node:read":         "YYYY",
	"node:manage":       "YY--",
	"project:read":      "YYYY",
	"project:create":    "YYY-",
	"project:update":    "YYY-",
	"project:delete":    "YY--",
	"env:read":          "YYYY",
	"env:create":        "YYY-",
	"env:update":        "YYP-",
	"env:delete":        "YYP-",
	"service:read":      "YYYY",
	"service:create":    "YYP-",
	"service:update":    "YYP-",
	"service:delete":    "YYP-",
	"service:deploy":    "YYP-",
	"service:rollback":  "YYP-",
	"service:promote":   "YYP-",
	"service:restart":   "YYY-",
	"service:move":      "YYP-",
	"service:exec":      "YYP-",
	"logs:read":         "YYYY",
	"metrics:read":      "YYYY",
	"variable:read":     "YYYY",
	"variable:write":    "YYP-",
	"secret:list":       "YYY-",
	"secret:write":      "YYP-",
	"secret:destroy":    "YY--",
	"secret:reveal":     "YY--",
	"domain:manage":     "YYP-",
	"volume:manage":     "YYP-",
	"backup:read":       "YYYY",
	"backup:manage":     "YYP-",
	"backup:restore":    "YY--",
	"alert:read":        "YYYY",
	"alert:manage":      "YY--",
	"template:read":     "YYYY",
	"template:manage":   "YYY-",
	"git:manage":        "YY--",
	"preview:manage":    "YYY-",
	"config:apply":      "YYP-",
	"deployment:read":   "YYYY",
	"deployment:cancel": "YYP-",
}

// KnownPermission tells if the name is in the matrix.
func KnownPermission(permission string) bool {
	_, ok := matrix[permission]
	return ok
}

func roleIndex(role string) int {
	switch role {
	case RoleOwner:
		return 0
	case RoleAdmin:
		return 1
	case RoleDeveloper:
		return 2
	case RoleViewer:
		return 3
	}
	return -1
}

// Can tells if the role has the permission. envProtected says that the object is in a
// protected environment: then a developer is denied the permissions marked P.
// This is the one authorization function of REQ-RBAC-101. Unknown names are denied.
func Can(role, permission string, envProtected bool) bool {
	row, ok := matrix[permission]
	i := roleIndex(role)
	if !ok || i < 0 {
		return false
	}
	switch row[i] {
	case 'Y':
		return true
	case 'P':
		return !envProtected || i < 2
	}
	return false
}

// IsAdmin is true for owner and admin.
func IsAdmin(role string) bool { return role == RoleOwner || role == RoleAdmin }

// EffectiveRole is the role of a user in a project (10-security, section 3). orgRole is the org role,
// overrides are the roles of the user and of the teams of the user on this project.
// Owner and admin keep their role everywhere. For a developer or a viewer the highest override wins,
// up or down. Without an override the org role stays.
func EffectiveRole(orgRole string, overrides []string) string {
	if IsAdmin(orgRole) || len(overrides) == 0 {
		return orgRole
	}
	best := ""
	for _, o := range overrides {
		if i := roleIndex(o); i >= 0 && (best == "" || i < roleIndex(best)) {
			best = o
		}
	}
	if best == "" {
		return orgRole
	}
	return best
}

// Token scopes (10-security, section 1).
const (
	ScopeRead   = "read"
	ScopeDeploy = "deploy"
	ScopeWrite  = "write"
	ScopeAdmin  = "admin"
)

// ValidScope tells if the name is a token scope.
func ValidScope(s string) bool {
	switch s {
	case ScopeRead, ScopeDeploy, ScopeWrite, ScopeAdmin:
		return true
	}
	return false
}

var deployPermissions = map[string]bool{
	"service:deploy": true, "service:rollback": true, "service:promote": true, "service:restart": true,
}

// ScopeAllows tells if one of the token scopes covers the permission. This is only the scope side:
// the role of the user must allow it too (a token never has more rights than its user).
//
//	read   = every *:read permission, and token:own (list and revoke own tokens)
//	deploy = read + deploy, rollback, promote and restart
//	write  = deploy + every permission a developer can have (the non-admin writes)
//	admin  = everything
func ScopeAllows(scopes []string, permission string) bool {
	row, ok := matrix[permission]
	if !ok {
		return false
	}
	for _, s := range scopes {
		switch s {
		case ScopeAdmin:
			return true
		case ScopeWrite:
			if row[2] == 'Y' || row[2] == 'P' {
				return true
			}
			fallthrough
		case ScopeDeploy:
			if deployPermissions[permission] {
				return true
			}
			fallthrough
		case ScopeRead:
			if strings.HasSuffix(permission, ":read") || permission == "token:own" {
				return true
			}
		}
	}
	return false
}

// Permissions lists the permissions the role has on an unprotected environment, and those it has only
// when the environment is not protected (the P cells). The web app uses this for the hide and disable rule.
func Permissions(role string) (allowed, unprotectedOnly []string) {
	i := roleIndex(role)
	if i < 0 {
		return nil, nil
	}
	for name, row := range matrix {
		switch row[i] {
		case 'Y':
			allowed = append(allowed, name)
		case 'P':
			if i < 2 {
				allowed = append(allowed, name)
			} else {
				unprotectedOnly = append(unprotectedOnly, name)
			}
		}
	}
	sort.Strings(allowed)
	sort.Strings(unprotectedOnly)
	return allowed, unprotectedOnly
}
