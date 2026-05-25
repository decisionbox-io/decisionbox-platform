---
title: Insight validation
sidebar_label: Insight validation
description: How the LLM-native verifier + refuter validates discovered insights and recommendations against warehouse data.
---

# Insight validation

DecisionBox validates every insight and recommendation it generates by running two LLM agents — a **verifier** (defender frame) and a **refuter** (skeptic frame) — against row-level evidence from the warehouse. Each agent inspects the document's claims, gathers data via three tools (`lookup_schema`, `query_warehouse`, `read_step_rows`), and emits a structured verdict. The two verdicts are mechanically combined into one of seven terminal statuses that the dashboard renders alongside the writer's number.

This document describes the architecture; the per-knob reference is in [Configuration](../reference/configuration.md#validation), and the agent prompts live in `services/agent/internal/validation/verifier/prompts/`.

## What validation is for

The discovery agent writes claims it can't always justify. Two failure modes drive this:

1. **Superlatives.** "Massachusetts has the most extreme MAPD-driven access barrier." The writer chose Massachusetts; nothing downstream verifies that Maine, Michigan, or any other state isn't worse.
2. **Quantitative drift.** "Mounjaro-dominant prescribers write 47% less insulin." The writer computed a number; the description carries it; the published claim may be a rounding away from the actual data.

The validator's job is to attach **row-level evidence** to every load-bearing claim — every quantitative figure, every superlative, every comparison — and to flag any that don't hold up.

## Pipeline placement

Validation runs in two phases of the discovery lifecycle, both inside `services/agent/internal/discovery/orchestrator.go`:

- **Phase 4.5 — insight validation.** After each analysis area produces insights, the verifier + refuter pair runs on every insight, in `affected_count` descending order. The run-level cap (`VALIDATION_MAX_INSIGHTS_PER_RUN`, default 30) stops validation past the threshold; surplus insights get `combined = "skipped_budget_cap"`.
- **Phase 5.5 — recommendation validation.** After Phase 5 generates recommendations, the verifier + refuter pair runs on every kept recommendation against the token-budgeted union of source steps from its related insights.

Phase 5 also gains a **pre-generation filter**: only insights with `combined ∈ {supported, confirmed}` are forwarded to the recommendation prompt. When that set is empty, recommendation generation is skipped entirely and a `RecommendationStep{Status: "skipped_no_eligible_insights"}` is persisted for observability.

```
Phase 3   exploration              ────► explorationResult.Steps (in-memory)
Phase 4   analysis (per area)
            generate insights[]
   ┌───────────────────────────────────────────────────┐
   │ Phase 4.5  validate INSIGHTS                      │
   │   for each insight (desc by affected_count):      │
   │     verifier.Verify(bundle)                       │
   │     verifier.Refute(bundle)  (if enabled)         │
   │     insight.Validation = Combine(v, r)            │
   └───────────────────────────────────────────────────┘
Phase 5   recommendations
            recommenderInput = filter(allInsights, supported+confirmed)
            recs            = generateRecommendations(recommenderInput)
            recs            = validateRelatedInsightIDs(recs, eligibleSet)
   ┌───────────────────────────────────────────────────┐
   │ Phase 5.5  validate RECOMMENDATIONS               │
   │   for each rec:                                   │
   │     bundle = union of related insights' steps     │
   │     verifier.Verify(bundle)                       │
   │     verifier.Refute(bundle)  (if enabled)         │
   │     rec.Validation = Combine(v, r)                │
   └───────────────────────────────────────────────────┘
Phase 6+  persistence, embed/index (unchanged)
```

## The bundle

Both agents consume a read-only `verifier.Bundle` assembled at the start of each per-doc run:

- `Doc` — the insight or recommendation's headline, description, severity, affected count, source step IDs, language.
- `SourceSteps[]` — for each cited exploration step: the SQL, row schema, sample rows (capped at `VALIDATION_BUNDLE_SAMPLE_ROWS`, default 50), per-cell character cap (`VALIDATION_BUNDLE_CELL_CHAR_CAP`, default 200), and a `truncated` flag.
- `Warehouse` — dialect (BigQuery / Snowflake / Postgres / Redshift / MSSQL / Databricks), dataset, and the run-wide filter clause (if any) the verifier MUST include in every `query_warehouse` SQL.
- `Discovery` — project / run / domain / language.
- `SourceStepsTruncated` + `SourceStepsOmitted` — set when a recommendation's source-step union exceeded the token budget (`VALIDATION_REC_STEPS_TOKEN_BUDGET`, default 12 000).
- `PriorClaims` — the verifier's enumerated claim set, copied verbatim into the refuter's bundle so both agents attack the same surface.

The bundle is built in-memory from `explorationResult.Steps` (the live slice the orchestrator already holds); no Mongo round-trip is required. The replay CLI (`services/agent/cmd/validation-replay/`) builds the same bundle from the persisted `discovery_exploration_steps` collection — useful for replay-testing a discovery without re-running the agent.

**Production vs MVP-CLI deviation**: the orchestrator wires the production `ai.SchemaProvider` (Qdrant-backed retrieval + ranking) for `lookup_schema`; the MVP CLI bypasses Qdrant and uses a `warehouse.GetTableSchemaInDataset` shim. Bundle correctness and validator behavior are identical — only the `lookup_schema` tool's ranking layer differs, and the validation finaliser doesn't depend on it.

## The four tools

Each agent loops up to N rounds (`VALIDATION_VERIFIER_MAX_ROUNDS`, default 8; refuter default 6), emitting EXACTLY ONE JSON envelope per turn:

| Envelope | Body | What it does |
|---|---|---|
| `{"lookup_schema": ["dataset.table", …]}` | Array of qualified table references | Fetches column metadata via the schema provider. |
| `{"query_warehouse": "SELECT …"}` | Bare SQL string | Runs a read-only SQL via the same self-healing executor exploration uses. |
| `{"read_step_rows": {"step_id": N, "offset": K, "limit": L}}` | Paginated step snapshot | Returns rows from the in-memory exploration step. Out-of-range offsets return `truncated: true` so the agent can mark the dependent claim `unverifiable`. |
| `{"submit_verdict": {…}}` | Full structured verdict | Terminal output. |

Extra top-level keys (e.g. `{"thinking": "…", "query_warehouse": "…"}`) are parse errors — the agent gets the error in `recent_tool_errors` and retries.

## The verdict

```jsonc
{
  "submit_verdict": {
    "doc_id": "...",
    "doc_kind": "insight",
    "mode": "verifier",
    "claims_considered": ["headline", "claim 2", "claim 3"],
    "claim_verdicts": [
      {
        "claim_text": "headline",
        "claim_kind": "headline",
        "is_headline": true,
        "status": "supported",
        "reasoning": "...",
        "evidence": {
          "kind": "step_row",
          "step_id": 25,
          "row": {"prescribers": 4688},
          "additional_rows": 0
        }
      }
    ],
    "overall": "supported",
    "overall_reason": "..."
  }
}
```

Every load-bearing claim gets a per-claim verdict. The headline lives at `claims_considered[0]` and is the verdict carrying `is_headline: true`. The `evidence.kind` must be `step_row`, `warehouse_query`, or `none` (the last is only valid for `status: "unverifiable"`); every non-unverifiable claim MUST attach an `evidence.row`.

## Coverage finaliser

After the agent submits, the verifier package's `finalise()` runs deterministic checks. Any failure rewrites `Overall` (typically to `partial`):

| Step | Check |
|---|---|
| 0 | No duplicate `claims_considered` entries; no duplicate `claim_verdicts.claim_text` (catches the "all empty strings" failure mode). |
| 1 | `claims_considered` is non-empty (else `unverifiable`). |
| 2 | Exactly one `claim_verdicts[i].is_headline: true`; that entry's `claim_text` matches `claims_considered[0]` verbatim. |
| 3 | The set of `claim_verdicts.claim_text` equals the set of `claims_considered`. |
| 4 | Every claim with status in `{confirmed, supported, rejected}` has `evidence.kind != ""` AND `evidence.row != nil`. |
| 4.5 | Every per-claim `status` is a known value (catches typos like `"spported"`). |
| 5 | If the headline claim is `unverifiable`, overall is `unverifiable`. If every claim is `unverifiable`, overall is `unverifiable`. If any claim is `unverifiable`, overall downgrades to `partial`. |
| 6 | If the model omitted top-level `Overall`, derive it conservatively from per-claim verdicts (any rejected → rejected; all confirmed and no supported → confirmed; any supported/confirmed → supported; else unverifiable). |

These checks are what mechanically distinguish "the model claimed coverage" from "the model actually achieved structurally-valid coverage." The MVP smoke runs showed each one fires against real LLM output — see the running notes in `open-source/plans/PLAN-LLM-NATIVE-VALIDATION-MVP-FINDINGS.md`.

## Refuter discipline

The refuter is adversarial: its job is to find row-level evidence that contradicts the verifier's claims. Without enforcement, the model short-circuits with "I tried and could not refute" verdicts that never actually inspected any data. Two deterministic gates prevent this:

1. **In-loop**: every refuter `submit_verdict` on a normal round is rejected if `queries_issued + step_reads_used == 0`. The rejection is appended to `recent_tool_errors`; the model must call a tool and retry. `lookup_schema` does NOT count as evidence-gathering — only `query_warehouse` and `read_step_rows` do.
2. **Forced final**: when the loop runs out of rounds, the forced-final verdict is accepted but the `Overall` is downgraded to `partial` with reason `refuter discipline: zero tool calls before forced final`.

## Combine() — the 7-status matrix

After verifier and refuter finalise, `valmodels.Combine(v, r, refuterDisabled)` merges them into one of seven combined statuses:

| Combined | Meaning |
|---|---|
| `confirmed` | Verifier produced exact-match row evidence; refuter could not refute. |
| `supported` | Verifier produced consistent row evidence; refuter could not refute (or refuter intentionally off). |
| `rejected` | Verifier OR refuter produced row evidence contradicting the headline. |
| `partial` | Verifier covered some claims but not all OR verifier supported but refuter incomplete OR a coverage check failed. |
| `unverifiable` | No row evidence available for the headline (binary columns, beyond-snapshot, no tool succeeded). |
| `validation_disabled` | Validator not constructed (no LLM client at run start). |
| `skipped_budget_cap` | Run-level cap reached before this doc was reached. |

`refuterDisabled` distinguishes "refuter intentionally not run" (no penalty — `supported` stays `supported`) from "refuter expected but missing" (treated as evidence gap — `supported` downgrades to `partial`). When `VALIDATION_REFUTER_ENABLED=false`, every doc carries `RefuterDisabled: true` so the dashboard renders a "refuter off" pill instead of a misleading "incomplete refutation" badge.

The full 7×7×2 = 98-cell truth table is exhaustively tested in `libs/go-common/models/validation/verdict_test.go`.

## Wire-type ownership

Verdict types live in `libs/go-common/models/validation/` so the agent (writes) and the API (reads) share one definition:

- `StructuredVerdict`, `ClaimVerdict`, `ClaimEvidence` — what each agent emits.
- `Status`, `DocKind`, `AgentMode` — taxonomy.
- `InsightValidation` — the doc-attached summary (legacy `Status`/`VerifiedCount` fields + the new `Verifier`/`Refuter`/`Combined`/`RefuterDisabled` fields).
- `Combine()` — the 7-status matrix.

The agent + API model packages alias the local `InsightValidation` to the shared type. The standalone `StandaloneInsight` / `StandaloneRecommendation` in `libs/go-common/models/` carry the same pointer.

## Dashboard rendering

The dashboard surfaces the verdict in three places:

1. **Insight detail page** — sticky right sidebar shows a compact `<ValidationPanel />` with the combined verdict badge, a decision-friendly tagline (e.g. "Evidence disagrees with at least one headline claim."), an optional "refuter was disabled" note, and a "Show breakdown" button.
2. **Recommendation detail page** — same compact panel, same sidebar position.
3. **Discovery overview** — "Validation" accordion in the technical-details section renders one `<ValidationLogRow />` per validated insight: analysis area, verdict badge, one-line reason, "Show breakdown" button.

Clicking **Show breakdown** opens the shared `<ValidationBreakdownDrawer />`. The drawer stacks two cards — verifier above refuter — each showing:

- The agent's overall verdict (`confirmed` / `supported` / `partial` / `rejected` / `unverifiable`) and overall reason.
- A one-line stats readout: tokens in/out, duration, SQL queries issued, step rows read.
- The per-claim breakdown. The headline claim is **pinned first** regardless of input order; remaining claims preserve narrative order. Each claim shows its status badge, claim text, agent reasoning, and an evidence cell.

The evidence cell handles three kinds:

- `step_row` — renders the cited step number, a key-value table of the row, and a "(+N more rows not shown)" note when the agent saw additional rows beyond the one it picked.
- `warehouse_query` — renders the SQL the refuter ran plus the result row.
- `none` — explicit "No evidence attached." line so the reader knows the agent declined to back the claim with a citation.

All UI lives under `ui/dashboard/src/components/validation/`. A single router, `<ValidationPanel />`, dispatches between the new shape and the legacy (pre-plan-v5) `<LegacyValidationCard />` using a tiny `validationShape.ts` predicate. When legacy docs are retired, the legacy card and the predicate can be deleted in one commit — the router collapses to `return <NewValidationPanel ... />`, and no page-level call site changes.

Decision-maker status copy (label + tagline + tone) lives in `statusMeta.ts` — single source of truth so changing how we describe `unverifiable` is a one-file edit.

## What's deliberately out of scope

- **Parallel doc-level validation** (v1.5). Today the loop is sequential — `VALIDATION_CONCURRENCY` is not exposed until `go test -race` proves the verifier package's shared paths are safe.
- **Live row-cap on `ExplorationStep.QueryResult`** (separate plan). The verifier reads from the in-memory step snapshot; persistence is downstream.
- **Stricter executor-side filter check** (separate plan). `queryexec.verifyFilter` checks for the filter field NAME in the SQL, not predicate position. If telemetry shows the model exploits the gap, a follow-up plan tightens the executor.
- **`headline_claim` writer-side field**. Deferred unless coverage telemetry warrants it.
- **Migration of legacy `InsightValidation` shape**. Additive — no script needed; legacy fields stay populated on pre-plan docs.

## See also

- [Discovery lifecycle](../concepts/discovery-lifecycle.md) — where Phase 4.5 and Phase 5.5 sit in the run.
- [Configuration → Validation](../reference/configuration.md#validation) — every env var documented.
- `open-source/plans/PLAN-LLM-NATIVE-VALIDATION.md` — design doc + revision history.
- `open-source/plans/PLAN-LLM-NATIVE-VALIDATION-MVP-FINDINGS.md` — running notes from the standalone MVP smoke runs.
