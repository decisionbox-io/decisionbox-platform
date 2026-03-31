package postgres

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
	meta, ok := gowarehouse.GetProviderMeta("postgres")
	if !ok {
		t.Fatal("postgres provider not registered")
	}
	if meta.Name != "PostgreSQL" {
		t.Errorf("expected name 'PostgreSQL', got %q", meta.Name)
	}
	if len(meta.ConfigFields) == 0 {
		t.Error("expected config fields to be populated")
	}
}

func TestRegisteredConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("postgres")

	required := map[string]bool{}
	for _, f := range meta.ConfigFields {
		required[f.Key] = f.Required
	}

	for _, key := range []string{"host", "database", "user", "dataset"} {
		if !required[key] {
			t.Errorf("config field %q should be required", key)
		}
	}
	if required["port"] {
		t.Error("port should not be required")
	}
	if required["sslmode"] {
		t.Error("sslmode should not be required")
	}
}

func TestRegisteredAuthMethods(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("postgres")
	if len(meta.AuthMethods) != 2 {
		t.Fatalf("expected 2 auth methods, got %d", len(meta.AuthMethods))
	}
	ids := map[string]bool{}
	for _, m := range meta.AuthMethods {
		ids[m.ID] = true
	}
	if !ids["password"] {
		t.Error("missing 'password' auth method")
	}
	if !ids["connection_string"] {
		t.Error("missing 'connection_string' auth method")
	}
}

func TestAuthMethodFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("postgres")

	var pwMethod, csMethod *gowarehouse.AuthMethod
	for i := range meta.AuthMethods {
		switch meta.AuthMethods[i].ID {
		case "password":
			pwMethod = &meta.AuthMethods[i]
		case "connection_string":
			csMethod = &meta.AuthMethods[i]
		}
	}

	if pwMethod == nil {
		t.Fatal("missing password auth method")
		return
	}
	if len(pwMethod.Fields) != 1 || pwMethod.Fields[0].Type != "credential" {
		t.Error("password method should have 1 credential field")
	}
	if csMethod == nil {
		t.Fatal("missing connection_string auth method")
		return
	}
	if len(csMethod.Fields) != 1 || csMethod.Fields[0].Type != "credential" {
		t.Error("connection_string method should have 1 credential field")
	}
}

func TestDefaultPricing(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("postgres")
	if meta.DefaultPricing == nil {
		t.Fatal("expected default pricing")
	}
	if meta.DefaultPricing.CostModel != "per_hour" {
		t.Errorf("expected cost model 'per_hour', got %q", meta.DefaultPricing.CostModel)
	}
}

// --- Factory validation tests ---

func TestFactoryMissingHost(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"database":        "testdb",
		"user":            "testuser",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing host")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error should mention 'host', got: %v", err)
	}
}

func TestFactoryMissingDatabase(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"user":            "testuser",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing database")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("error should mention 'database', got: %v", err)
	}
}

func TestFactoryMissingUser(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing user")
	}
	if !strings.Contains(err.Error(), "user") {
		t.Errorf("error should mention 'user', got: %v", err)
	}
}

func TestFactoryNoCredentials(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":     "localhost",
		"database": "testdb",
		"user":     "testuser",
	})
	if err == nil {
		t.Fatal("expected error for missing password")
	}
	if !strings.Contains(err.Error(), "password is required") {
		t.Errorf("error should mention 'password is required', got: %v", err)
	}
}

func TestFactoryDatabaseWithHyphens(t *testing.T) {
	// Database names with hyphens (e.g., "my-app-db") are common in RDS,
	// Cloud SQL, and Supabase. They must be accepted.
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "my-app-db",
		"user":             "testuser",
		"credentials_json": "pw",
	})
	if err != nil {
		t.Fatalf("database names with hyphens should be accepted: %v", err)
	}
	defer p.Close()
}

func TestFactoryInvalidSchemaName(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"dataset":         "schema; DROP TABLE",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid schema name")
	}
	if !strings.Contains(err.Error(), "invalid schema name") {
		t.Errorf("error should mention 'invalid schema name', got: %v", err)
	}
}

func TestFactoryUnsupportedAuthMethod(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"auth_method":     "kerberos",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for unsupported auth method")
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error should mention unsupported, got: %v", err)
	}
}

