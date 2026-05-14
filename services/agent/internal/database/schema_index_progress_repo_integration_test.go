//go:build integration

package database

import (
	"context"
	"sync"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestAgentInteg_SchemaIndexProgress_WriteCycle(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)
	projectID := "proj-agent-1"

	if err := r.Reset(ctx, projectID, "run-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if err := r.SetTotals(ctx, projectID, 40); err != nil {
		t.Fatalf("SetTotals: %v", err)
	}
	if err := r.SetPhase(ctx, projectID, models.SchemaIndexPhaseDescribingTables); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := r.IncrementDone(ctx, projectID, 1); err != nil {
			t.Fatalf("IncrementDone: %v", err)
		}
	}

	// Pull it back out via the raw collection — mirrors how the API side reads.
	var got models.SchemaIndexProgress
	if err := db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got); err != nil {
		t.Fatalf("raw find: %v", err)
	}
	if got.Phase != "describing_tables" {
		t.Errorf("Phase = %q", got.Phase)
	}
	if got.TablesTotal != 40 {
		t.Errorf("TablesTotal = %d", got.TablesTotal)
	}
	if got.TablesDone != 5 {
		t.Errorf("TablesDone = %d", got.TablesDone)
	}
}

func TestAgentInteg_SchemaIndexProgress_ValidationPaths(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)

	// Empty projectID on each entrypoint.
	if err := r.Reset(ctx, "", "run"); err == nil {
		t.Error("Reset with empty projectID should error")
	}
	if err := r.SetPhase(ctx, "", "embedding"); err == nil {
		t.Error("SetPhase with empty projectID should error")
	}
	if err := r.SetTotals(ctx, "", 10); err == nil {
		t.Error("SetTotals with empty projectID should error")
	}
	if err := r.IncrementDone(ctx, "", 1); err == nil {
		t.Error("IncrementDone with empty projectID should error")
	}
	if err := r.RecordError(ctx, "", "x"); err == nil {
		t.Error("RecordError with empty projectID should error")
	}

	// Invalid phase.
	projectID := "proj-agent-validate"
	_ = r.Reset(ctx, projectID, "r")
	if err := r.SetPhase(ctx, projectID, "garbage"); err == nil {
		t.Error("invalid phase should error")
	}

	// Negative total.
	if err := r.SetTotals(ctx, projectID, -1); err == nil {
		t.Error("negative total should error")
	}

	// Non-positive delta: no-op, not an error.
	if err := r.IncrementDone(ctx, projectID, 0); err != nil {
		t.Errorf("zero delta should be no-op, got %v", err)
	}
	if err := r.IncrementDone(ctx, projectID, -3); err != nil {
		t.Errorf("negative delta should be no-op, got %v", err)
	}
}

func TestAgentInteg_SchemaIndexProgress_ConcurrentIncrement(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)
	projectID := "proj-agent-concurrent"
	_ = r.Reset(ctx, projectID, "r")
	_ = r.SetTotals(ctx, projectID, 64)

	// 8 workers × 8 tables each = 64 — mirrors the default BLURB_WORKERS=8.
	const workers, perWorker = 8, 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				_ = r.IncrementDone(ctx, projectID, 1)
			}
		}()
	}
	wg.Wait()

	var got models.SchemaIndexProgress
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.TablesDone != workers*perWorker {
		t.Errorf("TablesDone = %d, want %d (atomic $inc lost writes?)", got.TablesDone, workers*perWorker)
	}
}

func TestAgentInteg_SchemaIndexProgress_NoDocPropagatesError(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)

	// No Reset — every write path requires a matching doc.
	if err := r.SetPhase(ctx, "ghost", "embedding"); err == nil {
		t.Error("SetPhase without Reset should error")
	}
	if err := r.SetTotals(ctx, "ghost", 10); err == nil {
		t.Error("SetTotals without Reset should error")
	}
	if err := r.IncrementDone(ctx, "ghost", 1); err == nil {
		t.Error("IncrementDone without Reset should error")
	}
	if err := r.RecordError(ctx, "ghost", "x"); err == nil {
		t.Error("RecordError without Reset should error")
	}
}

// TestAgentInteg_SchemaIndexProgress_IncrementTokens — Reset() must zero
// token totals; IncrementTokens must sum atomically; the values must
// round-trip back through the typed model.
func TestAgentInteg_SchemaIndexProgress_IncrementTokens(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)
	projectID := "proj-agent-tokens"
	if err := r.Reset(ctx, projectID, "run-tokens-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// First increment.
	if err := r.IncrementTokens(ctx, projectID, 1500, 400); err != nil {
		t.Fatalf("IncrementTokens #1: %v", err)
	}
	var got models.SchemaIndexProgress
	if err := db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got.InputTokens != 1500 || got.OutputTokens != 400 {
		t.Errorf("after 1st IncrementTokens: got (%d, %d), want (1500, 400)", got.InputTokens, got.OutputTokens)
	}

	// Second increment — must accumulate, not replace.
	if err := r.IncrementTokens(ctx, projectID, 200, 75); err != nil {
		t.Fatalf("IncrementTokens #2: %v", err)
	}
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.InputTokens != 1700 || got.OutputTokens != 475 {
		t.Errorf("after 2nd IncrementTokens: got (%d, %d), want (1700, 475) — sums must accumulate", got.InputTokens, got.OutputTokens)
	}

	// Reset must zero totals for the next build.
	if err := r.Reset(ctx, projectID, "run-tokens-2"); err != nil {
		t.Fatalf("Reset #2: %v", err)
	}
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("after Reset: tokens = (%d, %d), want (0, 0) — Reset must zero per-build totals", got.InputTokens, got.OutputTokens)
	}

	// Empty projectID is an error.
	if err := r.IncrementTokens(ctx, "", 1, 1); err == nil {
		t.Error("IncrementTokens with empty projectID should error")
	}

	// Both deltas <= 0 is a no-op (not an error).
	if err := r.IncrementTokens(ctx, projectID, 0, 0); err != nil {
		t.Errorf("zero deltas should be no-op, got %v", err)
	}
	if err := r.IncrementTokens(ctx, projectID, -5, -3); err != nil {
		t.Errorf("negative deltas should be no-op, got %v", err)
	}
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("after no-op increments: tokens = (%d, %d), want (0, 0)", got.InputTokens, got.OutputTokens)
	}

	// Mixed-sign delta: positive applies, non-positive is dropped.
	if err := r.IncrementTokens(ctx, projectID, 10, -3); err != nil {
		t.Fatalf("mixed-sign IncrementTokens: %v", err)
	}
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.InputTokens != 10 || got.OutputTokens != 0 {
		t.Errorf("mixed-sign: got (%d, %d), want (10, 0)", got.InputTokens, got.OutputTokens)
	}

	// Without a Reset, the doc is missing — IncrementTokens must error.
	if err := r.IncrementTokens(ctx, "ghost-tokens", 1, 1); err == nil {
		t.Error("IncrementTokens without Reset should error")
	}
}

func TestAgentInteg_SchemaIndexProgress_RecordErrorStampedOnFailure(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	r := NewSchemaIndexProgressRepository(db)
	projectID := "proj-agent-err"
	_ = r.Reset(ctx, projectID, "r")
	if err := r.RecordError(ctx, projectID, "embedding provider 502"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	var got models.SchemaIndexProgress
	_ = db.Collection(CollectionSchemaIndexProgress).
		FindOne(ctx, map[string]string{"project_id": projectID}).
		Decode(&got)
	if got.ErrorMessage != "embedding provider 502" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
}
