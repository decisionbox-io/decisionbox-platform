# Plan — Discovery analysis fails with context-length 400: `max_tokens` not budgeted against the model's context window (#347)

## 1. Problem

A discovery analysis area fails (run → **Partial**) with a hard 400 from the model:

```
channelperformance: bedrock/openai-compat: InvokeModel failed: operation error Bedrock Runtime:
InvokeModel, StatusCode: 400, ValidationException: "This model's maximum context length is 202752
tokens. However, you requested 64000 output tokens and your prompt contains at least 138753 input
tokens, for a total of at least 202753 tokens. Please reduce the length of the input prompt or the
number of requested output tokens."
```

`138,753 input + 64,000 output = 202,753` → **exactly one token over** GLM‑5's 202,752 context. The area is lost, the run degrades to Partial, and nothing retries.

## 2. Root cause (confirmed in the code)

The analysis path picks a **fixed** output cap and lets the input grow to its **own, unrelated** budget. Nothing enforces `input + output ≤ context`.

- **Fixed output.** `services/agent/internal/discovery/orchestrator.go:906` sets
  `maxTokens := gollm.GetMaxOutputTokens(o.llmProvider, o.llmModel)`. GLM‑5 is **uncatalogued**, so this resolves to Bedrock's `DefaultMaxOutputTokens: 64000` (`providers/llm/bedrock/provider.go:100`, the #338 value). The recommendation phase does the same at `orchestrator.go:1324`.
- **Independent input budget.** The analysis step picker trims `{{QUERY_RESULTS}}` only against a **fixed** `AnalysisQueryResultsBudgetTokens = 200_000` (`services/agent/internal/discovery/analysis_step_picker.go:40`), which has no relationship to the model's window. On this run the assembled prompt reached ~138,753 input tokens.
- **No coupling.** The `llm.Budget` type that computes exactly `ModelMaxInput − ReservedOutput − ReservedSystem − SafetyMargin` (`libs/go-common/llm/budget.go`) is wired into **/ask only** (`services/api/internal/handler/search.go:635`), never into discovery.
- **No overflow retry.** `services/agent/internal/ai/retry.go` treats a 400 (including `ValidationException`) as **terminal** — `isRetryableLLMError` returns false, so `chatWithRetry` surfaces it immediately with no adaptation.
- **Unknown window.** GLM‑5 is not in any catalog, and Bedrock sets no `DefaultMaxInputTokens`, so `GetMaxInputTokens("bedrock","GLM-5")` falls back to the global `DefaultMaxInputTokens = 131072` (`libs/go-common/llm/registry.go:401`) — DecisionBox doesn't know the real 202,752 window.

### Why now (regression)
**#338** raised the uncatalogued openai‑compat/Bedrock output default from ~8K to **64,000**:
- before: `138,753 + 8,000 = 146,753 < 202,752` → fit;
- after: `138,753 + 64,000 = 202,753 > 202,752` → overflow.

## 3. Design — defense in depth, 3 layers + a config path

The fix is layered so the common case never 400s, the residual case self‑heals, and the whole thing reuses the existing `llm.Budget` arithmetic rather than inventing new math.

| Layer | What | Where | Fixes |
|---|---|---|---|
| **A — Proactive output budget** | Cap `max_tokens = clamp(context − input − system − margin, floor, effective_output_cap)` before the call, per area. Reuse `llm.Budget`. | analysis + recommendation call sites in `orchestrator.go` | issue fix #1; criteria #1, #4-partial, unit-test |
| **B — Couple the input budget to the window** | Set the picker's `BudgetTokens` to `min(AnalysisQueryResultsBudgetTokens, Budget.Available())` where `Budget` reserves the effective output cap, so query‑results can't grow past `context − output − system − margin`. | orchestrator wiring of the existing `picker.BudgetTokens` override | issue fix #4; criterion #3 (small windows) |
| **C — Adaptive context‑overflow retry** | On a "maximum context length" 400, parse the model's **stated** context + input tokens from the error, recompute a safe `max_tokens`, and re‑issue **once**. Universal — covers analysis, recommendation, exploration, sql‑fix. | `services/agent/internal/ai` (new file) + one hook in `client.go createMessage` | issue fix #2; criterion #2 |
| **D — Know the real window** | Thread `project.LLM.Config` into the orchestrator so the operator override `max_input_tokens` (and `max_output_tokens`) reaches discovery budgeting via `GetEffectiveInputWindow` / `MaxOutputOverride`; document setting `max_input_tokens=202752` for GLM‑5. Optionally catalog GLM once a verified Bedrock model ID is confirmed. | orchestrator plumbing + docs (+ optional catalog) | issue fix #3 |

