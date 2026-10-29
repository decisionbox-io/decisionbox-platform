//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// fatArea is an analysis area as the Phase 4 loop body leaves it: a large
// prompt, a response, insights carrying their validation verdicts and
// evidence-quality labels, validation results, and the picker telemetry.
// Everything a restored area has to still have.
func fatArea(areaID string) models.AnalysisStep {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return models.AnalysisStep{
		AreaID:            areaID,
		AreaName:          "Churn Risks",
		RunAt:             now,
		Prompt:            strings.Repeat("the rendered compacted steps ", 4000),
		RelevantQueries:   7,
		QueryResultsChars: 120_000,
		SelectedSteps: []models.SelectedStep{
			{Step: 3, Score: 0.81, Source: "vector"},
			{Step: 9, Score: 1.0, Source: "exact_match"},
		},
		DroppedSteps: []models.DroppedAnalysisStep{
			{Step: 12, Score: 0.11, Reason: "below_min_score"},
		},
		Response:             `{"insights":[]}`,
		TokensIn:             48_000,
		TokensOut:            3_100,
		DurationMs:           21_000,
		AnalysisParseRetries: 1,
		InsightsDroppedParse: 2,
		Insights: []models.Insight{
			{
				ID:            "insight-1",
				AnalysisArea:  areaID,
				Name:          "Cohort 7 churns at 3x",
				Severity:      "critical",
				AffectedCount: 412,
				SourceSteps:   []int{3, 9},
				DiscoveredAt:  now,
				Quality: []gowarehouse.QualityCaveat{
					{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
				},
				Validation: &valmodels.InsightValidation{
					Status:      string(valmodels.StatusConfirmed),
					Combined:    valmodels.StatusConfirmed,
					ValidatedAt: now,
				},
			},
		},
		ValidationResults: []models.ValidationResult{
			{
				InsightID:     "insight-1",
				AnalysisArea:  areaID,
				ClaimedMetric: "Cohort 7 churns at 3x",
				ClaimedCount:  412,
				VerifiedCount: 408,
				Status:        string(valmodels.StatusConfirmed),
				Combined:      valmodels.StatusConfirmed,
				ValidatedAt:   now,
				InputTokens:   9_000,
				OutputTokens:  700,
				DocKind:       valmodels.DocInsight,
			},
		},
	}
}

func analysisRepo(t *testing.T, db *DB, retention time.Duration) *DiscoveryAnalysisCheckpointRepository {
	t.Helper()
	return &DiscoveryAnalysisCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryAnalysisCheckpoints),
		retention: retention,
	}
}

