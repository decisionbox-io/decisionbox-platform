package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// SchemaDiscovery discovers and analyzes warehouse table schemas
// across multiple datasets.
type SchemaDiscovery struct {
	warehouse gowarehouse.Provider
	executor  *queryexec.QueryExecutor
	projectID string
	datasets  []string // multiple datasets to discover
	filter    string

	onTablesListed    func(dataset string, total int)
	onTableDiscovered func(dataset, table string, ok bool)
}

// SchemaDiscoveryOptions configures schema discovery.
type SchemaDiscoveryOptions struct {
	Warehouse gowarehouse.Provider
	Executor  *queryexec.QueryExecutor
	ProjectID string
	Datasets  []string
	Filter    string

	// OnTablesListed, when non-nil, is called once per dataset after
	// ListTablesInDataset returns but before per-table discovery
	// begins. Lets the schema-indexer stamp `tables_total` on the
	// progress doc so the dashboard renders a meaningful progress bar
	// during the longest leg of indexing.
	OnTablesListed func(dataset string, total int)

	// OnTableDiscovered, when non-nil, is called after each table's
	// schema has been pulled (or after the pull failed). Used by the
	// schema-indexer to increment the progress doc's tables_done
	// counter during the schema-discovery phase.
	OnTableDiscovered func(dataset, table string, ok bool)
}

// NewSchemaDiscovery creates a new schema discovery service.
func NewSchemaDiscovery(opts SchemaDiscoveryOptions) *SchemaDiscovery {
	return &SchemaDiscovery{
		warehouse:         opts.Warehouse,
		executor:          opts.Executor,
		projectID:         opts.ProjectID,
		datasets:          opts.Datasets,
		filter:            opts.Filter,
		onTablesListed:    opts.OnTablesListed,
		onTableDiscovered: opts.OnTableDiscovered,
	}
}

