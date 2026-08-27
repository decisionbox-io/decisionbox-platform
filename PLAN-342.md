# Plan — #342: Recommendations silently zeroed by strict parse of `expected_impact`

## 1. Problem

Discovery runs finish with insights but **0 recommendations, silently, across every LLM model**. The recommender generates good content; the parse then throws the **entire batch** away because of one field's type.

Captured failure (redshift project `6a8ec022eb80c67bce27b911`, dev — in `discovery_recommendation_log`):

```
insight_count=1, response_len=3320, parsed_recommendations=0,
error: parse error: json: cannot unmarshal string into Go struct field
       Recommendation.recommendations.expected_impact of type models.Impact
```

The model wrote `expected_impact` as a prose string (e.g. *"improves utilization accuracy and revenue forecasting"*) instead of the `Impact` object. `json.Unmarshal` of the whole `{"recommendations":[…]}` envelope fails → the parse returns `[]` → 0 recommendations persisted, no error surfaced to the user.

## 2. Root cause

Two independent defects compound:

1. **`models.Impact` has no tolerant decoding.** `Recommendation.ExpectedImpact` is a struct (`services/agent/internal/models/discovery.go:131`, type at `:145`) with no custom `UnmarshalJSON` → Go's default **strict** decoder rejects a JSON string for a struct field. Any model that phrases impact as prose (natural, common, model-independent) trips it.

2. **Whole-batch decode.** `generateRecommendations` decodes the envelope in one shot (`orchestrator.go:1331-1340`):

   ```go
   var result struct {
       Recommendations []models.Recommendation `json:"recommendations"`
   }
   if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
       step.Error = fmt.Sprintf("parse error: %s", err.Error())
       return make([]models.Recommendation, 0), step   // <- drops ALL recs
   }
   ```

   One malformed field on **one** recommendation aborts the decode for the **whole array**, so N-1 perfectly valid recommendations are lost with it.

This is the same strict-JSON family as pack-gen **#231** and exploration **#341**, but higher-severity: a single bad field nukes the entire array, and the failure is invisible to the user (the run reports success; the only trace is `discovery_recommendation_log.error`).

**Scope note — where the fix belongs.** The raw LLM JSON is decoded into `models.Recommendation` at exactly one place: `generateRecommendations`. Everything downstream (the persisted `DiscoveryResult.recommendations`, the `StandaloneRecommendation` written in the embed/index phase at `phase_embed_index.go:117`, the verifier digest at `verifier/bundle.go:238`, the API read models) builds from that already-parsed struct via **BSON**, never from raw LLM JSON. So coercing at this single boundary is sufficient and correct; the API-side `Impact` (`services/api/models/discovery.go:87`) and `libs/go-common/models.ExpectedImpact` decode BSON that is already object-shaped and are intentionally left untouched (adding a JSON method there would be dead code — Rule 6/8).

## 3. Approach

Three changes, in priority order. #1 and #2 are the fix and cover every acceptance criterion; #3 closes the "silent" gap.

### Fix 1 — Tolerant `Impact.UnmarshalJSON` (accept object **or** bare string)

Add a custom `UnmarshalJSON` to `models.Impact` (`services/agent/internal/models/discovery.go`) that accepts either shape:

- **object** → decode normally (via a type alias to avoid infinite recursion);
- **string** → coerce into `Impact{Reasoning: <string>}`;
- **`null`** → leave the zero value (json calls `UnmarshalJSON` even for `null`; treat as no-op);
- anything else (number, array, …) → return the decode error so the offending **item** is dropped by Fix 2 (not the batch).

`Reasoning` is the coercion target because it is the free-text field and it already flows into the embedding text (`StandaloneRecommendation.BuildEmbeddingText`), the validation digest (`verifier/bundle.go` includes a rec whose only populated impact field is `Reasoning`), and the dashboard's impact rendering — so a prose impact is preserved end-to-end rather than discarded.

BSON is unaffected: the driver uses struct tags / `UnmarshalBSON`, not `json.Unmarshal`, so the existing round-trip tests (`TestRecommendationJSON_RoundTrip`, `TestImpactFieldsRestored`, `TestImpact_OptionalFields`) and all persisted-doc reads keep working.

### Fix 2 — Per-item decode; skip the offender instead of zeroing the array

Refactor the parse block in `generateRecommendations` into a small, unit-testable helper:

