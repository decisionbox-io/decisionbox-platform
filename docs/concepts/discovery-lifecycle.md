# Discovery Lifecycle

> **Version**: 0.1.0

A discovery run is the core operation of DecisionBox. The agent autonomously explores your data warehouse, finds patterns, validates them, and generates recommendations. This page explains each phase in detail.

## Phases Overview

| Phase | What happens | Duration |
|-------|-------------|----------|
| 1. Initialization | Load project config, secrets, providers | ~2s |
| 2. Context Loading | Fetch previous discoveries + feedback | ~1s |
| 3. Schema Discovery | List tables, read schemas | 5-30s |
| 4. Exploration | AI writes + executes SQL queries | 2-30 min |
| 5. Analysis | Generate insights per analysis area | 1-5 min |
| 6. Validation | Verify insights against warehouse | 30s-2 min |
| 7. Recommendations | Generate actionable advice from insights | 1-3 min |
| 8. Saving | Write results to MongoDB | ~1s |

Total time depends on exploration steps, LLM speed, and warehouse query time. A typical 100-step run with Claude Sonnet takes 5-15 minutes.

## Phase 1: Initialization

The agent starts with a project ID and loads everything it needs.
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
Applies warehouse middleware (warehouse.ApplyMiddleware — e.g., governance)
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

## Phase 2: Context Loading

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

## Phase 3: Schema Discovery

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

Schema information is injected into the exploration prompt via `{{SCHEMA_INFO}}` so the AI knows what tables and columns are available.

## Phase 4: Exploration

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

**Step types reported to the dashboard:**
- `query` — SQL query executed (with thinking, SQL, row count, timing)
- `insight` — The AI identified a pattern (name, severity)
- `analysis` — Analysis phase started for an area
- `validation` — Insight validation result
- `error` — Something went wrong (with error message)

## Phase 5: Analysis

For each analysis area defined by the domain pack (e.g., churn, engagement, monetization for gaming; growth, engagement, retention for social), the agent:

```
Load area-specific prompt (e.g., analysis_churn.md)
  ↓
Filter exploration results to relevant queries (using area keywords)
  ↓
Prepend base context (profile + previous context)
  ↓
Substitute template variables:
  {{DATASET}} → dataset names
  {{TOTAL_QUERIES}} → number of relevant queries
  {{QUERY_RESULTS}} → JSON array of exploration results for this area
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

## Phase 6: Validation

Each insight with an `affected_count` is verified:

```
For each insight with affected_count > 0:
  ↓
  Generate a verification SQL query
  (e.g., COUNT(DISTINCT user_id) with the same filters)
  ↓
  Execute against warehouse
  ↓
  Compare claimed count vs verified count
  ↓
  Status:
    - "confirmed" — within 20% tolerance
    - "adjusted" — count differs, insight updated
    - "rejected" — count is drastically different
    - "error" — verification query failed
```

Validation results are stored on each insight and shown in the dashboard.

## Phase 7: Recommendations

All validated insights are fed to the recommendations prompt:

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

**Related insight IDs:** Each recommendation references the insights it addresses via `related_insight_ids`. These are the IDs assigned in Phase 5. The dashboard shows bidirectional links — recommendations show which insights they address, and insight detail pages show related recommendations.

## Phase 8: Saving

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
