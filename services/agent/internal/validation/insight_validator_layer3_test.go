package validation

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// stubSchemaProvider is a deterministic ai.SchemaProvider for verifier tool-
// loop tests. Lookup returns a pre-populated map keyed by table name; refs
// not in the map land in NotFound. Search is unused by the verifier today
// (the plan §4.3 reserves it for the empty-source-steps fallback) but is
// implemented for completeness.
type stubSchemaProvider struct {
	tables   map[string]ai.LookupTable
	lookErr  error
	lookups  int
	notFound []string
}

func (s *stubSchemaProvider) Lookup(_ context.Context, refs []string) (ai.LookupResult, error) {
	s.lookups++
	if s.lookErr != nil {
		return ai.LookupResult{}, s.lookErr
	}
	var res ai.LookupResult
	for _, r := range refs {
		if t, ok := s.tables[r]; ok {
			res.Tables = append(res.Tables, t)
		} else {
			res.NotFound = append(res.NotFound, r)
		}
	}
	s.notFound = append(s.notFound, res.NotFound...)
	return res, nil
}

func (s *stubSchemaProvider) Search(_ context.Context, _ string, _ int) ([]ai.SearchHit, error) {
	return nil, nil
}

func newValidatorWithSchemaProvider(
	t *testing.T,
	provider ai.SchemaProvider,
	llm *testutil.MockLLMProvider,
	exec SelfHealingExecutor,
) *InsightValidator {
	t.Helper()
	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:       aiClient,
		Warehouse:      testutil.NewMockWarehouseProvider("test_dataset"),
		Executor:       exec,
		SchemaProvider: provider,
		Dataset:        "test_dataset",
	})
	v.SetExplorationLog([]models.ExplorationStep{})
	return v
}

// TestInsightValidator_LookupSchemaActionSatisfied — first LLM round returns
// a lookup_schema action; the provider serves it; second round returns the
// final query. Both rounds and the lookup count are observable.
func TestInsightValidator_LookupSchemaActionSatisfied(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["test_dataset.events"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(DISTINCT user_id) AS count FROM test_dataset.events"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}

	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"test_dataset.events": {
				Table:    "test_dataset.events",
				RowCount: 1000,
				Columns: []ai.LookupColumn{
					{Name: "user_id", Type: "INT64"},
					{Name: "event_time", Type: "TIMESTAMP"},
				},
			},
		},
	}

	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(42)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)

	insights := []models.Insight{
		{ID: "1", Name: "active users", AffectedCount: 42, AnalysisArea: "engagement"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Fatalf("verifier should succeed, got: %s", results[0].QueryError)
	}
	if provider.lookups != 1 {
		t.Errorf("expected 1 lookup, got %d", provider.lookups)
	}
	if !strings.Contains(exec.lastOpts.VerificationContext, "test_dataset.events") {
		t.Errorf("FixOpts.VerificationContext should include the looked-up table, got:\n%s", exec.lastOpts.VerificationContext)
	}
	if !strings.Contains(exec.lastOpts.VerificationContext, "user_id") {
		t.Errorf("FixOpts.VerificationContext should include the looked-up columns, got:\n%s", exec.lastOpts.VerificationContext)
	}
}

// TestInsightValidator_LookupSchemaBudgetExceeded — LLM keeps issuing
// lookups beyond MaxLookupsPerVerification. After the budget runs out, one
// forced query round produces the SQL.
func TestInsightValidator_LookupSchemaBudgetExceeded(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	for i := 0; i < MaxLookupsPerVerification; i++ {
		llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
			Content: `{"lookup_schema": ["test_dataset.events"]}`,
			Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
		})
	}
	// Forced final round — bare SQL accepted via bareSQLFallback.
	llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM test_dataset.events",
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	})

	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"test_dataset.events": {
				Table:    "test_dataset.events",
				RowCount: 1,
				Columns:  []ai.LookupColumn{{Name: "user_id", Type: "INT64"}},
			},
		},
	}

	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Fatalf("forced final round should produce SQL; status=%s reason=%s", results[0].Status, results[0].Reasoning)
	}
	// Repeated lookup_schema requests still invoke SchemaProvider.Lookup on
	// every round — `mergeLookups` only dedupes the rendered accumulator, not
	// the upstream provider call. What matters here is that the validator
	// stays within the lookup budget, does not loop forever, and eventually
	// produces a query.
	if provider.lookups < 1 || provider.lookups > MaxLookupsPerVerification {
		t.Errorf("lookups = %d, want 1..%d", provider.lookups, MaxLookupsPerVerification)
	}
}

