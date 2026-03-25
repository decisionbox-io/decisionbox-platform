package snowflake

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func TestRegistered(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("snowflake")
	if !ok {
		t.Fatal("snowflake provider not registered")
	}
	if meta.Name != "Snowflake" {
		t.Errorf("expected name 'Snowflake', got %q", meta.Name)
	}
	if len(meta.ConfigFields) == 0 {
		t.Error("expected config fields to be populated")
	}
}

func TestRegisteredConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("snowflake")

	required := map[string]bool{}
	for _, f := range meta.ConfigFields {
		required[f.Key] = f.Required
	}

	for _, key := range []string{"account", "user", "warehouse", "database", "dataset"} {
		if !required[key] {
			t.Errorf("config field %q should be required", key)
		}
	}
	if required["role"] {
		t.Error("role should not be required")
	}
}

func TestFactoryMissingAccount(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB",
	})
	if err == nil {
		t.Fatal("expected error for missing account")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("error should mention 'account', got: %v", err)
	}
}

func TestFactoryMissingUser(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"warehouse": "WH",
		"database":  "DB",
	})
	if err == nil {
		t.Fatal("expected error for missing user")
	}
	if !strings.Contains(err.Error(), "user") {
		t.Errorf("error should mention 'user', got: %v", err)
	}
}

func TestFactoryMissingWarehouse(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":  "org-acct",
		"user":     "test",
		"database": "DB",
	})
	if err == nil {
		t.Fatal("expected error for missing warehouse")
	}
}

func TestFactoryMissingDatabase(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
	})
	if err == nil {
		t.Fatal("expected error for missing database")
	}
}

func TestFactoryInvalidDatabaseName(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB; DROP TABLE",
		"password":  "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid database name")
	}
	if !strings.Contains(err.Error(), "invalid database name") {
		t.Errorf("error should mention 'invalid database name', got: %v", err)
	}
}

func TestFactoryInvalidSchemaName(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB",
		"dataset":   "SCHEMA; DROP TABLE",
		"password":  "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid schema name")
	}
	if !strings.Contains(err.Error(), "invalid schema name") {
		t.Errorf("error should mention 'invalid schema name', got: %v", err)
	}
}

func TestFactoryPasswordAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":          "org-acct",
		"user":             "test",
		"warehouse":        "WH",
		"database":         "DB",
		"credentials_json": "my-password",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	sp := p.(*SnowflakeProvider)
	if sp.database != "DB" {
		t.Errorf("expected database 'DB', got %q", sp.database)
	}
	if sp.schema != "PUBLIC" {
		t.Errorf("expected default schema 'PUBLIC', got %q", sp.schema)
	}
}

func TestFactoryKeyPairAuth(t *testing.T) {
	// Generate a test RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8: %v", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})

	p, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":          "org-acct",
		"user":             "test",
		"warehouse":        "WH",
		"database":         "DB",
		"credentials_json": string(pemBlock),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()
}

func TestFactoryCustomSchema(t *testing.T) {
	p, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB",
		"dataset":   "ANALYTICS",
		"password":  "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	sp := p.(*SnowflakeProvider)
	if sp.schema != "ANALYTICS" {
		t.Errorf("expected schema 'ANALYTICS', got %q", sp.schema)
	}
}

func TestSQLDialect(t *testing.T) {
	p := &SnowflakeProvider{}
	dialect := p.SQLDialect()
	if !strings.Contains(dialect, "Snowflake") {
		t.Errorf("dialect should mention Snowflake, got %q", dialect)
	}
	for _, keyword := range []string{"QUALIFY", "FLATTEN", "VARIANT", "ILIKE", "LATERAL"} {
		if !strings.Contains(dialect, keyword) {
			t.Errorf("dialect should mention %s, got %q", keyword, dialect)
		}
	}
}

func TestSQLFixPrompt(t *testing.T) {
	p := &SnowflakeProvider{}
	prompt := p.SQLFixPrompt()
	if prompt == "" {
		t.Error("expected non-empty SQL fix prompt")
	}
	for _, required := range []string{
		"{{DATASET}}", "{{ORIGINAL_SQL}}", "{{ERROR_MESSAGE}}", "{{SCHEMA_INFO}}",
		"QUALIFY", "FLATTEN", "LATERAL", "VARIANT", "ILIKE",
		"TRY_CAST", "CURRENT_DATABASE", "UPPERCASE",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("SQL fix prompt should contain %q", required)
		}
	}
}

func TestGetDataset(t *testing.T) {
	p := &SnowflakeProvider{schema: "MY_SCHEMA"}
	if p.GetDataset() != "MY_SCHEMA" {
		t.Errorf("expected 'MY_SCHEMA', got %q", p.GetDataset())
	}
}

func TestHealthCheck(t *testing.T) {
	mock := &mockSFClient{pingErr: nil}
	p := &SnowflakeProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	mock := &mockSFClient{pingErr: fmt.Errorf("connection refused")}
	p := &SnowflakeProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err == nil {
		t.Error("expected error")
	}
}

