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
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// The discovery endpoint's whole purpose is to be readable by a client that
// has no credentials, mounted inside a server whose every other route
// demands them. That is a wiring property — the order of root's sub-mux
// registrations against the auth-wrapped catch-all — and no unit test on
// the handler can show it. These tests boot the real server pipeline with
// an auth provider that refuses everything and prove the three facts the
// client depends on:
//
//  1. GET /.well-known/decisionbox answers 200 without a token,
//  2. an ordinary app route still answers 401,
//  3. a non-GET on the discovery path answers 405, not 401 — the sub-mux
//     owns the path, so the client is never told it needs a login it does
//     not need.

// rejectingAuthProvider 401s every request. It stands in for a real OIDC
// provider without needing a signing key or a token: what matters here is
// only that the auth chain refuses, so anything reachable anyway is
// reachable because of where it is mounted.
type rejectingAuthProvider struct{}

func (rejectingAuthProvider) ValidateToken(context.Context, string) (*auth.UserPrincipal, error) {
	return nil, context.Canceled
}

func (rejectingAuthProvider) Middleware() func(http.Handler) http.Handler {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			auth.WriteJSONError(w, http.StatusUnauthorized, "unauthorized")
		})
	}
}

func newDiscoveryTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	secretProvider, err := mongoSecrets.NewMongoProvider(integTestDB.Collection("secrets"), "test", "")
	if err != nil {
		t.Fatalf("secret provider: %v", err)
	}
	var _ gosecrets.Provider = secretProvider

	handler := NewWithRouteGroups(
		integTestDB,
		health.NewHandler(database.NewMongoHealthChecker(integTestDB)),
		secretProvider,
		rejectingAuthProvider{},
		nil, // schemaCollectionDropper — not exercised here
		nil, // indexCanceller — not exercised here
		nil, // validationCanceller — not exercised here
		nil, // no plugin route groups
	)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestInteg_Discovery_ReachableWithoutAuthWhileAppRoutes401(t *testing.T) {
	wellknown.ResetForTest()
	srv := newDiscoveryTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/decisionbox")
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("discovery status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read discovery body: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("discovery body is not JSON: %v (body: %s)", err, body)
	}
	if _, enveloped := doc["data"]; enveloped {
		t.Errorf("discovery body is enveloped through the full pipeline: %s", body)
	}
	authBlock, ok := doc["auth"].(map[string]any)
	if !ok || authBlock["type"] != wellknown.AuthTypeNone {
		t.Errorf("auth = %v, want an explicit type of %q", doc["auth"], wellknown.AuthTypeNone)
	}

	// The same server, an ordinary app route: the auth chain refuses. Without
	// this half, a discovery 200 could just mean auth was never wired.
	appResp, err := http.Get(srv.URL + "/api/v1/projects")
	if err != nil {
		t.Fatalf("GET app route: %v", err)
	}
	defer func() { _ = appResp.Body.Close() }()
	if appResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("app route status = %d, want 401 — auth is not actually enforced, so the discovery result proves nothing", appResp.StatusCode)
	}
}

func TestInteg_Discovery_NonGETIs405NotUnauthorized(t *testing.T) {
	wellknown.ResetForTest()
	srv := newDiscoveryTestServer(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, err := http.NewRequest(method, srv.URL+"/.well-known/decisionbox", nil)
		if err != nil {
			t.Fatalf("build %s request: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s discovery: %v", method, err)
		}
		_ = resp.Body.Close()
		// 405 means the exact-path sub-mux owns the path for every method. A
		// 401 here would mean the request fell through "/" into the auth
		// chain, telling the client it needs a login for an endpoint that
		// exists to be read without one.
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s discovery status = %d, want 405", method, resp.StatusCode)
		}
	}
}

func TestInteg_Discovery_ServesRegisteredOIDCConfigUnauthenticated(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{
		Type: wellknown.AuthTypeOIDC,
		OIDC: &wellknown.OIDC{
			Issuer:         "https://id.example/realms/decisionbox",
			MobileClientID: "decisionbox-mobile",
			Scopes:         []string{"openid", "profile", "email", "roles"},
			Audience:       "decisionbox-api",
		},
	}})
	t.Cleanup(wellknown.ResetForTest)

	srv := newDiscoveryTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/decisionbox")
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The case that actually ships: auth is enforced, and the client can
	// still read the parameters it needs to satisfy it.
	if authBlock := doc["auth"].(map[string]any); authBlock["type"] != wellknown.AuthTypeOIDC {
		t.Errorf("auth.type = %v, want %q", authBlock["type"], wellknown.AuthTypeOIDC)
	}
	oidc, ok := doc["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("oidc = %v, want the config block", doc["oidc"])
	}
	if oidc["issuer"] != "https://id.example/realms/decisionbox" {
		t.Errorf("oidc.issuer = %v", oidc["issuer"])
	}
	if oidc["mobile_client_id"] != "decisionbox-mobile" {
		t.Errorf("oidc.mobile_client_id = %v", oidc["mobile_client_id"])
	}
}
