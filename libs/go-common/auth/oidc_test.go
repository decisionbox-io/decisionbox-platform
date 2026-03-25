package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testOIDCServer creates a mock OIDC discovery + JWKS server and returns the issuer URL, a token signer, and the server.
func testOIDCServer(t *testing.T) (issuerURL string, signer jose.Signer, key *rsa.PrivateKey, srv *httptest.Server) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		disc := map[string]any{
			"issuer":                 srv.URL,
			"jwks_uri":              srv.URL + "/.well-known/jwks.json",
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":        srv.URL + "/oauth/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(disc)
	})

	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{
					Key:       &privateKey.PublicKey,
					KeyID:     "test-key-1",
					Algorithm: string(jose.RS256),
					Use:       "sig",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "test-key-1"),
	)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	return srv.URL, sig, privateKey, srv
}

// signToken creates a signed JWT with the given claims.
func signToken(t *testing.T, signer jose.Signer, issuer, audience string, claims map[string]any, expiry time.Time) string {
	t.Helper()

	now := time.Now()
	stdClaims := jwt.Claims{
		Issuer:   issuer,
		Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(expiry),
	}

	builder := jwt.Signed(signer).Claims(stdClaims).Claims(claims)
	token, err := builder.Serialize()
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return token
}

// newTestOIDCProvider creates an OIDCProvider backed by the mock server.
func newTestOIDCProvider(t *testing.T, issuerURL, audience string, cfg OIDCConfig) *OIDCProvider {
	t.Helper()

	cfg.IssuerURL = issuerURL
	cfg.Audience = audience

	// Set defaults if not specified
	if cfg.ClaimSub == "" {
		cfg.ClaimSub = "sub"
	}
	if cfg.ClaimEmail == "" {
		cfg.ClaimEmail = "email"
	}
	if cfg.ClaimOrgID == "" {
		cfg.ClaimOrgID = "org_id"
	}
	if cfg.ClaimRoles == "" {
		cfg.ClaimRoles = "roles"
	}
	if cfg.DefaultOrgID == "" {
		cfg.DefaultOrgID = "default"
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "member"
	}

	provider, err := NewOIDCProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewOIDCProvider() error = %v", err)
	}
	return provider
}

// --- Happy path tests ---

func TestOIDCProvider_ValidateToken_AllClaims(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":    "user-123",
		"email":  "user@example.com",
		"org_id": "acme",
		"roles":  []any{"admin"},
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "user-123" {
		t.Errorf("Sub = %q, want %q", user.Sub, "user-123")
	}
	if user.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", user.Email, "user@example.com")
	}
	if user.OrgID != "acme" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "acme")
	}
	if len(user.Roles) != 1 || user.Roles[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", user.Roles)
	}
}

func TestOIDCProvider_ValidateToken_CustomClaimNames(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{
		ClaimSub:   "subject",
		ClaimEmail: "mail",
		ClaimOrgID: "tenant_id",
		ClaimRoles: "groups",
	})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"subject":   "user-456",
		"mail":      "user@corp.com",
		"tenant_id": "beta",
		"groups":    []any{"member"},
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "user-456" {
		t.Errorf("Sub = %q, want %q", user.Sub, "user-456")
	}
	if user.Email != "user@corp.com" {
		t.Errorf("Email = %q, want %q", user.Email, "user@corp.com")
	}
	if user.OrgID != "beta" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "beta")
	}
	if len(user.Roles) != 1 || user.Roles[0] != "member" {
		t.Errorf("Roles = %v, want [member]", user.Roles)
	}
}

func TestOIDCProvider_ValidateToken_SingleRoleString(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":   "user-789",
		"roles": "admin",
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if len(user.Roles) != 1 || user.Roles[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", user.Roles)
	}
}

func TestOIDCProvider_ValidateToken_MultipleRoles(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":   "user-multi",
		"roles": []any{"viewer", "member"},
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if len(user.Roles) != 2 {
		t.Fatalf("Roles length = %d, want 2", len(user.Roles))
	}
	if user.Roles[0] != "viewer" || user.Roles[1] != "member" {
		t.Errorf("Roles = %v, want [viewer member]", user.Roles)
	}
}

