# PLAN — Discovery analysis quality for smaller models (R1–R3, R5–R6 + "Enable reasoning")

Branch: `jale/issue-347` (extends PR #348, **no new PR**). Continue in-session after compaction.

## North-star constraint (non-negotiable)
**Big models that already work (Opus, GPT, large-window) must be byte-identical to today.**
Every change engages ONLY on a path a big model never takes:
- R1/R2 → only when strict JSON parse fails / yields 0 insights.
- R3 → only when reasoning is *effective* (checkbox OR catalog flag); non-reasoning + Opus keep exactly 4096.
- R5/R6 → only when picked evidence *exceeds* the window budget (big models fit → trim loop never runs).
- "Enable reasoning" → default OFF = today; native thinking (`ReasoningEffort=On`) set only when the operator explicitly checks it.

Enforced with **golden no-op tests** + a **before/after real-Opus diff** proving exploration output + insights are identical.

Data backing (34 real analysis areas + 64 Opus exploration steps, dev stack):
- Analysis output is tiny (max 6,057 tok; 0/34 > 7K) → output caps/floors never bind; **do not touch output budgeting**.
- Opus exploration output max 3,083 / 4096 (0/64 > 3,500) → 4096 never truncates Opus.
- Input is the lever: 21/34 areas > 32K, query-results are 48–94% of the prompt.
- Open model (qwen3-235b) yielded fewer insights (3.2/area) than Opus (4.9) and gateway (7.9) despite MORE steps → **format compliance + reasoning headroom**, not evidence volume, is the dominant small-model gap.

---

## Item 1 — R1: tolerant + per-item + retried INSIGHT parsing (highest leverage)

**Problem (verified):** `parseInsights` (orchestrator.go:1311) is a single strict `json.Unmarshal` into `{insights:[…]}`; `models.Insight` has **no** `UnmarshalJSON`. One off-typed field (`"affected_count":"1200"`, `"risk_score":"0.8"`, `source_steps` as strings) → the **whole area yields 0 insights**, silently. Recommendations got tolerance in #342; insights never did. This is the #1 small/open-model quality loss.

**Changes**
- `services/agent/internal/models/discovery.go`: add `Insight.UnmarshalJSON` mirroring `Recommendation.UnmarshalJSON` — alias + `json.RawMessage` for `AffectedCount` (`coerceFlexInt`), `RiskScore`/`Confidence` (`coerceFlexFloat`), `SourceSteps` (new `coerceFlexIntSlice`), `Indicators` (new `coerceStringSlice`: scalar→[]). Reuse existing helpers. **BSON untouched** (custom method is JSON-only; persisted docs read back identically).
- `orchestrator.go`: replace `parseInsights` single-shot with per-item decode (mirror `parseRecommendations`): envelope `{"insights":[…]}` (case-insensitive) or bare top-level array → per-item `RawMessage` decode → drop bad + count → **keep all current post-processing** (Md split, UUID, `AnalysisArea`, `DiscoveredAt`). Return `([]Insight, dropped int, err)`.
- Corrective re-prompt: in the analysis loop, wrap Chat+parse in a bounded retry `ANALYSIS_PARSE_MAX_RETRIES` (default 1), firing ONLY when parse yields 0 from a non-empty response (true parse failure, not a legitimately empty area). Add `analysisRepairSuffix()` (mirror `recommendationRepairSuffix`).
- Telemetry: `insights_dropped_parse`, `analysis_parse_retries` on `AnalysisStep` (mirror recs) + status reporter.

**Big-model no-op proof:** valid int/float/[]int/[]string coerce to identical values (golden round-trip test on real Opus insight JSON). Clean batch → per-item decode → same set, same order. Retry never fires (Opus parses first try). Only a *malformed* item — rare for Opus — is now salvaged instead of zeroing the area.

**Tests:** golden decode-equality (Opus JSON identical pre/post); small-model recovery (string-typed fields → coerced, kept); one-bad-item batch keeps the rest; 0-parse → one retry → recovered; legit-empty area → no retry.

---

## Item 2 — R2: structured output on the RETRY only (folded into R1)

**Problem:** analysis uses plain `Chat` today (no `ResponseFormat`) — adding structuring to the first call would change Opus. But small/open models parse-fail more; a schema-constrained *retry* recovers them where the provider supports it.

**Changes**
- `orchestrator.go`: add `insightResponseFormat()` (JSON schema for the insight envelope, `Strict:false` to allow the open `metrics` object — mirror `recommendationResponseFormat`).
- On the R1 retry ONLY, use `ChatWithFormat` (self-gates on `SupportsStructuredOutput`); the **first call stays plain `Chat`**.

**Big-model no-op proof:** first call unchanged; Opus never retries. Bedrock open models (qwen/GLM/DeepSeek) auto-drop the format (#345) → fall back to plain + R1's repair suffix. Recommendation path untouched.

---

## Item 3 — R3: window-budgeted exploration output ceiling, gated on *effective reasoning* (catalog-independent)

**Problem (verified):** `explorationOutputTokens()` (exploration.go:117) = `min(cap, EXPLORATION_MAX_OUTPUT_TOKENS=4096)`, fixed and shared across all 3 parse-retries, NOT budgeted against the window. Reasoning models (qwen3, deepseek-r1) whose `<think>`+action exceeds 4096 truncate identically on every retry → step fails. The catalog `Reasoning` flag is incomplete (misses Claude, GPT, and every uncatalogued reasoning model) so gating on it is the wrong instrument.

**Design:** ceiling = window-budgeted when reasoning is effective, else exactly today.
```
effectiveReasoning = reasoning_enabled(checkbox)  ||  gollm.IsReasoningModel(provider, model)
if effectiveReasoning:
    ceiling = clamp( min(reasoningExplorationCeiling=16384, outputCap),
                     explorationMinOutputTokens, window − inputEst − margin )
else:
    ceiling = min(outputCap, EXPLORATION_MAX_OUTPUT_TOKENS)      // == today, unchanged
```
- Thread `window` (from the run-start resolver, reuse #347's `LLMInputWindow`) + `inputEst` (rune/4 of the exploration prompt) into the exploration engine.
- Reuse `llm.Budget` for the window math (consistent with analysis).
- Exploration already flows through `ai.Client` → the #347 adaptive-overflow retry is the backstop if a raised reservation ever overflows a tight window.

**Big-model no-op proof:** Opus is unflagged + checkbox default off → `effectiveReasoning=false` → ceiling = `min(cap,4096)` = **byte-identical to today** (Opus emits ≤3,083). Non-reasoning models unchanged. Only reasoning-effective models get headroom (fixing the truncation that hurts small reasoning models).

**DECISION (locked):** reasoning-effective-only. The window-budgeted ceiling applies ONLY when `effectiveReasoning` (checkbox on OR catalog flag). Non-reasoning models + Opus keep exactly `min(cap,4096)` = today. Not "always for all".

**Tests:** Opus/non-reasoning → 4096 unchanged; reasoning model → window-budgeted 16K; tiny window → budgeted down, floor respected; overflow → adaptive retry catches.

---

## Item 4 — "Enable reasoning" checkbox — OLLAMA ONLY (option A, DECISION locked)

**Goal:** a checkbox on the analysis LLM config, **default OFF (= today)**, **shown ONLY for the Ollama provider** (hidden everywhere else, because only Ollama exposes a native reasoning toggle we wire). When on: (a) enables Ollama native Think (capability-checked via `/api/show`), and (b) marks the model reasoning-effective for R3 exploration headroom.

**Changes**
- Add the `reasoning_enabled` bool `ConfigField` (default false, label "Enable reasoning", help: "Produce hidden chain-of-thought during discovery. Off by default; enable for reasoning models like qwen3 / DeepSeek-R1.") **ONLY to the Ollama provider's `ProviderMeta.ConfigFields`** (providers/llm/ollama/provider.go). Because the dashboard renders `selected.config_fields`, it appears only when Ollama is the selected provider — no UI conditional needed. Add `gollm.ReasoningEnabled(cfg) bool` + `ReasoningEnabledKey` helper in registry.
- Dashboard `LLMFormFields.tsx`: renders automatically (bool ConfigField → checkbox, like `tls_skip_verify`); confirm placement + that it's absent for non-Ollama. (Overlay: LLMFormFields.tsx not overlaid — verified.)
- Agent: `reasoningExplicit = gollm.ReasoningEnabled(project.LLM.Config)` (only ever true for Ollama). `ai.Client.SetReasoning(explicit)`; `createMessage` sets `req.ReasoningEffort = ReasoningEffortOn` when explicit, else `Default("")` (today).
- **R3 gating:** `effectiveReasoning = reasoningExplicit || gollm.IsReasoningModel(provider, model)`. Ollama: checkbox OR catalog flag. Non-Ollama: catalog flag only (so catalog-flagged reasoning models like Vertex deepseek/gemma still get headroom automatically — no checkbox needed; big models unflagged → no headroom → unchanged).
- **Ollama Think gate:** enable Think when `effort=On` AND the model self-reports `thinking` (`/api/show` capabilities, contains `model.CapabilityThinking`) OR is catalog-flagged. Capability checked BEFORE sending `think=true` → no 400. Backstop: retry-without-think on "does not support thinking".

**Big-model no-op proof:** checkbox absent for non-Ollama → `reasoningExplicit=false` there. Opus (Bedrock, unflagged) → `effectiveReasoning=false` → 4096, no reasoning param → byte-identical.

**Deferred gap (documented, out of scope):** an UNCATALOGUED reasoning model on a NON-Ollama service (e.g. deepseek/qwen via LiteLLM / Bedrock-openai-compat) has no operator override here → relies on catalog flag only, so it won't get R3 headroom unless flagged. Test qwen3 / DeepSeek-R1 via **Ollama** (where the checkbox + capability detection apply). Non-Ollama operator override + native reasoning (Claude thinking, OpenAI effort, LiteLLM `supports_reasoning` auto-detect) = future follow-up.

**Ollama native-thinking — get capability FROM THE MODEL (DECISION locked, refined):** `reasoningEffortToThinkValue` currently gates `on` on the catalog `IsReasoningModel`, so the checkbox can't enable Think on an **uncatalogued** Ollama reasoning model, and forcing it risks Ollama's "model does not support thinking" 400.
- **Refined mechanism (catalog-independent, #347-consistent):** Ollama's `/api/show` returns `Capabilities []model.Capability` which contains `model.CapabilityThinking ("thinking")` for models that support it (verified in ollama@v0.18.1: `types/model/capability.go`, `server/routes.go`). Use it: `modelSupportsThinking = showCapabilities.contains("thinking") || IsReasoningModel(catalog)`.
- Extend the Ollama provider: fetch+cache `/api/show` capabilities once (reuse the same call as the #347 `ResolveModelInfo` context-window lookup), and gate Think on `modelSupportsThinking` instead of only the catalog flag. Because we check capability BEFORE sending `think=true`, we never hit the 400 — no blind force. Keep a retry-without-think as a backstop only.
- Gate result: operator checked reasoning (`effort=on`) AND model self-reports `thinking` → Think on. Catalog-flagged models: unchanged (already `IsReasoningModel`). Non-thinking models: safely ignored (no error). Uncatalogued reasoning models: now work.
- Providers not yet wiring `ReasoningEffort` (Claude/Bedrock/OpenAI/Vertex): the checkbox gives them **R3 headroom only** (their default-reasoning models — deepseek-r1, qwen on Bedrock — already emit thinking; headroom is what they need). Native thinking-param wiring for those is **out of scope here**; follow-up.

**Big-model no-op proof:** default OFF → `reasoningExplicit=false` → `ReasoningEffort=Default` (today) + Opus unflagged → `effectiveReasoning=false` → 4096 (today). Zero change unless the operator opts in.

**Tests:** field renders + persists false by default; agent reads it; Ollama gate honors explicit-on + falls back on unsupported; Opus with box unchecked = byte-identical.

---

## Item 5 — R5: smart overflow (dedup + breadcrumb), trim-path only

**Problem:** `AnalysisStepPicker.Pick` budget-trim loop drops **lowest-scored** steps until it fits — but lowest-score ≠ least-informative (redundant high-score steps survive), and the model is never told what was cut.

**Changes (inside the `for tokens > budget` loop only)**
- Dedup: when trimming, prefer dropping near-duplicate steps (same table set + normalized query/purpose) over unique evidence — keep the highest-scored per cluster.
- Breadcrumb: collect dropped steps' one-line purposes; pass a compact "Also examined (not shown): …" line into the analysis prompt via `buildAnalysisAreaPrompt` so the model knows the evidence exists.

**Big-model no-op proof:** the trim loop `if tokens<=budget break` returns before any of this when evidence fits. Big models fit → picked set + prompt identical, no breadcrumb.

**Tests:** under-budget → picked set + prompt byte-identical (golden); over-budget → duplicates dropped before uniques, breadcrumb present.

---

## Item 6 — R6: window-aware re-compaction of survivors, trim-path only

**Problem:** compaction is fixed head+tail 5+5 / cardinality 20 regardless of window. When a small window forces trimming, we drop whole steps instead of shrinking per-step detail to keep more steps.

**Feasibility (verified):** `ExplorationStep.QueryResult` holds the raw rows in memory at analysis time (alongside the pre-built `CompactResult`), so re-compaction is possible without re-querying.

**Changes**
- Parameterize `BuildCompactResult` (libs/go-common/models) with head/tail row counts (currently constants) — add a variant/opts.
- In the trim loop, BEFORE dropping a step: rebuild survivors' `CompactResult` at a tighter head/tail (e.g. 2+2) to fit more steps; drop only if still over budget.

**Big-model no-op proof:** engages only when over budget; big models keep the pre-built 5+5 digest → identical. (The default `CompactResult` computed at exploration time is never mutated for a fitting run.)

**Tests:** under-budget → digests unchanged; over-budget → survivors re-compacted tighter, more steps retained vs plain drop.

---

## Dropped: R4 (exact token counter)
Low value for the target: big models with an exact counter never trim; the target small/open models (Ollama, LiteLLM-qwen, Bedrock-qwen) lack an exact counter → fall back to rune/4 anyway. The 15% margin + adaptive retry already absorb rune/4 error. **Excluded.**

---

## Ordering, flags, safety
1. R1+R2 → 2. R3 + Reasoning checkbox (Item 4) → 3. R5+R6.
- **Smart-overflow toggle (DECISION locked): a PER-PROJECT SETTING** (dashboard toggle, default ON), mirroring the existing per-project validation toggle (`EffectiveValidationEnabled` / `ValidationEnabled` pattern — find where that lives and add `smart_overflow_enabled` alongside it, threaded into `OrchestratorOptions` and consulted by the picker trim path). Not an env-only kill-switch. R5/R6 no-op when off (→ today's plain drop-lowest-scored trim).
- `ANALYSIS_PARSE_MAX_RETRIES` (R1) stays an env knob (default 1).
- Every item lands with its **golden no-op test** + a **small-model recovery test**.

## Real-model verification (dev stack — projects already available)
Run and compare, capturing agent logs + Mongo `discovery_analysis_steps`/`discovery_exploration_steps`:
1. **Opus (Bedrock, `opus` project) — regression gate:** run before/after; assert exploration output tokens + parsed insights **byte-identical** (checkbox hidden for Bedrock, unflagged → no headroom, no reasoning param). This is the "don't touch what works" proof.
2. **qwen3 via Ollama — reasoning ON vs OFF:** the checkbox + `/api/show` capability path. Provision/point an Ollama qwen3 project (pull the model if needed). Box ON → native Think + headroom; measure insight yield + exploration step success (fewer truncated actions). Box OFF → today.
3. **DeepSeek-R1 via Ollama:** box ON → headroom lets its long `<think>` not truncate the exploration action; insight parse-recovery (R1) works.
4. **qwen3-235b (LiteLLM `runpod`/`aws`) — R1 format-compliance gate:** confirm R1 salvages insights that strict parsing would have zeroed (no reasoning checkbox for LiteLLM — that's the deferred gap).
5. Force a malformed-insight batch (string-typed fields) and confirm R1 keeps the valid insights instead of zeroing the area.

Note: the Ollama reasoning checkbox needs a real Ollama server. If the dev stack has none, run Ollama locally in the container (pull qwen3 / deepseek-r1) to exercise the capability path; otherwise unit-test the `/api/show`-capability gate with a mock and note the live gap.

## Gates before push
`make build` · `make test-go` · `make lint-go` · dashboard `tsc` + `next build` + `jest` + `eslint` · `make lint-docs` · targeted `make test-integration`. Docs: update `configuration.md` (`ANALYSIS_PARSE_MAX_RETRIES`, reasoning field), `configuring-llm.md` (Enable reasoning), `agent-analysis-compaction.md` (smart overflow), `CHANGELOG`.

## Files touched (estimate)
- `models/discovery.go` (Insight.UnmarshalJSON + coerce helpers)
- `discovery/orchestrator.go` (parseInsights per-item + retry + breadcrumb + reasoning wiring)
- `discovery/analysis_step_picker.go` (dedup + re-compaction in trim loop)
- `libs/go-common/models/compact_*.go` (parameterized head/tail)
- `ai/exploration.go` (window-budgeted, reasoning-gated ceiling)
- `ai/client.go` (SetReasoning → ReasoningEffort)
- `agentserver/agentserver.go` (resolve reasoning_enabled, thread window into exploration)
- `libs/go-common/llm/registry.go` (ReasoningConfigField + helper) + `provider.go` (schema)
- `providers/llm/ollama/provider.go` (honor explicit effort + graceful fallback)
- `ui/dashboard/.../LLMFormFields.tsx` (checkbox — auto-renders; confirm placement)
- docs + CHANGELOG + tests

## Decisions
1. R3 gating → **reasoning-effective-only. LOCKED.**
2. Smart-overflow toggle → **per-project dashboard setting, default ON. LOCKED.**
3. Ollama uncatalogued-reasoning → **INCLUDE. LOCKED.** Detect thinking support FROM THE MODEL via `/api/show` `capabilities` (contains `"thinking"`) — the same call #347 uses — not the catalog and not the operator's blind assertion. Enable Think only when reasoning requested AND the model self-reports `thinking`. No 400 risk (checked before sending). Flagged models untouched.
4. Reasoning checkbox scope → **OLLAMA ONLY (option A). LOCKED.** Shown only for the Ollama provider (field lives in Ollama's meta), hidden everywhere else. Native reasoning wiring for Claude/OpenAI/LiteLLM = future follow-up. Non-Ollama catalog-flagged reasoning models still get R3 headroom automatically via the catalog flag; uncatalogued non-Ollama reasoning models are a documented deferred gap.

**All decisions locked. Plan is ready to build.**
