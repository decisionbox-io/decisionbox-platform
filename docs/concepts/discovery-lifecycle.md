# Discovery Lifecycle

A discovery run is the core operation of DecisionBox. The agent autonomously explores your data warehouse, finds patterns, validates them, and generates recommendations. This page explains each phase in detail.

## Phases Overview

| Phase | What happens | Duration |
|-------|-------------|----------|
| Startup | Load project config, secrets, providers (pre-phase) | ~2s |
| 1. Load project context | Fetch previous discoveries + feedback | ~1s |
| 2. Load schemas | List tables, read schemas from cache | 5-30s |
| 3. Exploration | AI writes + executes SQL queries | 2-30 min |
| 4. Analysis | Generate insights per analysis area | 1-5 min |
| 4.5. Insight validation | LLM verifier + refuter check insights against row evidence | 1-5 min |
| 5. Recommendations | Generate actionable advice from supported insights | 1-3 min |
| 5.5. Recommendation validation | Same verifier + refuter check recommendations | 1-3 min |
| 6. Update project context | Write rolling context for the next run | <1s |
| 7. Save | Write results to MongoDB | ~1s |
| 9. Embed + index | Denormalize insights + recommendations to Qdrant for dashboard search (non-fatal) | 5-30s |

Total time depends on exploration steps, LLM speed, and warehouse query time. A typical 100-step run with Claude Sonnet takes 5-15 minutes.

The phase numbers above match the orchestrator's logged phase numbers — find any phase in the agent log by grepping for the relevant numeric prefix. <!-- lint-allow: log-grep-hint -->

## Startup (pre-phase)

The agent starts with a project ID and loads everything it needs before Phase 1.
The entry point is `agentserver.Run()`, which parses CLI flags, initializes providers, and runs the discovery pipeline.
Custom builds can import `agentserver` and register plugins (e.g., warehouse middleware) via `init()` blank imports before calling `Run()`.

```
Agent receives: --project-id=abc123 --run-id=run456 --max-steps=100
  ↓
Loads project from MongoDB (name, domain, category, warehouse, llm, profile)
  ↓
Sets project ID in context (warehouse.WithProjectID) for middleware
  ↓
Initializes secret provider (reads LLM API key, warehouse credentials)
  ↓
Initializes warehouse provider (BigQuery/Redshift with credentials)
  ↓
Applies warehouse middleware (warehouse.ApplyMiddleware)
  ↓
Initializes LLM provider (Claude/OpenAI/etc. with API key from secrets)
  ↓
Loads domain pack (e.g., gaming/match3 or social/content_sharing → analysis areas, prompts, profile schema)
  ↓
Loads project-level prompt overrides from MongoDB (if any)
```

**Secret loading order:**
1. Read `llm-api-key` from secret provider (per-project)
2. Read `warehouse-credentials` from secret provider (optional, for cross-cloud)
3. These credentials are passed to the LLM/warehouse provider constructors

## Phase 1: Load project context

The agent loads context from previous runs to avoid repetition:

```
Fetch last 5 discoveries for this project
  ↓
Fetch all feedback (likes/dislikes with comments)
  ↓
Build previous context:
  - Previously found insights (names, areas, severity, dates)
  - Disliked insights with user comments → "AVOID similar conclusions"
  - Liked insights → "MONITOR for changes"
  - Previous recommendations → "Don't repeat unless changed"
```

This context is injected into all prompts via the `{{PREVIOUS_CONTEXT}}` template variable in `base_context.md`.

## Phase 2: Load schemas

The agent reads your warehouse structure:

```
For each dataset in project.warehouse.datasets:
  ↓
  List all tables (excluding system tables like pg_*, stl_*, svv_*)
  ↓
  For each table:
    Get column names, types, nullable flags
    Get approximate row count
  ↓
  Cache schemas for the exploration phase
```

