package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// --- Registration tests ---

func TestRegistered(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("mssql")
	if !ok {
		t.Fatal("mssql provider not registered")
	}
	if meta.Name != "Microsoft SQL Server" {
		t.Errorf("expected name 'Microsoft SQL Server', got %q", meta.Name)
	}
	if meta.ID != "mssql" {
		t.Errorf("expected ID 'mssql', got %q", meta.ID)
	}
	if len(meta.ConfigFields) == 0 {
		t.Error("expected config fields to be populated")
	}
}

func TestRegisteredConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("mssql")

	required := map[string]bool{}
	defaults := map[string]string{}
	for _, f := range meta.ConfigFields {
		required[f.Key] = f.Required
		defaults[f.Key] = f.Default
	}

	for _, key := range []string{"host", "database", "user", "dataset"} {
		if !required[key] {
			t.Errorf("config field %q should be required", key)
		}
	}
	for _, key := range []string{"port", "encrypt", "trust_server_certificate"} {
		if required[key] {
			t.Errorf("config field %q should not be required", key)
		}
	}
	if defaults["port"] != defaultPort {
		t.Errorf("port default should be %q, got %q", defaultPort, defaults["port"])
	}
	if defaults["dataset"] != defaultSchema {
		t.Errorf("dataset default should be %q, got %q", defaultSchema, defaults["dataset"])
	}
	if defaults["encrypt"] != "true" {
		t.Errorf("encrypt default should be 'true', got %q", defaults["encrypt"])
	}
}

func TestRegisteredAuthMethods(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("mssql")
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
	meta, _ := gowarehouse.GetProviderMeta("mssql")

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
	}
	if len(pwMethod.Fields) != 1 || pwMethod.Fields[0].Type != "credential" {
		t.Error("password method should have 1 credential field")
	}
	if csMethod == nil {
		t.Fatal("missing connection_string auth method")
	}
	if len(csMethod.Fields) != 1 || csMethod.Fields[0].Type != "credential" {
		t.Error("connection_string method should have 1 credential field")
	}
}

func TestDefaultPricing(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("mssql")
	if meta.DefaultPricing == nil {
		t.Fatal("expected default pricing")
	}
	if meta.DefaultPricing.CostModel != "per_hour" {
		t.Errorf("expected cost model 'per_hour', got %q", meta.DefaultPricing.CostModel)
	}
}

// --- Factory validation tests (negative paths) ---

func TestFactoryMissingHost(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing host")
	}
	if !strings.Contains(err.Error(), "host is required") {
		t.Errorf("error should mention 'host is required', got: %v", err)
	}
}

func TestFactoryMissingDatabase(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"user":             "testuser",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing database")
	}
	if !strings.Contains(err.Error(), "database is required") {
		t.Errorf("error should mention 'database is required', got: %v", err)
	}
}

func TestFactoryMissingUser(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for missing user")
	}
	if !strings.Contains(err.Error(), "user is required") {
		t.Errorf("error should mention 'user is required', got: %v", err)
	}
}

func TestFactoryMissingPassword(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
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

func TestFactoryInvalidSchemaName(t *testing.T) {
	for _, bad := range []string{"schema; DROP TABLE", "dbo--x", "schema'1"} {
		t.Run(bad, func(t *testing.T) {
			_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
				"host":             "localhost",
				"database":         "testdb",
				"user":             "testuser",
				"dataset":          bad,
				"credentials_json": "pw",
			})
			if err == nil {
				t.Fatal("expected error for invalid schema name")
			}
			if !strings.Contains(err.Error(), "invalid schema name") {
				t.Errorf("error should mention 'invalid schema name', got: %v", err)
			}
		})
	}
}

func TestFactoryUnsupportedAuthMethod(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"auth_method":      "kerberos",
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
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
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

func TestFactoryValidEncryptValues(t *testing.T) {
	for _, mode := range []string{"true", "false", "strict", "disable"} {
		t.Run(mode, func(t *testing.T) {
			p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
				"host":             "localhost",
				"database":         "testdb",
				"user":             "testuser",
				"encrypt":          mode,
				"credentials_json": "pw",
			})
			if err != nil {
				t.Errorf("encrypt %q should be valid: %v", mode, err)
				return
			}
			p.Close()
		})
	}
}

