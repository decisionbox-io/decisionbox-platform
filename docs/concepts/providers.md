# Providers

> **Version**: 0.1.0

Providers are DecisionBox's plugin system for external services. Instead of hardcoding support for specific LLMs, warehouses, or secret managers, DecisionBox defines interfaces and lets provider packages implement them.

## The Pattern

All three provider types follow the same pattern:

1. **Interface** — Defined in `libs/go-common/` (e.g., `llm.Provider`)
2. **Registry** — Central map of name → factory function
3. **Registration** — Provider packages call `Register()` in their `init()` function
4. **Selection** — Services create providers by name at runtime

```go
// 1. Interface (libs/go-common/llm/provider.go)
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// 2. Registry (libs/go-common/llm/registry.go)
func Register(name string, factory ProviderFactory) { ... }
func NewProvider(name string, cfg ProviderConfig) (Provider, error) { ... }

// 3. Registration (providers/llm/claude/provider.go)
func init() {
    llm.Register("claude", func(cfg llm.ProviderConfig) (llm.Provider, error) {
        return NewClaudeProvider(cfg["api_key"], cfg["model"])
    })
}

// 4. Selection (services/agent/main.go)
import _ "github.com/decisionbox-io/decisionbox/providers/llm/claude"

provider, err := llm.NewProvider("claude", llm.ProviderConfig{
    "api_key": apiKey,
    "model":   "claude-sonnet-4-20250514",
})
```

The blank import (`import _ "..."`) triggers the `init()` function which registers the provider. The service then creates it by name.

## Provider Metadata

Each provider registers metadata alongside its factory function. This metadata powers the dashboard's dynamic forms — no hardcoded provider lists.

```go
llm.RegisterWithMeta("claude", factory, llm.ProviderMeta{
    Name:        "Claude (Anthropic)",
    Description: "Anthropic Claude API - direct access",
    ConfigFields: []llm.ConfigField{
        {Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "sk-ant-..."},
        {Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-20250514"},
    },
    DefaultPricing: map[string]llm.TokenPricing{
        "claude-sonnet-4": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
    },
})
```

The API returns this metadata via `GET /api/v1/providers/llm` and `GET /api/v1/providers/warehouse`. The dashboard renders dynamic configuration forms from the `ConfigFields` array — no UI code changes needed when a new provider is added.

## Three Provider Types

### LLM Providers

**Interface:** `llm.Provider` — One method: `Chat(ctx, request) → response`

**Purpose:** Send prompts to an AI model and get text responses.

| Provider | ID | Auth | Models |
|----------|----|------|--------|
| Anthropic Claude | `claude` | API key | claude-sonnet-4, claude-opus-4, claude-haiku-4-5 |
| OpenAI | `openai` | API key | gpt-4o, gpt-4o-mini |
| Ollama | `ollama` | None (local) | Any model: llama3.1, qwen2.5, mistral, etc. |
| Google Vertex AI | `vertex-ai` | GCP ADC | Claude + Gemini (via Google) |
| AWS Bedrock | `bedrock` | AWS credentials | Claude + Llama + Mistral (via AWS) |

**Location:** `providers/llm/{provider-name}/`

**Config:** Passed as `map[string]string`. Common fields:
- `api_key` — API key (Claude, OpenAI)
- `model` — Model identifier
- `timeout_seconds` — Per-call timeout (default: 300)
- Provider-specific: `project_id` + `location` (Vertex AI), `region` (Bedrock), `host` (Ollama)

See [Adding LLM Providers](../guides/adding-llm-providers.md) to implement your own.

### Warehouse Providers

**Interface:** `warehouse.Provider` — Query execution, table listing, schema inspection.

**Purpose:** Execute SQL queries against a data warehouse (read-only).

| Provider | ID | Auth | SQL Dialect |
|----------|----|------|-------------|
| Google BigQuery | `bigquery` | GCP ADC or SA key | BigQuery Standard SQL |
| Amazon Redshift | `redshift` | AWS credentials | PostgreSQL-compatible |

**Location:** `providers/warehouse/{provider-name}/`

**Interface methods:**

