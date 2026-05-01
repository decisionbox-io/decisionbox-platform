//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestRunRepository_SetDiscoveryID is the post-save linkage contract:
// the orchestrator stamps the parent DiscoveryResult ID onto the run
// doc so the dashboard can jump from a run row to its parent without
// scanning by (project_id, completed_at).
func TestRunRepository_SetDiscoveryID(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	runID, err := repo.Create(ctx, &models.DiscoveryRun{
		ProjectID: "proj-link",
		Status:    models.RunStatusRunning,
		Phase:     models.PhaseExploration,
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const wantDiscID = "65000000000000000000000a"
	if err := repo.SetDiscoveryID(ctx, runID, wantDiscID); err != nil {
		t.Fatalf("SetDiscoveryID: %v", err)
	}

	got, err := repo.GetByID(ctx, runID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DiscoveryID != wantDiscID {
		t.Errorf("DiscoveryID = %q, want %q", got.DiscoveryID, wantDiscID)
	}
	// updated_at must advance — exit handlers and the dashboard live
	// feed both rely on that field as a freshness signal.
	if !got.UpdatedAt.After(got.StartedAt) {
		t.Errorf("UpdatedAt should advance past StartedAt; updated=%v started=%v", got.UpdatedAt, got.StartedAt)
	}
}

// TestRunRepository_SetDiscoveryID_RejectsEmptyArgs proves the guard:
// neither runID nor discoveryID may be empty. Without the guard a
// caller bug would mass-stamp every run with discovery_id="" or worse.
func TestRunRepository_SetDiscoveryID_RejectsEmptyArgs(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)

	if err := repo.SetDiscoveryID(ctx, "", "disc-x"); err == nil {
		t.Error("empty runID should error; got nil")
	}
	if err := repo.SetDiscoveryID(ctx, "65000000000000000000000a", ""); err == nil {
		t.Error("empty discoveryID should error; got nil")
	}
}

// TestRunRepository_SetDiscoveryID_BadHexRejected — not a valid
// ObjectID hex. Surfaces the parse error to the caller rather than
// silently no-op'ing the linkage.
func TestRunRepository_SetDiscoveryID_BadHexRejected(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewRunRepository(db)
	if err := repo.SetDiscoveryID(ctx, "not-an-objectid", "disc-x"); err == nil {
		t.Error("non-hex runID should error; got nil")
	}
}
