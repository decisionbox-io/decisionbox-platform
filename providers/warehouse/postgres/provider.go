// Package postgres provides a warehouse.Provider for PostgreSQL.
//
// Configuration:
//
//	WAREHOUSE_PROVIDER=postgres
//	host + port + database + user in project config
//
// Authentication: Username/password or connection string.
//   - Password auth: set password in warehouse-credentials secret
//   - Connection string auth: set full DSN in warehouse-credentials secret
//
// The provider uses the lib/pq driver (database/sql compatible)
// and queries information_schema for table/column metadata.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

//go:embed prompts/sql_fix.md
var sqlFixPrompt string

func init() {
	gowarehouse.RegisterWithMeta("postgres", func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		schema := cfg["dataset"]
		if schema == "" {
			schema = "public"
		}
		if !validIdentifier(schema) {
			return nil, fmt.Errorf("postgres: invalid schema name %q", schema)
		}

		timeoutMin, _ := strconv.Atoi(cfg["timeout_minutes"])
		if timeoutMin == 0 {
			timeoutMin = 5
		}

		var dsn string
		switch cfg["auth_method"] {
		case "connection_string":
			dsn = cfg["credentials_json"]
			if dsn == "" {
				return nil, fmt.Errorf("postgres: connection string is required")
			}
		case "password", "":
			host := cfg["host"]
			if host == "" {
				return nil, fmt.Errorf("postgres: host is required")
			}
			database := cfg["database"]
			if database == "" {
				return nil, fmt.Errorf("postgres: database is required")
			}
			user := cfg["user"]
			if user == "" {
				return nil, fmt.Errorf("postgres: user is required")
			}

			port := cfg["port"]
			if port == "" {
				port = "5432"
			}
			if _, err := strconv.Atoi(port); err != nil {
				return nil, fmt.Errorf("postgres: invalid port %q", port)
			}

			sslmode := cfg["sslmode"]
			if sslmode == "" {
				sslmode = "require"
			}
			switch sslmode {
			case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
				// valid libpq SSL modes
			default:
				return nil, fmt.Errorf("postgres: invalid sslmode %q (must be disable, allow, prefer, require, verify-ca, or verify-full)", sslmode)
			}

			password := cfg["credentials_json"]
			if password == "" {
				return nil, fmt.Errorf("postgres: password is required")
			}

			// Build DSN with proper quoting. lib/pq uses libpq key=value format
			// where values containing spaces or quotes must be single-quoted,
			// with backslash escaping for single quotes and backslashes.
			dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s search_path=%s",
				quoteDSNValue(host), port, quoteDSNValue(user), quoteDSNValue(password),
				quoteDSNValue(database), sslmode, schema)
		default:
			return nil, fmt.Errorf("postgres: unsupported auth method %q", cfg["auth_method"])
		}

		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, fmt.Errorf("postgres: failed to open connection: %w", err)
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(10 * time.Minute)

		return &PostgresProvider{
			client:  db,
			schema:  schema,
			timeout: time.Duration(timeoutMin) * time.Minute,
		}, nil
	}, gowarehouse.ProviderMeta{
		Name:        "PostgreSQL",
		Description: "PostgreSQL database",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "host", Label: "Host", Required: true, Type: "string", Placeholder: "db.example.com"},
			{Key: "port", Label: "Port", Type: "string", Default: "5432"},
			{Key: "database", Label: "Database", Required: true, Type: "string"},
			{Key: "user", Label: "Username", Required: true, Type: "string"},
			{Key: "dataset", Label: "Schema", Required: true, Type: "string", Default: "public", Description: "PostgreSQL schema to explore."},
			{Key: "sslmode", Label: "SSL Mode", Type: "string", Default: "require", Description: "disable, require, verify-ca, verify-full"},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: "password", Name: "Username / Password",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Password", Required: true, Type: "credential"},
				},
			},
			{
				ID: "connection_string", Name: "Connection String",
				Description: "Full PostgreSQL connection string for advanced configurations.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Connection String", Required: true, Type: "credential", Placeholder: "postgres://user:pass@host:5432/db?sslmode=require"}, //nolint:gosec // G101: example placeholder, not a real credential
				},
			},
		},
		DefaultPricing: &gowarehouse.WarehousePricing{
			CostModel: "per_hour",
		},
	})
}

// PostgresProvider implements warehouse.Provider using the lib/pq driver.
type PostgresProvider struct {
	client  pgClient
	schema  string
	timeout time.Duration
}

func (p *PostgresProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	rows, err := p.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres: query failed: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("postgres: failed to get columns: %w", err)
	}

	// Get column type info for type-aware value conversion.
	colTypes, _ := rows.ColumnTypes()

	var result []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("postgres: row scan failed: %w", err)
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
		return nil, fmt.Errorf("postgres: row iteration error: %w", err)
	}

	return &gowarehouse.QueryResult{
		Columns: columns,
		Rows:    result,
	}, nil
}

