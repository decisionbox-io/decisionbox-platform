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
// am I" surfaces (sidebar avatar, account menu, request metadata for
// client-side actions). Because the endpoint reads exclusively from
// the request context, every registered auth backend works
// identically: whichever middleware injected the principal, this
// handler returns it as-is.
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
	// Advanced RBAC (#321): expose the RESOLVED role tiers as `effective_roles`
	// so the dashboard can gate UI on the same tier the API's RequireRole checks
	// enforce. A custom role resolves to a built-in-equivalent tier (member/
	// admin) that the genuine `roles` set doesn't carry; without this the client
	// would hide member-only controls from — or wrongly redirect — a custom-role
	// user the API would actually allow. `roles` stays the genuine set (the
	// project ACL keys on it); EffectiveRoles itself is json:"-", so it is
	// surfaced here under an explicit field. On community (no resolver)
	// HierarchyRoles() falls back to the genuine built-in roles — no change.
	writeJSON(w, http.StatusOK, meResponse{
		UserPrincipal:  user,
		EffectiveRoles: user.HierarchyRoles(),
	})
}

// meResponse augments the principal with its resolved effective role tiers for
// the client. Embedding promotes every UserPrincipal field (with its own JSON
// tags); the added field carries the tiers the principal's EffectiveRoles
// (json:"-") holds internally.
type meResponse struct {
	*auth.UserPrincipal
	EffectiveRoles []string `json:"effective_roles,omitempty"`
}
