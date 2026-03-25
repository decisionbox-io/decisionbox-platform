package auth

import (
	"context"
	"net/http"
)

// UserPrincipal represents the authenticated user.
// Field names match standard OIDC claim names.
type UserPrincipal struct {
	Sub   string   `json:"sub"`
	Email string   `json:"email"`
	OrgID string   `json:"org_id"`
	Roles []string `json:"roles"`
}

type contextKey string

const userKey contextKey = "user"

// FromContext extracts UserPrincipal from request context.
func FromContext(ctx context.Context) (*UserPrincipal, bool) {
	u, ok := ctx.Value(userKey).(*UserPrincipal)
	return u, ok
}

// WithUser stores UserPrincipal in context.
func WithUser(ctx context.Context, user *UserPrincipal) context.Context {
	return context.WithValue(ctx, userKey, user)
}

// Provider is the interface for authentication backends.
type Provider interface {
	ValidateToken(ctx context.Context, token string) (*UserPrincipal, error)
	Middleware() func(http.Handler) http.Handler
}
