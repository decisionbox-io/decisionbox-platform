# Agent Analysis Phase Compaction

> **Version**: 0.4.0+

## Why

Before this change, the analysis phase of discovery built a per-area
LLM prompt by:

1. Selecting exploration steps via case-insensitive substring keyword
   match against the area's keyword list, and
2. JSON-marshaling every selected step's full `ExplorationStep` —
   including every row of `query_result` — into the prompt as
   `{{QUERY_RESULTS}}`.

On an ERP-scale run (~80 exploration steps, several with thousand-row
result sets) one analysis area assembled a single prompt of
2,993,965 characters / ~1.4M tokens. Bedrock Claude rejected it:

```
prompt is too long: 1401958 tokens > 1000000 maximum
```

The discovery completed, but every area that hit the cap produced 0
insights.

The on-demand schema retrieval architecture (see
[agent-on-demand-schema.md](agent-on-demand-schema.md)) fixed the
parallel problem in the *exploration* phase. This change extends
the same idea — bounded prompts via deterministic compaction +
ranked retrieval — to the *analysis* phase.

## Two orthogonal axes

| Axis                                              | Before                                                                                   | After                                                                                            |
|---------------------------------------------------|------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------|
| **Selection** — which steps feed the prompt       | Case-insensitive substring match on `Query + QueryPurpose + Thinking`                    | Vector ranking against the area's identity (`Name + Description + Keywords`)                     |
| **Representation** — how each step is rendered    | Full `ExplorationStep` JSON, every row inlined                                           | `CompactResult` digest (per-column statistics + head/tail rows)                                  |

Either alone is insufficient: vector ranking still produces an
unbounded prompt if every selected step inlines a thousand rows;
compaction alone keeps a step's rendered size small but still
selects every keyword-matched step regardless of relevance.

## CompactResult — per-step deterministic digest

`libs/go-common/models/CompactResult` replaces the raw `query_result`
blob in the prompt. Computed once at exploration time and attached to
the step:

```go
type CompactResult struct {
    RowCount int
    Columns  []ColumnSummary
    HeadRows []map[string]any           // first 5 rows, always present
    TailRows []map[string]any           // last 5 rows, when RowCount > 2*HeadTailRowCount
    AllRows  []map[string]any           // every row when RowCount <= CompactInlineThreshold
}

type ColumnSummary struct {
    Name      string
    Kind      ColumnKind                // number / string / boolean / timestamp / null / mixed
    NullCount int
    Distinct  int
    Min, P25, Median, P75, Max *float64 // numeric percentiles
    MinTime, MaxTime           string   // timestamp range (ISO-8601)
    Top                        []ValueCount // top-3 values for low-cardinality strings + booleans
}
```

Constants live in the same file:

| Constant                          | Default | Role                                                                                  |
|-----------------------------------|---------|---------------------------------------------------------------------------------------|
| `CompactInlineThreshold`          | 20      | Row-count cap below which `AllRows` keeps everything                                  |
| `TopValueCardinality`             | 20      | Distinct-value cap above which a string column emits no `Top` (PII guard)            |
| `HeadTailRowCount`                | 5       | Number of rows in `HeadRows` / `TailRows`                                             |

The builder is pure: same input twice → byte-identical output. NaN /
+Inf / -Inf are excluded from numeric percentiles and counted toward
`NullCount` so a single bad row can't poison the statistics.

A 2,000-row result that previously rendered as ~80 KB JSON now
renders as ~1.5 KB.

## Vector ranking — per-run step index

Each completed exploration step is upserted into a per-run Qdrant
collection named `decisionbox_run_{run_id}` immediately after the
step's analysis is set. The embedding text is the step's
`QueryPurpose + "\n[SQL]: " + Query` — `Thinking` is intentionally
excluded because it tends to be exploratory chain-of-thought that
hurts ranking more than it helps. The payload carries
`{step, purpose, row_count, has_error}` so the picker can log
budget-trimming decisions without re-fetching the step doc.

For each analysis area:

```
area_query = area.Name + " — " + area.Description + ". Keywords: " + keywords
v          = embedding.Embed(area_query)
hits       = run_step_index.Search(v, TopK=24, MinScore=0.30)
```

