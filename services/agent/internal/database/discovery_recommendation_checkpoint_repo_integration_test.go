//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// fatRecStep is a recommendation phase as it leaves Phase 5: the full dialog,
// the surviving recommendations with their figures and citations, and every
// drop counter the dashboard renders.
func fatRecStep() models.RecommendationStep {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return models.RecommendationStep{
		RunAt:                              now,
		Prompt:                             strings.Repeat("the insights json ", 5000),
		InsightCount:                       12,
		Response:                           `{"recommendations":[]}`,
		TokensIn:                           61_000,
		TokensOut:                          4_200,
		DurationMs:                         33_000,
		RecommendationParseRetries:         1,
		RecommendationCitationRetries:      1,
		RecommendationsCitationsSalvaged:   2,
		RecommendationsCitationsBackfilled: 1,
		RecommendationsDropped:             2,
		RecommendationsDroppedUnknownID:    2,
		FiguresSettled:                     4,
		FiguresAdopted:                     3,
		FiguresResolved:                    7,
		Recommendations: []models.Recommendation{
			{
				ID: "rec-1", Category: "retention", Title: "Win back cohort 7",
				Description: "Offer a targeted discount.", Priority: 1,
				TargetSegment: "cohort-7", SegmentSize: 412, Confidence: 0.8,
				Actions:           []string{"build the segment", "ship the offer"},
				RelatedInsightIDs: []string{"insight-1", "insight-2"},
				CreatedAt:         now,
			},
			{
				ID: "rec-2", Category: "conversion", Title: "Fix weekend signup flow",
				Priority: 2, RelatedInsightIDs: []string{"insight-2"}, CreatedAt: now,
			},
		},
	}
}

func recRepo(t *testing.T, db *DB, retention time.Duration) *DiscoveryRecommendationCheckpointRepository {
	t.Helper()
	return &DiscoveryRecommendationCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryRecommendationCheckpoints),
		retention: retention,
	}
}

func recVerdictFor(id string) models.ValidationResult {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return models.ValidationResult{
		InsightID:     id,
		ClaimedMetric: "a recommendation titled " + id,
		Status:        string(valmodels.StatusSupported),
		Combined:      valmodels.StatusSupported,
		ValidatedAt:   now,
		DocKind:       valmodels.DocRecommendation,
		InputTokens:   7_000,
		OutputTokens:  450,
		Verifier: &valmodels.StructuredVerdict{
			DocID: id, DocKind: valmodels.DocRecommendation, Mode: valmodels.ModeVerifier,
			Overall: valmodels.StatusSupported, OverallReason: "the cited insights hold",
		},
	}
}

// TestInteg_RecommendationCheckpoint_RoundTripKeepsThePhaseWhole — the
// orchestrator restores this step as recStep and the recommendations as the
// run's own, so anything lost here is lost from the result and from the
// recommendation log.
func TestInteg_RecommendationCheckpoint_RoundTripKeepsThePhaseWhole(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)

	in := fatRecStep()
	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
		Step: in, EligibleInsightIDs: []string{"insight-1", "insight-2"},
		AnalysisGeneration: []string{"churn@1700000000000", "revenue@1700000000001"},
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	cp, err := repo.Load(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp == nil {
		t.Fatal("nothing came back")
	}
	if len(cp.EligibleInsightIDs) != 2 {
		t.Fatalf("eligible ids = %v, want 2 — they are half the restore gate", cp.EligibleInsightIDs)
	}
	// The other half, and the one an id cannot carry: which analysis those
	// insights came from. Lost, every restore would trust a model-supplied id.
	if len(cp.AnalysisGeneration) != 2 ||
		cp.AnalysisGeneration[0] != "churn@1700000000000" ||
		cp.AnalysisGeneration[1] != "revenue@1700000000001" {
		t.Errorf("analysis generation = %v, want it round-tripped verbatim", cp.AnalysisGeneration)
	}
	s := cp.Step
	if s.Prompt != in.Prompt || s.Response != in.Response {
		t.Error("lost the audit dialog")
	}
	if s.TokensIn != in.TokensIn || s.TokensOut != in.TokensOut || s.DurationMs != in.DurationMs {
		t.Error("lost the generation call's accounting, which the recommendation log reports")
	}
	if s.RecommendationsDropped != 2 || s.RecommendationsDroppedUnknownID != 2 ||
		s.RecommendationsCitationsSalvaged != 2 || s.RecommendationsCitationsBackfilled != 1 {
		t.Error("lost the drop / salvage telemetry the dashboard renders")
	}
	if s.FiguresSettled != 4 || s.FiguresAdopted != 3 || s.FiguresResolved != 7 {
		t.Error("lost the figure telemetry")
	}
	if len(s.Recommendations) != 2 {
		t.Fatalf("restored %d recommendations, want 2", len(s.Recommendations))
	}
	r := s.Recommendations[0]
	if r.ID != "rec-1" || r.Title == "" || r.Priority != 1 || r.SegmentSize != 412 {
		t.Errorf("recommendation came back hollow: %+v", r)
	}
	if len(r.Actions) != 2 {
		t.Error("lost the actions")
	}
	if len(r.RelatedInsightIDs) != 2 {
		t.Error("lost the citations — the restore gate translates them")
	}
	if len(cp.RecVerdicts) != 0 {
		t.Errorf("a fresh checkpoint carries %d markers, want none", len(cp.RecVerdicts))
	}
}

