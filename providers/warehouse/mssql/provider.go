// Package mssql provides a warehouse.Provider for Microsoft SQL Server.
//
// Configuration:
//
//	WAREHOUSE_PROVIDER=mssql
//	host + port + database + user + dataset (schema) in project config
//
// Authentication: SQL login (username/password) or full connection string.
//   - Password auth: set password in warehouse-credentials secret
//   - Connection string auth: set full sqlserver:// DSN in warehouse-credentials secret
//
// The provider uses the microsoft/go-mssqldb driver registered under the
// "sqlserver" driver name and queries INFORMATION_SCHEMA for table/column
// metadata. Supports SQL Server 2016+ and Azure SQL Database.
package mssql

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	// Registers "sqlserver" (and legacy "mssql") driver names with database/sql.
	_ "github.com/microsoft/go-mssqldb"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

//go:embed prompts/sql_fix.md
var sqlFixPrompt string

const (
	defaultSchema  = "dbo"
	defaultPort    = "1433"
	defaultTimeout = 5
)

func init() {
	gowarehouse.RegisterWithMeta("mssql", factory, gowarehouse.ProviderMeta{
		Name:        "Microsoft SQL Server",
		Description: "Microsoft SQL Server (SQL Server 2016+, Azure SQL Database)",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "host", Label: "Host", Required: true, Type: "string", Placeholder: "mssql.example.com"},
			{Key: "port", Label: "Port", Type: "string", Default: defaultPort},
			{Key: "database", Label: "Database", Required: true, Type: "string"},
			{Key: "user", Label: "Username", Required: true, Type: "string"},
			{Key: "dataset", Label: "Schema", Required: true, Type: "string", Default: defaultSchema, Description: "SQL Server schema to explore (commonly 'dbo')."},
			{Key: "encrypt", Label: "Encrypt", Type: "string", Default: "true", Description: "Encrypt TDS connection: true, false, or strict."},
			{Key: "trust_server_certificate", Label: "Trust Server Certificate", Type: "string", Default: "false", Description: "Skip TLS cert validation: true or false. Leave false in production."},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: "password", Name: "SQL Login (Username / Password)",
				Description: "SQL Server authentication with a username and password.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Password", Required: true, Type: "credential"},
				},
			},
			{
				ID: "connection_string", Name: "Connection String",
				Description: "Full SQL Server connection string (sqlserver:// URL format) for advanced configurations.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Connection String", Required: true, Type: "credential", Placeholder: "sqlserver://user:pass@host:1433?database=db"}, //nolint:gosec // G101: example placeholder, not a real credential
				},
			},
		},
		DefaultPricing: &gowarehouse.WarehousePricing{
			CostModel: "per_hour",
		},
	})
}

