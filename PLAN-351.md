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

## P2 backend approach — DECIDED: thread the form params to the agent (Option B)

Per Can (issue + Slack): **the API must never open a DB connection** — that is
the agent's job and stays that way (invariant #5 above). So the connection test
keeps running in the agent; we feed it the **form** instead of the saved Mongo
record. This is Can's Option B, done in the agent.

**Design.** The test endpoint reads the current connection params from the
request body and passes them to the agent through the runner. The agent, when a
test payload is present, builds the provider **from the payload** and skips the
`projectRepo.GetByID` load (`agentserver.go:502`) — so create (no project yet)
and edit behave identically. Nothing about the API's no-DB posture changes.

**One remaining sub-decision — how the credential reaches the agent process**
(the whole issue is about not exposing secrets, so this matters). The runner
passes only `ProjectID` + CLI `Args` today (`runner.go:72`); delivery must not
put the secret in a place it can leak:

| Delivery | subprocess | docker | k8s | Verdict |
|---|---|---|---|---|
| CLI args | visible in `ps` | `docker inspect` | Job spec + etcd | ✗ never for a secret |
| Env var (plain) | ok (local) | ok (local) | ⚠ in Job spec + etcd (~60s TTL) | simplest; first time a customer secret sits in a manifest |
| **Sensitive-env, Secret-backed in k8s** | env | env | one-shot K8s `Secret` via `secretKeyRef`, owned by the Job (GC'd) | ✅ recommended — one agent read-path, plaintext never in the Pod spec |
| stdin | clean | needs attach | can't stream to a Job | good locally, breaks k8s |

**Default in this plan: the Secret-backed sensitive-env channel.** Extend
`RunSyncOptions` with the test payload split into non-secret config (safe in a
JSON arg) and a sensitive-env map (the credential + any secret sub-fields). Each
runner delivers the sensitive-env map: subprocess/docker set it as process env;
the **k8s runner** creates a short-lived `Secret`, references it via
`env.valueFrom.secretKeyRef`, and sets an `ownerReference` to the Job so it is
garbage-collected with the Job. The agent reads one env var regardless of runner.
*(If Can prefers plain env for simplicity, drop the k8s Secret step — one-line
change; flagged for his pick.)*

The rest of the plan assumes this agent-based Option B.

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

### Approach (agent-based Option B)
The test always runs in the agent (the API never opens a DB). The test endpoint
accepts the current form params in the request body and threads them to the
agent; the agent builds the provider from the payload and connects. This tests
the **form** directly — no "save first" requirement — so create (no project) and
edit are identical. Testing never blocks: a failed/absent test still persists (on
Save) and leaves a persistent non-blocking warning on the section.

### Backend (platform)
Threading, from browser → API → runner → agent:

1. **API handler** — `test_connection.go:runTest` (`:51`) reads the request body
   `{ provider, config, credential, datasets?, warehouse_id? }` (the credential
   under its auth-method field key). When a body is present it forwards the
   params to the runner as a **test payload**; when absent it falls back to
   today's behavior (test the saved project) so existing callers/the internal
   flows keep working. The project-scoped route
   (`POST /api/v1/projects/{id}/test/{target}`) is reused; a **project-less**
   variant (`POST /api/v1/providers/{warehouse|llm|embedding}/test`) is added for
   the create wizard, where there is no project id yet. Both hand the same
   payload to the runner.
2. **Runner** — extend `RunSyncOptions` (`runner.go:72`) with the test payload:
   a **non-secret config** part (provider, host, dataset, region, auth_method →
   marshalled into a JSON CLI arg) and a **sensitive-env** map (credential + any
   secret sub-fields). Each runner delivers the sensitive-env map without leaking
   it: `subprocess.go`/`docker.go` set it as process env; `kubernetes.go` creates
   a short-lived `Secret`, references it via `env.valueFrom.secretKeyRef`, and
   sets an `ownerReference` to the Job so it is GC'd with the Job. (See the P2
   decision table for the plain-env fallback if Can prefers it.)
3. **Agent** — `runTestConnection` (`agentserver.go:481`): when the test payload
   is present, build the warehouse/LLM/embedding provider **from the payload**
   (provider id + non-secret config + credential from the sensitive env var) and
   run the existing `HealthCheck` / `Validate` / `Embed(["ping"])` checks —
   **skipping** the `projectRepo.GetByID` load (`:502`) and the
   `project.WarehouseByID` lookups (`:519-523`). The `--project-id` requirement is
   relaxed for payload-driven test mode (create has no project). When no payload
   is present, the current saved-project path is unchanged (back-compat; still
   used by the blurb path). The provider-init helpers
   (`initWarehouseProvider`/`initLLMProvider`/`initEmbeddingProvider`) are
   refactored to accept an explicit config+credential source so both the saved
   and payload paths share one code path.

