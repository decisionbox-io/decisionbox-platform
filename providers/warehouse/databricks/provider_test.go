package databricks

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// --- Registration tests ---

func TestRegistered(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("databricks")
	if !ok {
		t.Fatal("databricks provider not registered")
	}
	if meta.Name != "Databricks" {
		t.Errorf("expected name 'Databricks', got %q", meta.Name)
	}
	if len(meta.ConfigFields) == 0 {
		t.Error("expected config fields to be populated")
	}
}

func TestRegisteredConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("databricks")

	required := map[string]bool{}
	for _, f := range meta.ConfigFields {
		required[f.Key] = f.Required
	}

	for _, key := range []string{"host", "http_path", "catalog", "dataset"} {
		if !required[key] {
			t.Errorf("config field %q should be required", key)
		}
	}
}

func TestRegisteredAuthMethods(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("databricks")
	if len(meta.AuthMethods) != 2 {
		t.Fatalf("expected 2 auth methods, got %d", len(meta.AuthMethods))
	}
	ids := map[string]bool{}
	for _, m := range meta.AuthMethods {
		ids[m.ID] = true
	}
	if !ids["pat"] {
		t.Error("missing 'pat' auth method")
	}
	if !ids["oauth_m2m"] {
		t.Error("missing 'oauth_m2m' auth method")
	}
}

func TestAuthMethodFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("databricks")

	var patMethod, oauthMethod *gowarehouse.AuthMethod
	for i := range meta.AuthMethods {
		switch meta.AuthMethods[i].ID {
		case "pat":
			patMethod = &meta.AuthMethods[i]
		case "oauth_m2m":
			oauthMethod = &meta.AuthMethods[i]
		}
	}

	if patMethod == nil {
		t.Fatal("missing pat auth method")
		return
	}
	if len(patMethod.Fields) != 1 || patMethod.Fields[0].Type != "credential" {
		t.Error("pat method should have 1 credential field")
	}
	if oauthMethod == nil {
		t.Fatal("missing oauth_m2m auth method")
		return
	}
	if len(oauthMethod.Fields) != 1 || oauthMethod.Fields[0].Type != "credential" {
		t.Error("oauth_m2m method should have 1 credential field")
	}
}

func TestDefaultPricing(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("databricks")
	if meta.DefaultPricing == nil {
		t.Fatal("expected default pricing")
	}
	if meta.DefaultPricing.CostModel != "per_hour" {
		t.Errorf("expected cost model 'per_hour', got %q", meta.DefaultPricing.CostModel)
	}
}

// --- Factory validation tests ---

func TestFactoryMissingHost(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"http_path":       "/sql/1.0/warehouses/xxx",
		"catalog":         "main",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error should mention 'host', got: %v", err)
	}
}

func TestFactoryMissingHTTPPath(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"catalog":          "main",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing http_path")
	}
	if !strings.Contains(err.Error(), "http_path") {
		t.Errorf("error should mention 'http_path', got: %v", err)
	}
}

func TestFactoryMissingCatalog(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for missing catalog")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Errorf("error should mention 'catalog', got: %v", err)
	}
}

func TestFactoryMissingCredentials(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":      "xxx.cloud.databricks.com",
		"http_path": "/sql/1.0/warehouses/xxx",
		"catalog":   "main",
	})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !strings.Contains(err.Error(), "credentials are required") {
		t.Errorf("error should mention 'credentials are required', got: %v", err)
	}
}

func TestFactoryInvalidCatalogName(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "catalog; DROP TABLE",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for invalid catalog name")
	}
	if !strings.Contains(err.Error(), "invalid catalog name") {
		t.Errorf("error should mention 'invalid catalog name', got: %v", err)
	}
}

func TestFactoryInvalidSchemaName(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"dataset":          "schema; DROP TABLE",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for invalid schema name")
	}
	if !strings.Contains(err.Error(), "invalid schema name") {
		t.Errorf("error should mention 'invalid schema name', got: %v", err)
	}
}

