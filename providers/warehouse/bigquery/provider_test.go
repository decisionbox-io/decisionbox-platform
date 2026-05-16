package bigquery

import (
	"context"
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

func TestNewBigQueryProvider_MissingProjectID(t *testing.T) {
	_, err := NewBigQueryProvider(context.TODO(), BigQueryConfig{
		Dataset: "test",
	})
	if err == nil {
		t.Error("expected error for missing project_id")
	}
}

func TestNewBigQueryProvider_MissingDataset(t *testing.T) {
	_, err := NewBigQueryProvider(context.TODO(), BigQueryConfig{
		ProjectID: "test",
	})
	if err == nil {
		t.Error("expected error for missing dataset")
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

func TestBigQueryProvider_SQLDialect(t *testing.T) {
	p := &BigQueryProvider{dataset: "test_dataset"}
	dialect := p.SQLDialect()
	if dialect != "BigQuery Standard SQL" {
		t.Errorf("SQLDialect() = %q, want %q", dialect, "BigQuery Standard SQL")
	}
}

func TestBigQueryProvider_QuoteRef(t *testing.T) {
	p := &BigQueryProvider{dataset: "test_dataset"}
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "dataset.table", parts: []string{"events_prod", "sessions"}, want: "`events_prod`.`sessions`"},
		{name: "catalog.dataset.table", parts: []string{"main", "events_prod", "sessions"}, want: "`main`.`events_prod`.`sessions`"},
		{name: "single part", parts: []string{"sessions"}, want: "`sessions`"},
		{name: "empty parts", parts: nil, want: ""},
		{name: "empty middle part skipped", parts: []string{"events_prod", "", "sessions"}, want: "`events_prod`.`sessions`"},
		{name: "all empty", parts: []string{"", "  "}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.QuoteRef(tc.parts...); got != tc.want {
				t.Errorf("QuoteRef(%v) = %q, want %q", tc.parts, got, tc.want)
			}
		})
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
	for _, marker := range []string{"{{#VERIFICATION_CONTEXT}}", "{{VERIFICATION_CONTEXT}}", "{{/VERIFICATION_CONTEXT}}"} {
		if !bqContains(prompt, marker) {
			t.Errorf("SQLFixPrompt should contain %s for column-grounded retries", marker)
		}
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

func TestRegisteredAuthMethods(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("bigquery")
	if !ok {
		t.Fatal("bigquery not registered")
	}
	if len(meta.AuthMethods) != 2 {
		t.Fatalf("expected 2 auth methods, got %d", len(meta.AuthMethods))
	}
	ids := map[string]bool{}
	for _, m := range meta.AuthMethods {
		ids[m.ID] = true
	}
	if !ids["adc"] {
		t.Error("missing 'adc' auth method")
	}
	if !ids["sa_key"] {
		t.Error("missing 'sa_key' auth method")
	}
}

func TestBigQueryFactory_SAKeyEmptyFallsThroughToADC(t *testing.T) {
	// Under the new credential-resolution rule, sa_key with an empty
	// credential blob falls through to ADC — the SDK uses
	// GOOGLE_APPLICATION_CREDENTIALS / metadata server. The factory must
	// not error on empty credentials at this layer; the SDK will error
	// later if no ambient credentials are available.
	_, err := gowarehouse.NewProvider("bigquery", gowarehouse.ProviderConfig{
		"project_id":  "test-project",
		"dataset":     "test_dataset",
		"auth_method": "sa_key",
	})
	// The factory call may succeed (client constructor is lazy) or fail
	// downstream at client creation; what must NOT happen is a
	// "service account key is required" error.
	if err != nil && bqContains(err.Error(), "service account key is required") {
		t.Errorf("legacy 'sa key required' error must not return under env-fallback rule: %v", err)
	}
}

func TestBigQueryFactory_UnsupportedAuthMethod(t *testing.T) {
	_, err := gowarehouse.NewProvider("bigquery", gowarehouse.ProviderConfig{
		"project_id":  "test-project",
		"dataset":     "test_dataset",
		"auth_method": "oauth",
	})
	if err == nil {
		t.Fatal("expected error for unsupported auth method")
	}
	if !bqContains(err.Error(), "unsupported auth method") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestBigQueryProvider_AuthMethodSAKey_InvalidJSON(t *testing.T) {
	_, err := gowarehouse.NewProvider("bigquery", gowarehouse.ProviderConfig{
		"project_id":      "test-project",
		"dataset":         "test_dataset",
		"auth_method":     "sa_key",
		"credentials_json": "not-valid-json",
	})
	if err == nil {
		t.Error("expected error for invalid SA key JSON")
	}
}

func TestBigQueryProvider_AuthMethodFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("bigquery")

	// ADC should have no fields
	adc := findAuthMethod(meta.AuthMethods, "adc")
	if adc == nil {
		t.Fatal("missing adc auth method")
	}
	if len(adc.Fields) != 0 {
		t.Errorf("ADC should have 0 fields, got %d", len(adc.Fields))
	}

	// SA Key should have 1 credential field
	saKey := findAuthMethod(meta.AuthMethods, "sa_key")
	if saKey == nil {
		t.Fatal("missing sa_key auth method")
	}
	if len(saKey.Fields) != 1 {
		t.Fatalf("SA Key should have 1 field, got %d", len(saKey.Fields))
	}
	if saKey.Fields[0].Type != "credential" {
		t.Errorf("SA Key field should be type 'credential', got %q", saKey.Fields[0].Type)
	}
	if !saKey.Fields[0].Required {
		t.Error("SA Key credential field should be required")
	}
}

func TestBigQueryProvider_DefaultPricing(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("bigquery")
	if meta.DefaultPricing == nil {
		t.Fatal("expected default pricing")
	}
	if meta.DefaultPricing.CostModel != "per_byte_scanned" {
		t.Errorf("expected cost model 'per_byte_scanned', got %q", meta.DefaultPricing.CostModel)
	}
	if meta.DefaultPricing.CostPerTBScannedUSD != 6.25 {
		t.Errorf("expected 6.25, got %f", meta.DefaultPricing.CostPerTBScannedUSD)
	}
}

func findAuthMethod(methods []gowarehouse.AuthMethod, id string) *gowarehouse.AuthMethod {
	for i := range methods {
		if methods[i].ID == id {
			return &methods[i]
		}
	}
	return nil
}

func bqContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
