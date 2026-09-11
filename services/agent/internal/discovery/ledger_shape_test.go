package discovery

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The Discovery Ledger's world model was a set of tables. A cube-shaped
// datasource has none, so a run that explored one recorded no coverage for it
// and the next run was told the project was fully explored — every run, and
// cumulatively, because each one inherits the summary the last one wrote.
//
// These tests hold both halves of the fix: a run that reaches only tables must
// behave exactly as it did before cubes existed, and a run that reaches a cube
// must be able to say what it sliced and must never report an exhausted
// frontier.

// --- fixtures -------------------------------------------------------------

// reflectionPromptFixture is the input the table-only golden was captured
// from, on the commit before this change. Every field is fixed so the render
// is deterministic: the prompt is compared to that capture byte for byte.
func reflectionPromptFixture() (*Orchestrator, *models.DiscoveryResult, []commonmodels.LedgerFinding, []commonmodels.LedgerTask, agentplugin.DiscoveryPolicy) {
	seen := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	o := &Orchestrator{language: "English", datasets: []string{"analytics", "ops"}}
	result := &models.DiscoveryResult{
		Schemas: map[string]models.TableSchema{
			"analytics.orders":    {},
			"analytics.customers": {},
			"ops.shipments":       {},
		},
		Insights: []models.Insight{
			{AnalysisArea: "inventory", Name: "Dead stock concentrated in two sellers", Severity: "high", AffectedCount: 42, Description: "Two sellers hold 60% of the unsold stock."},
			{AnalysisArea: "churn", Name: "Churn rises after the second late delivery", Severity: "medium", AffectedCount: 310},
		},
	}
	prior := []commonmodels.LedgerFinding{
		{ID: "f-1", Area: "inventory", Name: "Slow movers in seasonal lines", Status: "confirmed", SeenCount: 3, KeyMetric: "units=1200", LastSeen: seen},
	}
	tasks := []commonmodels.LedgerTask{
		{ID: "t-1", Kind: "next_task", Text: "Check whether the two sellers share a fulfilment centre"},
	}
	pol := agentplugin.DiscoveryPolicy{FrontierPolicy: agentplugin.FrontierBreadthFirst, EvolutionMode: agentplugin.EvolutionModeAuto}
	return o, result, prior, tasks, pol
}

// --- the prompt -----------------------------------------------------------