// TestInteg_AnalysisCheckpoint_RoundTripKeepsEverythingTheLoopProduced is the
// contract restoring an area depends on: the orchestrator merges the stored
// AnalysisStep straight back into its analysisLog and allInsights, so
// anything lost here is lost from the run's result.
func TestInteg_AnalysisCheckpoint_RoundTripKeepsEverythingTheLoopProduced(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := analysisRepo(t, db, 48*time.Hour)

	in := fatArea("churn")
	if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID:          "p1",
		RunID:              "run-1",
		Attempt:            1,
		ExplorationAttempt: 1,
		Step:               in,
		InsightsValidated:  1,
	}); err != nil {
		t.Fatalf("SaveArea: %v", err)
	}

	set, err := repo.LoadAreas(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadAreas: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("restored %d areas, want 1", set.Len())
	}
	got, ok := set.Areas["churn"]
	if !ok {
		t.Fatal("the area must come back keyed by its own id")
	}
	if got.InsightsValidated != 1 {
		t.Errorf("InsightsValidated = %d, want 1 — the run's validation budget is restored from it", got.InsightsValidated)
	}
	if set.Attempt != 1 {
		t.Errorf("set attempt = %d, want 1", set.Attempt)
	}

	s := got.Step
	if s.AreaID != in.AreaID || s.AreaName != in.AreaName {
		t.Errorf("area identity lost: %q / %q", s.AreaID, s.AreaName)
	}
	if !s.RunAt.Equal(in.RunAt) {
		t.Errorf("RunAt = %v, want %v — the live-feed replay orders areas by it", s.RunAt, in.RunAt)
	}
	if s.Prompt != in.Prompt || s.Response != in.Response {
		t.Error("the prompt and response must survive: they are what the dashboard's analysis debug view shows for this area")
	}
	if s.TokensIn != in.TokensIn || s.TokensOut != in.TokensOut || s.DurationMs != in.DurationMs {
		t.Errorf("per-area metrics lost: %d in, %d out, %dms", s.TokensIn, s.TokensOut, s.DurationMs)
	}
	if s.RelevantQueries != in.RelevantQueries || s.QueryResultsChars != in.QueryResultsChars {
		t.Errorf("prompt sizing telemetry lost: %d queries, %d chars", s.RelevantQueries, s.QueryResultsChars)
	}
	if s.AnalysisParseRetries != 1 || s.InsightsDroppedParse != 2 {
		t.Errorf("parse telemetry lost: %d retries, %d dropped", s.AnalysisParseRetries, s.InsightsDroppedParse)
	}
	if len(s.SelectedSteps) != 2 || len(s.DroppedSteps) != 1 {
		t.Errorf("picker telemetry lost: %d selected, %d dropped", len(s.SelectedSteps), len(s.DroppedSteps))
	}

	if len(s.Insights) != 1 {
		t.Fatalf("got %d insights, want 1", len(s.Insights))
	}
	ins := s.Insights[0]
	// The id is load-bearing: Phase 5 resolves related_insight_ids against
	// it, so a restored insight that came back with a new id would silently
	// drop every recommendation citing it.
	if ins.ID != "insight-1" {
		t.Errorf("insight id = %q, want insight-1", ins.ID)
	}
	if len(ins.SourceSteps) != 2 || ins.SourceSteps[0] != 3 {
		t.Errorf("SourceSteps = %v, want [3 9] — the citations the verifier and the recommender resolve", ins.SourceSteps)
	}
	if ins.Validation == nil || ins.Validation.Combined != valmodels.StatusConfirmed {
		t.Errorf("the validation verdict must survive, got %+v", ins.Validation)
	}
	if len(ins.Quality) != 1 || ins.Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Errorf("the evidence-quality label must survive, got %+v — it is knowable nowhere else once the rows are gone", ins.Quality)
	}
	if len(s.ValidationResults) != 1 || s.ValidationResults[0].VerifiedCount != 408 {
		t.Errorf("validation results lost: %+v", s.ValidationResults)
	}
	if !s.ValidationResults[0].ValidatedAt.Equal(in.ValidationResults[0].ValidatedAt) {
		t.Error("ValidatedAt must survive — the replayed validation row carries it")
	}
}

// TestInteg_AnalysisCheckpoint_ResavingAnAreaReplacesItsRow is the issue's
// idempotency requirement at the layer that needs it. A resumed run that
// re-analyses an area a previous attempt persisted must upsert, not
// duplicate — and must not die on the unique index.
func TestInteg_AnalysisCheckpoint_ResavingAnAreaReplacesItsRow(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := analysisRepo(t, db, 48*time.Hour)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	first := fatArea("churn")
	first.TokensOut = 100
	second := fatArea("churn")
	second.TokensOut = 200

	for i, step := range []models.AnalysisStep{first, second, second} {
		if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
			ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
			Step: step, InsightsValidated: 1,
		}); err != nil {
			t.Fatalf("SaveArea %d: %v", i, err)
		}
	}

	n, err := repo.col.CountDocuments(ctx, bson.M{"run_id": "run-1", "area_id": "churn"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d rows for one area, want exactly 1 — the upsert must replace, not duplicate", n)
	}
	set, err := repo.LoadAreas(ctx, "run-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Areas["churn"].Step.TokensOut; got != 200 {
		t.Errorf("TokensOut = %d, want 200 — the latest write must win", got)
	}
}