// DiscoverSchemas discovers all tables across all configured datasets.
// Table keys are "dataset.table" for multi-dataset, or just "table" for single.
func (s *SchemaDiscovery) DiscoverSchemas(ctx context.Context) (map[string]models.TableSchema, error) {
	logger.WithField("datasets", s.datasets).Info("Discovering warehouse table schemas")

	allSchemas := make(map[string]models.TableSchema)

	// Why the failures are collected rather than only logged: a dataset that
	// cannot be listed is skipped so the others still index, which is right
	// when one of several fails. It is not right when EVERY dataset fails --
	// the caller then sees an empty map and a nil error, and reports "no
	// tables discovered -- check datasets and warehouse permissions" for what
	// was actually a refused connection. The reason reached a Warn line in the
	// agent's container log and went no further: for a project configured with
	// `sslmode=require` against a server without TLS, the operator was told to
	// check dataset names and grants while the product held
	// `pq: SSL is not enabled on the server`.
	// Collected under the same bound the error is rendered with, rather than
	// kept in full and trimmed at the end: a healthy dataset means none of this
	// is ever returned, and holding a formatted copy of every provider response
	// until the scan finishes is memory spent on a string nobody will read.
	// The COUNT is kept whole, because that is what the message reports.
	var datasetFailures []string
	datasetFailureCount := 0

	for _, dataset := range s.datasets {
		logger.WithField("dataset", dataset).Info("Discovering schemas for dataset")

		// Use provider's multi-dataset method
		tables, err := s.warehouse.ListTablesInDataset(ctx, dataset)
		if err != nil {
			logger.WithFields(logger.Fields{"dataset": dataset, "error": err.Error()}).Warn("Failed to list tables, skipping dataset")
			datasetFailureCount++
			if len(datasetFailures) < maxReportedDatasetFailures {
				datasetFailures = append(datasetFailures, clipFailure(dataset, err))
			}
			continue
		}

		// Run any registered ListTables filters (e.g. discovery-scope
		// plugins) before per-table schema discovery so plugins can
		// shrink the set. A filter error fails the dataset just like a
		// list-tables error — schema discovery skips it but other
		// datasets continue.
		preFilter := len(tables)
		filtered, ferr := ApplyListTablesFilters(ctx, s.projectID, dataset, tables)
		if ferr != nil {
			logger.WithFields(logger.Fields{
				"dataset": dataset,
				"error":   ferr.Error(),
			}).Warn("ListTables filter failed, skipping dataset")
			// NOT collected into datasetFailures, unlike a list failure. A
			// filter is the deployment's own scope plugin saying this dataset
			// is out of bounds, and `TestSchemaDiscovery_FilterErrorSkipsDataset`
			// pins that it skips without bubbling -- a scope that excludes
			// everything leaves a warehouse with nothing in scope, which is
			// what the caller's "no tables discovered" message describes. A
			// list failure is the different fact: the warehouse was never
			// reached at all.
			continue
		}
		tables = filtered
		if len(tables) != preFilter {
			logger.WithFields(logger.Fields{
				"dataset": dataset,
				"before":  preFilter,
				"after":   len(tables),
			}).Info("ListTables filter applied")
		}

		logger.WithFields(logger.Fields{"dataset": dataset, "tables": len(tables)}).Info("Listed tables, now pulling schema per-table")
		if s.onTablesListed != nil {
			s.onTablesListed(dataset, len(tables))
		}
		for i, tableName := range tables {
			// Per-table tick at Info level so a hang on a specific
			// table is visible in live logs without flipping the whole
			// agent to Debug. Keeps the observability bill cheap
			// (~2K log lines for a FINPORT run, one per table).
			logger.WithFields(logger.Fields{
				"dataset": dataset,
				"table":   tableName,
				"i":       i + 1,
				"of":      len(tables),
			}).Info("Discovering table schema")
			tableStart := time.Now()
			schema, err := s.discoverTable(ctx, dataset, tableName)
			if err != nil {
				logger.WithFields(logger.Fields{
					"table":   tableName,
					"dataset": dataset,
					"error":   err.Error(),
					"elapsed": time.Since(tableStart).String(),
				}).Warn("Failed to discover table, skipping")
				if s.onTableDiscovered != nil {
					s.onTableDiscovered(dataset, tableName, false)
				}
				continue
			}

			// Key on the schema's own qualified name (set by discoverTable
			// via the provider's RefQualifier) so the map key, the
			// schema-cache schema_key, the catalog line, and the refIndex
			// canonical key are all the same string — three-part
			// `dataproject.dataset.table` for cross-project BigQuery, the
			// plain "dataset.table" form everywhere else.
			allSchemas[schema.TableName] = *schema
			if s.onTableDiscovered != nil {
				s.onTableDiscovered(dataset, tableName, true)
			}
		}

		logger.WithFields(logger.Fields{
			"dataset": dataset,
			"tables":  len(allSchemas),
		}).Info("Dataset schema discovery complete")
	}

	logger.WithField("total_tables", len(allSchemas)).Info("All schema discovery complete")

	// Nothing found AND something failed: the failures are the answer. A
	// partial result still returns successfully with the datasets that worked,
	// and a warehouse that genuinely holds no tables still returns an empty
	// map and no error -- the caller's own "no tables discovered" message is
	// the right one for that, and only for that.
	if len(allSchemas) == 0 && len(datasetFailures) > 0 {
		// How many of how many, rather than "every": one dataset can fail to
		// list while another lists fine and is simply empty, or has had every
		// table filtered out of scope. Both end with nothing discovered, and
		// an operator reading "every dataset failed" about a half-failure
		// would go looking for a problem with the healthy one too.
		// Bounded, because this error is carried back as a single stderr line
		// and the subprocess runner reads one with a 512 KiB cap
		// (`scanner.Buffer` in services/api/internal/runner/subprocess.go). A
		// warehouse with many datasets failing at once could exceed that even
		// with every individual message well under it, and a scanner that
		// stops reading mid-line leaves the agent blocked on the write while
		// the worker waits for it. The count is the part an operator acts on;
		// every message is in the dataset warnings above regardless.
		return nil, fmt.Errorf("no tables discovered; %d of %d dataset(s) failed to list: %s",
			datasetFailureCount, len(s.datasets), boundedFailures(datasetFailures, datasetFailureCount))
	}

	return allSchemas, nil
}

