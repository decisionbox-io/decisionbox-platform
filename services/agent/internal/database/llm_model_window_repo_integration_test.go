//go:build integration

package database

import (
	"context"
	"testing"
)

// Integration tests for LLMModelWindowRepository against a live Mongo
// testcontainer. Covers: cold lookup, save/get round-trip, upsert overwrite,
// non-positive-window no-op, and validation errors.

const mwTestProject = "proj-model-window-1"

func TestAgentInteg_ModelWindow_ColdLookup_Zero(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	r := NewLLMModelWindowRepository(db)

	got, err := r.GetWindow(context.Background(), mwTestProject, "bedrock", "GLM-5")
	if err != nil {
		t.Fatalf("GetWindow: %v", err)
	}
	if got != 0 {
		t.Errorf("cold GetWindow should be 0, got %d", got)
	}
}

func TestAgentInteg_ModelWindow_SaveGetRoundTrip(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, mwTestProject, "bedrock", "GLM-5", 202752); err != nil {
		t.Fatalf("SaveWindow: %v", err)
	}
	got, err := r.GetWindow(ctx, mwTestProject, "bedrock", "GLM-5")
	if err != nil {
		t.Fatalf("GetWindow: %v", err)
	}
	if got != 202752 {
		t.Errorf("GetWindow = %d, want 202752", got)
	}

	// Upsert overwrites (a later, more accurate calibration).
	if err := r.SaveWindow(ctx, mwTestProject, "bedrock", "GLM-5", 200000); err != nil {
		t.Fatalf("SaveWindow overwrite: %v", err)
	}
	got, _ = r.GetWindow(ctx, mwTestProject, "bedrock", "GLM-5")
	if got != 200000 {
		t.Errorf("after upsert GetWindow = %d, want 200000", got)
	}

	// A different model for the same project is independent.
	if got, _ := r.GetWindow(ctx, mwTestProject, "bedrock", "other"); got != 0 {
		t.Errorf("unrelated model should be 0, got %d", got)
	}
}

func TestAgentInteg_ModelWindow_NonPositiveIsNoop(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, mwTestProject, "p", "m", 0); err != nil {
		t.Fatalf("SaveWindow(0) should be a no-op, got %v", err)
	}
	if got, _ := r.GetWindow(ctx, mwTestProject, "p", "m"); got != 0 {
		t.Errorf("no-op save should leave 0, got %d", got)
	}
}

func TestAgentInteg_ModelWindow_Validation(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, "", "p", "m", 100); err == nil {
		t.Error("SaveWindow with empty projectID should error")
	}
	if _, err := r.GetWindow(ctx, "proj", "", "m"); err == nil {
		t.Error("GetWindow with empty provider should error")
	}
}
