# Implementation plan — issue #432

**Recommendation prompt sends every insight's full validation transcript and overflows small context windows**

---

## 1. Problem

`generateRecommendations` serialises the whole `models.Insight` struct into `{{INSIGHTS_DATA}}`:

```go
// services/agent/internal/discovery/orchestrator.go:1800
insightsJSON, _ := json.MarshalIndent(insightsForRecommenderPrompt(insights), "", "  ")
```

`insightsForRecommenderPrompt` (`orchestrator.go:1537`) copies the slice and clears exactly one field, `DescriptionMd`.
Everything else on the struct reaches the prompt — including `Validation`, which #238 extended with the verifier's and the refuter's full structured verdicts (`ClaimsConsidered`, per-claim `Reasoning`, and a cited `Evidence.Row` + `QuerySQL` per claim, twice over: defender and skeptic).

On a real run with 14 eligible insights, counted with the model's own tokenizer:

| Part of the prompt | Characters | Tokens |
|---|---:|---:|
| Whole prompt | 235,701 | 63,658 |
| `INSIGHTS_DATA` | 214,021 | 58,418 |
| of which `validation` blocks | 180,101 | **51,135** |
| of which the insights themselves | 25,902 | 7,234 |

A model with a 40,960-token context window rejects the request and the run finishes with **0 recommendations**.

Nothing in the recommendation path reads those transcripts:

