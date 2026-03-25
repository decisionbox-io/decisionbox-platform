//go:build integration_snowflake

package snowflake

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func getIntegrationConfig(t *testing.T) gowarehouse.ProviderConfig {
	t.Helper()

	account := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_ACCOUNT")
	user := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_USER")
	password := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_PASSWORD")
	warehouse := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_WAREHOUSE")
	database := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_DATABASE")
	schema := os.Getenv("INTEGRATION_TEST_SNOWFLAKE_SCHEMA")

	if account == "" || user == "" || password == "" {
		t.Skip("INTEGRATION_TEST_SNOWFLAKE_ACCOUNT, INTEGRATION_TEST_SNOWFLAKE_USER, INTEGRATION_TEST_SNOWFLAKE_PASSWORD not set")
	}
	if warehouse == "" {
		warehouse = "COMPUTE_WH"
	}
	if database == "" {
		database = "SNOWFLAKE_SAMPLE_DATA"
	}
	if schema == "" {
		schema = "TPCDS_SF100TCL"
	}

	return gowarehouse.ProviderConfig{
		"account":          account,
		"user":             user,
		"credentials_json": password,
		"warehouse":        warehouse,
		"database":         database,
		"dataset":          schema,
		"timeout_minutes":  "2",
	}
}

func TestIntegration_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("HealthCheck: OK")
}

func TestIntegration_SQLDialect(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	dialect := provider.SQLDialect()
	if !strings.Contains(dialect, "Snowflake") {
		t.Errorf("dialect should mention Snowflake, got %q", dialect)
	}
	t.Logf("SQLDialect: %q", dialect)
}

func TestIntegration_GetDataset(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	dataset := provider.GetDataset()
	expected := cfg["dataset"]
	if dataset != expected {
		t.Errorf("expected %q, got %q", expected, dataset)
	}
	t.Logf("GetDataset: %q", dataset)
}

func TestIntegration_ListTables(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	if len(tables) == 0 {
		t.Error("expected at least one table")
	}
	t.Logf("ListTables: %d tables found", len(tables))
	for _, name := range tables {
		t.Logf("  - %s", name)
	}
}

func TestIntegration_ListTablesInDataset(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tables, err := provider.ListTablesInDataset(ctx, cfg["schema"])
	if err != nil {
		t.Fatalf("ListTablesInDataset failed: %v", err)
	}
	if len(tables) == 0 {
		t.Error("expected at least one table")
	}
	t.Logf("ListTablesInDataset(%s): %d tables", cfg["schema"], len(tables))
}

func TestIntegration_GetTableSchema(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First list tables to find a real one
	tables, err := provider.ListTables(ctx)
	if err != nil || len(tables) == 0 {
		t.Fatalf("ListTables failed or empty: %v", err)
	}

	tableName := tables[0]
	schema, err := provider.GetTableSchema(ctx, tableName)
	if err != nil {
		t.Fatalf("GetTableSchema(%s) failed: %v", tableName, err)
	}

	if schema.Name != tableName {
		t.Errorf("expected name %q, got %q", tableName, schema.Name)
	}
	if len(schema.Columns) == 0 {
		t.Error("expected at least one column")
	}
	t.Logf("GetTableSchema(%s): %d columns, %d rows", tableName, len(schema.Columns), schema.RowCount)
	for _, col := range schema.Columns {
		t.Logf("  - %-30s %-10s nullable=%v", col.Name, col.Type, col.Nullable)
	}
}

func TestIntegration_Query(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT 1 AS num, 'hello' AS str, TRUE AS flag, CURRENT_TIMESTAMP() AS ts", nil)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Columns) != 4 {
		t.Errorf("expected 4 columns, got %d", len(result.Columns))
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}

	t.Logf("Query result: columns=%v", result.Columns)
	for k, v := range result.Rows[0] {
		t.Logf("  %s = %v (type: %T)", k, v, v)
	}
}

func TestIntegration_QueryWithData(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Query from the actual dataset — use LIMIT to avoid scanning too much
	schema := cfg["schema"]
	tables, err := provider.ListTables(ctx)
	if err != nil || len(tables) == 0 {
		t.Fatalf("ListTables failed or empty: %v", err)
	}

	query := "SELECT * FROM " + cfg["database"] + "." + schema + "." + tables[0] + " LIMIT 5"
	t.Logf("Running: %s", query)

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Result: %d columns, %d rows", len(result.Columns), len(result.Rows))
	t.Logf("Columns: %v", result.Columns)
	if len(result.Rows) > 0 {
		for k, v := range result.Rows[0] {
			t.Logf("  %s = %v (type: %T)", k, v, v)
		}
	}
}

