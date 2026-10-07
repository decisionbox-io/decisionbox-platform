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

**Tolerant action parsing:** Exploration continues only when the LLM returns a JSON object containing a recognized action key — `query` (run a query), `lookup_schema` / `search_tables` (inspect the warehouse), `done` (terminate), the legacy `action` field, or a tool-use envelope (`{"name": "…", "input": {…}}`). To reach that action reliably on reasoning / open models, the parser strips `<think>…</think>` reasoning blocks (recovering an action written *inside* one) so brace-y reasoning can't shadow the real action, and rejects a truncated/malformed object rather than extracting a nested draft from it. The per-step output budget (`EXPLORATION_MAX_OUTPUT_TOKENS`) is sized so a long reasoning block cannot truncate the action to empty. (Exploration does not force a provider-native `response_format` on the action — doing so dropped the model's `thinking` and corrupted output on some backends; the tolerant parser and retry are the net instead.) Responses that are pure prose, malformed JSON, or JSON with no usable action (e.g. a reasoning-model preamble like `{"plan": "...", "thinking": "..."}`) are rejected and the agent re-prompts with a reason-aware reformat nudge (up to 3 retries per step), preventing silent early termination on models whose prose mentions words like "done" or "complete".

**Early-termination guard (`--min-steps`):** Models biased toward early completion (Qwen3, DeepSeek-R1, GPT-OSS) sometimes return `{"done": true}` after just 1–2 steps even with a 100-step budget. Setting `--min-steps=N` rejects `done` signals until step `N`, records a `complete_rejected` exploration step, and injects a nudge telling the model how many steps remain. Default is `0` (no floor) for backwards compatibility.

**Cube-shaped sources stop on signal, not on a step count.** The floor works because on a warehouse a step is roughly a table or a join, so counting steps approximates counting coverage. A cube-shaped source has no tables to work through — a query is a choice of metrics and dimensions — and the number of available slices is combinatorial, so any step count can be reached without covering anything. Sixty trivial breakdowns and fifteen revealing ones satisfy a floor equally well.

So on a run that can query a cube-shaped datasource, the marginal-value rule is added **past** the floor. `--min-steps` still gates completion exactly as it does today; once a run is past it, each executed step is scored against the steps already taken, using the per-run vector index the analysis phase already builds. While recent steps are still turning up something the run has not seen, a `done` signal is rejected; once three consecutive steps repeat earlier work, the exploration has finished. `--max-steps` is unaffected and remains the runaway cap.

**The rule can only make a run longer, never shorter.** That ordering matters because every discovery run has tables — a source that cannot anchor may not run discovery on its own, so a cube is only ever present *alongside* a warehouse, and the floor remains a good coverage proxy for that warehouse. An earlier version let novelty replace the floor outright, which meant connecting an enrichment source to an existing project silently dropped its effective floor from sixty steps to four, with no setting changed and nothing in the run log naming the floor, because the floor never fired to be named.

Two properties are worth knowing when reading a run:

- A run whose novelty cannot be measured falls back to `--min-steps` rather than refusing every completion until the cap. A degraded index should not turn every run into a maximum-length one against a source metered per request. Four cases reach that fallback: no vector index is wired at all (the rule is never armed), the index has been offered steps and kept none (its write path never worked), the index kept earlier steps but is now refusing several in a row (it is **stale** — it holds the run's early work and none of its latest, so a search reports steps as new that were never stored to be recognised), and several steps in a row could not be judged (its reads are failing).

  Both index checks ask about the index's state **now**, not about a tally of past observations — a judgement made while the machinery still looked healthy would otherwise stay on the books after it stopped being true, and the rule would never stand down.
- Steps novelty could not be *measured* on — no vector index, or one that is failing — count as neither new nor repeated. Three measurement failures are not three repetitions. A step with no earlier step to compare against is different: it cannot be repeating anything, so it counts as new ground. That distinction matters because neighbours are scoped per datasource, so a run's first query against each source has none routinely.
- **Novelty is judged within one datasource.** The same question asked of a second source returns different data, so it is new ground rather than a repeat — and a multi-datasource run asks parallel questions across its sources deliberately. Steps are only scored against earlier steps that queried the same datasource.
- **A query that failed is not evidence either.** It returned no data, so it says nothing about what the source has left to give, and re-asking a broken request is a model that is stuck rather than a run that is finished. Failed steps are also skipped when scoring later steps, so a retry is never counted as a repeat of the attempt it retries. A run whose queries all fail therefore falls back to `--min-steps` rather than exploring to the cap.

Whether a run takes this path is decided from the registered shape of its datasources' providers, and is logged at exploration start.

> **Testing note.** The Qdrant-backed tests for the run-scoped step index and
> the novelty lookup live behind the `integration` build tag in
> `services/agent/internal/discovery`. That package is **not** in CI's
> integration job, so those tests run only where someone runs them —
> `cd services/agent && go test -tags=integration ./internal/discovery/`.
> Adding the package to CI was tried and reverted: a pre-existing test there
> fails on a Qdrant gRPC connection reset when its container is started on a
> CI runner, which needs its own investigation. A run that can only reach tables behaves exactly as it always has.

**Step types reported to the dashboard:**
- `query` — SQL query executed (with thinking, SQL, row count, timing)
- `lookup_schema` — Agent fetched L1 detail (columns + sample rows) for one or more tables from the cache (no warehouse traffic)
- `search_tables` — Agent ran a semantic search against the per-project Qdrant index for tables not surfaced by the catalog
- `complete_rejected` — LLM signalled `done` too early — before `--min-steps`, or, on a cube-reaching run, while steps were still turning up new ground; rejected and exploration continued. The log line carries the engine's own reason, so it names the rule that actually refused rather than always pointing at the floor
- `insight` — The AI identified a pattern (name, severity)
- `analysis` — Analysis phase started for an area
- `validation` — Insight validation result
- `error` — Something went wrong (with error message)

## Checkpointing and resume

Exploration is where a run's cost sits: one agentic LLM call and one warehouse query per step, dozens of steps. Until checkpointing existed, none of that was written anywhere replayable until Phase 7, so a process that died anywhere earlier lost all of it — and the run was marked `failed`, which is terminal.

Each completed exploration step now lands in its own `discovery_checkpoints` document as it finishes, plus one summary document when exploration ends. A failed run with a checkpoint can be **resumed**: it re-enters the **same run id**, replays the steps already executed instead of re-querying them, and continues from the next one. A run that died *after* exploration finished goes straight to analysis, making zero exploration LLM calls and zero warehouse queries.

Resume is operator-initiated — the dashboard shows a **Resume from step N** button on a failed run that has a checkpoint, and the same thing is available as `POST /api/v1/runs/{runId}/resume`. Nothing retries automatically.

### What a checkpoint keeps

| Kept | Why |
|---|---|
| The action, its reasoning, the SQL and its purpose | Replay rebuilds the model's own prior turn from them |
| `row_count`, timing, token counts, self-heal summary | The result message and the run's accounting |
| The compact result digest | What the analysis phase renders |
| Up to 50 rows of the result, normalised | The insight validation phases read raw rows; their evidence bundle is already a 50-row sample on the live path, so a resumed run's verifier sees the same evidence |
| Quality caveats | Every insight's evidence label derives from them, and they are knowable nowhere else — a resumed run without them would relabel findings computed over withheld rows as sound |

Deliberately **not** kept: the full result set (what makes a step unbounded in size), the per-attempt SQL-fix log (audit data, written once at Phase 7), and the raw LLM dialog.

A resumed run's audit log therefore carries the 50-row sample for its pre-crash steps rather than their full results — those rows died with the crashed process. A normal run, and the steps a resumed run executes itself, are unchanged.

### What replay re-executes

Split by cost, so the expensive thing is never paid for twice:

| Action | On replay |
|---|---|
| `query_data` | **Never re-executed.** The result message is rebuilt from the retained rows and the step's metadata. |
| `lookup_schema` | Re-executed against the schema cache — a map lookup plus a Mongo read, no warehouse traffic and no LLM call. |
| `search_tables` | Re-executed — one embedding call and one vector query. |
| `get_correlations` | Re-executed — an in-process lookup. |
| `complete_rejected` | The nudge is re-derived from the persisted reason. |

Re-executing the cheap actions through their real code paths is what restores the per-run `lookup_schema` / `search_tables` / `get_correlations` budgets and the already-fetched-table dedupe as a side effect, so a resumed run cannot be handed a fresh schema budget.

No synthetic "you were resumed" message is injected. The replayed transcript *is* the signal: the model sees its own prior actions and the last result, and answers with the next step.

### Limits worth knowing

- **Checkpoints expire.** `DISCOVERY_CHECKPOINT_RETENTION` (default 48h) must exceed `DISCOVERY_MAX_DURATION`, or a long run's early checkpoints expire while it is still running. Raise them together. A resume with nothing left to replay is refused with an explanation rather than silently re-exploring.
- **A gap stops the replay.** Checkpoint writes are best-effort so they can never abort a working run. If one fails, replay stops at that step and re-explores from there — replaying across a hole would misnumber every later step, and insights cite step numbers.
- **Cancel stays terminal.** Cancelling is a deliberate hard kill; its checkpoints are deleted and the run cannot be resumed.
- **Analysis restarts.** A resumed run re-runs the whole analysis phase; only exploration is checkpointed today.
- **The novelty counters reset.** A cube-reaching resumed run has to re-establish its judged steps. Since that rule can only ever lengthen a run, resume can never end one early.
- **A previous attempt's agent may still be alive, and cannot corrupt the new one.** The API's startup sweep marks in-flight runs `failed` after a restart without reaping their workloads, so resuming such a run can leave two agents on one run id. Every write either agent makes is fenced by attempt: a superseded attempt cannot overwrite a newer one's checkpoints, declare exploration finished on its behalf, or stamp its terminal status. The orphan logs that its writes were refused and exits; the live attempt is unaffected.
- **A re-executed `lookup_schema` can answer differently** if the schema cache was re-indexed between attempts. That is visible rather than hidden: the replayed turn shows what the cache says now, which is also what the resumed run will query against.

### Across attempts

The run document records `attempt`, `last_resumed_at`, `last_checkpoint_step`, `active_ms` and an append-only `lifecycle` log, plus the run's own `max_steps` / `min_steps` / `areas` / `effort` so a resume replays the budget the operator chose rather than the defaults.

`active_ms` is cumulative **active** compute across attempts, which is what the dashboard shows as elapsed — otherwise a run resumed the next morning would report the hours it spent waiting to be noticed as work. It is recorded at each attempt's terminal write, so an attempt killed hard enough never to reach that point contributes nothing: a slight undercount, accepted because a dead attempt's compute is not worth counting.

Each attempt's `lifecycle` event carries the LLM provider and model that served it, which is how "which model ran which attempt" stays answerable.

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

**Markdown descriptions:** The platform instructs the LLM to author each `description` as a small GitHub-Flavored Markdown subset (a bold one-line takeaway, short paragraphs, lists, small sub-headings, simple tables), following a takeaway-first anatomy where the finding supports it. At parse time the agent splits this into two fields: `description` keeps the plain-text reduction (the raw form read by API consumers, previews, and embeddings) and `description_md` keeps the Markdown rendition rendered on the detail view. Plain descriptions leave `description_md` empty.

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

The prompt receives those insights **trimmed to the fields the recommender reasons over** — `id`, `analysis_area`, `name`, `description`, `severity`, `affected_count`, `risk_score`, `confidence`, `metrics`, `indicators`, `target_segment`, `evidence_quality`.
`validation`, `source_steps`, `sql_metadata`, `discovered_at` and `description_md` are deliberately left out: none of them is used to write a recommendation, the verdict has already been applied by the filter above, and the verifier + refuter write-ups carried in `validation` were roughly 80% of the rendered prompt — enough on their own to exceed a 40 960-token context window, fail the request, and end the run with no recommendations.
The stored insights are unchanged; only the prompt copy is trimmed (`insightsForRecommenderPrompt`, see [Prompt variables → {{INSIGHTS_DATA}}](../reference/prompt-variables.md#insights_data)).

After generation, `validateRelatedInsightIDs` drops any recommendation whose `related_insight_ids` list is empty or references an insight not in the eligible set. The dropped recommendation is logged with the bad IDs so operators can trace the cause, and the per-run drop counts are stamped onto the persisted `RecommendationStep` (`recommendations_dropped`, `recommendations_dropped_missing_ids`, `recommendations_dropped_unknown_id` — see the [RecommendationStep data model](../reference/data-models.md#recommendationstep)). The live dashboard run-step message reads "Generated N recommendations (M dropped due to invalid related_insight_ids)" when the drop counter is non-zero. The most common cause of `recommendations_dropped_unknown_id` is an LLM that emits category/severity/theme slug strings in place of the input insight's actual UUID (issue #237); the recommendation discipline rules explicitly forbid this shape, but a regression on a specific model surfaces here.

```
Load recommendations.md prompt
  ↓
Prepend base context (profile + previous context)
  ↓
Substitute:
  {{DISCOVERY_DATE}} → current date
  {{INSIGHTS_SUMMARY}} → "Total: 7 insights (churn: 3, engagement: 2, monetization: 2)"
  {{INSIGHTS_DATA}} → eligible insights as JSON, trimmed to the recommender's fields (with IDs)
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
  - run_id (the run that produced it — lets a resumed run retire the partial
    result its previous attempt left behind, and keep that result out of its
    own "previously discovered" context)
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

A **resumed** run writes its new result first and retires the superseded one afterwards, rather than clearing the old rows before writing the new ones. There is no window in which the project shows no result at all, and because the new rows go in under a freshly minted `discovery_id`, the unique index on `discovery_recommendation_log.discovery_id` cannot be violated by a second pass. Once the result is durable the run's checkpoints are deleted; on a failure they are kept, which is what leaves the run resumable.

## Error Handling

| Error | What happens |
|-------|-------------|
| Invalid API key | Agent fails immediately. Run marked "failed" with error message. |
| LLM timeout | The specific area is skipped. Other areas continue. Run marked "partial". |
| All areas timeout | Run marked "failed". Error banner shown in dashboard. |
| SQL query error | Agent asks LLM to fix the SQL. If still fails, step is skipped. |
| Warehouse unreachable | Agent fails during schema discovery. Run marked "failed". |
| Agent process crash | Subprocess runner detects exit code, updates run to "failed" with error from stderr. **Resumable** from the last exploration checkpoint. |
| K8s Job failure | K8s runner polls Job status, detects failure, updates run. **Resumable** from the last exploration checkpoint. |
| API restarted mid-run | Startup sweep marks the run "failed". **Resumable** — the checkpoints outlive the process that wrote them. |
| Run exceeded `DISCOVERY_MAX_DURATION` | Partial result is saved and the run is marked "failed". **Resumable**: exploration is replayed, not re-run. |
| Resume with no checkpoint | Refused with 409 and an explanation (it expired, or the run died before its first step). Start a new run. |

## Cost

Each run costs:
- **LLM tokens** — Exploration (many small calls) + Analysis (few large calls) + Recommendations (one call)
- **Warehouse queries** — Each exploration step executes one SQL query

Use the **cost estimation** feature (`POST /api/v1/projects/{id}/discover/estimate`) to preview costs before running. The dashboard has a checkbox: "Estimate cost before running."

## Next Steps

- [Architecture](architecture.md) — System components and data flow
- [Prompts](prompts.md) — Template variables and customization
- [Domain Packs](domain-packs.md) — How domain-specific analysis works
