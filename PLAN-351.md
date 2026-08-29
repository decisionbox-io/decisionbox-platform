# Plan — Issue #351: mask secret inputs, replace "Test" with Save / Save & Test, scrollable Run Discovery popover

Three independent UI/UX defects in the project **create / edit** and **project
home** screens. They share the dashboard component tree, so the platform repo
carries most of the fix; a paired enterprise bridge PR handles the
enterprise-only surfaces (see **Cross-repo** at the end).

This document is the source of truth for the build step. All `file:line`
references are against the current branch (cut from `origin/main` at `bcaeb3e`).
In this repo the dashboard lives at `ui/dashboard/…` (the issue writes
`platform/ui/dashboard/…`; same files).

---

## Investigation summary — what actually shapes the design

Facts verified in the code that the plan depends on:

1. **Every secret field the backend declares carries `Type: "credential"`.**
   Confirmed for warehouse (`providers/warehouse/*/provider.go`, e.g.
   `bigquery/provider.go:66` Service Account JSON), LLM
   (`providers/llm/openai/provider.go:79` API Key,
   `providers/llm/bedrock/provider.go:85` AWS keys), and embedding. The Go
   `ConfigField` struct (`libs/go-common/warehouse/registry.go:54`,
   `.../llm/registry.go:475`, `.../embedding/registry.go:35`) has **no
   multi-line flag** — single-line (API key, `AKID:secret`) and multi-line (SA
   JSON) credentials are both just `Type: "credential"`. Multi-line detection
   must therefore live in the UI. No backend change is needed for masking.

2. **The credential input is rendered in exactly three shared dashboard
   components today, all as a plaintext monospace `<Textarea>`:**
   - `ui/dashboard/src/components/projects/WarehouseFormFields.tsx:139-151`
     (`authCredField` slot).
   - `ui/dashboard/src/components/projects/LLMFormFields.tsx:155-169`
     (`credentialField` slot).
   - `ui/dashboard/src/components/ProviderCredentialsPhase.tsx:270-282`
     (`credentialField` slot) — used by **both** `EmbeddingEditor.tsx` and
     `BlurbLLMEditor.tsx`, which compose it. Fixing this one component masks
     embedding **and** blurb credentials.
   The generic field renderers `common/LLMModelField.tsx:24` (`DynamicField`)
   and `WarehouseFormFields.tsx:27` (`DynamicField`) only ever receive
   **non-credential** fields today (the credential is split out), but the issue
   wants a generic `type === 'credential'` guard there too so a future provider
   that puts a credential in `config_fields` is covered automatically.

3. **No `PasswordInput` is used anywhere in the dashboard yet** (`grep`
   confirms zero hits). Mantine `PasswordInput` (built-in `visibilityToggle`) is
   available and is the right building block for single-line secrets.

4. **The new-project wizard (`app/projects/new/page.tsx`) consumes the shared
   components directly** (`WarehouseFormFields`, `LLMFormFields`,
   `EmbeddingEditor` — imports at `:12-14`), so masking the three components
   above fixes the wizard for free. The edit panels
   (`WarehouseConfigPanel`, `ProvidersPanel`) also consume them.

5. **The API server already registers every warehouse/LLM/embedding provider
   in-process** (`services/api/apiserver/apiserver.go:43-67` blank imports) and
   already **builds providers in-process from user-entered form credentials**
   for the "Load models" feature: `fetchLiveModels`
   (`services/api/internal/handler/providers.go:185` → `gollm.NewProvider`) and
   `fetchLiveEmbeddingModels` (`:583` → `goembedding.NewProvider`). This is the
   established precedent for "use the current form's provider + credential
   against the real upstream, before anything is saved" — and it is decisive for
   the P2 approach (below).

6. **Why "Test before Save" fails today.** The test endpoint ignores the form
   entirely and tests the last-saved Mongo record:
   - Frontend sends **no body**: `api.testWarehouse/testLLM/testEmbedding/
     testBlurbLLM` (`lib/api.ts:1304-1311`) POST with method only.
   - `test_connection.go:runTest` (`:51`) reads only `r.PathValue("id")`,
     does `projectRepo.GetByID` as an existence check, and shells the agent
     with `--test-connection <target>` (`:67`). `r.Body` is never read.
   - The agent (`services/agent/agentserver/agentserver.go:481`
     `runTestConnection`) loads the project from Mongo (`:502`) and builds the
     provider from the **saved** record (`:512-649`). Nothing from the unsaved
     form ever reaches it.