func TestFactoryInvalidPort(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"port":             "abc",
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
	if !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("error should mention 'invalid port', got: %v", err)
	}
}

func TestFactoryValidSSLModes(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
			"host":             "localhost",
			"database":         "testdb",
			"user":             "testuser",
			"sslmode":          mode,
			"credentials_json": "pw",
		})
		if err != nil {
			t.Errorf("sslmode %q should be valid: %v", mode, err)
			continue
		}
		p.Close()
	}
}

func TestFactoryInvalidSSLMode(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"sslmode":          "invalid",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid sslmode")
	}
	if !strings.Contains(err.Error(), "invalid sslmode") {
		t.Errorf("error should mention 'invalid sslmode', got: %v", err)
	}
}

func TestFactoryPasswordWithSpaces(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "my password with spaces",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()
}

func TestFactoryConnectionStringMissingDSN(t *testing.T) {
	_, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"auth_method": "connection_string",
	})
	if err == nil {
		t.Fatal("expected error for missing connection string")
	}
	if !strings.Contains(err.Error(), "connection string is required") {
		t.Errorf("error should mention 'connection string is required', got: %v", err)
	}
}

// --- Factory success tests ---

func TestFactoryPasswordAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"credentials_json": "testpass",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*PostgresProvider)
	if pp.schema != "public" {
		t.Errorf("expected default schema 'public', got %q", pp.schema)
	}
}

func TestFactoryCustomSchema(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"dataset":         "analytics",
		"credentials_json": "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*PostgresProvider)
	if pp.schema != "analytics" {
		t.Errorf("expected schema 'analytics', got %q", pp.schema)
	}
}

func TestFactoryConnectionStringAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"auth_method":     "connection_string",
		"credentials_json": "postgres://user:pass@localhost:5432/testdb?sslmode=disable",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()
}

func TestFactoryTimeoutConfig(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"credentials_json": "pw",
		"timeout_minutes": "10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*PostgresProvider)
	if pp.timeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", pp.timeout)
	}
}

func TestFactoryDefaultTimeout(t *testing.T) {
	p, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"host":            "localhost",
		"database":        "testdb",
		"user":            "testuser",
		"credentials_json": "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*PostgresProvider)
	if pp.timeout != 5*time.Minute {
		t.Errorf("expected 5m default timeout, got %v", pp.timeout)
	}
}

// --- Provider method tests ---

func TestSQLDialect(t *testing.T) {
	p := &PostgresProvider{}
	dialect := p.SQLDialect()
	if !strings.Contains(dialect, "PostgreSQL") {
		t.Errorf("dialect should mention PostgreSQL, got %q", dialect)
	}
	for _, keyword := range []string{"DISTINCT ON", "CTEs", "window functions", "JSON operators", "array types"} {
		if !strings.Contains(dialect, keyword) {
			t.Errorf("dialect should mention %s, got %q", keyword, dialect)
		}
	}
}

func TestSQLFixPrompt(t *testing.T) {
	p := &PostgresProvider{}
	prompt := p.SQLFixPrompt()
	if prompt == "" {
		t.Error("expected non-empty SQL fix prompt")
	}
	for _, required := range []string{
		"{{DATASET}}", "{{ORIGINAL_SQL}}", "{{ERROR_MESSAGE}}", "{{SCHEMA_INFO}}",
		"DISTINCT ON", "ILIKE", "generate_series", "jsonb",
		"date_trunc", "INTERVAL", "COALESCE",
		"LATERAL", "FILTER", "RETURNING", "NOT EXISTS", "NULLIF",
		"HAVING", "Recursive", "ON CONFLICT", "string_agg", "array_agg", "unnest",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("SQL fix prompt should contain %q", required)
		}
	}
}

func TestGetDataset(t *testing.T) {
	p := &PostgresProvider{schema: "my_schema"}
	if p.GetDataset() != "my_schema" {
		t.Errorf("expected 'my_schema', got %q", p.GetDataset())
	}
}

func TestGetDatasetEmpty(t *testing.T) {
	p := &PostgresProvider{schema: ""}
	if p.GetDataset() != "" {
		t.Errorf("expected empty string, got %q", p.GetDataset())
	}
}

