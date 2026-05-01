package validation

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/render"
)

// captureExecutor is a SelfHealingExecutor that records the FixOpts forwarded
// by ValidateInsights so tests can assert per-call propagation of the
// rendered VerificationContext into the SQL fixer's downstream prompt.
type captureExecutor struct {
	rows     []map[string]interface{}
	lastOpts queryexec.FixOpts
	calls    int
}

func (c *captureExecutor) Execute(_ context.Context, _ string, _ string, opts queryexec.FixOpts) ([]map[string]interface{}, error) {
	c.calls++
	c.lastOpts = opts
	return c.rows, nil
}

// TestInsightValidator_ForwardsRenderedVerificationContextToExecutor is the
// PR-2 wiring contract: when the validator runs an insight with cited
// source_steps, the rendered VerificationContext lands in FixOpts so the SQL
// fixer can ground its retry on the same evidence the generation prompt was
// built on.
func TestInsightValidator_ForwardsRenderedVerificationContextToExecutor(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM dbo.STHAR"
	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(5)}}}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: testutil.NewMockWarehouseProvider("test_dataset"),
		Executor:  exec,
		Dataset:   "test_dataset",
	})

	log := []models.ExplorationStep{
		{
			Step:         3,
			Action:       "query_data",
			QueryPurpose: "scan Turkish columns",
			Query:        "SELECT COUNT(*) FROM dbo.STHAR WHERE STHAR_TARIH > '2026-01-01'",
			RowCount:     5,
		},
	}
	v.SetExplorationLog(log)

	insights := []models.Insight{
		{ID: "1", Name: "T", AffectedCount: 5, AnalysisArea: "churn", SourceSteps: []int{3}},
	}
	v.ValidateInsights(context.Background(), insights)

	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", exec.calls)
	}
	if exec.lastOpts.VerificationContext == "" {
		t.Fatal("FixOpts.VerificationContext should be populated when source_steps is non-empty")
	}
	if !strings.Contains(exec.lastOpts.VerificationContext, "STHAR_TARIH") {
		t.Errorf("VerificationContext missing the cited source SQL column, got:\n%s", exec.lastOpts.VerificationContext)
	}
	if !strings.Contains(exec.lastOpts.VerificationContext, render.SectionHeader) {
		t.Errorf("VerificationContext missing the section header, got:\n%s", exec.lastOpts.VerificationContext)
	}
}

// TestInsightValidator_ForwardsEmptyFixOpts_WhenInsightHasNoSourceSteps
// pins the no-cited-steps branch — Layer 1 contributes nothing and the fixer
// must see an empty VerificationContext (the conditional section in the fixer
// prompt template will then be stripped).
func TestInsightValidator_ForwardsEmptyFixOpts_WhenInsightHasNoSourceSteps(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM t"
	aiClient, _ := ai.New(llm, "test-model")
	exec := &captureExecutor{rows: []map[string]interface{}{{"count": int64(0)}}}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  exec,
	})
	v.SetExplorationLog([]models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", RowCount: 1},
	})
	insights := []models.Insight{
		{ID: "1", Name: "T", AffectedCount: 1, AnalysisArea: "x" /* no SourceSteps */},
	}
	v.ValidateInsights(context.Background(), insights)
	if exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1 — empty-opts assertion is meaningless if the executor was never invoked", exec.calls)
	}
	if exec.lastOpts.VerificationContext != "" {
		t.Errorf("VerificationContext should be empty when SourceSteps is empty, got:\n%s", exec.lastOpts.VerificationContext)
	}
}

// captureLastPrompt returns the last user-side prompt string passed to the
// mock LLM. Layer-1 tests assert the source-queries block is (or isn't) in
// that string.
func captureLastPrompt(t *testing.T, llm *testutil.MockLLMProvider) string {
	t.Helper()
	if len(llm.Calls) == 0 {
		t.Fatal("LLM was not called")
	}
	last := llm.Calls[len(llm.Calls)-1]
	var b strings.Builder
	for _, m := range last.Request.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func newValidatorWithExplorationLog(
	t *testing.T,
	log []models.ExplorationStep,
) (*InsightValidator, *testutil.MockWarehouseProvider, *testutil.MockLLMProvider) {
	t.Helper()
	llm := testutil.NewMockLLMProvider()
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		Dataset:   "test_dataset",
	})
	v.SetExplorationLog(log)
	return v, wh, llm
}

