# Plan — Harden the discovery exploration action parser (#341)

## 1. Problem

Discovery **bricks at step 1** when an LLM does not emit one perfectly clean
JSON action object. The real customer failure on the DecisionBox AI gateway:

```
discovery run failed: exploration failed: step 1: unable to parse LLM response after 4 attempts:
action JSON has no query, lookup_schema, search_tables, done flag, or recognized action (got action="")
```

Reasoning / open models (Qwen3, DeepSeek-R1, GPT-OSS, and quantised open models
behind LiteLLM→Ollama) routinely wrap their answer in a `<think>…</think>`
block, leading prose, markdown fences, or emit a "planning" JSON object
*before* the real action. The exploration parser then locks onto the wrong
object, the action decodes to `action=""`, all `maxParseRetries` exhaust, and
the run dies. The current workaround is to force a strict model (the AI-gateway
`analysis` alias was pinned to Opus 4.8), which is exactly the lock-in this
issue exists to remove.

This is the same failure class as pack-gen **enterprise#231** (structured-output
adherence) but on the **discovery path**, which #231 never touched.

## 2. Root cause (current code, `services/agent/internal/ai/exploration.go`)

The parse pipeline is `runStepWithRetry` (`:582`) → `parseAction`/`ParseAction`
(`:674`) → `extractJSON` (`:850`) → `json.Unmarshal` (`:681`) → action-key
validation switch (`:693–719`, empty-action error at `:718`).

Three concrete defects:

