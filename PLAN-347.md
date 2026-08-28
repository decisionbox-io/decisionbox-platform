# Plan — Discovery analysis fails with context-length 400: budget `max_tokens` against the model's real context window, without depending on the catalog (#347)

## 1. Problem

A discovery analysis area fails (run → **Partial**) with a hard 400:

```
channelperformance: bedrock/openai-compat: InvokeModel failed: operation error Bedrock Runtime:
InvokeModel, StatusCode: 400, ValidationException: "This model's maximum context length is 202752
tokens. However, you requested 64000 output tokens and your prompt contains at least 138753 input
tokens, for a total of at least 202753 tokens. Please reduce the length of the input prompt or the
number of requested output tokens."
```

`138,753 input + 64,000 output = 202,753` → **one token over** GLM‑5's 202,752 window. The area is lost, the run degrades to Partial, nothing retries.

## 2. Root cause & the real design constraint

**Mechanical cause (confirmed in code):**
- Fixed output cap: `orchestrator.go:906` (analysis) and `orchestrator.go:1324` (recommendations) request `gollm.GetMaxOutputTokens(...)`, which for uncatalogued GLM‑5 resolves to Bedrock's `DefaultMaxOutputTokens: 64000` (the #338 value).
- Unrelated input budget: the picker trims `{{QUERY_RESULTS}}` only against a fixed `AnalysisQueryResultsBudgetTokens = 200_000` (`analysis_step_picker.go:40`).
- No coupling: `llm.Budget` (`libs/go-common/llm/budget.go`) enforces `input + output ≤ context` but is wired into **/ask only**.
- No overflow retry: `internal/ai/retry.go` treats a 400 `ValidationException` as terminal.
- Unknown window: GLM‑5 is uncatalogued; `GetMaxInputTokens("bedrock","GLM-5")` falls back to the global `DefaultMaxInputTokens = 131072`, so DecisionBox never knew the real 202,752 window.

**The design constraint (from @abacigil): the catalog will never cover customer models.** Customers point DecisionBox at arbitrary models — often through gateways (LiteLLM) or local runtimes (Ollama). We already ask for **`max_input_tokens` / `max_output_tokens`** at LLM‑config time (`ContextWindowConfigFields`, `registry.go:852`), but:
- those are **static ceilings**, and a 400 depends on **this call's actual input** — a customer can set both correctly and still overflow when one area's input is large; and
- asking a human to type a model's context window is a bad default — most won't know it.

So the fix must **not depend on cataloging GLM‑5**. It has four pillars:

| Pillar | Idea | Guarantees |
|---|---|---|
| **1. Enforce** | Budget output against the **measured** input at call time; couple the input budget to the window; adaptively retry a context‑overflow 400. | Works for **any** model with zero catalog / zero config. Stops the failures. |
| **2. Auto‑detect** | Read the model's real window/output from the provider where it's exposed (LiteLLM `/model/info`, Ollama `/api/show`, OpenAI‑compat `max_model_len`). | Removes the guess for gateway/local models without user typing. |
| **3. Prefill** | Surface the detected window/output in the live‑models API and **prefill** the `max_input_tokens` / `max_output_tokens` inputs in the dashboard (user can override). | Makes the config fields self‑populating instead of "type a number you don't know". |
| **4. Self‑calibrate** | The overflow error states the model's **true** window; learn it, re‑budget the rest of the run, and **persist** it per project+model so we pay the 400 at most once. | The system converges to correct budgeting even for a model no one configured. |

A single **resolution order** ties them together (used by both the agent budget and the dashboard prefill):

```
effective input window  = operator max_input_tokens override
                        → self-calibrated (persisted) value
                        → live auto-detected value
                        → catalog MaxInputTokensFor
                        → global DefaultMaxInputTokens (128K)
effective output cap    = operator max_output_tokens override
                        → live auto-detected value
                        → catalog MaxOutputTokensFor
                        → global DefaultMaxOutputTokens (64K)
```

Catalog is now just **one rung**, not a prerequisite. No GLM‑5 catalog entry is added.

---

## 3. Pillar 1 — Enforce at call time (catalog‑independent core)

### 3.1 New: `services/agent/internal/discovery/analysis_budget.go`
Pure, unit‑testable helpers delegating the arithmetic to `llm.NewBudget` (the "reuse `llm.Budget`" the issue asks for):

