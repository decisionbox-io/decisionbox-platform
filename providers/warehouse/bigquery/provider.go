// Package warehouse provides warehouse.Provider implementations.
// The BigQuery provider registers itself via init() so services can
// select it with WAREHOUSE_PROVIDER=bigquery and warehouse.NewProvider("bigquery", cfg).
package bigquery

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"time"

	bq "cloud.google.com/go/bigquery"
	"github.com/decisionbox-io/decisionbox/libs/gcpcreds"
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

		clientOpts, err := gcpcreds.ClientOptions(context.Background(), gcpcreds.Config{
			Method:          cfg["auth_method"],
			CredentialsJSON: cfg[gcpcreds.FieldCredentials],
		})
		if err != nil {
			return nil, fmt.Errorf("bigquery: %w", err)
		}

		return NewBigQueryProvider(context.Background(), BigQueryConfig{
			ProjectID:     cfg["project_id"],
			DataProjectID: cfg["data_project_id"],
			Dataset:       cfg["dataset"],
			Location:      cfg["location"],
			Timeout:       time.Duration(timeoutMin) * time.Minute,
			ClientOptions: clientOpts,
		})
	}, gowarehouse.ProviderMeta{
		Name:        "Google BigQuery",
		Description: "Google Cloud data warehouse for analytics",
		ConfigFields: []gowarehouse.ConfigField{
			{Key: "project_id", Label: "GCP Project ID", Required: true, Type: "string", Placeholder: "my-gcp-project", Description: "GCP project the BigQuery client runs query jobs in (jobs.create required). For most setups this is also the project that owns the datasets — leave Data project ID empty."},
			{Key: "data_project_id", Label: "Data project ID (cross-project reads)", Required: false, Type: "string", Placeholder: "bigquery-public-data", Description: "GCP project that owns the datasets. Defaults to GCP Project ID. Set when reading datasets from a different project — e.g. bigquery-public-data samples — so jobs still bill to your own project while reads target the data's home."},
			{Key: "dataset", Label: "Datasets", Description: "Comma-separated dataset names", Required: true, Type: "string", Placeholder: "events_prod, features_prod"},
			{Key: "location", Label: "Location", Type: "string", Default: "US", Placeholder: "US"},
		},
		AuthMethods: []gowarehouse.AuthMethod{
			{
				ID: gcpcreds.MethodADC, Name: "Application Default Credentials",
				Description: "Automatic — GKE Workload Identity, gcloud auth, VM service account. No credentials needed.",
			},
			{
				ID: gcpcreds.MethodSAKey, Name: "Service Account Key",
				Description: "GCP service account JSON key. Also supports Workload Identity Federation credential configs.",
				Fields: []gowarehouse.ConfigField{
					{Key: gcpcreds.FieldCredentials, Label: "Service Account JSON", Required: true, Type: "credential"},
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
	// ProjectID is the GCP project the BigQuery client runs query
	// jobs in. The credential's principal needs bigquery.jobs.create
	// in this project. For single-project setups this is also where
	// the data lives.
	ProjectID string
	// DataProjectID is the GCP project that owns the datasets, when
	// it differs from ProjectID. Optional. Empty = same project
	// (the common case). Set when reading datasets the operator
	// doesn't own — e.g. `bigquery-public-data` samples — so jobs
	// still bill to the operator's project while reads target the
	// data's home project. The BigQuery SDK's
	// client.DatasetInProject(DataProjectID, ...) routes metadata
	// reads accordingly, and Query.DefaultProjectID is set so
	// unqualified table references in agent-generated SQL resolve
	// against DataProjectID rather than the client's job project.
	DataProjectID string
	Dataset       string
	Location      string
	Timeout       time.Duration
	// ClientOptions carries credentials (resolved upstream via
	// gcpcreds.ClientOptions) plus any custom options such as an
	// emulator endpoint for tests.
	ClientOptions []option.ClientOption
}

// BigQueryProvider implements warehouse.Provider for Google BigQuery.
type BigQueryProvider struct {
	client        bqClient
	dataset       string
	dataProjectID string // effective; defaults to config.ProjectID
	config        BigQueryConfig
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

	dataProjectID := cfg.DataProjectID
	if dataProjectID == "" {
		dataProjectID = cfg.ProjectID
	}

	client, err := bq.NewClient(ctx, cfg.ProjectID, cfg.ClientOptions...)
	if err != nil {
		return nil, fmt.Errorf("bigquery: failed to create client: %w", err)
	}

	return &BigQueryProvider{
		client:        client,
		dataset:       cfg.Dataset,
		dataProjectID: dataProjectID,
		config:        cfg,
	}, nil
}

// datasetRef returns a dataset reference scoped to the effective
// data project. When ProjectID == DataProjectID the reference is the
// SDK's default Dataset() call, identical to the pre-cross-project
// behaviour. When they differ, DatasetInProject routes metadata
// reads to the dataset's home project even though the client was
// constructed against the jobs project.
func (p *BigQueryProvider) datasetRef(name string) *bq.Dataset {
	if p.crossProject() {
		return p.client.DatasetInProject(p.dataProjectID, name)
	}
	return p.client.Dataset(name)
}

// crossProject reports whether reads target a different GCP project than
// the one running query jobs (the bigquery-public-data shape). When true,
// every table reference the agent shows the LLM must be three-part
// (`dataProjectID.dataset.table`): BigQuery resolves the project of a
// two-part `dataset.table` reference to the jobs/billing project, so a
// two-part ref to a dataset the jobs project doesn't own fails with
// "Dataset <jobsProject>:<dataset> was not found".
func (p *BigQueryProvider) crossProject() bool {
	return p.dataProjectID != p.config.ProjectID
}

// applyDefaultProject sets the query's default dataset (project + dataset)
// for cross-project reads. With the schema catalog now handing the LLM
// fully-qualified three-part refs, this is a robustness aid for the
// occasional bare `table` reference: the BigQuery SDK requires
// DefaultProjectID and DefaultDatasetID to be set together (a project-only
// default is silently ignored), so both are set and a bare name resolves
// against the default dataset in the data project. It does NOT rescue a
// two-part `dataset.table` — BigQuery always resolves that ref's project to
// the jobs project regardless of the default dataset.
func (p *BigQueryProvider) applyDefaultProject(q *bq.Query) {
	if p.crossProject() {
		q.DefaultProjectID = p.dataProjectID
		q.DefaultDatasetID = p.defaultDatasetID()
	}
}

// defaultDatasetID returns a single, format-valid dataset id for the
// query's default dataset. The provider's dataset field may be a
// comma-joined list for multi-dataset projects, but BigQuery's
// defaultDataset accepts exactly one dataset id and rejects a comma string
// with "400 Invalid dataset ID" — even when the query's table refs are
// fully qualified and the default would never be consulted. So we take the
// first dataset; that is the one a bare `table` reference resolves against.
func (p *BigQueryProvider) defaultDatasetID() string {
	ds := p.dataset
	if i := strings.IndexByte(ds, ','); i >= 0 {
		ds = ds[:i]
	}
	return strings.TrimSpace(ds)
}

func (p *BigQueryProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
	q := p.client.Query(query)
	p.applyDefaultProject(q)

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
	ds := p.datasetRef(p.dataset)
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
	t := p.datasetRef(p.dataset).Table(table)
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
	ds := p.datasetRef(dataset)
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
	t := p.datasetRef(dataset).Table(table)
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

// QuoteRef returns a backtick-quoted, dot-joined identifier in BigQuery
// Standard SQL form, e.g. `dataset`.`table`. For cross-project reads the
// data project is prepended to a dataset+table pair, so the reference is
// three-part (`dataProjectID`.`dataset`.`table`) — see crossProject for why
// a two-part `dataset`.`table` would 404.
//
// The prepend applies ONLY to an exactly two-part dataset+table ref — the
// shape the agent emits via {{REF:dataset, table}}. Other arities pass
// through untouched:
//   - a lone table part stays bare (prefixing would make a bogus two-part
//     `dataProjectID`.`table`; a bare table resolves against the query's
//     default dataset, which applyDefaultProject sets for cross-project
//     reads), and
//   - a ref that already has three or more parts is treated as
//     project-qualified (possibly to a project other than the data project),
//     so we never build an invalid four-part identifier.
func (p *BigQueryProvider) QuoteRef(parts ...string) string {
	if p.crossProject() && len(parts) == 2 {
		parts = append([]string{p.dataProjectID}, parts...)
	}
	return gowarehouse.QuotePartsWith("`", "`", parts)
}

// QualifiedName returns the unquoted, fully-qualified table reference used
// as the schema-catalog line and the canonical schema-cache key. It is
// three-part (`dataProjectID.dataset.table`) for cross-project reads and
// two-part (`dataset.table`) otherwise. Implements gowarehouse.RefQualifier
// so the discovery agent shows the LLM names that resolve on the first try.
func (p *BigQueryProvider) QualifiedName(dataset, table string) string {
	if p.crossProject() {
		return fmt.Sprintf("%s.%s.%s", p.dataProjectID, dataset, table)
	}
	return fmt.Sprintf("%s.%s", dataset, table)
}

// SampleQuery builds a BigQuery-native "sample N rows" query — backtick-
// quoted fully-qualified name + LIMIT n. The qualified name is three-part
// for cross-project reads (see QualifiedName). The filter clause (either
// empty or a full WHERE fragment) is inlined between FROM and LIMIT.
func (p *BigQueryProvider) SampleQuery(dataset, table, filterClause string, limit int) string {
	return fmt.Sprintf("SELECT * FROM `%s` %s LIMIT %d", p.QualifiedName(dataset, table), filterClause, limit)
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
	ds := p.datasetRef(p.dataset)
	if _, err := ds.Metadata(ctx); err != nil {
		return fmt.Errorf("bigquery: cannot access dataset %s: %w", p.dataset, err)
	}

	// Test 2: Can run a simple query
	q := p.client.Query("SELECT 1 as test")
	// SELECT 1 has no table references so DefaultProjectID is
	// irrelevant here, but applying it keeps the validation path
	// identical to Query() so a misconfigured cross-project setup
	// still surfaces a useful error at validation time rather than
	// at first real query.
	p.applyDefaultProject(q)
	q.Location = p.config.Location
	it, err := q.Read(ctx)
	if err != nil {
		return fmt.Errorf("bigquery: cannot execute queries: %w", err)
	}
	_ = it // just check it doesn't error

	return nil
}

// ValidateSQL asks BigQuery to parse + plan the statement via the
// native dry-run API (QueryConfig.DryRun = true). The job is
// configured but never scheduled; BigQuery returns parser /
// resolver errors immediately. Bytes are not scanned, no slot
// time is consumed, and the call shares its quota with regular
// metadata operations.
//
// Empty or whitespace-only input is rejected at the caller
// boundary so the BigQuery API call always sees a non-trivial
// payload.
func (p *BigQueryProvider) ValidateSQL(ctx context.Context, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("bigquery: empty SQL")
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	q := p.client.Query(sql)
	p.applyDefaultProject(q)
	q.DryRun = true
	q.Location = p.config.Location
	if _, err := q.Run(ctx); err != nil {
		return fmt.Errorf("bigquery: validate SQL: %w", err)
	}
	return nil
}

func (p *BigQueryProvider) HealthCheck(ctx context.Context) error {
	ds := p.datasetRef(p.dataset)
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
	p.applyDefaultProject(q)
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
