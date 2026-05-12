//go:build integration_redshift

package redshift

import (
	"context"
	"os"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func getIntegrationConfig(t *testing.T) gowarehouse.ProviderConfig {
	t.Helper()

	workgroup := os.Getenv("INTEGRATION_TEST_REDSHIFT_WORKGROUP")
	if workgroup == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_WORKGROUP not set")
	}

	region := os.Getenv("INTEGRATION_TEST_REDSHIFT_REGION")
	if region == "" {
		region = "us-east-1"
	}
	database := os.Getenv("INTEGRATION_TEST_REDSHIFT_DATABASE")
	if database == "" {
		database = "dev"
	}

	return gowarehouse.ProviderConfig{
		"workgroup": workgroup,
		"database":  database,
		"dataset":   "public",
		"region":    region,
	}
}

// --- IAM Role auth (default credentials) ---

func TestIntegration_IAMRole_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)
	cfg["auth_method"] = "iam_role"

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("IAM Role: HealthCheck OK")
}

// --- Access Keys auth ---

func TestIntegration_AccessKeys_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)

	accessKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID")
	secretKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID and INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY not set")
	}

	cfg["auth_method"] = "access_keys"
	cfg["credentials_json"] = accessKey + ":" + secretKey

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("Access Keys: HealthCheck OK")
}

func TestIntegration_AccessKeys_ListTables(t *testing.T) {
	cfg := getIntegrationConfig(t)

	accessKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID")
	secretKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID and INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY not set")
	}

	cfg["auth_method"] = "access_keys"
	cfg["credentials_json"] = accessKey + ":" + secretKey

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	t.Logf("Access Keys: ListTables returned %d tables", len(tables))
}

// TestIntegration_AccessKeys_QuoteRef_RoundTrip confirms that the
// double-quoted shape Redshift's QuoteRef emits is accepted by a
// real Redshift workgroup. We use the first table ListTables
// returns to avoid hardcoding a table name that may not be present
// in every configured test schema.
func TestIntegration_AccessKeys_QuoteRef_RoundTrip(t *testing.T) {
	cfg := getIntegrationConfig(t)

	accessKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID")
	secretKey := os.Getenv("INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_ACCESS_KEY_ID and INTEGRATION_TEST_REDSHIFT_SECRET_ACCESS_KEY not set")
	}

	cfg["auth_method"] = "access_keys"
	cfg["credentials_json"] = accessKey + ":" + secretKey

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	if len(tables) == 0 {
		t.Skip("schema has no tables — cannot exercise QuoteRef round-trip")
	}

	schema := provider.GetDataset()
	if schema == "" {
		schema = "public"
	}
	ref := provider.QuoteRef(schema, tables[0])
	expected := `"` + schema + `"."` + tables[0] + `"`
	if ref != expected {
		t.Fatalf("QuoteRef shape mismatch: got %q, want %q", ref, expected)
	}

	query := "SELECT 1 AS one FROM " + ref + " LIMIT 1"
	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("QuoteRef'd query failed against live Redshift: %v\nquery: %s", err, query)
	}
	if result == nil || len(result.Rows) == 0 {
		t.Fatalf("expected at least one result row, got %#v", result)
	}
}

// --- Assume Role auth ---

func TestIntegration_AssumeRole_HealthCheck(t *testing.T) {
	cfg := getIntegrationConfig(t)

	roleARN := os.Getenv("INTEGRATION_TEST_REDSHIFT_ROLE_ARN")
	if roleARN == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_ROLE_ARN not set")
	}

	cfg["auth_method"] = "assume_role"
	cfg["role_arn"] = roleARN
	if extID := os.Getenv("INTEGRATION_TEST_REDSHIFT_EXTERNAL_ID"); extID != "" {
		cfg["external_id"] = extID
	}

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("Assume Role: HealthCheck OK")
}

func TestIntegration_AssumeRole_Query(t *testing.T) {
	cfg := getIntegrationConfig(t)

	roleARN := os.Getenv("INTEGRATION_TEST_REDSHIFT_ROLE_ARN")
	if roleARN == "" {
		t.Skip("INTEGRATION_TEST_REDSHIFT_ROLE_ARN not set")
	}

	cfg["auth_method"] = "assume_role"
	cfg["role_arn"] = roleARN
	if extID := os.Getenv("INTEGRATION_TEST_REDSHIFT_EXTERNAL_ID"); extID != "" {
		cfg["external_id"] = extID
	}

	provider, err := gowarehouse.NewProvider("redshift", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT 1 AS test_val", nil)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	t.Logf("Assume Role: Query OK, result=%v", result.Rows)
}