A compact Level-0 catalog (one line per table: name, column count, row count, keyword hints, joins) is injected into the exploration prompt via `{{SCHEMA_INFO}}`. The agent fetches per-table column lists and sample rows on demand during exploration via `lookup_schema` (up to 10 tables per call, 30 calls per run) or `search_tables` (semantic query against the per-project Qdrant index, 30 calls per run). See [On-Demand Schema](../architecture/agent-on-demand-schema.md) for the rationale (the previous "always inject L1 detail" approach exhausted the Bedrock 1M-token context on long runs).

## Phase 3: Exploration

The core phase. The AI writes SQL queries, executes them, analyzes results, and decides what to query next.

```
Send to LLM:
  - System prompt (exploration.md + category context)
  - Schema information
  - Profile (game mechanics, monetization model, etc.)
  - Previous context (past insights, feedback)
  - Filter rules (WHERE clause for multi-tenant)
  ↓
LLM responds with JSON:
  {
    "thinking": "I want to check retention rates...",
    "query": "SELECT cohort_date, retention_d1 FROM ..."
  }
  ↓
Agent executes query against warehouse
  ↓
Send results back to LLM
  ↓
LLM writes next query based on results
  ↓
Repeat for max_steps (default: 100)
```

Each step is written to the `discovery_runs` collection in real-time, so the dashboard can show live progress.

**Self-healing SQL:** If a query fails, the agent sends the error message to the LLM and asks it to fix the SQL. This uses the warehouse provider's `SQLFixPrompt()` (BigQuery-specific SQL fix instructions, Redshift-specific, etc.).

**Strict action parsing:** Exploration continues only when the LLM returns a JSON object containing a recognized action key — `query` (run a query), `done` (terminate), or the legacy `action` field. Responses that are pure prose, malformed JSON, or JSON with unrelated keys (e.g., a reasoning-model preamble like `{"plan": "...", "thinking": "..."}`) are rejected and the agent re-prompts the LLM with a reformat nudge (up to 3 retries per step). This prevents silent early termination on reasoning models whose prose often mentions words like "done" or "complete".

**Early-termination guard (`--min-steps`):** Models biased toward early completion (Qwen3, DeepSeek-R1, GPT-OSS) sometimes return `{"done": true}` after just 1–2 steps even with a 100-step budget. Setting `--min-steps=N` rejects `done` signals until step `N`, records a `complete_rejected` exploration step, and injects a nudge telling the model how many steps remain. Default is `0` (no floor) for backwards compatibility.

**Step types reported to the dashboard:**
- `query` — SQL query executed (with thinking, SQL, row count, timing)
- `lookup_schema` — Agent fetched L1 detail (columns + sample rows) for one or more tables from the cache (no warehouse traffic)
- `search_tables` — Agent ran a semantic search against the per-project Qdrant index for tables not surfaced by the catalog
- `complete_rejected` — LLM signalled `done` before `--min-steps`; rejected and exploration continued
- `insight` — The AI identified a pattern (name, severity)
- `analysis` — Analysis phase started for an area
- `validation` — Insight validation result
- `error` — Something went wrong (with error message)

## Phase 4: Analysis

For each analysis area defined by the domain pack (e.g., churn, engagement, monetization for gaming; growth, engagement, retention for social), the agent:

```
Load area-specific prompt (e.g., analysis_churn.md)
  ↓
Pick relevant exploration steps:
  - vector-rank against the per-run Qdrant index of every step
    (embedding text = step purpose + SQL)
  - promote any step whose text contains a verbatim area keyword
  - drop the lowest-scored steps until the rendered prompt fits
    the per-area token budget
  ↓
Render each picked step as a compact digest (per-column
statistics + head/tail rows + small-result inlining), instead
of inlining the raw row blob
  ↓
Prepend base context (profile + previous context)
  ↓
Substitute template variables:
  {{DATASET}} → dataset names
  {{TOTAL_QUERIES}} → number of picked steps
  {{QUERY_RESULTS}} → JSON array of compact digests for this area
  ↓
Send to LLM
  ↓
LLM responds with JSON:
  {
    "insights": [
      {
        "name": "Day 0-to-Day 1 Drop: 67% Never Return",
        "description": "...",
        "severity": "critical",
        "affected_count": 8298,
        "risk_score": 0.67,
        "confidence": 0.85,
        "indicators": ["...", "..."],
        "source_steps": [1, 3, 5]
      }
    ]
  }
  ↓
Agent parses insights, assigns IDs (e.g., "churn-1", "churn-2")
```

