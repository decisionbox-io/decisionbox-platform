//go:build integration

package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// What this file covers, against a real MongoDB: the two places a derived
// grade has to survive a round trip, and the one place the embedded and
// standalone copies of it have to agree.
//
// Neither is reachable by a mock. The standalone row is written through the
// driver and read back through a different struct, so the only thing that
// establishes the sub-document arrives intact is a server in between; and a
// checkpoint restore goes through BSON twice, where a missing field is a nil
// pointer rather than an error.

func scoringPersistMongo(t *testing.T) (*database.DB, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo container: %v", err)
	}
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mongo connection string: %v", err)
	}
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "scoring_persist_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	return database.New(client), func() {
		_ = client.Disconnect(ctx)
		_ = testcontainers.TerminateContainer(container)
	}
}

// TestInteg_UpdateDocVerdict_KeepsTheStandaloneRowConsistentWithTheEmbeddedOne
// is the acceptance criterion for the on-demand re-score, at the write.
//
// The `insights` collection is what the insights list page and Ask read, and
// it holds its OWN copy of the verdict and the confidence derived from it. If
// the re-validation wrote only the discovery document, that row would show a
// reader a confidence derived from the verdict the document used to have,
// beside a validation block saying something else — and nothing about such a
// document looks wrong.
func TestInteg_UpdateDocVerdict_KeepsTheStandaloneRowConsistentWithTheEmbeddedOne(t *testing.T) {
	db, cleanup := scoringPersistMongo(t)
	defer cleanup()
	ctx := context.Background()
	store := NewMongoEmbedIndexStore(db)

	// As Phase 9 writes it: supported, scored high.
	seeded := &commonmodels.StandaloneInsight{
		ID: "ins-1", ProjectID: "proj-1", DiscoveryID: "disc-1",
		Name: "Day 1 drop", Severity: "critical", RiskScore: 0.67,
		Confidence: 0.80,
		Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSupported},
		Scoring: &commonmodels.Scoring{
			ConfidenceBand:  commonmodels.ConfidenceBandHigh,
			ConfidenceBase:  0.80,
			ConfidenceScore: 0.80,
			EngineVersion:   1,
		},
		CreatedAt: time.Now().UTC(),
	}
	sibling := &commonmodels.StandaloneInsight{
		ID: "ins-2", ProjectID: "proj-1", DiscoveryID: "disc-1",
		Name: "untouched", Confidence: 0.95,
		Validation: &valmodels.InsightValidation{Combined: valmodels.StatusConfirmed},
		Scoring: &commonmodels.Scoring{
			ConfidenceBand: commonmodels.ConfidenceBandHigh, ConfidenceScore: 0.95, EngineVersion: 1,
		},
	}
	if err := store.InsertInsights(ctx, []*commonmodels.StandaloneInsight{seeded, sibling}); err != nil {
		t.Fatalf("seeding the standalone rows: %v", err)
	}

	// The operator clicks Run validation; the verdict comes back rejected and
	// the scorer re-derives the confidence axis from it.
	target := models.Insight{
		ID: "ins-1", Name: "Day 1 drop", Severity: "critical", RiskScore: 0.67,
		Confidence: 0.80,
		Validation: &models.InsightValidation{Combined: valmodels.StatusRejected},
	}
	rescoreTargetInsight(&target, testScoringCfg())
	if target.Scoring == nil || target.Confidence != 0.05 {
		t.Fatalf("the re-score did not produce the rejected grade: %+v / %v", target.Scoring, target.Confidence)
	}

	update := database.DocVerdictUpdate{
		Validation: target.Validation,
		Scoring:    target.Scoring,
		Confidence: target.Confidence,
	}
	if err := store.UpdateDocVerdict(ctx, "insights", "ins-1", update); err != nil {
		t.Fatalf("UpdateDocVerdict: %v", err)
	}

	var got commonmodels.StandaloneInsight
	if err := db.Collection("insights").FindOne(ctx, bson.M{"_id": "ins-1"}).Decode(&got); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if got.Validation == nil || got.Validation.Combined != valmodels.StatusRejected {
		t.Errorf("the verdict did not land: %+v", got.Validation)
	}
	if got.Scoring == nil {
		t.Fatal("the scoring sub-document did not land")
	}
	if got.Scoring.ConfidenceBand != commonmodels.ConfidenceBandLow || got.Scoring.ConfidenceScore != 0.05 {
		t.Errorf("the score did not land: %+v", got.Scoring)
	}
	if got.Scoring.ConfidenceReason == "" {
		t.Error("the reason did not land; the dashboard renders the engine's words")
	}
	if got.Confidence != 0.05 {
		t.Errorf("confidence = %v, want 0.05", got.Confidence)
	}
	// The two copies of the one fact agree, which is the whole point.
	if got.Confidence != got.Scoring.ConfidenceScore {
		t.Errorf("the row's confidence (%v) and its scored confidence (%v) disagree", got.Confidence, got.Scoring.ConfidenceScore)
	}
	// Severity and risk_score are the analysis model's own words; this path
	// does not touch them, and neither does it touch the prose.
	if got.Severity != "critical" || got.RiskScore != 0.67 || got.Name != "Day 1 drop" {
		t.Errorf("the write reached a field outside the confidence axis: %+v", got)
	}

	var untouched commonmodels.StandaloneInsight
	if err := db.Collection("insights").FindOne(ctx, bson.M{"_id": "ins-2"}).Decode(&untouched); err != nil {
		t.Fatalf("reading the sibling back: %v", err)
	}
	if untouched.Confidence != 0.95 || untouched.Validation.Combined != valmodels.StatusConfirmed {
		t.Errorf("the sibling row was written: %v / %+v", untouched.Confidence, untouched.Validation)
	}
}