// TestInsightValidator_LookupSchemaForcedRoundEmits_NoQuery returns a hard
// error rather than re-using a fallback when the forced round still doesn't
// emit a query.
func TestInsightValidator_LookupSchemaForcedRound_NoQueryEmitted(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	for i := 0; i < MaxLookupsPerVerification; i++ {
		llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
			Content: `{"lookup_schema": ["test_dataset.events"]}`,
			Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
		})
	}
	// Forced final round — model still doesn't emit SQL.
	llm.ResponseQueue = append(llm.ResponseQueue, &gollm.ChatResponse{
		Content: `{"lookup_schema": ["test_dataset.events"]}`,
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	})

	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"test_dataset.events": {Table: "test_dataset.events", RowCount: 1},
		},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status != "error" {
		t.Errorf("expected status=error when forced round still emits no query, got %q", results[0].Status)
	}
	if !strings.Contains(results[0].Reasoning, "exhausted lookup budget") {
		t.Errorf("reasoning should mention exhausted lookup budget, got: %s", results[0].Reasoning)
	}
}

// TestInsightValidator_NotFoundFedBackToModel — when a SchemaProvider lookup
// reports NotFound, the next round's prompt must explicitly tell the model
// which refs were missing, so it can self-correct (retry a misspelled name,
// query without that table, etc). Without this signal the model has no way
// to recover on the next round and the loop wastes its budget.
func TestInsightValidator_NotFoundFedBackToModel(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["test_dataset.does_not_exist"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(*) AS count FROM test_dataset.events"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	provider := &stubSchemaProvider{tables: map[string]ai.LookupTable{}}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)

	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	v.ValidateInsights(context.Background(), insights)

	// Second LLM call's prompt should mention the not-found ref.
	if len(llm.Calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(llm.Calls))
	}
	secondPrompt := ""
	for _, m := range llm.Calls[1].Request.Messages {
		secondPrompt += m.Content
	}
	if !strings.Contains(secondPrompt, "test_dataset.does_not_exist") {
		t.Errorf("second-round prompt should surface the not-found ref so the model can self-correct, got:\n%s", secondPrompt)
	}
	if !strings.Contains(secondPrompt, "Lookup Notices") {
		t.Errorf("second-round prompt should include the Lookup Notices section, got:\n%s", secondPrompt)
	}
}

// TestInsightValidator_LookupSchemaNotFound — provider reports a ref it can't
// resolve. The validator does not fail; the model is expected to course-
// correct on the next round (here it issues the query directly).
func TestInsightValidator_LookupSchemaNotFound(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["test_dataset.does_not_exist"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(*) AS count FROM test_dataset.events"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	provider := &stubSchemaProvider{tables: map[string]ai.LookupTable{}}

	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Errorf("missing table in lookup should not abort verification, got: %s", results[0].QueryError)
	}
	if len(provider.notFound) == 0 {
		t.Errorf("provider should have recorded NotFound for the unresolved ref")
	}
}

// TestInsightValidator_NoSchemaProvider_FallsBackToSingleShot — when the
// SchemaProvider is nil, the verifier does a single-shot LLM call (legacy
// Layer 1 + 2 path) and emits the query directly with no tool loop.
func TestInsightValidator_NoSchemaProvider_FallsBackToSingleShot(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.DefaultResponse = &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM test_dataset.events",
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	aiClient, _ := ai.New(llm, "test-model")
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:       aiClient,
		Warehouse:      testutil.NewMockWarehouseProvider("test_dataset"),
		Executor:       exec,
		SchemaProvider: nil, // explicit
		Dataset:        "test_dataset",
	})
	v.SetExplorationLog([]models.ExplorationStep{})
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)

	if results[0].Status == "error" {
		t.Fatalf("single-shot fallback should succeed, got: %s", results[0].QueryError)
	}
	// Only one LLM call — no tool loop ran.
	if len(llm.Calls) != 1 {
		t.Errorf("expected 1 LLM call (no tool loop), got %d", len(llm.Calls))
	}
}

// TestInsightValidator_LookupResultsLandInFixOpts — the lookups gathered by
// the loop must reach the SQL fixer through FixOpts.VerificationContext, so
// the fixer benefits from the same evidence on retry.
func TestInsightValidator_LookupResultsLandInFixOpts(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["dbo.STHAR"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(*) AS count FROM dbo.STHAR"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"dbo.STHAR": {
				Table: "dbo.STHAR",
				Columns: []ai.LookupColumn{
					{Name: "STHAR_TARIH", Type: "DATE"},
					{Name: "STHAR_GCMIK", Type: "DECIMAL"},
				},
				RowCount: 9999,
			},
		},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(9999)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 9999, AnalysisArea: "x"},
	}
	v.ValidateInsights(context.Background(), insights)

	if exec.lastOpts.VerificationContext == "" {
		t.Fatal("FixOpts.VerificationContext should be populated when lookups were gathered")
	}
	for _, want := range []string{"dbo.STHAR", "STHAR_TARIH", "STHAR_GCMIK"} {
		if !strings.Contains(exec.lastOpts.VerificationContext, want) {
			t.Errorf("FixOpts.VerificationContext missing %q, got:\n%s", want, exec.lastOpts.VerificationContext)
		}
	}
}

