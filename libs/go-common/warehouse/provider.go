package warehouse

import (
	"context"
	"strings"
)

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

	// QuoteRef returns a dialect-correct fully-qualified identifier built
	// from the given parts (typically dataset + table, or catalog +
	// schema + table). Each part is quoted individually using the
	// dialect's identifier-quoting convention, then joined with dots.
	//   BigQuery / Databricks: `dataset`.`table`
	//   PostgreSQL / Redshift / Snowflake: "dataset"."table"
	//   SQL Server (MSSQL):    [dataset].[table]
	// Callers must validate parts are plain identifiers before calling;
	// this method does not sanitise its inputs (e.g., does not escape
	// embedded quote characters).
	//
	// Used by the discovery agent to render dialect-correct table refs
	// into exploration prompts so the LLM emits SQL the warehouse
	// accepts on the first try, instead of generating BigQuery-style
	// backticks and round-tripping through the SQL-fix LLM call.
	QuoteRef(parts ...string) string

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

	// ValidateSQL asks the warehouse to compile the given SQL
	// without executing it and returns nil iff the warehouse
	// accepts the statement in its own dialect.
	//
	// Each provider routes through the warehouse's native dry-run /
	// compile-only path so the dialect-owner decides — no shared
	// hand-rolled SQL grammar lives in this package. Typical
	// implementations:
	//   BigQuery:   QueryConfig.DryRun = true
	//   PostgreSQL: EXPLAIN <sql>
	//   Redshift:   EXPLAIN <sql>
	//   Snowflake:  EXPLAIN USING TEXT <sql>
	//   Databricks: EXPLAIN <sql>
	//   SQL Server: SET NOEXEC ON; <sql>; SET NOEXEC OFF;
	//
	// The check is "compile, don't execute". Concretely that
	// covers syntax and — for every provider above — name
	// resolution against the catalogue: a SELECT against a missing
	// table surfaces as an error. Implementations must NOT execute
	// the statement; a passing ValidateSQL on an INSERT / UPDATE /
	// DELETE still does not authorise execution. Read-only
	// enforcement remains the ValidateReadOnly contract's
	// responsibility.
	//
	// The returned error is suitable for surfacing back to the
	// caller: providers include the warehouse's own error text so
	// the caller can echo it directly into an end-user message or
	// retry loop.
	ValidateSQL(ctx context.Context, sql string) error

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

// RefQualifier is an optional interface for providers whose fully-qualified
// table reference includes a component the discovery agent cannot derive
// from the dataset + table alone. BigQuery cross-project reads are the
// motivating case: the data lives in a different GCP project than the one
// running the query job, so a correct reference must be three-part
// (`dataProjectID.dataset.table`). A two-part `dataset.table` resolves its
// project to the job's billing project and 404s.
//
// The agent uses QualifiedName when building the schema catalog and the
// canonical schema-cache key, so the LLM sees names that resolve on the
// first try. Providers that do not implement this interface fall back to
// the plain "dataset.table" form, which is correct for every
// single-project warehouse.
//
// Use type assertion to check: if q, ok := provider.(RefQualifier); ok { ... }
type RefQualifier interface {
	// QualifiedName returns the UNQUOTED, provider-native fully-qualified
	// table name (e.g. "bigquery-public-data.dataset.table" for
	// cross-project BigQuery, "dataset.table" otherwise). Callers must
	// validate inputs are plain identifiers; this method does not sanitise.
	QualifiedName(dataset, table string) string
}

// DryRunResult holds the result of a dry-run query estimation.
type DryRunResult struct {
	BytesProcessed int64
}

// QueryResult holds the result of a warehouse query.
type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}

	// Quality carries any source-reported caveat about how faithful this
	// result is to the query that produced it — rows withheld, values
	// sampled, a tail truncated. Nil for a result the source reported no
	// caveat on, which is every SQL warehouse today: they answer exactly what
	// was asked or fail, so there is nothing to declare.
	//
	// A source that CAN silently degrade is expected to populate this rather
	// than return the rows bare, because a degraded result is indistinguishable
	// from a sound one by inspection. See QualityCaveat.
	Quality []QualityCaveat
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

// QuotePartsWith builds a fully-qualified identifier by wrapping each
// non-empty part with the given open / close strings and joining the
// quoted parts with dots. Empty / whitespace-only parts are skipped;
// if all parts are empty the function returns "".
//
// Providers compose their dialect-specific QuoteRef on top of this
// helper to avoid duplicating the filter-and-join loop:
//
//	BigQuery / Databricks:               QuotePartsWith("`", "`", parts)
//	PostgreSQL / Redshift / Snowflake:   QuotePartsWith(`"`, `"`, parts)
//	SQL Server (MSSQL):                  QuotePartsWith("[", "]", parts)
//
// The helper does NOT escape embedded delimiter characters — the
// Provider.QuoteRef contract requires callers to pass plain
// identifiers, and warehouse identifier validation runs at each
// provider's request boundary.
func QuotePartsWith(open, close string, parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		quoted = append(quoted, open+p+close)
	}
	return strings.Join(quoted, ".")
}
