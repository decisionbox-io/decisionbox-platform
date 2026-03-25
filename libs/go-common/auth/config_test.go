package auth

import (
	"testing"
)

func TestLoadOIDCConfigFromEnv_AllValues(t *testing.T) {
	t.Setenv("AUTH_ISSUER_URL", "https://example.auth0.com")
	t.Setenv("AUTH_AUDIENCE", "my-api")
	t.Setenv("AUTH_CLAIM_SUB", "subject")
	t.Setenv("AUTH_CLAIM_EMAIL", "mail")
	t.Setenv("AUTH_CLAIM_ORG_ID", "tenant_id")
	t.Setenv("AUTH_CLAIM_ROLES", "groups")
	t.Setenv("AUTH_DEFAULT_ORG_ID", "acme")
	t.Setenv("AUTH_DEFAULT_ROLE", "viewer")

	cfg, err := LoadOIDCConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadOIDCConfigFromEnv() error = %v", err)
	}

	if cfg.IssuerURL != "https://example.auth0.com" {
		t.Errorf("IssuerURL = %q, want %q", cfg.IssuerURL, "https://example.auth0.com")
	}
	if cfg.Audience != "my-api" {
		t.Errorf("Audience = %q, want %q", cfg.Audience, "my-api")
	}
	if cfg.ClaimSub != "subject" {
		t.Errorf("ClaimSub = %q, want %q", cfg.ClaimSub, "subject")
	}
	if cfg.ClaimEmail != "mail" {
		t.Errorf("ClaimEmail = %q, want %q", cfg.ClaimEmail, "mail")
	}
	if cfg.ClaimOrgID != "tenant_id" {
		t.Errorf("ClaimOrgID = %q, want %q", cfg.ClaimOrgID, "tenant_id")
	}
	if cfg.ClaimRoles != "groups" {
		t.Errorf("ClaimRoles = %q, want %q", cfg.ClaimRoles, "groups")
	}
	if cfg.DefaultOrgID != "acme" {
		t.Errorf("DefaultOrgID = %q, want %q", cfg.DefaultOrgID, "acme")
	}
	if cfg.DefaultRole != "viewer" {
		t.Errorf("DefaultRole = %q, want %q", cfg.DefaultRole, "viewer")
	}
}

func TestLoadOIDCConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("AUTH_ISSUER_URL", "https://example.auth0.com")
	t.Setenv("AUTH_AUDIENCE", "my-api")

	cfg, err := LoadOIDCConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadOIDCConfigFromEnv() error = %v", err)
	}

	if cfg.ClaimSub != "sub" {
		t.Errorf("ClaimSub = %q, want default %q", cfg.ClaimSub, "sub")
	}
	if cfg.ClaimEmail != "email" {
		t.Errorf("ClaimEmail = %q, want default %q", cfg.ClaimEmail, "email")
	}
	if cfg.ClaimOrgID != "org_id" {
		t.Errorf("ClaimOrgID = %q, want default %q", cfg.ClaimOrgID, "org_id")
	}
	if cfg.ClaimRoles != "roles" {
		t.Errorf("ClaimRoles = %q, want default %q", cfg.ClaimRoles, "roles")
	}
	if cfg.DefaultOrgID != "default" {
		t.Errorf("DefaultOrgID = %q, want default %q", cfg.DefaultOrgID, "default")
	}
	if cfg.DefaultRole != "member" {
		t.Errorf("DefaultRole = %q, want default %q", cfg.DefaultRole, "member")
	}
}

func TestLoadOIDCConfigFromEnv_MissingIssuerURL(t *testing.T) {
	t.Setenv("AUTH_AUDIENCE", "my-api")

	_, err := LoadOIDCConfigFromEnv()
	if err == nil {
		t.Fatal("LoadOIDCConfigFromEnv() should return error when AUTH_ISSUER_URL is missing")
	}
}

func TestLoadOIDCConfigFromEnv_MissingAudience(t *testing.T) {
	t.Setenv("AUTH_ISSUER_URL", "https://example.auth0.com")

	_, err := LoadOIDCConfigFromEnv()
	if err == nil {
		t.Fatal("LoadOIDCConfigFromEnv() should return error when AUTH_AUDIENCE is missing")
	}
}

func TestOIDCConfig_Validate_Valid(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://example.auth0.com",
		Audience:  "my-api",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestOIDCConfig_Validate_EmptyIssuerURL(t *testing.T) {
	cfg := &OIDCConfig{
		Audience: "my-api",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should return error for empty IssuerURL")
	}
}

func TestOIDCConfig_Validate_EmptyAudience(t *testing.T) {
	cfg := &OIDCConfig{
		IssuerURL: "https://example.auth0.com",
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() should return error for empty Audience")
	}
}
