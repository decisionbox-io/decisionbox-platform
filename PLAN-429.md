# Default discovery steps by the model's context window (small / medium / large)

## 1. Problem

Discovery's exploration phase keeps **every** step in one conversation, so the prompt grows monotonically — the issue measures ~0.7–1.2K tokens per step.
The step budget, however, is a constant: the API applies `max_steps = 100` when the caller sends none (`services/api/internal/handler/discoveries.go:303-306`) and derives `min_steps = floor(0.6 * max_steps)` (`:316`), and the agent's own flag default is the same 100 (`services/agent/agentserver/agentserver.go:77`).

On a 40,960-token model (Qwen3.5-27B behind vLLM) that combination cannot fit: the reported runs died at steps **31** and **33** with a context-length `400`.
Worse, one run's model *tried* to stop at step 19 and the 60% floor (60) rejected the `done` signal and pushed it into the overflow.
The same model with a 262K window runs 60+ steps fine.

So the step budget is a function of the context window, and today nothing connects the two.
The window is already resolved — carefully — at agent run start (`resolveModelBudget` in `services/agent/agentserver/llm_window.go`, logged as `Resolved model context budget`): operator override → persisted calibration (capped by live) → live auto-detect → catalog → default.
That resolution lives inside the agent, and the dashboard needs the numbers *before* a run starts.

Two secondary problems fall out of the same place:

- The Run-discovery popup resets to `useState(100)` on every mount (`ui/dashboard/src/app/projects/[id]/page.tsx:68`), so a value a user picked for a small-window model is forgotten on the next page load, per browser, per person.
- Schedule-driven triggers hard-code a step count in their form and always send it, so a schedule can never say "use whatever this project should use".

## 2. Approach

**The API decides the step budget; the agent keeps receiving explicit flags.**
That is the only placement that satisfies "the dashboard needs the defaults before a run starts", and it needs no new agent behaviour — the API already passes `--max-steps` / `--min-steps` through every runner (`subprocess.go:41-47`, `docker.go:522-527`, `kubernetes.go:244-247`).

Four moving parts:

1. **One shared window resolver.** `resolveModelBudget` moves verbatim (behaviour-preserving) from `services/agent/agentserver/llm_window.go` into `libs/go-common/llm`, which both `services/agent` and `services/api` already depend on. `discovery.ModelWindowKey` moves with it — the API has to read the same `llm_model_windows` key the agent writes, and a second copy of that keying rule is exactly the drift the issue forbids.
2. **A window → steps tier table**, env-configurable, in a new leaf package `services/api/internal/discoverysteps`.
3. **A saved per-project override** — `project.discovery_steps` — that beats the tier and is cleared by **Reset to recommended**.
4. **The popup reads, writes and clears it** through `GET/PUT/DELETE /api/v1/projects/{id}/discovery/defaults`.

Precedence, highest first, resolved in `DiscoveriesHandler.StartRun`:

| # | Source | Why it wins |
|---|--------|-------------|
| 1 | `effort` | Cloud's customer-facing intensity; already overrides raw `max_steps` today (`discoveries.go:291-297`). Unchanged. |
| 2 | explicit `max_steps` / `min_steps` on the request | The caller said a number. |
| 3 | `project.discovery_steps` | What a human last chose in the popup, for everyone on the project. |
| 4 | tier from the resolved window | The new default. |

Tiers (defaults; every number env-overridable):

| Tier | Resolved window | `max_steps` | `min_steps` |
|---|---|---|---|
| small | `< 65536` | 20 | 5 |
| medium | `65536 … < 131072` | 30 | 12 |
| large | `>= 131072` | 100 | 60 |

The large tier reproduces today's numbers exactly, so every deployment on a ≥128K model is byte-identical to before.
A window that resolves to `<= 0` (i.e. we failed to learn anything) is treated as **large**, not small: "we don't know" must not silently shrink a run, and `GetEffectiveInputWindow` already floors at `DefaultMaxInputTokens` (131072), so this is a defensive branch rather than a live path.

## 3. Files

