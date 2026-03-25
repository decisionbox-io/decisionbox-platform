//go:build integration_auth

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// keycloakContainer holds the running Keycloak testcontainer and its config.
type keycloakContainer struct {
	container testcontainers.Container
	issuerURL string
	audience  string
}

// Keycloak realm import JSON — pre-configured with:
// - Realm: "decisionbox-test"
// - Client: "decisionbox-api" (public, direct access grants enabled)
// - Roles: viewer, member, admin
// - Users: viewer-user (viewer), member-user (member), admin-user (admin)
const keycloakRealmJSON = `{
  "realm": "decisionbox-test",
  "enabled": true,
  "roles": {
    "realm": [
      {"name": "viewer"},
      {"name": "member"},
      {"name": "admin"}
    ]
  },
  "clients": [
    {
      "clientId": "decisionbox-api",
      "enabled": true,
      "publicClient": true,
      "directAccessGrantsEnabled": true,
      "standardFlowEnabled": false,
      "protocolMappers": [
        {
          "name": "audience-mapper",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-audience-mapper",
          "config": {
            "included.client.audience": "decisionbox-api",
            "id.token.claim": "true",
            "access.token.claim": "true"
          }
        },
        {
          "name": "realm-roles",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-usermodel-realm-role-mapper",
          "config": {
            "claim.name": "roles",
            "jsonType.label": "String",
            "multivalued": "true",
            "id.token.claim": "true",
            "access.token.claim": "true",
            "userinfo.token.claim": "true"
          }
        },
        {
          "name": "org-id",
          "protocol": "openid-connect",
          "protocolMapper": "oidc-hardcoded-claim-mapper",
          "config": {
            "claim.name": "org_id",
            "claim.value": "test-org",
            "jsonType.label": "String",
            "id.token.claim": "true",
            "access.token.claim": "true",
            "userinfo.token.claim": "true"
          }
        }
      ]
    }
  ],
  "users": [
    {
      "username": "viewer-user",
      "firstName": "Viewer",
      "lastName": "User",
      "email": "viewer@test.com",
      "emailVerified": true,
      "enabled": true,
      "requiredActions": [],
      "credentials": [{"type": "password", "value": "test", "temporary": false}],
      "realmRoles": ["viewer"]
    },
    {
      "username": "member-user",
      "firstName": "Member",
      "lastName": "User",
      "email": "member@test.com",
      "emailVerified": true,
      "enabled": true,
      "requiredActions": [],
      "credentials": [{"type": "password", "value": "test", "temporary": false}],
      "realmRoles": ["member"]
    },
    {
      "username": "admin-user",
      "firstName": "Admin",
      "lastName": "User",
      "email": "admin@test.com",
      "emailVerified": true,
      "enabled": true,
      "requiredActions": [],
      "credentials": [{"type": "password", "value": "test", "temporary": false}],
      "realmRoles": ["admin"]
    }
  ]
}`


// getToken uses Keycloak's direct access grants (Resource Owner Password Grant)
// to obtain an access token for a user.
func getToken(t *testing.T, kc *keycloakContainer, username, password string) string {
	t.Helper()

	tokenURL := kc.issuerURL + "/protocol/openid-connect/token"
	data := url.Values{
		"grant_type": {"password"},
		"client_id":  {kc.audience},
		"username":   {username},
		"password":   {password},
		"scope":      {"openid"},
	}

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("token request status %d: %s", resp.StatusCode, body)
	}

	var tokenResp map[string]any
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}

	token, ok := tokenResp["access_token"].(string)
	if !ok || token == "" {
		t.Fatalf("no access_token in response: %v", tokenResp)
	}
	return token
}

// Shared Keycloak container — started once in TestMain, reused by all tests.
var sharedKC *keycloakContainer

func TestMain(m *testing.M) {
	ctx := context.Background()

	kc := startKeycloakForMain()
	if kc == nil {
		fmt.Fprintln(os.Stderr, "Failed to start Keycloak container")
		os.Exit(1)
	}
	sharedKC = kc
	defer kc.container.Terminate(ctx)

	os.Exit(m.Run())
}

// startKeycloakForMain is like startKeycloak but doesn't need *testing.T.
func startKeycloakForMain() *keycloakContainer {
	ctx := context.Background()

	realmFile, err := os.CreateTemp("", "keycloak-realm-*.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create realm file: %v\n", err)
		return nil
	}
	defer os.Remove(realmFile.Name())
	if _, err := realmFile.WriteString(keycloakRealmJSON); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write realm file: %v\n", err)
		return nil
	}
	realmFile.Close()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "quay.io/keycloak/keycloak:26.2",
			ExposedPorts: []string{"8080/tcp"},
			Cmd:          []string{"start-dev", "--import-realm"},
			Env: map[string]string{
				"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
				"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin",
			},
			Files: []testcontainers.ContainerFile{
				{
					HostFilePath:      realmFile.Name(),
					ContainerFilePath: "/opt/keycloak/data/import/realm.json",
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForHTTP("/realms/decisionbox-test/.well-known/openid-configuration").
				WithPort("8080/tcp").
				WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start Keycloak: %v\n", err)
		return nil
	}

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "8080")
	issuerURL := fmt.Sprintf("http://%s:%s/realms/decisionbox-test", host, port.Port())

	return &keycloakContainer{
		container: container,
		issuerURL: issuerURL,
		audience:  "decisionbox-api",
	}
}