func TestFactoryInvalidPort(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"port":             "abc",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
	if !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("error should mention 'invalid port', got: %v", err)
	}
}

func TestFactoryUnsupportedAuthMethod(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"auth_method":      "kerberos",
		"credentials_json": "dapi_test_token",
	})
	if err == nil {
		t.Fatal("expected error for unsupported auth method")
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error should mention unsupported, got: %v", err)
	}
}

func TestFactoryOAuthM2MMissingFormat(t *testing.T) {
	_, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"auth_method":      "oauth_m2m",
		"credentials_json": "missing-colon",
	})
	if err == nil {
		t.Fatal("expected error for invalid OAuth M2M format")
	}
	if !strings.Contains(err.Error(), "client_id:client_secret") {
		t.Errorf("error should mention format, got: %v", err)
	}
}

// --- Factory success tests ---

func TestFactoryPATAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"credentials_json": "dapi_test_token",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	dp := p.(*DatabricksProvider)
	if dp.schema != "default" {
		t.Errorf("expected default schema 'default', got %q", dp.schema)
	}
	if dp.catalog != "main" {
		t.Errorf("expected catalog 'main', got %q", dp.catalog)
	}
}

func TestFactoryCustomSchema(t *testing.T) {
	p, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"dataset":          "analytics",
		"credentials_json": "dapi_test_token",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	dp := p.(*DatabricksProvider)
	if dp.schema != "analytics" {
		t.Errorf("expected schema 'analytics', got %q", dp.schema)
	}
}

func TestFactoryOAuthM2MAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"auth_method":      "oauth_m2m",
		"credentials_json": "client-id-123:client-secret-456",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()
}

func TestFactoryTimeoutConfig(t *testing.T) {
	p, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"credentials_json": "dapi_test_token",
		"timeout_minutes":  "10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	dp := p.(*DatabricksProvider)
	if dp.timeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", dp.timeout)
	}
}

func TestFactoryDefaultTimeout(t *testing.T) {
	p, err := gowarehouse.NewProvider("databricks", gowarehouse.ProviderConfig{
		"host":             "xxx.cloud.databricks.com",
		"http_path":        "/sql/1.0/warehouses/xxx",
		"catalog":          "main",
		"credentials_json": "dapi_test_token",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	dp := p.(*DatabricksProvider)
	if dp.timeout != 5*time.Minute {
		t.Errorf("expected 5m default timeout, got %v", dp.timeout)
	}
}

// --- Provider method tests ---

func TestSQLDialect(t *testing.T) {
	p := &DatabricksProvider{}
	dialect := p.SQLDialect()
	if !strings.Contains(dialect, "Databricks") {
		t.Errorf("dialect should mention Databricks, got %q", dialect)
	}
	for _, keyword := range []string{"QUALIFY", "PIVOT", "UNPIVOT", "LATERAL VIEW", "Delta"} {
		if !strings.Contains(dialect, keyword) {
			t.Errorf("dialect should mention %s, got %q", keyword, dialect)
		}
	}
}

func TestSQLFixPrompt(t *testing.T) {
	p := &DatabricksProvider{}
	prompt := p.SQLFixPrompt()
	if prompt == "" {
		t.Error("expected non-empty SQL fix prompt")
	}
	for _, required := range []string{
		"{{DATASET}}", "{{ORIGINAL_SQL}}", "{{ERROR_MESSAGE}}", "{{SCHEMA_INFO}}",
		"QUALIFY", "PIVOT", "UNPIVOT", "explode", "explode_outer",
		"collect_list", "collect_set", "any_value", "TRY_CAST", "NULLIF",
		"yyyy-MM-dd", "YYYY", "TABLE_OR_VIEW_NOT_FOUND", "UNRESOLVED_COLUMN",
		"MISSING_AGGREGATION", "DIVIDE_BY_ZERO", "Delta",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("SQL fix prompt should contain %q", required)
		}
	}
}

func TestGetDataset(t *testing.T) {
	p := &DatabricksProvider{schema: "my_schema"}
	if p.GetDataset() != "my_schema" {
		t.Errorf("expected 'my_schema', got %q", p.GetDataset())
	}
}

func TestGetDatasetEmpty(t *testing.T) {
	p := &DatabricksProvider{schema: ""}
	if p.GetDataset() != "" {
		t.Errorf("expected empty string, got %q", p.GetDataset())
	}
}

// --- Mock-based method tests ---

func TestHealthCheck(t *testing.T) {
	mock := &mockDBClient{pingErr: nil}
	p := &DatabricksProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	mock := &mockDBClient{pingErr: fmt.Errorf("connection refused")}
	p := &DatabricksProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err == nil {
		t.Error("expected error")
	}
}

func TestClose(t *testing.T) {
	mock := &mockDBClient{closeErr: nil}
	p := &DatabricksProvider{client: mock}

	if err := p.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCloseError(t *testing.T) {
	mock := &mockDBClient{closeErr: fmt.Errorf("close failed")}
	p := &DatabricksProvider{client: mock}

	err := p.Close()
	if err == nil {
		t.Error("expected error")
	}
}

func TestValidateReadOnly(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("mock: not supported")
		},
	}
	p := &DatabricksProvider{client: mock}

	err := p.ValidateReadOnly(context.Background())
	if err == nil {
		t.Error("expected error when query fails")
	}
	if !strings.Contains(err.Error(), "read-only validation failed") {
		t.Errorf("error should mention 'read-only validation failed', got: %v", err)
	}
}

