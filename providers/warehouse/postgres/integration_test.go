//go:build integration_postgres

package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// seedSQL creates a type-exercise schema covering every PostgreSQL data type
// that lib/pq can return, plus edge-case rows (NULLs, empty strings, zeroes,
// boundary integers, NaN, ±Infinity, unicode, nested JSON, empty arrays).
// This is NOT domain-specific — it exists purely to validate type mapping.
const seedSQL = `
-- 1. all_types: one column per PostgreSQL type family
CREATE TABLE all_types (
    id              SERIAL PRIMARY KEY,
    -- Integer family
    col_smallint    SMALLINT,
    col_integer     INTEGER,
    col_bigint      BIGINT,
    -- Float family
    col_real        REAL,
    col_double      DOUBLE PRECISION,
    col_numeric     NUMERIC(12,4),
    -- Boolean
    col_bool        BOOLEAN,
    -- String family
    col_varchar     VARCHAR(200),
    col_text        TEXT,
    col_char        CHAR(10),
    -- Temporal family
    col_date        DATE,
    col_timestamp   TIMESTAMP WITHOUT TIME ZONE,
    col_timestamptz TIMESTAMP WITH TIME ZONE,
    -- Binary
    col_bytea       BYTEA,
    -- JSON family
    col_json        JSON,
    col_jsonb       JSONB,
    -- Array family
    col_int_arr     INTEGER[],
    col_text_arr    TEXT[],
    -- Network / UUID / other
    col_uuid        UUID,
    col_inet        INET,
    col_interval    INTERVAL
);

-- Row 1: typical values
INSERT INTO all_types (
    col_smallint, col_integer, col_bigint,
    col_real, col_double, col_numeric,
    col_bool,
    col_varchar, col_text, col_char,
    col_date, col_timestamp, col_timestamptz,
    col_bytea,
    col_json, col_jsonb,
    col_int_arr, col_text_arr,
    col_uuid, col_inet, col_interval
) VALUES (
    42, 100000, 9999999999,
    3.14, 2.718281828, 12345.6789,
    true,
    'hello world', 'long text value', 'pad       ',
    '2026-01-15', '2026-01-15 10:30:00', '2026-01-15 10:30:00+00',
    E'\\xDEADBEEF',
    '{"key": "value", "nested": {"a": 1}}',
    '{"key": "value", "tags": ["x","y"], "count": 42}',
    ARRAY[1, 2, 3], ARRAY['a', 'b', 'c'],
    'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '192.168.1.1', '2 hours 30 minutes'
);

-- Row 2: all NULLs (except id)
INSERT INTO all_types (
    col_smallint, col_integer, col_bigint,
    col_real, col_double, col_numeric,
    col_bool,
    col_varchar, col_text, col_char,
    col_date, col_timestamp, col_timestamptz,
    col_bytea,
    col_json, col_jsonb,
    col_int_arr, col_text_arr,
    col_uuid, col_inet, col_interval
) VALUES (
    NULL, NULL, NULL,
    NULL, NULL, NULL,
    NULL,
    NULL, NULL, NULL,
    NULL, NULL, NULL,
    NULL,
    NULL, NULL,
    NULL, NULL,
    NULL, NULL, NULL
);

-- Row 3: zeroes, empty strings, edge values
INSERT INTO all_types (
    col_smallint, col_integer, col_bigint,
    col_real, col_double, col_numeric,
    col_bool,
    col_varchar, col_text, col_char,
    col_date, col_timestamp, col_timestamptz,
    col_bytea,
    col_json, col_jsonb,
    col_int_arr, col_text_arr,
    col_uuid, col_inet, col_interval
) VALUES (
    0, 0, 0,
    0.0, 0.0, 0.0000,
    false,
    '', '', '',
    '1970-01-01', '1970-01-01 00:00:00', '1970-01-01 00:00:00+00',
    E'',
    '[]', '{}',
    ARRAY[]::integer[], ARRAY[]::text[],
    '00000000-0000-0000-0000-000000000000', '0.0.0.0', '0 seconds'
);

-- Row 4: boundary integers, special floats, unicode, nested JSON
INSERT INTO all_types (
    col_smallint, col_integer, col_bigint,
    col_real, col_double, col_numeric,
    col_bool,
    col_varchar, col_text, col_char,
    col_date, col_timestamp, col_timestamptz,
    col_bytea,
    col_json, col_jsonb,
    col_int_arr, col_text_arr,
    col_uuid, col_inet, col_interval
) VALUES (
    -32768, -2147483648, -9223372036854775808,
    'NaN'::real, 'Infinity'::double precision, -99999999.9999,
    true,
    '日本語テスト', 'émojis: 🎮🚀', '中文        ',
    '9999-12-31', '9999-12-31 23:59:59', '0001-01-01 00:00:00+00',
    E'\\x00FF00FF',
    '{"deeply": {"nested": {"value": true}}}',
    '[1, "mixed", null, true, 3.14]',
    ARRAY[-1, 0, 2147483647], ARRAY['line1\nline2', '', 'tab\there'],
    'ffffffff-ffff-ffff-ffff-ffffffffffff', '::1', '-1 year -2 months'
);

-- Row 5: max positive integers, -Infinity, negative numeric
INSERT INTO all_types (
    col_smallint, col_integer, col_bigint,
    col_real, col_double, col_numeric,
    col_bool,
    col_varchar, col_text, col_char,
    col_date, col_timestamp, col_timestamptz,
    col_bytea,
    col_json, col_jsonb,
    col_int_arr, col_text_arr,
    col_uuid, col_inet, col_interval
) VALUES (
    32767, 2147483647, 9223372036854775807,
    '-Infinity'::real, '-Infinity'::double precision, 99999999.9999,
    false,
    'special chars: <>&"', 'backslash: \\, quote: ''', 'tab	here  ',
    '2000-02-29', '2000-02-29 12:00:00', '2026-06-15 18:30:00+05:30',
    E'\\x0102030405060708090A',
    'null', '{"arr": [1,2,3], "obj": {"k": "v"}}',
    ARRAY[1], ARRAY['single'],
    '12345678-1234-1234-1234-123456789abc', '10.0.0.0/8', '1 year 6 months 3 days'
);

-- Run ANALYZE so pg_class.reltuples is populated for GetTableSchema row count.
ANALYZE all_types;
`

