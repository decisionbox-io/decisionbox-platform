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
