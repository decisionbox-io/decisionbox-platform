//go:build integration

package discovery

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// ledgerLoopMongo connects to a real MongoDB: the env-provided instance when
// LEDGER_TEST_MONGO_URI is set (used to verify against the live dev stack), else
// an ephemeral testcontainer (CI). Returns the DB + a cleanup.
func ledgerLoopMongo(t *testing.T) (*database.DB, func()) {
	t.Helper()
	ctx := context.Background()

	if uri := os.Getenv("LEDGER_TEST_MONGO_URI"); uri != "" {
		cfg := gomongo.DefaultConfig()
		cfg.URI = uri
		cfg.Database = envOr("LEDGER_TEST_MONGO_DB", "decisionbox")
		client, err := gomongo.NewClient(ctx, cfg)
		if err != nil {
			t.Fatalf("connect live mongo: %v", err)
		}
		return database.New(client), func() { _ = client.Disconnect(ctx) }
	}

	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo container: %v", err)
	}
	uri, _ := container.ConnectionString(ctx)
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "ledger_loop_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	return database.New(client), func() {
		_ = client.Disconnect(ctx)
		_ = testcontainers.TerminateContainer(container)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestLedgerLoop_Integration proves the compounding-discovery loop end to end
// against a real MongoDB: run 1 consolidates findings into the ledger; run 2
// reads that ledger back into its prompt context AND merges a repeated finding
// (dedup) rather than duplicating it, while the coverage + convergence grow.
func TestLedgerLoop_Integration(t *testing.T) {
	db, cleanup := ledgerLoopMongo(t)
	// Register the disconnect via t.Cleanup (not defer) so it runs AFTER the
	// per-project delete cleanup below — t.Cleanup is LIFO, and the delete needs
	// a live connection. A plain `defer cleanup()` would disconnect first and
	// leave the scoped docs behind on a shared/live Mongo.
	t.Cleanup(cleanup)
	ctx := context.Background()

	projectID := "itest-" + uuid.New().String()
	ledgerRepo := database.NewLedgerRepository(db)
	findingRepo := database.NewLedgerFindingRepository(db)
	taskRepo := database.NewLedgerTaskRepository(db)
	learningRepo := database.NewLedgerLearningRepository(db)
	for _, r := range []interface{ EnsureIndexes(context.Context) error }{ledgerRepo, findingRepo, taskRepo, learningRepo} {
		if err := r.EnsureIndexes(ctx); err != nil {
			t.Fatalf("ensure indexes: %v", err)
		}
	}
	// Scope every write to a throwaway project id and clean up after, so this is
	// safe to run against the shared/live Mongo.
	t.Cleanup(func() {
		for _, coll := range []string{
			database.CollectionDiscoveryLedgerFindings,
			database.CollectionDiscoveryLedgerTasks,
			database.CollectionDiscoveryLedgerLearnings,
			database.CollectionDiscoveryLedger,
		} {
			_, _ = db.Collection(coll).DeleteMany(ctx, map[string]any{"project_id": projectID})
		}
	})

	// Auto evolution so the read path surfaces next-tasks too.
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeAuto})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	o := &Orchestrator{
		projectID:    projectID,
		runID:        "run-1",
		datasets:     []string{"ds"},
		ledgerRepo:   ledgerRepo,
		findingRepo:  findingRepo,
		taskRepo:     taskRepo,
		learningRepo: learningRepo,
	}

	// --- Run 1: consolidate three findings, record coverage + a convergence point. ---
	run1 := &models.DiscoveryResult{
		ProjectID: projectID, ID: "disc-1",
		Schemas: map[string]models.TableSchema{"ds.orders": {}, "ds.events": {}, "ds.users": {}},
		Insights: []models.Insight{
			{ID: "ins-churn-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100, Description: "churn elevated in EU"},
			{ID: "ins-fraud-1", AnalysisArea: "fraud", Name: "Card testing spike", Severity: "critical", AffectedCount: 12},
			{ID: "ins-revenue-1", AnalysisArea: "revenue", Name: "Refund rate up", Severity: "medium", AffectedCount: 40},
		},
	}
	cons1, err := o.consolidateFindings(ctx, run1)
	if err != nil {
		t.Fatalf("run1 consolidate: %v", err)
	}
	newCount, total, reSeen := cons1.NewCount, cons1.TotalCount, cons1.ReSeen
	if newCount != 3 || total != 3 {
		t.Fatalf("run1 expected 3 new / 3 total, got %d / %d", newCount, total)
	}
	if len(reSeen) != 0 {
		t.Fatalf("run1 is the first run — nothing can have been re-seen, got %d", len(reSeen))
	}
	o.updateLedgerMeta(ctx, run1, &parsedReflection{
		CoverageSummary: "orders + users covered; the events tables are untouched",
		CoveredTables:   []string{"ds.orders", "ds.users"},
	}, newCount, total)

	// Seed a next-task the way applyNextTasks would, attributed to the finding
	// it grew out of — by the INSIGHT id, which is the id the model is shown
	// for a finding this run produced.
	o.applyNextTasks(ctx, &parsedReflection{NextTasks: []struct {
		Title            string   `json:"title"`
		Text             string   `json:"text"`
		Kind             string   `json:"kind"`
		TargetType       string   `json:"target_type"`
		TargetID         string   `json:"target_id"`
		Supersedes       string   `json:"supersedes"`
		OriginFindingIDs []string `json:"origin_finding_ids"`
	}{{Text: "explore the untouched events tables", Kind: "next_task",
		OriginFindingIDs: []string{"ins-churn-1"}}}}, nil, cons1)

	// Verify the ledger persisted with substance.
	stored, err := findingRepo.List(ctx, projectID)
	if err != nil || len(stored) != 3 {
		t.Fatalf("run1 findings not persisted: n=%d err=%v", len(stored), err)
	}
	var churn *commonmodels.LedgerFinding
	for i := range stored {
		if stored[i].Name == "High EU churn" {
			churn = &stored[i]
		}
	}
	if churn == nil || churn.KeyMetric == "" || churn.Description == "" {
		t.Fatalf("finding must be carried with substance: %+v", churn)
	}

	// --- Run 2: the next run READS the ledger back into its prompt context. ---
	o.runID = "run-2"
	lrc := o.loadLedgerReadContext(ctx)
	if lrc == nil || !lrc.hasLedger {
		t.Fatal("run2 must load a non-empty ledger read context")
	}
	if len(lrc.findings) != 3 {
		t.Errorf("run2 read context should carry 3 findings, got %d", len(lrc.findings))
	}
	// Critical finding ranks first.
	if lrc.findings[0].Name != "Card testing spike" {
		t.Errorf("critical finding should rank first, got %q", lrc.findings[0].Name)
	}
	prevCtx := o.buildPreviousContext(&models.ProjectContext{TotalDiscoveries: 1, LastDiscoveryDate: time.Now()}, nil, nil, nil, lrc)
	for _, want := range []string{"Investigation so far", "Coverage map", "untouched", "Card testing spike", "explore the untouched events tables"} {
		if !strings.Contains(prevCtx, want) {
			t.Errorf("run2 prompt context missing %q:\n%s", want, prevCtx)
		}
	}

	// --- Run 2 consolidation: a REPEAT finding merges (dedup); a new one is added. ---
	run2 := &models.DiscoveryResult{
		ProjectID: projectID, ID: "disc-2",
		Schemas: run1.Schemas,
		Insights: []models.Insight{
			{ID: "ins-churn-2", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100, Description: "still elevated"}, // repeat → merge
			{ID: "ins-events-2", AnalysisArea: "events", Name: "Event drop-off", Severity: "high", AffectedCount: 25},                              // new
		},
	}
	cons2, err := o.consolidateFindings(ctx, run2)
	if err != nil {
		t.Fatalf("run2 consolidate: %v", err)
	}
	newCount2, total2, reSeen2 := cons2.NewCount, cons2.TotalCount, cons2.ReSeen
	if newCount2 != 1 {
		t.Errorf("run2 should add exactly 1 new finding (the repeat merges), got %d", newCount2)
	}
	if total2 != 4 {
		t.Errorf("run2 total should be 4 (3 + 1 new), got %d", total2)
	}
	if len(reSeen2) != 1 {
		t.Errorf("run2 surfaced exactly 1 carried finding again, got %d", len(reSeen2))
	}
	after, _ := findingRepo.List(ctx, projectID)
	if len(after) != 4 {
		t.Fatalf("ledger should hold 4 findings after run2, got %d", len(after))
	}
	for _, f := range after {
		if f.Name == "High EU churn" && f.SeenCount != 2 {
			t.Errorf("repeated finding should have seen_count=2, got %d", f.SeenCount)
		}
	}

	// Convergence grew across the two runs.
	o.updateLedgerMeta(ctx, run2, &parsedReflection{CoverageSummary: "events now covered too"}, newCount2, total2)
	lg, _ := ledgerRepo.Get(ctx, projectID)
	if lg == nil || len(lg.Convergence) != 2 {
		t.Fatalf("convergence history should have 2 points, got %+v", lg)
	}
	t.Logf("compounding loop verified: run1=%d findings, run2 added %d (1 repeat merged), convergence points=%d", total, newCount2, len(lg.Convergence))

	// --- The investigation graph: the edges this run had in hand (#389). ---
	//
	// Everything above proves the loop compounds. This proves it can be
	// retraced: finding → insight, finding → per-run sightings, task → closing
	// run + the findings that answered it, and the mirror of that last edge on
	// the finding itself.

	// The repeated finding carries one sighting per run, with the magnitude as
	// it stood at each — the row that makes a trend drawable.
	var churnAfter *commonmodels.LedgerFinding
	for i := range after {
		if after[i].Name == "High EU churn" {
			churnAfter = &after[i]
		}
	}
	if churnAfter == nil {
		t.Fatal("the repeated finding disappeared")
	}
	if len(churnAfter.Sightings) != 2 {
		t.Fatalf("want a sighting per run, got %d: %+v", len(churnAfter.Sightings), churnAfter.Sightings)
	}
	if churnAfter.Sightings[0].RunID != "run-1" || churnAfter.Sightings[1].RunID != "run-2" {
		t.Errorf("sightings should be in run order, got %+v", churnAfter.Sightings)
	}
	if churnAfter.Sightings[0].DiscoveryID != "disc-1" || churnAfter.Sightings[1].DiscoveryID != "disc-2" {
		t.Errorf("each sighting must name the discovery it came from, got %+v", churnAfter.Sightings)
	}
	// Both runs' insights are recorded, so the finding drills through to the
	// SQL of either run.
	for _, want := range []string{"ins-churn-1", "ins-churn-2"} {
		if !containsString(churnAfter.SourceInsightIDs, want) {
			t.Errorf("finding should carry insight %s, got %v", want, churnAfter.SourceInsightIDs)
		}
	}
	// The thread opened in run 1 is attributed to the finding it grew out of —
	// resolved from the insight id the model was shown.
	openTasks, err := taskRepo.List(ctx, projectID, commonmodels.LedgerTaskStatusOpen)
	if err != nil || len(openTasks) != 1 {
		t.Fatalf("expected the seeded thread: n=%d err=%v", len(openTasks), err)
	}
	thread := openTasks[0]
	if len(thread.OriginFindingIDs) != 1 || thread.OriginFindingIDs[0] != churnAfter.ID {
		t.Errorf("thread origin should resolve to the churn finding (%s), got %v", churnAfter.ID, thread.OriginFindingIDs)
	}

	// Run 2 had that thread in front of it, whether or not it closed it.
	o.markThreadsPursued(ctx, agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto})

	// Run 2 closes the thread, pointing at the finding from run 2 that
	// answered it — by its insight id, the only id for it the model ever sees.
	var eventsFinding *commonmodels.LedgerFinding
	for i := range after {
		if after[i].Name == "Event drop-off" {
			eventsFinding = &after[i]
		}
	}
	if eventsFinding == nil {
		t.Fatal("run 2's new finding is missing")
	}
	ref2 := &parsedReflection{}
	ref2.TaskStatusUpdates = []struct {
		TaskID               string   `json:"task_id"`
		Status               string   `json:"status"`
		Reason               string   `json:"reason"`
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	}{{
		TaskID: thread.ID, Status: commonmodels.LedgerTaskStatusDone,
		Reason:               "the events tables explain the drop-off",
		ResolvedByFindingIDs: []string{"ins-events-2", "fabricated-id"},
	}}
	ref2.Learnings = []struct {
		Category  string  `json:"category"`
		Note      string  `json:"note"`
		Relevance float64 `json:"relevance"`
	}{{Category: "schema", Note: "the events tables are partitioned by day", Relevance: 0.8}}
	o.applyReflection(ctx, run2,
		agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto}, ref2, cons2)

	closedTasks, err := taskRepo.List(ctx, projectID, commonmodels.LedgerTaskStatusDone)
	if err != nil || len(closedTasks) != 1 {
		t.Fatalf("the thread should be closed: n=%d err=%v", len(closedTasks), err)
	}
	closedThread := closedTasks[0]
	if closedThread.ClosedRunID != "run-2" || closedThread.ClosedDiscoveryID != "disc-2" {
		t.Errorf("close not attributed to run 2: %+v", closedThread)
	}
	if closedThread.ClosedAt.IsZero() || closedThread.CloseReason == "" {
		t.Errorf("a close must carry when and why: %+v", closedThread)
	}
	if len(closedThread.ResolvedByFindingIDs) != 1 || closedThread.ResolvedByFindingIDs[0] != eventsFinding.ID {
		t.Errorf("resolved-by should be the run-2 finding (%s) with the fabricated id dropped, got %v",
			eventsFinding.ID, closedThread.ResolvedByFindingIDs)
	}
	if !containsString(closedThread.PursuedInRunIDs, "run-2") {
		t.Errorf("run 2 had this thread in front of it: %v", closedThread.PursuedInRunIDs)
	}

	// The reverse edge is on the finding, so the tree walks from either end.
	reloaded, err := findingRepo.List(ctx, projectID)
	if err != nil {
		t.Fatalf("reload findings: %v", err)
	}
	var mirrored []string
	for i := range reloaded {
		if reloaded[i].ID == eventsFinding.ID {
			mirrored = reloaded[i].AnsweredTaskIDs
		}
	}
	if len(mirrored) != 1 || mirrored[0] != closedThread.ID {
		t.Errorf("the answering finding should record the task it answered, got %v", mirrored)
	}

	// And the learning is on the ledger, attributed to the run that produced it
	// — it used to go only into the project-context notes, where the ledger
	// could not see it.
	learnings, err := learningRepo.List(ctx, projectID, 0)
	if err != nil {
		t.Fatalf("list learnings: %v", err)
	}
	if len(learnings) != 1 {
		t.Fatalf("want 1 learning, got %d", len(learnings))
	}
	if learnings[0].RunID != "run-2" || learnings[0].DiscoveryID != "disc-2" {
		t.Errorf("learning not attributed: %+v", learnings[0])
	}

	t.Logf("investigation graph verified: %d sightings on the repeated finding, thread %s closed by %s pointing at %s",
		len(churnAfter.Sightings), closedThread.ID, closedThread.ClosedRunID, closedThread.ResolvedByFindingIDs)
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
