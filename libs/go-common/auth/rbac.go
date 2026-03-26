package auth

import (
	"net/http"
)

// roleHierarchy defines the privilege level for each role.
// Higher values have more privileges. A user with a higher role
// automatically satisfies checks for lower roles.
var roleHierarchy = map[string]int{
	"viewer": 1,
	"member": 2,
	"admin":  3,
}

// RequireRole returns HTTP middleware that enforces role-based access control.
// The request is allowed if the user has any role with a privilege level
// greater than or equal to the minimum required role.
func RequireRole(minRole string) func(http.Handler) http.Handler {
	minLevel, ok := roleHierarchy[minRole]
	if !ok {
		panic("auth: unknown role " + minRole + " passed to RequireRole (valid: viewer, member, admin)")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := FromContext(r.Context())
			if !ok {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			if !hasMinRole(user.Roles, minLevel) {
				writeJSONError(w, http.StatusForbidden, "insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// hasMinRole checks if any of the user's roles meets or exceeds the minimum level.
func hasMinRole(userRoles []string, minLevel int) bool {
	for _, role := range userRoles {
		if level, ok := roleHierarchy[role]; ok && level >= minLevel {
			return true
		}
	}
	return false
}
