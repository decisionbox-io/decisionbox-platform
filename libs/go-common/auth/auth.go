package auth

import (
	"context"
	"encoding/json"
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

// Compile-time checks: all providers satisfy the Provider interface.
var (
	_ Provider = (*NoAuthProvider)(nil)
	_ Provider = (*OIDCProvider)(nil)
	_ Provider = (*ChainProvider)(nil)
)

// writeJSONError writes a JSON error response matching the API's error format.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