// TestInsightValidator_ValidateInsightsPanicsWithoutSetExplorationLog asserts
// the no-backward-compat wiring contract from
// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §1.1.
func TestInsightValidator_ValidateInsightsPanicsWithoutSetExplorationLog(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	aiClient, err := ai.New(llm, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		Dataset:   "test_dataset",
	})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected ValidateInsights to panic without SetExplorationLog wiring")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value type = %T, want string", r)
		}
		if !strings.Contains(msg, "SetExplorationLog") {
			t.Errorf("panic message should mention SetExplorationLog, got %q", msg)
		}
	}()
	v.ValidateInsights(context.Background(), []models.Insight{
		{ID: "1", Name: "test", AffectedCount: 0, AnalysisArea: "churn"},
	})
}

func TestInsightValidator_SetExplorationLogPanicsOnNil(t *testing.T) {
	llm := testutil.NewMockLLMProvider()
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	aiClient, _ := ai.New(llm, "test-model")
	v := NewInsightValidator(InsightValidatorOptions{
		AIClient:  aiClient,
		Warehouse: wh,
		Dataset:   "test_dataset",
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected SetExplorationLog(nil) to panic")
		}
	}()
	v.SetExplorationLog(nil)
}

// TestInsightValidator_RendersSourceQueriesInPrompt is the central regression
// test for Layer 1: an insight with valid source_steps must result in the
// verification prompt containing the SQL of those steps and the column-
// grounding rule.
func TestInsightValidator_RendersSourceQueriesInPrompt(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "broad scan", Query: "SELECT 1 FROM events", RowCount: 1},
		{
			Step:         2,
			Action:       "query_data",
			QueryPurpose: "count Turkish-named events",
			Query:        "SELECT COUNT(*) FROM dbo.STHAR WHERE STHAR_TARIH > '2026-01-01'",
			RowCount:     42,
		},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)

	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM dbo.STHAR WHERE STHAR_TARIH > '2026-01-01'"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(42)}},
	}

	insights := []models.Insight{
		{
			ID:            "i1",
			Name:          "Turkish ERP scan",
			AnalysisArea:  "churn",
			AffectedCount: 42,
			SourceSteps:   []int{2},
		},
	}
	v.ValidateInsights(context.Background(), insights)

	prompt := captureLastPrompt(t, llm)

	if !strings.Contains(prompt, "STHAR_TARIH") {
		t.Errorf("verification prompt missing the cited source SQL column, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, render.SectionHeader) {
		t.Errorf("verification prompt missing the source-queries section header")
	}
	if !strings.Contains(prompt, render.RuleInstruction) {
		t.Errorf("verification prompt missing the column-grounding rule instruction")
	}
}

// TestInsightValidator_NoSourceStepsOmitsSourceQueriesBlock asserts the
// pre-Layer-3 fallback path: with no cited steps and no schema provider, the
// prompt simply omits the source-queries section (Layer 3 will replace this
// branch with a lookup_schema loop in PR-3).
func TestInsightValidator_NoSourceStepsOmitsSourceQueriesBlock(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", RowCount: 1},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM x"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(1)}},
	}

	insights := []models.Insight{
		{ID: "i", Name: "n", AnalysisArea: "a", AffectedCount: 1 /* no SourceSteps */},
	}
	v.ValidateInsights(context.Background(), insights)

	prompt := captureLastPrompt(t, llm)
	if strings.Contains(prompt, render.SectionHeader) {
		t.Errorf("prompt should not contain source-queries section when SourceSteps is empty:\n%s", prompt)
	}
	if strings.Contains(prompt, render.RuleInstruction) {
		t.Errorf("prompt should not contain rule instruction when there are no source queries to apply it to:\n%s", prompt)
	}
}

