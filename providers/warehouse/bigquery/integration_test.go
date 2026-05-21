//go:build integration_bigquery

package bigquery

import (
	"context"
	"os"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func getIntegrationConfig(t *testing.T) gowarehouse.ProviderConfig {
	t.Helper()

	projectID := os.Getenv("INTEGRATION_TEST_BIGQUERY_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_BIGQUERY_PROJECT_ID not set")
	}
	dataset := os.Getenv("INTEGRATION_TEST_BIGQUERY_DATASET")
	if dataset == "" {
		dataset = "events_dev"
	}
	location := os.Getenv("INTEGRATION_TEST_BIGQUERY_LOCATION")
	if location == "" {
		location = "US"
	}

	return gowarehouse.ProviderConfig{
		"project_id": projectID,
		"dataset":    dataset,
		"location":   location,
	}
}

// --- ADC auth ---

func TestIntegration_ADC_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)
	cfg["auth_method"] = "adc"

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("ADC: HealthCheck OK")
}

func TestIntegration_ADC_ListTables(t *testing.T) {
	cfg := getIntegrationConfig(t)
	cfg["auth_method"] = "adc"

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	t.Logf("ADC: ListTables returned %d tables", len(tables))
	for _, name := range tables {
		t.Logf("  - %s", name)
	}
}

// TestIntegration_ADC_QuoteRef_RoundTrip confirms the backtick-quoted
// per-part shape BigQuery's QuoteRef emits is accepted verbatim by a
// real BigQuery dataset. Picks the first table ListTables returns
// (avoiding a hardcoded table name that may not exist in every
// configured test dataset).
func TestIntegration_ADC_QuoteRef_RoundTrip(t *testing.T) {
	cfg := getIntegrationConfig(t)
	cfg["auth_method"] = "adc"

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	if len(tables) == 0 {
		t.Skip("dataset has no tables — cannot exercise QuoteRef round-trip")
	}

	// ListTables returns fully qualified `dataset.table` strings on BQ;
	// split to feed QuoteRef as discrete parts so the helper renders
	// the per-part backtick shape (`dataset`.`table`).
	qualified := tables[0]
	dot := -1
	for i := 0; i < len(qualified); i++ {
		if qualified[i] == '.' {
			dot = i
			break
		}
	}
	if dot == -1 {
		t.Fatalf("expected ListTables entry to be qualified dataset.table, got %q", qualified)
	}
	dataset := qualified[:dot]
	table := qualified[dot+1:]

	ref := provider.QuoteRef(dataset, table)
	expected := "`" + dataset + "`.`" + table + "`"
	if ref != expected {
		t.Fatalf("QuoteRef shape mismatch: got %q, want %q", ref, expected)
	}

	query := "SELECT 1 AS one FROM " + ref + " LIMIT 1"
	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("QuoteRef'd query failed against live BigQuery: %v\nquery: %s", err, query)
	}
	if result == nil || len(result.Rows) == 0 {
		t.Fatalf("expected at least one result row, got %#v", result)
	}
}

// --- Service Account Key auth ---

func TestIntegration_SAKey_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)

	saKeyPath := os.Getenv("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE")
	if saKeyPath == "" {
		t.Skip("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE not set")
	}
	saKey, err := os.ReadFile(saKeyPath)
	if err != nil {
		t.Fatalf("failed to read SA key file: %v", err)
	}

	cfg["auth_method"] = "sa_key"
	cfg["credentials_json"] = string(saKey)

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("SA Key: HealthCheck OK")
}

func TestIntegration_SAKey_ListTables(t *testing.T) {
	cfg := getIntegrationConfig(t)

	saKeyPath := os.Getenv("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE")
	if saKeyPath == "" {
		t.Skip("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE not set")
	}
	saKey, err := os.ReadFile(saKeyPath)
	if err != nil {
		t.Fatalf("failed to read SA key file: %v", err)
	}

	cfg["auth_method"] = "sa_key"
	cfg["credentials_json"] = string(saKey)

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	t.Logf("SA Key: ListTables returned %d tables", len(tables))
}

