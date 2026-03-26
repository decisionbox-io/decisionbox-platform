package auth

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCProvider validates JWTs from any OIDC-compliant identity provider.
type OIDCProvider struct {
	verifier *oidc.IDTokenVerifier
	config   OIDCConfig
}

// NewOIDCProvider creates a new OIDC auth provider.
// It auto-discovers the OIDC endpoints from the issuer URL.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover OIDC provider: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.Audience,
	})

	return &OIDCProvider{
		verifier: verifier,
		config:   cfg,
	}, nil
}

// ValidateToken validates a JWT and extracts the UserPrincipal from claims.
func (p *OIDCProvider) ValidateToken(ctx context.Context, token string) (*UserPrincipal, error) {
	if token == "" {
		return nil, fmt.Errorf("empty token")
	}

	idToken, err := p.verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	return p.extractUserPrincipal(claims)
}

// extractUserPrincipal maps JWT claims to a UserPrincipal using configured claim names.
func (p *OIDCProvider) extractUserPrincipal(claims map[string]any) (*UserPrincipal, error) {
	sub, _ := claims[p.config.ClaimSub].(string)
	if sub == "" {
		return nil, fmt.Errorf("missing required claim %q", p.config.ClaimSub)
	}

	email, _ := claims[p.config.ClaimEmail].(string)

	orgID, _ := claims[p.config.ClaimOrgID].(string)
	if orgID == "" {
		orgID = p.config.DefaultOrgID
	}

	roles := p.extractRoles(claims)
	if len(roles) == 0 {
		// Claim was absent or empty — use default role.
		// Log a warning if the claim key was present but had no recognized values,
		// which suggests a misconfigured AUTH_CLAIM_ROLES.
		if _, claimExists := claims[p.config.ClaimRoles]; claimExists {
			log.Printf("[auth] warning: JWT has claim %q but no recognized roles — using default %q", p.config.ClaimRoles, p.config.DefaultRole)
		}
		roles = []string{p.config.DefaultRole}
	}

	return &UserPrincipal{
		Sub:   sub,
		Email: email,
		OrgID: orgID,
		Roles: roles,
	}, nil
}

// extractRoles extracts roles from claims, handling both string and []string formats.
func (p *OIDCProvider) extractRoles(claims map[string]any) []string {
	raw, ok := claims[p.config.ClaimRoles]
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		roles := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				roles = append(roles, s)
			}
		}
		return roles
	default:
		return nil
	}
}

// Middleware returns HTTP middleware that validates JWT tokens from the Authorization header.
func (p *OIDCProvider) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				writeJSONError(w, http.StatusUnauthorized, "missing authorization token")
				return
			}

			user, err := p.ValidateToken(r.Context(), token)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := WithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken extracts the token from the Authorization: Bearer <token> header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