// TestInteg_AnalysisCheckpoint_AttemptFence covers the state the API's
// startup sweep makes reachable: it marks in-flight runs `failed` without
// reaping their workloads, so a resumed run can have two agents on one run
// id. The older one must not overwrite the newer one's area, and must be
// TOLD rather than quietly refused — everything after analysis is expensive.
func TestInteg_AnalysisCheckpoint_AttemptFence(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := analysisRepo(t, db, 48*time.Hour)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	live := fatArea("churn")
	live.TokensOut = 222
	if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 2, ExplorationAttempt: 1,
		Step: live, InsightsValidated: 1,
	}); err != nil {
		t.Fatalf("live attempt SaveArea: %v", err)
	}

	orphan := fatArea("churn")
	orphan.TokensOut = 111
	err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
		Step: orphan, InsightsValidated: 1,
	})
	if !errors.Is(err, ErrSupersededAttempt) {
		t.Fatalf("orphan write error = %v, want ErrSupersededAttempt", err)
	}
	set, err := repo.LoadAreas(ctx, "run-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Areas["churn"].Step.TokensOut; got != 222 {
		t.Errorf("TokensOut = %d, want 222 — the orphan must not have overwritten the live attempt", got)
	}

	// The other direction is legitimate and must work: a resumed attempt
	// re-analysing an area an earlier attempt had done replaces its row.
	newer := fatArea("churn")
	newer.TokensOut = 333
	if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 3, ExplorationAttempt: 1,
		Step: newer, InsightsValidated: 1,
	}); err != nil {
		t.Fatalf("newer attempt SaveArea: %v", err)
	}
	set, err = repo.LoadAreas(ctx, "run-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Areas["churn"].Step.TokensOut; got != 333 {
		t.Errorf("TokensOut = %d, want 333 — a newer attempt may replace an older one's area", got)
	}
	if set.Attempt != 3 {
		t.Errorf("set attempt = %d, want 3", set.Attempt)
	}
}

// TestInteg_AnalysisCheckpoint_IgnoresAreasFromASupersededExploration is the
// correctness fence, built as the real sequence rather than a synthetic
// value.
//
// Attempt 1 explores, summarises and analyses an area. Its exploration is
// later superseded — a TTL expiry breaks the prefix, so attempt 2 re-explores
// and writes its own summary under a higher attempt. A third attempt reading
// a trustworthy summary over attempt 2's exploration must NOT restore
// attempt 1's area: that area's insights cite step numbers from an
// exploration that no longer exists.
func TestInteg_AnalysisCheckpoint_IgnoresAreasFromASupersededExploration(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := analysisRepo(t, db, 48*time.Hour)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Attempt 1, over attempt 1's exploration.
	if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1,
		Step: fatArea("churn"), InsightsValidated: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Attempt 2 re-explored, so its summary is attempt 2's. The area it
	// then analysed is valid against that exploration.
	if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 2, ExplorationAttempt: 2,
		Step: fatArea("engagement"), InsightsValidated: 2,
	}); err != nil {
		t.Fatal(err)
	}

	set, err := repo.LoadAreas(ctx, "run-1", 2)
	if err != nil {
		t.Fatalf("LoadAreas: %v", err)
	}
	if _, ok := set.Areas["churn"]; ok {
		t.Error("churn was analysed over a superseded exploration and must not be restored — its insights cite step numbers that no longer mean anything")
	}
	if _, ok := set.Areas["engagement"]; !ok {
		t.Error("engagement was analysed over THIS exploration and must be restored")
	}
	if set.Len() != 1 {
		t.Errorf("restored %d areas, want 1", set.Len())
	}

	// And the row is not destroyed, only ignored: it ages out on its own
	// TTL, so a transient misread cannot lose work.
	n, err := repo.col.CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d rows on file, want 2 — the fence ignores, it does not delete", n)
	}
}

