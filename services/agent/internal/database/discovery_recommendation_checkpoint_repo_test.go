package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestRecommendationCheckpointDocTagsAreDisjoint is the guard the doc comment
// promises, and here the hazard is live rather than theoretical: the inlined
// models.RecommendationStep already owns bson:"recommendations", so an outer
// field of that name would write the recommendation list twice — and the two
// copies could disagree, because applyRecommendationDropStats and
// applyUnresolvedFigureDropStats resync the step's own field as the phase
// drops recommendations.
func TestRecommendationCheckpointDocTagsAreDisjoint(t *testing.T) {
	docType := reflect.TypeOf(RecommendationCheckpointDoc{})
	own := bsonTagNames(t, docType, false)
	embedded := bsonTagNames(t, docType, true)

	if len(embedded) == 0 {
		t.Fatal("models.RecommendationStep is no longer inlined; this test is testing nothing")
	}
	for name := range own {
		if embedded[name] {
			t.Errorf("bson tag %q is declared on both RecommendationCheckpointDoc and the inlined models.RecommendationStep — one document cannot carry the same key twice", name)
		}
	}
	if !embedded["recommendations"] {
		t.Error("the inlined models.RecommendationStep must supply recommendations — it is the list the restore reads")
	}
	if !own["run_id"] {
		t.Error("RecommendationCheckpointDoc must supply run_id — the key of uq_run")
	}
	if !own["eligible_insight_ids"] {
		t.Error("RecommendationCheckpointDoc must supply eligible_insight_ids — half the restore gate")
	}
	if !own["analysis_generation"] {
		t.Error("RecommendationCheckpointDoc must supply analysis_generation — the half an insight id cannot carry")
	}
	if !own["rec_verdicts"] {
		t.Error("RecommendationCheckpointDoc must supply rec_verdicts — the per-recommendation completion markers")
	}
}

// TestRecommendationCheckpointSaveDraft_RejectsUnrestorableRows — a row
// written against an exploration that was never summarised can never be
// restored, so it is refused before any Mongo call.
func TestRecommendationCheckpointSaveDraft_RejectsUnrestorableRows(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	ctx := context.Background()

	if err := repo.SaveDraft(ctx, RecommendationCheckpointInput{ExplorationAttempt: 1}); err == nil ||
		!strings.Contains(err.Error(), "run_id is required") {
		t.Errorf("expected a run_id refusal, got %v", err)
	}
	err := repo.SaveDraft(ctx, RecommendationCheckpointInput{RunID: "run-1"})
	if err == nil || !strings.Contains(err.Error(), "exploration_attempt must be >= 1") {
		t.Errorf("expected an exploration_attempt refusal, got %v", err)
	}
}

// TestSaveRecVerdict_RejectsUnaddressableMarkers — the recommendation id
// becomes a field path, so an empty one would write to "rec_verdicts." and
// produce a marker no restore can match.
func TestSaveRecVerdict_RejectsUnaddressableMarkers(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	ctx := context.Background()

	if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{RecommendationID: "r1"}); err == nil ||
		!strings.Contains(err.Error(), "run_id is required") {
		t.Errorf("expected a run_id refusal, got %v", err)
	}
	if _, err := repo.SaveRecVerdict(ctx, RecommendationVerdictInput{RunID: "run-1"}); err == nil ||
		!strings.Contains(err.Error(), "recommendation_id is required") {
		t.Errorf("expected a recommendation_id refusal, got %v", err)
	}
}

// TestRecommendationCheckpointRunScopedCallsRequireARunID — same reason as
// the draft repo's: these are collection-wide filters on run_id.
func TestRecommendationCheckpointRunScopedCallsRequireARunID(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	ctx := context.Background()

	if _, err := repo.Load(ctx, "", 1); err == nil {
		t.Error("Load accepted an empty run id")
	}
	if _, err := repo.DeleteByRun(ctx, ""); err == nil {
		t.Error("DeleteByRun accepted an empty run id — that filter would match every run")
	}
	if _, err := repo.TouchByRun(ctx, ""); err == nil {
		t.Error("TouchByRun accepted an empty run id — that filter would match every run")
	}
}

// TestRecommendationCheckpointLoad_NoDurableSummaryReturnsNilWithoutQuerying
// pins the same gate the draft loader has: no exploration summary means no row
// could be valid, so the query is not worth making. Runs against a repository
// with no collection, which is what proves it never queried.
func TestRecommendationCheckpointLoad_NoDurableSummaryReturnsNilWithoutQuerying(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	cp, err := repo.Load(context.Background(), "run-1", 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp != nil {
		t.Error("expected nil for a run with no durable exploration summary")
	}
}

// TestSaveRecVerdict_RefusesAnIdThatWouldNestInsteadOfKey — the same hazard on
// the recommendation side, reachable the same way: the parser mints an id only
// when the model supplied none.
func TestSaveRecVerdict_RefusesAnIdThatWouldNestInsteadOfKey(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	_, err := repo.SaveRecVerdict(context.Background(), RecommendationVerdictInput{
		RunID: "run-1", RecommendationID: "rec.1",
	})
	if !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Fatalf("want ErrUnsafeMarkerKey, got %v", err)
	}
}

// TestRecommendationCheckpointSaveDraft_RefusesRecommendationsSharingAnId —
// the same validation bypass on the recommendation side.
func TestRecommendationCheckpointSaveDraft_RefusesRecommendationsSharingAnId(t *testing.T) {
	repo := &DiscoveryRecommendationCheckpointRepository{}
	step := models.RecommendationStep{Recommendations: []models.Recommendation{
		{ID: "r1", Title: "first"},
		{ID: "r1", Title: "a different one the model gave the same id"},
	}}
	err := repo.SaveDraft(context.Background(), RecommendationCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	})
	if !errors.Is(err, ErrAmbiguousMarkerKey) {
		t.Fatalf("want ErrAmbiguousMarkerKey, got %v", err)
	}
}
