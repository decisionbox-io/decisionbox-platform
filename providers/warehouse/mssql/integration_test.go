//go:build integration_mssql

package mssql

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/testcontainers/testcontainers-go"
	tcmssql "github.com/testcontainers/testcontainers-go/modules/mssql"
)

// seedSQL creates a type-exercise table covering every SQL Server data type
// that go-mssqldb can return, plus edge-case rows (NULLs, empty strings, zeroes,
// boundary integers, unicode, GUIDs, datetimeoffset, money). This is NOT
// domain-specific — it exists purely to validate type mapping and value
// normalization.
//
// SQL Server has no dedicated JSON type (JSON is stored in nvarchar(max)) and
// no dedicated array type. This is intentional.
const seedSQL = `
-- Secondary schema used to validate cross-schema listing. SQL Server's
-- INFORMATION_SCHEMA.TABLES never reports the sys schema's catalog objects
-- as BASE TABLE (they are views), so we cannot rely on it. CREATE SCHEMA
-- must be the first statement in its batch — wrap in EXEC to side-step that.
EXEC('CREATE SCHEMA analytics');
CREATE TABLE analytics.events (
    id INT IDENTITY(1,1) PRIMARY KEY,
    name NVARCHAR(100)
);
INSERT INTO analytics.events (name) VALUES (N'seed');

CREATE TABLE all_types (
    id              INT IDENTITY(1,1) PRIMARY KEY,
    col_tinyint     TINYINT,
    col_smallint    SMALLINT,
    col_int         INT,
    col_bigint      BIGINT,
    col_real        REAL,
    col_float       FLOAT,
    col_decimal     DECIMAL(18, 4),
    col_numeric     NUMERIC(12, 4),
    col_money       MONEY,
    col_smallmoney  SMALLMONEY,
    col_bit         BIT,
    col_char        CHAR(10),
    col_nchar       NCHAR(10),
    col_varchar     VARCHAR(200),
    col_nvarchar    NVARCHAR(200),
    col_text        TEXT,
    col_ntext       NTEXT,
    col_date        DATE,
    col_datetime    DATETIME,
    col_datetime2   DATETIME2(3),
    col_smalldt     SMALLDATETIME,
    col_dto         DATETIMEOFFSET,
    col_time        TIME,
    col_binary      BINARY(4),
    col_varbinary   VARBINARY(200),
    col_guid        UNIQUEIDENTIFIER,
    col_xml         XML,
    col_json        NVARCHAR(MAX)
);

INSERT INTO all_types (
    col_tinyint, col_smallint, col_int, col_bigint,
    col_real, col_float, col_decimal, col_numeric, col_money, col_smallmoney,
    col_bit,
    col_char, col_nchar, col_varchar, col_nvarchar, col_text, col_ntext,
    col_date, col_datetime, col_datetime2, col_smalldt, col_dto, col_time,
    col_binary, col_varbinary,
    col_guid, col_xml, col_json
) VALUES (
    42, 1000, 100000, 9999999999,
    3.14, 2.718281828, 12345.6789, 9876.5432, 1000000.1234, 2.50,
    1,
    'padded',   N'nchar_pad', 'hello world',        N'unicode: hello',      'long text value',    N'unicode long',
    '2026-01-15', '2026-01-15 10:30:00', '2026-01-15 10:30:00.123', '2026-01-15 10:30:00', '2026-01-15 10:30:00 +05:00', '14:30:15',
    0xDEADBEEF, 0xCAFEBABE00,
    'A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11', '<root><a>1</a></root>', '{"key":"value","nested":{"n":42}}'
);

INSERT INTO all_types (
    col_tinyint, col_smallint, col_int, col_bigint,
    col_real, col_float, col_decimal, col_numeric, col_money, col_smallmoney,
    col_bit,
    col_char, col_nchar, col_varchar, col_nvarchar, col_text, col_ntext,
    col_date, col_datetime, col_datetime2, col_smalldt, col_dto, col_time,
    col_binary, col_varbinary,
    col_guid, col_xml, col_json
) VALUES (
    NULL, NULL, NULL, NULL,
    NULL, NULL, NULL, NULL, NULL, NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL, NULL,
    NULL, NULL, NULL, NULL, NULL, NULL,
    NULL, NULL,
    NULL, NULL, NULL
);

INSERT INTO all_types (
    col_tinyint, col_smallint, col_int, col_bigint,
    col_real, col_float, col_decimal, col_numeric, col_money, col_smallmoney,
    col_bit,
    col_char, col_nchar, col_varchar, col_nvarchar, col_text, col_ntext,
    col_date, col_datetime, col_datetime2, col_smalldt, col_dto, col_time,
    col_binary, col_varbinary,
    col_guid, col_xml, col_json
) VALUES (
    0, 0, 0, 0,
    0.0, 0.0, 0.0000, 0.0000, 0.00, 0.00,
    0,
    '', N'', '', N'', '', N'',
    '1970-01-01', '1970-01-01 00:00:00', '1970-01-01 00:00:00', '1970-01-01 00:00:00', '1970-01-01 00:00:00 +00:00', '00:00:00',
    0x00000000, 0x,
    '00000000-0000-0000-0000-000000000000', '<empty/>', '{}'
);

INSERT INTO all_types (
    col_tinyint, col_smallint, col_int, col_bigint,
    col_real, col_float, col_decimal, col_numeric, col_money, col_smallmoney,
    col_bit,
    col_char, col_nchar, col_varchar, col_nvarchar, col_text, col_ntext,
    col_date, col_datetime, col_datetime2, col_smalldt, col_dto, col_time,
    col_binary, col_varbinary,
    col_guid, col_xml, col_json
) VALUES (
    255, 32767, 2147483647, 9223372036854775807,
    3.402823e+38, 1.7976931348623157e+308, 99999999999999.9999, 99999999.9999, 922337203685477.5807, 214748.3647,
    1,
    '中文',      N'日本語テスト',   'émojis: 🎮🚀', N'混合中英 ABC 123',   'special chars: <>&"''', N'line1' + CHAR(13)+CHAR(10) + N'line2',
    '9999-12-31', '9999-12-31 23:59:59', '9999-12-31 23:59:59.999', '2079-06-06 23:59:00', '9999-12-31 23:59:59 +14:00', '23:59:59',
    0xFFFFFFFF, 0x0102030405060708090A,
    'FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF', '<a attr="v"><b/></a>', '{"arr":[1,2,3],"flag":true,"null":null}'
);
`