// TestInteg_UpdateDocVerdict_AMissingRowIsNotAnError — a discovery whose
// standalone documents were never written, or have since been deleted.
//
// The discovery document is the authoritative copy and its write has already
// landed by this point, so failing here would report a completed validation as
// failed.
func TestInteg_UpdateDocVerdict_AMissingRowIsNotAnError(t *testing.T) {
	db, cleanup := scoringPersistMongo(t)
	defer cleanup()

	store := NewMongoEmbedIndexStore(db)
	err := store.UpdateDocVerdict(context.Background(), "insights", "no-such-row", database.DocVerdictUpdate{
		Validation: &valmodels.InsightValidation{Combined: valmodels.StatusRejected},
		Scoring:    &commonmodels.Scoring{ConfidenceScore: 0.05, EngineVersion: 1},
		Confidence: 0.05,
	})
	if err != nil {
		t.Errorf("a missing standalone row returned %v, want nil", err)
	}
}

// TestInteg_UpdateDocVerdict_TheRecommendationCollectionToo — the same write
// against the other collection the dashboard reads directly.
func TestInteg_UpdateDocVerdict_TheRecommendationCollectionToo(t *testing.T) {
	db, cleanup := scoringPersistMongo(t)
	defer cleanup()
	ctx := context.Background()
	store := NewMongoEmbedIndexStore(db)

	if err := store.InsertRecommendations(ctx, []*commonmodels.StandaloneRecommendation{{
		ID: "rec-1", ProjectID: "proj-1", DiscoveryID: "disc-1",
		Title: "Add retry mechanics", Priority: 2, Confidence: 0.80,
		Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSupported},
		Scoring: &commonmodels.Scoring{
			ConfidenceBand: commonmodels.ConfidenceBandHigh, ConfidenceScore: 0.80, EngineVersion: 1,
		},
	}}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	target := models.Recommendation{
		ID: "rec-1", Title: "Add retry mechanics", Priority: 2, Confidence: 0.80,
		Validation: &models.InsightValidation{Combined: valmodels.StatusUnverifiable},
	}
	rescoreTargetRecommendation(&target, testScoringCfg())

	if err := store.UpdateDocVerdict(ctx, "recommendations", "rec-1", database.DocVerdictUpdate{
		Validation: target.Validation, Scoring: target.Scoring, Confidence: target.Confidence,
	}); err != nil {
		t.Fatalf("UpdateDocVerdict: %v", err)
	}

	var got commonmodels.StandaloneRecommendation
	if err := db.Collection("recommendations").FindOne(ctx, bson.M{"_id": "rec-1"}).Decode(&got); err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if got.Scoring == nil || got.Scoring.ConfidenceBand != commonmodels.ConfidenceBandUnverifiable {
		t.Errorf("band = %+v, want unverifiable as its own state", got.Scoring)
	}
	if got.Confidence != 0.20 {
		t.Errorf("confidence = %v, want 0.2", got.Confidence)
	}
	if got.Priority != 2 || got.Title != "Add retry mechanics" {
		t.Errorf("the write reached a field outside the confidence axis: %+v", got)
	}
}

