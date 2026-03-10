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

		return NewBigQueryProvider(context.Background(), BigQueryConfig{
			ProjectID: cfg["project_id"],
			Dataset:   cfg["dataset"],
			Location:  cfg["location"],
			Timeout:   time.Duration(timeoutMin) * time.Minute,
		})
	}, gowarehouse.ProviderMeta{
		Name:        "Google BigQuery",
		Description: "Google Cloud data warehouse for analytics",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project"},
			{Key: "dataset", Label: "Dataset", Required: true, Type: "string", Placeholder: "my_analytics_dataset"},
			{Key: "location", Label: "Location", Type: "string", Default: "US", Placeholder: "US"},
		},
	})
}

// BigQueryConfig holds BigQuery-specific configuration.
type BigQueryConfig struct {
	ProjectID string
	Dataset   string
	Location  string
	Timeout   time.Duration
	// ClientOptions allows passing custom options (e.g., emulator endpoint).
	// Used for testing with BigQuery emulator.
	ClientOptions []option.ClientOption
}

// BigQueryProvider implements warehouse.Provider for Google BigQuery.
type BigQueryProvider struct {
	client  *bq.Client
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
		RowCount: int64(metadata.NumRows),
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
