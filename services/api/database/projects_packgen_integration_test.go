//go:build integration

package database

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// seedPackGenProject inserts a project document in pack_generation_pending
// state with generate_pack.enabled=true. Returns the hex object id.
func seedPackGenProject(t *testing.T, opts ...func(bson.M)) string {
	t.Helper()
	ctx := context.Background()
	oid := primitive.NewObjectID()
	doc := bson.M{
		"_id":   oid,
		"name":  "pack-gen project",
		"state": models.ProjectStatePackGenerationPending,
		"generate_pack": bson.M{
			"enabled":   true,
			"pack_name": "Acme",
			"pack_slug": "acme",
		},
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}
	for _, opt := range opts {
		opt(doc)
	}
	if _, err := testDB.Collection("projects").InsertOne(ctx, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return oid.Hex()
}

func TestInteg_ProjectRepo_EnqueuePackGen_Success(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t)

	runID := "20260526T120000.000Z"
	existing, queued, err := repo.EnqueuePackGen(ctx, id, runID)
	if err != nil {
		t.Fatalf("EnqueuePackGen: %v", err)
	}
	if queued {
		t.Error("alreadyQueued must be false on fresh enqueue")
	}
	if existing != runID {
		t.Errorf("existingRunID = %q, want %q (fresh enqueue should return our run_id)", existing, runID)
	}

	// Re-read and verify marker landed.
	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PackGenRequest == nil {
		t.Fatal("marker not set after EnqueuePackGen")
	}
	if got.PackGenRequest.RunID != runID {
		t.Errorf("marker.RunID = %q, want %q", got.PackGenRequest.RunID, runID)
	}
	if got.PackGenRequest.RequestedAt.IsZero() {
		t.Error("marker.RequestedAt is zero")
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_ClearsPriorError(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t, func(doc bson.M) {
		doc["pack_gen_last_error"] = "previous attempt: synth retry exhausted"
	})

	if _, _, err := repo.EnqueuePackGen(ctx, id, "run-clears-err"); err != nil {
		t.Fatalf("EnqueuePackGen: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.PackGenLastError != "" {
		t.Errorf("pack_gen_last_error = %q, want empty after atomic enqueue", got.PackGenLastError)
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_AlreadyQueued(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t)

	// First enqueue wins.
	if _, _, err := repo.EnqueuePackGen(ctx, id, "run-first"); err != nil {
		t.Fatalf("first EnqueuePackGen: %v", err)
	}
	// Second enqueue must observe marker + report alreadyQueued.
	existing, queued, err := repo.EnqueuePackGen(ctx, id, "run-second")
	if err != nil {
		t.Fatalf("second EnqueuePackGen: %v", err)
	}
	if !queued {
		t.Error("alreadyQueued must be true on duplicate enqueue")
	}
	if existing != "run-first" {
		t.Errorf("existingRunID = %q, want %q (must return original run_id)", existing, "run-first")
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_WrongState(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t, func(doc bson.M) {
		doc["state"] = models.ProjectStatePackGenerationDone
	})

	_, _, err := repo.EnqueuePackGen(ctx, id, "run-X")
	if err == nil {
		t.Fatal("expected error for state=done")
	}
	if !errors.Is(err, ErrPackGenStateMismatch) {
		t.Errorf("err = %v, want ErrPackGenStateMismatch", err)
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_NotEnabled(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t, func(doc bson.M) {
		doc["generate_pack"] = bson.M{"enabled": false}
	})

	_, _, err := repo.EnqueuePackGen(ctx, id, "run-X")
	if err == nil {
		t.Fatal("expected error for generate_pack.enabled=false")
	}
	if !errors.Is(err, ErrPackGenNotEnabled) {
		t.Errorf("err = %v, want ErrPackGenNotEnabled", err)
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_InvalidProjectID(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	if _, _, err := repo.EnqueuePackGen(ctx, "not-a-hex-oid", "run-X"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestInteg_ProjectRepo_EnqueuePackGen_ProjectMissing(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	// Valid hex but no row.
	missing := primitive.NewObjectID().Hex()
	_, _, err := repo.EnqueuePackGen(ctx, missing, "run-X")
	if err == nil {
		t.Fatal("expected project-not-found error")
	}
}

// TestInteg_ProjectRepo_EnqueuePackGen_ConcurrentDoubleEnqueue exercises
// the predicate's atomicity: two goroutines call EnqueuePackGen for the
// same project simultaneously; one must succeed (alreadyQueued=false),
// the other must observe the marker and report alreadyQueued=true. No
// case may result in both succeeding (which would mean two goroutines
// spawned for one project).
func TestInteg_ProjectRepo_EnqueuePackGen_ConcurrentDoubleEnqueue(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t)

	type outcome struct {
		existing string
		queued   bool
		err      error
	}
	results := make([]outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			runID := "concurrent-A"
			if idx == 1 {
				runID = "concurrent-B"
			}
			ex, q, err := repo.EnqueuePackGen(ctx, id, runID)
			results[idx] = outcome{existing: ex, queued: q, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	fresh := 0
	idem := 0
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("concurrent enqueue: %v", r.err)
		}
		if r.queued {
			idem++
		} else {
			fresh++
		}
	}
	if fresh != 1 || idem != 1 {
		t.Errorf("concurrent enqueue: fresh=%d idem=%d, want exactly 1/1 (atomic predicate violated)", fresh, idem)
	}
}

func TestInteg_ProjectRepo_FinalizePackGenIfStuck_FromPending(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t)

	// Enqueue to set the marker.
	if _, _, err := repo.EnqueuePackGen(ctx, id, "run-pending"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// State stays pending (orchestrator hasn't flipped yet).
	if err := repo.FinalizePackGenIfStuck(ctx, id, "run-pending", "generation interrupted"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.PackGenRequest != nil {
		t.Error("marker should be unset after finalize")
	}
	if got.PackGenLastError != "generation interrupted" {
		t.Errorf("pack_gen_last_error = %q", got.PackGenLastError)
	}
	if got.EffectiveState() != models.ProjectStatePackGenerationPending {
		t.Errorf("state = %q, want pack_generation_pending", got.State)
	}
}

func TestInteg_ProjectRepo_FinalizePackGenIfStuck_FromPackGeneration(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	oid := primitive.NewObjectID()
	if _, err := testDB.Collection("projects").InsertOne(ctx, bson.M{
		"_id":   oid,
		"name":  "in-flight",
		"state": models.ProjectStatePackGeneration,
		"generate_pack": bson.M{
			"enabled":   true,
			"pack_name": "Acme",
			"pack_slug": "acme",
		},
		"pack_gen_request": bson.M{
			"run_id":       "run-mid",
			"requested_at": time.Now(),
		},
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := oid.Hex()

	if err := repo.FinalizePackGenIfStuck(ctx, id, "run-mid", "interrupted mid-run"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.PackGenRequest != nil {
		t.Error("marker should be unset")
	}
	if got.EffectiveState() != models.ProjectStatePackGenerationPending {
		t.Errorf("state = %q, want pack_generation_pending", got.State)
	}
	if got.PackGenLastError != "interrupted mid-run" {
		t.Errorf("pack_gen_last_error = %q", got.PackGenLastError)
	}
}

func TestInteg_ProjectRepo_FinalizePackGenIfStuck_NoOpWhenDone(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	oid := primitive.NewObjectID()
	if _, err := testDB.Collection("projects").InsertOne(ctx, bson.M{
		"_id":        oid,
		"name":       "done",
		"state":      models.ProjectStatePackGenerationDone,
		"domain":     "acme",
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := oid.Hex()

	if err := repo.FinalizePackGenIfStuck(ctx, id, "any-run-id", "should not apply"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.EffectiveState() != models.ProjectStatePackGenerationDone {
		t.Errorf("state = %q, want pack_generation_done (finalize must not overwrite a clean done)", got.State)
	}
	if got.PackGenLastError != "" {
		t.Errorf("pack_gen_last_error = %q, want empty (no-op path must not write the error)", got.PackGenLastError)
	}
}

func TestInteg_ProjectRepo_FinalizePackGenIfStuck_NoOpWhenMarkerAbsent(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t) // pending state, no marker

	if err := repo.FinalizePackGenIfStuck(ctx, id, "any-run-id", "should not apply"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.PackGenLastError != "" {
		t.Errorf("pack_gen_last_error = %q, want empty (no marker → no-op)", got.PackGenLastError)
	}
}

// TestInteg_ProjectRepo_FinalizePackGenIfStuck_NoOpWhenRunIDMismatch
// covers the retry-after-failure race: a goroutine whose provider
// errored already had the orchestrator's revert run; an operator then
// retries and EnqueuePackGen writes a FRESH marker with a NEW run_id.
// The old goroutine's deferred safety-net call must NOT clobber the
// new marker. Predicate is scoped on `pack_gen_request.run_id`.
func TestInteg_ProjectRepo_FinalizePackGenIfStuck_NoOpWhenRunIDMismatch(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)
	id := seedPackGenProject(t)

	// Simulate: a FRESH retry has just enqueued a new marker.
	if _, _, err := repo.EnqueuePackGen(ctx, id, "run-new"); err != nil {
		t.Fatalf("seed fresh marker: %v", err)
	}

	// An OLD goroutine's defer fires with a stale run_id — must no-op.
	if err := repo.FinalizePackGenIfStuck(ctx, id, "run-old", "OLD defer fired"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, _ := repo.GetByID(ctx, id)
	if got.PackGenRequest == nil {
		t.Fatal("fresh marker was unexpectedly cleared by stale-run-id defer")
	}
	if got.PackGenRequest.RunID != "run-new" {
		t.Errorf("marker.RunID = %q, want %q (fresh marker must be preserved)", got.PackGenRequest.RunID, "run-new")
	}
	if got.PackGenLastError != "" {
		t.Errorf("pack_gen_last_error = %q, want empty (stale-run-id defer must not stamp an error)", got.PackGenLastError)
	}
}
