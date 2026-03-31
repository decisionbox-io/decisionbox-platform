//go:build integration_databricks

package databricks

import (
	"context"
	"os"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func getIntegrationConfig(t *testing.T) gowarehouse.ProviderConfig {
	t.Helper()

	host := os.Getenv("INTEGRATION_TEST_DATABRICKS_HOST")
	if host == "" {
		t.Skip("INTEGRATION_TEST_DATABRICKS_HOST not set — skipping integration test")
	}

	httpPath := os.Getenv("INTEGRATION_TEST_DATABRICKS_HTTP_PATH")
	if httpPath == "" {
		t.Skip("INTEGRATION_TEST_DATABRICKS_HTTP_PATH not set")
	}

	token := os.Getenv("INTEGRATION_TEST_DATABRICKS_TOKEN")
	if token == "" {
		t.Skip("INTEGRATION_TEST_DATABRICKS_TOKEN not set")
	}

	catalog := os.Getenv("INTEGRATION_TEST_DATABRICKS_CATALOG")
	if catalog == "" {
		catalog = "samples"
	}
	schema := os.Getenv("INTEGRATION_TEST_DATABRICKS_SCHEMA")
	if schema == "" {
		schema = "nyctaxi"
	}

	return gowarehouse.ProviderConfig{
		"host":             host,
		"http_path":        httpPath,
		"catalog":          catalog,
		"dataset":          schema,
		"credentials_json": token,
	}
}

func TestIntegration_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("HealthCheck OK")
}

func TestIntegration_ValidateReadOnly(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := provider.ValidateReadOnly(ctx); err != nil {
		t.Fatalf("validate read-only failed: %v", err)
	}
	t.Log("ValidateReadOnly OK")
}

func TestIntegration_Query(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT 1 AS test_val", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	t.Logf("Query OK: %v", result.Rows)
}

func TestIntegration_ListTables(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
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
		t.Error("expected at least 1 table")
	}
	t.Logf("ListTables: %d tables found", len(tables))
	for _, name := range tables {
		t.Logf("  - %s", name)
	}
}

func TestIntegration_GetTableSchema(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

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
		t.Logf("  %-30s %-10s nullable=%v", col.Name, col.Type, col.Nullable)
	}
}

// ---------------------------------------------------------------------------
// Data type assertions
// ---------------------------------------------------------------------------

func TestIntegration_ScalarTypes(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	query := `SELECT
		CAST(1 AS TINYINT) AS tinyint_val,
		CAST(100 AS SMALLINT) AS smallint_val,
		CAST(100000 AS INT) AS int_val,
		CAST(9999999999 AS BIGINT) AS bigint_val,
		CAST(3.14 AS FLOAT) AS float_val,
		CAST(2.718281828 AS DOUBLE) AS double_val,
		CAST(123.45 AS DECIMAL(10,2)) AS decimal_val,
		CAST('hello' AS STRING) AS string_val,
		CAST(true AS BOOLEAN) AS bool_val,
		CAST('2026-01-15' AS DATE) AS date_val,
		CAST('2026-01-15 10:30:00' AS TIMESTAMP) AS timestamp_val,
		CAST(NULL AS INT) AS null_val`

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-20s = %-40v (Go: %T)", col, row[col], row[col])
	}

	// Integer types → int64 (driver returns int8/16/32, normalizeValue promotes)
	assertGoType[int64](t, row, "tinyint_val")
	assertGoType[int64](t, row, "smallint_val")
	assertGoType[int64](t, row, "int_val")
	assertGoType[int64](t, row, "bigint_val")

	// Float types → float64
	assertGoType[float64](t, row, "float_val")
	assertGoType[float64](t, row, "double_val")

	// DECIMAL → float64 (driver returns string, convertStringByType converts)
	assertGoType[float64](t, row, "decimal_val")

	// STRING → string
	assertGoType[string](t, row, "string_val")

	// BOOLEAN → bool
	assertGoType[bool](t, row, "bool_val")

	// DATE/TIMESTAMP → string (RFC3339, from time.Time)
	assertGoType[string](t, row, "date_val")
	assertGoType[string](t, row, "timestamp_val")

	// NULL → nil
	if row["null_val"] != nil {
		t.Errorf("null_val: expected nil, got %T (%v)", row["null_val"], row["null_val"])
	}
}

