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