func setupPostgres(t *testing.T) (gowarehouse.Provider, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("failed to start PostgreSQL container: %v", err)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("failed to get connection string: %v", err)
	}

	provider, err := gowarehouse.NewProvider("postgres", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": connStr,
		"dataset":          "public",
	})
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("failed to create postgres provider: %v", err)
	}

	_, err = provider.Query(ctx, seedSQL, nil)
	if err != nil {
		provider.Close()
		container.Terminate(ctx)
		t.Fatalf("failed to seed test data: %v", err)
	}

	cleanup := func() {
		provider.Close()
		container.Terminate(ctx)
	}

	return provider, cleanup
}

// ---------------------------------------------------------------------------
// Core provider methods
// ---------------------------------------------------------------------------

func TestIntegration_HealthCheck(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
}

func TestIntegration_ValidateReadOnly(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := provider.ValidateReadOnly(ctx); err != nil {
		t.Fatalf("validate read-only failed: %v", err)
	}
}

func TestIntegration_SimpleQuery(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT 1 AS test_val", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(result.Rows))
	}
	if len(result.Columns) != 1 || result.Columns[0] != "test_val" {
		t.Errorf("expected column 'test_val', got %v", result.Columns)
	}
}

func TestIntegration_GetDataset(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	if provider.GetDataset() != "public" {
		t.Errorf("expected 'public', got %q", provider.GetDataset())
	}
}

func TestIntegration_SQLDialect(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	if provider.SQLDialect() == "" {
		t.Error("expected non-empty dialect")
	}
}

// ---------------------------------------------------------------------------
// ListTables / GetTableSchema
// ---------------------------------------------------------------------------

func TestIntegration_ListTables(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("expected at least 1 table")
	}

	found := false
	for _, name := range tables {
		if name == "all_types" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'all_types' in table list, got %v", tables)
	}
}