// The returned error crosses a runner boundary that reads it as ONE line with
// a 512 KiB cap (`scanner.Buffer` in services/api/internal/runner/subprocess.go),
// and a scanner that stops mid-line leaves the agent blocked on the write while
// the worker waits for it. So the diagnostic is bounded twice over: by how many
// messages it carries, and by how many bytes they come to -- a provider error
// can embed a whole response body, so three of them is not a size.
//
// Three is enough to show the SHAPE of the failure: the same refusal on every
// dataset, or three different ones. 8 KiB leaves room for the log escaping
// applied downstream and is still two orders of magnitude under the cap. The
// full set is in the per-dataset warnings either way.
const (
	maxReportedDatasetFailures = 3
	maxFailureDiagnosticBytes  = 8 * 1024
)

// clipFailure formats one dataset's failure, clipped so that no single
// provider message — a response body, a stack — can carry the whole budget.
func clipFailure(dataset string, err error) string {
	msg := fmt.Sprintf("%s: %v", dataset, err)
	if len(msg) > maxFailureDiagnosticBytes/maxReportedDatasetFailures {
		msg = msg[:maxFailureDiagnosticBytes/maxReportedDatasetFailures] + "…"
	}
	return msg
}

// boundedFailures renders the messages that were kept, clipped to
// maxFailureDiagnosticBytes in total, and says how many were left out.
func boundedFailures(kept []string, total int) string {
	joined := strings.Join(kept, "; ")
	if len(joined) > maxFailureDiagnosticBytes {
		joined = joined[:maxFailureDiagnosticBytes] + "… (truncated)"
	}
	if total > len(kept) {
		joined += fmt.Sprintf("; and %d more (see the agent log)", total-len(kept))
	}
	return joined
}

// perTableTimeout bounds how long a single table's schema + sample can
// take. A rogue MSSQL catalog view or a pathological linked-server table
// can hang `GetTableSchemaInDataset` indefinitely, wedging the entire
// indexing run. Two minutes is generous for well-behaved warehouses and
// short enough that a 1400-table run still completes in bounded time
// even if several tables each exhaust their budget.
const perTableTimeout = 2 * time.Minute

// discoverTable discovers the schema for a specific table using the provider.
// Enforces perTableTimeout so a single stuck catalog query doesn't block
// the rest of the discovery pass.
func (s *SchemaDiscovery) discoverTable(ctx context.Context, dataset, tableName string) (*models.TableSchema, error) {
	// Canonical qualified name shown to the LLM in the schema catalog and
	// used as the schema-cache / Qdrant key. Providers whose fully-qualified
	// ref needs a component the agent can't derive (BigQuery cross-project
	// reads need the data project: `dataproject.dataset.table`) supply it via
	// RefQualifier; everything else uses the plain "dataset.table" form.
	qualifiedName := fmt.Sprintf("%s.%s", dataset, tableName)
	if q, ok := s.warehouse.(gowarehouse.RefQualifier); ok {
		qualifiedName = q.QualifiedName(dataset, tableName)
	}

	tableCtx, cancel := context.WithTimeout(ctx, perTableTimeout)
	defer cancel()

	whSchema, err := s.warehouse.GetTableSchemaInDataset(tableCtx, dataset, tableName)
	if err != nil {
		if tableCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("get schema: timed out after %s (warehouse never responded)", perTableTimeout)
		}
		return nil, fmt.Errorf("get schema: %w", err)
	}

	schema := &models.TableSchema{
		TableName:    qualifiedName,
		RowCount:     whSchema.RowCount,
		Columns:      make([]models.ColumnInfo, 0, len(whSchema.Columns)),
		KeyColumns:   make([]string, 0),
		Metrics:      make([]string, 0),
		Dimensions:   make([]string, 0),
		DiscoveredAt: time.Now(),
	}

	for _, col := range whSchema.Columns {
		colInfo := models.ColumnInfo{
			Name:     col.Name,
			Type:     col.Type,
			Nullable: col.Nullable,
			Category: inferColumnCategory(col.Name, col.Type),
		}
		schema.Columns = append(schema.Columns, colInfo)
		categorizeColumn(&colInfo, schema)
	}

	// Get sample data under the same per-table budget — a slow SELECT
	// against a hostile table shouldn't extend the discovery pass either.
	sampleData, err := s.getSampleData(tableCtx, dataset, tableName)
	if err == nil {
		schema.SampleData = sampleData
	}

	logger.WithFields(logger.Fields{
		"table":   qualifiedName,
		"columns": len(schema.Columns),
		"rows":    schema.RowCount,
	}).Debug("Table schema discovered")

	return schema, nil
}