After vector retrieval, an exact-match boost promotes any step whose
`Query / QueryPurpose / Thinking` contains a verbatim area keyword
(case-insensitive substring) to a score of at least
`ExactMatchFloor = 0.55`. This guarantees a step explicitly written
for an area's keyword can never be ranked out by a semantically-
close-but-different step. The picker emits `(picked, dropped)`; the
caller logs the dropped list to telemetry.

The collection is dropped at run completion (success or failure) by
a `defer` in the orchestrator. On agent boot, an orphan sweep drops
any per-run collection whose run id is no longer in the active
`discovery_runs` set within a 24h window.

## Per-area token budget

After ranking + compaction, the picker estimates the rendered
`{{QUERY_RESULTS}}` JSON byte size and trims the lowest-scored
steps until the prompt fits under
`AnalysisQueryResultsBudgetTokens = 200_000`. The estimator runs
the same renderer the orchestrator runs, so
`len(RenderCompactedSteps(s)) == EstimateCompactedRenderedSize(s)`
holds for any input.

Dropped steps are reported with their step number, score, and the
drop reason (`below_min_score` or `over_budget`). Telemetry exposes
the totals on the run document.

## Flow

```
Orchestrator
  │
  ├─ Phase 3: Exploration
  │    └─ For each completed step:
  │         ├─ engine builds CompactResult and attaches it to the step
  │         └─ engine upserts (vector, payload) into the per-run Qdrant collection
  │              └─ counting decorator bumps analysis_step_index_upserts
  │
  ├─ Phase 4: Analysis (one iteration per area)
  │    │
  │    ├─ picker.Pick(area, steps) →
  │    │    ├─ run_step_index.Search(area_query, TopK=24, MinScore=0.30)
  │    │    │   └─ bumps analysis_step_index_search_calls
  │    │    ├─ exact-match boost (verbatim keyword in step text)
  │    │    ├─ sort by score desc, step asc on ties
  │    │    └─ budget trim until rendered size ≤ AnalysisQueryResultsBudgetTokens
  │    │         └─ bumps analysis_steps_dropped (per dropped step)
  │    │
  │    ├─ RenderCompactedSteps(picked) → fixed-size JSON
  │    ├─ Substitute into prompt's {{QUERY_RESULTS}}
  │    ├─ Run LLM, parse insights, validate
  │    └─ Stamp telemetry on the analysis_log entry: selected_steps,
  │        dropped_steps, query_results_chars
  │
  └─ defer run_step_index.Drop()
```

## Key files

| File                                                                           | Role                                                                                              |
|--------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------|
| `libs/go-common/models/compact_result.go`                                      | `CompactResult`, `ColumnSummary`, `ValueCount`, `ColumnKind` + tunable constants                  |
| `libs/go-common/models/compact_builder.go`                                     | `BuildCompactResult` — pure deterministic digest                                                  |
| `services/agent/internal/discovery/run_step_index.go`                          | Per-run Qdrant collection wrapper: `Upsert`, `Search`, `Drop`, `SweepOrphanRunStepIndexes`        |
| `services/agent/internal/discovery/analysis_step_picker.go`                    | Vector hits + exact-match boost + budget trimming. Pure logic, no IO.                             |
| `services/agent/internal/discovery/render_query_results.go`                    | Single source of truth for the `{{QUERY_RESULTS}}` JSON shape                                     |
| `services/agent/internal/ai/exploration.go`                                    | Computes `CompactResult` per step + upserts via the `StepIndexer` interface                       |
| `services/agent/internal/discovery/orchestrator.go`                            | Wires picker + render into the analysis loop; defers `Drop`                                       |
| `services/agent/agentserver/agentserver.go`                                    | Constructs `RunStepIndex`, runs the boot-time orphan sweep                                        |

## Telemetry

New fields on `discovery_runs`:

| Field                                  | What it counts                                                            |
|----------------------------------------|---------------------------------------------------------------------------|
| `analysis_step_index_upserts`          | How many exploration steps were indexed for this run                      |
| `analysis_step_index_search_calls`     | How many area-level vector searches the picker issued                     |
| `analysis_steps_dropped`               | How many steps the picker excluded (sum across areas, all reasons)        |

New fields on each `analysis_log` entry:

| Field                  | Shape                                                              |
|------------------------|--------------------------------------------------------------------|
| `selected_steps`       | `[{step:int, score:float, source:"vector"|"exact_match"}]`         |
| `dropped_steps`        | `[{step:int, score:float, reason:"below_min_score"|"over_budget"}]`|
| `query_results_chars`  | Final rendered byte size of the `{{QUERY_RESULTS}}` block          |

The dashboard's debug view (Project Settings → Advanced → Show debug
logs during discovery) renders these as a per-area "what fed the LLM"
breakdown so a human reviewer can see which steps were sacrificed.

## Tunable constants

All defined in code; documented in `docs/reference/configuration.md`.
Tune by editing the constant + redeploying the agent — there is no
runtime override surface (deliberately — they're algorithm parameters,
not user knobs).

| Constant                                                              | Default     | When to tune                                                                                             |
|-----------------------------------------------------------------------|-------------|----------------------------------------------------------------------------------------------------------|
| `models.CompactInlineThreshold`                                       | 20 rows     | Lower if your domain produces lots of 50-row aggregates and the head+tail summary is noticeably worse    |
| `models.TopValueCardinality`                                          | 20          | Lower for datasets where >20 distinct values is still meaningful (rare); higher leaks PII                |
| `models.HeadTailRowCount`                                             | 5           | Higher if the LLM consistently misses end-of-result patterns                                             |
| `discovery.AnalysisAreaTopK`                                          | 24          | Lower if you regularly hit budget trimming; higher if the picker is missing steps                        |
| `discovery.AnalysisAreaMinScore`                                      | 0.30        | Lower for highly-multilingual runs where cosine is naturally smaller                                     |
| `discovery.ExactMatchFloor`                                           | 0.55        | Raise to make the exact-match path more aggressive; lower to defer to vector ranking                     |
| `discovery.AnalysisQueryResultsBudgetTokens`                          | 200_000     | Lower if the surrounding prompt grows; raise only on models with >>1M-token windows                      |

## Tests

Unit tests cover every branch in the digest builder (12 numeric /
string / type-inference / boundary cases including determinism and
NaN handling), every picker path (vector-only, exact-match boost,
budget trim, ties, error propagation), and the renderer's golden
JSON shape. The picker uses a fake `Search` function and the renderer
runs deterministically end-to-end.

Integration tests run against a real Qdrant testcontainer:

- `TestIntegration_RunStepIndex_FullCycle` — upsert 30 steps, search,
  drop, second drop is a no-op.
- `TestIntegration_RunStepIndex_ConcurrentUpserts` — 30 goroutines
  upsert concurrently; collection-create races are resolved.
- `TestIntegration_RunStepIndex_MultilingualQuery` — a Turkish area
  query retrieves the matching English-described step, gated on
  `INTEGRATION_TEST_OPENAI_API_KEY` (uses
  `text-embedding-3-large`).

## Verification

After deploying:

1. Run discovery on a project that previously hit the prompt cap
   (e.g. an ERP-scale run with thousand-row result sets per area).
2. Confirm in the dashboard's debug view that
   `analysis_steps_dropped` is observable but not absurd. A
   well-tuned run drops 0 to a handful of steps per area.
3. No `prompt is too long` errors in the agent logs.
4. Spot-check insights against the last successful run for the same
   area — the same patterns should surface, possibly with slight
   ordering differences from the new ranker.

## Known limitations

- **Embedding cost per run**: one embed call per indexed step + one
  per area search. With `text-embedding-3-large` and a 100-step run
  with 8 areas, this is ~108 embed calls or ~$0.001. Negligible.
- **Vector ranking is approximate**: a step that *would* have been
  selected by keyword match may not survive the min-score floor. The
  exact-match boost is the safety net — but it's still possible to
  produce a different mix of steps than the legacy keyword-only
  selector. Score logging in `selected_steps` makes the difference
  auditable.
- **Per-run collections in Qdrant aren't free**: each one occupies
  ~kilobytes plus index overhead. The defer'd drop + boot-time
  sweep keeps Qdrant clean across thousands of runs.
- **The renderer never strips columns**: a result with 100 columns
  still emits 100 `ColumnSummary` entries. Most warehouses return
  ≤30 columns; for genuinely wide results, future work could trim
  to the columns the LLM actually needs.
