package queryexec

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// mockFixer implements the SQLFixer interface for the queryexec tests. It
// lives here rather than in testutil so testutil does not need to import
// queryexec (avoiding the cycle queryexec_test → testutil → queryexec).
type mockFixer struct {
	FixedQuery string
	Error      error
	Calls      int
	LastOpts   FixOpts
}

func (m *mockFixer) FixSQL(_ context.Context, _ string, _ string, _ int, opts FixOpts) (FixResult, error) {
	m.Calls++
	m.LastOpts = opts
	if m.Error != nil {
		return FixResult{}, m.Error
	}
	if m.FixedQuery != "" {
		return FixResult{
			FixedSQL:     m.FixedQuery,
			Prompt:       "[system]\nfix prompt\n[user]\nfix this",
			Response:     m.FixedQuery,
			InputTokens:  10,
			OutputTokens: 5,
			DurationMs:   1,
		}, nil
	}
	return FixResult{
		FixedSQL:     "SELECT fixed FROM `dataset.table` WHERE app_id = 'test'",
		Prompt:       "[system]\nfix prompt\n[user]\nfix this",
		Response:     "SELECT fixed FROM `dataset.table` WHERE app_id = 'test'",
		InputTokens:  10,
		OutputTokens: 5,
		DurationMs:   1,
	}, nil
}

func TestExecuteSuccess(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
	})

	result, err := executor.Execute(context.Background(), "SELECT 1", "test")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.RowCount == 0 {
		t.Error("should return rows")
	}
	if result.Fixed {
		t.Error("should not be marked as fixed")
	}
}

func TestExecuteWithFilter(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wh,
		MaxRetries:  3,
		FilterField: "app_id",
		FilterValue: "test-app",
	})

	// Query with filter field present — should pass
	_, err := executor.Execute(context.Background(),
		"SELECT * FROM t WHERE app_id = 'test-app'", "test")
	if err != nil {
		t.Fatalf("should pass with filter: %v", err)
	}

	// Query without filter field — should fail
	_, err = executor.Execute(context.Background(),
		"SELECT * FROM t", "test")
	if err == nil {
		t.Error("should fail without filter field in query")
	}
}

func TestExecuteNoFilterRequired(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
		// No FilterField/FilterValue — no filter required
	})

	// Any query should pass
	_, err := executor.Execute(context.Background(),
		"SELECT * FROM t", "test")
	if err != nil {
		t.Fatalf("should pass without filter requirement: %v", err)
	}
}

func TestExecuteRetryWithFix(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	callCount := 0
	wh.QueryResults["bad_query"] = nil // will use default
	// Make first call fail, second succeed
	origQuery := func(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("syntax error near 'BAD'")
		}
		return &gowarehouse.QueryResult{
			Columns: []string{"count"},
			Rows:    []map[string]interface{}{{"count": 42}},
		}, nil
	}
	// Override Query method via a wrapper
	wrapper := &queryWrapper{fn: origQuery, provider: wh}

	fixer := &mockFixer{
		FixedQuery: "SELECT count(*) as count FROM `test_dataset.table` WHERE app_id = 'test'",
	}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wrapper,
		SQLFixer:    fixer,
		MaxRetries:  3,
		FilterField: "app_id",
		FilterValue: "test",
	})

	result, err := executor.Execute(context.Background(),
		"SELECT BAD FROM t WHERE app_id = 'test'", "test")
	if err != nil {
		t.Fatalf("should succeed after fix: %v", err)
	}
	if !result.Fixed {
		t.Error("should be marked as fixed")
	}
	if result.FixAttempts != 1 {
		t.Errorf("FixAttempts = %d, want 1", result.FixAttempts)
	}
	if fixer.Calls != 1 {
		t.Errorf("fixer should be called once, got %d", fixer.Calls)
	}
}

func TestExecuteMaxRetries(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("persistent error")

	fixer := &mockFixer{}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		SQLFixer:   fixer,
		MaxRetries: 2,
	})

	_, err := executor.Execute(context.Background(), "SELECT 1", "test")
	if err == nil {
		t.Error("should fail after max retries")
	}
}