// factory creates an MSSQLProvider from the registry configuration.
func factory(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
	schema := cfg["dataset"]
	if schema == "" {
		schema = defaultSchema
	}
	if !validIdentifier(schema) {
		return nil, fmt.Errorf("mssql: invalid schema name %q", schema)
	}

	timeoutMin, _ := strconv.Atoi(cfg["timeout_minutes"])
	if timeoutMin == 0 {
		timeoutMin = defaultTimeout
	}

	var dsn string
	switch cfg["auth_method"] {
	case "connection_string":
		dsn = cfg["credentials_json"]
		if dsn == "" {
			return nil, fmt.Errorf("mssql: connection string is required")
		}
	case "password", "":
		host := cfg["host"]
		if host == "" {
			return nil, fmt.Errorf("mssql: host is required")
		}
		database := cfg["database"]
		if database == "" {
			return nil, fmt.Errorf("mssql: database is required")
		}
		user := cfg["user"]
		if user == "" {
			return nil, fmt.Errorf("mssql: user is required")
		}

		port := cfg["port"]
		if port == "" {
			port = defaultPort
		}
		if _, err := strconv.Atoi(port); err != nil {
			return nil, fmt.Errorf("mssql: invalid port %q", port)
		}

		encrypt := strings.ToLower(cfg["encrypt"])
		if encrypt == "" {
			encrypt = "true"
		}
		switch encrypt {
		case "true", "false", "strict", "disable":
			// valid encrypt values accepted by go-mssqldb
		default:
			return nil, fmt.Errorf("mssql: invalid encrypt %q (must be true, false, strict, or disable)", encrypt)
		}

		trust := strings.ToLower(cfg["trust_server_certificate"])
		if trust == "" {
			trust = "false"
		}
		switch trust {
		case "true", "false":
		default:
			return nil, fmt.Errorf("mssql: invalid trust_server_certificate %q (must be true or false)", trust)
		}

		password := cfg["credentials_json"]
		if password == "" {
			return nil, fmt.Errorf("mssql: password is required")
		}

		dsn = buildSQLServerURL(host, port, user, password, database, encrypt, trust)
	default:
		return nil, fmt.Errorf("mssql: unsupported auth method %q", cfg["auth_method"])
	}

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, fmt.Errorf("mssql: failed to open connection: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(10 * time.Minute)

	return &MSSQLProvider{
		client:  db,
		schema:  schema,
		timeout: time.Duration(timeoutMin) * time.Minute,
	}, nil
}

// MSSQLProvider implements warehouse.Provider using the microsoft/go-mssqldb driver.
type MSSQLProvider struct {
	client  msClient
	schema  string
	timeout time.Duration
}

func (p *MSSQLProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	rows, err := p.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("mssql: query failed: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("mssql: failed to get columns: %w", err)
	}

	// Column type info is used to route DECIMAL/NUMERIC/MONEY []byte values
	// through convertStringByType so they become float64 instead of []byte.
	colTypes, _ := rows.ColumnTypes()

	var result []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("mssql: row scan failed: %w", err)
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			var dbType string
			if colTypes != nil && i < len(colTypes) {
				dbType = colTypes[i].DatabaseTypeName()
			}
			row[col] = normalizeValue(values[i], dbType)
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mssql: row iteration error: %w", err)
	}

	return &gowarehouse.QueryResult{
		Columns: columns,
		Rows:    result,
	}, nil
}

func (p *MSSQLProvider) ListTables(ctx context.Context) ([]string, error) {
	return p.ListTablesInDataset(ctx, p.schema)
}

func (p *MSSQLProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	if dataset == "" {
		dataset = p.schema
	}

	rows, err := p.client.QueryContext(ctx,
		"SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = @p1 AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME",
		dataset,
	)
	if err != nil {
		return nil, fmt.Errorf("mssql: list tables failed: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("mssql: scan table name failed: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func (p *MSSQLProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	return p.GetTableSchemaInDataset(ctx, p.schema, table)
}

func (p *MSSQLProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	if dataset == "" {
		dataset = p.schema
	}

	rows, err := p.client.QueryContext(ctx,
		"SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2 ORDER BY ORDINAL_POSITION",
		dataset, table,
	)
	if err != nil {
		return nil, fmt.Errorf("mssql: get table schema failed: %w", err)
	}
	defer rows.Close()

	schema := &gowarehouse.TableSchema{Name: table}
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, fmt.Errorf("mssql: scan column schema failed: %w", err)
		}
		schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
			Name:     name,
			Type:     normalizeMSSQLType(dataType),
			Nullable: nullable == "YES",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mssql: column iteration error: %w", err)
	}

	// Row count: try sys.dm_db_partition_stats first (accurate, fast,
	// no table scan). If that errors / returns no rows we fall back to
	// sys.partitions, which is a catalog view reachable by any account
	// with SELECT on the table — ERP service accounts without VIEW
	// DATABASE STATE would otherwise stay at row_count=0 across the
	// entire warehouse.
	//
	// Short timeout guards against callers that pass context.Background()
	// — the lookup should return in milliseconds and we don't want it
	// to block GetTableSchema if permissions are wrong or the DMV is
	// contended.
	countCtx, countCancel := context.WithTimeout(ctx, 10*time.Second)
	defer countCancel()
	if got, ok := mssqlCountViaStats(countCtx, p.client, dataset, table); ok {
		schema.RowCount = got
	} else if got, ok := mssqlCountViaPartitions(countCtx, p.client, dataset, table); ok {
		schema.RowCount = got
	}

	return schema, nil
}

// mssqlCountViaStats reads sys.dm_db_partition_stats. Returns (count,
// true) on success; (0, false) when the DMV is blocked by permissions
// or the query returns no rows.
func mssqlCountViaStats(ctx context.Context, c msClient, schemaName, table string) (int64, bool) {
	rows, err := c.QueryContext(ctx, `
		SELECT SUM(ps.row_count)
		FROM sys.dm_db_partition_stats ps
		JOIN sys.tables t ON t.object_id = ps.object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE s.name = @p1 AND t.name = @p2 AND ps.index_id IN (0, 1)
	`, schemaName, table)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false
	}
	var n sql.NullInt64
	if err := rows.Scan(&n); err != nil || !n.Valid || n.Int64 < 0 {
		return 0, false
	}
	return n.Int64, true
}

