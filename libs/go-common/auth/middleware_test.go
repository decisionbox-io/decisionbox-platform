package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectIDAfterPrefix(t *testing.T) {
	const prefix = "/api/v1/projects/"
	tests := []struct{ path, want string }{
		{"/api/v1/projects", ""},
		{"/api/v1/projects/", ""},
		{"/api/v1/projects/abc", "abc"},
		{"/api/v1/projects/abc/insights", "abc"},
		{"/api/v1/projects/abc/sources/xyz/blob", "abc"},
		{"/api/v1/discoveries/abc", ""},
		{"/api/v1/me", ""},
	}
	for _, tt := range tests {
		if got := projectIDAfterPrefix(tt.path, prefix); got != tt.want {
			t.Errorf("projectIDAfterPrefix(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestResolvePermissionsMiddleware(t *testing.T) {
	t.Cleanup(func() { RegisterPermissionResolver(nil) })

	// Resolver that appends a permission + an effective role.
	RegisterPermissionResolver(resolverFunc(func(_ context.Context, p *UserPrincipal) ([]string, []string, error) {
		return []string{"project.view"}, append(append([]string{}, p.Roles...), "viewer"), nil
	}))

	var gotPerms []string
	var gotRoles []string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		u, _ := FromContext(r.Context())
		gotPerms = u.Permissions
		gotRoles = u.Roles
	})
	h := ResolvePermissionsMiddleware()(next)

	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(WithUser(req.Context(), &UserPrincipal{Roles: []string{"hr-analyst"}}))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(gotPerms) != 1 || gotPerms[0] != "project.view" {
		t.Errorf("permissions = %v, want [project.view]", gotPerms)
	}
	if len(gotRoles) != 2 || gotRoles[1] != "viewer" {
		t.Errorf("effective roles = %v, want [hr-analyst viewer]", gotRoles)
	}
}

type resolverFunc func(context.Context, *UserPrincipal) ([]string, []string, error)

func (f resolverFunc) Resolve(ctx context.Context, p *UserPrincipal) ([]string, []string, error) {
	return f(ctx, p)
}

func TestProjectACLMiddleware(t *testing.T) {
	loader := func(_ context.Context, id string) (string, []string, bool, error) {
		switch id {
		case "open":
			return "", nil, true, nil
		case "hr":
			return "", []string{"hr-analyst"}, true, nil
		case "ghost":
			return "", nil, false, nil
		}
		return "", nil, false, nil
	}
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := ProjectACLMiddleware("/api/v1/projects/", loader)(next)

	call := func(path string, p *UserPrincipal) (int, bool) {
		reached = false
		req := httptest.NewRequest("GET", path, nil)
		if p != nil {
			req = req.WithContext(WithUser(req.Context(), p))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, reached
	}

	tests := []struct {
		name       string
		path       string
		principal  *UserPrincipal
		wantStatus int
		wantReach  bool
	}{
		{"non-project passes", "/api/v1/me", &UserPrincipal{Roles: []string{"viewer"}}, http.StatusOK, true},
		{"collection passes", "/api/v1/projects", &UserPrincipal{Roles: []string{"viewer"}}, http.StatusOK, true},
		{"open passes", "/api/v1/projects/open/sources", &UserPrincipal{Roles: []string{"viewer"}}, http.StatusOK, true},
		{"restricted non-matching 404", "/api/v1/projects/hr/sources", &UserPrincipal{Roles: []string{"viewer"}}, http.StatusNotFound, false},
		{"restricted matching passes", "/api/v1/projects/hr/sources", &UserPrincipal{Roles: []string{"hr-analyst"}}, http.StatusOK, true},
		{"restricted admin passes", "/api/v1/projects/hr", &UserPrincipal{Roles: []string{"admin"}}, http.StatusOK, true},
		{"unknown project passes through", "/api/v1/projects/ghost/sources", &UserPrincipal{Roles: []string{"viewer"}}, http.StatusOK, true},
		{"no principal passes", "/api/v1/projects/hr", nil, http.StatusOK, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reach := call(tt.path, tt.principal)
			if status != tt.wantStatus || reach != tt.wantReach {
				t.Errorf("%s: status=%d reached=%v, want %d/%v", tt.path, status, reach, tt.wantStatus, tt.wantReach)
			}
		})
	}
}
