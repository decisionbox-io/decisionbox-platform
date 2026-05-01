//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestDiscoveryLogRepository_RoundTrip exercises every Save method against a
// real MongoDB testcontainer and confirms the paired List/Get readers
// rehydrate the same payload. The previous embedded-array design hit the
// 16MB BSON limit on long runs; this test pins the per-step persistence
// path that fixes it.
func TestDiscoveryLogRepository_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewDiscoveryLogRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	const (
		projectID   = "proj-rt"
		discoveryID = "disc-rt"
		runID       = "run-rt"
	)

	// Exploration steps — ascending step numbers persist + read in order.
	steps := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", LLMRequest: "p1", LLMResponse: "r1"},
		{Step: 2, Action: "lookup_schema", LLMRequest: "p2", LLMResponse: "r2"},
		{Step: 3, Action: "query_data", Query: "SELECT 2", LLMRequest: "p3", LLMResponse: "r3"},
	}
	if err := repo.SaveExplorationSteps(ctx, projectID, discoveryID, runID, steps); err != nil {
		t.Fatalf("SaveExplorationSteps: %v", err)
	}
	gotSteps, err := repo.ListExplorationStepsByDiscovery(ctx, discoveryID, 0)
	if err != nil {
		t.Fatalf("ListExplorationStepsByDiscovery: %v", err)
	}
	if len(gotSteps) != 3 {
		t.Fatalf("got %d exploration steps, want 3", len(gotSteps))
	}
	for i, s := range gotSteps {
		if s.Step != steps[i].Step {
			t.Errorf("ordering broke at index %d: got step=%d, want %d", i, s.Step, steps[i].Step)
		}
		if s.LLMRequest != steps[i].LLMRequest {
			t.Errorf("LLMRequest round-trip mismatch at step %d", s.Step)
		}
	}

	// Analysis steps — multiple areas, one row each.
	now := time.Now().UTC().Truncate(time.Millisecond)
	areas := []models.AnalysisStep{
		{AreaID: "churn", AreaName: "Churn", RunAt: now, Prompt: "p", Response: "r"},
		{AreaID: "engagement", AreaName: "Engagement", RunAt: now.Add(time.Second), Prompt: "p2", Response: "r2"},
	}
	if err := repo.SaveAnalysisSteps(ctx, projectID, discoveryID, runID, areas); err != nil {
		t.Fatalf("SaveAnalysisSteps: %v", err)
	}
	gotAreas, err := repo.ListAnalysisStepsByDiscovery(ctx, discoveryID)
	if err != nil {
		t.Fatalf("ListAnalysisStepsByDiscovery: %v", err)
	}
	if len(gotAreas) != 2 {
		t.Fatalf("got %d analysis steps, want 2", len(gotAreas))
	}
	if gotAreas[0].AreaID != "churn" || gotAreas[1].AreaID != "engagement" {
		t.Errorf("analysis ordering broke: got %q,%q", gotAreas[0].AreaID, gotAreas[1].AreaID)
	}

	// Validation results — multiple rows.
	vals := []models.ValidationResult{
		{InsightID: "i1", AnalysisArea: "churn", Status: "confirmed", VerifiedCount: 100, ValidatedAt: now},
		{InsightID: "i2", AnalysisArea: "engagement", Status: "rejected", VerifiedCount: 0, ValidatedAt: now.Add(time.Second)},
	}
	if err := repo.SaveValidationResults(ctx, projectID, discoveryID, runID, vals); err != nil {
		t.Fatalf("SaveValidationResults: %v", err)
	}
	gotVals, err := repo.ListValidationResultsByDiscovery(ctx, discoveryID)
	if err != nil {
		t.Fatalf("ListValidationResultsByDiscovery: %v", err)
	}
	if len(gotVals) != 2 {
		t.Fatalf("got %d validations, want 2", len(gotVals))
	}

	// Recommendation log — single row per discovery.
	rec := &models.RecommendationStep{RunAt: now, Prompt: "p", Response: "r", InsightCount: 5}
	if err := repo.SaveRecommendationLog(ctx, projectID, discoveryID, runID, rec); err != nil {
		t.Fatalf("SaveRecommendationLog: %v", err)
	}
	gotRec, err := repo.GetRecommendationLogByDiscovery(ctx, discoveryID)
	if err != nil {
		t.Fatalf("GetRecommendationLogByDiscovery: %v", err)
	}
	if gotRec == nil || gotRec.InsightCount != 5 {
		t.Errorf("recommendation log mis-fetched: %+v", gotRec)
	}
}

// TestDiscoveryLogRepository_EmptyInputs is the no-op contract — every Save
// method is a no-op for empty inputs and never errors. The orchestrator
// relies on this to keep zero-step runs cheap.
func TestDiscoveryLogRepository_EmptyInputs(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewDiscoveryLogRepository(db)
	if err := repo.SaveExplorationSteps(ctx, "p", "d", "r", nil); err != nil {
		t.Errorf("nil exploration: %v", err)
	}
	if err := repo.SaveExplorationSteps(ctx, "p", "d", "r", []models.ExplorationStep{}); err != nil {
		t.Errorf("empty exploration: %v", err)
	}
	if err := repo.SaveAnalysisSteps(ctx, "p", "d", "r", nil); err != nil {
		t.Errorf("nil analysis: %v", err)
	}
	if err := repo.SaveValidationResults(ctx, "p", "d", "r", nil); err != nil {
		t.Errorf("nil validation: %v", err)
	}
	if err := repo.SaveRecommendationLog(ctx, "p", "d", "r", nil); err != nil {
		t.Errorf("nil recommendation: %v", err)
	}

	// Readers on a discovery with no rows return empty (not nil-error) for
	// the list shape and nil for the singular shape.
	if got, err := repo.ListExplorationStepsByDiscovery(ctx, "missing", 0); err != nil || len(got) != 0 {
		t.Errorf("list missing exploration: got=%v err=%v", got, err)
	}
	if got, err := repo.GetRecommendationLogByDiscovery(ctx, "missing"); err != nil || got != nil {
		t.Errorf("get missing recommendation: got=%v err=%v", got, err)
	}
}

// TestDiscoveryLogRepository_LimitAndIsolation pins the per-discovery
// scoping: rows for one discovery never leak into reads for another, even
// when both share project_id and run_id.
func TestDiscoveryLogRepository_LimitAndIsolation(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewDiscoveryLogRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	stepsA := []models.ExplorationStep{{Step: 1, Action: "query_data", Query: "A1"}, {Step: 2, Action: "query_data", Query: "A2"}}
	stepsB := []models.ExplorationStep{{Step: 1, Action: "query_data", Query: "B1"}}
	if err := repo.SaveExplorationSteps(ctx, "shared-proj", "disc-a", "run", stepsA); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if err := repo.SaveExplorationSteps(ctx, "shared-proj", "disc-b", "run", stepsB); err != nil {
		t.Fatalf("save B: %v", err)
	}

	gotA, _ := repo.ListExplorationStepsByDiscovery(ctx, "disc-a", 0)
	if len(gotA) != 2 {
		t.Errorf("disc-a: got %d, want 2 (cross-discovery leak?)", len(gotA))
	}
	gotB, _ := repo.ListExplorationStepsByDiscovery(ctx, "disc-b", 0)
	if len(gotB) != 1 {
		t.Errorf("disc-b: got %d, want 1", len(gotB))
	}

	// limit honoured.
	gotALimited, _ := repo.ListExplorationStepsByDiscovery(ctx, "disc-a", 1)
	if len(gotALimited) != 1 {
		t.Errorf("limit=1: got %d, want 1", len(gotALimited))
	}
}