// TestReflectionPrompt_TableOnlyRunIsUnchanged is the protection for every
// deployment that exists today. The golden was captured by rendering this same
// fixture on the parent commit, so a drift of even one byte — a heading, a
// bullet, a stray newline left behind by the new token substitution — fails
// here rather than quietly changing what every SQL-only project's reflection
// is asked to do.
func TestReflectionPrompt_TableOnlyRunIsUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/reflection_prompt_table_only.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	o, result, prior, tasks, pol := reflectionPromptFixture()

	got := o.buildReflectionPrompt(result, prior, tasks, pol, nil)

	if got != string(want) {
		t.Errorf("table-only reflection prompt drifted from the pre-cube render.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestReflectionPrompt_CubeRunCanReportWhatItSliced is the bug itself: without
// a cube catalog in the prompt there is no name for the model to copy, so work
// it genuinely did against a cube is unreportable no matter how well it
// reasons.
func TestReflectionPrompt_CubeRunCanReportWhatItSliced(t *testing.T) {
	o, result, prior, tasks, pol := reflectionPromptFixture()
	items := []string{"sessions", "activeUsers", "sessionDefaultChannelGroup"}

	got := o.buildReflectionPrompt(result, prior, tasks, pol, items)

	// The warehouse catalog is still there — a cube run has a table side too.
	if !strings.Contains(got, reflectionTableCatalogHeading) {
		t.Error("the warehouse catalog heading must survive on a mixed run")
	}
	for _, table := range []string{"analytics.orders", "ops.shipments"} {
		if !strings.Contains(got, table) {
			t.Errorf("table %q missing from the mixed-run prompt", table)
		}
	}
	// The cube catalog and its output field are what make cube work reportable.
	if !strings.Contains(got, "## Cube catalog") {
		t.Error("a run that reaches a cube must be shown the cube catalog")
	}
	for _, item := range items {
		if !strings.Contains(got, item) {
			t.Errorf("catalog item %q missing from the mixed-run prompt", item)
		}
	}
	if !strings.Contains(got, "- **covered_catalog_items**") {
		t.Error("the output contract must define covered_catalog_items on a cube run")
	}
	// And it must not import the framing it exists to escape: a cube is not a
	// frontier to tile, or a model reports 470 metrics as covered.
	if !strings.Contains(got, "no frontier to tile") {
		t.Error("the cube section must say a cube has no frontier to tile")
	}
	// The two namespaces are untyped, so the prompt has to name which catalog
	// each field is copied from.
	if !strings.Contains(got, "never a cube metric or dimension") {
		t.Error("covered_tables must be scoped to the warehouse catalog on a cube run")
	}
}

// TestReflectionRepairSuffix_NamesTheCubeFieldOnlyWhenThereIsOne: the repair
// prompt re-states the output contract, so on a table-only run it must not
// name a field that run's prompt never defined.
func TestReflectionRepairSuffix_NamesTheCubeFieldOnlyWhenThereIsOne(t *testing.T) {
	tableOnly := reflectionRepairSuffix(errors.New("unexpected end of JSON input"), false)
	const wantTableOnly = "\n\nYour previous response could not be used: unexpected end of JSON input.\n" +
		"Respond with ONLY a single JSON object with the fields coverage_summary, covered_tables, " +
		"covered_areas, prior_status_updates, task_status_updates, learnings, next_tasks, domain_pack_deltas, " +
		"convergence_note — no prose and no markdown fences."
	if tableOnly != wantTableOnly {
		t.Errorf("table-only repair suffix drifted:\ngot:  %q\nwant: %q", tableOnly, wantTableOnly)
	}

	cube := reflectionRepairSuffix(errors.New("boom"), true)
	if !strings.Contains(cube, "covered_tables, covered_catalog_items, covered_areas") {
		t.Errorf("cube repair suffix must name covered_catalog_items, got %q", cube)
	}
}

// TestRunCatalogItems_FlattensDedupesAndSorts: coverage is a project-level
// record, so the per-datasource keying collapses — but deterministically, and
// without letting a name shared by two cubes appear twice.
func TestRunCatalogItems_FlattensDedupesAndSorts(t *testing.T) {
	o := &Orchestrator{runCatalogRefs: map[string][]string{
		"ga4_a": {"sessions", " activeUsers ", "sessions"},
		"ga4_b": {"activeUsers", "purchaseRevenue", "", "   "},
	}}

	got := o.runCatalogItems()

	want := []string{"activeUsers", "purchaseRevenue", "sessions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runCatalogItems() = %v, want %v", got, want)
	}
	if items := (&Orchestrator{}).runCatalogItems(); items != nil {
		t.Errorf("a run with no catalog-shaped datasource has no items, got %v", items)
	}
}

// --- coverage validation --------------------------------------------------

// TestMergeExplored_CountsOnlyWhatTheRunCouldHaveQueried. An unchecked merge
// is what let the explored count pass the catalog size, which the frontier
// arithmetic then clamps to zero — so the counter was wrong in both
// directions at once.
func TestMergeExplored_CountsOnlyWhatTheRunCouldHaveQueried(t *testing.T) {
	catalog := nameIndex([]string{"analytics.orders", "analytics.customers", "ops.shipments"})

	tests := []struct {
		name     string
		carried  []string
		reported []string
		want     []string
	}{
		{
			name:     "a name the catalog has is counted",
			reported: []string{"analytics.orders"},
			want:     []string{"analytics.orders"},
		},
		{
			name:     "an invented name is dropped",
			reported: []string{"analytics.orders", "analytics.does_not_exist"},
			want:     []string{"analytics.orders"},
		},
		{
			name:     "a cube metric answered into covered_tables is dropped",
			reported: []string{"sessionDefaultChannelGroup", "ops.shipments"},
			want:     []string{"ops.shipments"},
		},
		{
			name:     "a case-different spelling resolves to the catalog's own",
			reported: []string{"ANALYTICS.ORDERS"},
			want:     []string{"analytics.orders"},
		},
		{
			name:     "a case-different spelling does not double-count",
			carried:  []string{"analytics.orders"},
			reported: []string{"Analytics.Orders"},
			want:     []string{"analytics.orders"},
		},
		{
			name:     "surrounding whitespace is not a different table",
			reported: []string{"  ops.shipments  "},
			want:     []string{"ops.shipments"},
		},
		{
			name:     "what earlier runs recorded is never revoked",
			carried:  []string{"analytics.retired_table"},
			reported: []string{"analytics.orders"},
			want:     []string{"analytics.orders", "analytics.retired_table"},
		},
		{
			name:    "nothing reported leaves the carried set alone",
			carried: []string{"analytics.orders"},
			want:    []string{"analytics.orders"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeExplored(tc.carried, tc.reported, catalog)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeExplored(%v, %v) = %v, want %v", tc.carried, tc.reported, got, tc.want)
			}
		})
	}
}

