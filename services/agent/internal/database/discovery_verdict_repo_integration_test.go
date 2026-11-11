//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// What this file covers: the write an on-demand re-validation makes, against a
// real MongoDB.
//
// A positional array-filter update over an embedded array is not something a
// mock can establish anything about. The two failures that matter are both
// shapes of "the update went somewhere else": it touched a sibling insight, or
// it matched the discovery document and silently updated no array element at
// all. Both are invisible without a server to run the pipeline.

func seedDiscoveryWithTwoOfEach(t *testing.T, db *DB) (string, time.Time) {
	t.Helper()
	ctx := context.Background()
	oid := primitive.NewObjectID()
	before := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	legacyScoring := bson.M{
		"confidence_band":  "high",
		"confidence_base":  0.95,
		"confidence_score": 0.95,
		"engine_version":   1,
	}
	doc := bson.M{
		"_id":        oid,
		"project_id": "proj-1",
		"insights": bson.A{
			bson.M{"id": "ins-1", "name": "target", "severity": "critical", "risk_score": 0.67,
				"confidence": 0.93, "validation": bson.M{"combined": "partial"}, "scoring": legacyScoring},
			bson.M{"id": "ins-2", "name": "sibling", "severity": "low", "risk_score": 0.12,
				"confidence": 0.91, "validation": bson.M{"combined": "supported"}, "scoring": legacyScoring},
		},
		"recommendations": bson.A{
			bson.M{"id": "rec-1", "title": "target", "priority": 2,
				"confidence": 0.90, "validation": bson.M{"combined": "partial"}, "scoring": legacyScoring},
			bson.M{"id": "rec-2", "title": "sibling", "priority": 4,
				"confidence": 0.88, "validation": bson.M{"combined": "supported"}, "scoring": legacyScoring},
		},
		"updated_at": before,
	}
	if _, err := db.Collection(CollectionDiscoveries).InsertOne(ctx, doc); err != nil {
		t.Fatalf("seeding the discovery: %v", err)
	}
	return oid.Hex(), before
}

func rescoredUpdate() DocVerdictUpdate {
	return DocVerdictUpdate{
		Validation: &valmodels.InsightValidation{
			Combined:    valmodels.StatusRejected,
			ValidatedAt: time.Now().UTC().Truncate(time.Millisecond),
		},
		Scoring: &gomodels.Scoring{
			ConfidenceBand:   gomodels.ConfidenceBandLow,
			ConfidenceBase:   0.05,
			ConfidenceScore:  0.05,
			ConfidenceReason: "Warehouse rows contradict a headline claim.",
			EngineVersion:    1,
		},
		Confidence: 0.05,
	}
}

func readDiscovery(t *testing.T, db *DB, idHex string) *models.DiscoveryResult {
	t.Helper()
	got, err := NewDiscoveryRepository(db).GetByID(context.Background(), idHex)
	if err != nil {
		t.Fatalf("reading the discovery back: %v", err)
	}
	if got == nil {
		t.Fatal("the discovery is gone")
	}
	return got
}

// TestUpdateInsightValidation_WritesTheVerdictAndItsDerivedConfidence is the
// contract the on-demand re-validation rests on: all three fields land
// together, on the right element, and the siblings are untouched.
//
// Together matters more than it looks. Confidence is a function of the verdict
// now, so a write that landed the verdict and not the score would leave the
// document claiming a confidence derived from a verdict it no longer has --
// internally contradictory, and nothing about it looks wrong to a reader.
func TestUpdateInsightValidation_WritesTheVerdictAndItsDerivedConfidence(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	idHex, before := seedDiscoveryWithTwoOfEach(t, db)
	repo := NewDiscoveryRepository(db)
	update := rescoredUpdate()

	if err := repo.UpdateInsightValidation(context.Background(), idHex, "ins-1", update); err != nil {
		t.Fatalf("UpdateInsightValidation: %v", err)
	}

	got := readDiscovery(t, db, idHex)
	if len(got.Insights) != 2 {
		t.Fatalf("the discovery has %d insights, want 2", len(got.Insights))
	}
	target, sibling := got.Insights[0], got.Insights[1]

	if target.Validation == nil || target.Validation.Combined != valmodels.StatusRejected {
		t.Errorf("the target's verdict did not land: %+v", target.Validation)
	}
	if target.Scoring == nil {
		t.Fatal("the target's scoring sub-document did not land")
	}
	if target.Scoring.ConfidenceBand != gomodels.ConfidenceBandLow || target.Scoring.ConfidenceScore != 0.05 {
		t.Errorf("the target's score did not land: %+v", target.Scoring)
	}
	if target.Scoring.ConfidenceReason == "" {
		t.Error("the target's reason did not land")
	}
	if target.Confidence != 0.05 {
		t.Errorf("the target's confidence = %v, want 0.05", target.Confidence)
	}
	// Severity and risk_score are the analysis model's own words and this
	// path does not rewrite them.
	if target.Severity != "critical" || target.RiskScore != 0.67 {
		t.Errorf("the write touched severity / risk_score: %q / %v", target.Severity, target.RiskScore)
	}
	if target.Name != "target" {
		t.Errorf("the write touched the prose: %q", target.Name)
	}

	// The sibling is byte-identical to how it was seeded.
	if sibling.Validation == nil || sibling.Validation.Combined != valmodels.StatusSupported {
		t.Errorf("the sibling's verdict was overwritten: %+v", sibling.Validation)
	}
	if sibling.Confidence != 0.91 {
		t.Errorf("the sibling's confidence was overwritten: %v", sibling.Confidence)
	}
	if sibling.Scoring == nil || sibling.Scoring.ConfidenceScore != 0.95 {
		t.Errorf("the sibling's score was overwritten: %+v", sibling.Scoring)
	}

	// And the recommendations are untouched by an insight write.
	if got.Recommendations[0].Confidence != 0.90 {
		t.Errorf("an insight write reached a recommendation: %v", got.Recommendations[0].Confidence)
	}

	if !got.UpdatedAt.After(before) {
		t.Errorf("updated_at = %v, want something after %v", got.UpdatedAt, before)
	}
}

