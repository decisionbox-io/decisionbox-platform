package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
)

// Me returns the authenticated principal that the auth middleware
// attached to the request context. The handler writes the standard
// APIResponse envelope (`{"data": {...}}`), so a typed consumer
// decodes the response into a struct with a single `Data UserPrincipal`
// field — same wrapper every other endpoint in this server uses.
//
// All authenticated dashboards reach this endpoint to populate "who
// am I" surfaces (sidebar avatar, account menu, audit metadata for
// client-side actions). Because the endpoint reads exclusively from
// the request context, every auth backend works identically:
//
//   - NoAuth deployments — middleware injects the anonymous principal,
//     this handler returns it.
//   - OIDC / customer-IdP — middleware validates the JWT and writes
//     the principal; this handler returns it.
//   - Cloud-SSO chain — middleware dispatches to cloud-auth or OIDC by
//     issuer; whichever validator matched produces the principal and
//     this handler returns it.
//
// 200 with the principal on success; 401 if no principal is in the
// context (which is unusual — the auth middleware should already have
// short-circuited unauth requests, but the explicit check here keeps
// the handler safe to mount independently of the middleware ordering).
func Me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.FromContext(r.Context())
	if !ok || user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, user)
}