// TestMergeExplored_NoCatalogAcceptsNothing. A run with no cube has no cube
// catalog, so every item claimed against one is unverifiable — and the
// permissive reading is exactly how a stray name used to become coverage.
func TestMergeExplored_NoCatalogAcceptsNothing(t *testing.T) {
	got := mergeExplored([]string{"kept.from.before"}, []string{"sessions", "activeUsers"}, nil)
	want := []string{"kept.from.before"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeExplored with no catalog = %v, want %v", got, want)
	}
}

// TestNameIndex_ResolvesDeterministically. Two catalog names differing only in
// case are a real possibility on a case-sensitive warehouse; whichever one
// wins, it must be the same one on every run rather than whichever the map
// happened to yield.
func TestNameIndex_ResolvesDeterministically(t *testing.T) {
	first := nameIndex([]string{"ds.Orders", "ds.orders", "ds.ORDERS"})["ds.orders"]
	for i := 0; i < 20; i++ {
		if got := nameIndex([]string{"ds.ORDERS", "ds.orders", "ds.Orders"})["ds.orders"]; got != first {
			t.Fatalf("nameIndex resolved %q then %q for the same catalog", first, got)
		}
	}
	if nameIndex(nil) != nil {
		t.Error("an empty catalog has no index")
	}
}

// --- what the next run reads ----------------------------------------------