```go
const analysisReservedSystemTokens = 512          // chat-template / scaffolding headroom
const defaultAnalysisMinOutputTokens = 8192       // floor; env-overridable (Rule 2)
const analysisMinOutputTokensEnv = "ANALYSIS_MIN_OUTPUT_TOKENS"

// out = clamp(window − input − system − margin, floor, effectiveOutputCap)
func budgetedMaxOutputTokens(window, inputTokens, effectiveOutputCap, floor int) int {
    avail := gollm.NewBudget(window, 0, analysisReservedSystemTokens, false).Available()
    out := avail - inputTokens
    if out > effectiveOutputCap { out = effectiveOutputCap }
    if out < floor { out = floor }
    return out
}

// picker budget = min(default, window − output − system − margin)  (only ever lowers it)
func analysisPickerBudgetTokens(defaultBudget, window, effectiveOutputCap int) int {
    avail := gollm.NewBudget(window, effectiveOutputCap, analysisReservedSystemTokens, false).Available()
    if avail > 0 && avail < defaultBudget { return avail }
    return defaultBudget
}
```
Input estimate: `gollm.ApproximateCounter{}` (rune/4) over the assembled prompt — cheap, local, consistent with the picker's char/4 estimator; fall back to `utf8.RuneCountInString/4` on a ctx error. The 15% approx‑tier margin in `NewBudget` + Pillar‑1 retry absorb undercount on dense JSON.

### 3.2 Wire into analysis + recommendation (`orchestrator.go`)
- Before the area loop (~line 818): resolve `window` and `effOut` (see §7 resolution), then `picker.BudgetTokens = analysisPickerBudgetTokens(AnalysisQueryResultsBudgetTokens, window, effOut)` (the field is already honored in `Pick`; today unset → 200K).
- Replace line 906 with `maxTokens := budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), effOut, analysisMinOutputTokens())` + a Debug log of the decision.
- Same one‑line output‑budget swap at line 1324 (recommendations). Exploration (`exploration.go:117`) and sql‑fix (`sql_fixer.go:86`) have small bounded inputs → covered by the retry alone, not proactively budgeted (Rule 8).

### 3.3 New: adaptive context‑overflow retry — `services/agent/internal/ai/context_overflow.go`
```go
var contextOverflowMarkers = []string{
    "maximum context length", "context_length_exceeded",
    "reduce the length of the input prompt or the number of requested output",
}
const contextOverflowRetryMarginPct = 2
const contextOverflowMinRetryTokens = 512

func isContextLengthError(err error) bool
// Bedrock/GLM: "...maximum context length is 202752 tokens. However, you requested 64000
//   output tokens and your prompt contains at least 138753 input tokens..."
// OpenAI:      "...maximum context length is 8192 tokens. However, you requested 9000 tokens
//   (5000 in the messages, 4000 in the completion)..."
func parseContextLengthError(msg string) (window, input int, ok bool)

func reducedMaxTokensForContextOverflow(currentMax int, err error) (newMax int, ok bool)
// window,input parsed → newMax = window − input − margin; ok=false if newMax<min or ≥ currentMax
// recognised-but-unparseable → single blind halving
```
Hook in `client.go createMessage`: after `chatWithRetry` returns an error, attempt **one** adapted re‑issue (`req.MaxTokens = newMax; chatWithRetry(...)`), then run the existing usage/debug emit against the final outcome. `req` is a local value copy — safe to mutate. Deterministic, bounded to one extra call, non‑overflow 400s untouched.

The parsed `(window)` is also surfaced upward for Pillar 4 via an optional callback on the `ai.Client`:
```go
// SetContextWindowObserver registers a callback invoked with (model, learnedWindow) whenever an
// overflow 400 reveals the model's true window. Nil by default (tests, non-discovery callers).
func (c *Client) SetContextWindowObserver(fn func(model string, window int))
```

---

## 4. Pillar 2 — Auto‑detect the real window/output from the provider

### 4.1 New capability — `libs/go-common/llm/provider.go`
```go
// ModelCapabilities is what a provider can self-report about a model from its upstream metadata
// endpoint. Zero fields mean "unknown" — callers fall through the resolution order.
type ModelCapabilities struct {
    MaxInputTokens  int
    MaxOutputTokens int
}

// ModelInfoResolver is an optional capability: providers that expose a per-model metadata endpoint
// implement it. Read-only, must not consume tokens. Return (zero, nil) when the upstream doesn't
// expose the numbers (callers fall through), and an error only on a genuine call failure.
type ModelInfoResolver interface {
    ResolveModelInfo(ctx context.Context, model string) (ModelCapabilities, error)
}
```
Also extend `RemoteModel` with `MaxInputTokens int` / `MaxOutputTokens int` (0 = unknown) so `ListModels` can carry the detected numbers to the dashboard (Pillar 3).

