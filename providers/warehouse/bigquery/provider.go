// Package warehouse provides warehouse.Provider implementations.
// The BigQuery provider registers itself via init() so services can
// select it with WAREHOUSE_PROVIDER=bigquery and warehouse.NewProvider("bigquery", cfg).
package bigquery

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	bq "cloud.google.com/go/bigquery"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

//go:embed prompts/sql_fix.md
var sqlFixPrompt string

func init() {
	gowarehouse.RegisterWithMeta("bigquery", func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
		timeoutMin, _ := strconv.Atoi(cfg["timeout_minutes"])
		if timeoutMin == 0 {
			timeoutMin = 5
		}

		// Resolve credentials based on auth method.
		var credentialsJSON string
		switch cfg["auth_method"] {
		case "sa_key":
			credentialsJSON = cfg["credentials_json"]
			if credentialsJSON == "" {
				return nil, fmt.Errorf("bigquery: service account key is required for sa_key auth")
			}
		case "adc", "":
			// Application Default Credentials — no explicit credentials needed.
		default:
			return nil, fmt.Errorf("bigquery: unsupported auth method %q", cfg["auth_method"])
		}

		return NewBigQueryProvider(context.Background(), BigQueryConfig{
			ProjectID:       cfg["project_id"],
			Dataset:         cfg["dataset"],
			Location:        cfg["location"],
			Timeout:         time.Duration(timeoutMin) * time.Minute,
			CredentialsJSON: credentialsJSON,
		})
	}, gowarehouse.ProviderMeta{
		Name:        "Google BigQuery",
		Description: "Google Cloud data warehouse for analytics",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "dataset", Label: "Datasets", Description: "Comma-separated dataset names", Required: true, Type: "string", Placeholder: "events_prod, features_prod"},
			{Key: "location", Label: "Location", Type: "string", Default: "US", Placeholder: "US"},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: "adc", Name: "Application Default Credentials",
				Description: "Automatic — GKE Workload Identity, gcloud auth, VM service account. No credentials needed.",
			},
			{
				ID: "sa_key", Name: "Service Account Key",
				Description: "GCP service account JSON key. Also supports Workload Identity Federation credential configs.",
				Fields: []gowarehouse.ConfigField{
					{Key: "credentials", Label: "Service Account JSON", Required: true, Type: "credential"},
				},
			},
		},
		DefaultPricing: &gowarehouse.WarehousePricing{
			CostModel:           "per_byte_scanned",
			CostPerTBScannedUSD: 6.25,
		},
	})
}

// BigQueryConfig holds BigQuery-specific configuration.
type BigQueryConfig struct {
	ProjectID string
	Dataset   string
	Location  string
	Timeout   time.Duration
	// CredentialsJSON is an optional GCP service account key (JSON).
	// When set, BigQuery authenticates with this key instead of ADC.
	// Use this when running outside GCP (e.g., on AWS, Azure, or on-prem).
	// If empty, falls back to Application Default Credentials.
	CredentialsJSON string
	// ClientOptions allows passing custom options (e.g., emulator endpoint).
	// Used for testing with BigQuery emulator.
	ClientOptions []option.ClientOption
}

// BigQueryProvider implements warehouse.Provider for Google BigQuery.
type BigQueryProvider struct {
	client  bqClient
	dataset string
	config  BigQueryConfig
}

// NewBigQueryProvider creates a BigQuery warehouse provider.
func NewBigQueryProvider(ctx context.Context, cfg BigQueryConfig) (*BigQueryProvider, error) {
	if cfg.ProjectID == "" {
		return nil, fmt.Errorf("bigquery: project_id is required")
	}
	if cfg.Dataset == "" {
		return nil, fmt.Errorf("bigquery: dataset is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}

	// If credentials JSON provided, use it instead of ADC
	if cfg.CredentialsJSON != "" {
		cfg.ClientOptions = append(cfg.ClientOptions,
			option.WithCredentialsJSON([]byte(cfg.CredentialsJSON)))
	}

	client, err := bq.NewClient(ctx, cfg.ProjectID, cfg.ClientOptions...)
	if err != nil {
		return nil, fmt.Errorf("bigquery: failed to create client: %w", err)
	}

	return &BigQueryProvider{client: client, dataset: cfg.Dataset, config: cfg}, nil
}

func (p *BigQueryProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	q := p.client.Query(query)

	if len(params) > 0 {
		qp := make([]bq.QueryParameter, 0, len(params))
		for name, value := range params {
			qp = append(qp, bq.QueryParameter{Name: name, Value: value})
		}
		q.Parameters = qp
	}

	if p.config.Location != "" {
		q.Location = p.config.Location
	}

	queryCtx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()

	it, err := q.Read(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("bigquery: query failed: %w", err)
	}

	var columns []string
	if it.Schema != nil {
		for _, field := range it.Schema {
			columns = append(columns, field.Name)
		}
	}

	var rows []map[string]interface{}
	for {
		var row map[string]bq.Value
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bigquery: failed to read row: %w", err)
		}
		result := make(map[string]interface{})
		for k, v := range row {
			result[k] = v
		}
		rows = append(rows, result)
	}

	return &gowarehouse.QueryResult{Columns: columns, Rows: rows}, nil
}

func (p *BigQueryProvider) ListTables(ctx context.Context) ([]string, error) {
	ds := p.client.Dataset(p.dataset)
	it := ds.Tables(ctx)

	var tables []string
	for {
		table, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bigquery: failed to list tables: %w", err)
		}
		md, err := table.Metadata(ctx)
		if err != nil {
			continue
		}
		if md.Type == bq.ViewTable || md.Type == bq.MaterializedView {
			continue
		}
		tables = append(tables, table.TableID)
	}
	return tables, nil
}