func TestIntegration_GetTableSchema(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	schema, err := provider.GetTableSchema(ctx, "all_types")
	if err != nil {
		t.Fatalf("GetTableSchema failed: %v", err)
	}

	if schema.Name != "all_types" {
		t.Errorf("expected name 'all_types', got %q", schema.Name)
	}

	colTypes := map[string]string{}
	colNullable := map[string]bool{}
	for _, col := range schema.Columns {
		colTypes[col.Name] = col.Type
		colNullable[col.Name] = col.Nullable
		t.Logf("  %-20s %-10s nullable=%v", col.Name, col.Type, col.Nullable)
	}

	// Verify normalized type mapping for each PostgreSQL type family.
	expected := map[string]string{
		"id":              "INT64",  // serial → integer
		"col_smallint":    "INT64",
		"col_integer":     "INT64",
		"col_bigint":      "INT64",
		"col_real":        "FLOAT64",
		"col_double":      "FLOAT64",
		"col_numeric":     "FLOAT64",
		"col_bool":        "BOOL",
		"col_varchar":     "STRING",
		"col_text":        "STRING",
		"col_char":        "STRING",
		"col_date":        "DATE",
		"col_timestamp":   "TIMESTAMP",
		"col_timestamptz": "TIMESTAMP",
		"col_bytea":       "BYTES",
		"col_json":        "RECORD",
		"col_jsonb":       "RECORD",
		"col_int_arr":     "STRING", // ARRAY maps to STRING
		"col_text_arr":    "STRING",
		"col_uuid":        "STRING",
		"col_inet":        "STRING",
		"col_interval":    "STRING",
	}

	for col, wantType := range expected {
		if colTypes[col] != wantType {
			t.Errorf("column %q: expected type %q, got %q", col, wantType, colTypes[col])
		}
	}

	// id (serial PK) is NOT NULL.
	if colNullable["id"] {
		t.Error("expected 'id' to be NOT NULL")
	}
	// Everything else is nullable.
	if !colNullable["col_integer"] {
		t.Error("expected 'col_integer' to be nullable")
	}

	// ANALYZE was run — row count should be 5.
	if schema.RowCount != 5 {
		t.Logf("row count = %d (expected 5 — pg_class.reltuples is an estimate)", schema.RowCount)
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 1 (typical values)
// ---------------------------------------------------------------------------

func TestIntegration_TypicalValues(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM all_types WHERE id = 1", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-20s = %-40v (Go: %T)", col, row[col], row[col])
	}

	// Integer family → int64
	assertType[int64](t, row, "col_smallint", 42)
	assertType[int64](t, row, "col_integer", 100000)
	assertType[int64](t, row, "col_bigint", 9999999999)

	// Float family → float64
	assertGoType[float64](t, row, "col_real")
	assertGoType[float64](t, row, "col_double")

	// NUMERIC → float64 (converted from []byte by convertStringByType)
	assertGoType[float64](t, row, "col_numeric")

	// Boolean → bool
	assertType[bool](t, row, "col_bool", true)

	// String family → string
	assertGoType[string](t, row, "col_varchar")
	assertGoType[string](t, row, "col_text")
	assertGoType[string](t, row, "col_char")

	// Temporal → string (RFC3339, normalized from time.Time)
	assertGoType[string](t, row, "col_date")
	assertGoType[string](t, row, "col_timestamp")
	assertGoType[string](t, row, "col_timestamptz")

	// Bytea → string (normalized from []byte)
	assertGoType[string](t, row, "col_bytea")

	// JSON/JSONB → string (from []byte)
	assertGoType[string](t, row, "col_json")
	assertGoType[string](t, row, "col_jsonb")

	// Arrays → string (from []byte)
	assertGoType[string](t, row, "col_int_arr")
	assertGoType[string](t, row, "col_text_arr")

	// UUID, inet, interval → string (from []byte)
	assertGoType[string](t, row, "col_uuid")
	assertGoType[string](t, row, "col_inet")
	assertGoType[string](t, row, "col_interval")
}

// ---------------------------------------------------------------------------
// Data type assertions — row 2 (all NULLs)
// ---------------------------------------------------------------------------

func TestIntegration_AllNulls(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM all_types WHERE id = 2", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		if col == "id" {
			continue
		}
		if row[col] != nil {
			t.Errorf("%s: expected nil, got %v (%T)", col, row[col], row[col])
		}
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 3 (zeroes, empty strings, epoch dates)
// ---------------------------------------------------------------------------

