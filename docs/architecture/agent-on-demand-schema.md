# Agent On-Demand Schema Retrieval

> **See also**: [agent-analysis-compaction.md](agent-analysis-compaction.md)
> applies the same prompt-bounding pattern to the analysis phase
> (vector-ranked step selection + per-step compact digest).

## Why

A naïve exploration prompt that carries a Level-0 catalog **plus** a Level-1
block — full column lists and 3 sample rows for the top-K tables the
retriever matched up front — sits at the top of the system prompt for every
step of the run, and every step also appends the previous turn's full SQL
result to the conversation. On long runs against a wide warehouse the two
sources combine and bump into model token limits: a customer report against
~2K tables hit the Bedrock 1M-token limit at step 98 with a
`prompt is too long: 1002763 tokens > 1000000 maximum` error, killing an
otherwise healthy discovery.

The on-demand schema architecture fixes this at the source rather than
papering over it with token-based trimming or summarisation. Two actions let
the model fetch L1 detail only for tables it actually wants to use:

| Action          | What it does                                                | Per-call limit | Per-run budget |
|-----------------|-------------------------------------------------------------|----------------|----------------|
| `lookup_schema` | Returns columns + 3 sample rows for fully-qualified refs    | 10 tables      | 30 calls       |
| `search_tables` | Semantic search against the per-project Qdrant collection   | TopK ≤ 30      | 30 calls       |

Both numbers are constants in `services/agent/internal/ai/schema_provider.go`
(`MaxLookupTablesPerCall`, `DefaultMaxLookupsPerRun`, `DefaultMaxSearchesPerRun`,
`DefaultSearchTopK`, `MaxSearchTopK`) and are duplicated verbatim in every
domain-pack exploration prompt. Changing a constant requires updating both.

## Token math

| Slice                                | Without on-demand schema          | With on-demand schema                  |
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
| `services/agent/internal/discovery/schema_context.go`                        | Catalog-only renderer (`BuildCatalog`)                                            |
| `services/agent/internal/discovery/orchestrator.go`                          | Wires the catalog + provider into the engine, substitutes `{{SCHEMA_INFO}}`       |
| `services/agent/internal/database/run_repo.go`                               | Telemetry: `IncrementSchemaActionCalls(ctx, runID, "lookup_schema"\|"search_tables")` |
| `domain-packs/{gaming,social,ecommerce,system-test}/prompts/base/exploration.md` | Action contract — exact JSON shapes + budgets                                |

## Telemetry

Per-action counters live on `discovery_runs`:

- `schema_lookup_calls` — increments on every `lookup_schema` step
- `schema_search_calls` — increments on every `search_tables` step

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

## Verification phase column grounding

The same "catalog alone is not enough" pressure that drove the on-demand
schema retrieval design above hits the verification phase too. The verifier
now runs a bounded tool loop per insight/recommendation, so it can issue
`lookup_schema`, `query_warehouse`, `read_step_rows`, and `submit_verdict`
envelopes during validation. On warehouses with non-English / abbreviated
column names a customer report against an MSSQL Netsis-style warehouse on
2026-04-30 saw 9 of 10 insights end with `validation.status = "error"` and
`Invalid column name 'TARIiH' / 'STHAR_SUBE' / 'SUBEKODU' / …` — the
verifier had no column information, so it guessed.

The verification-grounding fix layers in three steps:

| Layer | Mechanism |
|-------|-----------|
| 1     | Render the SQL of cited `source_steps` into the verification prompt as priority-1 column evidence (above the catalog). |
| 2     | The self-healing SQL fixer receives the same evidence on retry via per-call `FixOpts`, so it does not re-emit the same hallucinated column. Per-warehouse `prompts/sql_fix.md` templates gain a conditional `{{#VERIFICATION_CONTEXT}}…{{/VERIFICATION_CONTEXT}}` section that's stripped on the explore path (zero opts) and populated on the validate path. |
| 3     | Verifier owns its own `SchemaProvider` and runs through `VALIDATION_VERIFIER_MAX_ROUNDS` (default 8); the refuter uses `VALIDATION_REFUTER_MAX_ROUNDS` (default 6). The verifier action parser accepts only `lookup_schema`, `query_warehouse`, `read_step_rows`, and `submit_verdict`. Lookup results land in the rendered `VerificationContext` after the source-queries block, so the SQL fixer benefits from them too on retry. |

Layer 1 is implemented in `services/agent/internal/validation/render` (the
`RenderVerificationContext` helper) and consumed by the verifier prompt
builder at `services/agent/internal/validation/verifier/prompt.go`. The
orchestrator wires the cited source-step rows into the verifier `Bundle`
(`services/agent/internal/validation/verifier/bundle.go`) before each
per-doc verification round so the verifier always has authoritative SQL
evidence for any claim that cites a step.

Layer 2 lives in `services/agent/internal/queryexec` (`FixOpts`,
`ExecuteWithFixOpts` entry point, plus the `Execute` shim that calls it
with empty opts) and `services/agent/internal/ai/sql_fixer.go`
(`{{VERIFICATION_CONTEXT}}` substitution). Each per-warehouse
`prompts/sql_fix.md` declares a
`{{#VERIFICATION_CONTEXT}}…{{/VERIFICATION_CONTEXT}}` block that can host
warehouse-specific phrasing of the column-grounding rule alongside the
shared evidence. Adding a new warehouse means keeping that contract — the
provider's `provider_test.go` asserts the markers are present so a missed
template never silently strips the layer's evidence.

Layer 3 lives in `services/agent/internal/validation/verifier/agent.go`
(`(*Agent).Verify` / `(*Agent).Refute` run the tool loop;
`Verifier.MaxRounds` / `Refuter.MaxRounds` in `verifier/config.go` cap it,
tunable via `VALIDATION_VERIFIER_MAX_ROUNDS` / `VALIDATION_REFUTER_MAX_ROUNDS`).
The verifier reuses the catalog/Qdrant `CacheSchemaProvider` the explorer
already uses; the orchestrator forwards the same instance via the bundle
so cross-table lookups hit the same cache. The shared action parser
(`services/agent/internal/validation/verifier/action.go`'s `ParseAction`)
takes an allow-list; the verifier passes
`{lookup_schema, query_warehouse, read_step_rows, submit_verdict}` and
refuses any other top-level key.

When an insight cites no `source_steps` AND no SchemaProvider is wired,
Layer 1 contributes nothing and the verifier falls through to catalog-only
reasoning. With a SchemaProvider wired, the verifier's first round can
issue `lookup_schema` to fetch the column detail it needs.