func TestIntegration_ValidateReadOnly(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.ValidateReadOnly(ctx); err != nil {
		t.Errorf("ValidateReadOnly failed: %v", err)
	}
}

func TestIntegration_AllDataTypes(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("snowflake", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	query := `SELECT
		-- Integer types
		1::NUMBER AS number_val, 2::INT AS int_val, 3::BIGINT AS bigint_val,
		4::SMALLINT AS smallint_val, 5::TINYINT AS tinyint_val,
		-- Decimal/float types
		3.14::NUMBER(10,2) AS number_decimal_val, 2.718::FLOAT AS float_val,
		1.618::DOUBLE AS double_val, 12.345::DECIMAL(10,3) AS decimal_val,
		-- String types
		'hello'::VARCHAR AS varchar_val, 'test'::STRING AS string_val,
		-- Boolean
		TRUE::BOOLEAN AS bool_true_val, FALSE::BOOLEAN AS bool_false_val,
		-- Date/time types
		'2026-01-15'::DATE AS date_val,
		'2026-01-15 10:30:00'::TIMESTAMP_NTZ AS ts_ntz_val,
		'2026-01-15 10:30:00 +05:00'::TIMESTAMP_TZ AS ts_tz_val,
		'2026-01-15 10:30:00'::TIMESTAMP_LTZ AS ts_ltz_val,
		'14:30:00'::TIME AS time_val,
		-- Semi-structured
		PARSE_JSON('{"key": "value"}')::VARIANT AS variant_val,
		OBJECT_CONSTRUCT('a', 1)::OBJECT AS object_val,
		ARRAY_CONSTRUCT(1, 2, 3)::ARRAY AS array_val,
		-- Binary
		TO_BINARY('48656C6C6F', 'HEX')::BINARY AS binary_val,
		-- NULLs
		NULL::NUMBER AS null_number, NULL::VARCHAR AS null_string,
		NULL::BOOLEAN AS null_boolean, NULL::VARIANT AS null_variant`

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		val := row[col]
		if val == nil {
			t.Logf("  %-25s = <nil>", col)
		} else {
			t.Logf("  %-25s = %-30v (Go: %T)", col, val, val)
		}
	}

	// Assert integer types → int64
	for _, col := range []string{"NUMBER_VAL", "INT_VAL", "BIGINT_VAL", "SMALLINT_VAL", "TINYINT_VAL"} {
		if _, ok := row[col].(int64); !ok {
			t.Errorf("%s: expected int64, got %T (%v)", col, row[col], row[col])
		}
	}

	// Assert decimal/float types → float64
	for _, col := range []string{"NUMBER_DECIMAL_VAL", "FLOAT_VAL", "DOUBLE_VAL", "DECIMAL_VAL"} {
		if _, ok := row[col].(float64); !ok {
			t.Errorf("%s: expected float64, got %T (%v)", col, row[col], row[col])
		}
	}

	// Assert string types → string
	for _, col := range []string{"VARCHAR_VAL", "STRING_VAL"} {
		if _, ok := row[col].(string); !ok {
			t.Errorf("%s: expected string, got %T (%v)", col, row[col], row[col])
		}
	}

	// Assert boolean
	if _, ok := row["BOOL_TRUE_VAL"].(bool); !ok {
		t.Errorf("BOOL_TRUE_VAL: expected bool, got %T", row["BOOL_TRUE_VAL"])
	}

	// Assert date/time → string (RFC3339)
	for _, col := range []string{"DATE_VAL", "TS_NTZ_VAL", "TS_TZ_VAL", "TS_LTZ_VAL", "TIME_VAL"} {
		if _, ok := row[col].(string); !ok {
			t.Errorf("%s: expected string, got %T (%v)", col, row[col], row[col])
		}
	}

	// Assert semi-structured → string (JSON)
	for _, col := range []string{"VARIANT_VAL", "OBJECT_VAL", "ARRAY_VAL"} {
		if _, ok := row[col].(string); !ok {
			t.Errorf("%s: expected string, got %T (%v)", col, row[col], row[col])
		}
	}

	// Assert NULLs → nil
	for _, col := range []string{"NULL_NUMBER", "NULL_STRING", "NULL_BOOLEAN", "NULL_VARIANT"} {
		if row[col] != nil {
			t.Errorf("%s: expected nil, got %T (%v)", col, row[col], row[col])
		}
	}
}
