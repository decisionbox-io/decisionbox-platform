# Adding Warehouse Providers

> **Version**: 0.1.0

This guide shows how to add support for a new SQL data warehouse (e.g., Snowflake, PostgreSQL, Databricks).

## Interface

```go
// libs/go-common/warehouse/provider.go
type Provider interface {
    Query(ctx context.Context, query string, params map[string]interface{}) (*QueryResult, error)
    ListTables(ctx context.Context) ([]string, error)
    ListTablesInDataset(ctx context.Context, dataset string) ([]string, error)
    GetTableSchema(ctx context.Context, table string) (*TableSchema, error)
    GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*TableSchema, error)
    GetDataset() string
    SQLDialect() string
    SQLFixPrompt() string
    ValidateReadOnly(ctx context.Context) error
    HealthCheck(ctx context.Context) error
    Close() error
}
```

### Method Details

| Method | Purpose | Notes |
|--------|---------|-------|
| `Query` | Execute a SQL SELECT query | Return rows as `[]map[string]interface{}`. Must be read-only. |
| `ListTables` | List tables in default dataset | Return fully qualified names (e.g., `dataset.table`) |
| `ListTablesInDataset` | List tables in a specific dataset | For multi-dataset projects |
| `GetTableSchema` | Get column definitions | Return column name, normalized type, nullable |
| `GetTableSchemaInDataset` | Get schema for dataset.table | For multi-dataset projects |
| `GetDataset` | Return default dataset name | Used in prompts |
| `SQLDialect` | Return SQL dialect description | E.g., `"PostgreSQL 15"`, `"Snowflake SQL"` |
| `SQLFixPrompt` | Return warehouse-specific SQL fix prompt | Instructions for the AI to fix SQL errors |
| `ValidateReadOnly` | Verify read access works | Run a simple query to confirm connectivity |
| `HealthCheck` | Quick connectivity check | Used by health endpoints |
| `Close` | Clean up connections | Called on shutdown |

### Optional: Cost Estimation

If your warehouse supports dry-run or EXPLAIN, implement `CostEstimator`:

```go
type CostEstimator interface {
    DryRun(ctx context.Context, query string) (*DryRunResult, error)
}
```

`DryRunResult` includes `EstimatedBytesProcessed` and `EstimatedRowsProcessed`.

### Return Types

**QueryResult:**
```go
type QueryResult struct {
    Columns []string
    Rows    []map[string]interface{}
}
```

**TableSchema:**
```go
type TableSchema struct {
    Name    string
    Columns []ColumnSchema
    RowCount int64
}

type ColumnSchema struct {
    Name     string
    Type     string  // Normalized: STRING, INT64, FLOAT64, BOOL, TIMESTAMP, DATE, BYTES, RECORD
    Nullable bool
}
```

**Type normalization:** Convert warehouse-native types to normalized types:
- `VARCHAR`, `TEXT`, `CHAR` → `STRING`
- `INT`, `INTEGER`, `BIGINT`, `SMALLINT` → `INT64`
- `FLOAT`, `DOUBLE`, `DECIMAL`, `NUMERIC` → `FLOAT64`
- `BOOLEAN` → `BOOL`
- `TIMESTAMP`, `TIMESTAMPTZ`, `DATETIME` → `TIMESTAMP`
- `DATE` → `DATE`

## Step 1: Create the Package

```bash
mkdir -p providers/warehouse/snowflake
cd providers/warehouse/snowflake
go mod init github.com/decisionbox-io/decisionbox/providers/warehouse/snowflake
```

## Step 2: Implement the Provider