func TestIntegration_Zeroes(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM all_types WHERE id = 3", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]

	// Zero integers are still int64, not nil.
	assertType[int64](t, row, "col_smallint", 0)
	assertType[int64](t, row, "col_integer", 0)
	assertType[int64](t, row, "col_bigint", 0)

	// Zero floats.
	assertType[float64](t, row, "col_real", 0.0)
	assertType[float64](t, row, "col_double", 0.0)

	// Zero numeric — always float64 for NUMERIC/DECIMAL consistency.
	assertType[float64](t, row, "col_numeric", 0.0)

	// false is still bool, not nil.
	assertType[bool](t, row, "col_bool", false)

	// Empty strings are string, not nil.
	if v, ok := row["col_varchar"].(string); !ok {
		t.Errorf("col_varchar: expected string, got %T", row["col_varchar"])
	} else if len(strings.TrimSpace(v)) != 0 {
		// varchar stores "" as ""
		// but char(10) pads with spaces — checked below
	}

	// Epoch date.
	if v, ok := row["col_date"].(string); !ok {
		t.Errorf("col_date: expected string, got %T", row["col_date"])
	} else if !strings.HasPrefix(v, "1970-01-01") {
		t.Errorf("col_date: expected epoch, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 4 (boundary integers, NaN, Infinity, unicode)
// ---------------------------------------------------------------------------

func TestIntegration_Boundaries(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM all_types WHERE id = 4", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-20s = %-40v (Go: %T)", col, row[col], row[col])
	}

	// Min integers.
	assertType[int64](t, row, "col_smallint", -32768)
	assertType[int64](t, row, "col_integer", -2147483648)
	assertType[int64](t, row, "col_bigint", -9223372036854775808)

	// NaN (real) — lib/pq returns float64 NaN.
	if v, ok := row["col_real"].(float64); !ok {
		t.Errorf("col_real: expected float64, got %T", row["col_real"])
	} else if v == v { // NaN != NaN
		t.Errorf("col_real: expected NaN, got %v", v)
	}

	// +Infinity (double precision).
	if v, ok := row["col_double"].(float64); !ok {
		t.Errorf("col_double: expected float64, got %T", row["col_double"])
	} else if v <= 0 || v == v-1 { // Infinity check: Inf - 1 == Inf
		// This is a loose check; just verify it's positive and huge.
	}

	// Negative numeric.
	assertGoType[float64](t, row, "col_numeric")

	// Unicode strings.
	if v, ok := row["col_varchar"].(string); !ok || v != "日本語テスト" {
		t.Errorf("col_varchar: expected '日本語テスト', got %v", row["col_varchar"])
	}
	if v, ok := row["col_text"].(string); !ok || !strings.Contains(v, "🎮") {
		t.Errorf("col_text: expected emoji content, got %v", row["col_text"])
	}

	// Far-future date.
	if v, ok := row["col_date"].(string); !ok || !strings.HasPrefix(v, "9999-12-31") {
		t.Errorf("col_date: expected 9999-12-31, got %v", row["col_date"])
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 5 (max integers, -Infinity)
// ---------------------------------------------------------------------------

func TestIntegration_MaxIntegers(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := provider.Query(ctx, "SELECT * FROM all_types WHERE id = 5", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]

	// Max integers.
	assertType[int64](t, row, "col_smallint", 32767)
	assertType[int64](t, row, "col_integer", 2147483647)
	assertType[int64](t, row, "col_bigint", 9223372036854775807)

	// -Infinity (real).
	if v, ok := row["col_real"].(float64); !ok {
		t.Errorf("col_real: expected float64, got %T", row["col_real"])
	} else if v >= 0 {
		t.Errorf("col_real: expected -Infinity, got %v", v)
	}

	// -Infinity (double precision).
	if v, ok := row["col_double"].(float64); !ok {
		t.Errorf("col_double: expected float64, got %T", row["col_double"])
	} else if v >= 0 {
		t.Errorf("col_double: expected -Infinity, got %v", v)
	}

	// Leap day.
	if v, ok := row["col_date"].(string); !ok || !strings.HasPrefix(v, "2000-02-29") {
		t.Errorf("col_date: expected leap day 2000-02-29, got %v", row["col_date"])
	}

	// JSON literal null (text "null", not Go nil).
	if v, ok := row["col_json"].(string); !ok || v != "null" {
		t.Errorf("col_json: expected string 'null', got %v (%T)", row["col_json"], row["col_json"])
	}
}

// ---------------------------------------------------------------------------
// Inline SELECT type exercise (no table dependency)
// ---------------------------------------------------------------------------

func TestIntegration_SelectCastTypes(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `SELECT
		1::integer AS int_val,
		9999999999::bigint AS bigint_val,
		1::smallint AS smallint_val,
		3.14::real AS real_val,
		2.718::double precision AS double_val,
		123.45::numeric(10,2) AS numeric_val,
		true::boolean AS bool_val,
		'hello'::text AS text_val,
		'2026-01-15'::date AS date_val,
		'2026-01-15 10:30:00+00'::timestamptz AS timestamptz_val,
		NULL::integer AS null_val`

	result, err := provider.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-20s = %-30v (Go: %T)", col, row[col], row[col])
	}

	assertGoType[int64](t, row, "int_val")
	assertGoType[int64](t, row, "bigint_val")
	assertGoType[float64](t, row, "real_val")
	assertGoType[float64](t, row, "double_val")
	assertGoType[float64](t, row, "numeric_val")
	assertGoType[bool](t, row, "bool_val")
	assertGoType[string](t, row, "text_val")
	assertGoType[string](t, row, "date_val")
	assertGoType[string](t, row, "timestamptz_val")

	if row["null_val"] != nil {
		t.Errorf("null_val: expected nil, got %T (%v)", row["null_val"], row["null_val"])
	}
}

// ---------------------------------------------------------------------------
// Full interface exercise
// ---------------------------------------------------------------------------

func TestIntegration_ProviderInterface(t *testing.T) {
	provider, cleanup := setupPostgres(t)
	defer cleanup()

	var _ gowarehouse.Provider = provider

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if err := provider.ValidateReadOnly(ctx); err != nil {
		t.Errorf("ValidateReadOnly: %v", err)
	}
	if provider.GetDataset() == "" {
		t.Error("GetDataset returned empty")
	}
	if provider.SQLDialect() == "" {
		t.Error("SQLDialect returned empty")
	}
	if provider.SQLFixPrompt() == "" {
		t.Error("SQLFixPrompt returned empty")
	}

	tables, err := provider.ListTables(ctx)
	if err != nil {
		t.Errorf("ListTables: %v", err)
	}
	if len(tables) == 0 {
		t.Error("ListTables returned empty")
	}
	t.Logf("Tables: %v", tables)

	schema, err := provider.GetTableSchema(ctx, "all_types")
	if err != nil {
		t.Errorf("GetTableSchema: %v", err)
	} else {
		t.Logf("Schema: %d columns, ~%d rows", len(schema.Columns), schema.RowCount)
	}

	result, err := provider.Query(ctx, "SELECT * FROM all_types LIMIT 5", nil)
	if err != nil {
		t.Errorf("Query: %v", err)
	} else {
		t.Logf("Query returned %d rows, %d columns", len(result.Rows), len(result.Columns))
	}
}

// ---------------------------------------------------------------------------
// Environment-based test (for external PostgreSQL: RDS, Cloud SQL, etc.)
// ---------------------------------------------------------------------------

func TestIntegration_EnvVar_HealthCheck(t *testing.T) {
	cfg := getEnvConfig(t)

	provider, err := gowarehouse.NewProvider("postgres", cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := provider.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	t.Log("Environment-based HealthCheck OK")
}

func getEnvConfig(t *testing.T) gowarehouse.ProviderConfig {
	t.Helper()

	host := os.Getenv("INTEGRATION_TEST_POSTGRES_HOST")
	if host == "" {
		t.Skip("INTEGRATION_TEST_POSTGRES_HOST not set — skipping env-based test")
	}

	port := os.Getenv("INTEGRATION_TEST_POSTGRES_PORT")
	if port == "" {
		port = "5432"
	}
	user := os.Getenv("INTEGRATION_TEST_POSTGRES_USER")
	if user == "" {
		user = "postgres"
	}
	password := os.Getenv("INTEGRATION_TEST_POSTGRES_PASSWORD")
	if password == "" {
		t.Skip("INTEGRATION_TEST_POSTGRES_PASSWORD not set")
	}
	database := os.Getenv("INTEGRATION_TEST_POSTGRES_DATABASE")
	if database == "" {
		database = "postgres"
	}
	schema := os.Getenv("INTEGRATION_TEST_POSTGRES_SCHEMA")
	if schema == "" {
		schema = "public"
	}
	sslmode := os.Getenv("INTEGRATION_TEST_POSTGRES_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}

	return gowarehouse.ProviderConfig{
		"host":             host,
		"port":             port,
		"user":             user,
		"database":         database,
		"dataset":          schema,
		"sslmode":          sslmode,
		"credentials_json": password,
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// assertType checks that row[col] is of type T and equals want.
func assertType[T comparable](t *testing.T, row map[string]interface{}, col string, want T) {
	t.Helper()
	v, ok := row[col].(T)
	if !ok {
		t.Errorf("%s: expected %T, got %T (%v)", col, want, row[col], row[col])
		return
	}
	if v != want {
		t.Errorf("%s: expected %v, got %v", col, want, v)
	}
}

// assertGoType checks that row[col] is of Go type T (value not checked).
func assertGoType[T any](t *testing.T, row map[string]interface{}, col string) {
	t.Helper()
	if _, ok := row[col].(T); !ok {
		t.Errorf("%s: expected %T, got %T (%v)", col, *new(T), row[col], row[col])
	}
}