func TestFactoryEncryptCaseInsensitive(t *testing.T) {
	// Users commonly paste "TRUE" / "FALSE" / "Strict" from docs and GUIs.
	// Accept any case — go-mssqldb itself expects lowercase.
	for _, mode := range []string{"TRUE", "FALSE", "Strict", "DISABLE"} {
		t.Run(mode, func(t *testing.T) {
			p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
				"host":             "localhost",
				"database":         "testdb",
				"user":             "testuser",
				"encrypt":          mode,
				"credentials_json": "pw",
			})
			if err != nil {
				t.Errorf("encrypt %q (mixed case) should be accepted: %v", mode, err)
				return
			}
			p.Close()
		})
	}
}

func TestFactoryTrustCertificateCaseInsensitive(t *testing.T) {
	for _, v := range []string{"TRUE", "False"} {
		t.Run(v, func(t *testing.T) {
			p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
				"host":                     "localhost",
				"database":                 "testdb",
				"user":                     "testuser",
				"trust_server_certificate": v,
				"credentials_json":         "pw",
			})
			if err != nil {
				t.Errorf("trust_server_certificate %q (mixed case) should be accepted: %v", v, err)
				return
			}
			p.Close()
		})
	}
}

func TestFactoryInvalidEncrypt(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"encrypt":          "maybe",
		"credentials_json": "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid encrypt")
	}
	if !strings.Contains(err.Error(), "invalid encrypt") {
		t.Errorf("error should mention 'invalid encrypt', got: %v", err)
	}
}

func TestFactoryInvalidTrustServerCertificate(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":                     "localhost",
		"database":                 "testdb",
		"user":                     "testuser",
		"trust_server_certificate": "sometimes",
		"credentials_json":         "pw",
	})
	if err == nil {
		t.Fatal("expected error for invalid trust_server_certificate")
	}
	if !strings.Contains(err.Error(), "invalid trust_server_certificate") {
		t.Errorf("error should mention 'invalid trust_server_certificate', got: %v", err)
	}
}

func TestFactoryConnectionStringMissing(t *testing.T) {
	_, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"auth_method": "connection_string",
	})
	if err == nil {
		t.Fatal("expected error for missing connection string")
	}
	if !strings.Contains(err.Error(), "connection string is required") {
		t.Errorf("error should mention 'connection string is required', got: %v", err)
	}
}

// --- Factory validation tests (positive paths) ---

func TestFactoryDatabaseWithHyphens(t *testing.T) {
	// Azure SQL and managed SQL Server installs often use hyphenated DB names.
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
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

func TestFactoryPasswordAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "testpass",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*MSSQLProvider)
	if pp.schema != defaultSchema {
		t.Errorf("expected default schema %q, got %q", defaultSchema, pp.schema)
	}
}

func TestFactoryCustomSchema(t *testing.T) {
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"dataset":          "analytics",
		"credentials_json": "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*MSSQLProvider)
	if pp.schema != "analytics" {
		t.Errorf("expected schema 'analytics', got %q", pp.schema)
	}
}

func TestFactoryConnectionStringAuth(t *testing.T) {
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": "sqlserver://user:pass@localhost:1433?database=testdb&encrypt=false",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()
}

func TestFactoryTimeoutConfig(t *testing.T) {
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "pw",
		"timeout_minutes":  "10",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*MSSQLProvider)
	if pp.timeout != 10*time.Minute {
		t.Errorf("expected 10m timeout, got %v", pp.timeout)
	}
}

func TestFactoryDefaultTimeout(t *testing.T) {
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"host":             "localhost",
		"database":         "testdb",
		"user":             "testuser",
		"credentials_json": "pw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer p.Close()

	pp := p.(*MSSQLProvider)
	if pp.timeout != time.Duration(defaultTimeout)*time.Minute {
		t.Errorf("expected %dm default timeout, got %v", defaultTimeout, pp.timeout)
	}
}

func TestFactoryPasswordWithSpecialChars(t *testing.T) {
	// Passwords can contain @, /, :, ?, &, #, space — all of which have
	// special meaning in URL DSNs. buildSQLServerURL must URL-encode them.
	for _, pw := range []string{
		"p@ss:word",
		"pass w/ spaces",
		"pass?word&extra",
		"pass#frag",
		"pass/slash",
	} {
		t.Run(pw, func(t *testing.T) {
			p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
				"host":             "localhost",
				"database":         "testdb",
				"user":             "testuser",
				"credentials_json": pw,
			})
			if err != nil {
				t.Fatalf("unexpected error for password %q: %v", pw, err)
			}
			p.Close()
		})
	}
}