func TestQueryError(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	p := &DatabricksProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "query failed") {
		t.Errorf("error should mention 'query failed', got: %v", err)
	}
}

func TestQueryContextCancelled(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, context.Canceled
		},
	}
	p := &DatabricksProvider{client: mock, timeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Query(ctx, "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestQueryErrorWrapping(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("syntax error at position 42")
		},
	}
	p := &DatabricksProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT BAD", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "databricks:") {
		t.Errorf("error should be wrapped with 'databricks:', got: %v", err)
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("error should contain original message, got: %v", err)
	}
}

func TestListTablesError(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("access denied")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "default"}

	_, err := p.ListTables(context.Background())
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "list tables failed") {
		t.Errorf("error should mention 'list tables failed', got: %v", err)
	}
}

func TestListTablesUsesDefaultSchema(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			if !strings.Contains(query, "'custom_schema'") {
				return nil, fmt.Errorf("query should contain schema: %s", query)
			}
			return nil, fmt.Errorf("expected")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "custom_schema"}

	_, _ = p.ListTables(context.Background())
	if !strings.Contains(mock.lastQuery, "'custom_schema'") {
		t.Errorf("expected query to contain 'custom_schema', got: %s", mock.lastQuery)
	}
}

func TestListTablesQueryUsesCatalog(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			if !strings.Contains(query, "my_catalog.information_schema.tables") {
				return nil, fmt.Errorf("query should reference catalog: %s", query)
			}
			return nil, fmt.Errorf("expected")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "my_catalog", schema: "default"}

	_, _ = p.ListTables(context.Background())
}

func TestGetTableSchemaError(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "default"}

	_, err := p.GetTableSchema(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "get table schema failed") {
		t.Errorf("error should mention 'get table schema failed', got: %v", err)
	}
}

// --- Type normalization tests ---

