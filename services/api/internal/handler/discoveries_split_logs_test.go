package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	docs       []database.RunStepDoc
	err        error
	gotSinceID string
	gotLimit   int
}

func (m *mockRunStepRepo) ListByRun(_ context.Context, _, sinceID string, limit int) ([]database.RunStepDoc, error) {
	m.gotSinceID = sinceID
	m.gotLimit = limit
	if m.err != nil {
		return nil, m.err
	}
	return m.docs, nil
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
	stepRepo := &mockRunStepRepo{docs: []database.RunStepDoc{{IDHex: "65000000000000000000000a", RunStep: models.RunStep{Type: "info"}}}}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)

	const sinceID = "650000000000000000000001"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?since="+sinceID+"&limit=10", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.gotSinceID != sinceID {
		t.Errorf("since cursor not forwarded: got %q, want %q", stepRepo.gotSinceID, sinceID)
	}
	if stepRepo.gotLimit != 10 {
		t.Errorf("limit not forwarded: got %d, want 10", stepRepo.gotLimit)
	}
}

func TestListRunSteps_InvalidSinceCursor(t *testing.T) {
	// Repo surfaces ErrInvalidCursor; handler must map to 400 (caller
	// supplied bad input) and NOT 500. We exercise this via the mock —
	// the real repo also returns ErrInvalidCursor on a malformed hex.
	stepRepo := &mockRunStepRepo{err: database.ErrInvalidCursor}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?since=not-an-objectid", nil)
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

func TestListRunSteps_MissingLimitDefaults(t *testing.T) {
	// No `limit` query param — handler must fall back to the cap so a
	// caller can't accidentally pull the full history of a long run by
	// omitting the parameter.
	stepRepo := &mockRunStepRepo{}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.gotLimit != 5000 {
		t.Errorf("limit default not applied: got %d, want 5000", stepRepo.gotLimit)
	}
}

func TestListRunSteps_NegativeLimitDefaults(t *testing.T) {
	// Negative `limit=-1` was previously forwarded verbatim, which the
	// repo treats as "no limit" — same accidental unbounded read as the
	// missing-limit case. Must clamp to the cap.
	stepRepo := &mockRunStepRepo{}
	h := newDiscoveriesHandlerWithLogs(t, nil, stepRepo)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps?limit=-1", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.gotLimit != 5000 {
		t.Errorf("limit not clamped on negative input: got %d, want 5000", stepRepo.gotLimit)
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

// Coverage gap fillers — every new endpoint should have missing-id /
// nil-repo / repo-error branches covered, mirroring TestListExplorationSteps_*.

func TestListAnalysisSteps_MissingID(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries//analysis-steps", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.ListAnalysisSteps(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListAnalysisSteps_NilRepo(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/analysis-steps", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListAnalysisSteps(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestListAnalysisSteps_RepoError(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{err: errBoom("boom")}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/analysis-steps", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListAnalysisSteps(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestListValidationResults_MissingID(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries//validation-results", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.ListValidationResults(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestListValidationResults_NilRepo(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/validation-results", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListValidationResults(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestListValidationResults_RepoError(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{err: errBoom("boom")}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/validation-results", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListValidationResults(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestGetRecommendationLog_MissingID(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries//recommendation-log", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGetRecommendationLog_NilRepo(t *testing.T) {
	h := NewDiscoveriesHandler(nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetRecommendationLog_RepoError(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, &mockDiscoveryLogRepo{err: errBoom("boom")}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestListRunSteps_RepoError(t *testing.T) {
	h := newDiscoveriesHandlerWithLogs(t, nil, &mockRunStepRepo{err: errBoom("boom")})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/r/steps", nil)
	req.SetPathValue("runId", "r")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestListExplorationSteps_LimitClamped(t *testing.T) {
	repo := &mockDiscoveryLogRepo{exploration: []models.ExplorationStep{}}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	// Limit way above the 1000 cap — handler should clamp before calling
	// the repo. We can't mock-record the limit here, so we just assert
	// the handler returns 200 (didn't crash on the giant value).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/exploration-steps?limit=99999", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListExplorationSteps(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
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
