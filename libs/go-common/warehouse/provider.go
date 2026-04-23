package warehouse

import "context"

// Provider abstracts data warehouse query operations.
// Implement this interface to add support for a new data warehouse
// (e.g., ClickHouse, Redshift, Snowflake, DuckDB).
//
// The BigQuery implementation is provided in warehouse/bigquery/.
//
// Selection via WAREHOUSE_PROVIDER env var (e.g., "bigquery").
type Provider interface {
	// Query executes a SQL query and returns results.
	Query(ctx context.Context, query string, params map[string]interface{}) (*QueryResult, error)

	// ListTables returns all table names in the configured default dataset/schema.
	ListTables(ctx context.Context) ([]string, error)

	// ListTablesInDataset returns all table names in a specific dataset/schema.
	// For providers that don't support multiple datasets, this can delegate to ListTables.
	ListTablesInDataset(ctx context.Context, dataset string) ([]string, error)

	// GetTableSchema returns schema metadata for a table in the default dataset.
	GetTableSchema(ctx context.Context, table string) (*TableSchema, error)

	// GetTableSchemaInDataset returns schema metadata for a table in a specific dataset.
	GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*TableSchema, error)

	// GetDataset returns the default dataset/schema name.
	GetDataset() string

	// SQLDialect returns the SQL dialect name for this warehouse.
	// Used by the discovery agent to give the LLM context about syntax.
	//   BigQuery: "BigQuery Standard SQL"
	//   PostgreSQL: "PostgreSQL"
	//   ClickHouse: "ClickHouse SQL"
	SQLDialect() string

	// SQLFixPrompt returns a prompt template for fixing SQL errors in this
	// warehouse's dialect. The prompt contains common error patterns and
	// syntax rules specific to this warehouse.
	//
	// Templates use placeholders: {{DATASET}}, {{FILTER}}, {{SCHEMA_INFO}},
	// {{ORIGINAL_SQL}}, {{ERROR_MESSAGE}}, {{CONVERSATION_HISTORY}}.
	//
	// Returns empty string if no warehouse-specific prompt is available.
	SQLFixPrompt() string

	// ValidateReadOnly checks that the configured credentials have
	// read-only access (no write/delete permissions). Safety check
	// to prevent accidental data modification.
	ValidateReadOnly(ctx context.Context) error

	// HealthCheck verifies the warehouse connection is alive.
	HealthCheck(ctx context.Context) error

	// Close releases warehouse resources.
	Close() error
}

// CostEstimator is an optional interface for providers that support dry-run cost estimation.
// Use type assertion to check: if ce, ok := provider.(CostEstimator); ok { ... }
type CostEstimator interface {
	// DryRun estimates bytes that would be scanned by a query without executing it.
	DryRun(ctx context.Context, query string) (*DryRunResult, error)
}

// SampleQueryBuilder is an optional interface for providers that can build
// a native-dialect "sample N rows" query. Schema discovery uses this to
// read a few rows per table during the warehouse scan; implementing it
// avoids a round-trip through the SQL-fix retry loop for every table,
// which would otherwise cost one LLM call per table for any non-BigQuery
// provider (backticks + LIMIT is BigQuery/MySQL syntax; T-SQL uses TOP n
// and square brackets, Postgres/Snowflake use double-quoted identifiers,
// Databricks accepts backticks, etc.).
//
// `filterClause` is either an empty string or a full SQL fragment such as
// `WHERE country = 'TR'` — the caller passes it through verbatim. The
// implementation decides where to place it relative to TOP / LIMIT.
//
// Providers that do NOT implement this interface fall back to a generic
// BigQuery-style query in schema discovery; the SQL fixer will rewrite
// it on first use at the cost of one LLM call per table.
//
// Use type assertion to check: if b, ok := provider.(SampleQueryBuilder); ok { ... }
type SampleQueryBuilder interface {
	// SampleQuery returns a read-only "SELECT up to `limit` rows from
	// `dataset`.`table`" in the provider's native SQL dialect. Callers
	// must validate `dataset` and `table` are plain identifiers before
	// calling; this method does not sanitise its inputs.
	SampleQuery(dataset, table, filterClause string, limit int) string
}

// DryRunResult holds the result of a dry-run query estimation.
type DryRunResult struct {
	BytesProcessed int64
}

// QueryResult holds the result of a warehouse query.
type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}
}

// TableSchema describes a table's structure in a warehouse-agnostic way.
type TableSchema struct {
	Name     string
	Columns  []ColumnSchema
	RowCount int64
}

// ColumnSchema describes a single column in a warehouse-agnostic way.
type ColumnSchema struct {
	Name     string
	Type     string // Normalized type: "STRING", "INT64", "FLOAT64", "BOOL", "TIMESTAMP", "DATE", "BYTES", "RECORD"
	Nullable bool
}