// --- Provider method tests ---

func TestSQLDialect(t *testing.T) {
	p := &MSSQLProvider{}
	dialect := p.SQLDialect()
	for _, keyword := range []string{"Microsoft SQL Server", "T-SQL", "TOP", "OFFSET", "FETCH", "CROSS APPLY", "PIVOT"} {
		if !strings.Contains(dialect, keyword) {
			t.Errorf("dialect should mention %q, got %q", keyword, dialect)
		}
	}
}

func TestSQLFixPrompt(t *testing.T) {
	p := &MSSQLProvider{}
	prompt := p.SQLFixPrompt()
	if prompt == "" {
		t.Fatal("expected non-empty SQL fix prompt")
	}
	for _, required := range []string{
		// Template variables the caller expects
		"{{DATASET}}", "{{ORIGINAL_SQL}}", "{{ERROR_MESSAGE}}", "{{SCHEMA_INFO}}", "{{FILTER}}", "{{CONVERSATION_HISTORY}}",
		// T-SQL-specific constructs
		"TOP", "OFFSET", "FETCH NEXT", "CROSS APPLY", "OUTER APPLY", "PIVOT",
		"SYSUTCDATETIME", "TRY_CAST", "TRY_CONVERT", "NULLIF", "COALESCE", "ISNULL",
		"NOT EXISTS", "IS NULL", "STRING_AGG", "STRING_SPLIT",
		"JSON_VALUE", "JSON_QUERY", "OPENJSON",
		// Error-message fragments that show we mention known Msg numbers
		"Msg 208", "Msg 8120", "Msg 147", "Msg 209", "Msg 245", "Msg 4108", "Msg 8134",
		// Reserved-keyword bracketing guidance
		"[user]", "[order]",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("SQL fix prompt should contain %q", required)
		}
	}
}

func TestGetDataset(t *testing.T) {
	p := &MSSQLProvider{schema: "my_schema"}
	if p.GetDataset() != "my_schema" {
		t.Errorf("expected 'my_schema', got %q", p.GetDataset())
	}
}

func TestGetDatasetEmpty(t *testing.T) {
	p := &MSSQLProvider{schema: ""}
	if p.GetDataset() != "" {
		t.Errorf("expected empty string, got %q", p.GetDataset())
	}
}

// --- Mock-based method tests ---

func TestHealthCheck(t *testing.T) {
	mock := &mockMSClient{pingErr: nil}
	p := &MSSQLProvider{client: mock}

	if err := p.HealthCheck(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	mock := &mockMSClient{pingErr: fmt.Errorf("connection refused")}
	p := &MSSQLProvider{client: mock}

	if err := p.HealthCheck(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestClose(t *testing.T) {
	mock := &mockMSClient{closeErr: nil}
	p := &MSSQLProvider{client: mock}

	if err := p.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCloseError(t *testing.T) {
	mock := &mockMSClient{closeErr: fmt.Errorf("close failed")}
	p := &MSSQLProvider{client: mock}

	if err := p.Close(); err == nil {
		t.Error("expected error")
	}
}

func TestValidateReadOnly(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("mock: not supported")
		},
	}
	p := &MSSQLProvider{client: mock}

	err := p.ValidateReadOnly(context.Background())
	if err == nil {
		t.Error("expected error when query fails")
	}
	if !strings.Contains(err.Error(), "read-only validation failed") {
		t.Errorf("error should mention 'read-only validation failed', got: %v", err)
	}
}

func TestValidateReadOnlySendsSelectOne(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("short-circuit")
		},
	}
	p := &MSSQLProvider{client: mock}
	_ = p.ValidateReadOnly(context.Background())

	if !strings.EqualFold(strings.TrimSpace(mock.lastQuery), "SELECT 1") {
		t.Errorf("expected SELECT 1, got %q", mock.lastQuery)
	}
}

func TestQueryError(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	p := &MSSQLProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "query failed") {
		t.Errorf("error should mention 'query failed', got: %v", err)
	}
}

func TestQueryErrorWrapping(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("Incorrect syntax near 'FROM'")
		},
	}
	p := &MSSQLProvider{client: mock, timeout: 5 * time.Second}

	_, err := p.Query(context.Background(), "SELECT BAD", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "mssql:") {
		t.Errorf("error should be wrapped with 'mssql:', got: %v", err)
	}
	if !strings.Contains(err.Error(), "Incorrect syntax") {
		t.Errorf("error should contain original message, got: %v", err)
	}
}