func (p *BigQueryProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
	t := p.client.Dataset(p.dataset).Table(table)
	metadata, err := t.Metadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("bigquery: failed to get metadata for %s: %w", table, err)
	}

	schema := &gowarehouse.TableSchema{
		Name:     table,
		RowCount: int64(metadata.NumRows), //nolint:gosec // NumRows won't exceed int64 max
	}

	if metadata.Schema != nil {
		for _, field := range metadata.Schema {
			schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
				Name:     field.Name,
				Type:     string(field.Type),
				Nullable: !field.Required,
			})
		}
	}

	return schema, nil
}

func (p *BigQueryProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
	ds := p.client.Dataset(dataset)
	it := ds.Tables(ctx)

	var tables []string
	for {
		table, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bigquery: failed to list tables in %s: %w", dataset, err)
		}
		md, err := table.Metadata(ctx)
		if err != nil {
			continue
		}
		if md.Type == bq.ViewTable || md.Type == bq.MaterializedView {
			continue
		}
		tables = append(tables, table.TableID)
	}
	return tables, nil
}

func (p *BigQueryProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
	t := p.client.Dataset(dataset).Table(table)
	metadata, err := t.Metadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("bigquery: failed to get metadata for %s.%s: %w", dataset, table, err)
	}

	schema := &gowarehouse.TableSchema{
		Name:     table,
		RowCount: int64(metadata.NumRows), //nolint:gosec // NumRows won't exceed int64 max
	}

	if metadata.Schema != nil {
		for _, field := range metadata.Schema {
			schema.Columns = append(schema.Columns, gowarehouse.ColumnSchema{
				Name:     field.Name,
				Type:     string(field.Type),
				Nullable: !field.Required,
			})
		}
	}

	return schema, nil
}

func (p *BigQueryProvider) GetDataset() string {
	return p.dataset
}

func (p *BigQueryProvider) SQLDialect() string {
	return "BigQuery Standard SQL"
}

func (p *BigQueryProvider) SQLFixPrompt() string {
	return sqlFixPrompt
}

func (p *BigQueryProvider) ValidateReadOnly(ctx context.Context) error {
	// Validate by attempting a safe read query. If the service account
	// has only BigQuery Data Viewer + Job User roles, this succeeds
	// but write operations would fail.
	//
	// We verify read access works and DON'T test write access.
	// The proper IAM roles for read-only BigQuery access are:
	//   - roles/bigquery.dataViewer (read tables)
	//   - roles/bigquery.jobUser (run queries)
	// These do NOT include bigquery.tables.create/update/delete.

	// Test 1: Can read dataset metadata
	ds := p.client.Dataset(p.dataset)
	if _, err := ds.Metadata(ctx); err != nil {
		return fmt.Errorf("bigquery: cannot access dataset %s: %w", p.dataset, err)
	}

	// Test 2: Can run a simple query
	q := p.client.Query("SELECT 1 as test")
	q.Location = p.config.Location
	it, err := q.Read(ctx)
	if err != nil {
		return fmt.Errorf("bigquery: cannot execute queries: %w", err)
	}
	_ = it // just check it doesn't error

	return nil
}

func (p *BigQueryProvider) HealthCheck(ctx context.Context) error {
	ds := p.client.Dataset(p.dataset)
	_, err := ds.Metadata(ctx)
	if err != nil {
		return fmt.Errorf("bigquery: health check failed: %w", err)
	}
	return nil
}

func (p *BigQueryProvider) Close() error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// DryRun estimates bytes that would be scanned by a query without executing it.
// Implements warehouse.CostEstimator.
func (p *BigQueryProvider) DryRun(ctx context.Context, query string) (*gowarehouse.DryRunResult, error) {
	q := p.client.Query(query)
	q.DryRun = true

	job, err := q.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("bigquery dry-run: %w", err)
	}

	status := job.LastStatus()
	if status == nil {
		return &gowarehouse.DryRunResult{BytesProcessed: 0}, nil
	}

	return &gowarehouse.DryRunResult{
		BytesProcessed: status.Statistics.TotalBytesProcessed,
	}, nil
}

// Compile-time check that BigQueryProvider implements CostEstimator.
var _ gowarehouse.CostEstimator = (*BigQueryProvider)(nil)
