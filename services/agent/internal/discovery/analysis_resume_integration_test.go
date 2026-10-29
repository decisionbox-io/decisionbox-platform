//go:build integration

package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// Per-area analysis resume against a real MongoDB.
//
// The seam these tests exercise is the whole of the feature's behaviour that
// is reachable without an LLM and a warehouse: the orchestrator's own
// checkpoint and restore methods, running against the real repository and the
// real indexes, with the real attempt and staleness fences in play. What they
// deliberately do NOT do is drive RunDiscovery — nothing in this repository
// does, because it needs a warehouse provider, an LLM client, a schema cache
// and a prompt set. So the acceptance criteria are proven here at the level of
// "what does the loop get handed, and what does it merge", plus the pure
// restoreArea arithmetic in the unit tests.

func analysisResumeMongo(t *testing.T) (*database.DB, func()) {
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
	cfg.Database = "analysis_resume_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	return database.New(client), func() {
		_ = client.Disconnect(ctx)
		_ = testcontainers.TerminateContainer(container)
	}
}

// completedArea builds an area as the Phase 4 loop leaves it on success.
func completedArea(id string, insights int, at time.Time) models.AnalysisStep {
	step := models.AnalysisStep{
		AreaID:    id,
		AreaName:  "Area " + id,
		RunAt:     at,
		Prompt:    "prompt for " + id,
		Response:  `{"insights":[]}`,
		TokensIn:  5000,
		TokensOut: 400,
	}
	for i := 0; i < insights; i++ {
		step.Insights = append(step.Insights, models.Insight{
			ID:           id + "-insight",
			AnalysisArea: id,
			Name:         "Finding in " + id,
			Severity:     "high",
			SourceSteps:  []int{1, 2},
			DiscoveredAt: at,
		})
	}
	return step
}

// resumedOrchestrator is an orchestrator as a resumed run arrives at Phase 4:
// exploration already complete from explorationAttempt, and this attempt's
// own number on the clock.
func resumedOrchestrator(
	t *testing.T,
	db *database.DB,
	runID string,
	attempt, explorationAttempt, exploredSteps int,
) (*Orchestrator, *database.DiscoveryAnalysisCheckpointRepository) {
	t.Helper()
	repo := database.NewDiscoveryAnalysisCheckpointRepository(db)
	steps := make([]models.ExplorationCheckpoint, 0, exploredSteps)
	for i := 1; i <= exploredSteps; i++ {
		steps = append(steps, models.ExplorationCheckpoint{
			Step: models.ExplorationStep{Step: i, Action: "query_data", Query: "SELECT 1"},
		})
	}
	o := &Orchestrator{
		projectID:                 "proj",
		runID:                     runID,
		analysisCheckpointRepo:    repo,
		explorationSummaryAttempt: explorationAttempt,
		resume: &ResumeState{
			Attempt: attempt,
			Checkpoints: &database.CheckpointSet{
				Steps: steps,
				Summary: &models.ExplorationCheckpointSummary{
					Completed:  true,
					TotalSteps: exploredSteps,
					Attempt:    explorationAttempt,
				},
			},
		},
	}
	return o, repo
}