```go
// providers/warehouse/snowflake/provider.go
package snowflake

import (
    "context"
    "fmt"

    gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func init() {
    gowarehouse.RegisterWithMeta("snowflake", func(cfg gowarehouse.ProviderConfig) (gowarehouse.Provider, error) {
        account := cfg["account"]
        if account == "" {
            return nil, fmt.Errorf("snowflake: account is required")
        }

        // Apply authentication based on selected method.
        // cfg["credentials_json"] is populated by the agent from the secret provider.
        // cfg["auth_method"] is the ID selected by the user in the dashboard.
        creds := cfg["credentials_json"]
        switch cfg["auth_method"] {
        case "key_pair":
            // Parse PEM key and configure JWT auth
        case "password", "":
            // Use creds as password
        default:
            return nil, fmt.Errorf("snowflake: unsupported auth method %q", cfg["auth_method"])
        }

        return &SnowflakeProvider{
            account:   account,
            warehouse: cfg["warehouse"],
            database:  cfg["database"],
            schema:    cfg["dataset"],
        }, nil
    }, gowarehouse.ProviderMeta{
        Name:        "Snowflake",
        Description: "Snowflake cloud data warehouse",
        ConfigFields: []gowarehouse.ConfigField{
            {Key: "account", Label: "Account", Required: true, Type: "string", Placeholder: "myorg-myaccount"},
            {Key: "warehouse", Label: "Warehouse", Required: true, Type: "string", Default: "COMPUTE_WH"},
            {Key: "database", Label: "Database", Required: true, Type: "string"},
            {Key: "dataset", Label: "Schema", Required: true, Type: "string", Default: "PUBLIC"},
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
                Description: "RSA private key authentication.",
                Fields: []gowarehouse.ConfigField{
                    {Key: "credentials", Label: "PEM Private Key", Required: true, Type: "credential"},
                },
            },
        },
        DefaultPricing: &gowarehouse.WarehousePricing{
            CostModel: "per_second",
        },
    })
}

type SnowflakeProvider struct {
    account   string
    warehouse string
    database  string
    schema    string
    // Add your client here
}

func (p *SnowflakeProvider) Query(ctx context.Context, query string, params map[string]interface{}) (*gowarehouse.QueryResult, error) {
    // Execute query, return results
    // IMPORTANT: Only allow SELECT queries (read-only)
    return nil, fmt.Errorf("not implemented")
}

func (p *SnowflakeProvider) ListTables(ctx context.Context) ([]string, error) {
    // SELECT table_name FROM information_schema.tables WHERE table_schema = ...
    return nil, fmt.Errorf("not implemented")
}

func (p *SnowflakeProvider) ListTablesInDataset(ctx context.Context, dataset string) ([]string, error) {
    return p.ListTables(ctx) // If single-schema, delegate
}

func (p *SnowflakeProvider) GetTableSchema(ctx context.Context, table string) (*gowarehouse.TableSchema, error) {
    // SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_name = ...
    // Normalize types to STRING, INT64, FLOAT64, BOOL, TIMESTAMP, DATE
    return nil, fmt.Errorf("not implemented")
}

func (p *SnowflakeProvider) GetTableSchemaInDataset(ctx context.Context, dataset, table string) (*gowarehouse.TableSchema, error) {
    return p.GetTableSchema(ctx, table)
}

func (p *SnowflakeProvider) GetDataset() string {
    return p.schema
}

func (p *SnowflakeProvider) SQLDialect() string {
    return "Snowflake SQL"
}

func (p *SnowflakeProvider) SQLFixPrompt() string {
    return `When fixing SQL for Snowflake:
- Use double quotes for identifiers with special characters
- LIMIT goes at the end (no TOP)
- Date functions: DATEADD, DATEDIFF, DATE_TRUNC
- String functions: LIKE (case-sensitive), ILIKE (case-insensitive)
- Use :: for type casting (e.g., column::DATE)
`
}

func (p *SnowflakeProvider) ValidateReadOnly(ctx context.Context) error {
    _, err := p.Query(ctx, "SELECT 1", nil)
    return err
}

func (p *SnowflakeProvider) HealthCheck(ctx context.Context) error {
    return p.ValidateReadOnly(ctx)
}

func (p *SnowflakeProvider) Close() error {
    // Close connections
    return nil
}
```

### SQL Fix Prompt

The `SQLFixPrompt()` is crucial. When the AI writes invalid SQL, the agent feeds the error + your fix prompt to the LLM for correction. Include:
- Warehouse-specific syntax rules
- Common mistakes and their corrections
- Type casting syntax
- Date function differences

## Step 3: Register and Test

Same pattern as LLM providers:

1. Import in `services/agent/main.go` and `services/api/main.go`
2. Add `replace` directives in go.mod files
3. Update Dockerfiles with COPY line
4. Write unit tests (registration, config validation, type normalization)
5. Write integration tests (skip without credentials)
6. Add to Makefile test targets

## Checklist

- [ ] All 11 interface methods implemented
- [ ] Type normalization (warehouse types → STRING, INT64, FLOAT64, etc.)
- [ ] System tables filtered from ListTables (e.g., `pg_*`, `information_schema`)
- [ ] SQLFixPrompt includes warehouse-specific SQL rules
- [ ] ConfigFields includes all connection config options
- [ ] AuthMethods declared with `type: "credential"` fields for secrets
- [ ] Factory uses `switch cfg["auth_method"]` with a `default` error case
- [ ] ProviderMeta includes DefaultPricing
- [ ] Imported in agent + API, replace directives, Dockerfile COPY
- [ ] Unit tests: auth method registration, factory validation, unsupported method error
- [ ] Integration tests (skip without credentials, opt-in via `INTEGRATION_TEST_*` env vars)

## Next Steps

- [Providers Concept](../concepts/providers.md) — Plugin architecture overview
- [Configuring Warehouses](configuring-warehouse.md) — How users set up warehouses