func TestExecuteNoFixer(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("error")

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
		// No SQLFixer
	})

	_, err := executor.Execute(context.Background(), "SELECT 1", "test")
	if err == nil {
		t.Error("should fail when no fixer available")
	}
}

func TestExecuteWithHistory(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
	})

	result, history := executor.ExecuteWithHistory(context.Background(), "SELECT 1", "test purpose")

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if history == nil {
		t.Fatal("history should not be nil")
	}
	if !history.Success {
		t.Error("history should show success")
	}
	if history.Purpose != "test purpose" {
		t.Errorf("purpose = %q, want %q", history.Purpose, "test purpose")
	}
}

func TestVerifyFilter(t *testing.T) {
	executor := &QueryExecutor{
		filterField: "tenant_id",
		filterValue: "abc",
	}

	if err := executor.verifyFilter("SELECT * FROM t WHERE tenant_id = 'abc'"); err != nil {
		t.Errorf("should pass: %v", err)
	}

	if err := executor.verifyFilter("SELECT * FROM t"); err == nil {
		t.Error("should fail without filter field")
	}

	// Case insensitive
	if err := executor.verifyFilter("SELECT * FROM t WHERE TENANT_ID = 'abc'"); err != nil {
		t.Errorf("should pass case-insensitive: %v", err)
	}
}

func TestVerifyFilterEmpty(t *testing.T) {
	executor := &QueryExecutor{} // No filter configured

	if err := executor.verifyFilter("SELECT * FROM anything"); err != nil {
		t.Errorf("should pass when no filter configured: %v", err)
	}
}

// queryWrapper lets us override Query while keeping other methods.
type queryWrapper struct {
	fn       func(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error)
	provider *testutil.MockWarehouseProvider
}

func (w *queryWrapper) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	return w.fn(ctx, query, params)
}
func (w *queryWrapper) ListTables(ctx context.Context) ([]string, error) {
	return w.provider.ListTables(ctx)
}
func (w *queryWrapper) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	return w.provider.GetTableSchema(ctx, table)
}
func (w *queryWrapper) GetDataset() string      { return w.provider.GetDataset() }
func (w *queryWrapper) SQLDialect() string      { return w.provider.SQLDialect() }
func (w *queryWrapper) QuoteRef(parts ...string) string {
	return w.provider.QuoteRef(parts...)
}
func (w *queryWrapper) SQLFixPrompt() string    { return w.provider.SQLFixPrompt() }
func (w *queryWrapper) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	return w.provider.ListTables(ctx)
}
func (w *queryWrapper) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	return w.provider.GetTableSchema(ctx, table)
}
func (w *queryWrapper) ValidateReadOnly(ctx context.Context) error { return nil }
func (w *queryWrapper) HealthCheck(ctx context.Context) error { return nil }
func (w *queryWrapper) Close() error            { return nil }

func TestExecute_EmptyQuery(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 1,
	})

	// Empty query should still be executed (warehouse decides if it's valid)
	result, err := executor.Execute(context.Background(), "", "test empty")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.OriginalQuery != "" {
		t.Errorf("OriginalQuery = %q, want empty", result.OriginalQuery)
	}
}

func TestExecuteWithHistory_Error(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("connection refused")

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 0, // No retries
	})

	result, history := executor.ExecuteWithHistory(context.Background(), "SELECT 1", "test error")

	// Executor now returns a non-nil partial ExecuteResult on every
	// error branch so callers can read accumulated FixHistory (failed-
	// fixer attempts in particular). For this case there's no fixer
	// configured and the warehouse errored first try, so FixHistory is
	// empty, but the result itself must be present and reflect the
	// original query.
	if result == nil {
		t.Fatal("result should be a non-nil partial on error so callers can read FixHistory")
	}
	if result.OriginalQuery != "SELECT 1" {
		t.Errorf("OriginalQuery = %q, want 'SELECT 1'", result.OriginalQuery)
	}
	if len(result.FixHistory) != 0 {
		t.Errorf("FixHistory should be empty when no fixer is configured, got %d entries", len(result.FixHistory))
	}

	if history == nil {
		t.Fatal("history should not be nil even on error")
	}
	if history.Success {
		t.Error("history.Success should be false on error")
	}
	if history.Error == "" {
		t.Error("history.Error should be set")
	}
	if history.Query != "SELECT 1" {
		t.Errorf("history.Query = %q, want 'SELECT 1'", history.Query)
	}
	if history.Purpose != "test error" {
		t.Errorf("history.Purpose = %q, want 'test error'", history.Purpose)
	}
}

