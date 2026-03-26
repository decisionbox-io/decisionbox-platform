package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithUserAndFromContext(t *testing.T) {
	user := &UserPrincipal{
		Sub:   "user1",
		Email: "user1@example.com",
		OrgID: "org1",
		Roles: []string{"admin"},
	}

	ctx := WithUser(context.Background(), user)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() returned false")
	}
	if got.Sub != "user1" {
		t.Errorf("Sub = %q, want %q", got.Sub, "user1")
	}
	if got.OrgID != "org1" {
		t.Errorf("OrgID = %q, want %q", got.OrgID, "org1")
	}
}

func TestFromContextMissing(t *testing.T) {
	_, ok := FromContext(context.Background())
	if ok {
		t.Error("FromContext() should return false for empty context")
	}
}

func TestNoAuthProviderValidateToken(t *testing.T) {
	provider := NewNoAuthProvider()
	user, err := provider.ValidateToken(context.Background(), "any-token")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if user.Sub != "anonymous" {
		t.Errorf("Sub = %q, want %q", user.Sub, "anonymous")
	}
	if user.OrgID != "default" {
		t.Errorf("OrgID = %q, want %q", user.OrgID, "default")
	}
}

func TestNoAuthProviderMiddleware(t *testing.T) {
	provider := NewNoAuthProvider()
	middleware := provider.Middleware()

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
		t.Errorf("user Sub = %q, want %q", capturedUser.Sub, "anonymous")
	}
}

func TestFromContextWrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), userKey, "not-a-user-principal")
	_, ok := FromContext(ctx)
	if ok {
		t.Error("FromContext() should return false for wrong type in context")
	}
}
