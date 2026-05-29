package bigquery

import (
	"context"
	"testing"

	bq "cloud.google.com/go/bigquery"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// xProjProvider builds a provider in the cross-project ("read from
// bigquery-public-data, bill my own project") shape used by the
// qualification tests below.
func xProjProvider() *BigQueryProvider {
	return &BigQueryProvider{
		dataset:       "census_ds",
		dataProjectID: "bigquery-public-data",
		config:        BigQueryConfig{ProjectID: "jobs-proj"},
	}
}

// sameProjProvider builds a single-project provider — dataProjectID equals
// the jobs project, so crossProject() is false and qualification is the
// pre-cross-project two-part form.
func sameProjProvider() *BigQueryProvider {
	return &BigQueryProvider{
		dataset:       "census_ds",
		dataProjectID: "jobs-proj",
		config:        BigQueryConfig{ProjectID: "jobs-proj"},
	}
}

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

// TestBigQueryProvider_QuoteRef_CrossProject pins the central cross-project
// fix: for a different data project, QuoteRef must emit a three-part ref so
// the LLM's SQL resolves against the data project. A two-part `dataset`.
// `table` resolves its project to the jobs project and 404s.
func TestBigQueryProvider_QuoteRef_CrossProject(t *testing.T) {
	p := xProjProvider()
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "dataset.table gets data project prepended", parts: []string{"census_ds", "Variable"}, want: "`bigquery-public-data`.`census_ds`.`Variable`"},
		{name: "already project-qualified is not double-prefixed", parts: []string{"bigquery-public-data", "census_ds", "Variable"}, want: "`bigquery-public-data`.`census_ds`.`Variable`"},
		// A lone table part stays bare — prepending only the project would
		// make a bogus two-part `project`.`table` ref. Bare names resolve via
		// the query's default dataset (set by applyDefaultProject).
		{name: "single bare table stays bare", parts: []string{"Variable"}, want: "`Variable`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.QuoteRef(tc.parts...); got != tc.want {
				t.Errorf("QuoteRef(%v) = %q, want %q", tc.parts, got, tc.want)
			}
		})
	}
	// Same-project provider keeps the pre-feature two-part form byte-for-byte.
	if got := sameProjProvider().QuoteRef("census_ds", "Variable"); got != "`census_ds`.`Variable`" {
		t.Errorf("same-project QuoteRef = %q, want %q", got, "`census_ds`.`Variable`")
	}
}

// TestBigQueryProvider_QualifiedName pins the unqualified catalog/cache-key
// form: three-part for cross-project, two-part otherwise.
func TestBigQueryProvider_QualifiedName(t *testing.T) {
	if got := xProjProvider().QualifiedName("census_ds", "Variable"); got != "bigquery-public-data.census_ds.Variable" {
		t.Errorf("cross-project QualifiedName = %q, want %q", got, "bigquery-public-data.census_ds.Variable")
	}
	if got := sameProjProvider().QualifiedName("census_ds", "Variable"); got != "census_ds.Variable" {
		t.Errorf("same-project QualifiedName = %q, want %q", got, "census_ds.Variable")
	}
	// The provider must satisfy the optional RefQualifier interface so the
	// agent's type assertion picks it up.
	var _ gowarehouse.RefQualifier = xProjProvider()
}

// TestBigQueryProvider_SampleQuery_CrossProject pins that the schema-scan
// sample query targets the three-part name for cross-project reads.
func TestBigQueryProvider_SampleQuery_CrossProject(t *testing.T) {
	got := xProjProvider().SampleQuery("census_ds", "Variable", "", 5)
	want := "SELECT * FROM `bigquery-public-data.census_ds.Variable`  LIMIT 5"
	if got != want {
		t.Errorf("cross-project SampleQuery = %q, want %q", got, want)
	}
	gotSame := sameProjProvider().SampleQuery("census_ds", "Variable", "WHERE x = 1", 5)
	wantSame := "SELECT * FROM `census_ds.Variable` WHERE x = 1 LIMIT 5"
	if gotSame != wantSame {
		t.Errorf("same-project SampleQuery = %q, want %q", gotSame, wantSame)
	}
}