### 3.1 `libs/go-common/llm` — the shared resolver

**New `libs/go-common/llm/window.go`:**

```go
// ResolveModelBudget resolves the effective context window and output cap
// for a (provider, model, project-config) triple without depending on the
// model being catalogued. Window precedence: operator max_input_tokens
// override → persisted calibration → live auto-detection → catalog/default,
// then capped by the live-detected window. Output: live → catalog/default,
// clamped by the operator max_output_tokens override.
func ResolveModelBudget(ctx context.Context, in BudgetInput) ResolvedBudget

type BudgetInput struct {
    Provider        Provider        // may be nil — live auto-detect is then skipped
    ProviderName    string
    Model           string
    Config          ProviderConfig
    PersistedWindow int
}

type ResolvedBudget struct {
    Window       int
    OutputCap    int
    WindowSource string // operator_override | persisted_calibration | live_autodetect | catalog_default (+ "_capped_by_live")
    LiveError    error  // non-nil when the live model-info lookup failed; never fatal
}

// ModelWindowKey is the identifier a model's self-calibrated window is
// persisted under (moved from services/agent/internal/discovery).
func ModelWindowKey(model string, cfg ProviderConfig) string
```

The logic is lifted unchanged, including the 8-second `modelInfoResolveTimeout` and the cap-by-live rule.
Two deliberate shape changes, both so `libs/go-common/llm` stays logging-free (it has no logger dependency today and I don't want to add one):

- `WindowSource` is **returned** instead of logged.
- The live-lookup failure is **returned** as `LiveError` instead of being logged at `Debug`.

**Deleted:** `services/agent/agentserver/llm_window.go`'s `resolveModelBudget` body and `services/agent/internal/discovery/model_window.go`'s `ModelWindowKey` (Rule 6 — moved, not shimmed).

### 3.2 `services/agent` — call the moved resolver, log identically

- `agentserver.go:1051` → `gollm.ModelWindowKey(...)`.
- `agentserver.go:1059` → `gollm.ResolveModelBudget(...)`, then emit the existing two log lines from the returned struct: the `Debug` "Live model-info lookup failed…" when `LiveError != nil`, and the `Info` `Resolved model context budget` with the same `provider/model/window/window_source/output_cap` fields. Log output is unchanged.
- `services/agent/internal/discovery/model_window.go:128` → `gollm.ModelWindowKey`.
- `llm_window.go` keeps only `projectModelWindowStore`; the file is renamed to `model_window_store.go` since it no longer holds a resolver.
- `services/agent/agentserver/llm_window_test.go` moves to `libs/go-common/llm/window_test.go` (same nine cases, in-package so `gollm.` qualifiers drop). `model_window_test.go`'s `TestModelWindowKey` moves with it.
- **No behavioural agent change.** A direct `decisionbox-agent --max-steps=…` CLI run keeps its 100/0 flag defaults. Tiering there would be a second decision point for the same question and the API is the one the product goes through; out of scope by the issue's own "the API decides the tier, not the agent".

### 3.3 `services/api/internal/discoverysteps` — the tier table (new leaf package)

```go
type Tier string
const (TierSmall Tier = "small"; TierMedium Tier = "medium"; TierLarge Tier = "large")

type Steps struct{ MaxSteps, MinSteps int }

// Tiers is the resolved tier configuration (env-driven, defaults above).
type Tiers struct {
    SmallBelow, MediumBelow int
    Small, Medium, Large    Steps
}

func LoadTiers() Tiers                               // reads env, lenient
func (t Tiers) For(window int) (Steps, Tier)
func Recommend(window int) (Steps, Tier)             // LoadTiers().For(window)
```

Env vars, parsed with the same leniency as `llm.ResolveHTTPTimeout` (unparseable or out-of-range → that field's default, never a panic or a start-up failure):

| Var | Default | Meaning |
|---|---|---|
| `DISCOVERY_STEPS_TIER_SMALL_BELOW` | `65536` | windows strictly below this are small |
| `DISCOVERY_STEPS_TIER_MEDIUM_BELOW` | `131072` | below this (and ≥ the small bound) is medium; at or above is large |
| `DISCOVERY_STEPS_SMALL_MAX` / `_MIN` | `20` / `5` | |
| `DISCOVERY_STEPS_MEDIUM_MAX` / `_MIN` | `30` / `12` | |
| `DISCOVERY_STEPS_LARGE_MAX` / `_MIN` | `100` / `60` | |

Validation inside `LoadTiers`: a `max` must be `> 0`; a `min` may be `0` (floor disabled) but not negative and not `> max`; `SMALL_BELOW` must be `< MEDIUM_BELOW`. Any violated pair falls back to the defaults for that pair alone, so one typo cannot take out the whole table. Nothing logs from here — the `GET .../discovery/defaults` response returns `tier`, `window` and `recommended`, which is how an operator confirms what took effect, and `StartRun`'s existing "Starting discovery run" log line gains `max_steps` / `min_steps` / `steps_source`.

Placement rationale: this is a *discovery* concept, not an LLM one, so it does not belong in `libs/go-common/llm`; only the API needs it, so a go-common package would be reach without a caller. `libs/go-common/policy` (which already holds `effortSteps`) was the other candidate — see §8.

### 3.4 `services/api/models/project.go` — the saved override

```go
// DiscoverySteps is the project's remembered exploration-step budget, set
// from the Run-discovery popup and cleared by Reset to recommended. Nil
// (absent) means "use the context-window tier default". MinSteps 0 is
// meaningful (floor disabled), which is why presence of the subdocument —
// not a zero value — is what marks it as set.
type DiscoverySteps struct {
    MaxSteps int `bson:"max_steps" json:"max_steps"`
    MinSteps int `bson:"min_steps" json:"min_steps"`
}

DiscoverySteps *DiscoverySteps `bson:"discovery_steps,omitempty" json:"discovery_steps,omitempty"`
```

A stored document with `MaxSteps <= 0` is treated as unset (defensive against a hand-edited row).

The agent's `services/agent/internal/models/project.go` copy is **not** touched: the agent never reads this field (the API passes flags), and an unread mirror field would be dead code (Rule 6). Called out explicitly because several sibling fields do carry a "keep the two definitions in sync" comment — the new field gets a comment saying why it is API-only.

### 3.5 `services/api/database`

- **New `llm_model_window_repo.go`** — read-only accessor for the agent-written `llm_model_windows` collection (already in `database.go:230`'s collection list): `GetWindow(ctx, projectID, provider, model) (int, error)`, a miss returning `(0, nil)`. Mirrors the existing precedent of the API reading agent-written collections (`debug_log_repo.go`, `discovery_log_repo.go`). It never writes and never creates indexes — the agent owns both.
- **`database.go`** — `(*ProjectRepository).SetDiscoverySteps(ctx, id string, steps *models.DiscoverySteps) error`: `$set` when non-nil, `$unset` when nil. A focused mutator is required because `Update`'s `$set: p` marshal with `omitempty` **cannot clear** the field — Reset to recommended would silently no-op through it. Also stamps `updated_at`.
- **`interfaces.go`** — add `SetDiscoverySteps` to `ProjectRepo` (alongside the existing focused mutators `SetSchemaIndexStatus` / `BeginReindex`) and a new `ModelWindowRepo` interface for handler tests. Three test stubs implement `ProjectRepo` and need the new method: `handler/mock_test.go:208`-area `mockProjectRepo`, `handler/providers_test.go:244`-area `stubProjectRepo`, `handler/search_test.go:62`-area `mockProjectRepoForSearch`.

### 3.6 `services/api/internal/handler` — resolution + endpoints

**`discoveries.go`:**

- `DiscoveriesHandler` gains two optional deps and a builder in the established style (`WithRunSummaries` / `WithDeleteCascadeDeps`):
  `func (h *DiscoveriesHandler) WithDiscoveryDefaults(secretProvider gosecrets.Provider, windowRepo database.ModelWindowRepo) *DiscoveriesHandler`.
  Both may be nil — the resolver then skips the persisted lookup and live auto-detect and falls through to override/catalog/default, so every existing handler test keeps compiling and passing untouched.
- `StartRun`'s step block is restructured to the four-rung precedence in §2. The `effort` branch and the `min_steps` range validation stay where they are. Concretely:
  - `effort` set → today's path, unchanged (`StepsForEffort` → max, `min = 60%`).
  - else resolve `(steps, source)`: explicit `opts.MaxSteps > 0` → `("request")`; else `project.DiscoverySteps` → `("saved")`; else tier → `("tier")`.
  - `opts.MinSteps != nil` still wins for the floor, and is still range-checked against the **resolved** max.
  - `opts.MaxSteps` is set to the resolved max before the runner call, so the agent always receives an explicit `--max-steps` (it did not before when the caller omitted it).
- **Behaviour change worth stating:** a caller that sends `min_steps` alone, with no `max_steps`, is now validated against the tier's max instead of a constant 100. On a small-window model `{"min_steps": 80}` becomes a `400` naming the resolved max and its source, where it used to be accepted. Clamping instead was rejected — silently lowering a floor a caller explicitly asked for is worse than telling them. The popup always sends both, so this only reaches raw API callers.
- Only rung 4 pays for window resolution, so a trigger with explicit or saved steps makes no extra Mongo read and no provider call.

**New `discovery_defaults.go`:**

- `resolveDiscoveryDefaults(ctx, p *models.Project) discoveryDefaults` — the single resolution used by all three endpoints *and* `StartRun`: read the persisted window (`ModelWindowKey` + `ModelWindowRepo`, best-effort), build the project's LLM provider (best-effort), `llm.ResolveModelBudget`, `discoverysteps.Recommend(window)`, then apply the saved override. Every failure degrades — a dead gateway, a missing credential or an unreachable secret store yields a catalog/default window and a tier, never an error to the caller.
- `GET /api/v1/projects/{id}/discovery/defaults` → `{max_steps, min_steps, source: "saved"|"tier", tier, window, recommended: {max_steps, min_steps}}`. `viewer`.
- `PUT` same path, body `{max_steps, min_steps}` → validates `max_steps > 0` and `0 <= min_steps <= max_steps` (400 otherwise), persists, returns the fresh `GET` shape. `member`.
- `DELETE` same path → `SetDiscoverySteps(nil)`, returns the fresh `GET` shape with `source: "tier"`. `member`. Idempotent on a project that has nothing saved.

**`search.go` / shared:** `(*SearchHandler).createLLMProvider` (`search.go:950-966`) becomes a package-level `newProjectLLMProvider(ctx, secrets, project, projectID)`, called by both `SearchHandler` and the new resolver — mirroring the existing package-level `newProjectEmbeddingProvider`. This is de-duplication forced by a second caller, not a speculative refactor; a third copy of the credential-resolution order is precisely what `gosecrets.ResolveCredential` exists to prevent.

**`server.go`:** three routes on the `/api/v1/projects/{id}/…` subtree (so the project ACL middleware already covers them), and `discoveries := handler.NewDiscoveriesHandler(...).WithDiscoveryDefaults(secretProvider, modelWindowRepo)`.

### 3.7 `ui/dashboard`

**`src/lib/api.ts`** — a `DiscoveryDefaults` type and three calls: `getDiscoveryDefaults(projectId)`, `saveDiscoverySteps(projectId, {max_steps, min_steps})`, `resetDiscoverySteps(projectId)`. Also drop the stale "default 100" wording from the `triggerDiscovery` doc comment (`api.ts:1730-1735`).

**`src/app/projects/[id]/page.tsx`:**

- `maxSteps` / `minSteps` seed from `getDiscoveryDefaults`, fetched in the existing mount `Promise.all`. A `defaults` state holds the response so the popup can render the recommendation and decide whether values changed.
- **Failure is not fatal:** if the call fails, the popup falls back to today's 100 / 60 and simply shows no recommendation line. A defaults endpoint blip must not block starting a run.
- The popup shows the recommendation next to the fields — `Recommended for this model: 20 / 5 (40,960-token window · small)` — so a saved 100 on a small-window model is visible. When `source === "saved"` and the values differ from `recommended`, the line reads as an explicit contrast rather than a hint.
- **Reset to recommended** menu item: `resetDiscoverySteps` → reseed both fields from the response → toast.
- On run: if either field differs from the loaded `{max_steps, min_steps}`, `saveDiscoverySteps` first (a save failure toasts and the run continues — the run is what the user asked for). Then trigger with **both** values explicit; the `if (maxSteps !== 100)` guard at `:189` is deleted, since 100 is no longer a universal default.
- The existing "min tracks 60% of max until touched" behaviour is kept for *user edits of max* only; the initial seed comes from saved-or-tier, which supplies both numbers. Changing that rule is not in scope.
- The "Discovery started" toast reports the values actually sent.

### 3.8 Downstream overlay (private repo, matching branch `jale/issue-429`)

`ui/dashboard/src/app/projects/[id]/page.tsx` is mirrored by a downstream dashboard overlay whose copy **completely replaces** the community file at image-build time; the two have diverged (1399 vs 1358 lines), so the popup change must be applied to both or the overlay build silently loses it. A linked PR on the same branch name carries:

- the same popup changes, merged into the overlay's copy;
- the schedule form: stop seeding a hard-coded step count and leave it unset, so a schedule follows the project's saved-or-tier values unless it sets its own. **No server change is needed there** — the schedule model already declares `max_steps,omitempty` with "0 = agent default", its store deliberately `$set`s 0 so the field can be cleared, its validator already accepts 0, and its trigger already leaves `min_steps` to the community default. The form is the only thing that forced a value.
- the two doc lines that quote "default 100" for the popup and the schedule API example.

The overlay's `lib/` does not shadow the community `src/lib/api.ts`, so the three new API calls are available there with no extra work.

*(Kept deliberately unspecific here to respect Rule 11; the exact file list is in the review thread.)*

## 4. Phases

1. **Shared resolver.** Move `ResolveModelBudget` + `ModelWindowKey` into `libs/go-common/llm` with their tests; rewire the agent; delete the originals. `make build` + `make test-go` green, agent log output unchanged.
2. **Tier package.** `services/api/internal/discoverysteps` + boundary tests. No wiring yet.
3. **Persistence.** `models.DiscoverySteps`, `SetDiscoverySteps`, `ModelWindowRepo`, interface + three test stubs.
4. **Resolution + endpoints.** `newProjectLLMProvider` extraction, `resolveDiscoveryDefaults`, the three routes, `WithDiscoveryDefaults`, `StartRun` precedence. Handler tests.
5. **Dashboard.** `api.ts`, popup, `ProjectPage.test.tsx` cases.
6. **Docs + CHANGELOG** (§6).
7. **Overlay PR** on the matching branch, cross-referenced.

Each phase is a separate commit so the review loop can bisect.

## 5. Tests

### Unit — `libs/go-common/llm/window_test.go`
The nine moved cases (operator override wins; persisted beats live; stale persisted capped by live; stale override capped by live; override uncapped without live; live beats catalog; falls back to default; live error falls through; output override caps live) plus `ModelWindowKey`'s three (model id, `endpoint:<id>`, empty). These must pass **unchanged in meaning** — that is the evidence the move was behaviour-preserving.

### Unit — `discoverysteps`
- Boundaries, table-driven: `40960→small`, `65535→small`, `65536→medium`, `131071→medium`, `131072→large`, `262144→large`.
- `window <= 0` → large (the "unknown must not shrink a run" rule).
- Env overrides applied via `t.Setenv`: a moved boundary re-tiers a window; custom step values are returned.
- Malformed env (`"abc"`, `"-5"`, `min > max`, `SMALL_BELOW >= MEDIUM_BELOW`) → that pair falls back to defaults, others still honoured.

### Unit — `handler` (`discovery_defaults_test.go`, `discoveries_test.go`)
- Precedence matrix: effort > explicit > saved > tier, including *saved-over-tier* and *explicit-over-saved*.
- Saved `{max: 100, min: 60}` on a small-window project → run gets 100/60 (the saved value is honoured and the popup's recommendation line is how the user sees the mismatch).
- Saved `{min: 0}` survives round-trip as "floor disabled" and is not mistaken for unset.
- No steps anywhere + a catalog default window → 100/60, i.e. **the existing default is preserved** for large models.
- `min_steps` alone against a small tier → 400 naming the resolved max.
- `PUT` validation: `max_steps <= 0` → 400; `min_steps < 0` → 400; `min_steps > max_steps` → 400.
- `DELETE` clears and flips `source` to `"tier"`; `DELETE` on an unsaved project is a 200 no-op.
- Nil `secretProvider` / nil `windowRepo` → resolution still returns a tier (degradation path).
- `ModelWindowRepo` returning an error → not fatal; falls through to catalog/default.
- `GET` shape: all six fields present, `recommended` always the tier values even when `source == "saved"`.

### Integration (testcontainers, real Mongo)
- `SetDiscoverySteps` set → `GetByID` reads it back; `SetDiscoverySteps(nil)` → the field is **absent** from the stored document (the `$unset` assertion — the bug a `$set`-based implementation would have).
- `LLMModelWindowRepository.GetWindow` reads a row the agent's writer shape produced (same collection, same key), and returns `(0, nil)` on a miss. This is the cross-service contract; asserting it against a mock would assert nothing.

### Dashboard (`ProjectPage.test.tsx`)
- Popup prefills from `getDiscoveryDefaults` (20/5) rather than 100/60, and renders the recommendation line.
- `source: "saved"` with values above `recommended` → saved values in the fields, recommendation still shown.
- Reset to recommended → `resetDiscoverySteps` called, fields reseed from the response.
- Edited value → `saveDiscoverySteps` called before `triggerDiscovery`, and `triggerDiscovery` receives both values explicitly.
- `getDiscoveryDefaults` rejecting → popup still renders, falls back to 100/60, run still triggerable.

### Local gates before the PR
`make build`, `make test-go`, `make lint-go`, `make test-ui`, `make lint-ui`, `make lint-docs`; the relevant `make test-integration` target; plus the overlay repo's own build/typecheck for the overlay PR (it has no PR-time UI CI, so a local dashboard image build is the check).

### Real-model verification (acceptance 1 and 2)
Against the dev stack: a project on the 40,960-window vLLM model with nothing saved → `GET .../discovery/defaults` reports `small`, 20/5; a real discovery completes exploration with **no** context-length 400. A ≥128K-window project → 100/60 and an unchanged run. Reported honestly on the PR, including anything I cannot reach.

## 6. Docs (Rule 4)

- `docs/reference/api.md` — `max_steps` / `min_steps` defaults in the `/discover` table become "project's saved value, else the context-window tier"; new `GET/PUT/DELETE /api/v1/projects/{id}/discovery/defaults` section with the response shape and the tier table; the "Run all areas with default steps" example comment.
- `docs/reference/configuration.md` — the seven `DISCOVERY_STEPS_*` vars in the discovery table; the `--max-steps` / `--min-steps` rows noted as "set by the API from the project's saved or tier value".
- `docs/reference/cli.md` — same note on the two flag rows (the agent's own defaults are unchanged for direct CLI use).
- `docs/reference/data-models.md` — `discovery_steps` in the Project table.
- `docs/concepts/discovery-lifecycle.md` — the exploration section explains that the default budget follows the resolved window, and why (the conversation grows per step).
- `docs/guides/configuring-llm.md` — one paragraph in "Context window and output limits": the resolved window now also sets the default step budget.
- `docs/getting-started/first-discovery.md:90` — "default: 100" → the tier/saved wording.
- `helm-charts/decisionbox-api/values.yaml` + `docs/reference/helm-values.md` — commented `DISCOVERY_STEPS_*` block.
- `CHANGELOG.md` — an `### Added` entry in the house narrative style: what overflowed, why steps are a window-sized budget, the tier table, that large is unchanged, that values are remembered per project, and the honest limit from the issue's own out-of-scope note.

## 7. Risks

- **The move is the risky part, not the feature.** `ResolveModelBudget` guards every analysis/recommendation call against a hard 400. Mitigation: move it verbatim, carry all nine tests, keep the log line and its fields identical, and make the only shape changes (source + live error returned rather than logged) mechanical.
- **Trigger latency.** Rung 4 adds one Mongo read plus one best-effort provider metadata call, bounded at 8s by the moved constant. Only the no-steps-anywhere path pays it, and every failure mode degrades to catalog/default rather than blocking. The popup's `GET` pays it on page load, where the existing live-models endpoint already spends 15s.
- **Steps are a proxy, not a guarantee.** The issue says so: per-step size varies several-fold, so a small-window run can still overflow. This lowers the risk; token-based trimming of the exploration conversation is the actual fix and is a separate issue. The PR will say this plainly rather than claiming the 400 is eliminated.
- **A saved 100 on a small-window model still overflows.** Deliberate — a human's explicit choice wins. Visibility is the mitigation: the popup shows the recommendation next to the field.
- **Overlay drift.** If the overlay PR is not merged with this one, the overlay dashboard keeps `useState(100)` and loses the feature silently. Mitigation: same branch name, cross-referenced PRs, merged together.
- **`min_steps`-alone callers.** Covered in §3.6 — a documented, tested `400` rather than a silent clamp.

## 8. Alternatives considered

- **Tier in the agent.** Rejected by the issue and on merit: the dashboard needs the numbers before the run exists, and the agent resolves its window after start-up.
- **Tier table in `libs/go-common/policy`, beside `effortSteps`.** Genuinely tempting — the two step-budget tables would live together. Rejected because `policy` is the plan/metering surface (`Operation`, `OperationCharger`, plan gating) and `effortSteps` is there because *effort* is a plan-facing knob; a window→steps table is neither. Cheap to relocate if review prefers it.
- **A single packed env var** (`DISCOVERY_STEP_TIERS="65536:20/5,131072:30/12,*:100/60"`). One variable instead of seven, but a bespoke mini-format to document, parse and validate. Named vars are boring and individually overridable.
- **Store the saved steps in `project.llm.config`.** Rejected: that map is operator LLM settings and the self-calibration code deliberately keeps its learned values out of it. Steps are a discovery setting.
- **Save through the existing `PUT /api/v1/projects/{id}`.** Rejected: that route cannot clear the field (`$set` + `omitempty`), it re-validates datasources and LLM config on every call, and it would make the popup round-trip the whole project object to change two numbers.
- **Scale `min_steps` as 60% of the tier's max instead of naming it.** Gives 12 for the small tier where the issue asks for 5 — and 5 is the number that lets the run the issue describes actually finish. Named per tier.
- **Derive the tier client-side from the model name.** Rejected: the whole point of #347/#348 was that the catalog cannot be relied on for customer models; only the resolver knows the real window.

## 9. Acceptance mapping

| Issue acceptance | Where |
|---|---|
| < 64K, no explicit steps → 20/5, no context-length 400 | §3.3 tiers, §3.6 rung 4, §5 real-model verification |
| ≥ 128K still gets 100/60 | large tier == today's numbers; §5 "existing default preserved" test |
| explicit steps and `effort` override the tier | §2 rungs 1–2, §5 precedence matrix |
| popup values saved on the project; next popup (any user/browser) opens with them; a no-steps trigger uses them | §3.4, §3.6 `PUT`, §3.7, §5 saved-over-tier |
| Reset to recommended clears them; popup and triggers fall back to the tier | §3.5 `$unset`, §3.6 `DELETE`, §3.7, §5 integration `$unset` assertion |
| unit tests at each boundary + saved-over-tier precedence; dashboard shows saved or tier values | §5 |

---

**This is a PLAN for review — no implementation is included in this PR. Implementation follows after approval.**

Closes #429

— Co-coded with Jale 🤖