func (p *PostgresProvider) ListTables(ctx context.Context) ([]string, error) {
	return p.ListTablesInDataset(ctx, p.schema)
}

func (p *PostgresProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	if dataset == "" {
		dataset = p.schema
	}

	rows, err := p.client.QueryContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE' ORDER BY table_name",
		dataset,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tables failed: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("postgres: scan table name failed: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func (p *PostgresProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	return p.GetTableSchemaInDataset(ctx, p.schema, table)
}

func (p *PostgresProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	if dataset == "" {
		dataset = p.schema
	}

	rows, err := p.client.QueryContext(ctx,
		"SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position",
		dataset, table,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: get table schema failed: %w", err)
	}
	defer rows.Close()

	schema := &gowarehouse.TableSchema{Name: table}
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, fmt.Errorf("postgres: scan column schema failed: %w", err)
		}
		schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
			Name:     name,
			Type:     normalizePostgresType(dataType),
			Nullable: nullable == "YES",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: column iteration error: %w", err)
	}

	// Get approximate row count from pg_class (fast, no full scan).
	countRows, err := p.client.QueryContext(ctx,
		"SELECT reltuples::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2",
		dataset, table,
	)
	if err == nil {
		defer countRows.Close()
		if countRows.Next() {
			var rowCount int64
			if err := countRows.Scan(&rowCount); err == nil && rowCount >= 0 {
				schema.RowCount = rowCount
			}
		}
	}

	return schema, nil
}

func (p *PostgresProvider) GetDataset() string {
	return p.schema
}

func (p *PostgresProvider) SQLDialect() string {
	return "PostgreSQL (ANSI SQL with extensions: DISTINCT ON, CTEs, window functions, JSON operators, array types)"
}

func (p *PostgresProvider) SQLFixPrompt() string {
	return sqlFixPrompt
}

func (p *PostgresProvider) ValidateReadOnly(ctx context.Context) error {
	_, err := p.client.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("postgres: read-only validation failed: %w", err)
	}
	return nil
}

func (p *PostgresProvider) HealthCheck(ctx context.Context) error {
	return p.client.PingContext(ctx)
}

func (p *PostgresProvider) Close() error {
	return p.client.Close()
}

// normalizeValue converts database/sql driver values to standard Go types.
// The lib/pq driver returns Go-native types for most PostgreSQL types,
// but NUMERIC/DECIMAL columns are returned as []byte (string representation).
// The dbType parameter (from ColumnTypes().DatabaseTypeName()) is used to
// convert these to proper numeric Go types.
func normalizeValue(v interface{}, dbType string) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return convertStringByType(string(val), dbType)
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return val
	}
}

// convertStringByType converts string values to numeric types based on the
// PostgreSQL column type. The lib/pq driver returns NUMERIC and DECIMAL
// columns as []byte (string representation).
func convertStringByType(val string, dbType string) interface{} {
	switch strings.ToUpper(dbType) {
	case "NUMERIC", "DECIMAL":
		// Always return float64 for NUMERIC/DECIMAL to ensure consistent
		// types across rows. The schema maps these to FLOAT64.
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	default:
		return val
	}
}

// normalizePostgresType maps PostgreSQL data types to warehouse-agnostic types.
func normalizePostgresType(t string) string {
	t = strings.ToLower(t)
	switch {
	case t == "integer" || t == "int" || t == "int4" || t == "bigint" || t == "int8" ||
		t == "smallint" || t == "int2" || t == "serial" || t == "bigserial" || t == "smallserial":
		return "INT64"
	case t == "real" || t == "float4" || t == "double precision" || t == "float8" ||
		strings.HasPrefix(t, "numeric") || strings.HasPrefix(t, "decimal"):
		return "FLOAT64"
	case t == "boolean" || t == "bool":
		return "BOOL"
	case t == "date":
		return "DATE"
	case strings.HasPrefix(t, "timestamp"):
		return "TIMESTAMP"
	case t == "bytea":
		return "BYTES"
	case t == "json" || t == "jsonb":
		return "RECORD"
	default:
		// varchar, character varying, char, text, uuid, inet, cidr,
		// macaddr, money, interval, time, point, etc.
		return "STRING"
	}
}

// validIdentifier checks that a PostgreSQL identifier contains only safe characters.
// Prevents SQL injection when interpolating database/schema names into queries.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func validIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}

// quoteDSNValue wraps a value in single quotes for the libpq key=value DSN format.
// Single quotes and backslashes within the value are escaped with a backslash.
func quoteDSNValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}
