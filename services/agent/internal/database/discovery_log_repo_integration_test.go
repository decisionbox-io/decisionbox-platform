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

// TestDiscoveryLogRepository_FixHistoryRoundTrip pins the BSON round-trip
// for the per-attempt SQL-fix history field on ExplorationStep: every
// scalar, the ordered slice itself, and the empty-history case all
// rehydrate to the same shape they were saved with. This is the
// persistence guarantee downstream tooling depends on — a marshal that
// silently dropped a field on read would surface as "no fix history" in
// the dashboard / exports despite the agent recording it.
func TestDiscoveryLogRepository_FixHistoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewDiscoveryLogRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	const (
		projectID   = "proj-fix"
		discoveryID = "disc-fix"
		runID       = "run-fix"
	)

	// One step that needed two fix attempts before succeeding (mirrors a
	// real exploration in which attempt 0 was still wrong, attempt 1
	// landed). Plus a step with no fix history at all to verify omitempty
	// behaviour.
	steps := []models.ExplorationStep{
		{
			Step:        7,
			Action:      "query_data",
			Query:       "SELECT n FROM t WHERE app_id = 'x'",
			LLMRequest:  "p",
			LLMResponse: "r",
			FixAttempts: 2,
			Fixed:       true,
			FixHistory: []models.FixAttempt{
				{
					Step:         7,
					Attempt:      0,
					PromptIn:     "[system]\nfix prompt v1\n[user]\nfix this",
					ResponseOut:  "```sql\nSELECT bad2 FROM t WHERE app_id = 'x'\n```",
					SQLBefore:    "SELECT BAD FROM t WHERE app_id = 'x'",
					SQLAfter:     "SELECT bad2 FROM t WHERE app_id = 'x'",
					ErrorIn:      "Unknown column 'BAD'",
					InputTokens:  120,
					OutputTokens: 60,
					DurationMs:   840,
					Timestamp:    now,
				},
				{
					Step:         7,
					Attempt:      1,
					PromptIn:     "[system]\nfix prompt v2\n[user]\nfix this",
					ResponseOut:  "```sql\nSELECT n FROM t WHERE app_id = 'x'\n```",
					SQLBefore:    "SELECT bad2 FROM t WHERE app_id = 'x'",
					SQLAfter:     "SELECT n FROM t WHERE app_id = 'x'",
					ErrorIn:      "Unknown column 'bad2'",
					InputTokens:  130,
					OutputTokens: 55,
					DurationMs:   910,
					Timestamp:    now.Add(time.Second),
				},
				{
					// Failed-fixer attempt: LLM ran to max_tokens and
					// response could not be parsed into SQL. FixerError
					// is set, SQLAfter is empty. Pins the BSON round-
					// trip for the FixerError field so the negative-
					// example rows downstream tooling cares about are
					// preserved through Mongo.
					Step:         7,
					Attempt:      2,
					PromptIn:     "[system]\nfix prompt v3\n[user]\nfix this",
					ResponseOut:  "<truncated nonsense>",
					SQLBefore:    "SELECT n FROM t WHERE app_id = 'x'",
					SQLAfter:     "",
					ErrorIn:      "extracted SQL parse error",
					FixerError:   "failed to extract fixed SQL: empty response",
					InputTokens:  140,
					OutputTokens: 4000,
					DurationMs:   112000,
					Timestamp:    now.Add(2 * time.Second),
				},
			},
		},
		// Step that ran cleanly first time — FixHistory must be empty
		// after round-trip (the `omitempty` BSON tag means it may be
		// absent from the document, but the decoded slice must remain
		// non-failing and zero-length).
		{
			Step:        8,
			Action:      "query_data",
			Query:       "SELECT 1",
			LLMRequest:  "p",
			LLMResponse: "r",
		},
	}
	if err := repo.SaveExplorationSteps(ctx, projectID, discoveryID, runID, steps); err != nil {
		t.Fatalf("SaveExplorationSteps: %v", err)
	}

	got, err := repo.ListExplorationStepsByDiscovery(ctx, discoveryID, 0)
	if err != nil {
		t.Fatalf("ListExplorationStepsByDiscovery: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d steps, want 2", len(got))
	}

	// First step: full FixHistory round-trip.
	first := got[0]
	if first.Step != 7 {
		t.Fatalf("ordering broke: first.Step = %d, want 7", first.Step)
	}
	if first.FixAttempts != 2 {
		t.Errorf("FixAttempts = %d, want 2", first.FixAttempts)
	}
	if !first.Fixed {
		t.Errorf("Fixed = false, want true")
	}
	if len(first.FixHistory) != 3 {
		t.Fatalf("FixHistory entries = %d, want 3 — slice ordering / encoding broke", len(first.FixHistory))
	}
	for i, entry := range first.FixHistory {
		want := steps[0].FixHistory[i]
		if entry.Step != want.Step || entry.Attempt != want.Attempt {
			t.Errorf("entry %d Step/Attempt: got (%d, %d), want (%d, %d)", i, entry.Step, entry.Attempt, want.Step, want.Attempt)
		}
		if entry.PromptIn != want.PromptIn || entry.ResponseOut != want.ResponseOut {
			t.Errorf("entry %d prompt/response round-trip broke", i)
		}
		if entry.SQLBefore != want.SQLBefore || entry.SQLAfter != want.SQLAfter {
			t.Errorf("entry %d SQL before/after round-trip broke: got (%q -> %q), want (%q -> %q)", i, entry.SQLBefore, entry.SQLAfter, want.SQLBefore, want.SQLAfter)
		}
		if entry.ErrorIn != want.ErrorIn {
			t.Errorf("entry %d ErrorIn = %q, want %q", i, entry.ErrorIn, want.ErrorIn)
		}
		if entry.FixerError != want.FixerError {
			t.Errorf("entry %d FixerError = %q, want %q", i, entry.FixerError, want.FixerError)
		}
		if entry.InputTokens != want.InputTokens || entry.OutputTokens != want.OutputTokens {
			t.Errorf("entry %d token counts: got (in=%d, out=%d), want (in=%d, out=%d)", i, entry.InputTokens, entry.OutputTokens, want.InputTokens, want.OutputTokens)
		}
		if entry.DurationMs != want.DurationMs {
			t.Errorf("entry %d DurationMs = %d, want %d", i, entry.DurationMs, want.DurationMs)
		}
		if !entry.Timestamp.Equal(want.Timestamp) {
			t.Errorf("entry %d Timestamp = %v, want %v", i, entry.Timestamp, want.Timestamp)
		}
	}

	// Second step: no FixHistory recorded — decoded slice must be
	// nil-or-empty, never a non-nil placeholder with junk values.
	second := got[1]
	if second.Step != 8 {
		t.Fatalf("second.Step = %d, want 8", second.Step)
	}
	if second.FixAttempts != 0 {
		t.Errorf("FixAttempts = %d, want 0", second.FixAttempts)
	}
	if len(second.FixHistory) != 0 {
		t.Errorf("FixHistory should be empty for a step that ran cleanly, got %d entries", len(second.FixHistory))
	}
}
