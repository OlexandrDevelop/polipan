// SPDX-License-Identifier: Apache-2.0

package auth

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
