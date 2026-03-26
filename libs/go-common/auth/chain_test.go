package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChainProvider_NoAuthMode(t *testing.T) {
	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	user, err := chain.ValidateToken(context.Background(), "anything")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "anonymous" {
		t.Errorf("Sub = %q, want %q", user.Sub, "anonymous")
	}
	if user.Roles[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", user.Roles)
	}
}

func TestChainProvider_NoAuthMode_Middleware(t *testing.T) {
	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	middleware := chain.Middleware()
	var capturedUser *UserPrincipal
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if capturedUser == nil {
		t.Fatal("user not set in context")
	}
	if capturedUser.Sub != "anonymous" {
		t.Errorf("Sub = %q, want %q", capturedUser.Sub, "anonymous")
	}
}

func TestChainProvider_AuthEnabled_JWTRoutes(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: true,
		OIDCConfig: &OIDCConfig{
			IssuerURL:    issuer,
			Audience:     "test-api",
			ClaimSub:     "sub",
			ClaimEmail:   "email",
			ClaimOrgID:   "org_id",
			ClaimRoles:   "roles",
			DefaultOrgID: "default",
			DefaultRole:  "member",
		},
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":   "chain-user",
		"roles": []any{"admin"},
	}, time.Now().Add(time.Hour))

	user, err := chain.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "chain-user" {
		t.Errorf("Sub = %q, want %q", user.Sub, "chain-user")
	}
}

func TestChainProvider_AuthEnabled_NoToken(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: true,
		OIDCConfig: &OIDCConfig{
			IssuerURL:    issuer,
			Audience:     "test-api",
			ClaimSub:     "sub",
			ClaimEmail:   "email",
			ClaimOrgID:   "org_id",
			ClaimRoles:   "roles",
			DefaultOrgID: "default",
			DefaultRole:  "member",
		},
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	middleware := chain.Middleware()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestChainProvider_AuthEnabled_APIKeyPrefix(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: true,
		OIDCConfig: &OIDCConfig{
			IssuerURL:    issuer,
			Audience:     "test-api",
			ClaimSub:     "sub",
			ClaimEmail:   "email",
			ClaimOrgID:   "org_id",
			ClaimRoles:   "roles",
			DefaultOrgID: "default",
			DefaultRole:  "member",
		},
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	_, err = chain.ValidateToken(context.Background(), "dbx_someapikey123")
	if err == nil {
		t.Fatal("ValidateToken() should return error for API key tokens (not yet supported)")
	}
}

func TestChainProvider_AuthEnabled_UnsupportedTokenFormat(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	chain, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: true,
		OIDCConfig: &OIDCConfig{
			IssuerURL:    issuer,
			Audience:     "test-api",
			ClaimSub:     "sub",
			ClaimEmail:   "email",
			ClaimOrgID:   "org_id",
			ClaimRoles:   "roles",
			DefaultOrgID: "default",
			DefaultRole:  "member",
		},
	})
	if err != nil {
		t.Fatalf("NewChainProvider() error = %v", err)
	}

	_, err = chain.ValidateToken(context.Background(), "random-garbage")
	if err == nil {
		t.Fatal("ValidateToken() should return error for unsupported token format")
	}
}

func TestChainProvider_AuthEnabled_MissingOIDCConfig(t *testing.T) {
	_, err := NewChainProvider(context.Background(), ChainConfig{
		AuthEnabled: true,
		OIDCConfig:  nil,
	})
	if err == nil {
		t.Fatal("NewChainProvider() should return error when auth enabled but no OIDC config")
	}
}