func TestNormalizeDatabricksType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Integer types
		{"tinyint", "INT64"},
		{"smallint", "INT64"},
		{"int", "INT64"},
		{"integer", "INT64"},
		{"bigint", "INT64"},

		// Float types
		{"float", "FLOAT64"},
		{"double", "FLOAT64"},
		{"decimal(10,2)", "FLOAT64"},
		{"decimal", "FLOAT64"},

		// Boolean
		{"boolean", "BOOL"},

		// Date/time
		{"date", "DATE"},
		{"timestamp", "TIMESTAMP"},
		{"timestamp_ntz", "TIMESTAMP"},

		// Binary
		{"binary", "BYTES"},

		// Complex types
		{"array<string>", "RECORD"},
		{"array<int>", "RECORD"},
		{"map<string,int>", "RECORD"},
		{"struct<name:string,age:int>", "RECORD"},
		{"variant", "RECORD"},
		{"object", "RECORD"},

		// String types
		{"string", "STRING"},
		{"char(10)", "STRING"},
		{"varchar(100)", "STRING"},
		{"void", "STRING"},
		{"interval", "STRING"},

		// Case insensitivity
		{"BIGINT", "INT64"},
		{"DOUBLE", "FLOAT64"},
		{"BOOLEAN", "BOOL"},
		{"ARRAY<STRING>", "RECORD"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeDatabricksType(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeDatabricksType(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// --- Value normalization tests ---

func TestNormalizeValue(t *testing.T) {
	// nil
	if v := normalizeValue(nil, ""); v != nil {
		t.Errorf("expected nil, got %v", v)
	}

	// []byte DECIMAL → float64
	if v := normalizeValue([]byte("123.45"), "DECIMAL"); v != float64(123.45) {
		t.Errorf("expected float64(123.45), got %v (%T)", v, v)
	}

	// []byte DECIMAL integer → float64 (always float64 for consistency)
	if v := normalizeValue([]byte("42"), "DECIMAL"); v != float64(42) {
		t.Errorf("expected float64(42), got %v (%T)", v, v)
	}

	// []byte non-DECIMAL → string
	if v := normalizeValue([]byte("hello"), "STRING"); v != "hello" {
		t.Errorf("expected 'hello', got %v", v)
	}

	// time.Time → RFC3339 string
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	if v := normalizeValue(ts, ""); v != "2026-01-15T10:30:00Z" {
		t.Errorf("expected RFC3339, got %v", v)
	}

	// int8 → int64
	if v := normalizeValue(int8(42), ""); v != int64(42) {
		t.Errorf("expected int64(42), got %v (%T)", v, v)
	}

	// int16 → int64
	if v := normalizeValue(int16(1000), ""); v != int64(1000) {
		t.Errorf("expected int64(1000), got %v (%T)", v, v)
	}

	// int32 → int64
	if v := normalizeValue(int32(100000), ""); v != int64(100000) {
		t.Errorf("expected int64(100000), got %v (%T)", v, v)
	}

	// int64 passthrough
	if v := normalizeValue(int64(42), ""); v != int64(42) {
		t.Errorf("expected int64(42), got %v", v)
	}

	// float32 → float64
	if v := normalizeValue(float32(3.14), ""); v != float64(float32(3.14)) {
		t.Errorf("expected float64, got %v (%T)", v, v)
	}

	// float64 passthrough
	if v := normalizeValue(float64(2.718), ""); v != float64(2.718) {
		t.Errorf("expected 2.718, got %v", v)
	}

	// bool passthrough
	if v := normalizeValue(true, "BOOLEAN"); v != true {
		t.Errorf("expected true, got %v", v)
	}

	// string passthrough (non-DECIMAL type)
	if v := normalizeValue("test", "STRING"); v != "test" {
		t.Errorf("expected 'test', got %v", v)
	}

	// string DECIMAL → float64 (driver returns DECIMAL as string)
	if v := normalizeValue("123.45", "DECIMAL"); v != float64(123.45) {
		t.Errorf("expected float64(123.45), got %v (%T)", v, v)
	}

	// string DECIMAL integer → float64 (always float64 for consistency)
	if v := normalizeValue("42", "DECIMAL"); v != float64(42) {
		t.Errorf("expected float64(42), got %v (%T)", v, v)
	}
}

func TestConvertStringByType(t *testing.T) {
	tests := []struct {
		name     string
		val      string
		dbType   string
		expected interface{}
	}{
		{"DECIMAL integer", "42", "DECIMAL", float64(42)},
		{"DECIMAL decimal", "3.14", "DECIMAL", float64(3.14)},
		{"DECIMAL negative", "-100", "DECIMAL", float64(-100)},
		{"unparseable DECIMAL", "abc", "DECIMAL", "abc"},
		{"STRING passthrough", "hello", "STRING", "hello"},
		{"empty type passthrough", "test", "", "test"},
		{"case insensitive", "42", "decimal", float64(42)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertStringByType(tt.val, tt.dbType)
			if result != tt.expected {
				t.Errorf("convertStringByType(%q, %q) = %v (%T), want %v (%T)",
					tt.val, tt.dbType, result, result, tt.expected, tt.expected)
			}
		})
	}
}

// --- SQL injection prevention tests ---

func TestListTablesInDatasetInvalidSchema(t *testing.T) {
	mock := &mockDBClient{}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "default"}

	_, err := p.ListTablesInDataset(context.Background(), "schema; DROP TABLE")
	if err == nil {
		t.Fatal("expected error for invalid schema name")
	}
	if !strings.Contains(err.Error(), "invalid schema name") {
		t.Errorf("error should mention 'invalid schema name', got: %v", err)
	}
}

func TestGetTableSchemaInDatasetInvalidSchema(t *testing.T) {
	mock := &mockDBClient{}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "default"}

	_, err := p.GetTableSchemaInDataset(context.Background(), "schema; DROP TABLE", "users")
	if err == nil {
		t.Fatal("expected error for invalid schema name")
	}
	if !strings.Contains(err.Error(), "invalid schema name") {
		t.Errorf("error should mention 'invalid schema name', got: %v", err)
	}
}

