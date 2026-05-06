package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/askoverride"
)

// TestAskWithOverride_FallbackRunsWhenNoOverride asserts the wrapper
// passes through to the community handler when no override is registered.
func TestAskWithOverride_FallbackRunsWhenNoOverride(t *testing.T) {
	askoverride.ResetForTest()
	defer askoverride.ResetForTest()

	called := false
	fallback := func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}

	rec := httptest.NewRecorder()
	askWithOverride(fallback)(rec, httptest.NewRequest("POST", "/api/v1/projects/p1/ask", nil))

	if !called {
		t.Fatal("fallback handler must run when no override is registered")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestAskWithOverride_RegisteredOverridePreemptsFallback asserts a
// registered override handler runs instead of the community handler.
func TestAskWithOverride_RegisteredOverridePreemptsFallback(t *testing.T) {
	askoverride.ResetForTest()
	defer askoverride.ResetForTest()

	overrideRan := false
	askoverride.Register(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		overrideRan = true
		w.WriteHeader(http.StatusTeapot)
	}))

	fallbackRan := false
	fallback := func(w http.ResponseWriter, _ *http.Request) {
		fallbackRan = true
	}

	rec := httptest.NewRecorder()
	askWithOverride(fallback)(rec, httptest.NewRequest("POST", "/api/v1/projects/p1/ask", nil))

	if !overrideRan {
		t.Fatal("override handler must run when registered")
	}
	if fallbackRan {
		t.Fatal("fallback handler must NOT run when override is registered")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("response code = %d, want %d (override's body)", rec.Code, http.StatusTeapot)
	}
}

// TestAskWithOverride_RequestPlumbing asserts the override receives the
// raw request, including method/path, so it can implement RBAC and path
// parsing the same way the community handler does.
func TestAskWithOverride_RequestPlumbing(t *testing.T) {
	askoverride.ResetForTest()
	defer askoverride.ResetForTest()

	var gotMethod, gotPath string
	askoverride.Register(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))

	// Pass an explicit fallback that fails the test if invoked. If the
	// override is set, the wrapper must never reach the fallback —
	// passing nil here would silently rely on that contract and panic
	// with a less actionable error if the wrapper changed.
	fallback := func(http.ResponseWriter, *http.Request) {
		t.Fatal("fallback handler invoked despite registered override")
	}
	rec := httptest.NewRecorder()
	askWithOverride(fallback)(rec, httptest.NewRequest("POST", "/api/v1/projects/p1/ask", nil))

	if gotMethod != "POST" || gotPath != "/api/v1/projects/p1/ask" {
		t.Fatalf("override saw (%q, %q), want (POST, /api/v1/projects/p1/ask)", gotMethod, gotPath)
	}
}