---

## ⚠️ Decision needed before build — P2 backend approach

Can chose **Option B** (root-cause: test the current form values, works in
create and edit). His comment describes threading the params *"→ agent (instead
of `projectRepo.GetByID` at `agentserver.go:502/519-523`)"*. Investigation
surfaced a cleaner realization of that same intent, plus a security problem with
the literal agent-threading path. I want sign-off on which to build.

**Recommended — B2: test in-process in the API** (mirrors the existing
`/models/live` endpoints).
The API already constructs providers from posted form credentials for "Load
models". The connection test is the same shape of problem, so the test endpoint
builds the provider in-process from the request body and calls
`HealthCheck` / `Validate` / `Embed` directly — **no agent subprocess, no runner
plumbing, and the secret never leaves the API process.**
- Pros: small, reuses an established pattern; identical behavior for create (no
  project) and edit; nothing written to argv/env/Mongo; no cross-runner work.
- Cons: a warehouse `HealthCheck` now runs in the API process rather than the
  agent. In practice reachability is the same (compose: shared network; K8s:
  same namespace, and the API already egresses to LLM/embedding upstreams). It
  skips the agent's warehouse governance middleware — but a connectivity ping
  runs no SQL, so governance (which masks *query results*) has nothing to gate.
- Enterprise note: B2 tests Oracle/HANA in-process in the **enterprise API**
  binary. That binary already registers those providers (it must, to list them
  under `/api/v1/providers/warehouse`), so this works with no new registration —
  to be confirmed on the enterprise bridge branch.