### Why this composes correctly
- Layer A is the precise guarantee: whatever the assembled prompt turns out to be, output is reduced so `input + output ≤ context − margin`. For the reported GLM‑5 run it makes the request fit on the **first** call (output auto‑reduced), so no 400.
- Layer B protects **small‑context** models: today the fixed 200K query‑results budget can, on a 128K model, produce an input that alone exceeds the window — unfittable even at the output floor. Tying it to `Budget.Available()` prevents that. It only ever *lowers* the budget (`min(...)`), so it never loosens today's cap. (It also quietly fixes a latent bug for Claude‑200K: 200K query‑results + any output already exceeds Claude's window.)
- Layer C is the net for estimator drift and window under/over‑estimation: rune/4 undercounts dense JSON, and an uncatalogued window is a guess. The error message states the model's **true** context and input sizes, so a single recomputed retry is exact, not a blind guess.
- Layer D removes the guess entirely when the operator knows the window, and restores full input fidelity for large uncatalogued models (without it, Layer B against the guessed 131K window over‑trims GLM‑5's input — safe, but lower quality).

## 4. Exact changes

### 4.1 New: proactive analysis/recommendation output budget
**File:** `services/agent/internal/discovery/analysis_budget.go` (new)

Small, pure, unit‑testable helpers that delegate the arithmetic to `llm.NewBudget` (the "reuse `llm.Budget`" the issue asks for):

```go
// reservedSystemTokens: flat headroom for chat-template / scaffolding overhead the
// rune/4 estimate of the user prompt does not see. The analysis + recommendation
// calls pass an empty system prompt, so this stays small.
const analysisReservedSystemTokens = 512

// analysisMinOutputTokens is the floor the output budget never drops below, so a
// near-window input still gets a usable (if small) generation instead of a
// zero/negative request. Env-overridable (Rule 2); mirrors EXPLORATION_MAX_OUTPUT_TOKENS.
const defaultAnalysisMinOutputTokens = 8192
const analysisMinOutputTokensEnv = "ANALYSIS_MIN_OUTPUT_TOKENS"

// budgetedMaxOutputTokens caps requested output so input+output fits the window.
//   out = clamp(window − input − system − margin, floor, effectiveOutputCap)
// window/effectiveOutputCap come from the caller (catalog/override aware).
func budgetedMaxOutputTokens(window, inputTokens, effectiveOutputCap, floor int) int {
    avail := gollm.NewBudget(window, 0 /*reservedOutput*/, analysisReservedSystemTokens, false /*approx tier*/).Available()
    out := avail - inputTokens
    if out > effectiveOutputCap {
        out = effectiveOutputCap
    }
    if out < floor {
        out = floor
    }
    return out
}

// analysisPickerBudgetTokens couples the picker's query-results budget to the window:
// min(default, window − output − system − margin) via Budget.Available().
func analysisPickerBudgetTokens(defaultBudget, window, effectiveOutputCap int) int {
    avail := gollm.NewBudget(window, effectiveOutputCap, analysisReservedSystemTokens, false).Available()
    if avail > 0 && avail < defaultBudget {
        return avail
    }
    return defaultBudget
}
```
A tiny `analysisMinOutputTokens()` reads the env floor (default `defaultAnalysisMinOutputTokens`, `>0` guard), matching `explorationOutputTokens`'s pattern.

**Token estimate:** use `gollm.ApproximateCounter{}` (rune/4) over the fully assembled prompt string — cheap, local, consistent with the picker's char/4 estimator and /ask's walk. On a counter error (ctx cancel only) fall back to `utf8.RuneCountInString(prompt)/4`. The 15% approximate‑tier margin baked into `NewBudget` plus Layer C absorb the undercount on dense JSON.

### 4.2 Wire the budget into the analysis phase
**File:** `services/agent/internal/discovery/orchestrator.go`

- Before the `for _, area := range runAreas` loop (near line 818, where the picker is built): compute once
  ```go
  window := gollm.GetEffectiveInputWindow(o.llmProvider, o.llmModel, o.llmConfig)
  effOut := gollm.ClampMaxTokens(gollm.GetMaxOutputTokens(o.llmProvider, o.llmModel), gollm.MaxOutputOverride(o.llmConfig))
  picker.BudgetTokens = analysisPickerBudgetTokens(AnalysisQueryResultsBudgetTokens, window, effOut)
  ```
  (`picker.BudgetTokens` is already an honored override in `Pick`; today it's unset → defaults to 200K.)
- Replace line 906 (`maxTokens := gollm.GetMaxOutputTokens(...)`) with:
  ```go
  inTok := approxTokens(ctx, prompt)                 // rune/4 over the assembled prompt
  maxTokens := budgetedMaxOutputTokens(window, inTok, effOut, analysisMinOutputTokens())
  ```
  and add a `Debug` log (window / input / max_tokens / effOut) so operators can see the cap decision, mirroring the existing "rendered prompt sizing" debug.

### 4.3 Apply the output budget to the recommendation phase
**File:** `services/agent/internal/discovery/orchestrator.go:1324`

Same root cause, same one‑line fix (recommendations assemble a large insights‑JSON prompt and call with the fixed cap). Replace the fixed `maxTokens` with `budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), effOut, analysisMinOutputTokens())`. The recommendation phase has no picker (Layer B is analysis‑only); Layer A + Layer C protect it. Scoped, not gold‑plated: exploration (`exploration.go:117`) and sql‑fix (`sql_fixer.go:86`) have small bounded inputs and are covered by Layer C alone — no proactive budget added there (Rule 8).

### 4.4 New: adaptive context‑overflow retry
**File:** `services/agent/internal/ai/context_overflow.go` (new)

```go
// Markers that identify a context-length 400 across providers.
var contextOverflowMarkers = []string{
    "maximum context length",        // OpenAI + Bedrock/GLM ValidationException
    "context_length_exceeded",       // OpenAI error code
    "reduce the length of the input prompt or the number of requested output", // Bedrock tail
}

// contextOverflowRetryMarginPct is headroom kept when recomputing max_tokens from the
// model's stated numbers — covers tokenizer disagreement between our estimate and theirs.
const contextOverflowRetryMarginPct = 2
const contextOverflowMinRetryTokens = 512

func isContextLengthError(err error) bool { /* lowercased substring scan of err.Error() */ }

// parseContextLengthError extracts (contextWindow, inputTokens) from the two phrasings:
//   Bedrock/GLM: "...maximum context length is 202752 tokens. However, you requested 64000
//                 output tokens and your prompt contains at least 138753 input tokens..."
//   OpenAI:      "...maximum context length is 8192 tokens. However, you requested 9000
//                 tokens (5000 in the messages, 4000 in the completion)..."
// Returns ok=false when neither shape matches.
func parseContextLengthError(msg string) (window, input int, ok bool) { /* regexes */ }

// reducedMaxTokensForContextOverflow returns a smaller max_tokens to retry with, or ok=false
// when this isn't an overflow error, or when output-reduction alone cannot help (input ≥ window).
func reducedMaxTokensForContextOverflow(currentMax int, err error) (int, bool) {
    if !isContextLengthError(err) { return 0, false }
    if window, input, ok := parseContextLengthError(err.Error()); ok {
        margin := window * contextOverflowRetryMarginPct / 100
        nm := window - input - margin
        if nm < contextOverflowMinRetryTokens || nm >= currentMax { return 0, false }
        return nm, true
    }
    // Recognised overflow but numbers unparseable → single blind halving.
    nm := currentMax / 2
    if nm < contextOverflowMinRetryTokens { return 0, false }
    return nm, true
}
```

**File:** `services/agent/internal/ai/client.go` — in `createMessage`, after `chatWithRetry` returns an error, attempt exactly one adapted re‑issue before the error/observability path:

```go
resp, err := chatWithRetry(ctx, c.provider, req)
if err != nil {
    if nm, ok := reducedMaxTokensForContextOverflow(req.MaxTokens, err); ok {
        logger.WithFields(logger.Fields{
            "model": req.Model, "old_max_tokens": req.MaxTokens, "new_max_tokens": nm,
        }).Warn("LLM context-length overflow; retrying once with reduced max_tokens")
        req.MaxTokens = nm
        resp, err = chatWithRetry(ctx, c.provider, req)
    }
}
```
`req` is a local value copy, so mutation is safe. The existing `emitLLMUsage` / debug‑logger block then runs against the **final** `resp`/`err` (small refactor so usage is emitted once, for the outcome that actually returned). This is deterministic (not transient), bounded to one extra call, and terminal 400s that aren't overflow are unaffected.

### 4.5 Thread `project.LLM.Config` into the orchestrator (Layer D)
- `services/agent/internal/discovery/orchestrator.go`: add field `llmConfig gollm.ProviderConfig` to `Orchestrator` and `LLMConfig gollm.ProviderConfig` to `OrchestratorOptions`; set it in `NewOrchestrator`.
- `services/agent/agentserver/agentserver.go:~901`: pass `LLMConfig: project.LLM.Config` (already in scope — it builds the provider config a few lines up at :450). This lets `GetEffectiveInputWindow` honor an operator `max_input_tokens` override and `MaxOutputOverride` honor `max_output_tokens`.

### 4.6 GLM / uncatalogued‑model window (Layer D, config‑first)
- **Primary (no code guess):** document that for an uncatalogued large‑context model (GLM‑5), the operator sets **`max_input_tokens=202752`** (and, if its output cap differs from 64K, `max_output_tokens`) in the project's LLM config — the existing fields from `ContextWindowConfigFields()` (`registry.go:852`). With that set, Layer B stops over‑trimming and Layer A budgets against the true window.
- **Optional catalog entry:** a Bedrock `ModelEntry` for GLM‑5 (`MaxInputTokens: 202752`, its real output cap, `Wire: WireOpenAICompat`, alias list) is the clean long‑term fix — **but only with a verified Bedrock `ModelId`**. I don't have a confirmed AWS model identifier for "GLM‑5" (it appears to reach Bedrock via the openai‑compat wire, possibly a Marketplace/proxy deployment), and hard‑coding a guessed ID/alias that never matches (or a wrong window) is worse than none (Rule 2). **Open question for @abacigil below** — if you give me the exact model string the project uses, I'll add the catalog entry in the build step.

## 5. Phases (build‑step order)

1. `analysis_budget.go` + `analysis_budget_test.go` — pure helpers, TDD the acceptance‑criteria case first.
2. `context_overflow.go` + `context_overflow_test.go` — parser + reducer, both phrasings + unparseable + input‑too‑big.
3. `client.go` adaptive‑retry hook + a `createMessage`‑level test (fake provider: overflow‑400 then success on reduced retry). Refactor `emitLLMUsage` to fire once on the final outcome.
4. `orchestrator.go` + `agentserver.go` wiring: `llmConfig` field, per‑loop `window`/`effOut`/`picker.BudgetTokens`, per‑area output budget, recommendation output budget.
5. Docs (§7) + `CHANGELOG.md`.
6. Local gates: `make build`, `make test-go`, `make lint-go` (after `export PATH=$PATH:$(go env GOPATH)/bin`). No UI changes → no `test-ui`/`lint-ui`.
7. `make test-integration` for the agent module (testcontainers) to confirm nothing in the discovery wiring regressed.

## 6. Data / schema / API / UI impact

- **None to persisted schemas, Mongo docs, REST contracts, or the dashboard.** All changes are internal agent budgeting + one agent‑side LLM‑retry path. `AnalysisStep.TokensIn/Out` telemetry shape is unchanged.
- New **env var** `ANALYSIS_MIN_OUTPUT_TOKENS` (agent). New **operator‑facing guidance** for the existing `max_input_tokens` / `max_output_tokens` LLM config fields (no new fields — they already exist).
- No enterprise‑overlay files touched.

## 7. Docs to update (Rule 4)

- `docs/reference/configuration.md`:
  - Agent → "LLM Behavior": add `ANALYSIS_MIN_OUTPUT_TOKENS` (default 8192) row.
  - "Analysis Phase Compaction Tunables": update the `AnalysisQueryResultsBudgetTokens` row/"when to tune" note to say it's now a **ceiling** that is further capped to `context − output − system − margin` at run time.
  - Note that the analysis + recommendation phases budget `max_tokens` against the model window and adaptively retry a context‑length 400.
- `docs/architecture/agent-analysis-compaction.md`: "Per‑area token budget" section — document the window‑coupled budget + the output cap + the adaptive retry.
- `docs/concepts/providers.md` (and/or the LLM config guide): document `max_input_tokens` / `max_output_tokens` as the escape hatch for uncatalogued large‑context models (GLM‑5 example → `202752`).
- `CHANGELOG.md` `[Unreleased] → Fixed`: one entry describing the overflow fix (budget‑against‑window + adaptive retry), referencing the #338 regression.

## 8. Test strategy (Rule 9 — failure & edge cases, not just happy path)

**`analysis_budget_test.go`** (table‑driven):
- **Acceptance‑criteria case:** `window=202752, input=138753, effOut=64000` → output ≤ `202752 − 138753 − margin` and `> 0`. Assert `input + out ≤ window`.
- Small window forcing the **floor**: `window=131072, input=138753` (input > window) → returns `analysisMinOutputTokens()` (floor), never negative.
- Large headroom: tiny input → returns `effOut` exactly (not more).
- Operator output override lowers `effOut` (via `ClampMaxTokens`) → output capped to the override.
- `analysisPickerBudgetTokens`: 128K window → below the 200K default (protective); 1M window → stays at the 200K default; never returns ≤0.
- Zero/negative window guards (NewBudget clamps → floor).

**`context_overflow_test.go`:**
- Parse the **Bedrock/GLM** ValidationException (the issue's verbatim string) → `(202752, 138753)`.
- Parse the **OpenAI** "N tokens (X in the messages, Y in the completion)" phrasing.
- Unparseable‑but‑recognised overflow → blind‑halve branch.
- Non‑overflow 400 (e.g. `AccessDenied`) → `ok=false` (untouched).
- Input ≥ window (output reduction can't help) → `ok=false` (no infinite/pointless retry).
- Computed `nm ≥ currentMax` → `ok=false` (never a no‑op or larger retry).

**`client.go` retry test** (reuse the existing fake‑provider pattern in `services/agent/internal/ai/*_test.go`): a provider that returns the Bedrock overflow 400 on call 1 and a valid response on call 2 → `createMessage` returns the success, having re‑issued with the reduced `max_tokens`; assert the second request's `MaxTokens` equals the parsed target and that usage is emitted once. A provider that returns a **non‑overflow** 400 → no second call.

**Existing `analysis_step_picker_test.go`** already exercises `BudgetTokens` trimming; no change needed beyond confirming the new wiring passes a positive budget.

**Integration:** `make test-integration` (agent testcontainers) to confirm the discovery orchestrator still wires and runs end‑to‑end. (A full live GLM‑5 reproduction needs Bedrock GLM access; I'll note in the PR whether a live rerun was possible or whether verification rests on the unit + integration suite plus the parsed real error string.)

## 9. Risks & mitigations

- **Estimator drift (rune/4 undercounts dense JSON).** Mitigated by the 15% approx‑tier margin inside `NewBudget` **and** Layer C (which uses the model's exact stated numbers). Worst case: one extra round‑trip.
- **Over‑trimming input on uncatalogued large‑context models.** Layer B against a guessed 131K window trims GLM‑5's input more than necessary (safe, lower quality). Mitigated by Layer D's `max_input_tokens` override / catalog entry. Called out in docs.
- **Provider error‑string coupling.** Layer C parses free‑text error messages. Mitigated by matching multiple stable markers + graceful `ok=false` fallthrough (never worse than today's terminal failure), and by unit tests pinned to the real strings. A future typed‑error refactor in `gollm` would localize to this one file (same rationale as the existing `retry.go` substring matching).
- **Double retry cost.** Layer C composes with `chatWithRetry`'s transient loop, but the overflow branch fires only on a deterministic 400 that `chatWithRetry` returns immediately (no backoff), and is bounded to a single adapted re‑issue.

## 10. Alternatives considered

- **Only add the adaptive retry (skip proactive budget).** Simpler, but every overflow costs a wasted round‑trip + backoff and the retry can't help when input alone exceeds a small window — Layer B is needed for 128K‑class models. Rejected as insufficient.
- **Trim the input on retry (re‑run the picker) instead of reducing output.** More complex, and unnecessary: the model's error states exact numbers, so a single output‑reduction retry is deterministic. Input trimming is already handled proactively by Layer B. Noted, not implemented.
- **Put the proactive budget in `ai.Client` for all callers.** Broader blast radius (exploration/sql‑fix don't need it) and violates Rule 8. Kept proactive budgeting at the two large‑prompt call sites; used Layer C as the universal net.
- **Hard‑code a GLM‑5 catalog entry now.** Rejected without a verified Bedrock model ID (guessed alias never matches / wrong window is worse than none). Deferred to the open question.
- **Use the provider's exact `TokenCounter` per area.** A network round‑trip per area (dozens per run) for marginal accuracy over rune/4; /ask does one exact check for one prompt. Rejected on latency; approx + margin + Layer C is the right trade for discovery.

## 11. Acceptance‑criteria mapping

| Criterion | Covered by |
|---|---|
| ~138K‑input analysis on a 202,752 model **succeeds** (output auto‑reduced, no 400) | Layer A (first‑call fit) + Layer C (net) |
| Context‑overflow 400 triggers a reduced‑budget **retry**, not an area failure | Layer C |
| No area fails purely because `input + fixed_output > context` | Layer A + Layer B + Layer C |
| Unit test: `context=202752, input=138753, default_output=64000` → output ≤ `202752 − 138753 − margin` | `analysis_budget_test.go` |

## 12. Open question for @abacigil (non‑blocking)

For **fix #3 (catalog GLM‑5)** I need the exact model string the failing project uses (the Bedrock `ModelId` / the value in `project.LLM.Model`) and its real output cap. Without a verified ID I'll ship the robust path (budget‑against‑window + adaptive retry + the `max_input_tokens=202752` override, documented) and add the catalog entry once you confirm the ID. If you'd rather I catalog it now with a best‑guess alias set, say so and I will. I'll proceed with the config‑first approach in the meantime so the run stops failing regardless.

---

This is a **PLAN for review** — implementation follows after approval.

Closes #347

— Co-coded with Jale 🤖