func TestExecuteWithHistory_Success_Fields(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
	})

	result, history := executor.ExecuteWithHistory(context.Background(), "SELECT COUNT(*) FROM users", "count users")

	if result == nil {
		t.Fatal("result should not be nil")
	}
	if history == nil {
		t.Fatal("history should not be nil")
	}
	if !history.Success {
		t.Error("history.Success should be true")
	}
	if history.Query != "SELECT COUNT(*) FROM users" {
		t.Errorf("history.Query = %q", history.Query)
	}
	if history.Purpose != "count users" {
		t.Errorf("history.Purpose = %q", history.Purpose)
	}
	if history.RowsReturned != result.RowCount {
		t.Errorf("history.RowsReturned = %d, want %d", history.RowsReturned, result.RowCount)
	}
	if history.ExecutionTimeMs != result.ExecutionTimeMs {
		t.Errorf("history.ExecutionTimeMs = %d, want %d", history.ExecutionTimeMs, result.ExecutionTimeMs)
	}
	if history.ExecutedAt.IsZero() {
		t.Error("history.ExecutedAt should be set")
	}
}

func TestNewQueryExecutor_DefaultMaxRetries(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse: wh,
		// MaxRetries not set — should default to 5
	})

	if executor.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5 (default)", executor.maxRetries)
	}
}

func TestNewQueryExecutor_CustomMaxRetries(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 10,
	})

	if executor.maxRetries != 10 {
		t.Errorf("maxRetries = %d, want 10", executor.maxRetries)
	}
}

func TestExecutor_SetStep(t *testing.T) {
	executor := &QueryExecutor{}

	executor.SetStep(5)
	if executor.currentStep != 5 {
		t.Errorf("currentStep = %d, want 5", executor.currentStep)
	}
}

func TestExecutor_SetPhase(t *testing.T) {
	executor := &QueryExecutor{}

	executor.SetPhase("analysis")
	if executor.currentPhase != "analysis" {
		t.Errorf("currentPhase = %q, want analysis", executor.currentPhase)
	}
}

func TestExecuteResult_Fields(t *testing.T) {
	result := ExecuteResult{
		Data:            []map[string]interface{}{{"count": 42}},
		RowCount:        1,
		ExecutionTimeMs: 200,
		FixAttempts:     2,
		Fixed:           true,
		OriginalQuery:   "SELECT BAD",
		FinalQuery:      "SELECT count(*) FROM t",
		Errors:          []string{"syntax error", "column not found"},
	}

	if result.RowCount != 1 {
		t.Errorf("RowCount = %d, want 1", result.RowCount)
	}
	if !result.Fixed {
		t.Error("Fixed should be true")
	}
	if result.FixAttempts != 2 {
		t.Errorf("FixAttempts = %d, want 2", result.FixAttempts)
	}
	if result.OriginalQuery != "SELECT BAD" {
		t.Errorf("OriginalQuery = %q", result.OriginalQuery)
	}
	if result.FinalQuery != "SELECT count(*) FROM t" {
		t.Errorf("FinalQuery = %q", result.FinalQuery)
	}
	if len(result.Errors) != 2 {
		t.Errorf("Errors = %d, want 2", len(result.Errors))
	}
}

func TestExecute_FilterSecurityViolation(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wh,
		MaxRetries:  3,
		FilterField: "tenant_id",
		FilterValue: "abc",
	})

	// Query missing required filter field
	_, err := executor.Execute(context.Background(), "SELECT * FROM users", "test")
	if err == nil {
		t.Error("should fail when required filter field is missing")
	}
	if !contains(err.Error(), "security violation") {
		t.Errorf("error = %q, should contain 'security violation'", err.Error())
	}
}