// TestInteg_AScoredAreaSurvivesTheCheckpointRoundTrip is the resume half.
//
// The scorer runs before the area checkpoint is written, so the grade rides
// inside the checkpoint's own analysis step and no checkpoint schema changed.
// That only holds if the sub-document survives BSON in both directions — and a
// field that does not comes back as a nil pointer rather than an error, which
// the dashboard would render as a legacy document on every resumed run.
func TestInteg_AScoredAreaSurvivesTheCheckpointRoundTrip(t *testing.T) {
	db, cleanup := scoringPersistMongo(t)
	defer cleanup()
	ctx := context.Background()
	repo := database.NewDiscoveryAnalysisCheckpointRepository(db)

	step := completedArea("churn", 2, time.Now().Add(-time.Hour))
	// Two different grades, so a restore that returned one for both would
	// fail rather than pass by coincidence.
	step.Insights[0].Validation = &models.InsightValidation{Combined: valmodels.StatusSupported}
	step.Insights[0].FigureVerdicts = []commonmodels.FigureVerdict{{ID: "f1", Status: commonmodels.FigureFails}}
	step.Insights[1].Validation = &models.InsightValidation{Combined: valmodels.StatusUnverifiable}
	scoreInsights(step.Insights, testScoringCfg(), currencyPlan{}, nil)

	want := make([]commonmodels.Scoring, 0, len(step.Insights))
	for i := range step.Insights {
		if step.Insights[i].Scoring == nil {
			t.Fatalf("insight %d was not scored before the checkpoint", i)
		}
		want = append(want, *step.Insights[i].Scoring)
	}
	if want[0].ConfidenceBand == want[1].ConfidenceBand {
		t.Fatalf("both fixtures scored to %q; this test would pass by coincidence", want[0].ConfidenceBand)
	}

	if err := repo.SaveArea(ctx, database.AnalysisAreaCheckpointInput{
		ProjectID: "proj", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
		Step: step, InsightsValidated: len(step.Insights),
	}); err != nil {
		t.Fatalf("SaveArea: %v", err)
	}

	set, err := repo.LoadAreas(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadAreas: %v", err)
	}
	cp, ok := set.Areas["churn"]
	if !ok {
		t.Fatal("the area did not come back")
	}
	_, restored, _ := restoreArea(cp, nil, nil)
	if len(restored) != 2 {
		t.Fatalf("restored %d insights, want 2", len(restored))
	}
	for i := range restored {
		if restored[i].Scoring == nil {
			t.Fatalf("restored insight %d came back unscored, so every resumed run would render it as legacy", i)
		}
		if restored[i].Scoring.ConfidenceBand != want[i].ConfidenceBand {
			t.Errorf("restored insight %d band = %q, want %q", i, restored[i].Scoring.ConfidenceBand, want[i].ConfidenceBand)
		}
		if restored[i].Scoring.ConfidenceScore != want[i].ConfidenceScore {
			t.Errorf("restored insight %d score = %v, want %v", i, restored[i].Scoring.ConfidenceScore, want[i].ConfidenceScore)
		}
		if restored[i].Scoring.ConfidenceReason != want[i].ConfidenceReason {
			t.Errorf("restored insight %d lost its reason: %q", i, restored[i].Scoring.ConfidenceReason)
		}
		if len(restored[i].Scoring.ConfidenceDocks) != len(want[i].ConfidenceDocks) {
			t.Errorf("restored insight %d has %d docks, want %d",
				i, len(restored[i].Scoring.ConfidenceDocks), len(want[i].ConfidenceDocks))
		}
		for j := range want[i].ConfidenceDocks {
			if restored[i].Scoring.ConfidenceDocks[j] != want[i].ConfidenceDocks[j] {
				t.Errorf("restored insight %d dock %d = %+v, want %+v",
					i, j, restored[i].Scoring.ConfidenceDocks[j], want[i].ConfidenceDocks[j])
			}
		}
		if restored[i].Confidence != want[i].ConfidenceScore {
			t.Errorf("restored insight %d confidence = %v, want %v", i, restored[i].Confidence, want[i].ConfidenceScore)
		}
	}
}
