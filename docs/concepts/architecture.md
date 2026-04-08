# Architecture

> **Version**: 0.3.0

DecisionBox has three services, one database, and a plugin system for extensibility. There are no message queues, caches, or event streams — just MongoDB.

## System Overview

```
┌─────────────────────────────────────────────────────────┐
│                   Dashboard (Next.js 16)                 │
│                  http://localhost:3000                    │
│                                                          │
│   - Project management (create, edit, delete)            │
│   - Discovery results (insights table, recommendations)  │
│   - Live progress (real-time step feed)                  │
│   - Prompt editor (markdown, per-project)                │
│   - Settings (warehouse, LLM, secrets, schedule)         │
│   - Feedback (like/dislike insights + recommendations)   │
│                                                          │
│   All /api/* requests proxied to API via Next.js         │
│   middleware (server-side, API never exposed publicly)    │
└──────────────────────────┬──────────────────────────────┘
                           │ HTTP proxy (runtime, not build-time)
                           ▼
┌─────────────────────────────────────────────────────────┐
│                      API (Go, net/http)                   │
│                  http://localhost:8080                    │
│                                                          │
│   - REST endpoints (projects, discoveries, prompts,      │
│     feedback, pricing, secrets, health)                   │
│   - Spawns agent as subprocess (local) or K8s Job (prod) │
│   - Reads provider metadata for dynamic UI forms         │
│   - Seeds pricing from registered providers              │
│   - No authentication (open-source, internal use)        │
└──────┬──────────────────────────────────────┬───────────┘
       │ exec.Command / K8s Job               │ MongoDB driver
       ▼                                      ▼
┌──────────────────────┐              ┌──────────────────┐
│   Agent (Go binary)  │              │    MongoDB 7+    │
│                      │──────write──▶│                  │
│   Autonomous AI      │              │  Collections:    │
│   data explorer      │              │  - projects      │
│                      │              │  - discoveries   │
│   Components:        │              │  - discovery_runs│
│   - LLM provider     │              │  - feedback      │
│   - Warehouse prov.  │              │  - secrets       │
│   - Domain pack      │              │  - pricing       │
│   - Secret provider  │              │  - project_ctx   │
│   - Prompts          │              │  - debug_logs    │
│   - Vector store     │              │                  │
└──────────┬───────────┘              └──────────────────┘
           │ SQL queries                    ▲
           ▼                                │ search
┌──────────────────────┐              ┌──────────────────┐
│   Data Warehouse     │              │   Qdrant         │
│                      │              │   (Vector Store)  │
│   BigQuery           │              │                  │
│   Amazon Redshift    │◀─────search──│   Collections:   │
│   (read-only access) │              │   - insights     │
└──────────────────────┘              └──────────────────┘
```

## Components

### Dashboard

The web UI. Built with Next.js 16, React 19, TypeScript, and Mantine 8.