func TestGetTableSchemaInDatasetInvalidTable(t *testing.T) {
	mock := &mockDBClient{}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "default"}

	_, err := p.GetTableSchemaInDataset(context.Background(), "default", "table; DROP TABLE")
	if err == nil {
		t.Fatal("expected error for invalid table name")
	}
	if !strings.Contains(err.Error(), "invalid table name") {
		t.Errorf("error should mention 'invalid table name', got: %v", err)
	}
}

// --- Empty dataset fallback tests ---

func TestListTablesInDatasetEmptyFallback(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			if !strings.Contains(query, "'my_schema'") {
				return nil, fmt.Errorf("query should contain default schema: %s", query)
			}
			return nil, fmt.Errorf("expected")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "my_schema"}

	_, _ = p.ListTablesInDataset(context.Background(), "")
	if !strings.Contains(mock.lastQuery, "'my_schema'") {
		t.Errorf("expected query to use default schema 'my_schema', got: %s", mock.lastQuery)
	}
}

func TestGetTableSchemaInDatasetEmptyFallback(t *testing.T) {
	mock := &mockDBClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("expected")
		},
	}
	p := &DatabricksProvider{client: mock, catalog: "main", schema: "my_schema"}

	_, _ = p.GetTableSchemaInDataset(context.Background(), "", "users")
	if !strings.Contains(mock.lastQuery, "'my_schema'") {
		t.Errorf("expected query to use default schema 'my_schema', got: %s", mock.lastQuery)
	}
}

// --- Identifier validation tests ---

func TestValidIdentifier(t *testing.T) {
	valid := []string{"main", "my_schema", "Schema123", "_private", "a$b"}
	for _, id := range valid {
		if !validIdentifier(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}

	invalid := []string{"", "123abc", "my schema", "schema;drop", "a-b", "a.b"}
	for _, id := range invalid {
		if validIdentifier(id) {
			t.Errorf("expected %q to be invalid", id)
		}
	}
}
