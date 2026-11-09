//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// fatDraft is an area as the Phase 4 loop body leaves it just before
// validation: a large prompt, insights with figures (one of them value-missing)
// and quality caveats, picker telemetry — and no validation results, because
// that is the work it has not done.
func fatDraft(areaID string) models.AnalysisStep {
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
		DroppedSteps:         []models.DroppedAnalysisStep{{Step: 12, Score: 0.11, Reason: "below_min_score"}},
		Response:             `{"insights":[]}`,
		TokensIn:             48_000,
		TokensOut:            3_100,
		DurationMs:           21_000,
		AnalysisParseRetries: 1,
		InsightsDroppedParse: 2,
		InsightsRepaired:     1,
		AnalysisRepairRounds: 2,
		FiguresCorrected:     1,
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
				Figures: []commonmodels.Figure{
					{ID: "f1", Value: 3.1},
					// The flag is bson:"-", so the row has to carry it
					// separately or Phase 5 loses it. See
					// AnalysisAreaDraftDoc.MissingValueFigures.
					{ID: "f2", ValueMissing: true},
				},
			},
			{
				ID:            "insight-2",
				AnalysisArea:  areaID,
				Name:          "Weekend signups convert worse",
				Severity:      "high",
				AffectedCount: 88,
				SourceSteps:   []int{3},
				DiscoveredAt:  now,
			},
		},
	}
}

func draftRepo(t *testing.T, db *DB, retention time.Duration) *DiscoveryAnalysisDraftRepository {
	t.Helper()
	return &DiscoveryAnalysisDraftRepository{
		col:       db.Collection(CollectionDiscoveryAnalysisDrafts),
		retention: retention,
	}
}

func verdictFor(id string) models.ValidationResult {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return models.ValidationResult{
		InsightID:     id,
		AnalysisArea:  "churn",
		ClaimedMetric: "a claim about " + id,
		ClaimedCount:  412,
		VerifiedCount: 408,
		Status:        string(valmodels.StatusConfirmed),
		Combined:      valmodels.StatusConfirmed,
		ValidatedAt:   now,
		DocKind:       valmodels.DocInsight,
		InputTokens:   9_000,
		OutputTokens:  700,
		Verifier: &valmodels.StructuredVerdict{
			DocID:         id,
			DocKind:       valmodels.DocInsight,
			Mode:          valmodels.ModeVerifier,
			Overall:       valmodels.StatusConfirmed,
			OverallReason: "the rows agree",
			LLMTokensIn:   9_000,
			LLMTokensOut:  700,
		},
	}
}

// TestInteg_AnalysisDraft_RoundTripKeepsWhatTheRestoreDependsOn — the
// orchestrator re-enters the Phase 4 loop with this step, so anything lost
// here is work the resumed run pays for again or, worse, ships without.
func TestInteg_AnalysisDraft_RoundTripKeepsWhatTheRestoreDependsOn(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	in := fatDraft("churn")
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: in,
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("restored %d drafts, want 1", set.Len())
	}
	got, ok := set.Drafts["churn"]
	if !ok {
		t.Fatal("the draft must come back keyed by its own area id")
	}
	s := got.Step
	if s.AreaID != in.AreaID || s.AreaName != in.AreaName || s.Prompt != in.Prompt || s.Response != in.Response {
		t.Error("lost part of the audit dialog")
	}
	if s.TokensIn != in.TokensIn || s.TokensOut != in.TokensOut || s.DurationMs != in.DurationMs {
		t.Error("lost the analysis call's accounting — the completed checkpoint reports it as the area's cost")
	}
	if s.InsightsRepaired != 1 || s.AnalysisRepairRounds != 2 || s.FiguresCorrected != 1 {
		t.Error("lost the repair telemetry, so a restored area would report its repairs as never having happened")
	}
	if len(s.SelectedSteps) != 2 || len(s.DroppedSteps) != 1 {
		t.Error("lost the picker telemetry")
	}
	if len(s.Insights) != 2 {
		t.Fatalf("restored %d insights, want 2 — they are the point of the row", len(s.Insights))
	}
	if s.Insights[0].ID != "insight-1" || s.Insights[1].ID != "insight-2" {
		t.Error("insight ids changed; this attempt's markers key on them")
	}
	if len(s.Insights[0].Quality) != 1 || s.Insights[0].Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Error("lost the quality caveats, which are knowable nowhere else")
	}
	if len(s.Insights[0].SourceSteps) != 2 {
		t.Error("lost the source steps, which the verifier resolves its evidence through")
	}

	// The bson:"-" flag, restored from its own field. Losing it makes
	// usableFigures offer the recommender a figure it must never see, which
	// then prints as a real zero.
	if len(s.Insights[0].Figures) != 2 {
		t.Fatalf("restored %d figures, want 2", len(s.Insights[0].Figures))
	}
	if s.Insights[0].Figures[0].ValueMissing {
		t.Error("f1 came back value-missing; the flag was applied to the wrong figure")
	}
	if !s.Insights[0].Figures[1].ValueMissing {
		t.Error("f2 lost its value-missing flag — a resumed run would print a fabricated measurement")
	}

	// A draft carries no markers until one is written.
	if len(got.InsightVerdicts) != 0 {
		t.Errorf("a fresh draft carries %d markers, want none", len(got.InsightVerdicts))
	}
}