func TestIntegration_ComplexTypes(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	query := `SELECT
		array(1, 2, 3) AS array_val,
		map('key1', 'val1', 'key2', 'val2') AS map_val,
		struct(1 AS id, 'hello' AS name) AS struct_val,
		named_struct('a', 1, 'b', 'two') AS named_struct_val`

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-20s = %-60v (Go: %T)", col, row[col], row[col])
	}

	// All complex types → string (from sql.RawBytes or string)
	for _, col := range []string{"array_val", "map_val", "struct_val", "named_struct_val"} {
		assertGoType[string](t, row, col)
	}
}

func TestIntegration_NULLHandling(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	query := `SELECT
		CAST(NULL AS INT) AS null_int,
		CAST(NULL AS STRING) AS null_string,
		CAST(NULL AS BOOLEAN) AS null_bool,
		CAST(NULL AS DOUBLE) AS null_double,
		CAST(NULL AS DATE) AS null_date,
		CAST(NULL AS TIMESTAMP) AS null_timestamp,
		CAST(NULL AS DECIMAL(10,2)) AS null_decimal`

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		if row[col] != nil {
			t.Errorf("%s: expected nil, got %v (%T)", col, row[col], row[col])
		}
	}
}

func TestIntegration_TripsData(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM "+cfg["dataset"]+".trips LIMIT 3", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) == 0 {
		t.Fatal("expected at least 1 row")
	}
	t.Logf("Trips: %d rows, %d columns", len(result.Rows), len(result.Columns))

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-30s = %-40v (Go: %T)", col, row[col], row[col])
	}

	// Verify known column types from samples.nyctaxi.trips
	assertGoType[string](t, row, "tpep_pickup_datetime")   // TIMESTAMP → string (RFC3339)
	assertGoType[string](t, row, "tpep_dropoff_datetime")  // TIMESTAMP → string (RFC3339)
	assertGoType[float64](t, row, "trip_distance")          // DOUBLE → float64
	assertGoType[float64](t, row, "fare_amount")            // DOUBLE → float64
	assertGoType[int64](t, row, "pickup_zip")               // INT → int64
	assertGoType[int64](t, row, "dropoff_zip")              // INT → int64
}

func TestIntegration_SchemaTypeMapping(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema, err := provider.GetTableSchema(ctx, "trips")
	if err != nil {
		t.Fatalf("GetTableSchema failed: %v", err)
	}

	colTypes := map[string]string{}
	for _, col := range schema.Columns {
		colTypes[col.Name] = col.Type
	}

	expected := map[string]string{
		"tpep_pickup_datetime":  "TIMESTAMP",
		"tpep_dropoff_datetime": "TIMESTAMP",
		"trip_distance":         "FLOAT64",
		"fare_amount":           "FLOAT64",
		"pickup_zip":            "INT64",
		"dropoff_zip":           "INT64",
	}

	for col, wantType := range expected {
		if colTypes[col] != wantType {
			t.Errorf("column %q: expected type %q, got %q", col, wantType, colTypes[col])
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func assertGoType[T any](t *testing.T, row map[string]interface{}, col string) {
	t.Helper()
	if _, ok := row[col].(T); !ok {
		t.Errorf("%s: expected %T, got %T (%v)", col, *new(T), row[col], row[col])
	}
}

// ---------------------------------------------------------------------------
// Full interface exercise
// ---------------------------------------------------------------------------

func TestIntegration_ProviderInterface(t *testing.T) {
	cfg := getIntegrationConfig(t)
	provider, err := gowarehouse.NewProvider("databricks", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	var _ gowarehouse.Provider = provider

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if err := provider.ValidateReadOnly(ctx); err != nil {
		t.Errorf("ValidateReadOnly: %v", err)
	}
	if provider.GetDataset() == "" {
		t.Error("GetDataset returned empty")
	}
	if provider.SQLDialect() == "" {
		t.Error("SQLDialect returned empty")
	}
	if provider.SQLFixPrompt() == "" {
		t.Error("SQLFixPrompt returned empty")
	}

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Errorf("ListTables: %v", err)
	}
	if len(tables) == 0 {
		t.Error("ListTables returned empty")
	}
	t.Logf("Tables: %v", tables)

	if len(tables) > 0 {
		schema, err := provider.GetTableSchema(ctx, tables[0])
		if err != nil {
			t.Errorf("GetTableSchema(%s): %v", tables[0], err)
		} else {
			t.Logf("Schema for %s: %d columns, ~%d rows", schema.Name, len(schema.Columns), schema.RowCount)
		}

		result, err := provider.Query(ctx, "SELECT * FROM "+cfg["dataset"]+"."+tables[0]+" LIMIT 5", nil)
		if err != nil {
			t.Errorf("Query: %v", err)
		} else {
			t.Logf("Query returned %d rows, %d columns", len(result.Rows), len(result.Columns))
		}
	}
}