// sampleRowLimit is the number of rows fetched per table when sampling for
// schema discovery. Small enough to stay cheap on large tables, big enough
// to reveal value distribution and nullability patterns to the LLM.
const sampleRowLimit = 5

func (s *SchemaDiscovery) getSampleData(ctx context.Context, dataset, tableName string) ([]map[string]interface{}, error) {
	// Prefer the provider's own dialect-aware builder when it implements
	// SampleQueryBuilder. This avoids a per-table LLM round-trip through
	// the SQL fixer for every non-BigQuery provider (MSSQL, Snowflake,
	// Redshift, Postgres, Databricks), which is particularly expensive
	// during schema discovery on large warehouses (~1 LLM call + ~5s +
	// ~8KB tokens per table). Providers that haven't implemented the
	// builder fall back to a generic SELECT … LIMIT N. The qualified
	// table ref is rendered via the provider's QuoteRef so the fallback
	// still uses the dialect's identifier-quoting convention; only LIMIT
	// remains potentially non-portable (T-SQL uses TOP — providers that
	// need it implement SampleQueryBuilder explicitly).
	var query string
	if b, ok := s.warehouse.(gowarehouse.SampleQueryBuilder); ok {
		query = b.SampleQuery(dataset, tableName, s.filter, sampleRowLimit)
	} else {
		query = fmt.Sprintf("SELECT * FROM %s %s LIMIT %d", s.warehouse.QuoteRef(dataset, tableName), s.filter, sampleRowLimit)
	}

	result, err := s.executor.Execute(ctx, query, "sample data for "+dataset+"."+tableName)
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

func inferColumnCategory(name string, fieldType string) string {
	if name == "id" || name == "user_id" || name == "player_id" ||
		name == "session_id" || name == "event_id" {
		return "primary_key"
	}
	if name == "created_at" || name == "updated_at" || name == "timestamp" ||
		name == "start_time" || name == "end_time" || name == "date" ||
		fieldType == "TIMESTAMP" || fieldType == "DATE" || fieldType == "DATETIME" {
		return "time"
	}
	if fieldType == "INT64" || fieldType == "FLOAT64" || fieldType == "NUMERIC" || fieldType == "BIGNUMERIC" ||
		fieldType == "INTEGER" || fieldType == "FLOAT" {
		if name == "id" || name == "user_id" || name == "player_id" {
			return "dimension"
		}
		return "metric"
	}
	return "dimension"
}

func categorizeColumn(col *models.ColumnInfo, schema *models.TableSchema) {
	switch col.Category {
	case "primary_key":
		schema.KeyColumns = append(schema.KeyColumns, col.Name)
	case "metric":
		schema.Metrics = append(schema.Metrics, col.Name)
	case "dimension", "time":
		schema.Dimensions = append(schema.Dimensions, col.Name)
	}
}