// TestInteg_AnalysisDraft_MarkersAccumulateAcrossAttemptsOnOneImmutableDraft
// is the acceptance criterion's storage half: three attempts, each validating
// part of the same area, and the markers add up rather than replacing one
// another.
//
// It also pins that SaveDraft does NOT clear the markers — a resumed attempt
// restores a draft and does not re-save it, which is what lets a third attempt
// read every marker any attempt wrote.
func TestInteg_AnalysisDraft_MarkersAccumulateAcrossAttemptsOnOneImmutableDraft(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	step := fatDraft("churn")
	step.Insights = append(step.Insights, models.Insight{
		ID: "insight-3", AnalysisArea: "churn", Name: "third", AffectedCount: 5,
	})
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	// Attempt 1 validates one insight and dies. landed=true is the other half
	// of the pause contract: a verdict that IS durable has to say so, or the
	// run never stops at the finest seam it has.
	landed, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
		RunID: "run-1", AreaID: "churn", Attempt: 1, InsightID: "insight-1", Verdict: verdictFor("insight-1"),
	})
	if err != nil {
		t.Fatalf("SaveInsightVerdict: %v", err)
	}
	if !landed {
		t.Error("a marker that reached its draft reported landed=false; the per-insight pause seam would never fire")
	}
	// Attempt 2 restores the draft, validates a second, and dies.
	if _, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
		RunID: "run-1", AreaID: "churn", Attempt: 2, InsightID: "insight-2", Verdict: verdictFor("insight-2"),
	}); err != nil {
		t.Fatalf("SaveInsightVerdict: %v", err)
	}

	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	got := set.Drafts["churn"]
	if len(got.InsightVerdicts) != 2 {
		t.Fatalf("%d markers, want 2 — attempt 3 must validate only the third insight", len(got.InsightVerdicts))
	}
	for _, id := range []string{"insight-1", "insight-2"} {
		vr, ok := got.InsightVerdicts[id]
		if !ok {
			t.Fatalf("no marker for %s", id)
		}
		if vr.Combined != valmodels.StatusConfirmed || vr.InputTokens != 9_000 {
			t.Errorf("%s came back hollow: %+v", id, vr)
		}
		if vr.Verifier == nil || vr.Verifier.OverallReason != "the rows agree" {
			t.Errorf("%s lost its structured verdict, so the restore could not render its Validation", id)
		}
	}
	if _, ok := got.InsightVerdicts["insight-3"]; ok {
		t.Error("a marker exists for an insight nobody validated")
	}

	// Exactly one row throughout.
	n, err := db.Collection(CollectionDiscoveryAnalysisDrafts).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d draft rows for one area, want 1", n)
	}
}