func TestQueryContextCancelled(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, context.Canceled
		},
	}
	p := &MSSQLProvider{client: mock, timeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Query(ctx, "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestListTablesError(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("access denied")
		},
	}
	p := &MSSQLProvider{client: mock, schema: defaultSchema}

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
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			capturedArgs = args
			return nil, fmt.Errorf("expected")
		},
	}
	p := &MSSQLProvider{client: mock, schema: "custom_schema"}

	_, _ = p.ListTables(context.Background())
	if len(capturedArgs) == 0 {
		t.Fatal("expected query args")
	}
	if capturedArgs[0] != "custom_schema" {
		t.Errorf("expected schema 'custom_schema', got %v", capturedArgs[0])
	}
}

func TestListTablesUsesNamedPlaceholders(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("short-circuit")
		},
	}
	p := &MSSQLProvider{client: mock, schema: defaultSchema}
	_, _ = p.ListTables(context.Background())

	// SQL Server requires @p1 / @Name parameters with the "sqlserver" driver.
	if !strings.Contains(mock.lastQuery, "@p1") {
		t.Errorf("ListTables query should use @p1 parameter, got: %s", mock.lastQuery)
	}
	if strings.Contains(mock.lastQuery, "$1") || strings.Contains(mock.lastQuery, "?") {
		t.Errorf("ListTables query should not use ? or $1 placeholders, got: %s", mock.lastQuery)
	}
}

func TestListTablesFiltersToBaseTables(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("short-circuit")
		},
	}
	p := &MSSQLProvider{client: mock, schema: defaultSchema}
	_, _ = p.ListTables(context.Background())

	if !strings.Contains(mock.lastQuery, "BASE TABLE") {
		t.Errorf("list tables query should filter to BASE TABLE, got: %s", mock.lastQuery)
	}
}

func TestListTablesInDatasetEmpty(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("schema not found")
		},
	}
	p := &MSSQLProvider{client: mock, schema: "nonexistent"}

	_, err := p.ListTablesInDataset(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent schema")
	}
}

func TestGetTableSchemaError(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &MSSQLProvider{client: mock, schema: defaultSchema}

	_, err := p.GetTableSchema(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "get table schema failed") {
		t.Errorf("error should mention 'get table schema failed', got: %v", err)
	}
}

func TestGetTableSchemaInDatasetError(t *testing.T) {
	mock := &mockMSClient{
		queryFunc: func(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
			return nil, fmt.Errorf("table not found")
		},
	}
	p := &MSSQLProvider{client: mock, schema: defaultSchema}

	_, err := p.GetTableSchemaInDataset(context.Background(), defaultSchema, "missing")
	if err == nil {
		t.Error("expected error")
	}
}

// --- Type normalization tests ---

