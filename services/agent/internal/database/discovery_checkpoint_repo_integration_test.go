//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// Checkpoints against a real MongoDB. What is worth a container here is
// everything a fake cannot answer: whether the BSON round-trip actually
// preserves what replay reads back, whether the unique index makes a retried
// write a replace rather than a duplicate, and whether a changed retention
// value takes effect instead of failing startup forever.

func checkpointStepInput(runID string, n, rowCount int) CheckpointStepInput {
	rows := []map[string]interface{}{
		{"cohort": "2026-01", "users": int64(412)},
		{"cohort": "2026-02", "users": int64(388)},
	}
	compact := gomodels.BuildCompactResult(rows)
	return CheckpointStepInput{
		ProjectID: "proj-1",
		RunID:     runID,
		Attempt:   1,
		Step: models.ExplorationStep{
			Step:            n,
			Timestamp:       time.Now().UTC().Truncate(time.Millisecond),
			WarehouseID:     "crm",
			Action:          "query_data",
			Thinking:        "checking retention",
			QueryPurpose:    "retention by cohort",
			Query:           "SELECT cohort, COUNT(*) FROM ds.users GROUP BY 1",
			RowCount:        rowCount,
			ExecutionTimeMs: 1234,
			CompactResult:   &compact,
			Quality: []gowarehouse.QualityCaveat{
				{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
			},
			Fixed:       true,
			FixAttempts: 2,
			TokensIn:    900,
			TokensOut:   120,
			// Dropped by checkpointPayload — asserted gone below.
			FixHistory:  []models.FixAttempt{{Step: n, PromptIn: "long prompt"}},
			LLMRequest:  "the entire prompt",
			LLMResponse: "the entire response",
		},
		RowSample: rows,
		Args:      models.CheckpointArgs{Datasource: "crm"},
	}
}

// TestInteg_Checkpoint_RoundTripsEverythingReplayReads is the contract: what
// comes back has to be enough to rebuild the turn and to rebuild the
// verifier's evidence bundle. Anything silently lost in the BSON round-trip
// would surface only as a degraded resumed run.
func TestInteg_Checkpoint_RoundTripsEverythingReplayReads(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	in := checkpointStepInput("run-1", 1, 50_000)
	if err := repo.SaveStep(ctx, in); err != nil {
		t.Fatalf("SaveStep: %v", err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatalf("LoadPrefix: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("prefix len = %d, want 1", set.Len())
	}
	got := set.Steps[0]

	if got.Step.Step != 1 || got.Step.Action != "query_data" {
		t.Errorf("step identity = (%d, %q)", got.Step.Step, got.Step.Action)
	}
	if got.Step.Query != in.Step.Query || got.Step.QueryPurpose != in.Step.QueryPurpose {
		t.Errorf("the query and its purpose must survive: %q / %q", got.Step.Query, got.Step.QueryPurpose)
	}
	if got.Step.Thinking != in.Step.Thinking {
		t.Errorf("Thinking = %q", got.Step.Thinking)
	}
	// The authoritative count — what keeps the rebuilt bundle honest once the
	// rows are a sample.
	if got.Step.RowCount != 50_000 {
		t.Errorf("RowCount = %d, want 50000", got.Step.RowCount)
	}
	if len(got.Step.QueryResult) != len(in.RowSample) {
		t.Errorf("retained rows = %d, want %d", len(got.Step.QueryResult), len(in.RowSample))
	}
	if got.Step.CompactResult == nil {
		t.Fatal("CompactResult must survive — it is what the analysis phase renders")
	}
	if got.Step.CompactResult.RowCount != in.Step.CompactResult.RowCount {
		t.Errorf("digest row count = %d, want %d",
			got.Step.CompactResult.RowCount, in.Step.CompactResult.RowCount)
	}
	// Quality is the field whose loss would be silent AND wrong: every
	// insight's evidence label is derived from it and it is knowable nowhere
	// else, so a resumed run without it would relabel findings computed over
	// withheld rows as sound.
	if len(got.Step.Quality) != 1 || got.Step.Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Errorf("Quality = %+v, want the withheld caveat", got.Step.Quality)
	}
	if got.Step.WarehouseID != "crm" {
		t.Errorf("WarehouseID = %q, want crm", got.Step.WarehouseID)
	}
	if got.Step.TokensIn != 900 || got.Step.TokensOut != 120 {
		t.Errorf("token counts = (%d, %d)", got.Step.TokensIn, got.Step.TokensOut)
	}
	if !got.Step.Fixed || got.Step.FixAttempts != 2 {
		t.Errorf("self-heal summary = (%v, %d)", got.Step.Fixed, got.Step.FixAttempts)
	}
	// The replay arguments the step struct has no field for.
	if got.Args.Datasource != "crm" {
		t.Errorf("Args.Datasource = %q, want crm", got.Args.Datasource)
	}

	// And what is deliberately NOT kept.
	if len(got.Step.FixHistory) != 0 {
		t.Errorf("FixHistory must not be persisted, got %d entries", len(got.Step.FixHistory))
	}
	if got.Step.LLMRequest != "" || got.Step.LLMResponse != "" {
		t.Errorf("LLM dialog must not be persisted: %q / %q", got.Step.LLMRequest, got.Step.LLMResponse)
	}
}

// TestInteg_Checkpoint_RetriedWriteReplacesRatherThanDuplicating is why the
// (run_id, step_number) index is unique and the write is an upsert. A retried
// checkpoint — or a resumed run re-checkpointing the prefix it replayed —
// must not duplicate the row or fail on the index.
func TestInteg_Checkpoint_RetriedWriteReplacesRatherThanDuplicating(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	first := checkpointStepInput("run-1", 1, 10)
	if err := repo.SaveStep(ctx, first); err != nil {
		t.Fatalf("first SaveStep: %v", err)
	}

	// The same step, written again by a later attempt with a different row
	// count — exactly what a replayed prefix does.
	second := checkpointStepInput("run-1", 1, 99)
	second.Attempt = 2
	if err := repo.SaveStep(ctx, second); err != nil {
		t.Fatalf("second SaveStep must replace, not conflict: %v", err)
	}

	n, err := db.Collection(CollectionDiscoveryCheckpoints).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("documents = %d, want 1 — the write must replace", n)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.Steps[0].Step.RowCount != 99 {
		t.Errorf("RowCount = %d, want 99 — the later write must win", set.Steps[0].Step.RowCount)
	}
	if set.Attempt != 2 {
		t.Errorf("set attempt = %d, want 2", set.Attempt)
	}
}

// TestInteg_Checkpoint_PrefixStopsAtAHole is the honesty rule. Checkpoint
// writes are best-effort so they can never abort a run, which means a hole is
// possible. Replaying across one would misnumber every later step relative to
// what the model was told — and insights cite step numbers, so the run would
// attribute evidence to the wrong queries.
func TestInteg_Checkpoint_PrefixStopsAtAHole(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	for _, n := range []int{1, 2, 4, 5} { // step 3's write "failed"
		if err := repo.SaveStep(ctx, checkpointStepInput("run-1", n, 10)); err != nil {
			t.Fatalf("SaveStep %d: %v", n, err)
		}
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 2 {
		t.Fatalf("prefix len = %d, want 2 — the replayable prefix ends at the hole", set.Len())
	}
	if set.Steps[0].Step.Step != 1 || set.Steps[1].Step.Step != 2 {
		t.Errorf("prefix = %d, %d; want 1, 2", set.Steps[0].Step.Step, set.Steps[1].Step.Step)
	}
}

// TestInteg_Checkpoint_SummaryIsDroppedOverAHole pins the stronger fact. A
// summary promises "exploration finished", and acting on it over a prefix
// that cannot be replayed in full would send the resumed run straight to
// analysis over an incomplete step set.
func TestInteg_Checkpoint_SummaryIsDroppedOverAHole(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	for _, n := range []int{1, 3} {
		if err := repo.SaveStep(ctx, checkpointStepInput("run-1", n, 10)); err != nil {
			t.Fatal(err)
		}
	}
	err := repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 1,
		Summary: models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 3, Duration: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.ExplorationComplete() {
		t.Error("a summary over a broken prefix must not claim exploration finished")
	}
	if set.Len() != 1 {
		t.Errorf("prefix len = %d, want 1", set.Len())
	}
}

// TestInteg_Checkpoint_SummaryIsDroppedWhenItClaimsMoreThanThePrefixHolds is
// the shape a gap check alone misses, and the one that actually loses work.
//
// If the checkpoint write that failed was for the LAST step, there is no later
// row to leave a hole: the prefix looks perfectly clean at 39 steps while the
// summary says 40. Trusting it sends the resumed run straight to analysis over
// 39 steps' worth of evidence, silently dropping the final query instead of
// re-exploring it.
func TestInteg_Checkpoint_SummaryIsDroppedWhenItClaimsMoreThanThePrefixHolds(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Steps 1..3 landed; step 4's write failed; exploration then finished.
	for n := 1; n <= 3; n++ {
		if err := repo.SaveStep(ctx, checkpointStepInput("run-1", n, 10)); err != nil {
			t.Fatal(err)
		}
	}
	err := repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 1,
		Summary: models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 4, Duration: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 3 {
		t.Fatalf("prefix len = %d, want 3", set.Len())
	}
	if set.ExplorationComplete() {
		t.Error("a summary claiming 4 steps over a 3-step prefix must not be trusted — the resumed run has to keep exploring")
	}

	// And the honest case still works: a summary whose count the prefix
	// covers is trusted.
	if err := repo.SaveStep(ctx, checkpointStepInput("run-1", 4, 10)); err != nil {
		t.Fatal(err)
	}
	set, err = repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 4 || !set.ExplorationComplete() {
		t.Errorf("once the missing step lands, the summary must be trusted: len=%d complete=%v",
			set.Len(), set.ExplorationComplete())
	}
}

// TestInteg_Checkpoint_SummaryRoundTrip covers the cheapest resume path: a run
// that died after exploration finished goes straight to analysis, and
// everything that branch needs comes from this one document.
func TestInteg_Checkpoint_SummaryRoundTrip(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := repo.SaveStep(ctx, checkpointStepInput("run-1", 1, 10)); err != nil {
		t.Fatal(err)
	}
	err := repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 1,
		Summary: models.ExplorationCheckpointSummary{
			Completed: true, CompletionMsg: "covered everything", TotalSteps: 1,
			Duration: 3*time.Minute + 500*time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("SaveExplorationSummary: %v", err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !set.ExplorationComplete() {
		t.Fatal("the summary must mark exploration complete")
	}
	if set.Summary.CompletionMsg != "covered everything" || set.Summary.TotalSteps != 1 {
		t.Errorf("summary = %+v", set.Summary)
	}
	if set.Summary.Duration != 3*time.Minute+500*time.Millisecond {
		t.Errorf("Duration = %s, want 3m0.5s", set.Summary.Duration)
	}
	// The summary rides at step_number 0, so it must not appear as a step.
	if set.Len() != 1 {
		t.Errorf("prefix len = %d, want 1 — the summary is not a replayable step", set.Len())
	}

	// And rewriting it replaces rather than duplicating.
	if err := repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 2,
		Summary: models.ExplorationCheckpointSummary{Completed: false, TotalSteps: 1},
	}); err != nil {
		t.Fatal(err)
	}
	n, _ := db.Collection(CollectionDiscoveryCheckpoints).CountDocuments(ctx, bson.M{
		"run_id": "run-1", "step_number": 0,
	})
	if n != 1 {
		t.Errorf("summary documents = %d, want 1", n)
	}
}

// TestInteg_Checkpoint_DeleteAndListAreRunScoped pins that both the purge and
// the sweep's live-set query stay inside one run. The sweep decides which
// per-run vector collections to drop, so over-reaching either way destroys or
// leaks work.
func TestInteg_Checkpoint_DeleteAndListAreRunScoped(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	for _, run := range []string{"run-a", "run-b"} {
		for n := 1; n <= 2; n++ {
			if err := repo.SaveStep(ctx, checkpointStepInput(run, n, 10)); err != nil {
				t.Fatal(err)
			}
		}
	}

	ids, err := repo.ListRunIDsWithCheckpoints(ctx)
	if err != nil {
		t.Fatalf("ListRunIDsWithCheckpoints: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("run ids = %v, want both runs", ids)
	}

	deleted, err := repo.DeleteByRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("DeleteByRun: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if set, _ := repo.LoadPrefix(ctx, "run-a"); set.Len() != 0 {
		t.Errorf("run-a still has %d checkpoints", set.Len())
	}
	if set, _ := repo.LoadPrefix(ctx, "run-b"); set.Len() != 2 {
		t.Errorf("run-b lost checkpoints: %d, want 2", set.Len())
	}

	ids, _ = repo.ListRunIDsWithCheckpoints(ctx)
	if len(ids) != 1 || ids[0] != "run-b" {
		t.Errorf("run ids after delete = %v, want [run-b]", ids)
	}
}

// TestInteg_Checkpoint_RetentionChangeRecreatesTheTTL is what makes
// DISCOVERY_CHECKPOINT_RETENTION an actual knob.
//
// A TTL's expireAfterSeconds cannot be mutated in place: CreateOne against an
// existing index of the same name with a different value returns
// IndexOptionsConflict. Without the drop-and-recreate, changing the env var
// would fail agent startup forever instead of taking effect — i.e. the
// operator could not raise retention to match a raised DISCOVERY_MAX_DURATION,
// which is the one situation where it matters.
func TestInteg_Checkpoint_RetentionChangeRecreatesTheTTL(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	ttlSeconds := func() int32 {
		t.Helper()
		cur, err := db.Collection(CollectionDiscoveryCheckpoints).Indexes().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cur.Close(ctx)
		for cur.Next(ctx) {
			var idx struct {
				Name               string `bson:"name"`
				ExpireAfterSeconds *int32 `bson:"expireAfterSeconds"`
			}
			if err := cur.Decode(&idx); err != nil {
				t.Fatal(err)
			}
			if idx.Name == checkpointTTLIndexName && idx.ExpireAfterSeconds != nil {
				return *idx.ExpireAfterSeconds
			}
		}
		t.Fatalf("no %s index found", checkpointTTLIndexName)
		return 0
	}

	first := &DiscoveryCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryCheckpoints),
		retention: 48 * time.Hour,
	}
	if err := first.EnsureIndexes(ctx); err != nil {
		t.Fatalf("first EnsureIndexes: %v", err)
	}
	if got := ttlSeconds(); got != int32((48 * time.Hour).Seconds()) {
		t.Fatalf("TTL = %ds, want 48h", got)
	}

	// Idempotent at the same value.
	if err := first.EnsureIndexes(ctx); err != nil {
		t.Fatalf("re-running EnsureIndexes at the same retention must be a no-op: %v", err)
	}

	// An operator raises DISCOVERY_MAX_DURATION to a week and retention with
	// it. The next boot must take effect, not fail.
	raised := &DiscoveryCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryCheckpoints),
		retention: 192 * time.Hour,
	}
	if err := raised.EnsureIndexes(ctx); err != nil {
		t.Fatalf("a changed retention must recreate the TTL, not fail startup: %v", err)
	}
	if got := ttlSeconds(); got != int32((192 * time.Hour).Seconds()) {
		t.Errorf("TTL = %ds, want 192h — the env var did not take effect", got)
	}

	// And the unique ordering index survives the dance.
	if err := raised.SaveStep(ctx, checkpointStepInput("run-1", 1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := raised.SaveStep(ctx, checkpointStepInput("run-1", 1, 20)); err != nil {
		t.Fatalf("the unique index must still make a repeat write a replace: %v", err)
	}
	n, _ := db.Collection(CollectionDiscoveryCheckpoints).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if n != 1 {
		t.Errorf("documents = %d, want 1", n)
	}
}

// TestInteg_Checkpoint_LoadPrefixOnAnUnknownRunIsEmptyNotAnError pins the
// race the agent's resume path has to survive: the API verified a checkpoint
// existed, then the retention TTL reclaimed it before the agent booted.
func TestInteg_Checkpoint_LoadPrefixOnAnUnknownRunIsEmptyNotAnError(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	set, err := repo.LoadPrefix(ctx, "run-that-expired")
	if err != nil {
		t.Fatalf("LoadPrefix on an unknown run must not error: %v", err)
	}
	if set.Len() != 0 || set.ExplorationComplete() {
		t.Errorf("set = %+v, want nothing to resume from", set)
	}
}

// TestInteg_Checkpoint_ANewerAttemptOwnsItsSteps is the fence against two
// agents on one run.
//
// That is reachable without anything exotic: the API's startup sweep marks
// in-flight runs `failed` after a restart WITHOUT reaping their workloads, so
// an operator resuming such a run can have the orphaned previous agent and the
// live one both writing checkpoints keyed on (run_id, step_number). Unfenced,
// the orphan would overwrite the live attempt's steps with its own and replay
// would reconstruct a conversation spliced from two different runs.
func TestInteg_Checkpoint_ANewerAttemptOwnsItsSteps(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// The live attempt (2) writes step 1.
	live := checkpointStepInput("run-1", 1, 777)
	live.Attempt = 2
	if err := repo.SaveStep(ctx, live); err != nil {
		t.Fatal(err)
	}

	// The orphaned attempt (1) tries to write the same step with its own
	// content. Refused — and NOT reported as an error, because nothing is
	// wrong and there is nothing to retry: the newer attempt owns the step.
	orphan := checkpointStepInput("run-1", 1, 111)
	orphan.Attempt = 1
	if err := repo.SaveStep(ctx, orphan); err != nil {
		t.Fatalf("a superseded attempt's write must be refused quietly, got: %v", err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 1 {
		t.Fatalf("prefix len = %d, want 1 — the refused write must not have inserted a second row", set.Len())
	}
	if got := set.Steps[0].Step.RowCount; got != 777 {
		t.Errorf("step row count = %d, want 777 — the orphan overwrote the live attempt's step", got)
	}
	if set.Attempt != 2 {
		t.Errorf("set attempt = %d, want 2", set.Attempt)
	}

	// The live attempt can still re-write its own step (a replayed prefix
	// does exactly that), and a LATER attempt can replace it.
	live.Step.RowCount = 888
	if err := repo.SaveStep(ctx, live); err != nil {
		t.Fatalf("the owning attempt must still be able to rewrite its step: %v", err)
	}
	newer := checkpointStepInput("run-1", 1, 999)
	newer.Attempt = 3
	if err := repo.SaveStep(ctx, newer); err != nil {
		t.Fatalf("a newer attempt must be able to replace an older attempt's step: %v", err)
	}
	set, _ = repo.LoadPrefix(ctx, "run-1")
	if got := set.Steps[0].Step.RowCount; got != 999 {
		t.Errorf("step row count = %d, want 999 — a newer attempt must win", got)
	}
}

// TestInteg_Checkpoint_ANewerAttemptOwnsTheSummary is the same fence on the
// summary row: an orphan must not declare exploration finished on behalf of
// the live attempt, which would send it straight to analysis.
func TestInteg_Checkpoint_ANewerAttemptOwnsTheSummary(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryCheckpointRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := repo.SaveStep(ctx, func() CheckpointStepInput {
		in := checkpointStepInput("run-1", 1, 10)
		in.Attempt = 2
		return in
	}()); err != nil {
		t.Fatal(err)
	}

	// The live attempt has NOT finished exploring. The orphan says it has.
	err := repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 2,
		Summary: models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 1,
		Summary: models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 99},
	})
	if err != nil {
		t.Fatalf("a superseded attempt's summary write must be refused quietly, got: %v", err)
	}

	set, err := repo.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !set.ExplorationComplete() {
		t.Fatal("the live attempt's own summary should stand")
	}
	if set.Summary.TotalSteps != 1 {
		t.Errorf("summary total steps = %d, want 1 — the orphan's summary won", set.Summary.TotalSteps)
	}
}
