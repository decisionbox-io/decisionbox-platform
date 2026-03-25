package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ChainProvider routes authentication to the appropriate provider based on the token format.
// Currently supports NoAuth and OIDC. Designed to support API key auth in the future (#99).
type ChainProvider struct {
	oidc   *OIDCProvider
	noAuth *NoAuthProvider

	// authEnabled controls whether authentication is enforced.
	// When false, all requests are handled by noAuth.
	authEnabled bool
}

// ChainConfig configures the auth provider chain.
type ChainConfig struct {
	AuthEnabled bool
	OIDCConfig  *OIDCConfig // nil when auth is disabled
}

// NewChainProvider creates an auth provider chain.
// When authEnabled is false, all requests use NoAuthProvider.
// When authEnabled is true, JWT tokens are validated via OIDCProvider.
func NewChainProvider(ctx context.Context, cfg ChainConfig) (*ChainProvider, error) {
	chain := &ChainProvider{
		noAuth:      &NoAuthProvider{},
		authEnabled: cfg.AuthEnabled,
	}

	if cfg.AuthEnabled {
		if cfg.OIDCConfig == nil {
			return nil, fmt.Errorf("OIDC config is required when auth is enabled")
		}
		oidcProvider, err := NewOIDCProvider(ctx, *cfg.OIDCConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
		}
		chain.oidc = oidcProvider
	}

	return chain, nil
}

// ValidateToken validates a token using the appropriate provider.
func (c *ChainProvider) ValidateToken(ctx context.Context, token string) (*UserPrincipal, error) {
	if !c.authEnabled {
		return c.noAuth.ValidateToken(ctx, token)
	}

	// Future #99: route "dbx_" prefixed tokens to APIKeyProvider
	if strings.HasPrefix(token, "dbx_") {
		return nil, fmt.Errorf("API key authentication is not yet supported")
	}

	return c.oidc.ValidateToken(ctx, token)
}

// Middleware returns HTTP middleware that authenticates requests.
func (c *ChainProvider) Middleware() func(http.Handler) http.Handler {
	if !c.authEnabled {
		return c.noAuth.Middleware()
	}

	return c.oidc.Middleware()
}
