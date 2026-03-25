package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func rbacHandler(t *testing.T, minRole string, user *UserPrincipal) *httptest.ResponseRecorder {
	t.Helper()

	middleware := RequireRole(minRole)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)

	if user != nil {
		ctx := WithUser(req.Context(), user)
		req = req.WithContext(ctx)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// --- Happy path: access granted ---

func TestRequireRole_ViewerAccessesViewerRoute(t *testing.T) {
	w := rbacHandler(t, "viewer", &UserPrincipal{Sub: "u1", Roles: []string{"viewer"}})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireRole_MemberAccessesViewerRoute(t *testing.T) {
	w := rbacHandler(t, "viewer", &UserPrincipal{Sub: "u2", Roles: []string{"member"}})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireRole_AdminAccessesViewerRoute(t *testing.T) {
	w := rbacHandler(t, "viewer", &UserPrincipal{Sub: "u3", Roles: []string{"admin"}})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireRole_AdminAccessesAdminRoute(t *testing.T) {
	w := rbacHandler(t, "admin", &UserPrincipal{Sub: "u4", Roles: []string{"admin"}})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestRequireRole_MemberAccessesMemberRoute(t *testing.T) {
	w := rbacHandler(t, "member", &UserPrincipal{Sub: "u5", Roles: []string{"member"}})
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// --- Unhappy path: access denied ---

func TestRequireRole_ViewerAccessesMemberRoute(t *testing.T) {
	w := rbacHandler(t, "member", &UserPrincipal{Sub: "u6", Roles: []string{"viewer"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestRequireRole_ViewerAccessesAdminRoute(t *testing.T) {
	w := rbacHandler(t, "admin", &UserPrincipal{Sub: "u7", Roles: []string{"viewer"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestRequireRole_MemberAccessesAdminRoute(t *testing.T) {
	w := rbacHandler(t, "admin", &UserPrincipal{Sub: "u8", Roles: []string{"member"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestRequireRole_NoUserInContext(t *testing.T) {
	w := rbacHandler(t, "viewer", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireRole_EmptyRoles(t *testing.T) {
	w := rbacHandler(t, "viewer", &UserPrincipal{Sub: "u9", Roles: []string{}})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestRequireRole_UnknownRole(t *testing.T) {
	w := rbacHandler(t, "viewer", &UserPrincipal{Sub: "u10", Roles: []string{"superuser"}})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// --- hasMinRole unit tests ---

func TestHasMinRole(t *testing.T) {
	tests := []struct {
		name     string
		roles    []string
		minLevel int
		want     bool
	}{
		{"admin meets admin level", []string{"admin"}, 3, true},
		{"admin meets viewer level", []string{"admin"}, 1, true},
		{"viewer does not meet admin level", []string{"viewer"}, 3, false},
		{"multiple roles highest wins", []string{"viewer", "admin"}, 3, true},
		{"empty roles", []string{}, 1, false},
		{"nil roles", nil, 1, false},
		{"unknown role ignored", []string{"unknown"}, 1, false},
		{"zero min level always passes", []string{"viewer"}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasMinRole(tt.roles, tt.minLevel)
			if got != tt.want {
				t.Errorf("hasMinRole(%v, %d) = %v, want %v", tt.roles, tt.minLevel, got, tt.want)
			}
		})
	}
}

// --- RequireRole with unknown min role ---

func TestRequireRole_UnknownMinRole(t *testing.T) {
	// Unknown min role defaults to level 0, so any known role passes
	middleware := RequireRole("nonexistent")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := WithUser(req.Context(), &UserPrincipal{Sub: "u11", Roles: []string{"viewer"}})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (unknown min role defaults to level 0)", w.Code)
	}
}

// --- Verify context is preserved through middleware ---

func TestRequireRole_PreservesContext(t *testing.T) {
	type ctxKey string
	middleware := RequireRole("viewer")

	var gotValue string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotValue, _ = r.Context().Value(ctxKey("test")).(string)
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware(inner)
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := WithUser(req.Context(), &UserPrincipal{Sub: "u12", Roles: []string{"admin"}})
	ctx = context.WithValue(ctx, ctxKey("test"), "preserved")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotValue != "preserved" {
		t.Errorf("context value = %q, want %q", gotValue, "preserved")
	}
}
