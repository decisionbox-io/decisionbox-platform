package auth

import (
	"context"
	"net/http"
)

// NoAuthProvider bypasses authentication. Used for internal/testing deployments.
type NoAuthProvider struct{}

func NewNoAuthProvider() Provider {
	return &NoAuthProvider{}
}

func (p *NoAuthProvider) ValidateToken(ctx context.Context, token string) (*UserPrincipal, error) {
	return &UserPrincipal{
		Sub:   "anonymous",
		OrgID: "default",
		Roles: []string{"admin"},
	}, nil
}

func (p *NoAuthProvider) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := &UserPrincipal{
				Sub:   "anonymous",
				OrgID: "default",
				Roles: []string{"admin"},
			}
			ctx := WithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
