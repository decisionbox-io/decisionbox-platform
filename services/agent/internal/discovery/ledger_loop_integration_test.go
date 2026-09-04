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
	for _, r := range []interface{ EnsureIndexes(context.Context) error }{ledgerRepo, findingRepo, taskRepo} {
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
			database.CollectionDiscoveryLedger,
		} {
			_, _ = db.Collection(coll).DeleteMany(ctx, map[string]any{"project_id": projectID})
		}
	})

	// Auto evolution so the read path surfaces next-tasks too.
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeAuto})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	o := &Orchestrator{
		projectID:   projectID,
		runID:       "run-1",
		datasets:    []string{"ds"},
		ledgerRepo:  ledgerRepo,
		findingRepo: findingRepo,
		taskRepo:    taskRepo,
	}

	// --- Run 1: consolidate three findings, record coverage + a convergence point. ---
	run1 := &models.DiscoveryResult{
		ProjectID: projectID, ID: "disc-1",
		Schemas: map[string]models.TableSchema{"ds.orders": {}, "ds.events": {}, "ds.users": {}},
		Insights: []models.Insight{
			{AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100, Description: "churn elevated in EU"},
			{AnalysisArea: "fraud", Name: "Card testing spike", Severity: "critical", AffectedCount: 12},
			{AnalysisArea: "revenue", Name: "Refund rate up", Severity: "medium", AffectedCount: 40},
		},
	}
	newCount, total, err := o.consolidateFindings(ctx, run1)
	if err != nil {
		t.Fatalf("run1 consolidate: %v", err)
	}
	if newCount != 3 || total != 3 {
		t.Fatalf("run1 expected 3 new / 3 total, got %d / %d", newCount, total)
	}
	o.updateLedgerMeta(ctx, run1, &parsedReflection{
		CoverageSummary: "orders + users covered; the events tables are untouched",
		CoveredTables:   []string{"ds.orders", "ds.users"},
	}, newCount, total)

	// Seed a next-task the way applyNextTasks would.
	o.applyNextTasks(ctx, &parsedReflection{NextTasks: []struct {
		Text       string `json:"text"`
		Kind       string `json:"kind"`
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}{{Text: "explore the untouched events tables", Kind: "next_task"}}})

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
			{AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100, Description: "still elevated"}, // repeat → merge
			{AnalysisArea: "events", Name: "Event drop-off", Severity: "high", AffectedCount: 25},                               // new
		},
	}
	newCount2, total2, err := o.consolidateFindings(ctx, run2)
	if err != nil {
		t.Fatalf("run2 consolidate: %v", err)
	}
	if newCount2 != 1 {
		t.Errorf("run2 should add exactly 1 new finding (the repeat merges), got %d", newCount2)
	}
	if total2 != 4 {
		t.Errorf("run2 total should be 4 (3 + 1 new), got %d", total2)
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
}