func TestIntegration_SAKey_Query(t *testing.T) {
	cfg := getIntegrationConfig(t)

	saKeyPath := os.Getenv("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE")
	if saKeyPath == "" {
		t.Skip("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE not set")
	}
	saKey, err := os.ReadFile(saKeyPath)
	if err != nil {
		t.Fatalf("failed to read SA key file: %v", err)
	}

	cfg["auth_method"] = "sa_key"
	cfg["credentials_json"] = string(saKey)

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT 1 AS test_val, 'hello' AS test_str", nil)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	t.Logf("SA Key: Query OK, result=%v", result.Rows)
}

// ---------------------------------------------------------------------------
// ValidateSQL
// ---------------------------------------------------------------------------

func bigqueryFromSAKey(t *testing.T) (gowarehouse.Provider, string) {
	t.Helper()
	cfg := getIntegrationConfig(t)

	saKeyPath := os.Getenv("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE")
	if saKeyPath == "" {
		t.Skip("INTEGRATION_TEST_BIGQUERY_SA_KEY_FILE not set")
	}
	saKey, err := os.ReadFile(saKeyPath) //nolint:gosec // G304: path comes from operator env, not user input
	if err != nil {
		t.Fatalf("failed to read SA key file: %v", err)
	}

	cfg["auth_method"] = "sa_key"
	cfg["credentials_json"] = string(saKey)

	provider, err := gowarehouse.NewProvider("bigquery", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	t.Cleanup(func() { provider.Close() })

	return provider, cfg["dataset"]
}

func TestIntegration_ValidateSQL_BigQuery_AcceptsValidSelect(t *testing.T) {
	provider, dataset := bigqueryFromSAKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The test fixture lives at `<dataset>.customers` in
	// demo_olist_ecommerce. The dataset name comes from
	// INTEGRATION_TEST_BIGQUERY_DATASET so the test stays portable
	// to whatever project/dataset the operator wires in.
	cases := []string{
		"SELECT 1",
		"SELECT 1 AS one",
		"SELECT customer_id FROM `" + dataset + ".customers` LIMIT 5",
		"WITH src AS (SELECT customer_id FROM `" + dataset + ".customers`) SELECT count(*) FROM src",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			if err := provider.ValidateSQL(ctx, sql); err != nil {
				t.Errorf("ValidateSQL rejected a valid statement: %v", err)
			}
		})
	}
}

func TestIntegration_ValidateSQL_BigQuery_RejectsMalformed(t *testing.T) {
	provider, _ := bigqueryFromSAKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cases := []struct {
		name, sql string
	}{
		{"typo in keyword", "SELEC 1"},
		{"unbalanced paren", "SELECT (1"},
		{"missing FROM target", "SELECT * FROM"},
		{"unterminated string", "SELECT 'oops"},
		{"random prose", "this is not sql"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := provider.ValidateSQL(ctx, c.sql); err == nil {
				t.Errorf("ValidateSQL accepted invalid input %q", c.sql)
			}
		})
	}
}

func TestIntegration_ValidateSQL_BigQuery_RejectsNonexistentTable(t *testing.T) {
	provider, _ := bigqueryFromSAKey(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// BigQuery's dry-run path resolves table references, so a
	// nonexistent table surfaces as an error here. Unlike Postgres
	// (where EXPLAIN against a missing table also errors), this is
	// a stronger guarantee — BigQuery is one of the dialects where
	// ValidateSQL doubles as a basic catalog check.
	err := provider.ValidateSQL(ctx, "SELECT 1 FROM `no_such_dataset.no_such_table`")
	if err == nil {
		t.Error("expected ValidateSQL to reject a query against a missing table")
	}
}

func TestIntegration_ValidateSQL_BigQuery_RejectsEmpty(t *testing.T) {
	provider, _ := bigqueryFromSAKey(t)
	for _, sql := range []string{"", "   ", "\t\n"} {
		if err := provider.ValidateSQL(context.Background(), sql); err == nil {
			t.Errorf("ValidateSQL accepted whitespace-only input %q", sql)
		}
	}
}
