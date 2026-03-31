// Package databricks provides a warehouse.Provider for Databricks SQL.
//
// Configuration:
//
//	WAREHOUSE_PROVIDER=databricks
//	host + http_path + catalog in project config
//
// Authentication: Personal Access Token or OAuth M2M (service principal).
//   - PAT auth: set token in warehouse-credentials secret
//   - OAuth M2M auth: set client_id:client_secret in warehouse-credentials secret
//
// The provider uses the databricks-sql-go driver (database/sql compatible)
// and queries information_schema for table/column metadata.
// Unity Catalog uses a 3-level namespace: catalog.schema.table.
package databricks

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	dbsql "github.com/databricks/databricks-sql-go"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

//go:embed prompts/sql_fix.md
var sqlFixPrompt string

func init() {
	gowarehouse.RegisterWithMeta("databricks", func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		host := cfg["host"]
		if host == "" {
			return nil, fmt.Errorf("databricks: host is required")
		}
		httpPath := cfg["http_path"]
		if httpPath == "" {
			return nil, fmt.Errorf("databricks: http_path is required")
		}
		catalog := cfg["catalog"]
		if catalog == "" {
			return nil, fmt.Errorf("databricks: catalog is required")
		}
		if !validIdentifier(catalog) {
			return nil, fmt.Errorf("databricks: invalid catalog name %q", catalog)
		}

		schema := cfg["dataset"]
		if schema == "" {
			schema = "default"
		}
		if !validIdentifier(schema) {
			return nil, fmt.Errorf("databricks: invalid schema name %q", schema)
		}

		portNum := 443
		if p := cfg["port"]; p != "" {
			var err error
			portNum, err = strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("databricks: invalid port %q", p)
			}
		}

		timeoutMin, _ := strconv.Atoi(cfg["timeout_minutes"])
		if timeoutMin == 0 {
			timeoutMin = 5
		}

		creds := cfg["credentials_json"]
		if creds == "" {
			return nil, fmt.Errorf("databricks: credentials are required")
		}

		var opts []dbsql.ConnOption
		opts = append(opts,
			dbsql.WithServerHostname(host),
			dbsql.WithPort(portNum),
			dbsql.WithHTTPPath(httpPath),
			dbsql.WithInitialNamespace(catalog, schema),
			dbsql.WithTimeout(time.Duration(timeoutMin)*time.Minute),
		)

		switch cfg["auth_method"] {
		case "oauth_m2m":
			parts := strings.SplitN(creds, ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return nil, fmt.Errorf("databricks: OAuth M2M credentials must be in client_id:client_secret format")
			}
			opts = append(opts, dbsql.WithClientCredentials(parts[0], parts[1]))
		case "pat", "":
			opts = append(opts, dbsql.WithAccessToken(creds))
		default:
			return nil, fmt.Errorf("databricks: unsupported auth method %q", cfg["auth_method"])
		}

		connector, err := dbsql.NewConnector(opts...)
		if err != nil {
			return nil, fmt.Errorf("databricks: failed to create connector: %w", err)
		}

		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(10 * time.Minute)

		return &DatabricksProvider{
			client:  db,
			catalog: catalog,
			schema:  schema,
			timeout: time.Duration(timeoutMin) * time.Minute,
		}, nil
	}, gowarehouse.ProviderMeta{
		Name:        "Databricks",
		Description: "Databricks SQL warehouse (Unity Catalog)",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "host", Label: "Server Hostname", Required: true, Type: "string", Placeholder: "xxx.cloud.databricks.com"},
			{Key: "http_path", Label: "HTTP Path", Required: true, Type: "string", Placeholder: "/sql/1.0/warehouses/xxx"},
			{Key: "catalog", Label: "Catalog", Required: true, Type: "string", Placeholder: "main"},
			{Key: "dataset", Label: "Schema", Required: true, Type: "string", Default: "default", Description: "Databricks schema to explore."},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: "pat", Name: "Personal Access Token",
				Description: "Databricks personal access token for authentication.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Access Token", Required: true, Type: "credential", Placeholder: "dapi..."},
				},
			},
			{
				ID: "oauth_m2m", Name: "OAuth M2M (Service Principal)",
				Description: "OAuth machine-to-machine authentication using a Databricks service principal.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Client ID : Client Secret", Required: true, Type: "credential", Placeholder: "client-id:client-secret"},
				},
			},
		},
		DefaultPricing: &gowarehouse.WarehousePricing{
			CostModel: "per_hour",
		},
	})
}

// DatabricksProvider implements warehouse.Provider using the databricks-sql-go driver.
type DatabricksProvider struct {
	client  dbClient
	catalog string
	schema  string
	timeout time.Duration
}

func (p *DatabricksProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	rows, err := p.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("databricks: query failed: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("databricks: failed to get columns: %w", err)
	}

	colTypes, _ := rows.ColumnTypes()

	var result []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}

		if err := rows.Scan(pointers...); err != nil {
			return nil, fmt.Errorf("databricks: row scan failed: %w", err)
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
		return nil, fmt.Errorf("databricks: row iteration error: %w", err)
	}

	return &gowarehouse.QueryResult{
		Columns: columns,
		Rows:    result,
	}, nil
}

func (p *DatabricksProvider) ListTables(ctx context.Context) ([]string, error) {
	return p.ListTablesInDataset(ctx, p.schema)
}

