package discovery

import (
	"context"
	"fmt"
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

	for _, dataset := range s.datasets {
		logger.WithField("dataset", dataset).Info("Discovering schemas for dataset")

		// Use provider's multi-dataset method
		tables, err := s.warehouse.ListTablesInDataset(ctx, dataset)
		if err != nil {
			logger.WithFields(logger.Fields{"dataset": dataset, "error": err.Error()}).Warn("Failed to list tables, skipping dataset")
			continue
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
			schema, err := s.discoverTable(ctx, dataset, tableName)
			if err != nil {
				logger.WithFields(logger.Fields{"table": tableName, "dataset": dataset, "error": err.Error()}).Warn("Failed to discover table, skipping")
				if s.onTableDiscovered != nil {
					s.onTableDiscovered(dataset, tableName, false)
				}
				continue
			}

			key := fmt.Sprintf("%s.%s", dataset, tableName)
			allSchemas[key] = *schema
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

	return allSchemas, nil
}

// discoverTable discovers the schema for a specific table using the provider.
func (s *SchemaDiscovery) discoverTable(ctx context.Context, dataset, tableName string) (*models.TableSchema, error) {
	qualifiedName := fmt.Sprintf("%s.%s", dataset, tableName)

	// Use provider's multi-dataset schema method
	whSchema, err := s.warehouse.GetTableSchemaInDataset(ctx, dataset, tableName)
	if err != nil {
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

	// Get sample data
	sampleData, err := s.getSampleData(ctx, dataset, tableName)
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
	// builder fall back to the legacy BigQuery/MySQL-style query below,
	// which the SQL fixer will rewrite on first use.
	var query string
	if b, ok := s.warehouse.(gowarehouse.SampleQueryBuilder); ok {
		query = b.SampleQuery(dataset, tableName, s.filter, sampleRowLimit)
	} else {
		query = fmt.Sprintf("SELECT * FROM `%s.%s` %s LIMIT %d", dataset, tableName, s.filter, sampleRowLimit)
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