// --- Mock-based method tests ---

func TestHealthCheck(t *testing.T) {
	mock := &mockPGClient{pingErr: nil}
	p := &PostgresProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	mock := &mockPGClient{pingErr: fmt.Errorf("connection refused")}
	p := &PostgresProvider{client: mock}

	err := p.HealthCheck(context.Background())
	if err == nil {
		t.Error("expected error")
	}
}

func TestClose(t *testing.T) {
	mock := &mockPGClient{closeErr: nil}
	p := &PostgresProvider{client: mock}

	if err := p.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCloseError(t *testing.T) {
	mock := &mockPGClient{closeErr: fmt.Errorf("close failed")}
	p := &PostgresProvider{client: mock}

	err := p.Close()
	if err == nil {
		t.Error("expected error")
	}
}

func TestValidateReadOnly(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("mock: not supported")
		},
	}
	p := &PostgresProvider{client: mock}

	err := p.ValidateReadOnly(context.Background())
	if err == nil {
		t.Error("expected error when query fails")
	}
	if !strings.Contains(err.Error(), "read-only validation failed") {
		t.Errorf("error should mention 'read-only validation failed', got: %v", err)
	}
}

func TestQueryError(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	p := &PostgresProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "query failed") {
		t.Errorf("error should mention 'query failed', got: %v", err)
	}
}

func TestQueryContextCancelled(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, context.Canceled
		},
	}
	p := &PostgresProvider{client: mock, timeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Query(ctx, "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestQueryErrorWrapping(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("syntax error at position 42")
		},
	}
	p := &PostgresProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT BAD", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "postgres:") {
		t.Errorf("error should be wrapped with 'postgres:', got: %v", err)
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("error should contain original message, got: %v", err)
	}
}

func TestListTablesError(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("access denied")
		},
	}
	p := &PostgresProvider{client: mock, schema: "public"}

	_, err := p.ListTables(context.Background())
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "list tables failed") {
		t.Errorf("error should mention 'list tables failed', got: %v", err)
	}
}

func TestListTablesUsesDefaultSchema(t *testing.T) {
	var capturedArgs []interface{}
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			capturedArgs = args
			return nil, fmt.Errorf("expected")
		},
	}
	p := &PostgresProvider{client: mock, schema: "custom_schema"}

	_, _ = p.ListTables(context.Background())
	if len(capturedArgs) == 0 {
		t.Fatal("expected query args")
	}
	if capturedArgs[0] != "custom_schema" {
		t.Errorf("expected schema 'custom_schema', got %v", capturedArgs[0])
	}
}

func TestListTablesInDatasetEmpty(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("schema not found")
		},
	}
	p := &PostgresProvider{client: mock, schema: "nonexistent"}

	_, err := p.ListTablesInDataset(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent schema")
	}
}

func TestGetTableSchemaError(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &PostgresProvider{client: mock, schema: "public"}

	_, err := p.GetTableSchema(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "get table schema failed") {
		t.Errorf("error should mention 'get table schema failed', got: %v", err)
	}
}

func TestGetTableSchemaInDatasetError(t *testing.T) {
	mock := &mockPGClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &PostgresProvider{client: mock, schema: "public"}

	_, err := p.GetTableSchemaInDataset(context.Background(), "public", "missing")
	if err == nil {
		t.Error("expected error")
	}
}

// --- Type normalization tests ---

