// Package snowflake provides a warehouse.Provider for Snowflake.
//
// Configuration:
//
//	WAREHOUSE_PROVIDER=snowflake
//	account + user + warehouse + database in project config
//
// Authentication: Username/password or key pair (JWT).
//   - Password auth: set password in warehouse-credentials secret
//   - Key pair auth: set PEM-encoded private key in warehouse-credentials secret
//
// The provider uses the gosnowflake driver (database/sql compatible)
// and queries INFORMATION_SCHEMA for table/column metadata.
package snowflake

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	_ "embed"
	"encoding/pem"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sf "github.com/snowflakedb/gosnowflake"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

//go:embed prompts/sql_fix.md
var sqlFixPrompt string

func init() {
	gowarehouse.RegisterWithMeta("snowflake", func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		// Credentials: password or PEM private key, stored via secret provider.
		creds := cfg["credentials_json"]
		if creds == "" {
			creds = cfg["password"]
		}

		account := cfg["account"]
		if account == "" {
			return nil, fmt.Errorf("snowflake: account is required")
		}
		user := cfg["user"]
		if user == "" {
			return nil, fmt.Errorf("snowflake: user is required")
		}
		warehouse := cfg["warehouse"]
		if warehouse == "" {
			return nil, fmt.Errorf("snowflake: warehouse is required")
		}
		database := cfg["database"]
		if database == "" {
			return nil, fmt.Errorf("snowflake: database is required")
		}
		if !validIdentifier(database) {
			return nil, fmt.Errorf("snowflake: invalid database name %q", database)
		}

		schema := cfg["dataset"]
		if schema == "" {
			schema = "PUBLIC"
		}
		if !validIdentifier(schema) {
			return nil, fmt.Errorf("snowflake: invalid schema name %q", schema)
		}

		timeoutMin, _ := strconv.Atoi(cfg["timeout_minutes"])
		if timeoutMin == 0 {
			timeoutMin = 5
		}

		sfConfig := &sf.Config{
			Account:                   account,
			User:                      user,
			Database:                  database,
			Schema:                    schema,
			Warehouse:                 warehouse,
			Role:                      cfg["role"],
			LoginTimeout:              30 * time.Second,
			RequestTimeout:            time.Duration(timeoutMin) * time.Minute,
			ValidateDefaultParameters: sf.ConfigBoolFalse,
		}

		// Apply authentication based on selected method.
		authMethod := cfg["auth_method"]
		switch authMethod {
		case "key_pair":
			if creds == "" {
				return nil, fmt.Errorf("snowflake: PEM private key is required for key pair auth")
			}
			key, err := parsePrivateKey(creds)
			if err != nil {
				return nil, fmt.Errorf("snowflake: failed to parse private key: %w", err)
			}
			sfConfig.Authenticator = sf.AuthTypeJwt
			sfConfig.PrivateKey = key
		case "password", "":
			if creds == "" {
				return nil, fmt.Errorf("snowflake: password is required")
			}
			sfConfig.Password = creds
		default:
			return nil, fmt.Errorf("snowflake: unsupported auth method %q", authMethod)
		}

		connector := sf.NewConnector(sf.SnowflakeDriver{}, *sfConfig)
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(10 * time.Minute)

		return &SnowflakeProvider{
			client:   db,
			database: database,
			schema:   schema,
			timeout:  time.Duration(timeoutMin) * time.Minute,
		}, nil
	}, gowarehouse.ProviderMeta{
		Name:        "Snowflake",
		Description: "Snowflake cloud data warehouse",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "account", Label: "Account Identifier", Required: true, Type: "string", Placeholder: "org-account"},
			{Key: "user", Label: "Username", Required: true, Type: "string"},
			{Key: "warehouse", Label: "Warehouse", Required: true, Type: "string", Placeholder: "COMPUTE_WH"},
			{Key: "database", Label: "Database", Required: true, Type: "string"},
			{Key: "dataset", Label: "Schema", Required: true, Type: "string", Default: "PUBLIC", Description: "Snowflake schema to explore."},
			{Key: "role", Label: "Role", Type: "string", Placeholder: "ANALYST_ROLE", Description: "Snowflake role to use for the session."},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: "password", Name: "Username / Password",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Password", Required: true, Type: "credential"},
				},
			},
			{
				ID: "key_pair", Name: "Key Pair (JWT)",
				Description: "RSA private key authentication — recommended for production.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "PEM Private Key", Required: true, Type: "credential", Placeholder: "-----BEGIN PRIVATE KEY-----"},
				},
			},
		},
		DefaultPricing: &gowarehouse.WarehousePricing{
			CostModel:           "per_second",
			CostPerTBScannedUSD: 0, // Snowflake pricing is credit-based per warehouse size and time
		},
	})
}

// SnowflakeProvider implements warehouse.Provider using the gosnowflake driver.
type SnowflakeProvider struct {
	client   sfClient
	database string
	schema   string
	timeout  time.Duration
}

func (p *SnowflakeProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	rows, err := p.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("snowflake: query failed: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("snowflake: failed to get columns: %w", err)
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
			return nil, fmt.Errorf("snowflake: row scan failed: %w", err)
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
		return nil, fmt.Errorf("snowflake: row iteration error: %w", err)
	}

	return &gowarehouse.QueryResult{
		Columns: columns,
		Rows:    result,
	}, nil
}