func TestClose(t *testing.T) {
	mock := &mockSFClient{closeErr: nil}
	p := &SnowflakeProvider{client: mock}

	if err := p.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNormalizeSnowflakeType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Integer types
		{"NUMBER", "INT64"},
		{"INT", "INT64"},
		{"INTEGER", "INT64"},
		{"BIGINT", "INT64"},
		{"SMALLINT", "INT64"},
		{"TINYINT", "INT64"},
		{"BYTEINT", "INT64"},

		// Float types
		{"FLOAT", "FLOAT64"},
		{"FLOAT4", "FLOAT64"},
		{"FLOAT8", "FLOAT64"},
		{"DOUBLE", "FLOAT64"},
		{"DOUBLE PRECISION", "FLOAT64"},
		{"REAL", "FLOAT64"},
		{"DECIMAL(18,2)", "FLOAT64"},
		{"NUMERIC(10,4)", "FLOAT64"},

		// Boolean
		{"BOOLEAN", "BOOL"},

		// Date/time
		{"DATE", "DATE"},
		{"TIMESTAMP_NTZ", "TIMESTAMP"},
		{"TIMESTAMP_LTZ", "TIMESTAMP"},
		{"TIMESTAMP_TZ", "TIMESTAMP"},
		{"DATETIME", "TIMESTAMP"},

		// Binary
		{"BINARY", "BYTES"},
		{"VARBINARY", "BYTES"},

		// Semi-structured
		{"VARIANT", "RECORD"},
		{"OBJECT", "RECORD"},
		{"ARRAY", "RECORD"},

		// String types
		{"VARCHAR", "STRING"},
		{"CHAR", "STRING"},
		{"STRING", "STRING"},
		{"TEXT", "STRING"},
		{"TIME", "STRING"},

		// Case insensitivity
		{"number", "INT64"},
		{"float", "FLOAT64"},
		{"boolean", "BOOL"},
		{"variant", "RECORD"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeSnowflakeType(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeSnowflakeType(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeValue(t *testing.T) {
	// nil
	if v := normalizeValue(nil, ""); v != nil {
		t.Errorf("expected nil, got %v", v)
	}

	// []byte → string
	if v := normalizeValue([]byte("hello"), ""); v != "hello" {
		t.Errorf("expected 'hello', got %v", v)
	}

	// time.Time → RFC3339 string
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	if v := normalizeValue(ts, ""); v != "2026-01-15T10:30:00Z" {
		t.Errorf("expected RFC3339, got %v", v)
	}

	// int64 passthrough
	if v := normalizeValue(int64(42), ""); v != int64(42) {
		t.Errorf("expected 42, got %v", v)
	}

	// string passthrough (non-numeric type)
	if v := normalizeValue("test", "VARCHAR"); v != "test" {
		t.Errorf("expected 'test', got %v", v)
	}

	// FIXED/NUMBER string → int64
	if v := normalizeValue("42", "FIXED"); v != int64(42) {
		t.Errorf("expected int64(42), got %v (type %T)", v, v)
	}

	// FIXED/NUMBER string with decimal → float64
	if v := normalizeValue("3.14", "FIXED"); v != float64(3.14) {
		t.Errorf("expected float64(3.14), got %v (type %T)", v, v)
	}

	// NUMBER type → int64
	if v := normalizeValue("100", "NUMBER"); v != int64(100) {
		t.Errorf("expected int64(100), got %v (type %T)", v, v)
	}

	// FLOAT type string → float64
	if v := normalizeValue("2.718", "FLOAT"); v != float64(2.718) {
		t.Errorf("expected float64(2.718), got %v (type %T)", v, v)
	}

	// bool passthrough
	if v := normalizeValue(true, "BOOLEAN"); v != true {
		t.Errorf("expected true, got %v", v)
	}
}

func TestParsePrivateKey_PKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))

	parsed, err := parsePrivateKey(pemStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.N.Cmp(key.N) != 0 {
		t.Error("parsed key does not match original")
	}
}

func TestParsePrivateKey_PKCS1(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := x509.MarshalPKCS1PrivateKey(key)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1}))

	parsed, err := parsePrivateKey(pemStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.N.Cmp(key.N) != 0 {
		t.Error("parsed key does not match original")
	}
}

func TestParsePrivateKey_Invalid(t *testing.T) {
	_, err := parsePrivateKey("not a PEM block")
	if err == nil {
		t.Error("expected error for invalid PEM")
	}
}

func TestValidateReadOnly(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("mock: not supported")
		},
	}
	p := &SnowflakeProvider{client: mock}

	err := p.ValidateReadOnly(context.Background())
	if err == nil {
		t.Error("expected error when query fails")
	}
	if !strings.Contains(err.Error(), "read-only validation failed") {
		t.Errorf("error should mention 'read-only validation failed', got: %v", err)
	}
}