// TestUpdateRecommendationValidation_WritesTheVerdictAndItsDerivedConfidence
// is the recommendation twin.
func TestUpdateRecommendationValidation_WritesTheVerdictAndItsDerivedConfidence(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	idHex, _ := seedDiscoveryWithTwoOfEach(t, db)
	repo := NewDiscoveryRepository(db)

	if err := repo.UpdateRecommendationValidation(context.Background(), idHex, "rec-1", rescoredUpdate()); err != nil {
		t.Fatalf("UpdateRecommendationValidation: %v", err)
	}

	got := readDiscovery(t, db, idHex)
	target, sibling := got.Recommendations[0], got.Recommendations[1]

	if target.Validation == nil || target.Validation.Combined != valmodels.StatusRejected {
		t.Errorf("the target's verdict did not land: %+v", target.Validation)
	}
	if target.Scoring == nil || target.Scoring.ConfidenceScore != 0.05 || target.Confidence != 0.05 {
		t.Errorf("the target's score did not land: %+v / %v", target.Scoring, target.Confidence)
	}
	if target.Priority != 2 || target.Title != "target" {
		t.Errorf("the write touched a field outside the confidence axis: %+v", target)
	}
	if sibling.Confidence != 0.88 || sibling.Validation.Combined != valmodels.StatusSupported {
		t.Errorf("the sibling was overwritten: %v / %+v", sibling.Confidence, sibling.Validation)
	}
	if got.Insights[0].Confidence != 0.93 {
		t.Errorf("a recommendation write reached an insight: %v", got.Insights[0].Confidence)
	}
}

// TestUpdateDocVerdict_ABadIDIsAnErrorNotASilentNoOp pins the predicate on the
// main filter.
//
// Without `insights.id` in it the UpdateOne still matches the discovery
// document, bumps updated_at, touches no array element and returns nil — a
// silent no-op on a wrong id, which would report a validation as persisted
// when nothing was written.
func TestUpdateDocVerdict_ABadIDIsAnErrorNotASilentNoOp(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	idHex, _ := seedDiscoveryWithTwoOfEach(t, db)
	repo := NewDiscoveryRepository(db)
	update := rescoredUpdate()
	ctx := context.Background()

	if err := repo.UpdateInsightValidation(ctx, idHex, "no-such-insight", update); err == nil {
		t.Error("a wrong insight id returned nil; the write would be reported as having landed")
	}
	if err := repo.UpdateRecommendationValidation(ctx, idHex, "no-such-rec", update); err == nil {
		t.Error("a wrong recommendation id returned nil")
	}
	// A discovery that does not exist, and an id that is not an ObjectId.
	missing := primitive.NewObjectID().Hex()
	if err := repo.UpdateInsightValidation(ctx, missing, "ins-1", update); err == nil {
		t.Error("a missing discovery returned nil")
	}
	if err := repo.UpdateInsightValidation(ctx, "not-an-objectid", "ins-1", update); err == nil {
		t.Error("a malformed discovery id returned nil")
	}

	// And nothing was written by any of them.
	got := readDiscovery(t, db, idHex)
	for i, ins := range got.Insights {
		if ins.Validation.Combined == valmodels.StatusRejected {
			t.Errorf("insights[%d] was written by a failed update", i)
		}
	}
	for i, rec := range got.Recommendations {
		if rec.Validation.Combined == valmodels.StatusRejected {
			t.Errorf("recommendations[%d] was written by a failed update", i)
		}
	}
}