// TestInteg_AnalysisCheckpoint_DeleteAndTouchAreRunScoped — delete must not
// reach another run's rows, and touch must move the retention anchor WITHOUT
// disturbing either attempt field. Restamping `attempt` would hand a restored
// area this attempt's number; restamping `exploration_attempt` would defeat
// the fence above outright.
func TestInteg_AnalysisCheckpoint_DeleteAndTouchAreRunScoped(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := analysisRepo(t, db, 48*time.Hour)

	for _, runID := range []string{"run-1", "run-2"} {
		if err := repo.SaveArea(ctx, AnalysisAreaCheckpointInput{
			ProjectID: "p1", RunID: runID, Attempt: 1, ExplorationAttempt: 1,
			Step: fatArea("churn"), InsightsValidated: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var before AnalysisAreaCheckpointDoc
	if err := repo.col.FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&before); err != nil {
		t.Fatal(err)
	}
	// Age the row so the touch is observable rather than a same-millisecond
	// no-op.
	aged := time.Now().Add(-24 * time.Hour)
	if _, err := repo.col.UpdateOne(ctx, bson.M{"run_id": "run-1"},
		bson.M{"$set": bson.M{"created_at": aged}}); err != nil {
		t.Fatal(err)
	}

	touched, err := repo.TouchByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("TouchByRun: %v", err)
	}
	if touched != 1 {
		t.Errorf("touched %d rows, want 1", touched)
	}
	var after AnalysisAreaCheckpointDoc
	if err := repo.col.FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if !after.CreatedAt.After(aged.Add(time.Hour)) {
		t.Errorf("created_at = %v, want it re-anchored near now", after.CreatedAt)
	}
	if after.Attempt != before.Attempt {
		t.Errorf("attempt moved from %d to %d — touch must set created_at and nothing else", before.Attempt, after.Attempt)
	}
	if after.ExplorationAttempt != before.ExplorationAttempt {
		t.Errorf("exploration_attempt moved from %d to %d — that would defeat the staleness fence",
			before.ExplorationAttempt, after.ExplorationAttempt)
	}

	deleted, err := repo.DeleteByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("DeleteByRun: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}
	n, err := repo.col.CountDocuments(ctx, bson.M{"run_id": "run-2"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("run-2 has %d rows, want 1 — delete must be scoped to one run", n)
	}
}

// TestInteg_AnalysisCheckpoint_EnsureIndexes covers both indexes and the one
// path that is easy to get wrong: a TTL's expireAfterSeconds cannot be
// mutated in place, so without the drop-and-recreate dance, raising
// DISCOVERY_CHECKPOINT_RETENTION would fail agent startup forever instead of
// taking effect on this collection.
func TestInteg_AnalysisCheckpoint_EnsureIndexes(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	indexes := func() map[string]struct {
		unique bool
		ttl    *int32
	} {
		t.Helper()
		cur, err := db.Collection(CollectionDiscoveryAnalysisCheckpoints).Indexes().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cur.Close(ctx)
		out := map[string]struct {
			unique bool
			ttl    *int32
		}{}
		for cur.Next(ctx) {
			var idx struct {
				Name               string `bson:"name"`
				Unique             bool   `bson:"unique"`
				ExpireAfterSeconds *int32 `bson:"expireAfterSeconds"`
			}
			if err := cur.Decode(&idx); err != nil {
				t.Fatal(err)
			}
			out[idx.Name] = struct {
				unique bool
				ttl    *int32
			}{idx.Unique, idx.ExpireAfterSeconds}
		}
		return out
	}

	first := analysisRepo(t, db, 48*time.Hour)
	if err := first.EnsureIndexes(ctx); err != nil {
		t.Fatalf("first EnsureIndexes: %v", err)
	}
	got := indexes()
	if !got["uq_run_area"].unique {
		t.Error("uq_run_area must be unique — it is what makes a re-save a replace rather than a duplicate")
	}
	ttl := got[analysisCheckpointTTLIndexName].ttl
	if ttl == nil || *ttl != int32((48*time.Hour).Seconds()) {
		t.Fatalf("TTL = %v, want 48h", ttl)
	}

	// Idempotent at the same value — this runs on every agent boot.
	if err := first.EnsureIndexes(ctx); err != nil {
		t.Fatalf("second EnsureIndexes at the same retention: %v", err)
	}

	// And a changed retention is applied rather than rejected.
	raised := analysisRepo(t, db, 192*time.Hour)
	if err := raised.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes at a raised retention: %v", err)
	}
	ttl = indexes()[analysisCheckpointTTLIndexName].ttl
	if ttl == nil || *ttl != int32((192*time.Hour).Seconds()) {
		t.Fatalf("TTL = %v, want 192h after the change", ttl)
	}
}

// TestInteg_AnalysisCheckpoint_SummaryAttemptIsReadableFromTheExplorationPrefix
// closes the loop between the two collections: the exploration summary's
// attempt is what the analysis rows are stamped and filtered on, so it has
// to survive LoadPrefix.
func TestInteg_AnalysisCheckpoint_SummaryAttemptIsReadableFromTheExplorationPrefix(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	cp := &DiscoveryCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryCheckpoints),
		retention: 48 * time.Hour,
	}
	if err := cp.SaveStep(ctx, CheckpointStepInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 2,
		Step: models.ExplorationStep{Step: 1, Action: "query_data", Query: "SELECT 1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cp.SaveExplorationSummary(ctx, CheckpointSummaryInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 2,
		Summary: models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1},
	}); err != nil {
		t.Fatal(err)
	}

	set, err := cp.LoadPrefix(ctx, "run-1")
	if err != nil {
		t.Fatalf("LoadPrefix: %v", err)
	}
	if set.Summary == nil {
		t.Fatal("the summary must be trusted: the prefix covers every step it claims")
	}
	if set.Summary.Attempt != 2 {
		t.Errorf("summary attempt = %d, want 2 — this is the value analysis checkpoints are fenced on", set.Summary.Attempt)
	}
}