Result: the agent connects using the **form** values; the API still never opens a
DB; create and edit use one mechanism.

### Frontend (platform)
- **`lib/api.ts`**: extend `testWarehouse/testLLM/testEmbedding/testBlurbLLM` to
  send the current form params in the body; add project-less
  `test{Warehouse,LLM,Embedding}Config(providerId, config)` for the create
  wizard.
- **`WarehouseConfigPanel.tsx`**:
  - Remove the standalone `<TestConnectionButton>` render (`:161`).
  - Replace the single "Save …" button (`:163-167`) with **Save** and **Save and
    Test**. "Save" = existing `handleSave`. "Save and Test" = save then test the
    form (`api.testWarehouse(projectId, formParams)`); on `!success`/throw keep a
    persistent non-blocking warning on the section
    ("⚠ Last test failed: <reason>" / "⚠ Connection not verified"), never block.
  - Delete the now-unused `TestConnectionButton` (Rule 6). It is defined here and
    imported only by `ProvidersPanel.tsx:17` (grep-verified), which also drops it.
- **`ProvidersPanel.tsx`**:
  - Remove the LLM (`:310-312`) and embedding (`:330-332`)
    `<TestConnectionButton>` renders and the `TestConnectionButton` import
    (`:17`).
  - Replace the single "Save providers" button (`:334-338`) with **Save** +
    **Save and Test** (tests LLM + embedding with the form params after save;
    aggregates results into per-section non-blocking warnings).
- **Blurb** (`app/projects/[id]/settings/page.tsx`, `saveBlurb:234`): add "Save
  and Test" alongside "Save blurb model" (`:335`) using
  `api.testBlurbLLM(projectId, formParams)`; non-blocking warning on failure.
- **Create wizard** (`app/projects/new/page.tsx`): add an optional, non-blocking
  **"Test connection"** button to the warehouse step and the AI step that calls
  the **project-less** endpoints with the current form config (no project needed
  — satisfies "first-time create can Save-and-Test without a prior save"). The
  final "Create Project" (`:459`) is unchanged and never blocked by a test.
- **Non-blocking warning state**: a small per-section piece of state
  (`'unverified' | 'failed:<reason>' | 'ok'`) rendered as a persistent Mantine
  `Alert`/`Text` under the section. It does **not** gate Save / Next / Create.

### Enterprise (companion PR)
- `enterprise/ui/src/components/projects/WarehouseConfigPanel.tsx` — remove its
  `WarehouseTestButton` render (`:188`) + component (`:277`); add Save / Save &
  Test mirroring the platform panel.
- `enterprise/ui/src/app/projects/[id]/data-sources/page.tsx:377`
  (`WarehouseTestButton`, multi-warehouse) — same treatment; per-datasource test
  sends the datasource's form params (or `?warehouse_id=` for a saved one).
- `enterprise/ui/src/lib/warehouses-api.ts:127-131` — send form params in the
  body.
- `enterprise/ui/src/app/projects/new/page.tsx:232` — optional form-value test
  as in platform. No enterprise agent change needed — Oracle/HANA already run in
  the enterprise agent, which is exactly where the payload-driven test executes.

### Acceptance mapping
- No standalone "Test" anywhere in DB/LLM/embedding/blurb config → removed from
  all edit panels + enterprise data-sources.
- Save persists without testing; Save & Test persists then tests.
- Always proceed on no/failed test; persistent non-blocking warning on the
  section.
- Testing validates the **current form** (payload → agent) in both create and
  edit — a first-time create Save-and-Tests without a prior save.
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
| P2 | `services/api/internal/handler/test_connection.go` | Read form params from body; forward as a test payload; add project-less variant |
| P2 | `services/api/internal/runner/runner.go` | Extend `RunSyncOptions` with test payload (non-secret config + sensitive-env) |
| P2 | `services/api/internal/runner/{subprocess,docker,kubernetes}.go` | Deliver the payload; k8s backs sensitive-env with a Job-owned `Secret` |
| P2 | `services/api/apiserver/apiserver.go` | Register the project-less `…/test` route(s) |
| P2 | `services/agent/agentserver/agentserver.go` | `runTestConnection` builds provider from payload (skips Mongo load); refactor init helpers to share one config source |
| P2 | `ui/dashboard/src/lib/api.ts` | Send form params in test bodies; add project-less `test*Config` |
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
`app/projects/new/page.tsx` — send form params in the test body. **No enterprise
agent change** — Oracle/HANA already run in the enterprise agent, which is where
the payload-driven test executes. P3: `app/projects/[id]/page.tsx` (duplicate).
Remove any `.platform-ref` pin so enterprise CI builds against the platform
bridge branch.

---

## Build phases