// TestInteg_AnalysisDraft_AttemptFenceInBothDirections — the API's startup
// sweep marks in-flight runs failed WITHOUT reaping their workloads, so an
// operator resuming such a run can have two agents on one run id.
func TestInteg_AnalysisDraft_AttemptFenceInBothDirections(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)
	// The fence is the unique index turning a filter that matched nothing
	// into a duplicate-key refusal, so the index has to exist.
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	newer := fatDraft("churn")
	newer.Response = `{"from":"attempt-3"}`
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 3, ExplorationAttempt: 1, Step: newer,
	}); err != nil {
		t.Fatalf("SaveDraft(attempt 3): %v", err)
	}

	// An orphaned earlier attempt must not overwrite it, and must be TOLD.
	older := fatDraft("churn")
	older.Response = `{"from":"attempt-1"}`
	err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: older,
	})
	if !errors.Is(err, ErrSupersededAttempt) {
		t.Fatalf("want ErrSupersededAttempt, got %v", err)
	}
	set, _ := repo.LoadDrafts(ctx, "run-1", 1)
	if set.Drafts["churn"].Step.Response != `{"from":"attempt-3"}` {
		t.Error("an orphaned attempt overwrote a newer one's draft")
	}

	// A marker from the orphan must not land either — and must SAY it did
	// not. The bool is what the cooperative pause reads to decide whether
	// stopping is free, so a fenced no-op reporting `true` would stop a run
	// on a verdict no draft is holding: the resumed attempt would re-validate
	// it, which is the work the pause was supposed to preserve. Asserted
	// against a real MongoDB because MatchedCount is the server's answer, not
	// the driver's.
	landed, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
		RunID: "run-1", AreaID: "churn", Attempt: 1, InsightID: "insight-1", Verdict: verdictFor("insight-1"),
	})
	if err != nil {
		t.Fatalf("SaveInsightVerdict must not error on a fenced no-op, got %v", err)
	}
	if landed {
		t.Error("a fenced no-op reported landed=true; a pause taken on it would stop the run on a verdict nothing stored")
	}
	set, _ = repo.LoadDrafts(ctx, "run-1", 1)
	if len(set.Drafts["churn"].InsightVerdicts) != 0 {
		t.Error("an orphaned attempt wrote a marker into a newer attempt's draft")
	}

	// And a LATER attempt legitimately replaces the draft.
	latest := fatDraft("churn")
	latest.Response = `{"from":"attempt-4"}`
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 4, ExplorationAttempt: 1, Step: latest,
	}); err != nil {
		t.Fatalf("SaveDraft(attempt 4): %v", err)
	}
	set, _ = repo.LoadDrafts(ctx, "run-1", 1)
	if set.Drafts["churn"].Step.Response != `{"from":"attempt-4"}` {
		t.Error("a later attempt could not replace an earlier draft")
	}
	if set.Attempt != 4 {
		t.Errorf("set attempt = %d, want 4", set.Attempt)
	}
}

// TestInteg_AnalysisDraft_StalenessFenceAsTheRealTTLSequence — the reachable
// three-attempt shape: attempt 1 explores and drafts, its early step rows TTL
// out, attempt 2 reads a prefix with a hole and re-explores to a DIFFERENT
// exploration, and attempt 3 must not restore attempt 1's draft into it,
// because its insights cite step numbers that now mean something else.
func TestInteg_AnalysisDraft_StalenessFenceAsTheRealTTLSequence(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: fatDraft("churn"),
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	// Attempt 3, working over attempt 2's exploration.
	set, err := repo.LoadDrafts(ctx, "run-1", 2)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if set.Len() != 0 {
		t.Error("restored a draft analysed over a DIFFERENT exploration; its evidence citations would name the wrong queries")
	}

	// Ignored, not deleted — a transient misread must not lose paid-for work.
	n, err := db.Collection(CollectionDiscoveryAnalysisDrafts).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("the stale row was deleted (%d left); it must be ignored so a later attempt can still use it", n)
	}

	// And over the matching exploration it comes back.
	if set, _ = repo.LoadDrafts(ctx, "run-1", 1); set.Len() != 1 {
		t.Error("refused a draft analysed over the exploration this attempt is using")
	}
}

