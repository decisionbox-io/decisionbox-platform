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

5. **The API never opens a warehouse/DB connection — connections are the
   agent's job alone (confirmed, architectural invariant to preserve).**
   `grep` of `services/api` (non-test) finds **no** `gowarehouse.NewProvider`
   or any warehouse client construction; the only warehouse import use is the
   metadata *listing* (`ListWarehouseProviders`) and *pricing*. The only DB/
   network `Ping`/`HealthCheck` calls in the API are MongoDB
   (`database/health.go:19`), the Mongo-backed schema cache
   (`schema_index.go:449` → `SchemaCacheRepository` Distinct), Qdrant
   (`backfill/embeddings.go:504`, `apiserver.go:423`), and the Docker engine
   (`runner/docker.go:119`) — none are a warehouse. (The LLM/embedding "Load
   models" endpoints build providers in-process but make **HTTP** calls to the
   model APIs, not DB connections.) **The connection test therefore stays in the
   agent** — the API must not gain a warehouse connection. This decides the P2
   backend approach below.

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

## P2 backend approach — DECIDED: real draft projects (test the saved draft)

Per Can (issue + Slack), two constraints govern P2:
1. **The API must never open a DB connection** — that stays the agent's job
   (invariant #5). So the connection test keeps running in the **agent, via the
   existing `POST /projects/{id}/test/{target}` path, unchanged.**
2. **Test the current form, and do it properly** — via a **real draft project**
   that starts **no indexing and no side effects** until the user activates it.

**Why a draft (not agent-threading, not test-in-process).** A draft is a real
persisted project, so the existing agent test reads its saved config + secret
from Mongo and connects exactly as discovery would — no new test endpoint, **no
transient credential, no runner/agent/secret-delivery machinery.** "Test before
Save fails" disappears because the wizard persists the draft before testing it.

**What `Create` does today that a draft must defer** (`projects.go`): plan-quota
reservations `CheckCreateProject` (`:328`) / `CheckAddDataSource` (`:347`) and
the schema-index enqueue `SetSchemaIndexStatus(pending_indexing)` (`:400`). A
draft skips **all** of these until activation.

**Draft lifecycle**

| Step | Endpoint | Behavior |
|---|---|---|
| Create draft | `POST /projects` `{state:"draft", …}` | Persist; **skip** quota reservation, schema-index enqueue, and telemetry. No side effects. |
| Edit draft | `PUT /projects/{id}` | Drafts are **freely mutable** — bypass the settings route's datasource-edit guard (`:537`); still no indexing while draft. |
| Set draft secret | `PUT /projects/{id}/secrets/{key}` | Existing keys, encrypted — same as any project. |
| **Test** draft | `POST /projects/{id}/test/{target}` | **Existing, unchanged.** Reads the draft's saved config + secret; runs the agent. |
| **Activate** | `POST /projects/{id}/activate` (new) | Validate completeness → run the deferred plan gates → `state=ready` → **now** enqueue schema indexing + telemetry. |
| Discard | `DELETE /projects/{id}` | Existing cascade (drops the draft's secrets too). Wizard calls on cancel. |
| GC | API sweep | Delete drafts older than `PROJECT_DRAFT_TTL_HOURS` (default 24h) + cascade — backstop for abandoned wizards. |

`GET /projects` **excludes** drafts (no dashboard pollution); `GET /projects/{id}`
still returns them (the wizard reads its draft by id). The schema-index worker
only picks up `pending_indexing`, which a draft never has — so no worker change.

**Sub-decisions (defaulted; flagged for Can):** (1) a dedicated
`POST /projects/{id}/activate` rather than overloading `PUT` with a state
transition (keeps the settings route's "no quota / no indexing" contract intact);
(2) draft GC as an API sweep, 24h default TTL. (Community policy is Noop, so quota
timing is a no-op there; in cloud the reservation simply moves create→activate, so
drafts don't consume a slot until activated — no `contract.yaml`/helm change.)

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

### Approach (real draft projects)
The test always runs in the agent through the **existing**
`POST /projects/{id}/test/{target}` path — the API never opens a DB. It tests the
**current form** because the form is persisted first: in edit against the real
project, in create against a **draft** project (a real, side-effect-free project
that indexes/reserves nothing until the user activates it). Testing never blocks:
a failed test still leaves the config saved with a persistent non-blocking
warning on the section.

### Backend (platform) — draft lifecycle
See the "P2 backend approach" section above for the full table. Concretely:

1. **`models/project.go`** — add `ProjectStateDraft = "draft"` + an `IsDraft()`
   helper. `EffectiveState()` already refuses discovery for non-`ready` states,
   so a draft can't run discovery.
2. **`handler/projects.go` `Create` (`:207`)** — accept `state:"draft"`. For a
   draft: **skip** `CheckCreateProject`/`CheckAddDataSource` reservations
   (`:328/347`), the `SetSchemaIndexStatus(pending_indexing)` enqueue (`:400`),
   and `TrackProjectCreated`. A draft is a pure persist.
3. **`handler/projects.go` `Update` (`:455`)** — when `existing.IsDraft()`,
   bypass the datasource-edit guard (`:537`) so the wizard can freely set/replace
   the warehouse; still no indexing while draft.
4. **`handler/projects.go` `Activate` (new, `POST /projects/{id}/activate`)** —
   validate the draft is complete (warehouse + llm + embedding present), run the
   deferred plan gates, set `state=ready`, then enqueue schema indexing +
   telemetry (the create-side side effects, now user-triggered).
5. **`handler/projects.go` `List` (`:412`)** — exclude `state=="draft"` (repo
   query filter) so drafts don't appear on the dashboard. `Get` returns them.
6. **Draft GC** — a periodic API sweep deletes drafts older than
   `PROJECT_DRAFT_TTL_HOURS` (default 24h) via the existing cascade (project +
   secrets). Runs in the existing background-worker wiring in `apiserver.go`.
7. **Test path** — `test_connection.go` + `agentserver.go` **unchanged**. The
   existing project-scoped test reads the draft's saved config + secret and runs
   the agent. No runner/agent/secret change.

### Frontend (platform)
- **`lib/api.ts`**: add `createDraftProject`, `activateProject(id)`; the existing
  `testWarehouse/testLLM/testEmbedding/testBlurbLLM(projectId)` are reused as-is.
- **Create wizard** (`app/projects/new/page.tsx`): create the **draft** when the
  user first needs to test (or on entering the config steps), `PUT`-update it as
  they edit, write its secrets, and **test** it via the existing endpoints. The
  final "Create Project" (`:459`) calls **`activateProject`** (draft→ready →
  indexing). Cancel/leaving the wizard **discards** the draft (`DELETE`); the GC
  sweep is the backstop. Per-step **"Save and Test"** persists to the draft then
  tests; a failed test is non-blocking.
- **`WarehouseConfigPanel.tsx`** (edit, real project):
  - Remove the standalone `<TestConnectionButton>` render (`:161`).
  - Replace the single "Save …" button (`:163-167`) with **Save** and **Save and
    Test**. "Save" = existing `handleSave`. "Save and Test" = save then test
    (`api.testWarehouse(projectId)`); on `!success`/throw keep a persistent
    non-blocking warning ("⚠ Last test failed: <reason>"), never block.
  - Delete the now-unused `TestConnectionButton` (Rule 6) — defined here, imported
    only by `ProvidersPanel.tsx:17` (grep-verified), which also drops it.
- **`ProvidersPanel.tsx`** (edit): remove the LLM (`:310-312`) + embedding
  (`:330-332`) `<TestConnectionButton>` renders and the import (`:17`); replace
  the single "Save providers" button (`:334-338`) with **Save** + **Save and
  Test** (tests LLM + embedding after save; per-section non-blocking warnings).
- **Blurb** (`app/projects/[id]/settings/page.tsx`, `saveBlurb:234`): add "Save
  and Test" alongside "Save blurb model" (`:335`) using
  `api.testBlurbLLM(projectId)`; non-blocking warning on failure.
- **Non-blocking warning state**: a small per-section state
  (`'unverified' | 'failed:<reason>' | 'ok'`) rendered as a persistent Mantine
  `Alert`/`Text`. It does **not** gate Save / Next / Create / activate.

### Enterprise (companion PR)
- `enterprise/ui/src/components/projects/WarehouseConfigPanel.tsx` — remove its
  `WarehouseTestButton` render (`:188`) + component (`:277`); add Save / Save &
  Test mirroring the platform panel.
- `enterprise/ui/src/app/projects/[id]/data-sources/page.tsx:377`
  (`WarehouseTestButton`, multi-warehouse) — Save & Test against the saved
  datasource (`?warehouse_id=`).
- `enterprise/ui/src/lib/warehouses-api.ts:127-131` — unchanged call shape (the
  existing project-scoped test is reused).
- `enterprise/ui/src/app/projects/new/page.tsx:232` — draft create + activate as
  in platform. **No enterprise agent or test-endpoint change** — the draft is a
  normal project the enterprise agent already knows how to test (incl.
  Oracle/HANA). The draft state itself lives in the community model/handlers, so
  enterprise inherits it.

### Acceptance mapping
- No standalone "Test" anywhere in DB/LLM/embedding/blurb config → removed from
  all edit panels + enterprise data-sources.
- Save persists without testing; Save & Test persists then tests.
- Always proceed on no/failed test; persistent non-blocking warning on the
  section.
- Testing validates the **current form** in both create and edit — create tests
  the saved **draft**, edit tests the saved project; a first-time create
  Save-and-Tests against its draft (no separate prior save).
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
| P2 | `services/api/models/project.go` | Add `ProjectStateDraft = "draft"` + `IsDraft()` |
| P2 | `services/api/internal/handler/projects.go` | `Create`: honor `state:"draft"` (skip quota/index/telemetry); `Update`: bypass datasource guard for drafts; **new** `Activate`; `List`: exclude drafts |
| P2 | `services/api/database/*project*` | Repo: `List` filter `state != draft`; draft-GC query (list drafts older than TTL) |
| P2 | `services/api/apiserver/apiserver.go` | Register `POST /projects/{id}/activate`; wire the draft-GC sweep |
| P2 | (new) draft-GC sweep | Periodic delete of stale drafts + cascade; `PROJECT_DRAFT_TTL_HOURS` (default 24h) |
| P2 | `services/api/internal/server/server.go` | Route for `Activate` |
| P2 | `ui/dashboard/src/lib/api.ts` | `createDraftProject`, `activateProject(id)`; reuse existing `test*` |
| P2 | `ui/dashboard/src/components/projects/WarehouseConfigPanel.tsx` | Remove Test btn; Save + Save&Test; non-blocking warning; delete `TestConnectionButton` |
| P2 | `ui/dashboard/src/components/projects/ProvidersPanel.tsx` | Remove Test btns/import; Save + Save&Test |
| P2 | `ui/dashboard/src/app/projects/[id]/settings/page.tsx` | Blurb Save + Save&Test |
| P2 | `ui/dashboard/src/app/projects/new/page.tsx` | Draft create → edit/test → activate on finish; discard on cancel |
| P3 | `ui/dashboard/src/app/projects/[id]/page.tsx` | `ScrollArea.Autosize` around area list |
| Docs | `CHANGELOG.md`, `docs/reference/configuration.md` | Three fixes; new `PROJECT_DRAFT_TTL_HOURS` env var + draft-project note |

### Enterprise (companion bridge PR — not editable from this container)
P1: `finetuning/ProviderConfigForm.tsx`; verify overlaid `WarehouseConfigPanel`
+ Oracle/HANA. P2: `projects/WarehouseConfigPanel.tsx`,
`app/projects/[id]/data-sources/page.tsx`, `app/projects/new/page.tsx` — Save /
Save & Test + draft create/activate in the wizard, reusing the existing test
endpoint (`lib/warehouses-api.ts` unchanged). **No enterprise agent or
test-endpoint change** — the draft state lives in the community model/handlers,
so enterprise inherits it; the enterprise agent already tests Oracle/HANA. P3:
`app/projects/[id]/page.tsx` (duplicate). Remove any `.platform-ref` pin so
enterprise CI builds against the platform bridge branch.

---

## Build phases

1. **P1** — new `MaskedSecretInput` + helper; wire the 3 credential slots + 2
   `DynamicField` guards. `make test-ui lint-ui`, visual check via the local
   dashboard image build (enterprise overlay validation pattern).
2. **P2 backend** — draft state + `Create`/`Update`/`Activate`/`List` handling;
   repo list filter; draft-GC sweep; route wiring; unit + integration tests.
   `make build test-go lint-go`. (Test endpoint + agent are untouched.)
3. **P2 frontend** — `api.ts` draft/activate helpers; wizard draft→test→activate
   →discard; Save / Save&Test in both panels + blurb; remove standalone Test
   buttons + `TestConnectionButton`; non-blocking warning UI.
4. **P3** — `ScrollArea.Autosize` wrap.
5. **Docs** — CHANGELOG. Full local check suite, then open the (non-draft) PR
   and run the review loop.

---

## Data / schema / API / UI impacts
- **Model:** one new `Project.State` value, `"draft"` (the `State` field already
  exists; empty/`ready` unchanged, no migration). No secret-storage change (a
  draft uses the existing encrypted keys; cascade-deleted on discard).
- **API:** one new route `POST /projects/{id}/activate`; `Create` honors
  `state:"draft"`; `List` excludes drafts. **Test endpoint + agent + runner are
  unchanged** — the API still opens no DB connection.
- **Behavior:** for a draft, schema-index enqueue + plan-quota reservation +
  telemetry move from create → **activate** (user-triggered). Cloud plan counting
  stays correct (drafts don't reserve until activated); no `contract.yaml`/helm
  change. One new optional env var `PROJECT_DRAFT_TTL_HOURS` (default 24h).
- **UI:** credential inputs masked; wizard uses a draft; per-section
  Save/Save&Test; scrollable popover.

---

## Test strategy (Rule 9 — failure + edge cases, not just happy path)

**Frontend (Jest, `make test-ui`)**
- `MaskedSecretInput`: single-line renders `PasswordInput` masked by default,
  toggle reveals; multi-line renders masked textarea, toggle reveals; `onChange`
  round-trips; `autoComplete` set. `credentialIsMultiline`: "Service Account
  JSON" → true, "API Key" / "Access Key ID : Secret Access Key" → false,
  value-with-newline → true.
- Panels/wizard: standalone Test button gone; **Save** does not call any test
  endpoint; **Save and Test** saves then tests; a **failed** test still persists
  and shows a non-blocking warning while leaving Save/Next/Create enabled;
  "leave empty to keep current" preserved for a saved credential (no secret write
  when the field is blank); wizard **activates** on finish and **discards** the
  draft on cancel.
- Popover: area list wrapped in a bounded scroll container; header + Run
  Selected/All remain outside it.

**Backend (Go, `make test-go`)**
- Draft `Create`: `state:"draft"` → project persisted with **no** quota
  reservation, **no** `pending_indexing`, **no** telemetry; a non-draft create is
  unchanged (still reserves + enqueues).
- `Update` on a draft: datasource edits allowed (guard bypassed); on a ready
  project the guard still rejects them.
- `Activate`: incomplete draft (missing warehouse/llm/embedding) → 400; complete
  draft → `state=ready` + `pending_indexing` set + plan gates run (over-quota →
  policy error, project stays draft); activating a non-draft → 409/no-op.
- `List`: drafts excluded; `Get` by id returns a draft.
- Draft GC: a draft older than the TTL is deleted with its secrets; a fresh draft
  and any ready project are untouched.

**Integration (Rule 9 — real, testcontainers; `run-integration-tests` label)**
- Mongo-backed: create draft → set warehouse-credentials secret → existing
  `POST /projects/{id}/test/warehouse` against a Postgres testcontainer: good
  creds → success; wrong password → `success:false` with the driver error (the
  real create-time "test the form" path). Then `activate` → assert
  `pending_indexing`. Discard → assert project + secret both gone.
- Draft excluded from `List`; GC removes a stale draft + its secret.

**Manual (local dashboard image, enterprise-overlay validation memory)**
- Create + edit: every credential field masked by default with a working toggle
  (Postgres password, BigQuery SA JSON, OpenAI key, Bedrock AWS keys); Save vs
  Save&Test behavior; failed test is non-blocking; Run Discovery popover scrolls
  on a short viewport.

---

## Risks & mitigations
- **Draft ripples into other reads** — any code that lists/counts projects must
  ignore drafts (dashboard list, cloud plan counting, run summaries). Mitigation:
  filter at the repo `List`; drafts never get `pending_indexing` (worker ignores
  them) and never reserve quota (moved to activate). Audit read sites during
  build.
- **Abandoned drafts leak rows/secrets** — mitigated by wizard discard-on-cancel
  **plus** the GC sweep (TTL). GC uses the existing cascade so secrets go too.
- **Activation partial failure** — plan gate passes but index-enqueue fails, or
  vice-versa: order as gate → `state=ready` → enqueue, and treat a failed enqueue
  as non-fatal (the existing create path already logs-and-continues at `:401`;
  user can Re-index). The project is `ready` and usable regardless.
- **Sub-decisions** — dedicated `/activate` vs `PUT` state-transition; GC TTL
  default. Defaulted above; flagged for Can.
- **`-webkit-text-security` browser support** — Chromium/Safari/Firefox 117+;
  universal by 2026. Single-line path uses Mantine `PasswordInput` (no reliance
  on it). If a reviewer objects, the multi-line path can fall back to a
  raw↔dots swap.
- **Cross-repo lockstep** — platform + enterprise land together on paired bridge
  branches; enterprise `.platform-ref` pin removed so its CI builds against the
  platform branch. Jale runs one repo per issue, so the enterprise PR is a
  documented companion (flagged to Can). No enterprise **agent/test-endpoint**
  change needed.
- **Removing `TestConnectionButton`** — defined in `WarehouseConfigPanel.tsx`,
  imported only by `ProvidersPanel.tsx:17` (grep-verified); safe to delete once
  both usages are gone (Rule 6).
- **Scope** — P2 now adds a real draft-project lifecycle (a feature), larger than
  the issue's original "swap the buttons". Done at Can's explicit direction; P1/P3
  are unchanged.

## Alternatives considered
- **P2 "test in-process in the API"** (build the provider + `HealthCheck` in the
  API): smallest change, reuses the "Load models" pattern — but it opens a
  **warehouse/DB connection in the API**, which the API never does (invariant #5).
  Rejected by Can to keep DB connections in the agent.
- **P2 thread the form params to the agent** (extend the runner with the test
  payload + deliver the credential via a Job-owned k8s Secret): tests the form
  without persisting, but adds runner/agent/secret-delivery machinery and a
  transient credential. Rejected in favor of a real draft — no new test path, no
  transient secret.
- **P2 "save-then-test the real project"** (no draft state): zero backend change,
  but a mid-wizard project would start indexing + count against quota. Rejected —
  Can wants a real draft that does nothing until activated.
- **P2 activation via `PUT {state:"ready"}`** instead of a dedicated `/activate`:
  fewer routes, but overloads the settings route (which by contract does not
  reserve quota or enqueue indexing). Dedicated `/activate` keeps that contract
  clean; flagged for Can.
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