// TestInteg_AnalysisResume_KOfNAreasAreRestoredAndTheRestAreNot is the first
// acceptance criterion, at the seam the Phase 4 loop reads: a run that
// completed K of N areas must be handed exactly those K back, so the loop
// runs only the remaining N−K — and the K must come back intact, not
// duplicated.
func TestInteg_AnalysisResume_KOfNAreasAreRestoredAndTheRestAreNot(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	runAreas := []AnalysisArea{
		{ID: "churn"}, {ID: "engagement"}, {ID: "levels"}, {ID: "monetisation"}, {ID: "retention"},
	}
	at := time.Now().UTC().Truncate(time.Millisecond)

	// Attempt 1 finishes three of the five areas, then dies.
	first, repo := resumedOrchestrator(t, db, "run-k-of-n", 1, 1, 12)
	first.resume = nil // attempt 1 is not a resume
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	validatedPerArea := map[string]int{"churn": 2, "engagement": 1, "levels": 0}
	for i, id := range []string{"churn", "engagement", "levels"} {
		step := completedArea(id, validatedPerArea[id], at.Add(time.Duration(i)*time.Minute))
		if err := first.checkpointAnalysisArea(ctx, step, validatedPerArea[id]); err != nil {
			t.Fatalf("checkpoint %s: %v", id, err)
		}
	}

	// Attempt 2 resumes. Exploration is complete from attempt 1, so this
	// attempt takes the skip path and may restore.
	second, _ := resumedOrchestrator(t, db, "run-k-of-n", 2, 1, 12)
	completed := restorableAreas(runAreas, second.loadCompletedAreas(ctx))
	if len(completed) != 3 {
		t.Fatalf("restored %d areas, want 3", len(completed))
	}
	for _, id := range []string{"churn", "engagement", "levels"} {
		if _, ok := completed[id]; !ok {
			t.Errorf("%s was completed on attempt 1 and must be restored", id)
		}
	}
	for _, id := range []string{"monetisation", "retention"} {
		if _, ok := completed[id]; ok {
			t.Errorf("%s was never analysed and must NOT be restored", id)
		}
	}
	// What the loop then does with them, in runAreas order. The validation
	// budget is seeded from the restored areas BEFORE the loop, which is
	// what keeps an unfinished area that happens to come first from spending
	// a budget the previous attempt had already used.
	var analysisLog []models.AnalysisStep
	var allInsights []models.Insight
	validated := restoredValidationBudget(completed)
	if validated != 3 {
		t.Fatalf("seeded budget = %d, want 3 (2 + 1 + 0 from the restored areas)", validated)
	}
	analysed := 0
	for _, area := range runAreas {
		if cp, ok := completed[area.ID]; ok {
			analysisLog, allInsights = restoreArea(cp, analysisLog, allInsights)
			continue
		}
		analysed++
		// What this attempt actually pays for: two areas, one insight each.
		step := completedArea(area.ID, 1, at.Add(time.Hour))
		analysisLog = append(analysisLog, step)
		allInsights = append(allInsights, step.Insights...)
		validated++
		if err := second.checkpointAnalysisArea(ctx, step, 1); err != nil {
			t.Fatalf("checkpoint %s on attempt 2: %v", area.ID, err)
		}
	}

	if analysed != 2 {
		t.Errorf("this attempt analysed %d areas, want 2 — the other three were already paid for", analysed)
	}
	if len(analysisLog) != 5 {
		t.Errorf("analysis log has %d entries, want 5 — the union of restored and re-run", len(analysisLog))
	}
	// 2 + 1 + 0 restored insights, plus 1 each from the two re-run areas.
	if len(allInsights) != 5 {
		t.Errorf("got %d insights, want 5", len(allInsights))
	}
	if validated != 5 {
		t.Errorf("validation budget = %d, want 5 (3 restored + 2 spent here)", validated)
	}
	// No duplicates: one row per area, five areas, after two attempts wrote
	// over the same run id.
	set, err := repo.LoadAreas(ctx, "run-k-of-n", 1)
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 5 {
		t.Errorf("%d checkpoint rows for 5 areas, want 5 — a re-save must replace, not duplicate", set.Len())
	}

	// Seen once each: an area restored, then re-checkpointed by a later
	// attempt, must not appear twice in the run's result.
	seen := map[string]int{}
	for _, step := range analysisLog {
		seen[step.AreaID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("area %s appears %d times in the analysis log, want 1", id, n)
		}
	}
}

// TestInteg_AnalysisResume_AFailedAreaIsRetriedNotRestored is the §3.5
// invariant against a real database, and the case that makes a run killed by
// DISCOVERY_MAX_DURATION recoverable: the deadline error-marks every
// remaining area, and if those counted as done the resume would restore them
// all and re-run nothing.
func TestInteg_AnalysisResume_AFailedAreaIsRetriedNotRestored(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	o, repo := resumedOrchestrator(t, db, "run-deadline", 1, 1, 9)
	o.resume = nil
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)

	// One area got through before the cap expired; the remaining three
	// returned context.DeadlineExceeded.
	if err := o.checkpointAnalysisArea(ctx, completedArea("churn", 2, at), 2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"engagement", "levels", "retention"} {
		step := completedArea(id, 0, at)
		step.Error = "context deadline exceeded"
		if err := o.checkpointAnalysisArea(ctx, step, 0); err != nil {
			t.Fatal(err)
		}
	}

	resumed, _ := resumedOrchestrator(t, db, "run-deadline", 2, 1, 9)
	completed := resumed.loadCompletedAreas(ctx)
	if len(completed) != 1 {
		t.Fatalf("restored %d areas, want 1 — only the area that actually finished", len(completed))
	}
	if _, ok := completed["churn"]; !ok {
		t.Error("churn finished and must be restored")
	}
	for _, id := range []string{"engagement", "levels", "retention"} {
		if _, ok := completed[id]; ok {
			t.Errorf("%s failed on the deadline and must be RE-RUN; restoring it would reproduce the same near-empty discovery the operator resumed to escape", id)
		}
	}
}