func TestExecute_FixerFailure(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("table not found")

	fixer := &mockFixer{
		Error: fmt.Errorf("fixer broke"),
	}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		SQLFixer:   fixer,
		MaxRetries: 3,
	})

	_, err := executor.Execute(context.Background(), "SELECT 1", "test")
	if err == nil {
		t.Error("should fail when fixer fails")
	}
	if !contains(err.Error(), "failed to fix SQL") {
		t.Errorf("error = %q, should mention fixer failure", err.Error())
	}
}

func TestExecute_FixedQueryFailsFilterCheck(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("syntax error")

	fixer := &mockFixer{
		// Returns a fixed query that doesn't include the required filter field
		FixedQuery: "SELECT * FROM users",
	}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wh,
		SQLFixer:    fixer,
		MaxRetries:  3,
		FilterField: "app_id",
		FilterValue: "test",
	})

	_, err := executor.Execute(context.Background(),
		"SELECT * FROM users WHERE app_id = 'test'", "test")
	if err == nil {
		t.Error("should fail when fixed query doesn't pass filter check")
	}
	if !contains(err.Error(), "security violation") {
		t.Errorf("error = %q, should mention security violation", err.Error())
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestExecuteWithFixOpts_ForwardsOptsToFixerOnRetry pins the per-call FixOpts
// propagation: the validator's rendered VerificationContext must reach the
// SQL fixer on every retry attempt so the LLM sees the same column-grounding
// evidence the verification prompt was built on. Background:
// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.2.
func TestExecuteWithFixOpts_ForwardsOptsToFixerOnRetry(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("Invalid column name 'TARIiH'")

	fixer := &mockFixer{
		FixedQuery: "SELECT COUNT(*) AS count FROM `test_dataset.t` WHERE app_id = 'test'",
	}
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		SQLFixer:   fixer,
		MaxRetries: 1,
	})

	opts := FixOpts{VerificationContext: "## Source Exploration Queries\n\nStep 7: SELECT TARIH FROM dbo.STHAR"}
	_, err := executor.ExecuteWithFixOpts(context.Background(), "SELECT bad FROM t", "verify", opts)
	if err == nil {
		// The mock keeps returning the same error so retries exhaust;
		// what we care about is the LastOpts the fixer captured.
		_ = err
	}
	if fixer.Calls < 1 {
		t.Fatalf("fixer should be called on retry, got %d calls", fixer.Calls)
	}
	if fixer.LastOpts.VerificationContext != opts.VerificationContext {
		t.Errorf("FixOpts.VerificationContext not forwarded: got %q, want %q", fixer.LastOpts.VerificationContext, opts.VerificationContext)
	}
}

// TestExecute_ShimAlwaysPassesEmptyFixOpts pins the explore-path contract:
// callers using the legacy Execute method must never accidentally leak
// per-call grounding context — the fixer should see FixOpts{} on every retry,
// otherwise the conditional {{#VERIFICATION_CONTEXT}} section in the
// per-warehouse fixer prompt would render with stale or irrelevant evidence.
func TestExecute_ShimAlwaysPassesEmptyFixOpts(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("syntax error")

	fixer := &mockFixer{
		FixedQuery: "SELECT 1 FROM `test_dataset.t` WHERE app_id = 'test'",
		// Pre-populate to a sentinel so we can detect if Execute leaks state.
		LastOpts: FixOpts{VerificationContext: "<should be cleared>"},
	}
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		SQLFixer:   fixer,
		MaxRetries: 1,
	})

	_, _ = executor.Execute(context.Background(), "SELECT bad FROM t", "explore")
	if fixer.LastOpts.VerificationContext != "" {
		t.Errorf("Execute shim must forward empty FixOpts, fixer saw VerificationContext=%q", fixer.LastOpts.VerificationContext)
	}
}