// TestRenderCoverage_TableOnlyIsUnchanged pins the strings a table-only
// project has always received, captured from the parent commit.
func TestRenderCoverage_TableOnlyIsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		cov  commonmodels.LedgerCoverage
		want string
	}{
		{
			name: "explored against a known catalog size",
			cov: commonmodels.LedgerCoverage{
				ExploredTables: []string{"analytics.customers", "analytics.orders"},
				TotalTables:    3,
				Summary:        "orders + customers covered; shipments untouched.",
			},
			want: "### Coverage map\nExplored 2 of 3 catalog tables (1 still on the frontier). orders + customers covered; shipments untouched.\n\n",
		},
		{
			name: "explored with no catalog size recorded",
			cov: commonmodels.LedgerCoverage{
				ExploredTables: []string{"analytics.orders"},
				Summary:        "orders covered.",
			},
			want: "### Coverage map\nExplored 1 tables so far. orders covered.\n\n",
		},
		{
			name: "an empty ledger renders nothing at all",
			cov:  commonmodels.LedgerCoverage{},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderCoverage(tc.cov); got != tc.want {
				t.Errorf("renderCoverage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderCoverage_ACubeProjectIsNeverReportedAsExhausted is the compounding
// failure in one assertion. Every table covered is a true statement about the
// table side and a false one about the project, and it is this string the next
// run inherits.
func TestRenderCoverage_ACubeProjectIsNeverReportedAsExhausted(t *testing.T) {
	cov := commonmodels.LedgerCoverage{
		ExploredTables:       []string{"analytics.customers", "analytics.orders", "ops.shipments"},
		TotalTables:          3,
		ExploredCatalogItems: []string{"activeUsers", "sessions"},
		TotalCatalogItems:    470,
		Summary:              "the warehouse is tiled.",
	}

	got := renderCoverage(cov)

	// The table arithmetic is untouched and still says what it says.
	if !strings.Contains(got, "Explored 3 of 3 catalog tables (0 still on the frontier).") {
		t.Errorf("the table clause must be unchanged, got %q", got)
	}
	// But it must no longer be the whole story.
	if !strings.Contains(got, "tables only") {
		t.Errorf("the table count must be qualified on a cube project, got %q", got)
	}
	if !strings.Contains(got, "2 of their 470 metrics and dimensions") {
		t.Errorf("what has been sliced on the cube must be carried forward, got %q", got)
	}
	if !strings.Contains(got, "never finished and never absent from the frontier") {
		t.Errorf("the next run must be told a cube is never exhausted, got %q", got)
	}
	if !strings.Contains(got, "the warehouse is tiled.") {
		t.Errorf("the reflection summary must still be rendered, got %q", got)
	}
}

// TestRenderCoverage_CubeWithNothingSlicedYet — the first run on a project
// with a cube has an empty explored set, which must read as "not looked at",
// never as "0 of 470 done".
func TestRenderCoverage_CubeWithNothingSlicedYet(t *testing.T) {
	got := renderCoverage(commonmodels.LedgerCoverage{
		ExploredTables: []string{"analytics.orders"}, TotalTables: 3, TotalCatalogItems: 470,
	})
	if !strings.Contains(got, "none of their 470 metrics and dimensions are recorded as queried yet") {
		t.Errorf("an untouched cube must say so plainly, got %q", got)
	}
}

// TestLoadLedgerReadContext_CubeCoverageAloneIsALedger: a project whose only
// accumulated state is cube slices still has something to carry forward.
func TestLoadLedgerReadContext_CubeCoverageAloneIsALedger(t *testing.T) {
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	o := &Orchestrator{
		projectID:   "proj-1",
		findingRepo: &fakeFindingRepo{},
		ledgerRepo: &fakeLedgerRepo{ledger: &commonmodels.DiscoveryLedger{
			ProjectID: "proj-1",
			Coverage:  commonmodels.LedgerCoverage{ExploredCatalogItems: []string{"sessions"}, TotalCatalogItems: 470},
		}},
	}

	lrc := o.loadLedgerReadContext(context.Background())

	if lrc == nil {
		t.Fatal("cube coverage on its own must still produce a ledger read context")
	}
	if len(lrc.coverage.ExploredCatalogItems) != 1 {
		t.Errorf("cube coverage lost on load: %+v", lrc.coverage)
	}
}

// --- the write path -------------------------------------------------------

// TestUpdateLedgerMeta_RecordsTheCubeEvenWhenReflectionProducedNothing. The
// catalog size is what tells the next run this project HAS a cube, so it
// cannot depend on an LLM call that is explicitly allowed to fail.
func TestUpdateLedgerMeta_RecordsTheCubeEvenWhenReflectionProducedNothing(t *testing.T) {
	ledger := &fakeLedgerRepo{}
	o := &Orchestrator{
		projectID: "proj-1", runID: "run-1", ledgerRepo: ledger,
		runCatalogRefs: map[string][]string{"ga4": {"sessions", "activeUsers"}},
	}
	result := &models.DiscoveryResult{Schemas: map[string]models.TableSchema{"ds.orders": {}}}

	o.updateLedgerMeta(context.Background(), result, nil, 0, 0)

	if ledger.saved == nil {
		t.Fatal("ledger was not saved")
	}
	if ledger.saved.Coverage.TotalTables != 1 {
		t.Errorf("TotalTables = %d, want 1", ledger.saved.Coverage.TotalTables)
	}
	if ledger.saved.Coverage.TotalCatalogItems != 2 {
		t.Errorf("TotalCatalogItems = %d, want 2 (recorded without a reflection)", ledger.saved.Coverage.TotalCatalogItems)
	}
}

// TestRunPhaseReflection_CubeRunSortsCoverageIntoTheRightNamespace is the
// end-to-end shape: the model answers with both kinds of name, one of each
// crossed over, and the ledger has to file them against the catalog each came
// from rather than believing the labels.
func TestRunPhaseReflection_CubeRunSortsCoverageIntoTheRightNamespace(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	// "sessions" is a cube metric offered as a table; "ds.orders" is a table
	// offered as a cube item. Both are crossed over and must be dropped, not
	// filed into the other set.
	resp := `{"coverage_summary":"orders covered; the cube barely touched",` +
		`"covered_tables":["ds.orders","sessions"],` +
		`"covered_catalog_items":["activeUsers","ds.orders","notAMetric"],` +
		`"covered_areas":["churn"]}`
	client, _ := stubClient(t, resp, nil)

	ledger := &fakeLedgerRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: ledger, findingRepo: &fakeFindingRepo{},
		runCatalogRefs: map[string][]string{"ga4": {"sessions", "activeUsers", "purchaseRevenue"}},
	}

	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{
		ID: "disc-1", ProjectID: "proj-1",
		Schemas:  map[string]models.TableSchema{"ds.orders": {}, "ds.events": {}},
		Insights: []models.Insight{{AnalysisArea: "churn", Name: "High churn", Severity: "high", AffectedCount: 40}},
	})

	if ledger.saved == nil {
		t.Fatal("ledger was not saved")
	}
	cov := ledger.saved.Coverage
	if !reflect.DeepEqual(cov.ExploredTables, []string{"ds.orders"}) {
		t.Errorf("ExploredTables = %v, want just the real table", cov.ExploredTables)
	}
	if !reflect.DeepEqual(cov.ExploredCatalogItems, []string{"activeUsers"}) {
		t.Errorf("ExploredCatalogItems = %v, want just the real catalog item", cov.ExploredCatalogItems)
	}
	if cov.TotalCatalogItems != 3 {
		t.Errorf("TotalCatalogItems = %d, want 3", cov.TotalCatalogItems)
	}
}

// --- the generation contract ----------------------------------------------

// TestReflectionSchema_MatchesStructTags keeps the structured-output schema and
// the struct the response is decoded into from drifting apart: a property the
// parser has no field for is silently discarded, which looks exactly like a
// model that declined to answer.
func TestReflectionSchema_MatchesStructTags(t *testing.T) {
	tags := jsonTagSet(reflect.TypeOf(parsedReflection{}))
	props := reflectionResponseSchema()["properties"].(map[string]interface{})

	for name := range props {
		if !tags[name] {
			t.Errorf("schema property %q has no matching json tag on parsedReflection", name)
		}
	}
	if _, ok := props["covered_catalog_items"]; !ok {
		t.Error("covered_catalog_items must be part of the generation contract")
	}
	// Server-assigned state is never the model's to produce.
	for _, internal := range []string{"id", "project_id", "created_at", "status"} {
		if _, ok := props[internal]; ok {
			t.Errorf("schema must not expose internal field %q to the model", internal)
		}
	}
}