1. **P1** — new `MaskedSecretInput` + helper; wire the 3 credential slots + 2
   `DynamicField` guards. `make test-ui lint-ui`, visual check via the local
   dashboard image build (enterprise overlay validation pattern).
2. **P2 backend** — handler body-parsing; `RunSyncOptions` payload + per-runner
   delivery (incl. k8s Job-owned Secret); agent payload path + init-helper
   refactor; unit tests. `make build test-go lint-go`.
3. **P2 frontend** — `api.ts` helpers; Save / Save&Test in both panels + blurb;
   remove standalone Test buttons + `TestConnectionButton`; wizard form-test
   buttons; non-blocking warning UI.
4. **P3** — `ScrollArea.Autosize` wrap.
5. **Docs** — CHANGELOG. Full local check suite, then open the (non-draft) PR
   and run the review loop.

---

## Data / schema / API / UI impacts
- **No schema/model changes.** No secret storage change (out of scope — storage
  already encrypts). **The API still opens no DB connection** — the test runs in
  the agent as before.
- **API:** the project-scoped test routes now accept an optional body of form
  params (back-compat when omitted); one additive project-less `…/test` route
  for the create wizard. `RunSyncOptions` gains a test-payload field.
- **Agent:** `runTestConnection` gains a payload-driven path; the saved-project
  path is unchanged.
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
- Handler: body present → payload forwarded to the runner (mock runner asserts
  the non-secret config + sensitive-env split, and that **no secret appears in
  argv**); body absent → legacy saved-project path; malformed body → 400.
- Runner: `RunSyncOptions` payload delivered correctly per mode — subprocess/
  docker set the sensitive env; the **k8s** runner creates the Job-owned Secret
  and references it via `secretKeyRef` (fake clientset asserts the Secret exists,
  has an `ownerReference` to the Job, and that the credential is **not** in the
  Pod container args/plain env).
- Agent `runTestConnection`: payload path builds the provider from the payload
  and skips the Mongo load (unit test with a payload + no project row → still
  attempts connect); missing/blank credential → clear error; back-compat saved
  path unchanged.

**Integration (Rule 9 — real, testcontainers; `run-integration-tests` label)**
- End-to-end warehouse test against a Postgres testcontainer via the
  subprocess runner: good form creds → success; wrong password →
  `success:false` with the driver error (the real "test the form, no prior save"
  path a first-time create exercises).
- LLM/embedding payload test env-var-gated with real keys where available
  (mirrors existing `providers_embedding_live_test.go` gating); skipped
  otherwise.

**Manual (local dashboard image, enterprise-overlay validation memory)**
- Create + edit: every credential field masked by default with a working toggle
  (Postgres password, BigQuery SA JSON, OpenAI key, Bedrock AWS keys); Save vs
  Save&Test behavior; failed test is non-blocking; Run Discovery popover scrolls
  on a short viewport.

---

## Risks & mitigations
- **Credential delivery to the agent** — the one open sub-decision (P2 table).
  Default = sensitive-env backed by a Job-owned k8s `Secret` so the plaintext
  never sits in a Pod spec; plain-env is the simpler fallback if Can accepts the
  transient (~60s TTL) Job-spec exposure. Either way no secret goes in argv.
- **Agent init-helper refactor** — `initWarehouseProvider`/`initLLMProvider`/
  `initEmbeddingProvider` are shared with discovery; the refactor to a common
  config source must not change the saved-project (discovery) path. Covered by
  keeping the saved path as the default and unit-testing both branches.
- **`-webkit-text-security` browser support** — Chromium/Safari/Firefox 117+;
  universal by 2026. Single-line path uses Mantine `PasswordInput` (no reliance
  on it). If a reviewer objects, the multi-line path can fall back to a
  raw↔dots swap.
- **Cross-repo lockstep** — platform + enterprise land together on paired bridge
  branches; enterprise `.platform-ref` pin removed so its CI builds against the
  platform branch. Jale runs one repo per issue, so the enterprise PR is a
  documented companion (flagged to Can). No enterprise **agent** change needed.
- **Removing `TestConnectionButton`** — defined in `WarehouseConfigPanel.tsx`,
  imported only by `ProvidersPanel.tsx:17` (grep-verified); safe to delete once
  both usages are gone (Rule 6).

## Alternatives considered
- **P2 Option A** (Save-then-existing-Mongo-test only): works for edit but the
  create wizard has no persisted project, so it needs a draft-persist first.
  Rejected per Can + issue (Option B).
- **P2 "test in-process in the API"** (build the provider + `HealthCheck` in the
  API): the smallest code change and reuses the "Load models" pattern, but it
  would open a **warehouse/DB connection in the API** — which the API never does
  today (invariant #5). Rejected by Can to keep DB connections in the agent.
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