**Key design decision:** The dashboard proxies all `/api/*` requests to the API via [Next.js middleware](https://nextjs.org/docs/app/building-your-application/routing/middleware). The API is never exposed publicly. This means:
- No CORS issues
- Single ingress point (only the dashboard needs a public URL)
- API URL is a runtime environment variable (`API_URL`), not baked at build time
- One Docker image works across all environments

### API

The REST API. Built with Go's standard `net/http` package (no frameworks). Handles:

- **Project CRUD** — Create, read, update, delete projects
- **Discovery management** — Trigger runs, list results, get status
- **Agent spawning** — Starts the agent as a subprocess or Kubernetes Job
- **Provider metadata** — Returns available LLM/warehouse providers with config field definitions for dynamic UI forms
- **Prompts** — Read/write per-project prompt overrides
- **Secrets** — Per-project encrypted key storage
- **Feedback** — Like/dislike on insights and recommendations
- **Health** — Liveness and readiness probes

The API has **no authentication** in v0.1.0. It's designed for internal use — the dashboard sits in front of it.

### Agent

The autonomous AI data explorer. A standalone Go binary that:

1. Loads project configuration from MongoDB
2. Initializes providers (LLM, warehouse, secrets, domain pack)
3. Discovers warehouse table schemas
4. Runs autonomous exploration (AI writes SQL, executes, iterates)
5. Analyzes results per analysis area
6. Validates insights against warehouse data
7. Generates recommendations
8. Saves results to MongoDB
9. Updates run status throughout

The agent is **stateless** — it reads everything from MongoDB and the domain pack files. It can run as:
- A **subprocess** spawned by the API (local development, `RUNNER_MODE=subprocess`)
- A **Kubernetes Job** created by the API (production, `RUNNER_MODE=kubernetes`)

### MongoDB

The only infrastructure dependency. Stores:

| Collection | Purpose |
|-----------|---------|
| `projects` | Project configuration (name, warehouse, LLM, schedule, profile, prompts) |
| `discoveries` | Discovery results (insights, recommendations, logs, validation) |
| `discovery_runs` | Live run status (phase, progress, steps, errors) |
| `feedback` | User feedback on insights and recommendations |
| `secrets` | Encrypted per-project secrets (API keys, credentials) |
| `pricing` | LLM and warehouse pricing configuration |
| `project_context` | Rolling context (previous insights, patterns) |
| `discovery_debug_logs` | Detailed debug logs (TTL: 30 days) |

All collections and indexes are created automatically on API startup (idempotent).

### Qdrant (Vector Store)

An optional infrastructure dependency for semantic search and discovery. Stores high-dimensional vector embeddings generated from your data.

- **Storage** — Collection of points (vector + metadata)
- **Search** — Similarity search (HNSW index)
- **API** — Both API and Agent connect to Qdrant via gRPC (port 6334)

When `QDRANT_URL` is set, the Agent automatically embeds and indexes insights, allowing the API to perform similarity searches for recommendations and related patterns.

## Plugin Architecture

DecisionBox is built on six plugin systems. Each uses the same pattern: plugins register themselves via `init()` functions, and services select or apply them at runtime.

### How Registration Works

```go
// In a provider package (e.g., providers/llm/claude/provider.go)
func init() {
    llm.Register("claude", func(cfg llm.ProviderConfig) (llm.Provider, error) {
        return NewClaudeProvider(cfg["api_key"], cfg["model"])
    })
}

// In a service (e.g., services/agent/main.go)
import _ "github.com/decisionbox-io/decisionbox/providers/llm/claude" // triggers init()

provider, err := llm.NewProvider("claude", llm.ProviderConfig{
    "api_key": "sk-ant-...",
    "model":   "claude-sonnet-4-20250514",
})
```

Services import provider packages with blank imports (`_`). The `init()` function runs at startup and registers the provider factory. The service then creates providers by name.

### Six Plugin Types

| Plugin | Interface / Hook | Purpose | Shipped Implementations |
|--------|-----------------|---------|------------------------|
| **LLM** | `llm.Provider` | AI model access | claude, openai, ollama, vertex-ai, bedrock, azure-foundry |
| **Warehouse** | `warehouse.Provider` | Data warehouse access | bigquery, redshift, snowflake, postgres, databricks |
| **Secrets** | `secrets.Provider` | Encrypted key storage | mongodb, gcp, aws, azure |
| **Domain Pack** | `domainpack.DiscoveryPack` | Domain-specific analysis | gaming, social, ecommerce |
| **Warehouse Middleware** | `warehouse.RegisterMiddleware()` | Wrap warehouse providers | (none shipped — extension point) |
| **HTTP Middleware** | `apiserver.RegisterGlobalMiddleware()` | Wrap API requests | (none shipped — extension point) |

The first four are provider plugins that implement an interface and register a factory.
The last two are middleware hooks that wrap existing providers or HTTP handlers with additional logic.

```go
// Warehouse middleware — wraps the warehouse provider
warehouse.RegisterMiddleware("my-plugin", func(p warehouse.Provider) warehouse.Provider {
    return &myWrappedProvider{inner: p}
})

// HTTP middleware — wraps all API requests
apiserver.RegisterGlobalMiddleware(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // custom logic before/after
        next.ServeHTTP(w, r)
    })
})
```

Custom builds can import `agentserver.Run()` or `apiserver.Run()` and register middleware via `init()` blank imports before calling `Run()`.

For details on implementing providers, see:
- [Adding LLM Providers](../guides/adding-llm-providers.md)
- [Adding Warehouse Providers](../guides/adding-warehouse-providers.md)
- [Adding Secret Providers](../guides/adding-secret-providers.md)
- [Creating Domain Packs](../guides/creating-domain-packs.md)

## Data Flow

### Discovery Run

```
1. User clicks "Run discovery" in Dashboard
   ↓
2. Dashboard sends POST /api/v1/projects/{id}/discover
   ↓
3. API creates a run record in MongoDB (status: pending)
   ↓
4. API spawns agent (subprocess or K8s Job)
   ↓
5. Agent loads project config, secrets, prompts from MongoDB
   ↓
6. Agent initializes LLM provider, warehouse provider, domain pack
   ↓
7. Agent discovers warehouse schemas (LIST TABLES, GET SCHEMA)
   ↓
8. Agent runs exploration:
   a. Sends schema + prompt to LLM
   b. LLM generates SQL query
   c. Agent executes query against warehouse
   d. Agent sends results back to LLM
   e. LLM generates next query based on results
   f. Repeat for N steps (default: 100)
   g. Each step written to run record in MongoDB (live progress)
   ↓
9. Agent runs analysis per area:
   a. Loads area-specific prompt (e.g., analysis_churn.md)
   b. Feeds relevant exploration results to LLM
   c. LLM generates insights (JSON)
   d. Agent parses and assigns IDs
   ↓
10. Agent validates insights:
    a. For each insight with affected_count
    b. Generates verification SQL
    c. Executes against warehouse
    d. Compares claimed vs verified count
    ↓
11. Agent generates recommendations:
    a. Feeds all validated insights to LLM
    b. LLM generates recommendations with related_insight_ids
    ↓
12. Agent saves DiscoveryResult to MongoDB
    ↓
13. Agent updates run status to "completed" (or "failed")
    ↓
14. Dashboard polls for status, shows completed results
```

### Prompt Flow

```
Domain Pack provides template files (.md)
  ↓
Project-level overrides stored in MongoDB (editable via dashboard)
  ↓
Agent loads prompts (project overrides take priority)
  ↓
Agent substitutes template variables:
  {{PROFILE}}         → JSON-encoded project profile
  {{PREVIOUS_CONTEXT}} → Previous discoveries + feedback
  {{SCHEMA_INFO}}      → Discovered table schemas
  {{DATASET}}          → Dataset names
  {{FILTER}}           → WHERE clause for multi-tenant
  {{QUERY_RESULTS}}    → Exploration query results (per area)
  ...
  ↓
Rendered prompt sent to LLM
```

See [Prompts](prompts.md) for the full variable reference.

## Deployment Models

### Local Development

```
Dashboard (npm run dev)  →  API (go run .)  →  Agent (subprocess)
                                ↕
                            MongoDB (Docker)
```

### Docker Compose

```
Dashboard (container)  →  API (container)  →  Agent (subprocess inside API container)
                               ↕
                           MongoDB (container)
```

### Kubernetes (Production)

```
Dashboard (Deployment)  →  API (Deployment)  →  Agent (K8s Job per discovery)
                                ↕
                           MongoDB (StatefulSet or external)
```

In Kubernetes mode (`RUNNER_MODE=kubernetes`), the API creates a K8s Job for each discovery run instead of spawning a subprocess. The agent runs as an isolated container with configurable CPU/memory limits.

For deployment guides, see:
- [Docker Compose](../deployment/docker.md) — single-server deployment
- [Kubernetes (Helm)](../deployment/kubernetes.md) — production deployment on any K8s cluster
- [Terraform GCP](../deployment/terraform-gcp.md) — automated GKE cluster provisioning

## Security Model

### v0.1.0 (Current)

- **No authentication** — Designed for internal/single-user deployment
- **API not publicly exposed** — Dashboard proxies all requests
- **Secrets encrypted at rest** — AES-256-GCM when using MongoDB provider with `SECRET_ENCRYPTION_KEY`
- **Warehouse read-only** — Agent only executes SELECT queries
- **Per-project isolation** — Each project has its own secrets, prompts, discoveries

### Future

- Authentication (OAuth2 / Auth0)
- Multi-user RBAC
- API key authentication for external integrations