func TestOIDCProvider_Middleware_InjectsUser(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})
	middleware := provider.Middleware()

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":    "mw-user",
		"email":  "mw@example.com",
		"org_id": "mw-org",
		"roles":  []any{"admin"},
	}, time.Now().Add(time.Hour))

	var capturedUser *UserPrincipal
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUser, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if capturedUser == nil {
		t.Fatal("user not set in context")
	}
	if capturedUser.Sub != "mw-user" {
		t.Errorf("Sub = %q, want %q", capturedUser.Sub, "mw-user")
	}
}

// --- Unhappy path tests ---

func TestOIDCProvider_ValidateToken_ExpiredToken(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub": "expired-user",
	}, time.Now().Add(-time.Hour))

	_, err := provider.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for expired token")
	}
}

func TestOIDCProvider_ValidateToken_WrongAudience(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "wrong-audience", map[string]any{
		"sub": "wrong-aud-user",
	}, time.Now().Add(time.Hour))

	_, err := provider.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for wrong audience")
	}
}

func TestOIDCProvider_ValidateToken_WrongIssuer(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, "https://evil-issuer.com", "test-api", map[string]any{
		"sub": "wrong-iss-user",
	}, time.Now().Add(time.Hour))

	_, err := provider.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for wrong issuer")
	}
}

func TestOIDCProvider_ValidateToken_MalformedToken(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	_, err := provider.ValidateToken(context.Background(), "not-a-valid-jwt")
	if err == nil {
		t.Fatal("ValidateToken() should return error for malformed token")
	}
}

func TestOIDCProvider_ValidateToken_EmptyToken(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	_, err := provider.ValidateToken(context.Background(), "")
	if err == nil {
		t.Fatal("ValidateToken() should return error for empty token")
	}
}

func TestOIDCProvider_ValidateToken_InvalidSignature(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	// Sign with a different key than what the JWKS server advertises
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate other RSA key: %v", err)
	}
	otherSigner, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: otherKey},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "test-key-1"),
	)
	if err != nil {
		t.Fatalf("failed to create other signer: %v", err)
	}

	token := signToken(t, otherSigner, issuer, "test-api", map[string]any{
		"sub": "tampered-user",
	}, time.Now().Add(time.Hour))

	_, err = provider.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("ValidateToken() should return error for invalid signature")
	}
}

func TestOIDCProvider_ValidateToken_MissingSub(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"email": "no-sub@example.com",
	}, time.Now().Add(time.Hour))

	_, err := provider.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("ValidateToken() should return error when sub claim is missing")
	}
}

func TestOIDCProvider_Middleware_EmptyAuthHeader(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})
	middleware := provider.Middleware()

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

func TestOIDCProvider_Middleware_BearerWithNoToken(t *testing.T) {
	issuer, _, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})
	middleware := provider.Middleware()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// --- Default/fallback behavior tests ---

func TestOIDCProvider_ValidateToken_MissingOrgID_UsesDefault(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{
		DefaultOrgID: "fallback-org",
	})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":   "no-org-user",
		"roles": []any{"member"},
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.OrgID != "fallback-org" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "fallback-org")
	}
}

func TestOIDCProvider_ValidateToken_MissingRoles_UsesDefault(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{
		DefaultRole: "viewer",
	})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub": "no-roles-user",
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if len(user.Roles) != 1 || user.Roles[0] != "viewer" {
		t.Errorf("Roles = %v, want [viewer]", user.Roles)
	}
}

func TestOIDCProvider_ValidateToken_MissingEmail_EmptyString(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub":   "no-email-user",
		"roles": []any{"admin"},
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Email != "" {
		t.Errorf("Email = %q, want empty string", user.Email)
	}
}