// TestExecute_FixHistoryRecordedOnSingleFix verifies that a single
// successful fix attempt produces exactly one FixAttempt entry on the
// result, populated with the broken SQL, the proposed fix, the warehouse
// error that triggered the call, and the LLM accounting fields.
func TestExecute_FixHistoryRecordedOnSingleFix(t *testing.T) {
	callCount := 0
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	origQuery := func(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("syntax error near 'BAD'")
		}
		return &gowarehouse.QueryResult{
			Columns: []string{"n"},
			Rows:    []map[string]interface{}{{"n": 1}},
		}, nil
	}
	wrapper := &queryWrapper{fn: origQuery, provider: wh}

	fixer := &mockFixer{FixedQuery: "SELECT 1 AS n FROM `test_dataset.t` WHERE app_id = 'test'"}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wrapper,
		SQLFixer:    fixer,
		MaxRetries:  3,
		FilterField: "app_id",
		FilterValue: "test",
	})
	executor.SetStep(7)

	original := "SELECT BAD FROM t WHERE app_id = 'test'"
	result, err := executor.Execute(context.Background(), original, "test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.FixHistory) != 1 {
		t.Fatalf("FixHistory entries = %d, want 1", len(result.FixHistory))
	}
	got := result.FixHistory[0]
	if got.Step != 7 {
		t.Errorf("Step = %d, want 7 (taken from QueryExecutor.currentStep)", got.Step)
	}
	if got.Attempt != 0 {
		t.Errorf("Attempt = %d, want 0 (zero-based first retry)", got.Attempt)
	}
	if got.SQLBefore != original {
		t.Errorf("SQLBefore = %q, want %q", got.SQLBefore, original)
	}
	if got.SQLAfter != fixer.FixedQuery {
		t.Errorf("SQLAfter = %q, want %q", got.SQLAfter, fixer.FixedQuery)
	}
	if got.ErrorIn != "syntax error near 'BAD'" {
		t.Errorf("ErrorIn = %q, want warehouse error verbatim", got.ErrorIn)
	}
	if got.PromptIn == "" || got.ResponseOut == "" {
		t.Errorf("PromptIn / ResponseOut should be populated, got prompt=%q response=%q", got.PromptIn, got.ResponseOut)
	}
	if got.InputTokens == 0 || got.OutputTokens == 0 {
		t.Errorf("token counts should be propagated from the fixer: in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

// TestExecute_FixHistoryRecordsAllAttemptsUntilSuccess verifies that
// every fix call lands on FixHistory in chronological order — including
// attempts whose proposed SQL still failed on the next warehouse round
// trip. Confirms attempt indexes are zero-based and increase by 1, and
// SQLBefore on attempt N matches SQLAfter on attempt N-1.
func TestExecute_FixHistoryRecordsAllAttemptsUntilSuccess(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	queries := make([]string, 0)
	fixer := &chainedFixer{
		FixedQueries: []string{
			"SELECT step1 FROM t WHERE app_id = 'test'",
			"SELECT step2 FROM t WHERE app_id = 'test'",
			"SELECT step3 FROM t WHERE app_id = 'test'", // this one succeeds
		},
	}

	callCount := 0
	origQuery := func(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
		queries = append(queries, query)
		callCount++
		if callCount <= 3 {
			return nil, fmt.Errorf("error attempt %d", callCount)
		}
		return &gowarehouse.QueryResult{
			Columns: []string{"n"},
			Rows:    []map[string]interface{}{{"n": 1}},
		}, nil
	}
	wrapper := &queryWrapper{fn: origQuery, provider: wh}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wrapper,
		SQLFixer:    fixer,
		MaxRetries:  5,
		FilterField: "app_id",
		FilterValue: "test",
	})

	original := "SELECT BAD FROM t WHERE app_id = 'test'"
	result, err := executor.Execute(context.Background(), original, "test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.FixHistory) != 3 {
		t.Fatalf("FixHistory entries = %d, want 3 (one per fixer call)", len(result.FixHistory))
	}
	for i, entry := range result.FixHistory {
		if entry.Attempt != i {
			t.Errorf("entry %d Attempt = %d, want %d", i, entry.Attempt, i)
		}
	}
	if result.FixHistory[0].SQLBefore != original {
		t.Errorf("first SQLBefore = %q, want original %q", result.FixHistory[0].SQLBefore, original)
	}
	if result.FixHistory[1].SQLBefore != result.FixHistory[0].SQLAfter {
		t.Errorf("attempt 1 SQLBefore (%q) should equal attempt 0 SQLAfter (%q) — failed proposals chain", result.FixHistory[1].SQLBefore, result.FixHistory[0].SQLAfter)
	}
	if result.FixHistory[2].SQLBefore != result.FixHistory[1].SQLAfter {
		t.Errorf("attempt 2 SQLBefore (%q) should equal attempt 1 SQLAfter (%q)", result.FixHistory[2].SQLBefore, result.FixHistory[1].SQLAfter)
	}
	if result.FixAttempts != 3 {
		t.Errorf("FixAttempts = %d, want 3", result.FixAttempts)
	}
}

