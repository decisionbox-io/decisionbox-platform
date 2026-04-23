package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// mockDebugLogRepo is a table-driven stub for DebugLogRepo used in handler
// tests. It records the last call arguments so tests can assert on how the
// handler forwards `since` and `limit` from the query string.
type mockDebugLogRepo struct {
	entries []models.DebugLogEntry
	err     error
	lastRun string
	lastSince time.Time
	lastLimit int
	calls   int
}

func (m *mockDebugLogRepo) ListByRun(_ context.Context, runID string, since time.Time, limit int) ([]models.DebugLogEntry, error) {
	m.calls++
	m.lastRun = runID
	m.lastSince = since
	m.lastLimit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.entries, nil
}

func newDebugLogsRequest(runID, query string) (*http.Request, *httptest.ResponseRecorder) {
	url := "/api/v1/runs/" + runID + "/debug-logs"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("runId", runID)
	return req, httptest.NewRecorder()
}

func TestDiscoveriesHandler_GetDebugLogs_EmptyRepo(t *testing.T) {
	// Handler must not panic when no debug log repo is configured. It should
	// return 200 with an empty array so the UI can render a "no logs yet"
	// state instead of an error.
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil)
	req, w := newDebugLogsRequest("run-1", "")

	h.GetDebugLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := decodeDebugLogsBody(t, w)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(got))
	}
}

// decodeDebugLogsBody peels the APIResponse envelope `{"data": [...]}`
// back to the raw slice. Responses from the handler layer always use this
// envelope — writeJSON adds it.
func decodeDebugLogsBody(t *testing.T, w *httptest.ResponseRecorder) []models.DebugLogEntry {
	t.Helper()
	var env struct {
		Data []models.DebugLogEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return env.Data
}

func TestDiscoveriesHandler_GetDebugLogs_MissingRunID(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, &mockDebugLogRepo{}, nil)
	req := httptest.NewRequest("GET", "/api/v1/runs//debug-logs", nil)
	req.SetPathValue("runId", "")
	w := httptest.NewRecorder()

	h.GetDebugLogs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDiscoveriesHandler_GetDebugLogs_InvalidSince(t *testing.T) {
	repo := &mockDebugLogRepo{}
	h := NewDiscoveriesHandler(nil, nil, nil, repo, nil)
	req, w := newDebugLogsRequest("run-1", "since=not-a-date")

	h.GetDebugLogs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if repo.calls != 0 {
		t.Errorf("repo should not be called on bad input, calls = %d", repo.calls)
	}
}

func TestDiscoveriesHandler_GetDebugLogs_ForwardsQueryParams(t *testing.T) {
	// Verifies the handler is a thin passthrough: it parses `since` + `limit`
	// from the query string and hands them to the repo unchanged. This is
	// what lets the UI poll idempotently by passing the last-seen timestamp.
	since := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	entry := models.DebugLogEntry{
		ID:             "log-1",
		DiscoveryRunID: "run-abc",
		CreatedAt:      since.Add(5 * time.Second),
		Operation:      "execute_query",
		Success:        true,
		SQLQuery:       "SELECT 1",
	}
	repo := &mockDebugLogRepo{entries: []models.DebugLogEntry{entry}}
	h := NewDiscoveriesHandler(nil, nil, nil, repo, nil)

	req, w := newDebugLogsRequest("run-abc", "since="+since.Format(time.RFC3339Nano)+"&limit=50")
	h.GetDebugLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if repo.lastRun != "run-abc" {
		t.Errorf("runID forwarded = %q, want %q", repo.lastRun, "run-abc")
	}
	if !repo.lastSince.Equal(since) {
		t.Errorf("since forwarded = %v, want %v", repo.lastSince, since)
	}
	if repo.lastLimit != 50 {
		t.Errorf("limit forwarded = %d, want 50", repo.lastLimit)
	}

	got := decodeDebugLogsBody(t, w)
	if len(got) != 1 || got[0].ID != "log-1" {
		t.Errorf("unexpected body: %+v", got)
	}
}

func TestDiscoveriesHandler_GetDebugLogs_CapsLimit(t *testing.T) {
	// The endpoint caps `limit` at 1000 to prevent a single request from
	// pulling the entire collection. Any larger value is silently clamped.
	repo := &mockDebugLogRepo{}
	h := NewDiscoveriesHandler(nil, nil, nil, repo, nil)

	req, w := newDebugLogsRequest("run-1", "limit=99999")
	h.GetDebugLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if repo.lastLimit != 1000 {
		t.Errorf("limit after cap = %d, want 1000", repo.lastLimit)
	}
}
