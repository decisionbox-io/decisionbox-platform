package auth

import (
	"fmt"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
)

// OIDCConfig holds the configuration for the OIDC authentication provider.
type OIDCConfig struct {
	// IssuerURL is the OIDC issuer URL (e.g., https://your-tenant.auth0.com).
	// The provider auto-discovers endpoints via {issuer}/.well-known/openid-configuration.
	IssuerURL string

	// Audience is the expected JWT audience claim.
	Audience string

	// Claim names — configurable to support different IdP claim conventions.
	ClaimSub   string // JWT claim name for subject (default: "sub")
	ClaimEmail string // JWT claim name for email (default: "email")
	ClaimOrgID string // JWT claim name for organization (default: "org_id")
	ClaimRoles string // JWT claim name for roles (default: "roles")

	// Defaults when claims are absent from the JWT.
	DefaultOrgID string // fallback org_id (default: "default")
	DefaultRole  string // fallback role (default: "member")
}

// LoadOIDCConfigFromEnv loads OIDC configuration from environment variables.
func LoadOIDCConfigFromEnv() (*OIDCConfig, error) {
	cfg := &OIDCConfig{
		IssuerURL:    goconfig.GetEnv("AUTH_ISSUER_URL"),
		Audience:     goconfig.GetEnv("AUTH_AUDIENCE"),
		ClaimSub:     goconfig.GetEnvOrDefault("AUTH_CLAIM_SUB", "sub"),
		ClaimEmail:   goconfig.GetEnvOrDefault("AUTH_CLAIM_EMAIL", "email"),
		ClaimOrgID:   goconfig.GetEnvOrDefault("AUTH_CLAIM_ORG_ID", "org_id"),
		ClaimRoles:   goconfig.GetEnvOrDefault("AUTH_CLAIM_ROLES", "roles"),
		DefaultOrgID: goconfig.GetEnvOrDefault("AUTH_DEFAULT_ORG_ID", "default"),
		DefaultRole:  goconfig.GetEnvOrDefault("AUTH_DEFAULT_ROLE", "member"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks that required fields are set.
func (c *OIDCConfig) Validate() error {
	if c.IssuerURL == "" {
		return fmt.Errorf("AUTH_ISSUER_URL is required when auth is enabled")
	}
	if c.Audience == "" {
		return fmt.Errorf("AUTH_AUDIENCE is required when auth is enabled")
	}
	return nil
}
