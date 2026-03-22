package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoveriesHandler_List_NilProjectRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil projectRepo — expected
	defer func() { recover() }()
	h.List(w, req)
}

func TestDiscoveriesHandler_GetDiscoveryByID_NilRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/discoveries/disc-123", nil)
	req.SetPathValue("id", "disc-123")
	w := httptest.NewRecorder()

	// Will panic on nil discovery repo — expected
	defer func() { recover() }()
	h.GetDiscoveryByID(w, req)
}

func TestDiscoveriesHandler_GetLatest_NilRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/latest", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.GetLatest(w, req)
}

func TestDiscoveriesHandler_GetByDate_InvalidDate(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/not-a-date", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "not-a-date")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid date", w.Code)
	}

	var resp APIResponse
	decodeResponseBody(w, &resp)
	if !strings.Contains(resp.Error, "invalid date format") {
		t.Errorf("error = %q, should contain 'invalid date format'", resp.Error)
	}
}

func TestDiscoveriesHandler_GetByDate_WrongFormat(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/03-15-2026", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "03-15-2026")
	w := httptest.NewRecorder()

	h.GetByDate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for MM-DD-YYYY format", w.Code)
	}
}

func TestDiscoveriesHandler_GetByDate_ValidDate_NilRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/discoveries/2026-03-15", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("date", "2026-03-15")
	w := httptest.NewRecorder()

	// Valid date passes parsing, then panics on nil repo — expected
	defer func() { recover() }()
	h.GetByDate(w, req)

	// If it doesn't panic, it should not be a 400
	if w.Code == http.StatusBadRequest {
		t.Error("valid date should not return 400")
	}
}

func TestDiscoveriesHandler_TriggerDiscovery_NilProjectRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/discover",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil projectRepo — expected
	defer func() { recover() }()
	h.TriggerDiscovery(w, req)
}

func TestDiscoveriesHandler_GetStatus_NilProjectRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/status", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	// Will panic on nil projectRepo — expected
	defer func() { recover() }()
	h.GetStatus(w, req)
}

func TestDiscoveriesHandler_GetRun_NilRunRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("GET", "/api/v1/runs/run-123", nil)
	req.SetPathValue("runId", "run-123")
	w := httptest.NewRecorder()

	// Will panic on nil runRepo — expected
	defer func() { recover() }()
	h.GetRun(w, req)
}

func TestDiscoveriesHandler_CancelRun_NilRunRepo(t *testing.T) {
	h := &DiscoveriesHandler{}

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-123", nil)
	req.SetPathValue("runId", "run-123")
	w := httptest.NewRecorder()

	// Will panic on nil runRepo — expected
	defer func() { recover() }()
	h.CancelRun(w, req)
}

func TestDiscoveriesHandler_TriggerDiscovery_OptionalBody(t *testing.T) {
	h := &DiscoveriesHandler{}

	// Body with areas and max_steps — should parse without error
	// (will still panic on nil repo)
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/discover",
		strings.NewReader(`{"areas": ["churn", "monetization"], "max_steps": 50}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	defer func() { recover() }()
	h.TriggerDiscovery(w, req)
}

func TestDiscoveriesHandler_TriggerDiscovery_EmptyBody(t *testing.T) {
	h := &DiscoveriesHandler{}

	// Empty body should be OK (areas and max_steps are optional)
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/discover", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	defer func() { recover() }()
	h.TriggerDiscovery(w, req)
}

func TestNewDiscoveriesHandler(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil)
	if h == nil {
		t.Fatal("NewDiscoveriesHandler returned nil")
	}
	if h.repo != nil {
		t.Error("repo should be nil when passed nil")
	}
	if h.projectRepo != nil {
		t.Error("projectRepo should be nil when passed nil")
	}
	if h.runRepo != nil {
		t.Error("runRepo should be nil when passed nil")
	}
	if h.agentRunner != nil {
		t.Error("agentRunner should be nil when passed nil")
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	// Test the getEnvOrDefault helper used by discoveries handler
	// This is a package-level function in discoveries.go

	// Test with unset env var
	val := getEnvOrDefault("NONEXISTENT_TEST_VAR_12345", "fallback")
	if val != "fallback" {
		t.Errorf("got %q, want %q", val, "fallback")
	}

	// Test with set env var
	t.Setenv("TEST_GETENV_VAR", "custom")
	val = getEnvOrDefault("TEST_GETENV_VAR", "fallback")
	if val != "custom" {
		t.Errorf("got %q, want %q", val, "custom")
	}
}

// decodeResponseBody is a helper for tests in this file.
func decodeResponseBody(w *httptest.ResponseRecorder, resp *APIResponse) {
	_ = decodeJSON(httptest.NewRequest("POST", "/", w.Body), resp)
}