func TestDefaultPricing(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("snowflake")
	if meta.DefaultPricing == nil {
		t.Fatal("expected default pricing")
	}
	if meta.DefaultPricing.CostModel != "per_second" {
		t.Errorf("expected cost model 'per_second', got %q", meta.DefaultPricing.CostModel)
	}
}

// --- Unhappy path tests ---

func TestQueryError(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	p := &SnowflakeProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "query failed") {
		t.Errorf("error should mention 'query failed', got: %v", err)
	}
}

func TestQueryContextCancelled(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, context.Canceled
		},
	}
	p := &SnowflakeProvider{client: mock, timeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := p.Query(ctx, "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestListTablesError(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("access denied")
		},
	}
	p := &SnowflakeProvider{client: mock, schema: "PUBLIC", database: "DB"}

	_, err := p.ListTables(context.Background())
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "list tables failed") {
		t.Errorf("error should mention 'list tables failed', got: %v", err)
	}
}

func TestGetTableSchemaError(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &SnowflakeProvider{client: mock, schema: "PUBLIC", database: "DB"}

	_, err := p.GetTableSchema(context.Background(), "NONEXISTENT")
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "get table schema failed") {
		t.Errorf("error should mention 'get table schema failed', got: %v", err)
	}
}

func TestCloseError(t *testing.T) {
	mock := &mockSFClient{closeErr: fmt.Errorf("close failed")}
	p := &SnowflakeProvider{client: mock}

	err := p.Close()
	if err == nil {
		t.Error("expected error")
	}
}

func TestGetDatasetEmpty(t *testing.T) {
	p := &SnowflakeProvider{schema: ""}
	if p.GetDataset() != "" {
		t.Errorf("expected empty string, got %q", p.GetDataset())
	}
}

func TestListTablesDefaultSchema(t *testing.T) {
	mock := &mockSFClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			// Verify the schema passed to the query
			if len(args) > 0 {
				if schema, ok := args[0].(string); ok && schema != "MY_SCHEMA" {
					return nil, fmt.Errorf("unexpected schema: %s", schema)
				}
			}
			return nil, fmt.Errorf("mock: expected error for verification")
		},
	}
	p := &SnowflakeProvider{client: mock, schema: "MY_SCHEMA", database: "DB"}

	// ListTables should use the default schema
	_, _ = p.ListTables(context.Background())
	if len(mock.lastArgs) == 0 {
		t.Error("expected query args to be set")
	}
}

func TestConvertStringByType(t *testing.T) {
	tests := []struct {
		name     string
		val      string
		dbType   string
		expected interface{}
	}{
		{"FIXED integer", "42", "FIXED", int64(42)},
		{"FIXED decimal", "3.14", "FIXED", float64(3.14)},
		{"FIXED negative", "-100", "FIXED", int64(-100)},
		{"FIXED large", "9999999999999", "FIXED", int64(9999999999999)},
		{"NUMBER integer", "7", "NUMBER", int64(7)},
		{"DECIMAL decimal", "1.5", "DECIMAL", float64(1.5)},
		{"NUMERIC decimal", "2.5", "NUMERIC", float64(2.5)},
		{"FLOAT", "2.718", "FLOAT", float64(2.718)},
		{"REAL", "9.81", "REAL", float64(9.81)},
		{"DOUBLE", "1.618", "DOUBLE", float64(1.618)},
		{"unparseable FIXED", "abc", "FIXED", "abc"},
		{"unparseable FLOAT", "xyz", "FLOAT", "xyz"},
		{"VARCHAR passthrough", "hello", "VARCHAR", "hello"},
		{"TEXT passthrough", "world", "TEXT", "world"},
		{"empty type passthrough", "test", "", "test"},
		{"case insensitive", "42", "fixed", int64(42)},
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

func TestParsePrivateKey_InvalidPEMContent(t *testing.T) {
	// Valid PEM block but invalid key content
	pemStr := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("not a valid key"),
	}))
	_, err := parsePrivateKey(pemStr)
	if err == nil {
		t.Error("expected error for invalid key content")
	}
}

func TestFactoryNoCredentials(t *testing.T) {
	_, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB",
	})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("error should mention 'credentials', got: %v", err)
	}
}

func TestFactoryTimeoutConfig(t *testing.T) {
	p, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":         "org-acct",
		"user":            "test",
		"warehouse":       "WH",
		"database":        "DB",
		"password":        "pw",
		"timeout_minutes": "10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	sp := p.(*SnowflakeProvider)
	if sp.timeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", sp.timeout)
	}
}

func TestFactoryDefaultTimeout(t *testing.T) {
	p, err := gowarehouse.NewProvider("snowflake", gowarehouse.ProviderConfig{
		"account":   "org-acct",
		"user":      "test",
		"warehouse": "WH",
		"database":  "DB",
		"password":  "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	sp := p.(*SnowflakeProvider)
	if sp.timeout != 5*time.Minute {
		t.Errorf("expected 5m default timeout, got %v", sp.timeout)
	}
}