// TestInteg_AnalysisDraft_IgnoresRowsNoRestoreCouldUse — a row with no
// insights protects nothing, and restoring it would hand the loop an empty
// area instead of letting it analyse the area again. SaveDraft's caller
// refuses to write one, so this is the corrupted-document case.
func TestInteg_AnalysisDraft_IgnoresRowsNoRestoreCouldUse(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	empty := fatDraft("churn")
	empty.Insights = nil
	if _, err := db.Collection(CollectionDiscoveryAnalysisDrafts).InsertOne(ctx, AnalysisAreaDraftDoc{
		RunID: "run-1", ProjectID: "p1", Attempt: 1, ExplorationAttempt: 1,
		CreatedAt: time.Now(), AnalysisStep: empty,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if set.Len() != 0 {
		t.Error("restored a draft with no insights; the area would come back empty rather than being analysed again")
	}
}

// TestInteg_AnalysisDraft_DeleteByAreaAndByRun — the per-area delete is the
// tidy-up when an area completes; the per-run delete is the terminal purge.
func TestInteg_AnalysisDraft_DeleteByAreaAndByRun(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	for _, area := range []string{"churn", "revenue", "retention"} {
		if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
			ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: fatDraft(area),
		}); err != nil {
			t.Fatalf("SaveDraft(%s): %v", area, err)
		}
	}
	// Another run's rows must survive both deletes.
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-2", Attempt: 1, ExplorationAttempt: 1, Step: fatDraft("churn"),
	}); err != nil {
		t.Fatalf("SaveDraft(run-2): %v", err)
	}

	if err := repo.DeleteByArea(ctx, "run-1", "churn"); err != nil {
		t.Fatalf("DeleteByArea: %v", err)
	}
	set, _ := repo.LoadDrafts(ctx, "run-1", 1)
	if _, ok := set.Drafts["churn"]; ok {
		t.Error("DeleteByArea left the area's draft behind")
	}
	if set.Len() != 2 {
		t.Errorf("DeleteByArea took %d other areas with it", 3-1-set.Len())
	}
	// Idempotent: the orchestrator calls it per completed area, and a crash
	// between the two writes means it may be called twice.
	if err := repo.DeleteByArea(ctx, "run-1", "churn"); err != nil {
		t.Errorf("DeleteByArea is not idempotent: %v", err)
	}

	deleted, err := repo.DeleteByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("DeleteByRun: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d rows, want 2", deleted)
	}
	if set, _ = repo.LoadDrafts(ctx, "run-2", 1); set.Len() != 1 {
		t.Error("DeleteByRun took another run's rows")
	}
}

// TestInteg_AnalysisDraft_TouchByRunMovesOnlyCreatedAt — touching `attempt`
// would hand a restored draft this attempt's number, and touching
// `exploration_attempt` would defeat the staleness fence outright.
func TestInteg_AnalysisDraft_TouchByRunMovesOnlyCreatedAt(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 2, ExplorationAttempt: 1, Step: fatDraft("churn"),
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	var before AnalysisAreaDraftDoc
	if err := db.Collection(CollectionDiscoveryAnalysisDrafts).
		FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&before); err != nil {
		t.Fatalf("read back: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	touched, err := repo.TouchByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("TouchByRun: %v", err)
	}
	if touched != 1 {
		t.Errorf("touched %d rows, want 1", touched)
	}

	var after AnalysisAreaDraftDoc
	if err := db.Collection(CollectionDiscoveryAnalysisDrafts).
		FindOne(ctx, bson.M{"run_id": "run-1"}).Decode(&after); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !after.CreatedAt.After(before.CreatedAt) {
		t.Error("created_at did not move, so the retention clock still runs from the attempt that wrote the row")
	}
	if after.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 — touching it would hand the draft this attempt's number", after.Attempt)
	}
	if after.ExplorationAttempt != 1 {
		t.Errorf("exploration_attempt = %d, want 1 — touching it would defeat the staleness fence", after.ExplorationAttempt)
	}
	if after.Response != before.Response {
		t.Error("TouchByRun rewrote the step")
	}
}