// TestInteg_RecommendationCheckpoint_MarkersAccumulateAndSurviveReload — a run
// killed after validating some recommendations validates only the rest.
func TestInteg_RecommendationCheckpoint_MarkersAccumulateAndSurviveReload(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
		Step: fatRecStep(), EligibleInsightIDs: []string{"insight-1"},
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{
		RunID: "run-1", Attempt: 1, RecommendationID: "rec-1", Verdict: recVerdictFor("rec-1"),
	}); err != nil {
		t.Fatalf("SaveRecVerdict(attempt 1): %v", err)
	}
	// Attempt 2 restores the row and validates the second.
	if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{
		RunID: "run-1", Attempt: 2, RecommendationID: "rec-2", Verdict: recVerdictFor("rec-2"),
	}); err != nil {
		t.Fatalf("SaveRecVerdict(attempt 2): %v", err)
	}

	cp, err := repo.Load(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cp.RecVerdicts) != 2 {
		t.Fatalf("%d markers, want 2", len(cp.RecVerdicts))
	}
	for _, id := range []string{"rec-1", "rec-2"} {
		vr, ok := cp.RecVerdicts[id]
		if !ok {
			t.Fatalf("no marker for %s", id)
		}
		if vr.Combined != valmodels.StatusSupported || vr.InputTokens != 7_000 {
			t.Errorf("%s came back hollow: %+v", id, vr)
		}
		if vr.Verifier == nil || vr.Verifier.OverallReason != "the cited insights hold" {
			t.Errorf("%s lost its structured verdict", id)
		}
	}
	// The step itself is untouched by a marker write.
	if len(cp.Step.Recommendations) != 2 {
		t.Error("a marker write disturbed the recommendation list")
	}

	n, err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows for one run, want 1", n)
	}
}

// TestInteg_RecommendationCheckpoint_AttemptFenceInBothDirections
func TestInteg_RecommendationCheckpoint_AttemptFenceInBothDirections(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)
	// The fence is the unique index turning a filter that matched nothing
	// into a duplicate-key refusal, so the index has to exist.
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	newer := fatRecStep()
	newer.Response = `{"from":"attempt-3"}`
	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 3, ExplorationAttempt: 1, Step: newer,
	}); err != nil {
		t.Fatalf("SaveDraft(attempt 3): %v", err)
	}

	older := fatRecStep()
	older.Response = `{"from":"attempt-1"}`
	err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: older,
	})
	if !errors.Is(err, ErrSupersededAttempt) {
		t.Fatalf("want ErrSupersededAttempt, got %v", err)
	}
	cp, _ := repo.Load(ctx, "run-1", 1)
	if cp.Step.Response != `{"from":"attempt-3"}` {
		t.Error("an orphaned attempt overwrote a newer one's recommendations")
	}

	// A marker from the orphan must not land.
	if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{
		RunID: "run-1", Attempt: 1, RecommendationID: "rec-1", Verdict: recVerdictFor("rec-1"),
	}); err != nil {
		t.Fatalf("SaveRecVerdict must not error on a fenced no-op, got %v", err)
	}
	cp, _ = repo.Load(ctx, "run-1", 1)
	if len(cp.RecVerdicts) != 0 {
		t.Error("an orphaned attempt wrote a marker into a newer attempt's row")
	}

	// And a later attempt legitimately replaces it.
	latest := fatRecStep()
	latest.Response = `{"from":"attempt-4"}`
	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 4, ExplorationAttempt: 1, Step: latest,
	}); err != nil {
		t.Fatalf("SaveDraft(attempt 4): %v", err)
	}
	cp, _ = repo.Load(ctx, "run-1", 1)
	if cp.Step.Response != `{"from":"attempt-4"}` {
		t.Error("a later attempt could not replace an earlier row")
	}
}

// TestInteg_RecommendationCheckpoint_StalenessFence — a run that re-explored
// must not restore recommendations reasoned over the exploration it discarded,
// and the row must be ignored rather than deleted.
func TestInteg_RecommendationCheckpoint_StalenessFence(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)

	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: fatRecStep(),
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	cp, err := repo.Load(ctx, "run-1", 2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp != nil {
		t.Error("restored recommendations reasoned over a DIFFERENT exploration")
	}
	n, err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Error("the stale row was deleted; it must be ignored")
	}
	if cp, _ = repo.Load(ctx, "run-1", 1); cp == nil {
		t.Error("refused a row written over the exploration this attempt is using")
	}
}

