package bigquery

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func TestBigQueryConfig_DefaultTimeout(t *testing.T) {
	cfg := BigQueryConfig{
		ProjectID: "test-project",
		Dataset:   "test_dataset",
	}
	if cfg.Timeout != 0 {
		t.Error("timeout should be zero before init")
	}
}

func TestBigQueryConfig_WithCredentials(t *testing.T) {
	cfg := BigQueryConfig{
		ProjectID:       "test-project",
		Dataset:         "test_dataset",
		CredentialsJSON: `{"type":"service_account","project_id":"test"}`,
	}
	if cfg.CredentialsJSON == "" {
		t.Error("credentials should be set")
	}
}

func TestBigQueryConfig_WithoutCredentials(t *testing.T) {
	cfg := BigQueryConfig{
		ProjectID: "test-project",
		Dataset:   "test_dataset",
	}
	if cfg.CredentialsJSON != "" {
		t.Error("credentials should be empty by default (ADC fallback)")
	}
}

func TestNewBigQueryProvider_MissingProjectID(t *testing.T) {
	_, err := NewBigQueryProvider(nil, BigQueryConfig{
		Dataset: "test",
	})
	if err == nil {
		t.Error("expected error for missing project_id")
	}
}

func TestNewBigQueryProvider_MissingDataset(t *testing.T) {
	_, err := NewBigQueryProvider(nil, BigQueryConfig{
		ProjectID: "test",
	})
	if err == nil {
		t.Error("expected error for missing dataset")
	}
}

func TestNewBigQueryProvider_InvalidCredentials(t *testing.T) {
	// Invalid JSON credentials should fail at client creation
	_, err := NewBigQueryProvider(nil, BigQueryConfig{
		ProjectID:       "test",
		Dataset:         "test",
		CredentialsJSON: "not-valid-json",
	})
	if err == nil {
		t.Error("expected error for invalid credentials JSON")
	}
}

func TestBigQueryProvider_Registered(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("bigquery")
	if !ok {
		t.Fatal("bigquery not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.DefaultPricing == nil {
		t.Error("missing default pricing")
	}
	if meta.DefaultPricing.CostPerTBScannedUSD != 6.25 {
		t.Errorf("cost = %f, want 6.25", meta.DefaultPricing.CostPerTBScannedUSD)
	}
}

func TestBigQueryProvider_ConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("bigquery")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	if !keys["project_id"] {
		t.Error("missing project_id config field")
	}
	if !keys["dataset"] {
		t.Error("missing dataset config field")
	}
	if !keys["location"] {
		t.Error("missing location config field")
	}
}

func TestBigQueryFactory_WithCredentials(t *testing.T) {
	// Factory should pass credentials_json to config
	// Can't fully test without real GCP, but verify it doesn't panic on empty
	_, err := gowarehouse.NewProvider("bigquery", gowarehouse.ProviderConfig{
		"project_id":       "test-project",
		"dataset":          "test_dataset",
		"credentials_json": "",
	})
	// Will fail on ADC (no GCP creds in test env) but should not panic
	if err != nil {
		// Expected — no GCP credentials available in test
		t.Logf("Expected error (no GCP creds): %v", err)
	}
}

func TestBigQueryFactory_CredentialsPassthrough(t *testing.T) {
	// Verify invalid credentials produce a clear error (not a panic)
	_, err := gowarehouse.NewProvider("bigquery", gowarehouse.ProviderConfig{
		"project_id":       "test-project",
		"dataset":          "test_dataset",
		"credentials_json": `{"type":"invalid"}`,
	})
	if err == nil {
		t.Error("expected error for invalid credentials")
	}
}

func TestBigQueryProvider_SQLDialect(t *testing.T) {
	p := &BigQueryProvider{dataset: "test_dataset"}
	dialect := p.SQLDialect()
	if dialect != "BigQuery Standard SQL" {
		t.Errorf("SQLDialect() = %q, want %q", dialect, "BigQuery Standard SQL")
	}
}

func TestBigQueryProvider_SQLFixPrompt(t *testing.T) {
	p := &BigQueryProvider{dataset: "test_dataset"}
	prompt := p.SQLFixPrompt()
	if prompt == "" {
		t.Error("SQLFixPrompt() should not be empty")
	}
	// Verify it contains expected template variables
	if !bqContains(prompt, "{{DATASET}}") {
		t.Error("SQLFixPrompt should contain {{DATASET}} template variable")
	}
	if !bqContains(prompt, "{{ORIGINAL_SQL}}") {
		t.Error("SQLFixPrompt should contain {{ORIGINAL_SQL}} template variable")
	}
	if !bqContains(prompt, "{{ERROR_MESSAGE}}") {
		t.Error("SQLFixPrompt should contain {{ERROR_MESSAGE}} template variable")
	}
	// Verify BigQuery-specific content
	if !bqContains(prompt, "BigQuery") {
		t.Error("SQLFixPrompt should mention BigQuery")
	}
}

func TestBigQueryProvider_GetDataset(t *testing.T) {
	tests := []struct {
		name    string
		dataset string
		want    string
	}{
		{"single dataset", "events_prod", "events_prod"},
		{"comma-separated", "events_prod, features_prod", "events_prod, features_prod"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &BigQueryProvider{dataset: tt.dataset}
			got := p.GetDataset()
			if got != tt.want {
				t.Errorf("GetDataset() = %q, want %q", got, tt.want)
			}
		})
	}
}

func bqContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