1. **`extractJSON` never strips `<think>…</think>`.** Reasoning text is fed
   straight into `collectJSONCandidates` (`:869`). Any brace-y prose inside the
   think block becomes a JSON "candidate", and — worse — `findBalancedJSONObjects`
   (`:908`) **aborts the entire scan on the first unbalanced `{`** (`break` at
   `:946`). A stray `{` in reasoning prose (e.g. `the set {a, b`) stops scanning
   before the real action object further down. (This exact abort-on-unbalanced
   weakness was flagged in the PR #176 Copilot review and left unaddressed.)

2. **`jsonHasActionKey` (`:955`) only recognises `query` / `done` / `action`.**
   It does **not** recognise `lookup_schema`, `search_tables`, or the
   tool-use envelope `{"name": …, "input": …}` that `normaliseToolEnvelope`
   (`:766`) already knows how to dispatch. So when the real action is a
   schema-discovery action or a tool envelope, `extractJSON` treats it as
   *not* an action, and falls back to "the last balanced object overall"
   (`:860`) — which is frequently a trailing thinking/planning fragment →
   `action=""`. This is the direct cause of the customer error.

3. **The corrective retry (`:645–651`) re-asks blindly.** It sends the same
   generic "respond with one JSON object" nudge on every failure and never
   tells the model *what* was wrong (no JSON at all vs. malformed JSON vs.
   recognised-but-empty action), so a model that produced an empty action keeps
   producing empty actions until the budget is gone.

Two adjacent gaps the issue also calls out:

4. **The action is never constrained via provider-native structured output.**
   platform#340 (already merged, `ed6c147`) shipped exactly this capability —
   `ChatRequest.ResponseFormat`, `ProviderMeta.SupportsStructuredOutput`, and
   the Anthropic-wire forced-tool helpers — and it is **unused** on the
   discovery path. The exploration call (`:590`) goes through the plain
   `CreateMessage`.

5. **Reasoning starvation.** The exploration LLM call hardcodes
   `maxTokens = 4096` (`:594`). On a reasoning model a long `<think>` block can
   consume the entire budget and the action is truncated to empty before it is
   ever emitted. (This is also a Rule 2 hardcode.)

## 3. Proposed approach

Four layers, defence-in-depth. Layer C is the *preferred* fix where the
provider supports it; A + B + D are the always-on safety net for providers that
don't (`vertex-ai`, `azure-foundry`) or for output that slips through anyway.

### A. Tolerant extraction (`extractJSON` and friends)

- **Strip reasoning blocks first.** New `stripReasoningBlocks(text)` removes
  `<think>…</think>` and `<thinking>…</thinking>` pairs (case-insensitive,
  across newlines). An **unclosed** trailing `<think>` (truncated output) is
  stripped to end-of-string — its content is all reasoning, no action follows,
  so dropping it lets extraction return `""` cleanly and trip the repair retry
  rather than mis-selecting a half-JSON fragment.
- **Widen action-key recognition.** `jsonHasActionKey` gains `lookup_schema`,
  `search_tables`, and the tool-use envelope (`name` ∈
  {`query_data`,`lookup_schema`,`search_tables`,`complete`} **with** an `input`
  key). The existing lenient, *presence-based* contract is preserved — e.g.
  `{"query": ""}` still counts — so `ParseAction` still gets to emit its precise
  error and drive a targeted retry rather than `extractJSON` silently guessing.
- **Skip, don't abort, on an unbalanced brace.** `findBalancedJSONObjects`
  advances past a `{` that never balances and keeps scanning, so a valid action
  object after a garbled preamble is still found.
- **Selection order is unchanged: last recognised candidate wins**, with the
  last balanced object as the final fallback. This keeps the PR #176
  anti-preamble regression tests green (a leading planning object must never
  hijack the parse). See §9 for why we keep "last" over the issue's literal
  "first".
- Single-element JSON arrays (`[ {action} ]`) need no special case:
  `findBalancedJSONObjects` already digs the inner object out of the array. We
  add explicit corpus coverage to lock that in.

### B. Targeted repair retry (`runStepWithRetry`)

Replace the blind nudge with a reason-aware one that echoes the concrete parse
failure (`lastParseErr`) and states the exact requirement:

- lead with *why* the last turn failed (no JSON found / malformed JSON /
  recognised-but-empty action);
- give the full menu of valid single-object shapes (`query`, `done`,
  `lookup_schema`, `search_tables`);
- explicitly forbid the wrappers that caused the failure: "no `<think>` block,
  no prose, no markdown fences, no planning JSON before it."

The model's own offending output is already in the conversation
(`AddAssistantMessage`, `:621`), so the model sees its mistake next to the
correction.

### C. Schema-/tool-constrained action (preferred, provider-gated)

Use platform#340 to pin the action shape with provider-native structured
output:

- New `explorationActionSchema()` builds a JSON Schema (draft 2020-12) for the
  action: an `object` whose properties are the `ExplorationAction` JSON fields
  (`thinking`, `query`, `datasource_id`, `done`, `summary`, `lookup_schema`
  as `array<string>`, `search_tables`, `search_top_k`), typed, **non-strict**,
  no `required` list. Non-strict + no-required is deliberate: it forces a single
  well-typed JSON object (no prose, no fences, no array wrapper, no `<think>` in
  `Content`) while staying expressible on every wire — OpenAI non-strict
  `json_schema`, the Ollama `format` grammar, and the Anthropic forced-tool
  `input_schema` (see §9 for why not a strict `oneOf`).
- New `Client.SupportsStructuredOutput()` looks up
  `gollm.GetProviderMeta(c.providerName).SupportsStructuredOutput` (provider name
  is already set via `SetProvenance` at `agentserver.go:777`).
- New `Client.CreateMessageWithFormat(...)` sets `req.ResponseFormat`; the
  provider layer already honours it end-to-end (Ollama `format`
  `provider.go:316`; Claude/Bedrock forced-tool + `NormalizeStructuredToolResponse`
  `claude/provider.go:177,209`), folding the result back into `Content` so the
  agent parses uniformly. `runStepWithRetry` attaches the schema when
  `SupportsStructuredOutput()` is true and passes `nil` otherwise — a `nil`
  `ResponseFormat` produces a byte-identical request to today.
- The tolerant parser (A) still runs on the constrained output, so a provider
  that ignores or imperfectly honours the schema is still covered.

### D. Reasoning-starvation guard (token budget)

Replace the hardcoded `4096` at `:594` with the catalogued cap for the active
(provider, model), mirroring the SQL fixer's established pattern
(`sql_fixer.go:86`):

```go
maxTokens := gollm.GetMaxOutputTokens(e.client.ProviderName(), e.client.ModelName())
```

`GetMaxOutputTokens` resolves catalog → provider default → global default
(64000), so reasoning models get ample room after a `<think>` block, tighter
models get their true cap, and the Rule 2 hardcode is gone. It is a *ceiling*,
not a target, so normal short actions cost the same.

## 4. Exact files / areas to change

| File | Change |
|------|--------|
| `services/agent/internal/ai/exploration.go` | `stripReasoningBlocks` (new); `extractJSON` calls it; `jsonHasActionKey` widened + envelope recognition; `findBalancedJSONObjects` skip-not-abort; `runStepWithRetry` reason-aware nudge + `ResponseFormat` attach + token cap via `GetMaxOutputTokens`; `explorationActionSchema` (new). |
| `services/agent/internal/ai/client.go` | `SupportsStructuredOutput() bool` (new); `CreateMessageWithFormat(...)` (new) reusing the `CreateMessageWithTools` body with `req.ResponseFormat` set. |
| `services/agent/internal/ai/exploration_test.go` | Extend `TestExtractJSON_Comprehensive`, `TestJSONHasActionKey`, `TestFindBalancedJSONObjects`, `TestParseAction_StrictModernContract`; new fuzz-corpus + repair-retry + structured-output-gating + token-cap tests (see §6). |
| `CHANGELOG.md` | Entry under `[Unreleased] › Fixed`. |
| `docs/` | Update the reasoning/open-model support note if one exists (grep in the build step); at minimum the changelog. No new env vars or public config, so `docs/reference/configuration.md` is unaffected. |

No changes to warehouse/secret providers, the API service, the dashboard, Helm,
or Terraform. This is contained to the agent's discovery parser plus two small
`Client` helpers.

## 5. Phased implementation

1. **Extraction hardening (A).** `stripReasoningBlocks`, widen
   `jsonHasActionKey`, skip-not-abort in `findBalancedJSONObjects`; wire
   `stripReasoningBlocks` into `extractJSON`. Land the fuzz corpus in the same
   commit so the corpus is green before anything else builds on it.
2. **Targeted repair retry (B).** Rework the nudge in `runStepWithRetry` to be
   reason-aware; add recovery tests over the mock's `ResponseQueue`.
3. **Reasoning-starvation guard (D).** Swap `4096` → `GetMaxOutputTokens(...)`;
   assert the captured request carries the catalogued cap.
4. **Structured output (C).** Add the two `Client` helpers, the schema builder,
   and the gated attach in `runStepWithRetry`; assert `ResponseFormat` is set
   for a structured-capable provider and `nil` for one that isn't.
5. **Docs + changelog**, then the full local gate (`make build`, `make test-go`,
   `make lint-go`), then the Codex review loop and Copilot pass.

Ordering rationale: A + B + D fix the crash for *all* providers with zero new
dependencies; C then removes the strict-model lock-in for capable providers on
top of an already-correct baseline.

## 6. Test strategy (Rule 9 — failure + edge cases, not happy paths)

Unit tests (`services/agent/internal/ai`, no external deps):

- **Fuzz corpus over `extractJSON` + `ParseAction`** — every shape from the
  acceptance criteria must select the real action:
  - `<think>…</think>` block before the action (and a `<thinking>` variant);
  - **the action *inside* a `<think>` block** followed by trailing prose;
  - leading prose / markdown fences around the action;
  - a leading **planning JSON** (`{"plan": …}`) then a valid action;
  - a valid action then a **trailing** think/reflection fragment (the exact
    `action=""` customer shape) — must still pick the action;
  - schema-discovery actions as the *only* action (`{"search_tables": …}`,
    `{"lookup_schema": […]}`) with reasoning noise around them;
  - the tool-use envelope `{"name":"query_data","input":{"query":…}}` wrapped in
    prose;
  - single-element array wrapper `[{"query": …}]`;
  - an **unbalanced `{` in the preamble** followed by a valid action object;
  - genuine no-action inputs (prose only, thinking-only object, truncated
    unclosed `<think>`) must still return an error / `""` (no silent complete).
- **`jsonHasActionKey`** — new true cases (`lookup_schema`, `search_tables`,
  known envelope) and new false cases (`{"name":"not a tool"}`, envelope with an
  unknown name, envelope without `input`); all existing cases stay unchanged.
- **`findBalancedJSONObjects`** — new "skips unbalanced prefix, finds later
  object" case; existing cases unchanged (single trailing unbalanced still → nil).
- **Repair retry** — with the mock `ResponseQueue`: empty-action then valid
  recovers within budget and the nudge text contains the specific reason;
  all-attempts-fail still returns the hard error after `maxParseRetries+1`.
- **Structured output gating** — a mock provider whose registered meta has
  `SupportsStructuredOutput=true` ⇒ captured `req.ResponseFormat != nil` and the
  schema names the action fields; a provider without it ⇒ `req.ResponseFormat ==
  nil` (byte-identical to today).
- **Token cap** — captured `req.MaxTokens` equals `GetMaxOutputTokens(provider,
  model)`, not 4096.

No new integration surface is introduced (no new provider, endpoint, or DB
shape), so the existing `make test-integration` suite is the regression guard;
the customer scenario ("a discovery run against `qwen3-235b-a22b-instruct`
completes step 1") is reproduced deterministically by the fuzz corpus rather
than a live-model soak, which would be non-deterministic and key-gated.

## 7. Data / schema / API / UI impact

None. No Mongo document changes, no API request/response changes, no dashboard
changes, no new env var or config field. `ResponseFormat` is an in-process,
per-call LLM request field. The `StepCallback` / `ExplorationStep` shapes are
untouched.

## 8. Risks & mitigations

- **Selection-order regression.** Widening `jsonHasActionKey` changes which
  candidate is "recognised". Mitigation: keep last-recognised-wins ordering
  and run the full PR #176/#198 regression tests plus the new corpus.
- **Forcing structured output changes model behaviour.** On the Anthropic wire,
  a `ResponseFormat` becomes a forced tool. Mitigation: it is gated on
  `SupportsStructuredOutput`, is orthogonal to the (empty) exploration tool set,
  and the tolerant parser remains the net; a `nil` format is byte-identical to
  today. Providers with the flag *off* (`vertex-ai`, `azure-foundry`) are
  unaffected.
- **Larger token ceiling.** Raising the cap from 4096 to the catalogued value is
  a ceiling change, not a target; runaway output is still bounded by
  `maxSteps`. Consistent with the SQL fixer, which already does this.
- **Over-stripping `<think>`.** A model that legitimately puts its only JSON
  inside `<think>` would lose it. Mitigation: we strip *matched* pairs and dig
  JSON out of the surviving text; the in-think action case is in the corpus and
  must still parse (the action is extracted from within the block content before
  stripping decides the block is pure reasoning — see the corpus case "action
  inside a `<think>` block").

## 9. Alternatives considered

- **Pick the *first* action-keyed object (issue's literal wording).** Rejected
  as the selection rule: it reintroduces the leading-planning-preamble hijack
  that PR #176 fixed and its regression tests lock in. Stripping `<think>` +
  widening recognition + last-wins already delivers the issue's *intent* (the
  real action wins even when wrapped in reasoning/prose) without that
  regression. Documented deviation, called out on the PR.
- **A strict `oneOf` action schema.** Rejected for the primary path: strict
  `oneOf` is brittle on quantised open-model grammars and OpenAI strict mode
  forbids the open-ended shapes #340 went out of its way to preserve. A
  permissive typed object plus the tolerant parser + repair retry achieves
  "always well-formed" without over-constraining weak decoders (Rule 8).
- **Send `ResponseFormat` unconditionally (no capability gate).** Harmless
  (advisory field) but noisier and against #340's documented "gate on the flag"
  contract; gating also gives a clean enabled/disabled log signal.
- **A regex JSON extractor.** Rejected — brace-aware, string-literal-aware
  scanning already exists and handles braces inside SQL string literals, which a
  regex cannot do safely.

## 10. Out of scope

- The **separate** verifier parser at
  `services/agent/internal/validation/verifier/action.go` (own `ParseAction`
  over `ActionKind`, from #198). The issue scopes to the discovery exploration
  path; the verifier is noted as a follow-up candidate if it shares the same
  wrapping weakness, but is not touched here (Rule 8).
- Any prompt-template rewrite in the domain packs. The strict-format prompt
  guidance stays; this change makes the *parser* tolerant of models that don't
  perfectly obey it.

## 11. Verification / rollout

- Local gate before PR: `make build`, `make test-go`, `make lint-go`
  (after `export PATH=$PATH:$(go env GOPATH)/bin`). No UI touched, so
  `make test-ui` / `make lint-ui` are not required.
- Codex review loop until a clean round, then the Copilot pass, per Rules 7.
- Post-merge, the AI-gateway `analysis` alias can be moved off the forced Opus
  4.8 pin back onto a cheaper reasoning/open model — the original motivation.

---

*This is a PLAN for review — implementation follows after approval.*

Closes #341

— Co-coded with Jale 🤖
