package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func handlerWriting(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

func TestMountRouteGroups_RoutesRequestsToPrefix(t *testing.T) {
	mux := http.NewServeMux()
	mountRouteGroups(mux, []RouteGroup{
		{Prefix: "/api/plugin-a", Handler: handlerWriting("a")},
		{Prefix: "/api/plugin-b", Handler: handlerWriting("b")},
	})

	cases := []struct {
		path string
		want string
	}{
		{"/api/plugin-a/anything", "a"},
		{"/api/plugin-a/", "a"},
		{"/api/plugin-b/x/y", "b"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Body.String() != c.want {
			t.Errorf("path %q: body = %q, want %q", c.path, rec.Body.String(), c.want)
		}
	}
}

func TestMountRouteGroups_DuplicatePrefixPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mountRouteGroups must panic on duplicate prefix so boot fails fast")
		}
	}()
	mountRouteGroups(mux, []RouteGroup{
		{Prefix: "/api/dup", Handler: handlerWriting("a")},
		{Prefix: "/api/dup", Handler: handlerWriting("b")},
	})
}

// The validation rules documented on RouteGroup are enforced by
// mountRouteGroups directly (not just apiserver.RegisterRouteGroup) so
// a caller that constructs a RouteGroup literal and passes it to
// NewWithRouteGroups cannot bypass the contract.
func TestMountRouteGroups_EmptyPrefixPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mountRouteGroups must panic on empty prefix")
		}
	}()
	mountRouteGroups(mux, []RouteGroup{{Prefix: "", Handler: handlerWriting("a")}})
}

func TestMountRouteGroups_MissingLeadingSlashPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mountRouteGroups must panic when prefix is missing leading '/'")
		}
	}()
	mountRouteGroups(mux, []RouteGroup{{Prefix: "api/foo", Handler: handlerWriting("a")}})
}

func TestMountRouteGroups_TrailingSlashPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mountRouteGroups must panic on trailing-slash prefix — the mux appends one internally")
		}
	}()
	mountRouteGroups(mux, []RouteGroup{{Prefix: "/api/foo/", Handler: handlerWriting("a")}})
}

func TestMountRouteGroups_NilHandlerPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mountRouteGroups must panic on nil handler — net/http would panic later anyway")
		}
	}()
	mountRouteGroups(mux, []RouteGroup{{Prefix: "/api/foo", Handler: nil}})
}

func TestMountRouteGroups_EmptyGroupsIsNoOp(t *testing.T) {
	mux := http.NewServeMux()
	// Must not panic, must not register anything spurious.
	mountRouteGroups(mux, nil)
	mountRouteGroups(mux, []RouteGroup{})
	req := httptest.NewRequest("GET", "/anything", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 on empty mux, got %d", rec.Code)
	}
}

func TestMountRouteGroups_HandlerSeesFullURL(t *testing.T) {
	// Plugins are documented as receiving the full URL including the
	// prefix — confirm the mux does not strip it (which would surprise
	// plugin authors who use their own sub-routers).
	var seenPath string
	captured := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
	})
	mux := http.NewServeMux()
	mountRouteGroups(mux, []RouteGroup{{Prefix: "/api/plugin", Handler: captured}})
	req := httptest.NewRequest("GET", "/api/plugin/sub/path?x=1", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if seenPath != "/api/plugin/sub/path" {
		t.Errorf("handler saw path %q, want full URL including prefix", seenPath)
	}
}