**Literal alternative — B1: thread the body through the runner to the agent.**
Matches Can's wording exactly and keeps byte-for-byte parity with discovery's
connect path (same middleware, same network context). But delivering an *unsaved
secret* to the agent is the problem: the runner passes only `ProjectID` + CLI
`Args` (`runner.go:72`), and the three runners deliver via argv
(`subprocess.go:137`, `docker.go:637`, `kubernetes.go:306`). Putting a
credential in a K8s **Job spec** (argv/env) exposes it via `kubectl get job -o
yaml` / etcd — a real regression. Doing it safely needs either stdin (K8s Jobs
can't stream it) or an ephemeral encrypted Mongo hand-off the agent reads by
token — a lot of new surface for a Test button.

**My recommendation: build B2.** It is the minimal, secure, precedent-following
way to achieve exactly what Can asked for ("test the form, works in create and
edit"). I'll proceed on B2 unless Can prefers B1. This is flagged to him in the
issue + Slack; the rest of the plan assumes **B2**.

---

## Problem 1 — Mask secret inputs

### Approach
Add one shared component and route every credential render through it. Do it
generically at the field renderer so all current and future providers are
covered without per-provider edits.

**New component — `ui/dashboard/src/components/common/MaskedSecretInput.tsx`:**
- Props: `label`, `description`, `required`, `placeholder`, `value`,
  `onChange`, and `multiline?: boolean`.
- `multiline` **false** (default, single-line secrets — API key, DB password,
  token, connection string, `AKID:secret`): render Mantine **`PasswordInput`**
  with its built-in visibility (eye) toggle. Set
  `autoComplete="new-password"` and `spellCheck={false}` so browsers don't
  autofill or surface the value.
- `multiline` **true** (SA JSON, multi-line blobs): render an autosize
  monospace **`Textarea`** that is obscured by default via
  `style={{ WebkitTextSecurity: revealed ? 'none' : 'disc' }}` with a small
  eye button (`IconEye` / `IconEyeOff`) in a right-section/label-adornment to
  toggle `revealed`. `autoComplete="off"`, `spellCheck={false}`. (`-webkit-text-
  security` renders masked in Chromium/Safari/Firefox 117+; universal in 2026.)
- Single-line is the default so the eye toggle and non-autofill behavior are
  consistent; the component keeps value/onChange controlled so the existing
  "leave empty to keep current" flow is untouched.

**Multi-line detection helper** (co-located, exported for reuse):
`credentialIsMultiline(field, value)` → true when the field label or placeholder
contains `"json"` (SA-JSON credentials are labelled e.g. "Service Account JSON")
**or** the current `value` contains a newline. This is generic (no provider
hardcoding) and covers the known multi-line cases (GCP/Vertex SA JSON, any
future JSON credential) while keeping API keys / AWS keys / DB passwords
single-line. Verified against `bigquery/provider.go:66` ("Service Account
JSON"), `openai/provider.go:79` ("API Key"), `bedrock/provider.go:85`
("Access Key ID : Secret Access Key").

### Wiring (platform)
1. **`WarehouseFormFields.tsx`**
   - Credential slot `:139-151`: replace the `<Textarea>` with
     `<MaskedSecretInput multiline={credentialIsMultiline(authCredField, value.credential)} …>`.
     Preserve the existing `hasSavedCredential` label ("Update …") + description
     ("Stored encrypted. Leave empty to keep current.") logic verbatim.
   - `DynamicField` (`:27-53`): add a `field.type === 'credential'` branch →
     single-line `MaskedSecretInput` (defensive; non-credential fields keep
     `TextInput`/`Textarea`).
2. **`LLMFormFields.tsx`** credential slot `:155-169`: same replacement, keeping
   the `hasSavedApiKey` label/description logic.
3. **`ProviderCredentialsPhase.tsx`** credential slot `:270-282`: same
   replacement (covers embedding + blurb via `EmbeddingEditor` /
   `BlurbLLMEditor`).
4. **`common/LLMModelField.tsx`** `DynamicField` (`:24`): add the
   `field.type === 'credential'` → single-line `MaskedSecretInput` branch (this
   renderer feeds the LLM/embedding non-credential fields; the guard future-
   proofs any provider that declares a credential in `config_fields`).

The read-only "saved" chips (`WarehouseConfigPanel.tsx:142-151`,
`ProvidersPanel.tsx:277-287`) already show only a server-side **masked** value
(`SecretEntryResponse.masked`) — no change needed.

### Enterprise (companion PR)
- `enterprise/ui/src/components/finetuning/ProviderConfigForm.tsx:348-355` —
  its own plaintext credential `<Textarea>`; swap to `MaskedSecretInput`
  (the new component ships in the community tree and is present in the enterprise
  Docker overlay build automatically).
- Verify the overlaid `WarehouseConfigPanel` + Oracle/HANA credential fields
  inherit masking through the shared `WarehouseFormFields` (they should — the
  enterprise panel delegates field rendering to the community component).

---

## Problem 2 — Replace standalone "Test" with **Save** and **Save and Test**

### Approach (assumes B2)
"Save and Test" always saves first, so the edit panels can test the
just-saved state; the create wizard tests the **form** through new project-less
endpoints. Testing never blocks — a failed/absent test persists anyway and
leaves a non-blocking warning on the section.

### Backend (platform)
Add three **project-less** test endpoints that build the provider in-process
from the posted config (direct analogues of the existing
`providers/{llm,embedding}/{id}/models/live` handlers in
`services/api/internal/handler/providers.go`):

- `POST /api/v1/providers/warehouse/{id}/test` — body `{ config }` →
  `gowarehouse.NewProvider(id, cfg)` → `ApplyMiddleware` → `HealthCheck(ctx)`.
  Returns `{ success, provider, datasets?, error? }`.
- `POST /api/v1/providers/llm/{id}/test` — body `{ config }` →
  `gollm.NewProvider` → `Validate(ctx)`.
- `POST /api/v1/providers/embedding/{id}/test` — body `{ config }` →
  `goembedding.NewProvider` → `Embed(ctx, ["ping"])`.

`config` carries the credential under its auth-method field key (same shape
`fetchLiveModels` already accepts). These live next to the `models/live`
handlers, reusing their config-building and error-shaping. Registered in
`apiserver.go` alongside the existing provider routes.

The existing **project-scoped** endpoints
(`POST /api/v1/projects/{id}/test/{warehouse|llm|embedding|blurb-llm}`,
`test_connection.go`) are **unchanged**: edit-panel "Save and Test" calls Save,
then the existing project test (which now reads the freshly-saved config — the
"before Save" bug is gone because Save always precedes Test). This keeps the
agent-backed project test intact for the blurb path and avoids touching the
runner. (If Can prefers literal body-testing in edit too, the project-scoped
handler can additionally accept a `{ config }` override — noted as an option, not
in the default build; Rule 8.)

### Frontend (platform)
- **`lib/api.ts`**: add `testWarehouseConfig(providerId, config)`,
  `testLLMConfig(providerId, config)`, `testEmbeddingConfig(providerId, config)`
  hitting the new project-less endpoints.
- **`WarehouseConfigPanel.tsx`**:
  - Remove the standalone `<TestConnectionButton>` render (`:161`).
  - Replace the single "Save …" button (`:163-167`) with **Save** and **Save
    and Test**. "Save" = existing `handleSave`. "Save and Test" = `handleSave`
    then `api.testWarehouse(projectId)`; on `!success`/throw, keep a persistent
    non-blocking warning on the section
    ("⚠ Last test failed: <reason>" / "⚠ Connection not verified"), never block.
  - Keep the `TestConnectionButton` export only if still referenced; otherwise
    delete it (Rule 6 — no dead code). It is imported by `ProvidersPanel.tsx:17`,
    so it will be removed there too → delete the component.
- **`ProvidersPanel.tsx`**:
  - Remove the LLM (`:310-312`) and embedding (`:330-332`)
    `<TestConnectionButton>` renders and the `TestConnectionButton` import
    (`:17`).
  - Replace the single "Save providers" button (`:334-338`) with **Save** +
    **Save and Test** (tests LLM and embedding via the project endpoints after a
    successful save; aggregates results into per-section non-blocking warnings).
- **Blurb** (`app/projects/[id]/settings/page.tsx`, `saveBlurb:234`): add a
  "Save and Test" alongside "Save blurb model" (`:335`) using
  `api.testBlurbLLM(projectId)` after save; non-blocking warning on failure.
- **Create wizard** (`app/projects/new/page.tsx`): add an optional, non-blocking
  **"Test connection"** button to the warehouse step and the AI step that calls
  the new **project-less** endpoints with the current form config (no project
  needed — satisfies "first-time create can Save-and-Test without a prior save").
  The final "Create Project" (`:459`) is unchanged and never blocked by a test.
- **Non-blocking warning state**: a small per-section piece of state
  (`'unverified' | 'failed:<reason>' | 'ok'`) rendered as a persistent Mantine
  `Alert`/`Text` under the section. It does **not** gate Save / Next / Create.

### Enterprise (companion PR)
- `enterprise/ui/src/components/projects/WarehouseConfigPanel.tsx` — remove its
  `WarehouseTestButton` render (`:188`) + component (`:277`); add Save / Save &
  Test mirroring the platform panel.
- `enterprise/ui/src/app/projects/[id]/data-sources/page.tsx:377`
  (`WarehouseTestButton`, multi-warehouse) — same treatment; per-datasource
  test uses `?warehouse_id=` (project-scoped) after save, or the project-less
  endpoint for the form.
- `enterprise/ui/src/lib/warehouses-api.ts:127-131` — align test call shape.
- `enterprise/ui/src/app/projects/new/page.tsx:232` — optional form-value test
  as in platform.

### Acceptance mapping
- No standalone "Test" anywhere in DB/LLM/embedding/blurb config → removed from
  all edit panels + enterprise data-sources.
- Save persists without testing; Save & Test persists then tests.
- Always proceed on no/failed test; persistent non-blocking warning on the
  section.
- Testing validates the **current form** — create via project-less endpoint;
  edit via save-then-test (the saved state *is* the form after Save).
- Applies to community + enterprise warehouses (incl. multi-warehouse) and
  analysis/embedding/blurb models.

---

## Problem 3 — Scrollable Run Discovery popover

### Approach
Constrain the analysis-area list height and make it scroll, keeping the run
options header and "Run Selected/All" footer fixed — mirroring the existing
`ScrollArea.Autosize` pattern at
`ui/dashboard/src/components/lists/AddToListMenu.tsx:130`. `ScrollArea` is
already imported in the project-home page (`page.tsx:7`).

### Platform
`ui/dashboard/src/app/projects/[id]/page.tsx`: wrap only the analysis-area
`Menu.Item` list (`:258-266`) in `<ScrollArea.Autosize mah={320}>` so the
Exploration/Minimum-steps inputs, Estimate checkbox, "Run All Areas", and the
"Run Selected" footer stay put while the area list scrolls. (The `Menu width=
{280}` at `:206` and `Menu.Dropdown` at `:224` otherwise unchanged.)

### Enterprise (companion PR — duplicated copy, not an overlay)
`enterprise/ui/src/app/projects/[id]/page.tsx` — same wrap around its area list
(`:302-310`); `Menu` `:250`, `Menu.Dropdown` `:268`.

### Acceptance mapping
- Full area list reachable by scrolling inside the popover on a short viewport;
  no browser zoom-out.
- Run options + Run Selected/All stay usable (list scrolls, they don't).
- Fixed in both platform and enterprise copies.

---

## Files to change

### Platform (this PR)
| Area | File | Change |
|---|---|---|
| P1 | `ui/dashboard/src/components/common/MaskedSecretInput.tsx` | **New** shared masked input + `credentialIsMultiline` helper |
| P1 | `ui/dashboard/src/components/projects/WarehouseFormFields.tsx` | Credential slot + `DynamicField` credential guard |
| P1 | `ui/dashboard/src/components/projects/LLMFormFields.tsx` | Credential slot |
| P1 | `ui/dashboard/src/components/ProviderCredentialsPhase.tsx` | Credential slot (covers embedding + blurb) |
| P1 | `ui/dashboard/src/components/common/LLMModelField.tsx` | `DynamicField` credential guard |
| P2 | `services/api/internal/handler/providers.go` | 3 project-less `…/test` handlers (in-process provider build) |
| P2 | `services/api/apiserver/apiserver.go` | Register the 3 new routes |
| P2 | `ui/dashboard/src/lib/api.ts` | `test{Warehouse,LLM,Embedding}Config` |
| P2 | `ui/dashboard/src/components/projects/WarehouseConfigPanel.tsx` | Remove Test btn; Save + Save&Test; non-blocking warning; delete `TestConnectionButton` |
| P2 | `ui/dashboard/src/components/projects/ProvidersPanel.tsx` | Remove Test btns/import; Save + Save&Test |
| P2 | `ui/dashboard/src/app/projects/[id]/settings/page.tsx` | Blurb Save + Save&Test |
| P2 | `ui/dashboard/src/app/projects/new/page.tsx` | Optional form-value Test buttons (project-less) |
| P3 | `ui/dashboard/src/app/projects/[id]/page.tsx` | `ScrollArea.Autosize` around area list |
| Docs | `CHANGELOG.md` | `[Unreleased]` entries for the three fixes |

### Enterprise (companion bridge PR — not editable from this container)
P1: `finetuning/ProviderConfigForm.tsx`; verify overlaid `WarehouseConfigPanel`
+ Oracle/HANA. P2: `projects/WarehouseConfigPanel.tsx`,
`app/projects/[id]/data-sources/page.tsx`, `lib/warehouses-api.ts`,
`app/projects/new/page.tsx`; confirm enterprise API registers Oracle/HANA
in-process (for B2). P3: `app/projects/[id]/page.tsx` (duplicate). Remove any
`.platform-ref` pin so enterprise CI builds against the platform bridge branch.

---

## Build phases

1. **P1** — new `MaskedSecretInput` + helper; wire the 3 credential slots + 2
   `DynamicField` guards. `make test-ui lint-ui`, visual check via the local
   dashboard image build (enterprise overlay validation pattern).
2. **P2 backend** — 3 project-less `…/test` handlers + routes; unit tests.
   `make build test-go lint-go`.
3. **P2 frontend** — `api.ts` helpers; Save / Save&Test in both panels + blurb;
   remove standalone Test buttons + `TestConnectionButton`; wizard form-test
   buttons; non-blocking warning UI.
4. **P3** — `ScrollArea.Autosize` wrap.
5. **Docs** — CHANGELOG. Full local check suite, then open the (non-draft) PR
   and run the review loop.

---

## Data / schema / API / UI impacts
- **No schema/model changes.** No secret storage change (out of scope — storage
  already encrypts). No agent/runner change (B2).
- **New API:** 3 additive project-less `POST …/{id}/test` routes. Existing
  project-scoped test routes unchanged and still used by the blurb path.
- **UI:** credential inputs masked; per-section Save/Save&Test; scrollable
  popover. No route/nav changes.

---

## Test strategy (Rule 9 — failure + edge cases, not just happy path)

**Frontend (Jest, `make test-ui`)**
- `MaskedSecretInput`: single-line renders `PasswordInput` masked by default,
  toggle reveals; multi-line renders masked textarea, toggle reveals; `onChange`
  round-trips; `autoComplete` set. `credentialIsMultiline`: "Service Account
  JSON" → true, "API Key" / "Access Key ID : Secret Access Key" → false,
  value-with-newline → true.
- Panels: standalone Test button gone; **Save** does not call any test endpoint;
  **Save and Test** calls save then the test endpoint; a **failed** test still
  persists and shows a non-blocking warning while leaving Save/Next enabled;
  "leave empty to keep current" preserved for a saved credential (no secret
  write when the field is blank).
- Popover: area list wrapped in a bounded scroll container; header + Run
  Selected/All remain outside it.

**Backend (Go, `make test-go`)**
- New handlers: success (valid config → `success:true`), provider-build failure
  (bad config → `success:false` + error, HTTP 200), unknown provider id → 400,
  malformed body → 400, upstream/connect failure surfaced as
  `success:false`. Table-driven, per target (warehouse/llm/embedding).

**Integration (Rule 9 — real, testcontainers; `run-integration-tests` label)**
- Warehouse `…/test` against a Postgres testcontainer: good creds → success;
  wrong password → `success:false` with the driver error (this is the real
  "test the form" path that a first-time create exercises).
- LLM/embedding `…/test` env-var-gated with real keys where available
  (mirrors existing `providers_embedding_live_test.go` gating); skipped
  otherwise.

**Manual (local dashboard image, enterprise-overlay validation memory)**
- Create + edit: every credential field masked by default with a working toggle
  (Postgres password, BigQuery SA JSON, OpenAI key, Bedrock AWS keys); Save vs
  Save&Test behavior; failed test is non-blocking; Run Discovery popover scrolls
  on a short viewport.

---

## Risks & mitigations
- **B2 vs Can's "→ agent" wording** — flagged for sign-off above; default build
  is B2, switchable to B1 if he prefers. *(Primary open item.)*
- **Warehouse HealthCheck in the API process (B2)** — reachability parity is
  fine in compose/K8s; governance is irrelevant to a no-SQL ping. Documented.
- **`-webkit-text-security` browser support** — Chromium/Safari/Firefox 117+;
  universal by 2026. Single-line path uses Mantine `PasswordInput` (no reliance
  on it). If a reviewer objects, the multi-line path can fall back to a
  raw↔dots swap.
- **Enterprise Oracle/HANA in-process registration (B2)** — confirm on the
  bridge branch that the enterprise API blank-imports them (near-certain, since
  the enterprise dashboard already lists them). If not, register them (one blank
  import each) or fall back to B1 for enterprise warehouses.
- **Cross-repo lockstep** — platform + enterprise land together on paired bridge
  branches; enterprise `.platform-ref` pin removed so its CI builds against the
  platform branch. Jale runs one repo per issue, so the enterprise PR is a
  documented companion (flagged to Can).
- **Removing `TestConnectionButton`** — it is exported and imported across two
  files; grep-verify no other importer before deletion (Rule 6).

## Alternatives considered
- **P2 Option A** (Save-then-existing-Mongo-test only): works for edit but the
  create wizard has no persisted project, so it needs a draft-persist first.
  Rejected per Can + issue (Option B).
- **P1 backend multi-line flag** (add `Multiline`/`Format:"json"` to
  `ConfigField`): cleaner signal but a cross-cutting backend + every-provider +
  marshaling + enterprise change for a UI-only concern. Rejected — UI heuristic
  keeps the backend untouched (Rule 8; issue says no backend change needed).
- **P1 single masked textarea for everything** (`-webkit-text-security` only):
  fewer moving parts but ignores the issue's explicit `PasswordInput` request
  for single-line secrets and leans entirely on a CSS quirk. Rejected in favor
  of the hybrid.
- **P3 `styles={{ dropdown: { maxHeight:'70vh', overflowY:'auto' } }}` on
  `Menu`**: simpler but scrolls the whole dropdown (options header + footer move
  too). `ScrollArea.Autosize` around just the list is the better UX and matches
  the in-repo `AddToListMenu` precedent.

## Out of scope
Redesigning the Run Discovery popover into a modal (only make it scrollable);
changing which secrets are stored/encrypted (storage already encrypts — this is
purely input rendering).

---

**This is a PLAN for review — no implementation is included. Implementation
follows after approval.**

Closes #351

— Co-coded with Jale 🤖