// TestInteg_AnalysisResume_AreasFromASupersededExplorationAreNotRestored is
// the correctness fence end to end: a resumed run whose exploration was
// replaced must not inherit areas whose insights cite the step numbers it
// discarded.
func TestInteg_AnalysisResume_AreasFromASupersededExplorationAreNotRestored(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	// Attempt 1 explored 40 steps, summarised, and analysed one area.
	first, repo := resumedOrchestrator(t, db, "run-reexplored", 1, 1, 40)
	first.resume = nil
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := first.checkpointAnalysisArea(ctx, completedArea("churn", 1, time.Now()), 1); err != nil {
		t.Fatal(err)
	}

	// Attempt 2's prefix was broken (a TTL expiry), so it re-explored and
	// wrote its own summary — attempt 2's, over a different 45 steps.
	second, _ := resumedOrchestrator(t, db, "run-reexplored", 2, 2, 45)
	if got := second.loadCompletedAreas(ctx); len(got) != 0 {
		t.Errorf("restored %v over a re-exploration; those insights cite step numbers that no longer mean anything", got)
	}
	// And this attempt's own area is stamped against ITS exploration.
	if err := second.checkpointAnalysisArea(ctx, completedArea("engagement", 1, time.Now()), 1); err != nil {
		t.Fatal(err)
	}
	third, _ := resumedOrchestrator(t, db, "run-reexplored", 3, 2, 45)
	got := third.loadCompletedAreas(ctx)
	if len(got) != 1 {
		t.Fatalf("restored %d areas, want 1", len(got))
	}
	if _, ok := got["engagement"]; !ok {
		t.Error("engagement was analysed over THIS exploration and must be restored")
	}

	// The stale row is ignored, not destroyed: a transient misread must not
	// be able to lose paid-for work. It ages out on its own TTL.
	all, err := repo.LoadAreas(ctx, "run-reexplored", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all.Areas["churn"]; !ok {
		t.Error("attempt 1's row must still be on file, merely unreachable from attempt 2's exploration")
	}
}

// TestInteg_AnalysisResume_DiscardingTakesBothCollections — the one place a
// terminal-and-not-resumable run drops what made it resumable has to drop
// all of it, or the area rows leak until their TTL for a run that can never
// read them again.
func TestInteg_AnalysisResume_DiscardingTakesBothCollections(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	explorationRepo := database.NewDiscoveryCheckpointRepository(db)
	o, analysisRepo := resumedOrchestrator(t, db, "run-discard", 1, 1, 3)
	o.resume = nil
	o.checkpointRepo = explorationRepo
	o.keepStepIndex = true

	if err := explorationRepo.SaveStep(ctx, database.CheckpointStepInput{
		ProjectID: "proj", RunID: "run-discard", Attempt: 1,
		Step: models.ExplorationStep{Step: 1, Action: "query_data", Query: "SELECT 1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := o.checkpointAnalysisArea(ctx, completedArea("churn", 1, time.Now()), 1); err != nil {
		t.Fatal(err)
	}

	o.discardCheckpoints(ctx, "completed")

	prefix, err := explorationRepo.LoadPrefix(ctx, "run-discard")
	if err != nil {
		t.Fatal(err)
	}
	if prefix.Len() != 0 || prefix.ExplorationComplete() {
		t.Errorf("exploration checkpoints survived the discard: %d steps, summary=%v", prefix.Len(), prefix.ExplorationComplete())
	}
	set, err := analysisRepo.LoadAreas(ctx, "run-discard", 1)
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() != 0 {
		t.Errorf("%d analysis checkpoints survived the discard — half a purge leaks rows nothing will ever read again", set.Len())
	}
	if o.keepStepIndex {
		t.Error("a run that cannot be resumed must let its per-run vector collection go")
	}
}

// TestInteg_AnalysisResume_RestoredInsightIDsSurviveTheStandaloneWrite is the
// regression test for the one way per-area resume could lose a successful
// run's findings outright.
//
// A restored area keeps the insight ids the attempt that produced them
// assigned — that is what makes Phase 5's related_insight_ids resolve. So a
// previous attempt that got as far as writing the standalone rows and then
// died before its terminal status, which the API's startup sweep reaches
// routinely, leaves those ids already in the `insights` collection. Phase 8
// is non-fatal, so an E11000 there is logged and the phase RETURNS: no
// standalone recommendation rows, no embedding, nothing in search. The retire
// step then removes the previous attempt's rows, and the run finishes
// successfully with no standalone findings at all.
//
// The write therefore has to be safe for an id it has seen before.
func TestInteg_AnalysisResume_RestoredInsightIDsSurviveTheStandaloneWrite(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()
	store := NewMongoEmbedIndexStore(db)

	restored := completedArea("churn", 1, time.Now().UTC().Truncate(time.Millisecond))
	insightID := restored.Insights[0].ID

	// Attempt 1 reached Phase 8 and wrote the row under its own discovery.
	first := &models.DiscoveryResult{
		ID: "discovery-1", ProjectID: "proj", Domain: "gaming", Category: "match3",
		Insights: restored.Insights,
	}
	o := &Orchestrator{embedIndexStore: store}
	if err := store.InsertInsights(ctx, o.denormalizeInsights(first)); err != nil {
		t.Fatalf("attempt 1 standalone write: %v", err)
	}

	// Attempt 2 restores that area, so the same insight id reaches the
	// standalone write again — under a new discovery id.
	second := &models.DiscoveryResult{
		ID: "discovery-2", ProjectID: "proj", Domain: "gaming", Category: "match3",
		Insights: restored.Insights,
	}
	if err := store.InsertInsights(ctx, o.denormalizeInsights(second)); err != nil {
		t.Fatalf("attempt 2 standalone write with a restored id: %v — Phase 8 would have returned here, skipping the recommendation rows and the embedding entirely", err)
	}

	// Exactly one row, carrying the LIVE attempt's discovery id.
	col := db.Collection("insights")
	n, err := col.CountDocuments(ctx, bson.M{"_id": insightID})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d rows for one insight id, want 1", n)
	}
	var row struct {
		DiscoveryID string `bson:"discovery_id"`
	}
	if err := col.FindOne(ctx, bson.M{"_id": insightID}).Decode(&row); err != nil {
		t.Fatal(err)
	}
	if row.DiscoveryID != "discovery-2" {
		t.Errorf("discovery_id = %q, want discovery-2 — the row must follow the attempt that owns the run", row.DiscoveryID)
	}

	// And that is what makes the retire step safe: listing attempt 1's
	// discovery no longer finds the row, so retiring it cannot take the live
	// attempt's finding and its vector with it.
	insightIDs, recIDs, err := store.PointIDsByDiscovery(ctx, "discovery-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(insightIDs) != 0 || len(recIDs) != 0 {
		t.Errorf("retiring discovery-1 would delete %v / %v — the restored finding now belongs to discovery-2", insightIDs, recIDs)
	}
	insightIDs, _, err = store.PointIDsByDiscovery(ctx, "discovery-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(insightIDs) != 1 || insightIDs[0] != insightID {
		t.Errorf("discovery-2 lists %v, want the restored insight id", insightIDs)
	}
}

// TestInteg_AnalysisResume_AFreshRunsStandaloneWriteIsUnchanged — the write
// above became an upsert for the resume case, so the ordinary case has to be
// pinned too: distinct ids all land, and nothing is silently collapsed.
func TestInteg_AnalysisResume_AFreshRunsStandaloneWriteIsUnchanged(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()
	store := NewMongoEmbedIndexStore(db)
	o := &Orchestrator{embedIndexStore: store}

	result := &models.DiscoveryResult{
		ID: "discovery-1", ProjectID: "proj", Domain: "gaming", Category: "match3",
	}
	for _, area := range []string{"churn", "engagement", "levels"} {
		result.Insights = append(result.Insights, completedArea(area, 1, time.Now()).Insights...)
	}
	if err := store.InsertInsights(ctx, o.denormalizeInsights(result)); err != nil {
		t.Fatalf("fresh standalone write: %v", err)
	}
	n, err := db.Collection("insights").CountDocuments(ctx, bson.M{"discovery_id": "discovery-1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("%d rows, want 3 — three distinct insights must all land", n)
	}
}
