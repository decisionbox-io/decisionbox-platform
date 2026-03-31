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