// TestInteg_AnalysisDraft_EnsureIndexesIncludingTheTTLRecreate — without the
// drop-and-recreate dance, changing DISCOVERY_CHECKPOINT_RETENTION would fail
// agent startup forever instead of taking effect.
func TestInteg_AnalysisDraft_EnsureIndexesIncludingTheTTLRecreate(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	if err := draftRepo(t, db, 48*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	// Idempotent.
	if err := draftRepo(t, db, 48*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes twice: %v", err)
	}
	// And a changed retention is applied rather than refused.
	if err := draftRepo(t, db, 96*time.Hour).EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes at a new retention: %v", err)
	}

	cur, err := db.Collection(CollectionDiscoveryAnalysisDrafts).Indexes().List(ctx)
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
		case analysisDraftTTLIndexName:
			if v, ok := idx["expireAfterSeconds"].(int32); ok {
				ttlSeconds = v
			}
		case "uq_run_area":
			unique, _ = idx["unique"].(bool)
		}
	}
	if !unique {
		t.Error("uq_run_area is not unique — two rows for one area would both be restorable")
	}
	if want := int32((96 * time.Hour).Seconds()); ttlSeconds != want {
		t.Errorf("TTL expireAfterSeconds = %d, want %d", ttlSeconds, want)
	}
}

// TestInteg_AnalysisDraft_ConcurrentMarkerWritesAllLand is the correctness
// claim behind storing markers as a map of fields rather than re-writing the
// row: the verdicts are produced by validationPhase's bounded workers, and two
// of them replacing the whole document would lose one of the two.
func TestInteg_AnalysisDraft_ConcurrentMarkerWritesAllLand(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	step := fatDraft("churn")
	ids := []string{"insight-1", "insight-2"}
	for i := 0; i < 8; i++ {
		id := "extra-" + string(rune('a'+i))
		ids = append(ids, id)
		step.Insights = append(step.Insights, models.Insight{ID: id, AnalysisArea: "churn", Name: id})
	}
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
				RunID: "run-1", AreaID: "churn", Attempt: 1, InsightID: id, Verdict: verdictFor(id),
			}); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SaveInsightVerdict: %v", err)
	}

	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	got := set.Drafts["churn"].InsightVerdicts
	if len(got) != len(ids) {
		t.Fatalf("%d markers landed out of %d concurrent writes — a verdict was lost", len(got), len(ids))
	}
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			t.Errorf("no marker for %s", id)
		}
	}
}