func TestNormalizeMSSQLType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Integer family
		{"tinyint", "INT64"},
		{"smallint", "INT64"},
		{"int", "INT64"},
		{"integer", "INT64"},
		{"bigint", "INT64"},

		// Float / numeric family → FLOAT64
		{"real", "FLOAT64"},
		{"float", "FLOAT64"},
		{"decimal", "FLOAT64"},
		{"numeric", "FLOAT64"},
		{"money", "FLOAT64"},
		{"smallmoney", "FLOAT64"},

		// Bit
		{"bit", "BOOL"},

		// Date / time
		{"date", "DATE"},
		{"datetime", "TIMESTAMP"},
		{"datetime2", "TIMESTAMP"},
		{"smalldatetime", "TIMESTAMP"},
		{"datetimeoffset", "TIMESTAMP"},
		// TIME is stored separately from date — best represented as STRING.
		{"time", "STRING"},

		// Binary
		{"binary", "BYTES"},
		{"varbinary", "BYTES"},
		{"image", "BYTES"},

		// Opaque/structured → STRING
		{"xml", "STRING"},
		{"sql_variant", "STRING"},

		// String family
		{"char", "STRING"},
		{"nchar", "STRING"},
		{"varchar", "STRING"},
		{"nvarchar", "STRING"},
		{"text", "STRING"},
		{"ntext", "STRING"},
		{"uniqueidentifier", "STRING"},
		{"rowversion", "STRING"},
		{"timestamp", "STRING"}, // SQL Server TIMESTAMP (a.k.a. rowversion) is an opaque binary — kept as STRING
		{"hierarchyid", "STRING"},
		{"geography", "STRING"},
		{"geometry", "STRING"},

		// Case insensitivity + whitespace
		{"INT", "INT64"},
		{"BIGINT", "INT64"},
		{"  datetime2 ", "TIMESTAMP"},
		{"DECIMAL", "FLOAT64"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMSSQLType(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeMSSQLType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// --- Value normalization tests ---

func TestNormalizeValueNil(t *testing.T) {
	if v := normalizeValue(nil, ""); v != nil {
		t.Errorf("expected nil, got %v", v)
	}
	if v := normalizeValue(nil, "INT"); v != nil {
		t.Errorf("expected nil even with typed column, got %v", v)
	}
}

func TestNormalizeValueIntegerPromotion(t *testing.T) {
	// Various signed integer widths should all promote to int64.
	if v := normalizeValue(int8(42), "TINYINT"); v != int64(42) {
		t.Errorf("int8 should promote to int64, got %v (%T)", v, v)
	}
	if v := normalizeValue(int16(-1000), "SMALLINT"); v != int64(-1000) {
		t.Errorf("int16 should promote to int64, got %v (%T)", v, v)
	}
	if v := normalizeValue(int32(100000), "INT"); v != int64(100000) {
		t.Errorf("int32 should promote to int64, got %v (%T)", v, v)
	}
	if v := normalizeValue(int64(9999999999), "BIGINT"); v != int64(9999999999) {
		t.Errorf("int64 should pass through, got %v (%T)", v, v)
	}
}

func TestNormalizeValueFloatPromotion(t *testing.T) {
	if v := normalizeValue(float32(3.14), "REAL"); v == nil {
		t.Fatal("float32 should be promoted, got nil")
	} else if f, ok := v.(float64); !ok {
		t.Errorf("float32 should promote to float64, got %T", v)
	} else if math.Abs(f-3.14) > 0.0001 {
		t.Errorf("expected ~3.14, got %v", f)
	}
	if v := normalizeValue(float64(2.718), "FLOAT"); v != float64(2.718) {
		t.Errorf("float64 should pass through, got %v (%T)", v, v)
	}
}

func TestNormalizeValueDecimalToFloat(t *testing.T) {
	// DECIMAL/NUMERIC come back as []byte string representation.
	if v := normalizeValue([]byte("123.45"), "DECIMAL"); v != float64(123.45) {
		t.Errorf("expected 123.45 (float64), got %v (%T)", v, v)
	}
	if v := normalizeValue([]byte("42"), "NUMERIC"); v != float64(42) {
		t.Errorf("expected 42 (float64), got %v (%T)", v, v)
	}
	if v := normalizeValue([]byte("-99.9999"), "MONEY"); v != float64(-99.9999) {
		t.Errorf("expected -99.9999 (float64), got %v (%T)", v, v)
	}
	if v := normalizeValue([]byte("1.50"), "SMALLMONEY"); v != float64(1.50) {
		t.Errorf("expected 1.50 (float64), got %v (%T)", v, v)
	}
}

func TestNormalizeValueDecimalUnparseableFallsBackToString(t *testing.T) {
	if v := normalizeValue([]byte("not-a-number"), "DECIMAL"); v != "not-a-number" {
		t.Errorf("expected fallback to string, got %v (%T)", v, v)
	}
}

func TestNormalizeValueUniqueIdentifier(t *testing.T) {
	// SQL Server returns GUIDs as 16 raw bytes with a mixed-endian layout.
	// The canonical form is 8-4-4-4-12 hex.
	guid := []byte{
		0x99, 0xBC, 0xEE, 0xA0,
		0x0B, 0x9C,
		0xF8, 0x4E,
		0xBB, 0x6D,
		0x6B, 0xB9, 0xBD, 0x38, 0x0A, 0x11,
	}
	want := "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"
	if v := normalizeValue(guid, "UNIQUEIDENTIFIER"); v != want {
		t.Errorf("expected %q, got %v (%T)", want, v, v)
	}
}

func TestNormalizeValueUniqueIdentifierWrongLength(t *testing.T) {
	// Defensive: if length isn't 16, don't panic — fall back to string.
	short := []byte{0x01, 0x02, 0x03}
	if v := normalizeValue(short, "UNIQUEIDENTIFIER"); v != string(short) {
		t.Errorf("expected raw-string fallback for short GUID, got %v (%T)", v, v)
	}
}

func TestNormalizeValueBinaryToString(t *testing.T) {
	// VARBINARY / BINARY / IMAGE are returned as []byte; normalize to string
	// so the map value is consistent with every other STRING-family column.
	if v := normalizeValue([]byte{0xDE, 0xAD, 0xBE, 0xEF}, "VARBINARY"); v != "\xDE\xAD\xBE\xEF" {
		t.Errorf("expected VARBINARY as string, got %v (%T)", v, v)
	}
}

func TestNormalizeValueTextTypes(t *testing.T) {
	// Text-like columns arrive as []byte; normalize to string.
	if v := normalizeValue([]byte("hello"), "NVARCHAR"); v != "hello" {
		t.Errorf("expected 'hello', got %v (%T)", v, v)
	}
	if v := normalizeValue([]byte("<xml/>"), "XML"); v != "<xml/>" {
		t.Errorf("expected '<xml/>', got %v (%T)", v, v)
	}
	if v := normalizeValue([]byte("padded    "), "CHAR"); v != "padded    " {
		t.Errorf("expected CHAR value preserved, got %v (%T)", v, v)
	}
}

func TestNormalizeValueTime(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	if v := normalizeValue(ts, "DATETIME"); v != "2026-01-15T10:30:00Z" {
		t.Errorf("expected RFC3339, got %v", v)
	}
}

func TestNormalizeValueBoolPassthrough(t *testing.T) {
	if v := normalizeValue(true, "BIT"); v != true {
		t.Errorf("expected true, got %v (%T)", v, v)
	}
	if v := normalizeValue(false, "BIT"); v != false {
		t.Errorf("expected false, got %v (%T)", v, v)
	}
}

func TestNormalizeValueStringPassthrough(t *testing.T) {
	// go-mssqldb can return text-like values as string directly. Pass
	// through unchanged — don't accidentally route strings through
	// convertBytesByType and mangle them.
	if v := normalizeValue("hello world", "NVARCHAR"); v != "hello world" {
		t.Errorf("expected string passthrough, got %v (%T)", v, v)
	}
}

func TestConvertBytesByType(t *testing.T) {
	tests := []struct {
		name     string
		val      []byte
		dbType   string
		expected interface{}
	}{
		{"DECIMAL integer", []byte("42"), "DECIMAL", float64(42)},
		{"DECIMAL decimal", []byte("3.14"), "DECIMAL", float64(3.14)},
		{"DECIMAL negative", []byte("-100.5"), "DECIMAL", float64(-100.5)},
		{"NUMERIC large", []byte("9999999999999"), "NUMERIC", float64(9999999999999)},
		{"MONEY simple", []byte("1.50"), "MONEY", float64(1.5)},
		{"SMALLMONEY simple", []byte("0.25"), "SMALLMONEY", float64(0.25)},
		{"NVARCHAR passthrough", []byte("hello"), "NVARCHAR", "hello"},
		{"TEXT passthrough", []byte("world"), "TEXT", "world"},
		{"empty type passthrough", []byte("test"), "", "test"},
		{"case insensitive DECIMAL", []byte("42"), "decimal", float64(42)},
		{"case mixed MONEY", []byte("1.5"), "Money", float64(1.5)},
		{"VARBINARY to string", []byte("bytes"), "VARBINARY", "bytes"},
		{"XML passthrough", []byte("<a>1</a>"), "XML", "<a>1</a>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertBytesByType(tt.val, tt.dbType)
			if got != tt.expected {
				t.Errorf("convertBytesByType(%q, %q) = %v (%T), want %v (%T)",
					tt.val, tt.dbType, got, got, tt.expected, tt.expected)
			}
		})
	}
}

func TestFormatGUID(t *testing.T) {
	// Byte order: first three groups little-endian, last two big-endian.
	// The 16-byte input A0EEBC999C0B4EF8BB6D6BB9BD380A11 is stored by SQL
	// Server as: 99 BC EE A0 0B 9C F8 4E BB 6D 6B B9 BD 38 0A 11.
	in := []byte{0x99, 0xBC, 0xEE, 0xA0, 0x0B, 0x9C, 0xF8, 0x4E,
		0xBB, 0x6D, 0x6B, 0xB9, 0xBD, 0x38, 0x0A, 0x11}
	want := "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"
	if got := formatGUID(in); got != want {
		t.Errorf("formatGUID() = %q, want %q", got, want)
	}

	// Zero GUID.
	zero := make([]byte, 16)
	if got := formatGUID(zero); got != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("zero GUID = %q", got)
	}

	// Wrong length → empty.
	if got := formatGUID([]byte{0x01}); got != "" {
		t.Errorf("wrong-length input should give empty, got %q", got)
	}
	if got := formatGUID(nil); got != "" {
		t.Errorf("nil input should give empty, got %q", got)
	}
}