func (p *DatabricksProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	if dataset == "" {
		dataset = p.schema
	}
	if !validIdentifier(dataset) {
		return nil, fmt.Errorf("databricks: invalid schema name %q", dataset)
	}

	// Use string formatting with single-quoted values instead of ? params.
	// The Databricks SQL driver has issues with positional ? parameters in
	// information_schema queries. Schema and table names are validated by
	// validIdentifier() above to prevent SQL injection.
	query := fmt.Sprintf(
		"SELECT table_name FROM %s.information_schema.tables WHERE table_schema = '%s' AND table_type IN ('MANAGED', 'EXTERNAL') ORDER BY table_name",
		p.catalog, dataset,
	)

	rows, err := p.client.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("databricks: list tables failed: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("databricks: scan table name failed: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func (p *DatabricksProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	return p.GetTableSchemaInDataset(ctx, p.schema, table)
}

func (p *DatabricksProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	if dataset == "" {
		dataset = p.schema
	}
	if !validIdentifier(dataset) {
		return nil, fmt.Errorf("databricks: invalid schema name %q", dataset)
	}
	if !validIdentifier(table) {
		return nil, fmt.Errorf("databricks: invalid table name %q", table)
	}

	// Use string formatting with single-quoted values instead of ? params.
	// Schema and table names are validated by validIdentifier() above to
	// prevent SQL injection. The Databricks SQL driver has issues with
	// multiple positional ? parameters.
	colQuery := fmt.Sprintf(
		"SELECT column_name, data_type, is_nullable FROM %s.information_schema.columns WHERE table_schema = '%s' AND table_name = '%s' ORDER BY ordinal_position",
		p.catalog, dataset, table,
	)

	rows, err := p.client.QueryContext(ctx, colQuery)
	if err != nil {
		return nil, fmt.Errorf("databricks: get table schema failed: %w", err)
	}
	defer rows.Close()

	schema := &gowarehouse.TableSchema{Name: table}
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return nil, fmt.Errorf("databricks: scan column schema failed: %w", err)
		}
		schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
			Name:     name,
			Type:     normalizeDatabricksType(dataType),
			Nullable: nullable == "YES",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("databricks: column iteration error: %w", err)
	}

	// Row count: Databricks DESCRIBE DETAIL returns numFiles and sizeInBytes
	// but not numRecords. Row count is left at 0 (the agent does not depend
	// on exact row counts for discovery).

	return schema, nil
}

func (p *DatabricksProvider) GetDataset() string {
	return p.schema
}

func (p *DatabricksProvider) SQLDialect() string {
	return "Databricks SQL (ANSI SQL with extensions: QUALIFY, PIVOT, UNPIVOT, LATERAL VIEW, Delta time travel, STRUCT/ARRAY/MAP types)"
}

func (p *DatabricksProvider) SQLFixPrompt() string {
	return sqlFixPrompt
}

func (p *DatabricksProvider) ValidateReadOnly(ctx context.Context) error {
	_, err := p.client.QueryContext(ctx, "SELECT 1")
	if err != nil {
		return fmt.Errorf("databricks: read-only validation failed: %w", err)
	}
	return nil
}

func (p *DatabricksProvider) HealthCheck(ctx context.Context) error {
	return p.client.PingContext(ctx)
}

func (p *DatabricksProvider) Close() error {
	return p.client.Close()
}

// normalizeValue converts database/sql driver values to standard Go types.
// The databricks-sql-go driver returns:
//   - TINYINT→int8, SMALLINT→int16, INT→int32, BIGINT→int64
//   - FLOAT→float32, DOUBLE→float64
//   - DECIMAL→string (despite docs saying sql.RawBytes)
//   - STRING→string, BOOLEAN→bool
//   - DATE/TIMESTAMP→time.Time
//   - BINARY/ARRAY/MAP/STRUCT→[]byte (sql.RawBytes)
func normalizeValue(v interface{}, dbType string) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []byte:
		return convertStringByType(string(val), dbType)
	case string:
		return convertStringByType(val, dbType)
	case time.Time:
		return val.Format(time.RFC3339)
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	case float32:
		return float64(val)
	default:
		return val
	}
}

// convertStringByType converts string values to numeric types based on the
// Databricks column type. The driver returns DECIMAL columns as string.
// Both []byte and string values are routed through this function.
func convertStringByType(val string, dbType string) interface{} {
	switch strings.ToUpper(dbType) {
	case "DECIMAL":
		// Always return float64 for DECIMAL to ensure consistent types
		// across rows. The schema maps DECIMAL to FLOAT64.
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	default:
		return val
	}
}

// normalizeDatabricksType maps Databricks SQL data types to warehouse-agnostic types.
func normalizeDatabricksType(t string) string {
	t = strings.ToLower(t)
	switch {
	case t == "tinyint" || t == "smallint" || t == "int" || t == "integer" || t == "bigint":
		return "INT64"
	case t == "float" || t == "double" || strings.HasPrefix(t, "decimal"):
		return "FLOAT64"
	case t == "boolean":
		return "BOOL"
	case t == "date":
		return "DATE"
	case strings.HasPrefix(t, "timestamp"):
		return "TIMESTAMP"
	case t == "binary":
		return "BYTES"
	case strings.HasPrefix(t, "array") || strings.HasPrefix(t, "map") ||
		strings.HasPrefix(t, "struct") || t == "variant" || t == "object":
		return "RECORD"
	default:
		// string, char, varchar, interval, void, etc.
		return "STRING"
	}
}

// validIdentifier checks that a Databricks identifier contains only safe characters.
// This function is the SQL injection boundary for all string-formatted queries.
// The databricks-sql-go driver does not support multiple positional ? parameters
// reliably, so ListTablesInDataset and GetTableSchemaInDataset use fmt.Sprintf
// with single-quoted values. Every user-provided identifier MUST be validated
// by this function before interpolation. Do not use fmt.Sprintf with user-provided
// values in SQL without calling validIdentifier first.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func validIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}