| Method | Purpose |
|--------|---------|
| `Query(ctx, sql, params)` | Execute a SQL query, return rows |
| `ListTables(ctx)` | List all tables in default dataset |
| `ListTablesInDataset(ctx, dataset)` | List tables in a specific dataset |
| `GetTableSchema(ctx, table)` | Get column names, types, nullable |
| `GetTableSchemaInDataset(ctx, dataset, table)` | Get schema for a specific dataset.table |
| `GetDataset()` | Return default dataset name |
| `SQLDialect()` | Return SQL dialect description |
| `SQLFixPrompt()` | Return warehouse-specific SQL fix instructions |
| `ValidateReadOnly(ctx)` | Verify read-only access works |
| `HealthCheck(ctx)` | Check warehouse connectivity |
| `Close()` | Clean up connections |

**Optional interface:** `warehouse.CostEstimator` — For providers that support dry-run cost estimation (BigQuery does, Redshift partially).

```go
type CostEstimator interface {
    DryRun(ctx context.Context, query string) (*DryRunResult, error)
}
```

See [Adding Warehouse Providers](../guides/adding-warehouse-providers.md) to implement your own.

### Secret Providers

**Interface:** `secrets.Provider` — Get, Set, List secrets (no Delete).

**Purpose:** Store and retrieve encrypted per-project secrets (API keys, credentials).

| Provider | ID | Auth | Storage |
|----------|----|------|---------|
| MongoDB (default) | `mongodb` | Encryption key env var | Encrypted MongoDB collection |
| Google Cloud | `gcp` | GCP ADC | GCP Secret Manager |
| AWS | `aws` | AWS credentials | AWS Secrets Manager |

**Location:** `providers/secrets/{provider-name}/`

**Interface methods:**

| Method | Purpose |
|--------|---------|
| `Get(ctx, projectID, key)` | Retrieve a secret value |
| `Set(ctx, projectID, key, value)` | Create or update a secret |
| `List(ctx, projectID)` | List secret keys with masked values |

**Design decisions:**
- **No Delete** — Secrets are removed manually via cloud console, CLI, or direct database access. This is intentional: preventing accidental deletion via API.
- **Per-project scoping** — All secrets are namespaced by `{namespace}/{projectID}/{key}`. The namespace (default: `decisionbox`) prevents conflicts in shared cloud accounts.
- **Masked listing** — `List()` returns keys with masked values (first 6 + last 4 characters with `***` in between). Full values are never returned via the API.

See [Adding Secret Providers](../guides/adding-secret-providers.md) to implement your own.

## How Services Use Providers

### Agent

The agent imports all providers and selects based on project configuration:

```go
// services/agent/main.go

// Import all providers (triggers init() registration)
import (
    _ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
    _ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
    _ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
    _ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
    _ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
    _ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
    _ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"
    _ "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
    _ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"
    _ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"
)

// Create providers from project config
secretProv, _ := secrets.NewProvider(secretsCfg)
apiKey, _ := secretProv.Get(ctx, projectID, "llm-api-key")

llmProv, _ := llm.NewProvider(project.LLM.Provider, llm.ProviderConfig{
    "api_key": apiKey,
    "model":   project.LLM.Model,
})

whProv, _ := warehouse.NewProvider(project.Warehouse.Provider, warehouse.ProviderConfig{
    "project_id": project.Warehouse.ProjectID,
    "dataset":    project.Warehouse.Datasets[0],
})
```

### API

The API imports providers for two reasons:
1. **Metadata** — Returns provider lists with config fields for the dashboard
2. **Pricing** — Seeds default pricing from provider registrations

```go
// services/api/main.go
import (
    _ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
    // ... same imports as agent
)

// GET /api/v1/providers/llm returns:
// [
//   { "id": "claude", "name": "Claude (Anthropic)",
//     "config_fields": [{"key": "api_key", ...}, {"key": "model", ...}],
//     "default_pricing": {"claude-sonnet-4": {"input_per_million": 3.0, ...}} },
//   { "id": "openai", ... },
//   ...
// ]
```

## Adding a New Provider

To add support for a new LLM, warehouse, or secret manager:

1. Create a package in the appropriate `providers/` directory
2. Implement the interface
3. Register with metadata in `init()`
4. Import in agent + API `main.go`
5. Write tests

No other code changes needed — the dashboard automatically shows the new provider in dropdowns and renders config forms from metadata.

See the implementation guides:
- [Adding LLM Providers](../guides/adding-llm-providers.md)
- [Adding Warehouse Providers](../guides/adding-warehouse-providers.md)
- [Adding Secret Providers](../guides/adding-secret-providers.md)

## Next Steps

- [Prompts](prompts.md) — Template variables and prompt customization
- [Domain Packs](domain-packs.md) — How domain-specific analysis works
- [Architecture](architecture.md) — System overview
