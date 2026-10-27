//go:build integration

package database

import (
	"context"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
)

// Integration tests for LLMModelWindowRepository against a live Mongo
// testcontainer. Covers: cold lookup, save/get round-trip, upsert overwrite,
// non-positive-window no-op, and validation errors.

const mwTestProject = "proj-model-window-1"

func TestAgentInteg_ModelWindow_ColdLookup_Zero(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	r := NewLLMModelWindowRepository(db)

	got, err := r.GetWindow(context.Background(), mwTestProject, llmlayer.DefaultProviderID, "bedrock", "GLM-5")
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

	if err := r.SaveWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "bedrock", "GLM-5", 202752); err != nil {
		t.Fatalf("SaveWindow: %v", err)
	}
	got, err := r.GetWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "bedrock", "GLM-5")
	if err != nil {
		t.Fatalf("GetWindow: %v", err)
	}
	if got != 202752 {
		t.Errorf("GetWindow = %d, want 202752", got)
	}

	// Upsert overwrites (a later, more accurate calibration).
	if err := r.SaveWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "bedrock", "GLM-5", 200000); err != nil {
		t.Fatalf("SaveWindow overwrite: %v", err)
	}
	got, _ = r.GetWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "bedrock", "GLM-5")
	if got != 200000 {
		t.Errorf("after upsert GetWindow = %d, want 200000", got)
	}

	// A different model for the same project is independent.
	if got, _ := r.GetWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "bedrock", "other"); got != 0 {
		t.Errorf("unrelated model should be 0, got %d", got)
	}
}

func TestAgentInteg_ModelWindow_NonPositiveIsNoop(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "p", "m", 0); err != nil {
		t.Fatalf("SaveWindow(0) should be a no-op, got %v", err)
	}
	if got, _ := r.GetWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "p", "m"); got != 0 {
		t.Errorf("no-op save should leave 0, got %d", got)
	}
}

func TestAgentInteg_ModelWindow_Validation(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, "", llmlayer.DefaultProviderID, "p", "m", 100); err == nil {
		t.Error("SaveWindow with empty projectID should error")
	}
	if _, err := r.GetWindow(ctx, "proj", llmlayer.DefaultProviderID, "", "m"); err == nil {
		t.Error("GetWindow with empty provider should error")
	}
}

// Two ENTRIES can name the same provider and model on different endpoints —
// a DeepInfra qwen and a self-hosted vLLM qwen, say — and their measured
// windows are not the same number. Keyed by provider+model alone, whichever
// was calibrated last answered for both, and the other layer budgeted
// against a window its endpoint does not have.
//
// The reserved default entry writes no provider_id at all, so windows
// measured before #407 keep answering for it.
func TestAgentInteg_ModelWindow_EntriesAreIndependent(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewLLMModelWindowRepository(db)

	if err := r.SaveWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "litellm", "qwen3", 32768); err != nil {
		t.Fatalf("SaveWindow(default): %v", err)
	}
	if err := r.SaveWindow(ctx, mwTestProject, "vllm", "litellm", "qwen3", 131072); err != nil {
		t.Fatalf("SaveWindow(vllm): %v", err)
	}

	got, err := r.GetWindow(ctx, mwTestProject, llmlayer.DefaultProviderID, "litellm", "qwen3")
	if err != nil {
		t.Fatalf("GetWindow(default): %v", err)
	}
	if got != 32768 {
		t.Errorf("the default entry's window = %d, want 32768 — the other entry's calibration answered for it", got)
	}
	if got, _ = r.GetWindow(ctx, mwTestProject, "vllm", "litellm", "qwen3"); got != 131072 {
		t.Errorf("the vllm entry's window = %d, want 131072", got)
	}
	// An entry nothing has measured is cold, not the default's value.
	if got, _ = r.GetWindow(ctx, mwTestProject, "deepinfra", "litellm", "qwen3"); got != 0 {
		t.Errorf("an unmeasured entry = %d, want 0", got)
	}
}