type mssqlFixture struct {
	masterDSN string
	testDSN   string
	container *tcmssql.MSSQLServerContainer
}

func setupMSSQLContainer(t *testing.T) *mssqlFixture {
	t.Helper()
	ctx := context.Background()
	password := "DecisionBox!Integration123"

	container, err := tcmssql.Run(ctx,
		"mcr.microsoft.com/mssql/server:2022-latest",
		tcmssql.WithAcceptEULA(),
		tcmssql.WithPassword(password),
	)
	if err != nil {
		t.Fatalf("failed to start MSSQL container: %v", err)
	}

	// Connection string for the default (master) database, used to bootstrap
	// a test database. The extra args disable TLS, which is fine for an
	// ephemeral local container.
	masterDSN, err := container.ConnectionString(ctx,
		"encrypt=disable",
		"TrustServerCertificate=true",
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to get master connection string: %v", err)
	}

	// Bootstrap database + seed data. database/sql runs each statement in one
	// batch; CREATE DATABASE must run on its own call.
	master, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": masterDSN,
	})
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to create master provider: %v", err)
	}
	bootCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := master.Query(bootCtx, "CREATE DATABASE dbx_integration_test", nil); err != nil {
		_ = master.Close()
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to create test database: %v", err)
	}
	_ = master.Close()

	// DSN that targets the new database. Parse the master DSN and swap in
	// the database query parameter so we don't rely on fragile substring
	// matching against whatever the testcontainer module emits.
	parsedDSN, err := url.Parse(masterDSN)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to parse master DSN: %v", err)
	}
	q := parsedDSN.Query()
	q.Set("database", "dbx_integration_test")
	parsedDSN.RawQuery = q.Encode()
	testDSN := parsedDSN.String()

	seedProvider, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": testDSN,
	})
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to create seed provider: %v", err)
	}
	if _, err := seedProvider.Query(bootCtx, seedSQL, nil); err != nil {
		_ = seedProvider.Close()
		_ = testcontainers.TerminateContainer(container)
		t.Fatalf("failed to seed test data: %v", err)
	}
	_ = seedProvider.Close()

	return &mssqlFixture{
		masterDSN: masterDSN,
		testDSN:   testDSN,
		container: container,
	}
}

