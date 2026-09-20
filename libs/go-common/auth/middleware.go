package auth

import (
	"context"
	"net/http"
	"strings"
)

// ResolvePermissionsMiddleware enriches the authenticated principal in context
// with the permissions its roles grant, via the registered PermissionResolver,
// and replaces the context principal with the enriched copy. It must run after
// the auth provider has attached the principal.
//
// It is exported (not just used by the API server) so enterprise plugins that
// mount via RegisterGlobalMiddleware — and therefore run their OWN auth
// middleware outside the server's chain (knowledge sources, executive
// summaries) — can apply the same resolution. Without it a custom role would be
// locked out of those plugins' routes (its built-in-equivalent tier is never
// appended) and /me-style permission checks would see nothing.
//
// No principal in context, or a resolver error, is non-fatal: the request
// proceeds with the un-enriched principal (built-in roles still apply). With no
// resolver registered (community) the no-op resolver leaves the principal
// unchanged — zero behaviour change.
func ResolvePermissionsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok || p == nil {
				next.ServeHTTP(w, r)
				return
			}
			perms, roles, err := GetPermissionResolver().Resolve(r.Context(), p)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			enriched := *p
			enriched.Permissions = perms
			// The resolver's effective roles (original + appended built-in tier
			// for custom roles) go on EffectiveRoles for the RequireRole
			// hierarchy — NOT on Roles, which stays the original set so a
			// synthesized tier can't satisfy a project ACL (CanAccessProject).
			if len(roles) > 0 {
				enriched.EffectiveRoles = roles
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), &enriched)))
		})
	}
}

// ProjectACLLoader returns a project's org id and allowed-roles ACL for the
// given id. found=false (nil error) means the project doesn't exist; the
// middleware then passes through so the downstream handler produces its own
// 404. It exists so the ACL middleware doesn't depend on any concrete project
// model or repository package.
type ProjectACLLoader func(ctx context.Context, projectID string) (orgID string, allowedRoles []string, found bool, err error)

// ProjectACLMiddleware enforces the role-based project ACL (advanced RBAC, #321)
// for every request whose path is under pathPrefix + "{id}" (e.g. pathPrefix
// "/api/v1/projects/" gates /api/v1/projects/{id}/…). It is the shared
// choke point used by both the API server (over its inner project routes) and
// the global-middleware plugins that short-circuit their own project-scoped
// routes.
//
// A restricted project is invisible (404, never 403, so its existence isn't
// disclosed) to a principal whose roles don't cover it. Requests with no {id}
// segment, no principal (off the auth chain), or an unknown/unloadable project
// pass through unchanged. It must run after ResolvePermissionsMiddleware so a
// custom role's effective tier is already on the principal.
func ProjectACLMiddleware(pathPrefix string, load ProjectACLLoader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := projectIDAfterPrefix(r.URL.Path, pathPrefix)
			if id == "" {
				next.ServeHTTP(w, r)
				return
			}
			u, ok := FromContext(r.Context())
			if !ok || u == nil {
				next.ServeHTTP(w, r)
				return
			}
			orgID, allowedRoles, found, err := load(r.Context(), id)
			if err != nil || !found {
				next.ServeHTTP(w, r)
				return
			}
			allowed := CanAccessProject(u, orgID, allowedRoles)
			RecordAccessDecision(r.Context(), AccessDecision{
				ProjectID:    id,
				UserEmail:    u.Email,
				OrgID:        orgID,
				Roles:        u.Roles,
				AllowedRoles: allowedRoles,
				Action:       ProjectActionForMethod(r.Method),
				Allowed:      allowed,
			})
			if !allowed {
				WriteJSONError(w, http.StatusNotFound, "project not found")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// projectIDAfterPrefix returns the first path segment after pathPrefix, or ""
// when the path is not under the prefix or carries no id segment (e.g. the bare
// collection route).
func projectIDAfterPrefix(path, pathPrefix string) string {
	if !strings.HasPrefix(path, pathPrefix) {
		return ""
	}
	rest := path[len(pathPrefix):]
	if rest == "" {
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// ProjectActionForMethod maps an HTTP method to the permission-shaped action
// label recorded in the access-audit trail. Exported so enterprise gates that
// enforce the project ACL for non-/api/v1/projects/ routes (e.g. discovery-
// scoped exec summaries) record the same action vocabulary.
func ProjectActionForMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return "project.view"
	case http.MethodDelete:
		return "project.delete"
	default:
		return "project.edit"
	}
}
