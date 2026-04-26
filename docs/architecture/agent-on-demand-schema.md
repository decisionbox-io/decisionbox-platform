# Agent On-Demand Schema Retrieval

> **Version**: 0.4.0

## Why

Before v0.4 the exploration prompt always carried a Level-0 catalog **plus**
a Level-1 block — full column lists and 3 sample rows for the top-K tables
the retriever matched up front. That payload sat at the top of the system
prompt for every step of the run, and every step also appended the previous
turn's full SQL result to the conversation. On long runs against a wide
warehouse the two sources combined: a customer report against ~2K tables
hit the Bedrock 1M-token limit at step 98 with a `prompt is too long:
1002763 tokens > 1000000 maximum` error, killing an otherwise healthy
discovery.

The v0.4 architecture fixes this at the source rather than papering over it
with token-based trimming or summarisation. Two new actions let the model
fetch L1 detail only for tables it actually wants to use:

| Action          | What it does                                                | Per-call limit | Per-run budget |
|-----------------|-------------------------------------------------------------|----------------|----------------|
| `lookup_schema` | Returns columns + 3 sample rows for fully-qualified refs    | 10 tables      | 30 calls       |
| `search_tables` | Semantic search against the per-project Qdrant collection   | TopK ≤ 30      | 30 calls       |

Both numbers are constants in `services/agent/internal/ai/schema_provider.go`
(`MaxLookupTablesPerCall`, `DefaultMaxLookupsPerRun`, `DefaultMaxSearchesPerRun`,
`DefaultSearchTopK`, `MaxSearchTopK`) and are duplicated verbatim in every
domain-pack exploration prompt. Changing a constant requires updating both.

## Token math

| Slice                                | Pre-v0.4                           | Post-v0.4                              |
|--------------------------------------|------------------------------------|----------------------------------------|
| Schema in system prompt              | catalog + L1 block (~80–200K)      | catalog only (~5–30K)                  |
| Per-step user message                | ~1 KB SQL result                   | ~1.2 KB (mix of SQL + lookup + search) |
| Per-step assistant output            | ~600 tokens                        | ~600 tokens                            |
| 100-step worst case                  | system + 100×(user + assistant)    | system + 100×(user + assistant)        |

The L1 dump moves out of the static system prompt and into the per-step
user messages — but only for tables the model touches. Models stop pulling
L1 detail for tables they never reference, and dedup on already-fetched
tables prevents repeats from spending budget twice.

## Flow

```
Orchestrator
  │
  ├─ DiscoverSchemas + cache to Mongo (unchanged)
  │
  ├─ Build catalog (Level-0)
  │    └─ Render `{{SCHEMA_INFO}}` from per-project schemas map
  │
  ├─ Build CacheSchemaProvider
  │    ├─ schemas map: in-memory (Mongo cache) — Lookup never hits warehouse
  │    └─ retriever:   per-project Qdrant collection — Search uses cosine + rerank
  │
  └─ NewExplorationEngine(SchemaProvider: ...)
       │
       └─ ExplorationLoop:
            ├─ Step 1: LLM emits {"lookup_schema": ["sales.orders", ...]}
            │            → engine calls SchemaProvider.Lookup
            │            → result formatted into next user message
            │            → debits lookupsUsed budget
            │
            ├─ Step 2: LLM emits {"search_tables": "cart abandoned events"}
            │            → engine calls SchemaProvider.Search
            │            → result formatted into next user message
            │            → debits searchesUsed budget
            │
            └─ Step 3: LLM emits {"query": "SELECT ..."}
                       → existing query_data path
```

## Key files

| File                                                                         | Role                                                                              |
|------------------------------------------------------------------------------|-----------------------------------------------------------------------------------|
| `services/agent/internal/ai/schema_provider.go`                              | `SchemaProvider` interface, `Lookup*` / `Search*` types, run-level constants      |
| `services/agent/internal/ai/exploration.go`                                  | Action parser + budget enforcement + result formatters                            |
| `services/agent/internal/discovery/cache_schema_provider.go`                 | Production `SchemaProvider` — in-memory schemas map + Qdrant retriever            |
| `services/agent/internal/discovery/schema_context.go`                        | Catalog-only renderer; old `BuildOnce` (catalog + L1) replaced by `BuildCatalog`  |
| `services/agent/internal/discovery/orchestrator.go`                          | Wires the catalog + provider into the engine, substitutes `{{SCHEMA_INFO}}`       |
| `services/agent/internal/database/run_repo.go`                               | Telemetry: `IncrementSchemaActionCalls(ctx, runID, "lookup_schema"\|"search_tables")` |
| `domain-packs/{gaming,social,ecommerce,system-test}/prompts/base/exploration.md` | Action contract — exact JSON shapes + budgets                                |

## Telemetry

Per-action counters live on `discovery_runs`:

- `schema_lookup_calls` — increments on every `lookup_schema` step
- `schema_search_calls` — increments on every `search_tables` step

The pre-v0.4 single counter `schema_inspect_table_calls` is gone. Dashboards
that referenced it must move to the per-action counters.

## Tests

- `services/agent/internal/ai/exploration_actions_test.go` — parseAction
  shapes, normaliseRefs, formatters, executeLookupSchema (success / dedup /
  partial dedup / per-call cap / budget exhausted / provider error / no
  provider), executeSearchTables (success / budget / topK clamp / default /
  empty / error / no provider), wiring defaults, end-to-end scripted run.
- `services/agent/internal/discovery/cache_schema_provider_test.go` —
  ref resolution (qualified, bare unambiguous, bare ambiguous → NotFound,
  case-insensitive), dedup, per-call truncation, column / sample limits,
  context cancellation, Search forwarding (projectID, topK, vector,
  RowCountPrior), defaults, error paths.

## Migration

DecisionBox is pre-1.0; no backwards-compatibility shims were carried.
Existing customised prompts on existing projects must be re-saved from the
new domain-pack defaults to pick up the action contract — see the runbook
in `PLAN-SCHEMA-RETRIEVAL.md` for the migration script.
