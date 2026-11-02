package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// gotAttempt is which attempt the handler asked for, and calls counts
	// how many times it asked at all — an unknown run must not reach the
	// step repository.
	gotAttempt int
	calls      int
}

func (m *mockRunStepRepo) ListByRun(_ context.Context, _, sinceID string, limit, attempt int) ([]database.RunStepDoc, error) {
	m.gotSinceID = sinceID
	m.gotLimit = limit
	m.gotAttempt = attempt
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.docs, nil
}

// newDiscoveriesHandlerWithLogs constructs a handler wired with the two
// new repos and stubs for everything else.
//
// The run repository is real-ish rather than nil because ListRunSteps now
// reads the run to learn which attempt's feed to serve. defaultRun answers
// for whatever run ID the caller invents, on attempt 1.
func newDiscoveriesHandlerWithLogs(t *testing.T, logRepo *mockDiscoveryLogRepo, stepRepo *mockRunStepRepo) *DiscoveriesHandler {
	t.Helper()
	return NewDiscoveriesHandler(nil, nil, runRepoOnAttempt(1), nil, logRepo, stepRepo, nil)
}

// runRepoOnAttempt is a run repository that answers every lookup with a run
// on the given attempt.
func runRepoOnAttempt(attempt int) *mockRunRepo {
	m := newMockRunRepo()
	m.defaultRun = &models.DiscoveryRun{Attempt: attempt}
	return m
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

func TestListValidationResults_WarehouseIDFlowsThroughResponse(t *testing.T) {
	// #161 regression: the agent persists warehouse_id on each validation
	// doc (ValidationResult.WarehouseID) so multi-warehouse runs can attribute
	// each verified insight to the datasource it was checked against. The API's
	// ValidationLogEntry has to mirror that tag or the dashboard drops the
	// datasource badge on every validation-log row.
	repo := &mockDiscoveryLogRepo{
		validation: []models.ValidationLogEntry{
			{InsightID: "i1", Status: "confirmed", WarehouseID: "wh_oracle"},
		},
	}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/validation-results", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.ListValidationResults(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// writeJSON wraps the slice in {"data": [...]} — decode through that
	// wrapper so the assertion reflects the wire shape the dashboard sees.
	var wrapper struct {
		Data []models.ValidationLogEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(wrapper.Data) != 1 {
		t.Fatalf("entries = %d, want 1 (body: %s)", len(wrapper.Data), w.Body.String())
	}
	if got := wrapper.Data[0].WarehouseID; got != "wh_oracle" {
		t.Errorf("WarehouseID = %q, want %q (body: %s)", got, "wh_oracle", w.Body.String())
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

func TestGetRecommendationLog_DroppedCountersFlowThroughResponse(t *testing.T) {
	// Issue #237 regression: the agent persists per-reason drop counts
	// on RecommendationStep, and the API's RecommendationLogEntry has
	// to mirror them or dashboard consumers cannot see how many recs
	// were dropped due to invalid related_insight_ids.
	repo := &mockDiscoveryLogRepo{rec: &database.RecommendationLogEntry{
		InsightCount:                     5,
		RecommendationsDropped:           6,
		RecommendationsDroppedParse:      2,
		RecommendationsDroppedMissingIDs: 1,
		RecommendationsDroppedUnknownID:  3,
		RecommendationParseRetries:       1,
		Status:                           "recommendation_parse_error",
	}}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// writeJSON wraps the entry in {"data": {...}} — decode through
	// that wrapper so the field-level assertions reflect the wire shape
	// dashboards actually see.
	var wrapper struct {
		Data database.RecommendationLogEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	got := wrapper.Data
	if got.RecommendationsDropped != 6 {
		t.Errorf("RecommendationsDropped = %d, want 6 (body: %s)", got.RecommendationsDropped, w.Body.String())
	}
	if got.RecommendationsDroppedParse != 2 {
		t.Errorf("RecommendationsDroppedParse = %d, want 2", got.RecommendationsDroppedParse)
	}
	if got.RecommendationsDroppedMissingIDs != 1 {
		t.Errorf("RecommendationsDroppedMissingIDs = %d, want 1", got.RecommendationsDroppedMissingIDs)
	}
	if got.RecommendationsDroppedUnknownID != 3 {
		t.Errorf("RecommendationsDroppedUnknownID = %d, want 3", got.RecommendationsDroppedUnknownID)
	}
	if got.RecommendationParseRetries != 1 {
		t.Errorf("RecommendationParseRetries = %d, want 1", got.RecommendationParseRetries)
	}
	if got.Status != "recommendation_parse_error" {
		t.Errorf("Status = %q, want recommendation_parse_error", got.Status)
	}
}

func TestGetRecommendationLog_CleanRunOmitsDropCounters(t *testing.T) {
	// On the happy path the per-reason fields must not appear in the
	// JSON payload — keeps the wire shape stable for clients that do
	// not expect them.
	repo := &mockDiscoveryLogRepo{rec: &database.RecommendationLogEntry{InsightCount: 5}}
	h := newDiscoveriesHandlerWithLogs(t, repo, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/discoveries/d/recommendation-log", nil)
	req.SetPathValue("id", "d")
	w := httptest.NewRecorder()
	h.GetRecommendationLog(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, banned := range []string{
		`"recommendations_dropped"`,
		`"recommendations_dropped_parse"`,
		`"recommendations_dropped_missing_ids"`,
		`"recommendations_dropped_unknown_id"`,
		`"recommendation_parse_retries"`,
		`"status"`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("clean run leaked %s into payload: %s", banned, body)
		}
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

// TestListRunSteps_ServesOnlyTheRunsCurrentAttempt is why the endpoint reads
// the run at all.
//
// A resume starts a new attempt while the superseded agent may still be
// mid-LLM-call, and that agent keeps writing live-feed rows until its next
// ownership gate. Those rows carry its own (older) attempt. If the endpoint
// served them, the operator would watch a resumed run's log interleave two
// attempts — and because the `_id > since_id` cursor assumes one writer per
// run, the live attempt's own rows could be dropped from the stream for
// good. So the handler asks the run which attempt it is on and forwards it.
func TestListRunSteps_ServesOnlyTheRunsCurrentAttempt(t *testing.T) {
	stepRepo := &mockRunStepRepo{}
	h := NewDiscoveriesHandler(nil, nil, runRepoOnAttempt(5), nil, nil, stepRepo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/steps", nil)
	req.SetPathValue("runId", "run-1")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.gotAttempt != 5 {
		t.Errorf("asked the step repo for attempt %d, want 5 — serving another attempt's rows is the bug this closes", stepRepo.gotAttempt)
	}
}

// TestListRunSteps_UnknownRunStaysAnEmptyList pins the contract for a run ID
// that resolves to nothing. The dashboard polls this endpoint on a timer with
// IDs it was handed, so a stale poll returns an empty feed rather than
// becoming an error — and it must not reach the step repository, because
// without a run there is no attempt to scope the query to.
func TestListRunSteps_UnknownRunStaysAnEmptyList(t *testing.T) {
	stepRepo := &mockRunStepRepo{docs: []database.RunStepDoc{{RunID: "run-1"}}}
	h := NewDiscoveriesHandler(nil, nil, newMockRunRepo(), nil, nil, stepRepo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/ghost/steps", nil)
	req.SetPathValue("runId", "ghost")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if stepRepo.calls != 0 {
		t.Errorf("step repo was called %d time(s) for a run that does not exist; an unscoped query is exactly what this endpoint must not issue", stepRepo.calls)
	}
	var env struct {
		Data []database.RunStepDoc `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(env.Data) != 0 {
		t.Errorf("got %d steps for an unknown run, want 0", len(env.Data))
	}
}

// TestListRunSteps_RunLookupError surfaces a failed run read rather than
// quietly serving an unscoped feed.
func TestListRunSteps_RunLookupError(t *testing.T) {
	runRepo := newMockRunRepo()
	runRepo.getErr = errBoom("mongo down")
	stepRepo := &mockRunStepRepo{}
	h := NewDiscoveriesHandler(nil, nil, runRepo, nil, nil, stepRepo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/steps", nil)
	req.SetPathValue("runId", "run-1")
	w := httptest.NewRecorder()
	h.ListRunSteps(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if stepRepo.calls != 0 {
		t.Errorf("step repo was called %d time(s) after the run read failed; the attempt was unknown", stepRepo.calls)
	}
}