func (f *mssqlFixture) newProvider(t *testing.T) gowarehouse.Provider {
	t.Helper()
	p, err := gowarehouse.NewProvider("mssql", gowarehouse.ProviderConfig{
		"auth_method":      "connection_string",
		"credentials_json": f.testDSN,
		"dataset":          "dbo",
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	return p
}

func (f *mssqlFixture) teardown() {
	if f.container != nil {
		_ = testcontainers.TerminateContainer(f.container)
	}
}

// ---------------------------------------------------------------------------
// Core provider methods
// ---------------------------------------------------------------------------

func TestIntegration_HealthCheck(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.HealthCheck(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}
}

func TestIntegration_ValidateReadOnly(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.ValidateReadOnly(ctx); err != nil {
		t.Fatalf("validate read-only failed: %v", err)
	}
}

func TestIntegration_SimpleQuery(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Query(ctx, "SELECT 1 AS test_val", nil)
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

func TestIntegration_GetDatasetAndDialect(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	if p.GetDataset() != "dbo" {
		t.Errorf("expected 'dbo', got %q", p.GetDataset())
	}
	if p.SQLDialect() == "" {
		t.Error("expected non-empty dialect")
	}
	if p.SQLFixPrompt() == "" {
		t.Error("expected non-empty SQL fix prompt")
	}
}

// ---------------------------------------------------------------------------
// ListTables / GetTableSchema
// ---------------------------------------------------------------------------

func TestIntegration_ListTables(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tables, err := p.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables failed: %v", err)
	}
	found := false
	for _, name := range tables {
		if name == "all_types" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'all_types' in tables, got %v", tables)
	}
}

func TestIntegration_ListTablesInDataset_OtherSchema(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The seed creates 'analytics.events' in a schema different from the
	// provider's default 'dbo' — verify cross-schema listing works.
	tables, err := p.ListTablesInDataset(ctx, "analytics")
	if err != nil {
		t.Fatalf("ListTablesInDataset(analytics) failed: %v", err)
	}
	if len(tables) == 0 {
		t.Error("expected at least one table in analytics schema")
	}
	found := false
	for _, name := range tables {
		if name == "events" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'events' in analytics schema, got %v", tables)
	}
}

func TestIntegration_GetTableSchema(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema, err := p.GetTableSchema(ctx, "all_types")
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
		t.Logf("  %-15s %-10s nullable=%v", col.Name, col.Type, col.Nullable)
	}

	expected := map[string]string{
		"id":             "INT64",
		"col_tinyint":    "INT64",
		"col_smallint":   "INT64",
		"col_int":        "INT64",
		"col_bigint":     "INT64",
		"col_real":       "FLOAT64",
		"col_float":      "FLOAT64",
		"col_decimal":    "FLOAT64",
		"col_numeric":    "FLOAT64",
		"col_money":      "FLOAT64",
		"col_smallmoney": "FLOAT64",
		"col_bit":        "BOOL",
		"col_char":       "STRING",
		"col_nchar":      "STRING",
		"col_varchar":    "STRING",
		"col_nvarchar":   "STRING",
		"col_text":       "STRING",
		"col_ntext":      "STRING",
		"col_date":       "DATE",
		"col_datetime":   "TIMESTAMP",
		"col_datetime2":  "TIMESTAMP",
		"col_smalldt":    "TIMESTAMP",
		"col_dto":        "TIMESTAMP",
		"col_time":       "STRING",
		"col_binary":     "BYTES",
		"col_varbinary":  "BYTES",
		"col_guid":       "STRING",
		"col_xml":        "STRING",
		"col_json":       "STRING",
	}
	for col, wantType := range expected {
		if colTypes[col] != wantType {
			t.Errorf("column %q: expected %q, got %q", col, wantType, colTypes[col])
		}
	}

	// id (IDENTITY PK) is NOT NULL; everything else is nullable.
	if colNullable["id"] {
		t.Error("expected 'id' to be NOT NULL")
	}
	if !colNullable["col_int"] {
		t.Error("expected 'col_int' to be nullable")
	}

	if schema.RowCount == 0 {
		t.Log("row count = 0 (sys.dm_db_partition_stats may not be populated yet)")
	} else if schema.RowCount != 4 {
		t.Logf("row count = %d (expected 4)", schema.RowCount)
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 1 (typical values)
// ---------------------------------------------------------------------------

func TestIntegration_TypicalValues(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Query(ctx, "SELECT * FROM all_types WHERE id = 1", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-15s = %-50v (Go: %T)", col, row[col], row[col])
	}

	// Integer family → int64.
	assertType[int64](t, row, "col_tinyint", 42)
	assertType[int64](t, row, "col_smallint", 1000)
	assertType[int64](t, row, "col_int", 100000)
	assertType[int64](t, row, "col_bigint", 9999999999)

	// Float/numeric family → float64 (DECIMAL/NUMERIC/MONEY/SMALLMONEY
	// routed through convertBytesByType).
	assertGoType[float64](t, row, "col_real")
	assertGoType[float64](t, row, "col_float")
	assertGoType[float64](t, row, "col_decimal")
	assertGoType[float64](t, row, "col_numeric")
	assertGoType[float64](t, row, "col_money")
	assertGoType[float64](t, row, "col_smallmoney")

	// BIT → bool.
	assertType[bool](t, row, "col_bit", true)

	// String family → string (both char-pad and variable-length).
	assertGoType[string](t, row, "col_char")
	assertGoType[string](t, row, "col_nchar")
	assertGoType[string](t, row, "col_varchar")
	assertGoType[string](t, row, "col_nvarchar")
	assertGoType[string](t, row, "col_text")
	assertGoType[string](t, row, "col_ntext")

	// Temporal → string (RFC3339, normalized from time.Time).
	assertGoType[string](t, row, "col_date")
	assertGoType[string](t, row, "col_datetime")
	assertGoType[string](t, row, "col_datetime2")
	assertGoType[string](t, row, "col_smalldt")
	assertGoType[string](t, row, "col_dto")
	assertGoType[string](t, row, "col_time")

	// Binary → string (normalized via convertBytesByType).
	assertGoType[string](t, row, "col_binary")
	assertGoType[string](t, row, "col_varbinary")

	// GUID → canonical hex string.
	if v, ok := row["col_guid"].(string); !ok {
		t.Errorf("col_guid: expected string, got %T", row["col_guid"])
	} else if !strings.EqualFold(v, "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11") {
		t.Errorf("col_guid: expected canonical GUID, got %q", v)
	}

	// XML and JSON stored as text.
	assertGoType[string](t, row, "col_xml")
	assertGoType[string](t, row, "col_json")
}

// ---------------------------------------------------------------------------
// Data type assertions — row 2 (all NULLs)
// ---------------------------------------------------------------------------

func TestIntegration_AllNulls(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Query(ctx, "SELECT * FROM all_types WHERE id = 2", nil)
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
// Data type assertions — row 3 (zeroes + empty strings)
// ---------------------------------------------------------------------------

func TestIntegration_Zeroes(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Query(ctx, "SELECT * FROM all_types WHERE id = 3", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]

	// Zero integers stay int64 (not nil).
	assertType[int64](t, row, "col_tinyint", 0)
	assertType[int64](t, row, "col_smallint", 0)
	assertType[int64](t, row, "col_int", 0)
	assertType[int64](t, row, "col_bigint", 0)

	// Zero floats.
	assertType[float64](t, row, "col_real", 0.0)
	assertType[float64](t, row, "col_float", 0.0)

	// Zero MONEY/DECIMAL (routed through convertBytesByType → float64).
	assertType[float64](t, row, "col_money", 0.0)
	assertType[float64](t, row, "col_smallmoney", 0.0)
	assertType[float64](t, row, "col_decimal", 0.0)
	assertType[float64](t, row, "col_numeric", 0.0)

	// false is still bool, not nil.
	assertType[bool](t, row, "col_bit", false)

	// Empty strings are still string (not nil).
	assertGoType[string](t, row, "col_varchar")
	assertGoType[string](t, row, "col_nvarchar")

	// Epoch.
	if v, ok := row["col_date"].(string); !ok {
		t.Errorf("col_date: expected string, got %T", row["col_date"])
	} else if !strings.HasPrefix(v, "1970-01-01") {
		t.Errorf("col_date: expected epoch, got %q", v)
	}

	// Zero GUID.
	if v, ok := row["col_guid"].(string); !ok {
		t.Errorf("col_guid: expected string, got %T", row["col_guid"])
	} else if v != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("col_guid: expected zero GUID, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// Data type assertions — row 4 (boundaries, unicode, max GUID)
// ---------------------------------------------------------------------------

func TestIntegration_Boundaries(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := p.Query(ctx, "SELECT * FROM all_types WHERE id = 4", nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-15s = %-50v (Go: %T)", col, row[col], row[col])
	}

	// Max integers — note: TINYINT in SQL Server is unsigned (0..255).
	assertType[int64](t, row, "col_tinyint", 255)
	assertType[int64](t, row, "col_smallint", 32767)
	assertType[int64](t, row, "col_int", 2147483647)
	assertType[int64](t, row, "col_bigint", 9223372036854775807)

	// Max GUID.
	if v, ok := row["col_guid"].(string); !ok {
		t.Errorf("col_guid: expected string, got %T", row["col_guid"])
	} else if !strings.EqualFold(v, "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF") {
		t.Errorf("col_guid: expected max GUID, got %q", v)
	}

	// Far-future date.
	if v, ok := row["col_date"].(string); !ok {
		t.Errorf("col_date: expected string, got %T", row["col_date"])
	} else if !strings.HasPrefix(v, "9999-12-31") {
		t.Errorf("col_date: expected 9999-12-31, got %q", v)
	}

	// Unicode. CHAR and VARCHAR are single-byte (Latin-1 / default CP);
	// SQL Server silently replaces CJK characters with '?' on insert. This
	// is expected — test Unicode on the NCHAR/NVARCHAR columns instead.
	if v, ok := row["col_nchar"].(string); !ok {
		t.Errorf("col_nchar: expected string, got %T", row["col_nchar"])
	} else if !strings.Contains(v, "日本語") {
		t.Errorf("col_nchar: expected unicode content, got %q", v)
	}
	if v, ok := row["col_nvarchar"].(string); !ok {
		t.Errorf("col_nvarchar: expected string, got %T", row["col_nvarchar"])
	} else if !strings.Contains(v, "混合中英") {
		t.Errorf("col_nvarchar: expected unicode content, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// Inline SELECT type exercise (no table dependency)
// ---------------------------------------------------------------------------

func TestIntegration_SelectCastTypes(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := `SELECT
		CAST(1 AS int)                    AS int_val,
		CAST(9999999999 AS bigint)        AS bigint_val,
		CAST(1 AS smallint)               AS smallint_val,
		CAST(42 AS tinyint)               AS tinyint_val,
		CAST(3.14 AS real)                AS real_val,
		CAST(2.718 AS float)              AS float_val,
		CAST(123.45 AS decimal(10,2))     AS decimal_val,
		CAST(1000 AS money)               AS money_val,
		CAST(1 AS bit)                    AS bool_val,
		CAST('hello' AS nvarchar(50))     AS text_val,
		CAST('2026-01-15' AS date)        AS date_val,
		CAST('2026-01-15 10:30:00 +00:00' AS datetimeoffset) AS dto_val,
		CAST(NULL AS int)                 AS null_val`

	result, err := p.Query(ctx, query, nil)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	row := result.Rows[0]
	for _, col := range result.Columns {
		t.Logf("  %-15s = %-30v (Go: %T)", col, row[col], row[col])
	}

	assertGoType[int64](t, row, "int_val")
	assertGoType[int64](t, row, "bigint_val")
	assertGoType[int64](t, row, "smallint_val")
	assertGoType[int64](t, row, "tinyint_val")
	assertGoType[float64](t, row, "real_val")
	assertGoType[float64](t, row, "float_val")
	assertGoType[float64](t, row, "decimal_val")
	assertGoType[float64](t, row, "money_val")
	assertGoType[bool](t, row, "bool_val")
	assertGoType[string](t, row, "text_val")
	assertGoType[string](t, row, "date_val")
	assertGoType[string](t, row, "dto_val")
	if row["null_val"] != nil {
		t.Errorf("null_val: expected nil, got %T (%v)", row["null_val"], row["null_val"])
	}
}

// ---------------------------------------------------------------------------
// Full interface exercise
// ---------------------------------------------------------------------------

func TestIntegration_ProviderInterface(t *testing.T) {
	f := setupMSSQLContainer(t)
	defer f.teardown()

	p := f.newProvider(t)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := p.HealthCheck(ctx); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if err := p.ValidateReadOnly(ctx); err != nil {
		t.Errorf("ValidateReadOnly: %v", err)
	}
	if p.GetDataset() == "" {
		t.Error("GetDataset returned empty")
	}
	if p.SQLDialect() == "" {
		t.Error("SQLDialect returned empty")
	}
	if p.SQLFixPrompt() == "" {
		t.Error("SQLFixPrompt returned empty")
	}

	tables, err := p.ListTables(ctx)
	if err != nil {
		t.Errorf("ListTables: %v", err)
	}
	if len(tables) == 0 {
		t.Error("ListTables returned empty")
	}
	t.Logf("Tables: %v", tables)

	schema, err := p.GetTableSchema(ctx, "all_types")
	if err != nil {
		t.Errorf("GetTableSchema: %v", err)
	} else {
		t.Logf("Schema: %d columns, ~%d rows", len(schema.Columns), schema.RowCount)
	}

	result, err := p.Query(ctx, "SELECT TOP 5 * FROM all_types ORDER BY id", nil)
	if err != nil {
		t.Errorf("Query: %v", err)
	} else {
		t.Logf("Query returned %d rows, %d columns", len(result.Rows), len(result.Columns))
	}
}

// ---------------------------------------------------------------------------
// Environment-based test (for external SQL Server: Azure SQL, RDS SQL, etc.)
// ---------------------------------------------------------------------------

func TestIntegration_EnvVar_HealthCheck(t *testing.T) {
	cfg := getEnvConfig(t)

	provider, err := gowarehouse.NewProvider("mssql", cfg)
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

	host := os.Getenv("INTEGRATION_TEST_MSSQL_HOST")
	if host == "" {
		t.Skip("INTEGRATION_TEST_MSSQL_HOST not set — skipping env-based test")
	}
	port := os.Getenv("INTEGRATION_TEST_MSSQL_PORT")
	if port == "" {
		port = defaultPort
	}
	user := os.Getenv("INTEGRATION_TEST_MSSQL_USER")
	if user == "" {
		user = "sa"
	}
	password := os.Getenv("INTEGRATION_TEST_MSSQL_PASSWORD")
	if password == "" {
		t.Skip("INTEGRATION_TEST_MSSQL_PASSWORD not set")
	}
	database := os.Getenv("INTEGRATION_TEST_MSSQL_DATABASE")
	if database == "" {
		database = "master"
	}
	schema := os.Getenv("INTEGRATION_TEST_MSSQL_SCHEMA")
	if schema == "" {
		schema = defaultSchema
	}
	encrypt := os.Getenv("INTEGRATION_TEST_MSSQL_ENCRYPT")
	if encrypt == "" {
		encrypt = "true"
	}
	trust := os.Getenv("INTEGRATION_TEST_MSSQL_TRUST_CERT")
	if trust == "" {
		trust = "false"
	}

	return gowarehouse.ProviderConfig{
		"host":                     host,
		"port":                     port,
		"user":                     user,
		"database":                 database,
		"dataset":                  schema,
		"encrypt":                  encrypt,
		"trust_server_certificate": trust,
		"credentials_json":         password,
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
