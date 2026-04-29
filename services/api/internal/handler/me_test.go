package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
)

func TestMe_ReturnsPrincipalFromContext(t *testing.T) {
	want := &auth.UserPrincipal{
		Sub:   "auth0|abc123",
		Email: "ops@example.com",
		OrgID: "org-9",
		Roles: []string{"owner", "admin"},
	}

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req = req.WithContext(auth.WithUser(context.Background(), want))
	w := httptest.NewRecorder()

	Me(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %q", resp.Error)
	}
	got, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Data = %T, want map", resp.Data)
	}
	if got["sub"] != want.Sub {
		t.Errorf("sub = %v, want %q", got["sub"], want.Sub)
	}
	if got["email"] != want.Email {
		t.Errorf("email = %v, want %q", got["email"], want.Email)
	}
	if got["org_id"] != want.OrgID {
		t.Errorf("org_id = %v, want %q", got["org_id"], want.OrgID)
	}
	roles, ok := got["roles"].([]interface{})
	if !ok {
		t.Fatalf("roles = %T, want []interface{}", got["roles"])
	}
	if len(roles) != 2 || roles[0] != "owner" || roles[1] != "admin" {
		t.Errorf("roles = %v, want [owner admin]", roles)
	}
}

func TestMe_ReturnsAnonymousFromNoAuthContext(t *testing.T) {
	// NoAuthProvider attaches an anonymous principal in its middleware;
	// /me should return it as-is so OSS deployments can render a generic
	// "anonymous" badge without special-casing.
	anon := &auth.UserPrincipal{
		Sub:   "anonymous",
		OrgID: "default",
		Roles: []string{"admin"},
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req = req.WithContext(auth.WithUser(context.Background(), anon))
	w := httptest.NewRecorder()

	Me(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := resp.Data.(map[string]interface{})
	if got["sub"] != "anonymous" {
		t.Errorf("sub = %v, want anonymous", got["sub"])
	}
}

func TestMe_Returns401WhenNoPrincipal(t *testing.T) {
	// Defensive branch: if /me is mounted independently of the auth
	// middleware (or behind a misconfigured chain), an unauth request
	// must not panic or leak an empty principal.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	w := httptest.NewRecorder()

	Me(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("error = %q, want unauthorized", resp.Error)
	}
}

func TestMe_Returns401WhenContextHoldsNilPrincipal(t *testing.T) {
	// Pathological case — a context that explicitly stored a nil
	// UserPrincipal pointer. FromContext returns ok=true, principal=nil.
	// Don't render an empty body; surface 401 the same as no context at
	// all.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req = req.WithContext(auth.WithUser(context.Background(), nil))
	w := httptest.NewRecorder()

	Me(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