func (p *SnowflakeProvider) ListTables(ctx context.Context) ([]string, error) {
	return p.ListTablesInDataset(ctx, p.schema)
}

func (p *SnowflakeProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	if dataset == "" {
		dataset = p.schema
	}

	query := fmt.Sprintf(
		"SELECT TABLE_NAME FROM %s.INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME",
		p.database,
	)

	rows, err := p.client.QueryContext(ctx, query, strings.ToUpper(dataset))
	if err != nil {
		return nil, fmt.Errorf("snowflake: list tables failed: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("snowflake: scan table name failed: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func (p *SnowflakeProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	return p.GetTableSchemaInDataset(ctx, p.schema, table)
}

func (p *SnowflakeProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	if dataset == "" {
		dataset = p.schema
	}

	// Get columns
	colQuery := fmt.Sprintf(
		"SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM %s.INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION",
		p.database,
	)

	rows, err := p.client.QueryContext(ctx, colQuery, strings.ToUpper(dataset), strings.ToUpper(table))
	if err != nil {
		return nil, fmt.Errorf("snowflake: get table schema failed: %w", err)
	}
	defer rows.Close()

	schema := &gowarehouse.TableSchema{Name: table}
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, fmt.Errorf("snowflake: scan column schema failed: %w", err)
		}
		schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
			Name:     name,
			Type:     normalizeSnowflakeType(dataType),
			Nullable: nullable == "YES",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snowflake: column iteration error: %w", err)
	}

	// Get row count from INFORMATION_SCHEMA (no full scan)
	countQuery := fmt.Sprintf(
		"SELECT ROW_COUNT FROM %s.INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?",
		p.database,
	)
	countRows, err := p.client.QueryContext(ctx, countQuery, strings.ToUpper(dataset), strings.ToUpper(table))
	if err == nil {
		defer countRows.Close()
		if countRows.Next() {
			var rowCount int64
			if err := countRows.Scan(&rowCount); err == nil {
				schema.RowCount = rowCount
			}
		}
	}

	return schema, nil
}

func (p *SnowflakeProvider) GetDataset() string {
	return p.schema
}

func (p *SnowflakeProvider) SQLDialect() string {
	return "Snowflake SQL (ANSI-based with extensions: QUALIFY, FLATTEN, VARIANT, ILIKE, LATERAL)"
}

func (p *SnowflakeProvider) SQLFixPrompt() string {
	return sqlFixPrompt
}

func (p *SnowflakeProvider) ValidateReadOnly(ctx context.Context) error {
	// Snowflake roles control write access. We verify connectivity works.
	_, err := p.client.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("snowflake: read-only validation failed: %w", err)
	}
	return nil
}

func (p *SnowflakeProvider) HealthCheck(ctx context.Context) error {
	return p.client.PingContext(ctx)
}

func (p *SnowflakeProvider) Close() error {
	return p.client.Close()
}

// parsePrivateKey decodes a PEM-encoded PKCS8 or PKCS1 RSA private key.
func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	// Try PKCS8 first (standard format for Snowflake key pair auth)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}

	// Fall back to PKCS1
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// normalizeValue converts database/sql driver values to standard Go types.
// The gosnowflake driver returns NUMBER/DECIMAL as strings, so we use the
// database type name to convert them to int64 or float64.
func normalizeValue(v interface{}, dbType string) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return string(val)
	case time.Time:
		return val.Format(time.RFC3339)
	case string:
		return convertStringByType(val, dbType)
	default:
		return val
	}
}

// convertStringByType converts string values to numeric types based on the
// Snowflake column type. The gosnowflake driver returns NUMBER, DECIMAL,
// and FIXED columns as strings.
func convertStringByType(val string, dbType string) interface{} {
	switch strings.ToUpper(dbType) {
	case "FIXED", "NUMBER", "DECIMAL", "NUMERIC":
		// Try int64 first (whole numbers), fall back to float64 (decimals)
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	case "REAL", "FLOAT", "DOUBLE":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	default:
		return val
	}
}

// normalizeSnowflakeType maps Snowflake data types to warehouse-agnostic types.
func normalizeSnowflakeType(t string) string {
	t = strings.ToUpper(t)
	switch {
	case t == "NUMBER" || t == "INT" || t == "INTEGER" || t == "BIGINT" || t == "SMALLINT" || t == "TINYINT" || t == "BYTEINT":
		return "INT64"
	case t == "FLOAT" || t == "FLOAT4" || t == "FLOAT8" || t == "DOUBLE" || t == "DOUBLE PRECISION" || t == "REAL" || strings.HasPrefix(t, "DECIMAL") || strings.HasPrefix(t, "NUMERIC"):
		return "FLOAT64"
	case t == "BOOLEAN":
		return "BOOL"
	case t == "DATE":
		return "DATE"
	case strings.HasPrefix(t, "TIMESTAMP") || t == "DATETIME":
		return "TIMESTAMP"
	case t == "BINARY" || t == "VARBINARY":
		return "BYTES"
	case t == "VARIANT" || t == "OBJECT" || t == "ARRAY":
		return "RECORD"
	default:
		// VARCHAR, CHAR, STRING, TEXT, TIME, etc.
		return "STRING"
	}
}

// validIdentifier checks that a Snowflake identifier contains only safe characters.
// Prevents SQL injection when interpolating database/schema names into queries.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func validIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}
