package handler

import (
	"net/http"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// callerSub returns the authenticated caller's subject. With NoAuth (the
// default) the middleware injects a principal with Sub "anonymous", so
// that is the value returned; when no principal is present at all it also
// falls back to "anonymous" so callers never see an empty owner.
func callerSub(r *http.Request) string {
	if u, ok := auth.FromContext(r.Context()); ok && u != nil && u.Sub != "" {
		return u.Sub
	}
	return "anonymous"
}

// callerIsAdmin reports whether the authenticated caller holds the admin
// role. With NoAuth the injected principal carries "admin", so this is
// true — preserving the pre-auth behaviour where every request is
// effectively an admin.
func callerIsAdmin(r *http.Request) bool {
	if u, ok := auth.FromContext(r.Context()); ok && u != nil {
		for _, role := range u.Roles {
			if role == "admin" {
				return true
			}
		}
	}
	return false
}

// canAccessSession reports whether the caller may read, rename, or delete
// the given ask session: the owner may, and an admin may (owner-or-admin).
// Under NoAuth the caller is the "anonymous" admin, so this is always
// true and per-user scoping is a no-op — exactly the pre-auth behaviour.
func canAccessSession(r *http.Request, s *commonmodels.AskSession) bool {
	if s == nil {
		return false
	}
	return s.UserID == callerSub(r) || callerIsAdmin(r)
}