func TestOIDCProvider_ValidateToken_BothDefaults(t *testing.T) {
	issuer, signer, _, srv := testOIDCServer(t)
	defer srv.Close()

	provider := newTestOIDCProvider(t, issuer, "test-api", OIDCConfig{
		DefaultOrgID: "default-org",
		DefaultRole:  "default-role",
	})

	token := signToken(t, signer, issuer, "test-api", map[string]any{
		"sub": "minimal-user",
	}, time.Now().Add(time.Hour))

	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.OrgID != "default-org" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "default-org")
	}
	if len(user.Roles) != 1 || user.Roles[0] != "default-role" {
		t.Errorf("Roles = %v, want [default-role]", user.Roles)
	}
}

// --- extractBearerToken tests ---

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"valid bearer", "Bearer eyJtoken", "eyJtoken"},
		{"case insensitive", "bearer eyJtoken", "eyJtoken"},
		{"mixed case", "BEARER eyJtoken", "eyJtoken"},
		{"empty header", "", ""},
		{"no bearer prefix", "Basic dXNlcjpwYXNz", ""},
		{"bearer no token", "Bearer ", ""},
		{"just bearer", "Bearer", ""},
		{"token with spaces trimmed", "Bearer  eyJtoken ", "eyJtoken"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			got := extractBearerToken(req)
			if got != tt.want {
				t.Errorf("extractBearerToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- extractRoles edge cases ---

func TestOIDCProvider_extractRoles_EmptyStringRole(t *testing.T) {
	p := &OIDCProvider{config: OIDCConfig{ClaimRoles: "roles"}}

	claims := map[string]any{"roles": ""}
	roles := p.extractRoles(claims)
	if len(roles) != 0 {
		t.Errorf("extractRoles() = %v, want empty", roles)
	}
}

func TestOIDCProvider_extractRoles_ArrayWithEmptyStrings(t *testing.T) {
	p := &OIDCProvider{config: OIDCConfig{ClaimRoles: "roles"}}

	claims := map[string]any{"roles": []any{"admin", "", "member"}}
	roles := p.extractRoles(claims)
	if len(roles) != 2 {
		t.Fatalf("extractRoles() length = %d, want 2", len(roles))
	}
	if roles[0] != "admin" || roles[1] != "member" {
		t.Errorf("extractRoles() = %v, want [admin member]", roles)
	}
}

func TestOIDCProvider_extractRoles_UnsupportedType(t *testing.T) {
	p := &OIDCProvider{config: OIDCConfig{ClaimRoles: "roles"}}

	claims := map[string]any{"roles": 42}
	roles := p.extractRoles(claims)
	if len(roles) != 0 {
		t.Errorf("extractRoles() = %v, want empty", roles)
	}
}

func TestOIDCProvider_extractRoles_MissingClaim(t *testing.T) {
	p := &OIDCProvider{config: OIDCConfig{ClaimRoles: "roles"}}

	claims := map[string]any{"sub": "user1"}
	roles := p.extractRoles(claims)
	if len(roles) != 0 {
		t.Errorf("extractRoles() = %v, want empty", roles)
	}
}

// --- NewOIDCProvider error cases ---

func TestNewOIDCProvider_InvalidIssuer(t *testing.T) {
	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		IssuerURL: "https://invalid-issuer-that-does-not-exist.example.com",
		Audience:  "test-api",
	})
	if err == nil {
		t.Fatal("NewOIDCProvider() should return error for invalid issuer")
	}
}

// --- Helpers ---

// Ensure the test helper coverage: verify signToken produces valid JWTs
func TestSignToken_ProducesValidJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		nil,
	)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	token := signToken(t, sig, "https://issuer.example.com", "test-api", map[string]any{
		"sub": "test-user",
	}, time.Now().Add(time.Hour))

	if token == "" {
		t.Fatal("signToken() returned empty string")
	}

	// Verify it's parseable
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatalf("failed to parse token: %v", err)
	}

	var claims map[string]any
	err = parsed.Claims(key.Public(), &claims)
	if err != nil {
		t.Fatalf("failed to verify claims: %v", err)
	}

	sub, _ := claims["sub"].(string)
	if sub != "test-user" {
		t.Errorf("sub = %q, want %q", sub, "test-user")
	}
}