**Insight IDs:** The agent generates deterministic IDs in the format `{area}-{index}` (e.g., `churn-1`, `monetization-3`). These IDs are used by recommendations to reference which insights they address.

**If analysis fails** (e.g., LLM timeout), the error is recorded in `analysis_log` and the area is skipped. If ALL areas fail, the run is marked as `run_type: "failed"`. If some fail, it's `run_type: "partial"`. The errors are surfaced in the dashboard as a red banner.

## Phase 4.5: Insight validation (LLM-native)

Immediately after each analysis area produces its insights, the orchestrator runs the **verifier + refuter agent pair** on every insight, in `affected_count` descending order. Per-run cap: `VALIDATION_MAX_INSIGHTS_PER_RUN` (default 30).

```
For each insight (desc by affected_count):
  ↓
  Build verifier.Bundle:
    - Doc digest (headline + description + severity + source_steps)
    - SourceSteps[] from explorationResult.Steps (sample-capped + cell-capped)
    - Warehouse info (dialect, dataset, run-wide filter)
    - Discovery context (project, run, domain, language)
  ↓
  Run verifier agent (defender frame):
    - Enumerate claims_considered (headline first)
    - Gather evidence via lookup_schema / query_warehouse / read_step_rows
    - Submit StructuredVerdict with per-claim evidence rows
  ↓
  Run refuter agent (skeptic frame, if VALIDATION_REFUTER_ENABLED):
    - Bundle carries PriorClaims (verifier's enumerated set, verbatim)
    - Attempt to refute each claim; tool-less verdicts are rejected
    - Submit StructuredVerdict with row-level contradicting evidence
  ↓
  Coverage finaliser runs deterministic checks:
    - duplicate detection, headline positional rule, set-equality,
      evidence-required, status-enum validation, derive-Overall fallback
  ↓
  Combine(verifier, refuter, refuterDisabled) → one of 7 statuses:
    confirmed | supported | rejected | partial | unverifiable
    | validation_disabled | skipped_budget_cap
  ↓
  Stamp insight.Validation = {Verifier, Refuter, Combined, RefuterDisabled}
```

The validator **does NOT overwrite** the writer's `affected_count`. The `Validation` block carries the verifier+refuter verdicts and the dashboard surfaces them alongside the writer's number — when row-level evidence disagrees, both numbers stay visible.