// TestInsightValidator_SetSchemaProviderEnablesToolLoop — wiring contract
// for the orchestrator's construct-then-set pattern. Asserts the toggle
// works: a validator built without a SchemaProvider can have one wired
// later via SetSchemaProvider, and the next ValidateInsights call exercises
// the tool loop instead of the single-shot path.
func TestInsightValidator_SetSchemaProviderEnablesToolLoop(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["ds.events"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(*) AS count FROM ds.events"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"ds.events": {Table: "ds.events", RowCount: 1},
		},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	aiClient, _ := ai.New(llm, "test-model")
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  exec,
		Dataset:   "ds",
		// SchemaProvider intentionally nil at construction.
	})
	v.SetExplorationLog([]models.ExplorationStep{})
	v.SetSchemaProvider(provider) // toggle on after construction

	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x"},
	}
	v.ValidateInsights(context.Background(), insights)

	if provider.lookups != 1 {
		t.Errorf("expected 1 lookup after SetSchemaProvider toggled on the loop, got %d", provider.lookups)
	}
	if len(llm.Calls) != 2 {
		t.Errorf("expected 2 LLM calls (lookup + query), got %d", len(llm.Calls))
	}
}

// Sanity: the parser allow-list rejects "complete" responses from the
// verifier loop — the model isn't allowed to "complete" mid-verify.
func TestParseAction_VerifierAllowListRejectsComplete(t *testing.T) {
	_, err := ai.ParseAction(`{"done": true, "summary": "wrap"}`, []string{"lookup_schema", "query_data"})
	if err == nil {
		t.Fatal("ParseAction should reject 'complete' when not in allow-list")
	}
}

// Sanity: the parser allow-list accepts what the verifier loop actually wants.
func TestParseAction_VerifierAllowListAcceptsLookupAndQuery(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"lookup_schema", `{"lookup_schema": ["a.b"]}`},
		{"query_data", `{"query": "SELECT 1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ai.ParseAction(tt.body, []string{"lookup_schema", "query_data"}); err != nil {
				t.Errorf("ParseAction rejected expected action: %v", err)
			}
		})
	}
}

// Defensive: ensure the warehouse-side path for the validator carries
// FixOpts forwarded correctly even when the validator runs without the
// executor adapter (raw warehouse Query path stays in v.warehouse.Query).
// We assert the validator still returns a result when only the warehouse
// is present and no SchemaProvider is wired — Layer 3 doesn't break the
// "no executor" fallback.
func TestInsightValidator_NoExecutorPathStillWorksWithSchemaProvider(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.DefaultResponse = &gollm.ChatResponse{
		Content: "SELECT COUNT(*) AS count FROM test_dataset.events",
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.DefaultResult = &gowarehouse.QueryResult{Rows: []map[string]interface{}{{"count": int64(7)}}}
	aiClient, _ := ai.New(llm, "test-model")
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		// SchemaProvider intentionally nil — single-shot path.
		Dataset: "test_dataset",
	})
	v.SetExplorationLog([]models.ExplorationStep{})
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 7, AnalysisArea: "x"},
	}
	results := v.ValidateInsights(context.Background(), insights)
	if results[0].Status == "error" {
		t.Errorf("warehouse-only path should still succeed: %s", results[0].QueryError)
	}
}

// Defensive: assert the queryexec.FixOpts forwarded by the validator carries
// non-empty VerificationContext when both source steps and lookups exist.
func TestInsightValidator_FixOptsCombinesSourceAndLookups(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.ResponseQueue = []*gollm.ChatResponse{
		{Content: `{"lookup_schema": ["test_dataset.t2"]}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: `{"query": "SELECT COUNT(*) AS count FROM test_dataset.t1 JOIN test_dataset.t2 USING (id)"}`, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	provider := &stubSchemaProvider{
		tables: map[string]ai.LookupTable{
			"test_dataset.t2": {Table: "test_dataset.t2", Columns: []ai.LookupColumn{{Name: "id"}, {Name: "country"}}},
		},
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(1)}}}
	v := newValidatorWithSchemaProvider(t, provider, llm, exec)
	v.SetExplorationLog([]models.ExplorationStep{
		{Step: 7, Action: "query_data", QueryPurpose: "broad", Query: "SELECT id FROM test_dataset.t1"},
	})
	_ = queryexec.FixOpts{} // import sanity
	insights := []models.Insight{
		{ID: "1", Name: "n", AffectedCount: 1, AnalysisArea: "x", SourceSteps: []int{7}},
	}
	v.ValidateInsights(context.Background(), insights)

	if !strings.Contains(exec.lastOpts.VerificationContext, "Step 7") {
		t.Errorf("VerificationContext should contain the cited source step, got:\n%s", exec.lastOpts.VerificationContext)
	}
	if !strings.Contains(exec.lastOpts.VerificationContext, "test_dataset.t2") {
		t.Errorf("VerificationContext should contain the looked-up table, got:\n%s", exec.lastOpts.VerificationContext)
	}
}