// --- Identifier validation tests ---

func TestValidIdentifier(t *testing.T) {
	valid := []string{"dbo", "my_schema", "Schema123", "_private", "a$b", "ANALYTICS"}
	for _, id := range valid {
		if !validIdentifier(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}

	invalid := []string{"", "123abc", "my schema", "schema;drop", "a-b", "a.b", "dbo'1", "dbo--comment"}
	for _, id := range invalid {
		if validIdentifier(id) {
			t.Errorf("expected %q to be invalid", id)
		}
	}
}

// --- DSN builder tests ---

func TestBuildSQLServerURLBasic(t *testing.T) {
	dsn := buildSQLServerURL("localhost", "1433", "sa", "Passw0rd!", "testdb", "true", "false")
	if !strings.HasPrefix(dsn, "sqlserver://sa:") {
		t.Errorf("DSN should start with sqlserver://sa:, got %s", dsn)
	}
	if !strings.Contains(dsn, "@localhost:1433") {
		t.Errorf("DSN should contain @localhost:1433, got %s", dsn)
	}
	if !strings.Contains(dsn, "database=testdb") {
		t.Errorf("DSN should contain database=testdb, got %s", dsn)
	}
	if !strings.Contains(dsn, "encrypt=true") {
		t.Errorf("DSN should contain encrypt=true, got %s", dsn)
	}
	if !strings.Contains(dsn, "TrustServerCertificate=false") {
		t.Errorf("DSN should contain TrustServerCertificate=false, got %s", dsn)
	}
}

func TestBuildSQLServerURLEscapesSpecialCharactersInPassword(t *testing.T) {
	// Password contains every URL-reserved character. buildSQLServerURL must
	// encode them or the DSN will be parsed incorrectly and credentials will
	// leak into unexpected places.
	dsn := buildSQLServerURL("host", "1433", "u:s@er", "p@ss:word&stuff?more/slash#frag", "db", "true", "false")

	// "@" in the password must be URL-encoded so the first "@" in the DSN is
	// the host delimiter, not part of the password.
	atCount := strings.Count(dsn, "@")
	if atCount != 1 {
		t.Errorf("expected exactly 1 '@' (host delimiter), got %d: %s", atCount, dsn)
	}

	// The query string encode path should not contain ' ' or raw '&' within
	// our values (both user info and query string are encoded).
	if strings.Contains(dsn, " ") {
		t.Errorf("DSN should have no spaces, got %s", dsn)
	}

	// The database name must come through cleanly.
	if !strings.Contains(dsn, "database=db") {
		t.Errorf("DSN should contain database=db, got %s", dsn)
	}
}

func TestBuildSQLServerURLIPv6Host(t *testing.T) {
	// net.JoinHostPort must bracket IPv6 literals — otherwise the DSN
	// parser sees ":1433" as part of the address and breaks.
	dsn := buildSQLServerURL("::1", "1433", "sa", "pw", "db", "true", "false")
	if !strings.Contains(dsn, "@[::1]:1433") {
		t.Errorf("DSN should bracket IPv6 literal, got %s", dsn)
	}
}

func TestBuildSQLServerURLEscapesSpecialCharactersInUser(t *testing.T) {
	// Usernames (e.g. Azure AD UPNs) can contain '@'. Must be URL-encoded.
	dsn := buildSQLServerURL("host", "1433", "alice@example.com", "pw", "db", "true", "false")

	// The user-info segment must encode the '@' in the email so the
	// delimiter '@' before "host" is still parseable.
	atCount := strings.Count(dsn, "@")
	if atCount != 1 {
		t.Errorf("expected exactly 1 '@' delimiter, got %d: %s", atCount, dsn)
	}
	if !strings.Contains(dsn, "alice%40example.com") {
		t.Errorf("DSN should URL-encode user '@', got %s", dsn)
	}
}