See [Insight validation](../architecture/insight-validation.md) for the architecture and the [Configuration → Validation](../reference/configuration.md#validation) reference for every knob.

## Phase 5: Recommendations

Only insights with `Combined ∈ {supported, confirmed}` are fed to the recommendations prompt — `partial`, `rejected`, `unverifiable`, and `skipped_budget_cap` insights are filtered out at this gate.

**Fail-open exception**: insights with `Combined == "validation_disabled"` (and legacy docs whose `Validation` field is missing entirely) **are** treated as eligible. The rationale is permissive: when validation didn't run at all (no LLM client, no schema provider), it would be misleading to penalise insights for the agent's absence — those insights should flow through unchanged. Operators who want strict gating should ensure validation is configured. When the eligible set is empty the recommendation phase is skipped and a `RecommendationStep{Status: "skipped_no_eligible_insights"}` is persisted for observability.

After generation, `validateRelatedInsightIDs` drops any recommendation whose `related_insight_ids` list is empty or references an insight not in the eligible set. The dropped recommendation is logged with the bad IDs so operators can trace the cause, and the per-run drop counts are stamped onto the persisted `RecommendationStep` (`recommendations_dropped`, `recommendations_dropped_missing_ids`, `recommendations_dropped_unknown_id` — see the [RecommendationStep data model](../reference/data-models.md#recommendationstep)). The live dashboard run-step message reads "Generated N recommendations (M dropped due to invalid related_insight_ids)" when the drop counter is non-zero. The most common cause of `recommendations_dropped_unknown_id` is an LLM that emits category/severity/theme slug strings in place of the input insight's actual UUID (issue #237); the recommendation discipline rules explicitly forbid this shape, but a regression on a specific model surfaces here.

```
Load recommendations.md prompt
  ↓
Prepend base context (profile + previous context)
  ↓
Substitute:
  {{DISCOVERY_DATE}} → current date
  {{INSIGHTS_SUMMARY}} → "Total: 7 insights (churn: 3, engagement: 2, monetization: 2)"
  {{INSIGHTS_DATA}} → full JSON array of all insights (with IDs)
  ↓
Send to LLM
  ↓
LLM responds with JSON:
  {
    "recommendations": [
      {
        "title": "Send Extra Lives After 3 Failures on Level 42",
        "description": "...",
        "priority": 1,
        "target_segment": "Players who failed level 42 3+ times",
        "segment_size": 642,
        "expected_impact": {
          "metric": "retention_rate",
          "estimated_improvement": "+15-20%"
        },
        "actions": ["Step 1...", "Step 2...", "Step 3..."],
        "related_insight_ids": ["churn-1", "levels-2"],
        "confidence": 0.85
      }
    ]
  }
```

**Related insight IDs:** Each recommendation references the insights it addresses via `related_insight_ids`. These are the IDs assigned in Phase 4. The dashboard shows bidirectional links — recommendations show which insights they address, and insight detail pages show related recommendations.

## Phase 5.5: Recommendation validation (LLM-native)

Each kept recommendation is then validated by the same verifier + refuter pair. The bundle's source steps are the **token-budgeted union** (`VALIDATION_REC_STEPS_TOKEN_BUDGET`, default 12 000) of source steps from the recommendation's related insights. When over-budget steps are dropped, `source_steps_truncated: true` is surfaced in the prompt so the agent can mark claims dependent on omitted steps as `unverifiable`.

Per-run cap: `VALIDATION_MAX_RECOMMENDATIONS_PER_RUN` (default 15). Output: `recommendation.Validation` populated with the same `StructuredVerdict` shape used for insights.

## Phase 7: Save

The agent writes the complete `DiscoveryResult` to MongoDB:

```
DiscoveryResult:
  - project_id, domain, category
  - run_type: "full" | "partial" | "failed"
  - areas_requested (if selective run)
  - total_steps, duration
  - insights[] (with validation results)
  - recommendations[] (with related_insight_ids)
  - summary (totals, errors)
  - exploration_log[] (every SQL query + result)
  - analysis_log[] (full LLM dialog per area)
  - recommendation_log (full LLM dialog)
  - validation_log[] (verification queries + results)
```

The run status is updated to `completed` (or `failed` if critical errors occurred).

## Error Handling

| Error | What happens |
|-------|-------------|
| Invalid API key | Agent fails immediately. Run marked "failed" with error message. |
| LLM timeout | The specific area is skipped. Other areas continue. Run marked "partial". |
| All areas timeout | Run marked "failed". Error banner shown in dashboard. |
| SQL query error | Agent asks LLM to fix the SQL. If still fails, step is skipped. |
| Warehouse unreachable | Agent fails during schema discovery. Run marked "failed". |
| Agent process crash | Subprocess runner detects exit code, updates run to "failed" with error from stderr. |
| K8s Job failure | K8s runner polls Job status, detects failure, updates run. |

## Cost

Each run costs:
- **LLM tokens** — Exploration (many small calls) + Analysis (few large calls) + Recommendations (one call)
- **Warehouse queries** — Each exploration step executes one SQL query

Use the **cost estimation** feature (`POST /api/v1/projects/{id}/discover/estimate`) to preview costs before running. The dashboard has a checkbox: "Estimate cost before running."

## Next Steps

- [Architecture](architecture.md) — System components and data flow
- [Prompts](prompts.md) — Template variables and customization
- [Domain Packs](domain-packs.md) — How domain-specific analysis works