// TestExecute_FixHistoryEmptyOnHappyPath confirms FixHistory is nil/empty
// when the query succeeds on the first try, mirroring FixAttempts=0.
func TestExecute_FixHistoryEmptyOnHappyPath(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		MaxRetries: 3,
	})

	result, err := executor.Execute(context.Background(), "SELECT 1", "happy")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.FixHistory) != 0 {
		t.Errorf("FixHistory should be empty on happy path, got %d entries", len(result.FixHistory))
	}
}

// TestExecute_FixerErrorIsRecordedWithFixerError verifies that when the
// fixer's LLM call errors (transport failure, unparseable response), the
// attempt is still recorded into FixHistory with FixerError set, the
// executor returns the partial result (not nil), and downstream callers
// can read every attempt that was made. These rows are the negative
// examples downstream tooling (fine-tuning pipelines, dashboards) wants
// to surface — discarding them was a silent data loss.
func TestExecute_FixerErrorIsRecordedWithFixerError(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("syntax error")
	fixer := &capturingFixer{
		Result: FixResult{
			Prompt:       "[system]\nfix prompt\n[user]\nfix this",
			Response:     "i cannot help",
			InputTokens:  120,
			OutputTokens: 4000, // simulates the model running to max_tokens
			DurationMs:   42000,
		},
		ReturnErr: fmt.Errorf("failed to extract fixed SQL: empty response"),
	}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:  wh,
		SQLFixer:   fixer,
		MaxRetries: 2,
	})
	executor.SetStep(11)

	result, err := executor.Execute(context.Background(), "SELECT BAD", "test")
	if err == nil {
		t.Fatal("expected error from executor when fixer fails")
	}
	if result == nil {
		t.Fatal("executor must return partial result on error so caller can read FixHistory")
	}
	if len(result.FixHistory) != 1 {
		t.Fatalf("FixHistory entries = %d, want 1 (the failed attempt must be recorded, not discarded)", len(result.FixHistory))
	}
	got := result.FixHistory[0]
	if got.FixerError == "" {
		t.Error("FixerError should be set on a failed fixer attempt")
	}
	if !strings.Contains(got.FixerError, "empty response") {
		t.Errorf("FixerError should carry the underlying parse failure, got %q", got.FixerError)
	}
	if got.SQLAfter != "" {
		t.Errorf("SQLAfter should be empty when the fixer failed to extract SQL, got %q", got.SQLAfter)
	}
	if got.PromptIn == "" || got.ResponseOut != "i cannot help" {
		t.Errorf("Prompt/Response should be carried from the partial FixResult: prompt=%q response=%q", got.PromptIn, got.ResponseOut)
	}
	if got.InputTokens != 120 || got.OutputTokens != 4000 {
		t.Errorf("token accounting should round-trip: in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.Step != 11 {
		t.Errorf("Step = %d, want 11 (taken from executor.currentStep)", got.Step)
	}
	if result.FixAttempts != 0 {
		t.Errorf("FixAttempts = %d, want 0 (failed fixer call must not count as applied)", result.FixAttempts)
	}
}