// newTestProvider creates an OIDCProvider connected to the shared Keycloak.
func newTestProvider(t *testing.T) *OIDCProvider {
	t.Helper()
	provider, err := NewOIDCProvider(context.Background(), OIDCConfig{
		IssuerURL:    sharedKC.issuerURL,
		Audience:     sharedKC.audience,
		ClaimSub:     "sub",
		ClaimEmail:   "email",
		ClaimOrgID:   "org_id",
		ClaimRoles:   "roles",
		DefaultOrgID: "default",
		DefaultRole:  "member",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider() error = %v", err)
	}
	return provider
}

// --- Integration Tests ---

func TestIntegAuth_OIDCProvider_ValidToken(t *testing.T) {
	provider := newTestProvider(t)

	token := getToken(t, sharedKC, "admin-user", "test")
	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}

	if user.Sub == "" {
		t.Error("Sub should not be empty")
	}
	if user.OrgID != "test-org" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "test-org")
	}

	hasAdmin := false
	for _, r := range user.Roles {
		if r == "admin" {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Errorf("Roles = %v, want to contain 'admin'", user.Roles)
	}
}

func TestIntegAuth_OIDCProvider_RoleExtraction(t *testing.T) {
	provider := newTestProvider(t)

	tests := []struct {
		username string
		wantRole string
	}{
		{"viewer-user", "viewer"},
		{"member-user", "member"},
		{"admin-user", "admin"},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			token := getToken(t, sharedKC, tt.username, "test")
			user, err := provider.ValidateToken(context.Background(), token)
			if err != nil {
				t.Fatalf("ValidateToken() error = %v", err)
			}

			hasRole := false
			for _, r := range user.Roles {
				if r == tt.wantRole {
					hasRole = true
				}
			}
			if !hasRole {
				t.Errorf("Roles = %v, want to contain %q", user.Roles, tt.wantRole)
			}
		})
	}
}

func TestIntegAuth_OIDCProvider_ExpiredToken(t *testing.T) {
	provider := newTestProvider(t)

	// Use a garbage token that looks like a JWT
	_, err := provider.ValidateToken(context.Background(), "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJmYWtlIn0.invalid")
	if err == nil {
		t.Fatal("ValidateToken() should return error for invalid token")
	}
}

func TestIntegAuth_RBAC_WithRealTokens(t *testing.T) {
	provider := newTestProvider(t)
	authMiddleware := provider.Middleware()

	tests := []struct {
		name       string
		username   string
		minRole    string
		wantStatus int
	}{
		{"viewer accessing viewer route", "viewer-user", "viewer", 200},
		{"viewer accessing member route", "viewer-user", "member", 403},
		{"viewer accessing admin route", "viewer-user", "admin", 403},
		{"member accessing viewer route", "member-user", "viewer", 200},
		{"member accessing member route", "member-user", "member", 200},
		{"member accessing admin route", "member-user", "admin", 403},
		{"admin accessing viewer route", "admin-user", "viewer", 200},
		{"admin accessing member route", "admin-user", "member", 200},
		{"admin accessing admin route", "admin-user", "admin", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := getToken(t, sharedKC, tt.username, "test")

			rbac := RequireRole(tt.minRole)
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			handler := authMiddleware(rbac(inner))

			req := httptest.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestIntegAuth_OrgIsolation(t *testing.T) {
	provider := newTestProvider(t)

	// All test users have org_id="test-org" from the hardcoded claim mapper
	token := getToken(t, sharedKC, "viewer-user", "test")
	user, err := provider.ValidateToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}

	if user.OrgID != "test-org" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "test-org")
	}
}

func TestIntegAuth_NoAuthHeader(t *testing.T) {
	provider := newTestProvider(t)
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

	// Verify JSON error response
	var errResp map[string]string
	json.NewDecoder(w.Body).Decode(&errResp)
	if errResp["error"] == "" {
		t.Error("expected JSON error response")
	}
}

func TestIntegAuth_ChainProvider_NoAuthMode(t *testing.T) {
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
	if capturedUser == nil || capturedUser.Sub != "anonymous" {
		t.Errorf("NoAuth mode should inject anonymous user, got %v", capturedUser)
	}
}