- No pack recommendation template mentions validation, verifiers, refuters or verdicts — verified across all four packs in `domain-packs/*/prompts/base/recommendations.md` and the five API seed packs in `services/api/internal/handler/seed/*.json`. (`system-test`'s pack says "Validation Findings", but that is warehouse *system-test* wording for its own insights, not the insight-validation verdict.)
- The verdict is applied **before** the prompt is built: `filterEligibleInsights` (`validation_phase.go:317`) picks the eligible set at `orchestrator.go:1287`, and `generateRecommendations` receives only that set.
- `discipline.RecommendationsRules()` (`services/agent/internal/discipline/rules.go:271-351`) never mentions validation either.

So ~80% of the prompt is payload no reader of that prompt has ever used.

### What still needs those fields (must keep working)

The *stored* insights must be untouched. These consumers read the same slice after the recommendation phase:

- `validation_phase.go:246` — recommendation validation unions `ins.SourceSteps` across a recommendation's related insights to build the verifier bundle.
- `orchestrator.go:1677` (`attachSourceQuality`) — derives `Quality` from `SourceSteps`.
- `phase_embed_index.go:82` — carries `SourceSteps` into the embedding index.
- Persistence + the dashboard render `validation`, `sql_metadata`, `discovered_at`, `description_md`.

All of them read `allInsights` / `recommenderInput`, never the prompt copy, so trimming the prompt copy is invisible to them — provided the originals are genuinely not mutated.

---

## 2. Approach

Replace the "copy the struct and clear a field" shape with an **explicit allow-list projection**: a small struct naming exactly the fields the recommender reasons over, built from `models.Insight`.

### Why a projection instead of clearing more fields

Two concrete reasons, not style.

**(a) Clearing cannot remove `discovered_at`.** `DiscoveredAt` is tagged `json:"discovered_at"` with no `omitempty`, and `encoding/json`'s `omitempty` does not apply to a `time.Time` struct anyway. I measured it: zeroing the field still renders

```json
"discovered_at": "0001-01-01T00:00:00Z"
```

which fails the first acceptance box ("carries none of … `discovered_at`"). Adding `omitempty`/`omitzero` to the model tag instead would change the agent `Insight`'s JSON shape everywhere it is marshalled — a much wider blast radius than the issue asks for.

**(b) An allow-list is the fix for the stated cause.** The issue's own diagnosis is that the transcripts "reached the recommender prompt without anyone choosing to send them." A deny-list repeats that: the *next* field added to `models.Insight` leaks again, silently, and is only noticed when it is fatal. With a projection, a field that is not named cannot reach the prompt — leaking becomes opt-in.

This is the established pattern in this codebase for LLM-facing payloads, not a new idea: `verifier.DocDigest` (`services/agent/internal/validation/verifier/bundle.go:46`), `models.InsightSummary`, `models.FeedbackSummary` and `models.RecommendationSummary` (`services/agent/internal/models/context.go:124-144`) are all hand-written projections of larger structs for exactly this reason.

### Measured effect

Built the 14-insight shape with realistic verifier + refuter transcripts (4 claims each, cited rows and SQL) and marshalled it three ways:

| Rendering of `INSIGHTS_DATA` | Characters | rune/4 tokens |
|---|---:|---:|
| Today (clears `description_md` only) | 155,363 | 38,840 |
| Clearing the four extra fields | 14,048 | 3,512 |
| **Projection (this plan)** | **13,418** | **3,354** |
| Projection, 32 insights | 30,666 | 7,666 |

(Absolute numbers differ from the issue's table because my transcripts are synthetic; the ratio and the conclusion match. The issue's tokenizer-measured figures are 63,658 → 11,715 for 14 insights and 110,108 → 20,736 for 32. Both fit 40,960 with room for the output.)

The ~630-character gap between "clearing" and "projection" is the residual `"discovered_at": "0001-01-01T00:00:00Z"` line, i.e. point (a) above.

---

## 3. Files to change

| File | Change |
|---|---|
| `services/agent/internal/discovery/orchestrator.go` | Add `recommenderInsight`; rewrite `insightsForRecommenderPrompt` to return `[]recommenderInsight`; delete the stale orphan comment at `:1529` |
| `services/agent/internal/discovery/orchestrator_test.go` | Extend `TestInsightsForRecommenderPrompt_ClearsMarkdownCopy`; add the projection-exhaustiveness guard and the edge-case tests |
| `services/agent/internal/discovery/recommendation_parse_test.go` | Add the real-prompt test (this file already owns the `newRecOrchestrator` harness) |
| `services/agent/internal/discipline/rules.go` | **Decision point — §5.** Two clauses in `recommendationsRulesText` that point at `source_steps` |
| `docs/reference/prompt-variables.md` | `{{INSIGHTS_DATA}}` section: field list + corrected example |
| `docs/concepts/prompts.md` | `{{INSIGHTS_DATA}}` table row |
| `docs/concepts/discovery-lifecycle.md` | Phase 5 flow diagram line + prose |
| `docs/guides/customizing-prompts.md` | `{{INSIGHTS_DATA}}` table row |
| `CHANGELOG.md` | `### Fixed` entry under `## [Unreleased]` |

Nothing else. No model, schema, API, dashboard, Helm, Terraform or domain-pack change — see §6.

### The stale comment at `orchestrator.go:1529`

```go
// parseInsights parses LLM response JSON into Insight structs.
// insightsForRecommenderPrompt returns a copy of insights with the Markdown
```

`parseInsights` is defined at `:1572` with its own doc comment at `:1558`. Line 1529 is an orphan left behind when `insightsForRecommenderPrompt` was inserted above it — it documents a function that is not there. Rule 6; it is in the exact hunk being edited, so it goes.

---

## 4. Phases

### Phase 1 — the projection (the fix)

In `orchestrator.go`, replacing `:1529-1544`:

```go
// recommenderInsight is the projection of models.Insight sent to the
// recommendation prompt as INSIGHTS_DATA: an explicit allow-list of the fields
// the recommender reasons over.
//
// It is an allow-list rather than a copy-and-clear because the prompt used to
// carry the whole struct, so every field added to models.Insight reached the
// prompt whether the recommender had a use for it or not. The cost stayed
// invisible until it was fatal: the verifier and refuter transcripts attached
// to Validation made up roughly 80% of the prompt (51,135 of 63,658 tokens on a
// 14-insight run), a model with a 40,960-token window rejected the request, and
// the run finished with zero recommendations (#432). A field this struct does
// not name cannot reach the prompt, so sending a new one is a decision someone
// makes here rather than a side effect of adding it to the model.
//
// Clearing fields on a copy could not have expressed the same thing anyway:
// DiscoveredAt's tag carries no omitempty, so a zeroed value still renders as
// "discovered_at": "0001-01-01T00:00:00Z".
//
// The json tags mirror models.Insight exactly, so INSIGHTS_DATA is unchanged
// for every field the recommender does use — including the field order, which
// follows the model's own declaration order.
type recommenderInsight struct {
	ID            string                      `json:"id"`
	AnalysisArea  string                      `json:"analysis_area"`
	Name          string                      `json:"name"`
	Description   string                      `json:"description"`
	Severity      string                      `json:"severity"`
	AffectedCount int                         `json:"affected_count"`
	RiskScore     float64                     `json:"risk_score"`
	Confidence    float64                     `json:"confidence"`
	Metrics       map[string]interface{}      `json:"metrics,omitempty"`
	Indicators    []string                    `json:"indicators,omitempty"`
	TargetSegment string                      `json:"target_segment,omitempty"`
	Quality       []gowarehouse.QualityCaveat `json:"evidence_quality,omitempty"`
}

// insightsForRecommenderPrompt projects insights onto recommenderInsight. The
// argument is read, never written: the caller's insights keep description_md,
// validation, source_steps, sql_metadata and discovered_at for storage, the
// dashboard, and the recommendation-validation phase, which unions their
// SourceSteps after this call.
func insightsForRecommenderPrompt(insights []models.Insight) []recommenderInsight {
	out := make([]recommenderInsight, 0, len(insights))
	for i := range insights {
		in := &insights[i]
		out = append(out, recommenderInsight{
			ID:            in.ID,
			AnalysisArea:  in.AnalysisArea,
			Name:          in.Name,
			Description:   in.Description,
			Severity:      in.Severity,
			AffectedCount: in.AffectedCount,
			RiskScore:     in.RiskScore,
			Confidence:    in.Confidence,
			Metrics:       in.Metrics,
			Indicators:    in.Indicators,
			TargetSegment: in.TargetSegment,
			Quality:       in.Quality,
		})
	}
	return out
}
```

Notes for the reviewer:

- `gowarehouse` is already imported in `orchestrator.go:21`; no new import.
- `Metrics` / `Indicators` / `Quality` are carried by reference, exactly as today's `copy(out, insights)` does. Nothing between here and `json.Marshal` writes them, and `json.Marshal` is read-only — so no defensive deep copy (Rule 8).
- `make(..., 0, len)` keeps a nil/empty input rendering as `[]`, not `null`, matching today.
- The call site at `:1800` is unchanged — `json.MarshalIndent` takes `any`. It is the only caller.
- Struct field order = `models.Insight` declaration order, so the rendered JSON key order for kept fields is byte-identical to today's.

### Phase 2 — the two `source_steps` clauses in the recommendation rules

See §5. Decision flagged for review; if it is declined, this phase is dropped and nothing else in the plan moves.

### Phase 3 — tests (§7)

### Phase 4 — docs + CHANGELOG (§8)

### Phase 5 — verification (§9), then delete `PLAN-432.md` in the final commit and mark the PR ready

---

## 5. Decision point for review: `source_steps` in the recommendation discipline rules

Dropping `source_steps` from `INSIGHTS_DATA` leaves two clauses in `discipline.RecommendationsRules()` pointing at a field the prompt no longer contains:

```
rules.go:282  insight's `metrics` or the step rows in its `source_steps` — not
rules.go:303  `source_steps`. `related_insight_ids` must point at the insights the
```

Rule 3 tells the model to re-derive a "top N" ranking "from the insight's `metrics` or the step rows in its `source_steps`"; rule 6 says every figure must be traceable "either to the cited insight's own values or to a row in one of that insight's `source_steps`".

**These are already only half-true today.** The recommendation prompt has never carried step *rows* — `SourceSteps` is a `[]int` of step numbers, and the rows live in the exploration steps, which are not in this prompt. A model cannot check a figure against a row it cannot see. What it can do is reach for the nearest plausible-looking number, which is the failure mode the rule was written to prevent.

**Recommendation: tighten both clauses to name only what the prompt carries** — the insight's own values and `metrics`. Two clause edits, no new rules, no renumbering. The rule gets *stricter*, not looser: figures must come from the insight, full stop. That is also the only reading the model can actually comply with.

**Why it is flagged rather than just done:** the issue says "Nothing else changes: … the template", and this is prompt text every run receives on every model. It is a behaviour change, so it should be a conscious call. If you'd rather keep the prompt byte-identical, drop Phase 2 — the fix, the tests and the docs all stand without it, and the tests in §7 do not depend on it either way.

The identical `source_steps` references in `analysisRulesText:233` and `verifierRulesText:417,420` stay exactly as they are: the analysis prompt carries `{{QUERY_RESULTS}}` and the verifier bundle carries `SourceStepDigest` rows, so there those rules are satisfiable.

---

## 6. Data / schema / API / UI impact

**None.** Explicitly:

- **Persistence:** unchanged. `models.Insight` is not touched, so BSON, Mongo documents and every existing index are unaffected. No migration.
- **API:** unchanged. `services/api/models.Insight` is a separate struct and is not touched; no response shape moves.
- **Dashboard:** unchanged. It renders `validation`, `description_md`, `sql_metadata` and `discovered_at` from the stored insight, which still carries them. No enterprise UI overlay file is touched.
- **`RecommendationStep.prompt`:** this is the one observable change — the persisted prompt is smaller, because it is the prompt that was actually sent. That is the point of the issue, not a side effect. No field is added or removed.
- **Domain packs / per-project prompt copies:** untouched. Projects that have customised `recommendations.md` get the trimmed payload too, because the trimming is on the substituted value, not the template.
- Helm, Terraform, env vars: untouched. No new config knob — there is nothing here an operator would want to tune, so adding one would be Rule 8.

---

## 7. Test strategy

All in `package discovery`, reusing the existing harnesses: `newRecOrchestrator` + `testutil.MockLLMProvider` (`recommendation_parse_test.go:134`), and `step.Prompt`, which `generateRecommendations` stamps with the fully-rendered prompt at `orchestrator.go:1826`.

### Extend `TestInsightsForRecommenderPrompt_ClearsMarkdownCopy`

Kept under its current name because the acceptance box names it. Extended to build one fully-populated insight (all five dropped fields set, every kept field set) and assert:

1. The marshalled projection contains none of the JSON keys `"description_md"`, `"validation"`, `"source_steps"`, `"sql_metadata"`, `"discovered_at"`.
2. Every keep-list key is present with the right value: `id`, `analysis_area`, `name`, `description`, `severity`, `affected_count`, `risk_score`, `confidence`, `metrics`, `indicators`, `target_segment`, `evidence_quality`.
3. The input is unmutated — all five dropped fields still set on the original afterwards, `Metrics` map contents unchanged (catches an accidental shared-map write).

### New: projection exhaustiveness guard

Reflects over `models.Insight`'s json tags and fails when a field is neither in the keep list nor in an explicit `deliberatelyDropped` set, with a message telling the author to decide which. Also asserts every tag `recommenderInsight` emits exists on `models.Insight` with the same name, so a typo cannot silently rename a field in the prompt.

This is the recurrence guard, and it is the test that would have caught #238's leak at review time. It is why this is the fourth "0 recommendations" issue (#237, #342, #347, #432) rather than the last.

### New: the real recommendation prompt carries no transcript

Second half of acceptance box 3. Runs `generateRecommendations` through the mock provider with an insight whose `Validation` carries distinctive sentinel strings in `Verifier.OverallReason`, `Refuter.OverallReason`, a `ClaimVerdict.Reasoning`, and `Evidence.QuerySQL`, plus sentinels in `DescriptionMd` and `SQLMetadata.Query`. Then on `step.Prompt`:

- none of the sentinels appears anywhere in the prompt — template, `INSIGHTS_DATA`, or appended discipline rules;
- none of `"validation":`, `"source_steps":`, `"sql_metadata":`, `"discovered_at":`, `"description_md":` appears. Asserted **with the quotes and colon** so it can only match a JSON key: the discipline rules mention `` `source_steps` `` in backticks, and that prose is not what this test is about (and stays matched-as-absent whether or not Phase 2 lands);
- the insight's `id` and `name` *are* present, so the test fails on an over-trim as well as an under-trim.

Plus the size assertion that encodes the fix directly: the same insight with and without a large `Validation` produces the **same** `step.Prompt` length.

### Edge and failure cases (Rule 9)

| Case | Expected |
|---|---|
| `nil` insights slice | `[]`, not `null` (the marshalled value the prompt embeds) |
| Empty slice | `[]`; `generateRecommendations` already short-circuits before this at `:1796`, covered by the existing `TestGenerateRecommendations_EmptyInsights` |
| Nil `Metrics` / `Indicators` / `Quality`, empty `TargetSegment` | Keys omitted — no `"metrics": null` noise |
| Zero `AffectedCount` / `RiskScore` / `Confidence`, empty `Severity` | Keys **present** — they have no `omitempty` on the model either, and an absent key reads differently to a model than a zero |
| `Validation` non-nil but `Verifier`/`Refuter` nil (verdict-only, validation disabled) | Still dropped entirely |
| `Validation` nil (legacy / fail-open insight) | Renders identically to the above — the two fail-open paths in `filterEligibleInsights` both reach here |
| Insight with a 50k-character transcript | Projection size independent of it |

### Existing tests that must keep passing unchanged

`recommendation_parse_test.go` in full (the parse/retry/citation-self-heal/structured-output suite drives `generateRecommendations` end to end, so a signature or marshalling mistake surfaces there), and `orchestrator_discipline_test.go:157-260` (`buildRecommendationsPrompt` substitution + rule-block wiring).

### Commands

```bash
export PATH=$PATH:$(go env GOPATH)/bin
make build
make test-go
make lint-go
make lint-docs
```

`services/agent/go.mod` requires `go >= 1.26.8` and the container's toolchain is `go1.25.10` with `GOTOOLCHAIN=local`, so a bare `go test` fails with `go.mod requires go >= 1.26.8`. `GOTOOLCHAIN=go1.26.8` fetches the right toolchain and both `go vet` and `go test` pass in this container — verified while writing this plan.

No integration test is added: this change touches no database, warehouse or HTTP boundary, so a testcontainer here would exercise nothing the unit tests don't. The real-run check in §9 is the end-to-end evidence.

---

## 8. Documentation (Rule 4)

1. **`docs/reference/prompt-variables.md`** — the `{{INSIGHTS_DATA}}` section. Name the exact fields the variable carries, state that `validation`, `source_steps`, `sql_metadata`, `discovered_at` and `description_md` are deliberately excluded and why (the eligibility filter has already applied the verdict; the prompt never refers to them; they were overflowing small context windows). Fix the example, which today ends on `"source_steps": [1, 3, 5]` and omits `target_segment` / `evidence_quality`; also replace the `"id": "churn-1"` placeholder, which is the exact slug shape `recommendationsRulesText` forbids and #237 was about.
2. **`docs/concepts/prompts.md`** — the `{{INSIGHTS_DATA}}` row: "Full JSON array of all validated insights" → the trimmed projection, with the field list.
3. **`docs/concepts/discovery-lifecycle.md`** — the Phase 5 flow line `{{INSIGHTS_DATA}} → full JSON array of all insights (with IDs)`, plus a sentence in the Phase 5 prose next to the eligibility-filter paragraph, since that is where a reader asks what the recommender actually sees.
4. **`docs/guides/customizing-prompts.md`** — the `{{INSIGHTS_DATA}}` row: "All insights as JSON (for linking)" → note the trimmed field set and link to the reference page.
5. **`CHANGELOG.md`** — one `### Fixed` bullet under `## [Unreleased]`, in the file's house style (bold lede, then the mechanism and the consequence), covering: why a small-window model returned 0 recommendations, that the transcripts were never read there, the new allow-list and what it means for future fields, and that stored insights and the dashboard are unaffected.

`docs/guides/creating-domain-packs.md:290` needs no change — the `{{INSIGHTS_DATA}}` there is an unannotated placeholder in a template skeleton.

Rule 10: no doc, comment, commit message or CHANGELOG line mentions this plan file or its phases. `make lint-docs` enforces the first part.

---

## 9. Real verification (acceptance box 4)

Unit tests prove the payload; they cannot prove a 40,960-token model now answers. Two steps:

1. **Token-count the rendered prompt.** Take the `prompt` from the `discovery_recommendation_log` document of the run that produced 0 recommendations, re-render it from the same stored insights through the patched `insightsForRecommenderPrompt`, and count both with the model's own tokenizer. Expect the issue's figures: 63,658 → ~11,715 for 14 insights, and the 32-insight case landing near ~20,736 — both inside 40,960 with the output budget on top.
2. **Run a real discovery against a 40,960-window model** on the dev stack (dbxdev Mongo `:27099`, Qdrant `:6499`, explicit `--run-id`), reaching the model through the funded LiteLLM gateway, and confirm the run finishes with a non-empty `recommendations` array and no `recommendation_parse_error` / context-overflow error on the `RecommendationStep`.

If no 40,960-window model is reachable from this container when the build step runs, step 1 stands on its own and I will say plainly in the PR that step 2 was measured on the prompt rather than on a live model — not claim a run I did not make.

---

## 10. Risks

| Risk | Assessment |
|---|---|
| Recommendation quality drops because the model no longer sees the transcripts | The prompt never referred to them and no rule tells the model to read them, so there is nothing for the model to have been using them *for*. The verdict still gates the input set upstream. Watched via the `recommendations_dropped*` / `recommendations_citations_*` counters on the real run. |
| A future `models.Insight` field the recommender *does* need is silently missing | This is the trade: silent-omission replaces silent-inclusion. The exhaustiveness test converts it into a compile-time-ish failure with a message naming the choice. |
| `related_insight_ids` citation regresses | `id` is first in the projection and unchanged; `recoverRelatedInsightIDs` and `validateRelatedInsightIDs` still run against the same eligible set. A shorter prompt makes ID copying easier, not harder. Both citation tests in `recommendation_parse_test.go` cover it. |
| Phase 2 changes model behaviour for everyone | Why it is flagged in §5 rather than folded in silently. The rule gets stricter and points only at data the prompt contains. Droppable without touching anything else. |
| `evidence_quality` is kept, and `Quality` is derived from `SourceSteps` | `attachSourceQuality` runs at `orchestrator.go:1242`, before the recommendation phase, so `Quality` is already populated on the input. No ordering change. |
| The `time.Time` / `omitempty` trap recurs elsewhere | Out of scope here, but named in the projection's doc comment so the next reader does not rediscover it the hard way. |

---

## 11. Alternatives considered

1. **Clear the four extra fields on a copy, as the issue literally describes.** Rejected: leaves `"discovered_at": "0001-01-01T00:00:00Z"` in the payload (measured, §2a), failing acceptance box 1, and keeps the deny-list that caused the bug. This is the only place the plan departs from the issue's wording, and it departs in order to satisfy the issue's own acceptance criteria.
2. **Add `omitempty`/`omitzero` to `Insight.DiscoveredAt` so clearing works.** Rejected: changes the agent `Insight`'s JSON shape for every consumer to fix one prompt.
3. **Keep `source_steps` (a `[]int`, ~30 tokens per insight) so the discipline rules stay literally true.** Rejected: acceptance box 1 names it explicitly, and the rules' reference to it is already unsatisfiable in this prompt. §5 addresses the rule text instead.
4. **A custom `MarshalJSON` on `models.Insight`.** Rejected outright: it would change every JSON marshal of the type, not just the prompt. The model already carries a custom `UnmarshalJSON` for tolerant decoding; adding an asymmetric marshaller to it is how you get a bug nobody can find.
5. **Truncate the transcripts instead of dropping them** (e.g. keep `Combined` and `OverallReason`). Rejected: still sends something no reader of the prompt uses, and still scales with insight count. Nothing asked for a verdict summary in this prompt; adding one would be Rule 8.
6. **Raise the context-overflow retry's competence instead.** Rejected: that is the out-of-scope item below, and it is a net, not a fix — a prompt that is 5× larger than it needs to be is the defect.
7. **A config knob for which fields to send.** Rejected: Rule 8. There is no operator use case for a smaller or larger recommender payload.

---

## 12. Out of scope, and the follow-up

Per the issue:

- **The context-overflow retry reads vLLM's floor as an exact count.** `parseContextLengthError` (`services/agent/internal/ai/context_overflow.go:102`) matches `contains at least\s+(\d+)\s+input tokens` and uses that number as the input size when recomputing `max_tokens`. On vLLM that figure is a floor, not the count, so the retry recomputes against an under-estimate and can fail again. No open issue covers this — I checked the open list. Rule 3: I will **file it as a separate GitHub issue** during the build step and reference it from the PR, rather than leaving a `// TODO` behind or quietly widening this PR.
- **The analysis prompts, the executive summary and Ask.** Not touched. The analysis prompt legitimately carries step rows; whether the other two over-send is a separate question with separate measurements.
- **The eligibility filter, the output budget and overflow retry, the template and knowledge-source injection, citation recovery, recommendation validation, and the stored insights.** All unchanged, as the issue requires.

---

## 13. Acceptance criteria → where each is met

| Acceptance criterion | Met by |
|---|---|
| `INSIGHTS_DATA` carries none of `validation`, `source_steps`, `sql_metadata`, `discovered_at`, `description_md`, and still carries every keep-list field | Phase 1 projection; asserted in the extended `TestInsightsForRecommenderPrompt_ClearsMarkdownCopy` (both directions) and the real-prompt test |
| The insights passed in are not mutated; `validation` and the rest are still stored and shown | Projection reads and never writes; asserted explicitly, including the shared `Metrics` map. §6 confirms no persistence/API/UI change |
| `TestInsightsForRecommenderPrompt_ClearsMarkdownCopy` extended to the new fields, plus one test that builds the real recommendation prompt and asserts no validation transcript text is in it | Phase 3 — the extended test and `TestGenerateRecommendations_PromptCarriesNoValidationTranscript` |
| A run on a 40,960-token model that previously produced 0 recommendations produces recommendations | §9, reported honestly either way |

---

*This is a **PLAN for review** — no implementation is included in this PR. Once the plan is approved I will implement it, delete this file in the final commit, mark the PR ready, and run the Codex review loop and the Copilot pass.*

Closes #432

— Co-coded with Jale 🤖
