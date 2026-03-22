package queryexec

import (
	"context"
	"fmt"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

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

	fixer := &testutil.MockSQLFixer{
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

	fixer := &testutil.MockSQLFixer{}

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

	// result may be nil on error
	if result != nil {
		t.Error("result should be nil on error with no fixer")
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

	fixer := &testutil.MockSQLFixer{
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

	fixer := &testutil.MockSQLFixer{
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