### 4.2 LiteLLM — `providers/llm/litellm/`
- Implement `ResolveModelInfo` via `GET /model/info` (LiteLLM's model‑cost map exposes `model_info.max_input_tokens` / `max_output_tokens` / `max_tokens` for ~thousands of models). Runs through the existing custom‑TLS transport + `setAuth`.
- In `list_models.go`, enrich each `RemoteModel` with those numbers when `/model/info` is reachable (best‑effort; a failure leaves them 0 and never blocks listing — same contract as today).

### 4.3 Ollama — `providers/llm/ollama/`
- Extend the `ollamaClient` interface (`client.go`) with `Show(ctx, *ShowRequest) (*ShowResponse, error)` (the official `ollamaapi` client already provides it; update the mock).
- Implement `ResolveModelInfo` reading `ShowResponse.ModelInfo["<arch>.context_length"]` → `MaxInputTokens`. This makes the real window available even when the operator never set `num_ctx`. (`num_ctx` still wins where set, via `ollamaEffectiveInputWindow`.)
- Enrich `RemoteModel` rows in `list_models.go` likewise.

### 4.4 OpenAI‑compat gateways (vLLM, etc.)
- Where `GET /v1/models` returns `max_model_len` (vLLM) parse it into `RemoteModel.MaxInputTokens` in the `openai` provider's list path. Best‑effort; absent field → 0. (No new endpoint; just read a field we currently drop.)

---

## 5. Pillar 3 — Prefill the config inputs in the dashboard

### 5.1 API surface — `services/api/internal/handler/providers.go`
`liveModelsResponse` already embeds `gollm.ModelInfo` (which serializes `max_input_tokens` / `max_output_tokens`). `writeLiveModelsResponse` must **carry the detected numbers from live rows** into the merged `ModelInfo` (today it only copies `DisplayName` / `Lifecycle` from live rows). So a live‑only or "both" row for an uncatalogued model surfaces its detected window/output.

### 5.2 Dashboard prefill — `ui/dashboard/`
- In the LLM model‑config form (the component that renders the model combobox + the `max_input_tokens` / `max_output_tokens` fields from `ContextWindowConfigFields`), when the user selects a model whose live row carries detected numbers, **prefill** the two inputs — only when the field is empty / not user‑edited, and always leave it editable (the values are hints, the customer can override).
- Types already exist in `src/lib/api.ts` (`ModelInfo.max_input_tokens` / `max_output_tokens`); wire the prefill in the selection handler.
- **Enterprise overlay check (per repo guide):** confirm whether this LLM‑config component is overlaid in `decisionbox-enterprise/ui/src/`; if so, sync the overlay copy. (Expected community‑only, but must be verified before merge.)

---

## 6. Pillar 4 — Self‑calibrate: learn the true window from the 400 and remember it

### 6.1 In‑run (memory)
The orchestrator registers a `ContextWindowObserver` (§3.3) on its `ai.Client`. When an overflow 400 reveals the true window, it updates the in‑run `window`/`picker.BudgetTokens` so **remaining areas in the same run** budget correctly — the reported failure was several areas in one run, so this alone converts a Partial into a full run after the first area's single retry.

### 6.2 Cross‑run (persist) — new minimal store
- New collection `llm_model_windows` + `database.LLMModelWindowRepo` (agent side): documents keyed `{project_id, provider, model}` → `{input_window, output_cap, source, updated_at}`. Small, self‑contained, tenant‑plane only (the agent already writes tenant Mongo — no two‑plane violation).
- The observer upserts the learned window here.
- The run‑start resolution (§7) reads it as rung 2 of the order, so a later run for the same project+model budgets correctly **before** the first call — no repeat 400.
- No mutation of user `project.LLM.Config` (avoids racing user edits / surprising overwrites); the persisted value is a system‑learned hint, the config override still wins.

---

## 7. Resolution wiring (single source of truth)

New context‑aware resolver, agent side, run once at run start (agentserver, where the provider instance + `project.LLM.Config` + Mongo are all in scope):

```go
// window, outCap resolved via the order in §2, consulting:
//  - project.LLM.Config (max_input_tokens / max_output_tokens overrides)
//  - LLMModelWindowRepo (persisted self-calibration)
//  - provider.(gollm.ModelInfoResolver) live lookup (best-effort, short timeout)
//  - gollm.GetEffectiveInputWindow / GetMaxOutputTokens (catalog + default)
```
Pass the resolved `window` + `outCap` into `OrchestratorOptions` (new fields `LLMInputWindow int`, `LLMOutputCap int`) so the orchestrator budgets without provider‑capability plumbing. Also thread `LLMConfig gollm.ProviderConfig` (from `project.LLM.Config`) so `MaxOutputOverride`/overrides remain available. Wire at `agentserver.go:~901`.

Files: `orchestrator.go` (+3 fields, budget calls, observer), `agentserver.go` (resolve + pass), plus the resolver helper (new small file, e.g. `agentserver/llm_window.go` or a `gollm.ResolveEffectiveWindow(ctx, provider, model, cfg, persisted)` helper in go‑common — leaning to the go‑common helper so /ask can reuse it later).

---

## 8. Phases (build order)

1. **Enforce core** — `analysis_budget.go` (+test), `context_overflow.go` (+test), `client.go` hook + observer (+test), `orchestrator.go`/`agentserver.go` budget wiring (`LLMConfig`, `window`, `outCap`, picker budget, per‑area + recommendation output budget). *Ships the fix on its own — every model, no catalog.*
2. **Auto‑detect** — `ModelInfoResolver` + `RemoteModel` fields (go‑common); LiteLLM `/model/info`; Ollama `/api/show` (+ mock); OpenAI‑compat `max_model_len`. Tests per provider.
3. **Resolution wiring** — the run‑start resolver consuming §7 order; live lookup best‑effort with a short timeout.
4. **Self‑calibrate** — `LLMModelWindowRepo` + collection; observer upsert; in‑run re‑budget; resolver reads persisted value.
5. **Prefill** — API merge carries detected numbers; dashboard prefill; enterprise‑overlay sync check; `make test-ui` / `make lint-ui`.
6. **Docs + CHANGELOG** (§10).
7. **Gates:** `make build`, `make test-go`, `make lint-go` (after `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`/`make lint-ui` (dashboard touched), `make test-integration` (agent testcontainers).

## 9. Test strategy (Rule 9 — failure & edge cases)

- **`analysis_budget_test.go`:** the acceptance case (`window=202752, input=138753, effOut=64000` → output ≤ `202752−138753−margin`, `input+out ≤ window`); input>window → floor (never negative); tiny input → exactly `effOut`; output override lowers `effOut`; `analysisPickerBudgetTokens` lowers on 128K, stays 200K on 1M, never ≤0.
- **`context_overflow_test.go`:** parse the verbatim Bedrock/GLM string → `(202752,138753)`; parse the OpenAI "(X in the messages, Y in the completion)" shape; recognised‑but‑unparseable → halve; non‑overflow 400 → `ok=false`; input≥window → `ok=false`; `newMax≥currentMax` → `ok=false`.
- **`client.go` retry test** (existing fake‑provider pattern): overflow‑400 then success on the reduced retry → returns success, second request's `MaxTokens` == parsed target, usage emitted once, observer fired with the learned window; non‑overflow 400 → no second call, observer not fired.
- **Provider auto‑detect tests:** LiteLLM `/model/info` parse (httptest); Ollama `/api/show` `context_length` parse (mock `Show`); OpenAI‑compat `max_model_len` parse; each: missing/garbled field → `(0,nil)` (graceful).
- **Resolver test:** order precedence (override > persisted > live > catalog > default), incl. live‑error fallthrough.
- **Persistence test:** `LLMModelWindowRepo` upsert/get round‑trip (Mongo testcontainer via the agent integration suite).
- **API merge test:** live row with detected numbers → surfaced in `liveModelsResponse.ModelInfo`.
- **Dashboard test (Jest):** selecting a model with detected numbers prefills the two inputs; user edit is not overwritten.
- **Integration:** `make test-integration` confirms discovery wiring still runs end‑to‑end. A full live GLM‑5 reproduction needs Bedrock GLM access; the PR will state whether a live rerun was possible or whether verification rests on the unit + integration suites + the parsed real error string.

## 10. Docs (Rule 4)

- `docs/reference/configuration.md`: add `ANALYSIS_MIN_OUTPUT_TOKENS`; update the `AnalysisQueryResultsBudgetTokens` note (now a ceiling further capped to `context − output − system − margin`); note analysis/recommendation budget `max_tokens` against the window + adaptively retry a context‑length 400.
- `docs/architecture/agent-analysis-compaction.md`: window‑coupled budget + output cap + adaptive retry.
- `docs/concepts/providers.md` + the LLM‑config guide: **auto‑detect** (LiteLLM `/model/info`, Ollama `/api/show`, vLLM `max_model_len`), **prefill**, and the `max_input_tokens`/`max_output_tokens` overrides as the final escape hatch; document the self‑calibration behavior.
- `docs/reference/api.md`: live‑models response now carries `max_input_tokens`/`max_output_tokens` for detected models.
- `CHANGELOG.md` `[Unreleased] → Fixed` (+ `Added` for auto‑detect/prefill): describe the overflow fix + the #338 regression.

## 11. Data / schema / API / UI impact

- **New Mongo collection** `llm_model_windows` (agent‑written, tenant plane). No change to existing docs/collections.
- **API:** live‑models response gains populated `max_input_tokens`/`max_output_tokens` on detected rows (additive, backward‑compatible).
- **UI:** prefill logic in the LLM‑config form (additive; fields already exist). Enterprise‑overlay sync verified.
- **Env:** new `ANALYSIS_MIN_OUTPUT_TOKENS` (agent).
- No enterprise‑feature mentions in community code (Rule 11).

## 12. Risks & mitigations

- **Estimator drift (rune/4 undercounts dense JSON).** 15% margin in `NewBudget` + the adaptive retry (uses the model's exact numbers). Worst case: one extra round‑trip.
- **Provider error‑string / metadata coupling.** Overflow parsing matches multiple stable markers with graceful fallthrough (never worse than today's terminal failure); auto‑detect is best‑effort and never blocks. Both pinned by tests to real strings/payloads.
- **Live lookup latency at run start.** Short timeout, best‑effort, cached (persisted); on failure we fall through to catalog/default and still self‑calibrate from the first 400.
- **PR size.** Four pillars across agent + go‑common + providers + API + dashboard is a large PR. Structured so Phase 1 alone resolves the reported failure; if the review loop shows it's unwieldy we can split at the phase boundaries — but per @abacigil's direction it's planned as one deliverable.
- **Persisted value going stale** (operator swaps the model behind the same gateway alias). Keyed by model string; the override always wins and a fresh 400 re‑calibrates.

## 13. Alternatives considered

- **Catalog GLM‑5 (original fix #3).** Dropped per @abacigil — the catalog can't track customer models; auto‑detect + self‑calibrate + override generalize instead.
- **Adaptive retry only.** Insufficient: wastes a round‑trip per area and can't help a small‑window model whose input alone overflows — the window‑coupled input budget (Pillar 1) is needed.
- **Trim input on retry (re‑run the picker).** Unnecessary: the model states exact numbers, so a single output‑reduction retry is deterministic; input is bounded proactively.
- **Persist detected window into `project.LLM.Config`.** Rejected: a background agent shouldn't overwrite user config (races, cloud two‑plane surprises). A dedicated system‑owned store keeps the override authoritative.
- **Exact `TokenCounter` per area.** A network round‑trip per area for marginal accuracy over rune/4; rejected on latency (approx + margin + retry is the right trade for discovery).

## 14. Acceptance‑criteria mapping

| Criterion | Covered by |
|---|---|
| ~138K‑input analysis on a 202,752 model **succeeds** (output auto‑reduced, no 400) | Pillar 1 (first‑call fit) + retry |
| Context‑overflow 400 triggers a reduced‑budget **retry**, not an area failure | Pillar 1 retry |
| No area fails purely because `input + fixed_output > context` | Pillar 1 (budget + coupled input budget + retry) |
| Unit test: `context=202752, input=138753, default_output=64000` → output ≤ `202752 − 138753 − margin` | `analysis_budget_test.go` |
| Works for **uncatalogued customer models** (the real constraint) | Pillars 2–4 (auto‑detect, prefill, self‑calibrate) |

---

This is a **PLAN for review** — implementation follows after approval.

Closes #347

— Co-coded with Jale 🤖