// TestInteg_AnalysisDraft_ADottedIdNeitherNestsNorCorruptsTheBudget is the
// end-to-end form of the regression, against real MongoDB.
//
// A model-supplied id containing a dot used to be concatenated straight into
// the `$set` path, so Mongo created a nested field instead of a marker. The
// row then came back carrying a zero-valued marker under the prefix: the
// budget charged it while the restore could not match it, which at the cap
// turns an already-verified insight into one skipped for budget.
//
// It is refused at the DRAFT write now, which is earlier and stronger — no row
// exists whose markers could be partial — and refused again at the marker
// write, which is what the unit tests cover.
func TestInteg_AnalysisDraft_ADottedIdNeitherNestsNorCorruptsTheBudget(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	step := fatDraft("churn")
	step.Insights[0].ID = "finding.1"
	err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	})
	if !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Fatalf("want ErrUnsafeMarkerKey, got %v", err)
	}
	n, err := db.Collection(CollectionDiscoveryAnalysisDrafts).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d rows written for a refused area, want none", n)
	}

	// A marker write for such an id is refused too, so a row that somehow
	// carried one could not gain a nested field either.
	safe := fatDraft("revenue")
	if err := repo.SaveDraft(ctx, AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: safe,
	}); err != nil {
		t.Fatalf("SaveDraft(safe): %v", err)
	}
	if _, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
		RunID: "run-1", AreaID: "revenue", Attempt: 1, InsightID: "finding.1",
		Verdict: verdictFor("finding.1"),
	}); !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Fatalf("want ErrUnsafeMarkerKey from the marker write, got %v", err)
	}
	var raw bson.M
	if err := db.Collection(CollectionDiscoveryAnalysisDrafts).
		FindOne(ctx, bson.M{"run_id": "run-1", "area_id": "revenue"}).Decode(&raw); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, nested := raw["insight_verdicts"]; nested {
		t.Error("a nested insight_verdicts field was created on the document")
	}

	// And a safe id still works.
	if _, err := repo.SaveInsightVerdict(ctx, InsightVerdictInput{
		RunID: "run-1", AreaID: "revenue", Attempt: 1, InsightID: "insight-2",
		Verdict: verdictFor("insight-2"),
	}); err != nil {
		t.Fatalf("SaveInsightVerdict for a safe id: %v", err)
	}
	set, _ := repo.LoadDrafts(ctx, "run-1", 1)
	if len(set.Drafts["revenue"].InsightVerdicts) != 1 {
		t.Error("the safe marker did not land")
	}
}

// TestInteg_AnalysisDraft_IgnoresAMarkerNamingNothingInTheDraft — the load-side
// guard, written straight into the document so it does not depend on the write
// path having a hole in it.
func TestInteg_AnalysisDraft_IgnoresAMarkerNamingNothingInTheDraft(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	if _, err := db.Collection(CollectionDiscoveryAnalysisDrafts).InsertOne(ctx, AnalysisAreaDraftDoc{
		RunID: "run-1", ProjectID: "p1", Attempt: 1, ExplorationAttempt: 1,
		CreatedAt: time.Now(), AnalysisStep: fatDraft("churn"),
		InsightVerdicts: map[string]models.ValidationResult{
			"insight-1": verdictFor("insight-1"),
			"ghost":     verdictFor("ghost"),
		},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	got := set.Drafts["churn"].InsightVerdicts
	if len(got) != 1 {
		t.Fatalf("kept %d markers, want 1 — a marker no insight can claim would charge the cap for nothing", len(got))
	}
	if _, ok := got["insight-1"]; !ok {
		t.Error("dropped the marker for an insight the draft carries")
	}
}

// TestInteg_AnalysisDraft_IgnoresADraftWhoseInsightsShareAnId is the load-side
// half of the validation-bypass guard, written straight into the document so
// it does not rest on the writer refusing it.
//
// One marker restored onto two findings means the second skips the verifier
// and can carry a verdict an unrelated finding earned — the one failure in
// this family that is a bypass rather than a cost, which is why it is checked
// on both sides.
func TestInteg_AnalysisDraft_IgnoresADraftWhoseInsightsShareAnId(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := draftRepo(t, db, 48*time.Hour)

	step := fatDraft("churn")
	step.Insights[1].ID = step.Insights[0].ID
	if _, err := db.Collection(CollectionDiscoveryAnalysisDrafts).InsertOne(ctx, AnalysisAreaDraftDoc{
		RunID: "run-1", ProjectID: "p1", Attempt: 1, ExplorationAttempt: 1,
		CreatedAt: time.Now(), AnalysisStep: step,
		InsightVerdicts: map[string]models.ValidationResult{
			step.Insights[0].ID: verdictFor(step.Insights[0].ID),
		},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	set, err := repo.LoadDrafts(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if set.Len() != 0 {
		t.Error("restored a draft whose marker cannot say which of two findings it describes")
	}

	// Ignored, not deleted, like every other refused row.
	n, err := db.Collection(CollectionDiscoveryAnalysisDrafts).CountDocuments(ctx, bson.M{"run_id": "run-1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Error("the refused row was deleted")
	}
}