func TestNormalizePostgresType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Integer types
		{"integer", "INT64"},
		{"int", "INT64"},
		{"int4", "INT64"},
		{"bigint", "INT64"},
		{"int8", "INT64"},
		{"smallint", "INT64"},
		{"int2", "INT64"},
		{"serial", "INT64"},
		{"bigserial", "INT64"},
		{"smallserial", "INT64"},

		// Float types
		{"real", "FLOAT64"},
		{"float4", "FLOAT64"},
		{"double precision", "FLOAT64"},
		{"float8", "FLOAT64"},
		{"numeric", "FLOAT64"},
		{"numeric(10,2)", "FLOAT64"},
		{"decimal", "FLOAT64"},
		{"decimal(18,4)", "FLOAT64"},

		// Boolean
		{"boolean", "BOOL"},
		{"bool", "BOOL"},

		// Date/time
		{"date", "DATE"},
		{"timestamp", "TIMESTAMP"},
		{"timestamp without time zone", "TIMESTAMP"},
		{"timestamp with time zone", "TIMESTAMP"},
		{"timestamptz", "TIMESTAMP"},

		// Binary
		{"bytea", "BYTES"},

		// JSON
		{"json", "RECORD"},
		{"jsonb", "RECORD"},

		// String types
		{"varchar", "STRING"},
		{"character varying", "STRING"},
		{"char", "STRING"},
		{"text", "STRING"},
		{"uuid", "STRING"},
		{"inet", "STRING"},
		{"cidr", "STRING"},
		{"macaddr", "STRING"},
		{"money", "STRING"},
		{"interval", "STRING"},
		{"time", "STRING"},
		{"point", "STRING"},

		// Case insensitivity
		{"INTEGER", "INT64"},
		{"BIGINT", "INT64"},
		{"BOOLEAN", "BOOL"},
		{"JSONB", "RECORD"},

		// Array types (default to STRING)
		{"ARRAY", "STRING"},
		{"integer[]", "STRING"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizePostgresType(tt.input)
			if result != tt.expected {
				t.Errorf("normalizePostgresType(%q) = %q, want %q", tt.input, result, tt.expected)
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

	// []byte → string (non-numeric type)
	if v := normalizeValue([]byte("hello"), "TEXT"); v != "hello" {
		t.Errorf("expected 'hello', got %v", v)
	}

	// []byte NUMERIC → float64
	if v := normalizeValue([]byte("123.45"), "NUMERIC"); v != float64(123.45) {
		t.Errorf("expected float64(123.45), got %v (%T)", v, v)
	}

	// []byte NUMERIC integer → float64 (always float64 for consistency)
	if v := normalizeValue([]byte("42"), "NUMERIC"); v != float64(42) {
		t.Errorf("expected float64(42), got %v (%T)", v, v)
	}

	// []byte DECIMAL → float64
	if v := normalizeValue([]byte("99.99"), "DECIMAL"); v != float64(99.99) {
		t.Errorf("expected float64(99.99), got %v (%T)", v, v)
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

	// float64 passthrough
	if v := normalizeValue(float64(3.14), ""); v != float64(3.14) {
		t.Errorf("expected 3.14, got %v", v)
	}

	// bool passthrough
	if v := normalizeValue(true, "BOOLEAN"); v != true {
		t.Errorf("expected true, got %v", v)
	}

	// string passthrough
	if v := normalizeValue("test", "VARCHAR"); v != "test" {
		t.Errorf("expected 'test', got %v", v)
	}
}

func TestConvertStringByType(t *testing.T) {
	tests := []struct {
		name     string
		val      string
		dbType   string
		expected interface{}
	}{
		{"NUMERIC integer", "42", "NUMERIC", float64(42)},
		{"NUMERIC decimal", "3.14", "NUMERIC", float64(3.14)},
		{"NUMERIC negative", "-100", "NUMERIC", float64(-100)},
		{"NUMERIC large", "9999999999999", "NUMERIC", float64(9999999999999)},
		{"DECIMAL decimal", "1.5", "DECIMAL", float64(1.5)},
		{"DECIMAL integer", "7", "DECIMAL", float64(7)},
		{"unparseable NUMERIC", "abc", "NUMERIC", "abc"},
		{"VARCHAR passthrough", "hello", "VARCHAR", "hello"},
		{"TEXT passthrough", "world", "TEXT", "world"},
		{"empty type passthrough", "test", "", "test"},
		{"case insensitive", "42", "numeric", float64(42)},
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

// --- Identifier validation tests ---

func TestValidIdentifier(t *testing.T) {
	valid := []string{"public", "my_schema", "Schema123", "_private", "a$b"}
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

func TestQuoteDSNValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"it's", `'it\'s'`},
		{`back\slash`, `'back\\slash'`},
		{`both 'quotes' and \slashes`, `'both \'quotes\' and \\slashes'`},
		{"", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := quoteDSNValue(tt.input)
			if result != tt.expected {
				t.Errorf("quoteDSNValue(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
