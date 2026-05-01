package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// mockDiscoveryLogRepo implements database.DiscoveryLogRepo for handler-
// level unit tests. All methods return either the canned payload or the
// canned error so tests can pin the handler -> repo -> JSON shape without
// spinning up MongoDB.
type mockDiscoveryLogRepo struct {
	exploration []models.ExplorationStep
	analysis    []models.AnalysisStep
	validation  []models.ValidationLogEntry
	rec         *database.RecommendationLogEntry
	err         error
}

func (m *mockDiscoveryLogRepo) ListExplorationSteps(_ context.Context, _ string, _ int) ([]models.ExplorationStep, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.exploration, nil
}
func (m *mockDiscoveryLogRepo) ListAnalysisSteps(_ context.Context, _ string) ([]models.AnalysisStep, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.analysis, nil
}
func (m *mockDiscoveryLogRepo) ListValidationResults(_ context.Context, _ string) ([]models.ValidationLogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.validation, nil
}
func (m *mockDiscoveryLogRepo) GetRecommendationLog(_ context.Context, _ string) (*database.RecommendationLogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rec, nil
}

// mockRunStepRepo implements database.RunStepRepo for handler tests.
type mockRunStepRepo struct {
	steps    []models.RunStep
	err      error
	gotSince time.Time
	gotLimit int
}

func (m *mockRunStepRepo) ListByRun(_ context.Context, _ string, since time.Time, limit int) ([]models.RunStep, error) {
	m.gotSince = since
	m.gotLimit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.steps, nil
}

// newDiscoveriesHandlerWithLogs constructs a handler wired with the two
// new repos and stubs for everything else.
func newDiscoveriesHandlerWithLogs(t *testing.T, logRepo *mockDiscoveryLogRepo, stepRepo *mockRunStepRepo) *DiscoveriesHandler {
	t.Helper()
	return NewDiscoveriesHandler(nil, nil, nil, nil, logRepo, stepRepo, nil)
}

func TestListExplorationSteps_HappyPath(t *testing.T) {
	repo := &mockDiscoveryLogRepo{
		exploration: []models.ExplorationStep{{Step: 1}, {Step: 2}, {Step: 3}},
	}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/disc-1/exploration-steps", nil)
	req.SetPathValue("id", "disc-1")
	w := httptest.NewRecorder()

	h.ListExplorationSteps(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// writeJSON wraps the payload in {"data": ...}.
	var env struct {
		Data []models.ExplorationStep `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(env.Data) != 3 {
		t.Errorf("got %d steps, want 3", len(env.Data))
	}
}

func TestListExplorationSteps_MissingID(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries//exploration-steps", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.ListExplorationSteps(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListExplorationSteps_NilRepo(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/exploration-steps", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListExplorationSteps(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (nil repo => empty list)", w.Code)
	}
}

func TestListAnalysisSteps_HappyPath(t *testing.T) {
	repo := &mockDiscoveryLogRepo{
		analysis: []models.AnalysisStep{{AreaID: "churn"}, {AreaID: "engagement"}},
	}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/analysis-steps", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListAnalysisSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestListValidationResults_HappyPath(t *testing.T) {
	repo := &mockDiscoveryLogRepo{
		validation: []models.ValidationLogEntry{{InsightID: "i1", Status: "confirmed"}},
	}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/validation-results", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListValidationResults(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestGetRecommendationLog_HappyPath(t *testing.T) {
	repo := &mockDiscoveryLogRepo{rec: &database.RecommendationLogEntry{InsightCount: 5}}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestGetRecommendationLog_NotFound(t *testing.T) {
	repo := &mockDiscoveryLogRepo{rec: nil}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestListRunSteps_SinceCursorParsedAndForwarded(t *testing.T) {
	stepRepo := &mockRunStepRepo{steps: []models.RunStep{{Type: "info"}}}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)

	since := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?since="+since.Format(time.RFC3339Nano)+"&limit=10", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !stepRepo.gotSince.Equal(since) {
		t.Errorf("since cursor not forwarded: got %v, want %v", stepRepo.gotSince, since)
	}
	if stepRepo.gotLimit != 10 {
		t.Errorf("limit not forwarded: got %d, want 10", stepRepo.gotLimit)
	}
}

func TestListRunSteps_InvalidSince(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, nil, &mockRunStepRepo{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?since=not-a-time", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListRunSteps_LimitClamped(t *testing.T) {
	stepRepo := &mockRunStepRepo{}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?limit=999999", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.gotLimit != 5000 {
		t.Errorf("limit not clamped: got %d, want 5000", stepRepo.gotLimit)
	}
}

func TestListRunSteps_MissingRunID(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, nil, &mockRunStepRepo{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs//steps", nil)
	req.SetPathValue("runId", "")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListRunSteps_NilRepo(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (nil repo => empty list)", w.Code)
	}
}

func TestListExplorationSteps_RepoError(t *testing.T) {
	repo := &mockDiscoveryLogRepo{err: errBoom("kaboom")}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/exploration-steps", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListExplorationSteps(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// errBoom is a tiny error type for test fixtures (avoid pulling fmt.Errorf
// into a test that already has its own scope).
type errBoom string

func (e errBoom) Error() string { return string(e) }