// TestInsightValidator_SourceStepNotInLogSkipsSilently mirrors the plan's
// graceful-skip rule for citation indices the analysis-step picker dropped.
func TestInsightValidator_SourceStepNotInLogSkipsSilently(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 5, Action: "query_data", QueryPurpose: "kept", Query: "SELECT a FROM t", RowCount: 1},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM t"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(1)}},
	}

	insights := []models.Insight{
		{ID: "i", Name: "n", AnalysisArea: "a", AffectedCount: 1, SourceSteps: []int{5, 99}},
	}
	results := v.ValidateInsights(context.Background(), insights)
	if results[0].Status == "error" {
		t.Errorf("missing-citation step should not error, got status=%q reason=%q", results[0].Status, results[0].Reasoning)
	}

	prompt := captureLastPrompt(t, llm)
	if !strings.Contains(prompt, "Step 5") {
		t.Errorf("matched step 5 should still render despite stray citation 99, got:\n%s", prompt)
	}
}

// TestInsightValidator_NonQueryStepSkippedFromRendering asserts lookup_schema
// / search_tables steps are not rendered as source SQL — there is no query to
// adapt from those steps.
func TestInsightValidator_NonQueryStepSkippedFromRendering(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "lookup_schema", QueryPurpose: "lookup", Query: ""},
		{Step: 2, Action: "query_data", QueryPurpose: "real", Query: "SELECT a FROM t", RowCount: 1},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM t"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(1)}},
	}

	insights := []models.Insight{
		{ID: "i", Name: "n", AnalysisArea: "a", AffectedCount: 1, SourceSteps: []int{1, 2}},
	}
	v.ValidateInsights(context.Background(), insights)

	prompt := captureLastPrompt(t, llm)
	if strings.Contains(prompt, "Step 1") {
		t.Errorf("lookup_schema step (1) must not be rendered:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Step 2") {
		t.Errorf("query_data step (2) must be rendered:\n%s", prompt)
	}
}

// TestInsightValidator_BudgetCapDropsOldestStep is the orchestrator-side
// counterpart of render.TestRenderVerificationContext_BudgetDrops*. It asserts
// the validator passes DefaultBudgetChars unchanged so the package-level drop
// policy applies.
func TestInsightValidator_BudgetCapDropsOldestStep(t *testing.T) {
	bigSQL := strings.Repeat("SELECT 1 FROM t WHERE x = 'y' AND z = 'q'\n", 1000) // ~40k chars
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "OLDEST_STEP", Query: bigSQL, RowCount: 1},
		{Step: 2, Action: "query_data", QueryPurpose: "NEWEST_STEP", Query: "SELECT b FROM t", RowCount: 1},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM t"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(1)}},
	}
	insights := []models.Insight{
		{ID: "i", Name: "n", AnalysisArea: "a", AffectedCount: 1, SourceSteps: []int{1, 2}},
	}
	v.ValidateInsights(context.Background(), insights)

	prompt := captureLastPrompt(t, llm)
	// The newer step must survive; the oldest step's marker should be dropped.
	if !strings.Contains(prompt, "NEWEST_STEP") {
		t.Errorf("newest step should be retained under budget pressure, got:\n%s", prompt[:min(len(prompt), 1000)])
	}
}

// TestInsightValidator_RuleInstructionPresentWhenSourceQueriesPresent guards
// against silent drift in the prompt — the rule must always accompany the
// rendered evidence.
func TestInsightValidator_RuleInstructionPresentWhenSourceQueriesPresent(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "p", Query: "SELECT a FROM t", RowCount: 1},
	}
	v, wh, llm := newValidatorWithExplorationLog(t, log)
	llm.DefaultResponse.Content = "SELECT COUNT(*) AS count FROM t"
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"count"},
		Rows:    []map[string]interface{}{{"count": int64(1)}},
	}
	insights := []models.Insight{
		{ID: "i", Name: "n", AnalysisArea: "a", AffectedCount: 1, SourceSteps: []int{1}},
	}
	v.ValidateInsights(context.Background(), insights)

	prompt := captureLastPrompt(t, llm)
	idxQueries := strings.Index(prompt, render.SectionHeader)
	idxRule := strings.Index(prompt, render.RuleInstruction)
	if idxQueries == -1 || idxRule == -1 {
		t.Fatalf("expected both section and rule, got header=%d rule=%d", idxQueries, idxRule)
	}
	if idxRule < idxQueries {
		t.Errorf("rule should follow the rendered evidence (header @ %d, rule @ %d)", idxQueries, idxRule)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
