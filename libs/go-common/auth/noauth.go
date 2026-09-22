package auth

import (
	"context"
	"net/http"
)

// AnonymousSubject is the principal subject NoAuthProvider issues. It is a real
// owner id, not a placeholder for "nobody": with authentication off every caller
// is this same person, so per-user data written by a NoAuth deployment belongs to
// it and is readable by it.
//
// It is exported because per-user repositories have to recognise it. Rows written
// before a deployment turned authentication on carry this subject and no real
// owner, and a repository that scopes reads to the caller needs to say what it
// does with them rather than compare against a string literal of its own.
const AnonymousSubject = "anonymous"

// NoAuthProvider bypasses authentication. Used for internal/testing deployments.
type NoAuthProvider struct{}

func NewNoAuthProvider() Provider {
	return &NoAuthProvider{}
}

func (p *NoAuthProvider) ValidateToken(ctx context.Context, token string) (*UserPrincipal, error) {
	return &UserPrincipal{
		Sub:   AnonymousSubject,
		OrgID: "default",
		Roles: []string{"admin"},
	}, nil
}

func (p *NoAuthProvider) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := &UserPrincipal{
				Sub:   AnonymousSubject,
				OrgID: "default",
				Roles: []string{"admin"},
			}
			ctx := WithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