```go
// parseRecommendations decodes the recommendation envelope per item.
// A malformed envelope (not valid JSON / no recommendations key) returns a
// hard error. A malformed individual item is skipped and counted, so one bad
// recommendation can't discard the valid ones. Returns kept recs + parse-drop count.
func parseRecommendations(cleaned string) ([]models.Recommendation, int, error)
```

- Decode into `struct{ Recommendations []json.RawMessage }` (envelope-level). If **that** fails → hard error (preserve today's `step.Error` + WARN + 0-recs behaviour — a truly unparseable blob still surfaces).
- Loop each `json.RawMessage`, `json.Unmarshal` into a `models.Recommendation`. On per-item error: log a WARN with the reason (and the item index; never the full item to keep logs bounded), increment `parseDropped`, `continue`.
- Return the kept slice + `parseDropped`.

`generateRecommendations` keeps its existing post-processing (UUID assignment, `splitMarkdownDescription`, `CreatedAt`) over the kept slice, records `parseDropped` on the step, and sets `step.Recommendations` to the kept slice (as today).

Mirrors the `json.RawMessage` per-item pattern already used by the exploration parser (`ai/exploration.go`, #341) and the pack-gen tolerant decode (#231).

### Fix 3 — Surface it (no more silent empty section)

Reuse the existing drop-telemetry infrastructure (`RecommendationStep` from #240) rather than inventing a new channel.

- **New per-reason counter** on `models.RecommendationStep`: `RecommendationsDroppedParse int` (bson `recommendations_dropped_parse,omitempty`), alongside the existing `…DroppedMissingIDs` / `…DroppedUnknownID`. `generateRecommendations` stamps it; `RecommendationsDropped` (the grand total) composes as `parse + missing_ids + unknown_id`.
- **Compose the two drop stages instead of clobbering.** `applyRecommendationDropStats` (`validation_phase.go:408`) currently *overwrites* `RecommendationsDropped = stats.Total`. Change it to `step.RecommendationsDroppedParse + stats.Total` so parse-drops (recorded earlier in `generateRecommendations`) and related-id-drops both land in the total. The `step.Recommendations = kept` re-sync stays.
- **Status marker.** Extend the existing `RecommendationStep.Status` convention (today only `skipped_no_eligible_insights`). When the phase produces **0** recommendations *because* of a parse failure/drop (envelope error, or every item dropped), set `Status = "recommendation_parse_error"` and ensure `Error` is populated + a WARN is logged. When ≥1 rec survives, no status is set — the drop counters alone carry the partial-loss signal.
- **Expose to the API.** Add `Status` and `RecommendationsDroppedParse` to `RecommendationLogEntry` (`services/api/database/discovery_log_repo.go:99`) so `GET /api/v1/discoveries/{id}/recommendation-log` returns them (it already returns `error` + the other drop counters).
- **Render in the dashboard.** On the discovery detail page (`ui/dashboard/src/app/projects/[id]/discoveries/[runId]/page.tsx`), fetch the recommendation-log alongside the other split logs and, when `recommendations.length === 0`, render the reason in the existing `EmptyState` (`status`/`error`) instead of the generic *"No actionable recommendations."* Extend the `getRecommendationLog` return type in `src/lib/api.ts` with `status?` + the `recommendations_dropped*` fields.

### Fix 4 — Bounded LLM re-prompt (self-heal), added per plan-review

**Two layers of self-healing.** Fixes 1–2 are already *deterministic* self-healing at the parse layer — coerce a string impact, skip/repair a bad item — free, and they cover the reported failure. Fix 4 adds an **LLM-level** self-heal for the residual case where in-process parsing still salvages **zero** recommendations from a **non-empty** response (a fully-garbled envelope, prose-wrapped JSON the extractor misses, or a wrong top-level shape).

**This is not new machinery — it's an existing pattern.** The exploration engine already does exactly this: `runStepWithRetry` (`services/agent/internal/ai/exploration.go:582`) re-prompts the model on an unparseable action — *"Your previous response could not be parsed… respond with exactly ONE JSON object…"* — bounded by `maxParseRetries` (`:97`). There is also transport-level retry (`chatWithRetry`, `ai/retry.go`, bounded backoff on 429/5xx/network, `LLM_RETRY_MAX_ATTEMPTS`) and the SQL self-heal fixer (`SQLFixer.FixSQL` → `FixAttempt`/`FixHistory`). Fix 4 is the *content*-level analogue for the recommendation phase, mirroring `runStepWithRetry`.

Design (in `generateRecommendations`):
- After `parseRecommendations`, retry **only** when `kept == 0 && strings.TrimSpace(response) != ""` **and** the response is not a legitimate empty `{"recommendations": []}`. Never retry when ≥1 rec survived (partial loss is already counted and persisted) — so the happy path and the partial-success path cost **nothing** extra. The retry fires precisely in the silent-zero case this issue is about.
- Each attempt re-issues the call with a correction suffix (same shape as exploration's nudge): the parse error + the required envelope shape + an explicit *"`expected_impact` is an object, not prose"* reminder. Reuse `GetMaxOutputTokens(provider, model)` so a retry does not re-truncate. Take the first attempt that yields ≥1 rec.
- **Config-driven** cap (Rule 2) via `config.GetEnvAsInt("RECOMMENDATION_PARSE_MAX_RETRIES", …)` with a small named-const default — **1** (one corrective shot; Rule 8). Lower than exploration's `3` on purpose: the recommendation call is a single large-output batch, so extra rounds are far more expensive than a per-step exploration turn.
- Telemetry: `RecommendationStep.RecommendationParseRetries int` (`omitempty`) + a WARN per attempt; `step.Response` keeps the last raw body for post-mortem. Surfaced on the recommendation-log endpoint like the drop counters.

**Honest scope note.** After Fixes 1–2 the marginal value is small: the coercion is what fixes the *reported* bug; the re-prompt only earns its keep on the rare fully-unparseable batch, and it does **not** fix genuine max-token truncation (that's the #140 per-model token-budget path — we log when `len(response) ≈ maxTokens` so the two are distinguishable rather than conflated). Kept deliberately minimal: one bounded batch-level retry, no per-item re-prompting, no negative-example `FixHistory` apparatus unless a later issue asks for it.

### Decision for review — Fix item #3 in the issue (schema-constrained output): **recommend deferring**

The issue's fix list also proposes *"prefer schema-constrained output where the provider supports it (reuse #340) to pin the recommendation shape at generation time."* I recommend **not** doing this in this PR:

- The tolerant parse (Fix 1) already fixes the bug for **all** providers — including `azure-foundry` and `vertex-ai`, where #340 deliberately left `SupportsStructuredOutput=false`. Schema-constraint only reduces frequency where supported; it is belt-and-suspenders, not the root-cause fix, and moves **no** acceptance criterion.
- It is real scope: the agent's `ai.Client.Chat` doesn't thread `ResponseFormat` today, so it needs a new gated client path **plus** a hand-authored JSON Schema for the recommendation envelope that must be kept in lockstep with the `Recommendation`/`Impact` structs — a fresh drift/duplication risk that cuts against Rules 2/3/8.

If Can wants it in-scope, it's a clean follow-up: a `ChatStructured` path gated on `GetProviderMeta(provider).SupportsStructuredOutput`, with graceful fallback to the (now-tolerant) plain parse. I'll open a follow-up issue referencing #340 rather than gold-plate this PR. **Prompt-hardening** (a terse discipline rule "`expected_impact` MUST be an object") is likewise deliberately excluded: the issue's whole point is that models ignore the shape, so parse-time coercion — not the prompt — is authoritative.

## 4. Files to change

**Agent (core fix + self-heal):**
| File | Change |
|------|--------|
| `services/agent/internal/models/discovery.go` | Add `Impact.UnmarshalJSON` (object/string/null tolerant); add `RecommendationsDroppedParse` + `RecommendationParseRetries` fields to `RecommendationStep` |
| `services/agent/internal/discovery/orchestrator.go` | Extract `parseRecommendations` (per-item `json.RawMessage` decode + drop count); rewire `generateRecommendations` to use it, stamp `RecommendationsDroppedParse`, set `Status`/`Error`/WARN on parse-caused 0-recs; **Fix 4** — bounded re-prompt loop (mirrors `runStepWithRetry`) with a `RECOMMENDATION_PARSE_MAX_RETRIES` named-const default (Rule 2 via `config.GetEnvAsInt`) |
| `services/agent/internal/discovery/validation_phase.go` | `applyRecommendationDropStats`: compose `RecommendationsDropped = Parse + stats.Total` instead of overwriting |

**API (surface):**
| File | Change |
|------|--------|
| `services/api/database/discovery_log_repo.go` | Add `Status`, `RecommendationsDroppedParse`, `RecommendationParseRetries` to `RecommendationLogEntry` |

**Dashboard (surface):**
| File | Change |
|------|--------|
| `ui/dashboard/src/lib/api.ts` | Extend `getRecommendationLog` return type (`status?`, `recommendations_dropped*?`, `recommendation_parse_retries?`) |
| `ui/dashboard/src/app/projects/[id]/discoveries/[runId]/page.tsx` | Fetch recommendation-log; render reason in the recommendations `EmptyState` when empty |

**Docs / changelog (Rule 4):**
| File | Change |
|------|--------|
| `docs/reference/data-models.md` | Impact §: note string→`reasoning` coercion; RecommendationStep §: add `recommendations_dropped_parse` + `recommendation_parse_retries` rows + new `status` value |
| `docs/reference/configuration.md` | Document `RECOMMENDATION_PARSE_MAX_RETRIES` (new env var — Fix 4) |
| `CHANGELOG.md` | Entry under `[Unreleased] → Fixed` |

## 5. Phases

1. **Fix 1** — `Impact.UnmarshalJSON` + unit tests (object, string, null, invalid). Confirms existing round-trip tests still green.
2. **Fix 2** — `parseRecommendations` helper + rewire `generateRecommendations`; unit tests (all-valid, one-string-impact, one-fully-malformed-item, whole-envelope-garbage, empty array).
3. **Fix 3 (agent)** — `RecommendationsDroppedParse`, `Status`/`Error`/WARN, `applyRecommendationDropStats` composition + tests (partial drop keeps others; parse-error sets status; parse + related-id drops sum correctly).
4. **Fix 4** — bounded re-prompt loop in `generateRecommendations` (mirrors `runStepWithRetry`), config const + `RECOMMENDATION_PARSE_MAX_RETRIES`, `RecommendationParseRetries` telemetry; tests (retry fires only on zero-from-nonempty; recovers on second try; never fires on partial success or legit-empty; respects the cap).
5. **Fix 3 (API)** — mirror `Status`, `RecommendationsDroppedParse`, `RecommendationParseRetries`; handler/repo test asserts they round-trip.
6. **Fix 3 (UI)** — type + `EmptyState` reason; `make test-ui` / `make lint-ui`. **Verify** whether this page is in the enterprise dashboard overlay; if so, note it (the enterprise repo isn't present in this container — flag for sync, don't guess).
7. **Docs + CHANGELOG**, then full local gate: `make build`, `make test-go`, `make lint-go` (after `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`, `make lint-ui`.
8. **Replay verification** — a Go test that feeds the captured redshift response shape (string `expected_impact`) through `parseRecommendations` and asserts ≥1 recommendation, satisfying acceptance criterion 3. (The real Mongo doc lives in the dev stack; the test encodes its shape as a fixture so it runs in CI without the DB.)

## 6. Data / schema / API / UI impact

- **Schema:** additive only. `recommendations_dropped_parse` is `omitempty` (clean runs don't gain the field); `status` already exists on the step. No migration; no change to persisted `Recommendation`/`Impact` shape — a coerced impact persists as a normal `Impact{reasoning:…}` object.
- **API:** `GET …/recommendation-log` gains two optional fields; backward compatible.
- **UI:** empty recommendations section now shows a reason when one exists; unchanged when recommendations are present.
- **Behavioural:** runs that previously persisted 0 recommendations from a string `expected_impact` now persist the recommendation(s). A rec with prose impact carries `expected_impact.reasoning` populated and the other impact fields empty.

## 7. Test strategy (failure + edge cases, Rule 9)

**`models` unit (`discovery_test.go`):**
- `Impact.UnmarshalJSON`: object (all fields) ✓; bare string → `Reasoning` set, others empty ✓; `null` → zero value, no error ✓; number/array → error ✓; full `Recommendation` with string `expected_impact` unmarshals ✓; existing round-trip/marshal tests unchanged ✓.

**`discovery` unit (`orchestrator_test.go` / new `_test.go`):**
- `parseRecommendations`: 2 valid → 2, drop 0; one item with string impact → **coerced, kept** (not dropped); one item with a genuinely malformed field (e.g. `priority: "high"`) → that item dropped, others kept, `parseDropped=1`; whole envelope garbage → hard error, 0 recs; empty `recommendations: []` → 0, no error.
- `generateRecommendations` (via `MockLLMProvider`, existing harness): string-impact response → ≥1 rec + `step.Error==""`; mixed valid+malformed batch → keeps valid, stamps `RecommendationsDroppedParse`; all-items-fail → 0 recs + `Status=="recommendation_parse_error"` + `Error` set.
- **Fix 4 retry** (`MockLLMProvider` with a scripted sequence of responses): first response garbage + second valid → recovers, `RecommendationParseRetries==1`, `Error==""`; partial success (≥1 kept) → **no** retry issued; legit-empty `{"recommendations": []}` → no retry; cap of 0 disables it; cap respected when every attempt fails (bounded, then `Status`/`Error` set).
- `applyRecommendationDropStats`: parse-drop + related-id-drop compose into `RecommendationsDropped` without clobbering; per-reason counters correct.
- **Replay fixture** (acceptance #3): captured-shape response → ≥1 recommendation.

**API:** repo/handler test asserts `Status` + `recommendations_dropped_parse` survive persist→read on `recommendation-log`.

**UI:** Jest test — `EmptyState` shows the reason when the fetched log has `status`/`error` and `recommendations` is empty; shows nothing extra otherwise.

## 8. Risks

- **Over-broad coercion.** Coercing non-string/non-object into `Reasoning` would hide real malformations. Mitigation: only `string` is coerced; every other non-object returns an error and is handled by the per-item drop (visible in telemetry), not silently swallowed.
- **`UnmarshalJSON` recursion.** Classic footgun — mitigated by decoding the object case through a `type impactAlias Impact` alias.
- **Telemetry double-count.** Parse-drops and related-id-drops must sum, not clobber. Mitigated by the explicit `Parse + stats.Total` composition and a dedicated test.
- **Enterprise dashboard overlay.** If `discoveries/[runId]/page.tsx` is overlaid in `decisionbox-enterprise`, the overlay must be synced. The enterprise repo isn't in this container; the build step will flag it explicitly rather than silently diverge.
- **Masking a systemic prompt/model regression.** Tolerant parsing could hide that a model always emits prose impact. Mitigated by `recommendations_dropped_parse` telemetry — the drop is counted and queryable per provider even when the rec is repaired/kept.
- **Retry cost / runaway loop (Fix 4).** A re-prompt doubles the (expensive) recommendation output tokens. Mitigated by firing *only* on zero-recs-from-nonempty (never on the happy or partial path), a default cap of **1**, and reuse of the existing `config.GetEnvAsInt` bound — same guard rails as exploration's `maxParseRetries` and `chatWithRetry`. Retry does **not** paper over truncation; the `len(response) ≈ maxTokens` signal is logged so a token-budget problem isn't misdiagnosed as a parse problem.

## 9. Alternatives considered

- **Prompt-hardening only** (a discipline rule pinning `expected_impact` shape) — rejected as the primary fix: the issue documents the failure as model-independent (models ignore the shape); parse-time coercion is the durable fix.
- **Schema-constrained output now (#340)** — deferred (see §3 decision): doesn't cover the unsupported providers, adds a drift-prone hand-maintained schema, moves no acceptance criterion. Better as a follow-up.
- **Tolerant decoding on all three `Impact`/`ExpectedImpact` types** — rejected: only the agent parse boundary ingests raw LLM JSON; the other two decode already-object BSON, so methods there would be dead code (Rule 6).
- **Keep whole-batch decode, only add tolerant `Impact`** — rejected: fixes the `expected_impact` case but leaves the "one bad field drops the whole array" class open (acceptance criterion 2 explicitly requires per-item resilience).
- **Retry-only (no tolerant parse)** — rejected: an LLM re-prompt is slow, costs a full extra batch, and isn't guaranteed to converge; the deterministic coercion fixes the reported case with zero extra calls. Retry (Fix 4) is the *last-resort* layer on top, not the primary fix.
- **Full `FixHistory`/negative-example apparatus for recommendations** (like the SQL fixer) — rejected for now (Rule 8): a single bounded batch-level retry + a retry counter is enough; the per-attempt training-example machinery is warranted for SQL self-heal but over-built for this path unless a later issue needs it.

## 10. Acceptance criteria → coverage

| Criterion | Covered by |
|-----------|-----------|
| String `expected_impact` parses (coerced), with a unit test | Fix 1 + `models` unit tests |
| One malformed rec keeps the valid ones (not 0) | Fix 2 + `parseRecommendations` / `generateRecommendations` tests |
| Re-running the parser over the captured redshift response yields ≥1 rec | Fix 1+2 + replay fixture test (§5.7) |

---

*This is a PLAN for review — implementation follows after approval.*

Closes #342

— Co-coded with Jale 🤖