// mssqlCountViaPartitions reads sys.partitions — the catalog-view
// fallback for accounts without VIEW DATABASE STATE. Same row-count
// semantics (heap/clustered rows), slightly less accurate under heavy
// concurrent DML but fine for indexing.
func mssqlCountViaPartitions(ctx context.Context, c msClient, schemaName, table string) (int64, bool) {
	rows, err := c.QueryContext(ctx, `
		SELECT SUM(p.rows)
		FROM sys.partitions p
		JOIN sys.tables t ON t.object_id = p.object_id
		JOIN sys.schemas s ON s.schema_id = t.schema_id
		WHERE s.name = @p1 AND t.name = @p2 AND p.index_id IN (0, 1)
	`, schemaName, table)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, false
	}
	var n sql.NullInt64
	if err := rows.Scan(&n); err != nil || !n.Valid || n.Int64 < 0 {
		return 0, false
	}
	return n.Int64, true
}

func (p *MSSQLProvider) GetDataset() string {
	return p.schema
}

func (p *MSSQLProvider) SQLDialect() string {
	return "Microsoft SQL Server T-SQL (ANSI SQL with extensions: TOP, OFFSET/FETCH, CTEs, window functions, PIVOT/UNPIVOT, MERGE, CROSS APPLY, OUTER APPLY, XML/JSON functions)"
}

// SampleQuery builds a T-SQL "sample N rows" query: `SELECT TOP N * FROM
// [schema].[table] <filter>`. T-SQL does not support LIMIT — TOP N must
// follow SELECT. Square brackets quote identifiers so names with reserved
// words, leading underscores, or dots still parse.
// `filterClause` is either empty or a full `WHERE ...` fragment; it goes
// after the table reference.
func (p *MSSQLProvider) SampleQuery(dataset, table, filterClause string, limit int) string {
	return fmt.Sprintf("SELECT TOP %d * FROM [%s].[%s] %s", limit, dataset, table, filterClause)
}

func (p *MSSQLProvider) SQLFixPrompt() string {
	return sqlFixPrompt
}

func (p *MSSQLProvider) ValidateReadOnly(ctx context.Context) error {
	_, err := p.client.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("mssql: read-only validation failed: %w", err)
	}
	return nil
}

func (p *MSSQLProvider) HealthCheck(ctx context.Context) error {
	return p.client.PingContext(ctx)
}

func (p *MSSQLProvider) Close() error {
	return p.client.Close()
}

// normalizeValue converts database/sql driver values to standard Go types.
// The microsoft/go-mssqldb driver returns:
//   - TINYINT/SMALLINT/INT/BIGINT → int64 (signed 64-bit, all integer types)
//   - REAL → float32, FLOAT → float64
//   - DECIMAL/NUMERIC → []byte (string representation)
//   - MONEY/SMALLMONEY → []byte (string representation)
//   - BIT → bool
//   - DATE/DATETIME/DATETIME2/SMALLDATETIME/DATETIMEOFFSET → time.Time
//   - TIME → time.Time (date portion zero)
//   - VARCHAR/NVARCHAR/CHAR/NCHAR/TEXT/NTEXT/XML → string
//   - BINARY/VARBINARY/IMAGE → []byte
//   - UNIQUEIDENTIFIER → []byte (16 raw bytes, needs formatting)
func normalizeValue(v interface{}, dbType string) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return convertBytesByType(val, dbType)
	case time.Time:
		return val.Format(time.RFC3339)
	case float32:
		return float64(val)
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	default:
		return val
	}
}

