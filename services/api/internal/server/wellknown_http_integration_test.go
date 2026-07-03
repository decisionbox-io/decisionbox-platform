//go:build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
)

// rejectingAuthProvider rejects any request that lacks a fixed test
// header, so we can prove the discovery endpoint bypasses the auth
// middleware (returns 200 without the header) while a normal app route
// behind it does not (returns 401).
type rejectingAuthProvider struct{}

func (rejectingAuthProvider) ValidateToken(_ context.Context, _ string) (*auth.UserPrincipal, error) {
	return &auth.UserPrincipal{Sub: "u", Roles: []string{"admin"}}, nil
}

func (rejectingAuthProvider) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Auth") != "ok" {
				auth.WriteJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			ctx := auth.WithUser(r.Context(), &auth.UserPrincipal{Sub: "u", Roles: []string{"admin"}})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bootDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	secretProvider, err := mongoSecrets.NewMongoProvider(routeGroupsTestDB.Collection("secrets"), "test", "")
	if err != nil {
		t.Fatalf("secret provider: %v", err)
	}
	healthHandler := health.NewHandler(database.NewMongoHealthChecker(routeGroupsTestDB))
	handler := NewWithRouteGroups(
		routeGroupsTestDB, healthHandler, secretProvider, rejectingAuthProvider{},
		nil, nil, nil, nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestInteg_WellKnown_UnauthenticatedAndRaw(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	srv := bootDiscoveryServer(t)

	// Discovery is reachable with NO auth header, even though the auth
	// provider rejects unauthenticated requests — it is mounted on the
	// pre-auth root mux.
	resp, err := http.Get(srv.URL + "/.well-known/decisionbox")
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status = %d, want 200 (should bypass auth)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("decode discovery body: %v (%s)", err, body)
	}
	if _, wrapped := top["data"]; wrapped {
		t.Error("discovery response is enveloped in {data}, want raw")
	}
	var authBlock struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(top["auth"], &authBlock)
	if authBlock.Type != "none" {
		t.Errorf("auth.type = %q, want none (nothing registered)", authBlock.Type)
	}

	// A normal app route behind the auth middleware IS rejected without
	// the header — proving discovery's 200 above is because it sits
	// outside the auth chain, not because auth is disabled.
	meResp, err := http.Get(srv.URL + "/api/v1/me")
	if err != nil {
		t.Fatalf("GET /me: %v", err)
	}
	defer func() { _ = meResp.Body.Close() }()
	if meResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/v1/me without auth = %d, want 401 (auth chain active)", meResp.StatusCode)
	}
}

func TestInteg_WellKnown_MethodNotAllowed(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	srv := bootDiscoveryServer(t)

	req, _ := http.NewRequest("POST", srv.URL+"/.well-known/decisionbox", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST discovery = %d, want 405", resp.StatusCode)
	}
}

func TestInteg_WellKnown_AdvertisesRegisteredOIDC(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)
	wellknown.Register(wellknown.Config{
		Auth: wellknown.Auth{
			Type: wellknown.AuthTypeOIDC,
			OIDC: &wellknown.OIDC{
				Issuer:         "https://issuer.example.com",
				MobileClientID: "mob-1",
				Scopes:         []string{"openid", "email"},
				Audience:       "https://api.example.com",
			},
		},
	})

	srv := bootDiscoveryServer(t)
	resp, err := http.Get(srv.URL + "/.well-known/decisionbox")
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var doc struct {
		Auth struct {
			Type string `json:"type"`
		} `json:"auth"`
		OIDC *struct {
			Issuer         string `json:"issuer"`
			MobileClientID string `json:"mobile_client_id"`
		} `json:"oidc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Auth.Type != "oidc" {
		t.Errorf("auth.type = %q, want oidc", doc.Auth.Type)
	}
	if doc.OIDC == nil || doc.OIDC.Issuer != "https://issuer.example.com" || doc.OIDC.MobileClientID != "mob-1" {
		t.Errorf("oidc block = %+v, want issuer+mobile client id from registry", doc.OIDC)
	}
}