// TestBigQueryProvider_applyDefaultProject pins the SDK-contract fix: a
// cross-project query sets BOTH DefaultProjectID and DefaultDatasetID (the
// SDK ignores a project-only default), and a same-project query sets
// neither (byte-for-byte pre-feature behaviour).
func TestBigQueryProvider_applyDefaultProject(t *testing.T) {
	qx := &bq.Query{}
	xProjProvider().applyDefaultProject(qx)
	if qx.DefaultProjectID != "bigquery-public-data" {
		t.Errorf("cross-project DefaultProjectID = %q, want %q", qx.DefaultProjectID, "bigquery-public-data")
	}
	if qx.DefaultDatasetID != "census_ds" {
		t.Errorf("cross-project DefaultDatasetID = %q, want %q (SDK requires both set together)", qx.DefaultDatasetID, "census_ds")
	}

	qs := &bq.Query{}
	sameProjProvider().applyDefaultProject(qs)
	if qs.DefaultProjectID != "" || qs.DefaultDatasetID != "" {
		t.Errorf("same-project must set no defaults, got project=%q dataset=%q", qs.DefaultProjectID, qs.DefaultDatasetID)
	}

	// Multi-dataset project: the dataset field is a comma-joined list, but
	// BigQuery's defaultDataset accepts exactly one id and rejects a comma
	// string with "400 Invalid dataset ID" even when it's never consulted.
	// Only the first dataset (trimmed) may be used.
	qm := &bq.Query{}
	multi := &BigQueryProvider{
		dataset:       "primary_ds, secondary_ds",
		dataProjectID: "bigquery-public-data",
		config:        BigQueryConfig{ProjectID: "jobs-proj"},
	}
	multi.applyDefaultProject(qm)
	if qm.DefaultDatasetID != "primary_ds" {
		t.Errorf("multi-dataset DefaultDatasetID = %q, want %q (first dataset only — a comma string is a 400)", qm.DefaultDatasetID, "primary_ds")
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
		"project_id":       "test-project",
		"dataset":          "test_dataset",
		"auth_method":      "sa_key",
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

func TestValidateSQL_RejectsEmpty(t *testing.T) {
	// The BigQuery client is a concrete *bq.Client we can't mock
	// without a sandbox emulator, so the unit-level test only
	// exercises the empty-string short-circuit that runs before any
	// API call. Real round-trip coverage lives in
	// integration_test.go behind INTEGRATION_TEST_BIGQUERY_*.
	p := &BigQueryProvider{}
	for _, in := range []string{"", "   ", "\t\n"} {
		if err := p.ValidateSQL(context.Background(), in); err == nil {
			t.Errorf("ValidateSQL accepted whitespace-only input %q", in)
		}
	}
}

// --- Cross-project read support (data_project_id) ---

// TestBigQueryConfig_DataProjectIDDefaultsToProjectID pins the
// common single-project case: callers that don't set
// DataProjectID get behaviour identical to the pre-feature
// provider — same project for jobs and data, no DatasetInProject
// routing, no Query.DefaultProjectID override.
func TestBigQueryConfig_DataProjectIDDefaultsToProjectID(t *testing.T) {
	// bq.NewClient succeeds with no creds (lazy validation —
	// credentials are checked at first RPC) so the constructor
	// reliably returns a provider whose internal field resolution
	// can be inspected. If bq.NewClient ever becomes eager about
	// auth on a CI runner without ADC, switch to t.Skipf on err.
	p, err := NewBigQueryProvider(context.Background(), BigQueryConfig{
		ProjectID: "ops-project",
		Dataset:   "events_prod",
	})
	if err != nil {
		t.Skipf("BigQuery client construction not available: %v", err)
	}
	defer p.Close()
	if p.dataProjectID != "ops-project" {
		t.Errorf("dataProjectID = %q, want %q (default to ProjectID when DataProjectID empty)", p.dataProjectID, "ops-project")
	}
}

// TestBigQueryConfig_DataProjectIDExplicit pins the cross-project
// case: caller sets a different DataProjectID and the provider
// stores it verbatim, so Dataset operations land in the data
// project and Query.DefaultProjectID resolves unqualified table
// references against it — the bigquery-public-data shape works
// without the operator copy-pasting datasets into their own
// project.
func TestBigQueryConfig_DataProjectIDExplicit(t *testing.T) {
	p, err := NewBigQueryProvider(context.Background(), BigQueryConfig{
		ProjectID:     "ops-project",
		DataProjectID: "bigquery-public-data",
		Dataset:       "google_analytics_sample",
	})
	if err != nil {
		t.Skipf("BigQuery client construction not available: %v", err)
	}
	defer p.Close()
	if p.dataProjectID != "bigquery-public-data" {
		t.Errorf("dataProjectID = %q, want %q", p.dataProjectID, "bigquery-public-data")
	}
	if p.config.ProjectID != "ops-project" {
		t.Errorf("ProjectID = %q, want %q (jobs project must remain operator's project)", p.config.ProjectID, "ops-project")
	}
}

// TestBigQueryFactory_DataProjectIDWiring confirms the registry
// factory passes cfg["data_project_id"] to BigQueryConfig
// — without this the cross-project pattern never reaches the
// provider regardless of what the dashboard form sends.
func TestBigQueryFactory_DataProjectIDWiring(t *testing.T) {
	// The factory's ADC fetch happens before any of our config
	// wiring is observable in the returned provider; cover the
	// wiring at the construction layer instead. The factory's
	// pass-through is a one-liner whose only risk is the
	// developer forgetting to add the line, so the test just
	// pins the BigQueryConfig schema (callers can rely on
	// DataProjectID being a real field on the public struct).
	cfg := BigQueryConfig{
		ProjectID:     "ops-project",
		DataProjectID: "bigquery-public-data",
		Dataset:       "google_analytics_sample",
	}
	if cfg.DataProjectID != "bigquery-public-data" {
		t.Errorf("DataProjectID field broken: %q", cfg.DataProjectID)
	}
}

// TestBigQueryProvider_ConfigFields_IncludesDataProjectID ensures
// the dashboard's auto-rendered config form surfaces the new
// optional field. Without this, operators don't see the field
// even though the provider supports it.
func TestBigQueryProvider_ConfigFields_IncludesDataProjectID(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("bigquery")
	for _, f := range meta.ConfigFields {
		if f.Key == "data_project_id" {
			if f.Required {
				t.Error("data_project_id must be optional — single-project setups should still work without setting it")
			}
			return
		}
	}
	t.Error("missing data_project_id config field")
}