// TestExecute_FilterRejectionIsRecordedWithFixerError verifies that
// when the fixer produces parseable SQL but the post-fix security
// filter check rejects it, the attempt is still recorded with
// FixerError set to a "security violation: …" message. This case
// produced a usable LLM dialog and is just as valuable as a successful
// repair for downstream tooling.
func TestExecute_FilterRejectionIsRecordedWithFixerError(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	wh.QueryError = fmt.Errorf("syntax error")
	fixer := &capturingFixer{
		Result: FixResult{
			FixedSQL:     "SELECT * FROM users", // missing app_id filter
			Prompt:       "[system]\nfix\n[user]\nfix",
			Response:     "SELECT * FROM users",
			InputTokens:  80,
			OutputTokens: 12,
			DurationMs:   500,
		},
	}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wh,
		SQLFixer:    fixer,
		MaxRetries:  2,
		FilterField: "app_id",
		FilterValue: "test",
	})

	result, err := executor.Execute(context.Background(), "SELECT BAD FROM users WHERE app_id = 'test'", "test")
	if err == nil {
		t.Fatal("expected security violation error")
	}
	if result == nil {
		t.Fatal("partial result expected even on filter rejection")
	}
	if len(result.FixHistory) != 1 {
		t.Fatalf("FixHistory entries = %d, want 1 (filter-rejected proposal must be recorded)", len(result.FixHistory))
	}
	got := result.FixHistory[0]
	if got.FixerError == "" {
		t.Error("FixerError should be set on filter rejection")
	}
	if !strings.Contains(got.FixerError, "security violation") {
		t.Errorf("FixerError should label this as a security violation, got %q", got.FixerError)
	}
	if got.SQLAfter != "SELECT * FROM users" {
		t.Errorf("SQLAfter should still record the rejected proposal, got %q", got.SQLAfter)
	}
	if result.FixAttempts != 0 {
		t.Errorf("FixAttempts = %d, want 0 (rejected proposals must not count as applied)", result.FixAttempts)
	}
}

// TestExecute_SuccessfulFixHasEmptyFixerError verifies that applied
// (successful) fixes leave FixerError empty — the field is the
// discriminator between "applied" and "rejected/failed" entries in
// FixHistory.
func TestExecute_SuccessfulFixHasEmptyFixerError(t *testing.T) {
	callCount := 0
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	origQuery := func(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("syntax error")
		}
		return &gowarehouse.QueryResult{Columns: []string{"n"}, Rows: []map[string]interface{}{{"n": 1}}}, nil
	}
	wrapper := &queryWrapper{fn: origQuery, provider: wh}
	fixer := &mockFixer{FixedQuery: "SELECT n FROM t WHERE app_id = 'test'"}

	executor := NewQueryExecutor(QueryExecutorOptions{
		Warehouse:   wrapper,
		SQLFixer:    fixer,
		MaxRetries:  3,
		FilterField: "app_id",
		FilterValue: "test",
	})

	result, err := executor.Execute(context.Background(), "SELECT BAD FROM t WHERE app_id = 'test'", "test")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.FixHistory) != 1 {
		t.Fatalf("FixHistory entries = %d, want 1", len(result.FixHistory))
	}
	if result.FixHistory[0].FixerError != "" {
		t.Errorf("FixerError = %q, want empty for an applied fix", result.FixHistory[0].FixerError)
	}
	if result.FixAttempts != 1 {
		t.Errorf("FixAttempts = %d, want 1 (applied fix counts)", result.FixAttempts)
	}
}

// capturingFixer returns a configurable FixResult and (optionally) an
// error — used to drive the FixerError / partial-result paths.
type capturingFixer struct {
	Result    FixResult
	ReturnErr error
	Calls     int
	LastOpts  FixOpts
}

func (c *capturingFixer) FixSQL(_ context.Context, _ string, _ string, _ int, opts FixOpts) (FixResult, error) {
	c.Calls++
	c.LastOpts = opts
	return c.Result, c.ReturnErr
}

// chainedFixer returns a different fixed query on each successive call
// — used to verify multi-attempt FixHistory chaining.
type chainedFixer struct {
	FixedQueries []string
	Calls        int
	LastOpts     FixOpts
}

func (c *chainedFixer) FixSQL(_ context.Context, _ string, _ string, _ int, opts FixOpts) (FixResult, error) {
	c.LastOpts = opts
	idx := c.Calls
	if idx >= len(c.FixedQueries) {
		idx = len(c.FixedQueries) - 1
	}
	c.Calls++
	return FixResult{
		FixedSQL:     c.FixedQueries[idx],
		Prompt:       "[system]\nfix\n[user]\nfix this",
		Response:     c.FixedQueries[idx],
		InputTokens:  1,
		OutputTokens: 1,
		DurationMs:   1,
	}, nil
}