// convertBytesByType converts []byte values from go-mssqldb to the right Go
// type based on the SQL Server column type. The driver returns numeric
// columns (DECIMAL, NUMERIC, MONEY, SMALLMONEY) as []byte string values,
// UNIQUEIDENTIFIER as 16 raw bytes, and text-like columns as []byte strings.
func convertBytesByType(val []byte, dbType string) interface{} {
	upper := strings.ToUpper(dbType)
	switch upper {
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		// Always return float64 for decimal-family types so types are
		// consistent across rows. The schema maps these to FLOAT64.
		if f, err := strconv.ParseFloat(string(val), 64); err == nil {
			return f
		}
		return string(val)
	case "UNIQUEIDENTIFIER":
		// go-mssqldb returns GUID as 16 raw bytes with SQL Server's
		// little-endian layout for the first three groups. Format to the
		// canonical 8-4-4-4-12 hex representation for user-facing output.
		if len(val) == 16 {
			return formatGUID(val)
		}
		return string(val)
	case "BINARY", "VARBINARY", "IMAGE":
		// Keep binary as string (hex-ish). Consumers rarely need raw bytes;
		// returning []byte through the warehouse result interface would be
		// inconsistent with every other STRING-family column.
		return string(val)
	default:
		// Text types (VARCHAR/NVARCHAR/CHAR/NCHAR/TEXT/NTEXT/XML) and
		// anything else we did not recognise — convert to string.
		return string(val)
	}
}

// formatGUID formats a SQL Server UNIQUEIDENTIFIER (16 raw bytes, little-endian
// for the first three groups) into the canonical 8-4-4-4-12 hex form.
func formatGUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	return fmt.Sprintf(
		"%02X%02X%02X%02X-%02X%02X-%02X%02X-%02X%02X-%02X%02X%02X%02X%02X%02X",
		b[3], b[2], b[1], b[0], // group 1: little-endian
		b[5], b[4], // group 2: little-endian
		b[7], b[6], // group 3: little-endian
		b[8], b[9], // group 4: big-endian
		b[10], b[11], b[12], b[13], b[14], b[15], // group 5: big-endian
	)
}

// normalizeMSSQLType maps SQL Server data types to warehouse-agnostic types.
// Input is the DATA_TYPE value from INFORMATION_SCHEMA.COLUMNS (e.g. "int",
// "nvarchar", "datetime2", "decimal").
func normalizeMSSQLType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "tinyint", "smallint", "int", "integer", "bigint":
		return "INT64"
	case "real", "float":
		return "FLOAT64"
	case "decimal", "numeric", "money", "smallmoney":
		return "FLOAT64"
	case "bit":
		return "BOOL"
	case "date":
		return "DATE"
	case "datetime", "datetime2", "smalldatetime", "datetimeoffset":
		return "TIMESTAMP"
	case "binary", "varbinary", "image":
		return "BYTES"
	case "xml", "sql_variant":
		// Represent XML and SQL_VARIANT as STRING (most consumers treat these
		// as opaque text); JSON is stored in SQL Server as NVARCHAR, not a
		// dedicated type, so no "json" case is needed.
		return "STRING"
	default:
		// char, nchar, varchar, nvarchar, text, ntext, time, uniqueidentifier,
		// hierarchyid, geography, geometry, rowversion, timestamp — all STRING.
		return "STRING"
	}
}

// validIdentifier checks that a SQL Server identifier contains only safe
// characters. Prevents SQL injection when interpolating schema/database names
// into queries. SQL Server allows more punctuation in bracketed identifiers,
// but this provider only accepts plain identifiers for schemas passed to
// ListTablesInDataset and GetTableSchemaInDataset.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func validIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}

// buildSQLServerURL constructs a sqlserver:// URL DSN with properly URL-encoded
// credentials and parameters. Using net/url guarantees that special characters
// in passwords (e.g. '@', '/', '?', ':', '&', '#', spaces) do not break the
// DSN parser. net.JoinHostPort handles IPv6 literals correctly ("[::1]:1433").
func buildSQLServerURL(host, port, user, password, database, encrypt, trust string) string {
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, port),
	}
	q := u.Query()
	q.Set("database", database)
	q.Set("encrypt", encrypt)
	q.Set("TrustServerCertificate", trust)
	u.RawQuery = q.Encode()
	return u.String()
}