// TestInteg_RecommendationCheckpoint_MissingRowIsNilNotAnError — the ordinary
// answer for a run that never reached Phase 5. A caller treating it as a
// failure would log noise on every first attempt.
func TestInteg_RecommendationCheckpoint_MissingRowIsNilNotAnError(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	cp, err := recRepo(t, db, 48*time.Hour).Load(ctx, "run-never-ran", 1)
	if err != nil {
		t.Fatalf("Load of a missing row must not error: %v", err)
	}
	if cp != nil {
		t.Error("something came back for a run with no checkpoint")
	}
}

// TestInteg_RecommendationCheckpoint_DeleteAndTouch
func TestInteg_RecommendationCheckpoint_DeleteAndTouch(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)

	for _, run := range []string{"run-1", "run-2"} {
		if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
			ProjectID: "p1", RunID: run, Attempt: 2, ExplorationAttempt: 1, Step: fatRecStep(),
		}); err != nil {
			t.Fatalf("SaveDraft(%s): %v", run, err)
		}
	}

	var before RecommendationCheckpointDoc
	if err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).
		FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&before); err != nil {
		t.Fatalf("read back: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := repo.TouchByRun(ctx, "run-1"); err != nil {
		t.Fatalf("TouchByRun: %v", err)
	}
	var after RecommendationCheckpointDoc
	if err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).
		FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&after); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !after.CreatedAt.After(before.CreatedAt) {
		t.Error("created_at did not move")
	}
	if after.Attempt != 2 || after.ExplorationAttempt != 1 {
		t.Error("TouchByRun moved a fence")
	}

	deleted, err := repo.DeleteByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("DeleteByRun: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}
	if cp, _ := repo.Load(ctx, "run-2", 1); cp == nil {
		t.Error("DeleteByRun took another run's row")
	}
}

// TestInteg_RecommendationCheckpoint_EnsureIndexesIncludingTheTTLRecreate
func TestInteg_RecommendationCheckpoint_EnsureIndexesIncludingTheTTLRecreate(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := recRepo(t, db, 48*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := recRepo(t, db, 48*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes twice: %v", err)
	}
	if err := recRepo(t, db, 96*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes at a new retention: %v", err)
	}

	cur, err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer cur.Close(ctx) //nolint:errcheck
	var ttlSeconds int32 = -1
	unique := false
	for cur.Next(ctx) {
		var idx bson.M
		if err := cur.Decode(&idx); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		switch idx["name"] {
		case recommendationCheckpointTTLIndexName:
			if v, ok := idx["expireAfterSeconds"].(int32); ok {
				ttlSeconds = v
			}
		case "uq_run":
			unique, _ = idx["unique"].(bool)
		}
	}
	if !unique {
		t.Error("uq_run is not unique — two rows for one run would both be restorable")
	}
	if want := int32((96 * time.Hour).Seconds()); ttlSeconds != want {
		t.Errorf("TTL expireAfterSeconds = %d, want %d", ttlSeconds, want)
	}
}

// TestInteg_RecommendationCheckpoint_ConcurrentMarkerWritesAllLand — same
// claim as the draft side: Phase 5.5's verdicts come from bounded workers.
func TestInteg_RecommendationCheckpoint_ConcurrentMarkerWritesAllLand(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)

	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: fatRecStep(),
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	ids := []string{"rec-1", "rec-2", "rec-3", "rec-4", "rec-5", "rec-6"}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{
				RunID: "run-1", Attempt: 1, RecommendationID: id, Verdict: recVerdictFor(id),
			}); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SaveRecVerdict: %v", err)
	}

	cp, err := repo.Load(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cp.RecVerdicts) != len(ids) {
		t.Fatalf("%d markers landed out of %d concurrent writes — a verdict was lost", len(cp.RecVerdicts), len(ids))
	}
}

// TestInteg_RecommendationCheckpoint_IgnoresRecommendationsSharingAnId — the
// load-side half on the recommendation side, same reasoning.
func TestInteg_RecommendationCheckpoint_IgnoresRecommendationsSharingAnId(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := recRepo(t, db, 48*time.Hour)

	step := fatRecStep()
	step.Recommendations[1].ID = step.Recommendations[0].ID
	if _, err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).InsertOne(ctx, RecommendationCheckpointDoc{
		RunID: "run-1", ProjectID: "p1", Attempt: 1, ExplorationAttempt: 1,
		CreatedAt: time.Now(), RecommendationStep: step,
		RecVerdicts: map[string]models.ValidationResult{
			step.Recommendations[0].ID: recVerdictFor(step.Recommendations[0].ID),
		},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cp, err := repo.Load(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp != nil {
		t.Error("restored a checkpoint whose marker cannot say which of two recommendations it describes")
	}
	n, err := db.Collection(CollectionDiscoveryRecommendationCheckpoints).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Error("the refused row was deleted")
	}
}
