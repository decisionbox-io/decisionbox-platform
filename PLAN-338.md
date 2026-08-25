# Implementation Plan — #338: custom TLS (CA upload + skip-verify), first-class LiteLLM provider, 128K input / 64K output defaults for unknown models

> Status: **PLAN for review.** No implementation in this PR — a human reviews and
> approves the plan, then the build step implements it.

## 1. Problem restatement

An on-prem PoC customer runs **LiteLLM over HTTPS behind a private CA**. Four gaps:

1. **Custom TLS for LLM endpoints.** Every LLM provider builds its HTTP client as
   `&http.Client{Timeout: timeout}` with Go's default transport + system trust
   store. A private-CA endpoint therefore fails with
   `x509: certificate signed by unknown authority`, with no way to trust a custom
   CA or skip verification per-project.
2. **LiteLLM is not a first-class provider.** It only works today as the bare
   `openai` provider with a `base_url` override — no dedicated config form, no live
   model listing, no dispatch-any-model handling.
3. **Unknown-model context default is too small.** The global fallback input
   window is **32K** (`libs/go-common/llm/registry.go:389`), too low for modern
   models. Should be **128K** across all providers.
4. **No end-to-end proof.** Custom SSL, LiteLLM setup, connection tests, and model
   listing need real (not mocked-TLS) coverage.

### Scope addition from Can (Slack, 2026-08-25)

> "I want to increase default **output** for all unknown models as well, we can
> take the reference of **qwen 3.6** model for this."

Qwen 3.6's catalog output cap is **65536** (`providers/llm/ollama/catalog.go:53`).
So this plan **also** raises the unknown-model **output** default to **64K
(65536)** — deliberately overriding the issue's original non-goal ("not changing
the default output cap (8192)"), on Can's explicit direction. See §4.3 and the
risk note in §9 (oversized `max_tokens` can 4xx on hard-cap API providers).

## 2. Key architecture findings (grounding)

These are verified against the current tree; edit sites reference them.

- **Provider registry / metadata:** `libs/go-common/llm/registry.go`.
  - Global fallbacks: `DefaultMaxInputTokens = 32000` (`:389`);
    `MaxOutputTokensFor` returns hardcoded `8192` (`:381`); `GetMaxOutputTokens`
    returns hardcoded `8192` (`:680`).
  - Per-provider knobs on `ProviderMeta`: `DefaultMaxInputTokens`,
    `DefaultMaxOutputTokens`, `DispatchAnyModelID`, `PreferLiveModelID`,
    `EffectiveInputWindow`, `ConfigFields`, `AuthMethods`, `Models`.
  - `ConfigField.Type` is a free-form string the UI switches on
    (`registry.go:451`).
- **Providers build a plain `*http.Client`** at:
  `openai/provider.go:113`, `claude/provider.go:112`, `azure-foundry/provider.go:133`,
  `bedrock/provider.go:163`, `vertex-ai/provider.go:205`,
  `ollama/provider.go:172` (via `ollamaapi.NewClient`).
  - **openai / claude / azure-foundry / vertex-ai / ollama** actually use this
    client for outbound model calls (`p.httpClient.Do(...)` / the ollama SDK).
    vertex-ai uses `p.httpClient.Do` for **all** wires
    (`anthropic.go:90`, `openaicompat.go:74/217`, `google_native.go:105`,
    `list_models.go:108`, `tokens.go`).
  - **bedrock is different:** the `httpClient` struct field
    (`bedrock/provider.go:163,183`) is **never read** — all Bedrock calls go
    through the AWS SDK (`p.client.InvokeModel` in `anthropic.go:62` /
    `openaicompat.go:33`; `bedrockcp.NewFromConfig(p.awsCfg)` in `list_models.go`).
    TLS for Bedrock is managed by the AWS SDK transport. See §4.1 for how we
    handle this faithfully.
- **Timeout resolution** already lives in a shared helper:
  `llm.ResolveHTTPTimeout(cfg, fallback)` (`libs/go-common/llm/timeout.go:39`).
  `HTTPClientFor` (new) mirrors its shape and composes with it.
- **Per-project config reaches every service via `project.LLM.Config`** (a
  `map[string]string`, `services/agent/internal/models/project.go` `LLMConfig`).
  Each service **merges every key of `project.LLM.Config` into the provider cfg**:
  - agent discovery + ask-serve: `initLLMProvider` merge loop
    `services/agent/agentserver/agentserver.go:447-451`.
  - API `/ask` (ask-serve RAG): `createLLMProvider` merge loop
    `services/api/internal/handler/search.go:849-851`.
  - API live-model listing (project-scoped): `ListLiveLLMModelsForProject` merges
    the slot config `services/api/internal/handler/providers.go` (~`:330`).
  - This is the crux of the issue: **anything stored in `project.LLM.Config`
    automatically reaches the spawned agent** (the agent reads the Mongo project
    doc; there is no env forwarding for it). `SSL_CERT_FILE` would **not** reach
    the agent — it is not in `agentForwardedEnvKeys`
    (`services/api/internal/runner/env.go`) and the CA is not in the agent image.
- **Secrets vs plaintext config:** credential fields (`ConfigField.Type ==
  "credential"`) are stored in the secret provider under a **hardcoded key**
  (`llm-credentials`) and re-hydrated at build time via
  `gosecrets.ResolveCredential(..., "llm-credentials", "LLM_API_KEY")`
  (agent `agentserver.go:438`, API `search.go:843`). Non-credential config fields
  are stored **as-is** in `project.LLM.Config`. There is **no** generic
  "any field marked secret → its own secret key" mechanism; each secret key is
  wired by hand at every build site.
- **Dashboard renders provider config generically.**
  `ui/dashboard/src/components/projects/LLMFormFields.tsx:120-130` maps over
  `selected.config_fields` (excluding `model` + `wire_override`) and renders each
  via `DynamicField` (`ui/dashboard/src/components/common/LLMModelField.tsx`).
  `DynamicField` already handles `type: 'textarea'` (`LLMModelField.tsx:75-88`);
  it has **no boolean branch** yet. Non-credential config is saved into
  `project.llm.config`; the credential is saved separately as the `llm-credentials`
  secret (`ProvidersPanel.tsx` `handleSave`). Provider selection is a generic
  `Select` built from provider metadata — **no per-provider tiles/icons**, so a
  new provider needs **zero** bespoke UI.
- **Live models / test connection endpoints:**
  `POST /api/v1/projects/{id}/providers/llm/models/live` and
  `POST /api/v1/projects/{id}/test/llm`. Both build the provider from
  `project.LLM.Config` + resolved secret, so TLS keys flow to them too.
- **Module wiring for a new provider** (mirrors every existing LLM provider):
  `services/agent/go.mod` (require `:15-20` + replace `:223-228`),
  `services/api/go.mod` (require `:15-20` + replace `:248-253`),
  blank imports (`services/agent/agentserver/agentserver.go:42-47`,
  `services/api/apiserver/apiserver.go:44-49`,
  `services/agent/cmd/validation-replay/main.go`),
  Dockerfiles (`services/{agent,api}/Dockerfile:18-23`),
  `Makefile` (`test-go` `:44-49`, `test-llm`, `lint-go`),
  `.github/workflows/ci.yml` (`go-test` `:101-106`).

## 3. Proposed approach (overview)

| Req | Approach |
|---|---|
| 1. Custom TLS | New `llm.HTTPClientFor(cfg, timeout) (*http.Client, error)` helper in `libs/go-common/llm`. Wire it into every provider's client construction. Add two config fields (`tls_ca_cert` textarea, `tls_skip_verify` boolean) to the relevant providers; render the boolean in the dashboard. **Store the CA as a plaintext `project.LLM.Config` field, not a secret** (see decision D-A). |
| 2. LiteLLM | New `providers/llm/litellm` module mirroring `openai` (reuse the `openaicompat` wire), with `DispatchAnyModelID` + `PreferLiveModelID`, live `ListModels` via `GET /v1/models`, the TLS fields, and 128K/64K defaults. |
| 3. Defaults | Global input default 32K→**128K**; global output default 8192→**64K** (Qwen 3.6 ref); audit + document per-provider defaults. |
| 4. Tests | `HTTPClientFor` unit tests via `httptest.NewTLSServer` (no Docker); a real **LiteLLM-over-HTTPS-behind-private-CA** testcontainer integration test proving the full TLS matrix + live listing; a service-wiring test proving `project.LLM.Config` carries the CA into provider construction (the agent's exact path). |

## 4. Detailed design

### 4.1 Req 1 — custom TLS

**New file `libs/go-common/llm/httpclient.go`:**

```go
// Config keys (exported constants — Rule 2, no magic strings).
const (
    TLSCACertKey     = "tls_ca_cert"     // PEM, appended to the system pool
    TLSSkipVerifyKey = "tls_skip_verify" // "true" => InsecureSkipVerify
)

// HTTPClientFor returns the *http.Client an LLM provider should use.
//   - neither key set  => &http.Client{Timeout: timeout}  (unchanged default)
//   - tls_ca_cert set  => RootCAs = SystemCertPool() + appended CA
//   - tls_skip_verify  => InsecureSkipVerify: true
// Returns an error when tls_ca_cert is non-empty but parses to zero certs
// (malformed PEM) so the failure surfaces on Load-models / Test-connection.
func HTTPClientFor(cfg ProviderConfig, timeout time.Duration) (*http.Client, error)

// HasCustomTLS reports whether cfg requests non-default TLS. Used by the
// bedrock factory to decide whether to override the AWS SDK's own client.
func HasCustomTLS(cfg ProviderConfig) bool
```

Behaviour details:
- CA path: `pool, err := x509.SystemCertPool()`; on error or nil, start from
  `x509.NewCertPool()` (append still works; log-free — the library stays pure).
  `pool.AppendCertsFromPEM([]byte(cfg[TLSCACertKey]))`; if it returns `false`,
  return `fmt.Errorf("llm: tls_ca_cert is not a valid PEM certificate")`.
  Set `tls.Config{RootCAs: pool}`.
- skip-verify: `InsecureSkipVerify: true` with an inline
  `//nolint:gosec // G402: operator-opted insecure escape hatch, warned in UI`.
  When **both** are set, skip-verify wins (verification off), matching Go
  semantics; the UI warns.
- Non-default case clones `http.DefaultTransport` and sets `TLSClientConfig`; the
  default case returns the exact same `&http.Client{Timeout: timeout}` as today
  (zero behavioural change when no TLS keys are present).

**Wiring per provider** (replace the `&http.Client{...}` construction):

| Provider | How | Notes |
|---|---|---|
| `openai` | factory builds client via `HTTPClientFor`; add `NewOpenAIProviderWithClient(apiKey, model, baseURL, *http.Client)`, keep `NewOpenAIProvider(..., timeout)` delegating to it | keeps existing constructor tests unchanged |
| `claude` | factory builds client via `HTTPClientFor`; add optional `HTTPClient *http.Client` to `ClaudeConfig` (used when set, else build from `Timeout`) | preserves `NewClaudeProvider` contract |
| `ollama` | factory builds client via `HTTPClientFor`, passes it into `ollamaapi.NewClient(url, client)`; thread through `NewOllamaProvider` (add client param or a `WithClient` variant) | ollama is usually plain HTTP, but a TLS-fronted Ollama now works |
| `azure-foundry` | factory: replace `:133` with `HTTPClientFor` result (handle err) | Foundry custom endpoints can sit behind private CAs |
| `vertex-ai` | factory: replace `:205` with `HTTPClientFor` result (handle err) | used for all wires incl. user-deployed `endpoint_id` |
| `bedrock` | **only when `HasCustomTLS(cfg)`**, set `awsCfg.HTTPClient = <HTTPClientFor result>` before building the SDK clients; **remove the currently-unused `httpClient` struct field** (Rule 6) | leaves the AWS SDK's tuned default transport in place for the normal public-AWS case; custom CA still honoured if ever pointed at a private-CA-fronted proxy |
| `litellm` (new) | built with `HTTPClientFor` from the start | the primary target of this feature |

**New config fields** (added to each provider's `ProviderMeta.ConfigFields`):

```go
{Key: "tls_ca_cert", Label: "Custom CA certificate (PEM)", Type: "textarea",
 Description: "Paste or upload the PEM CA cert for a private-CA HTTPS endpoint. Appended to the system trust store."},
{Key: "tls_skip_verify", Label: "Disable TLS verification", Type: "boolean", Default: "false",
 Description: "INSECURE — skips certificate verification entirely. Prefer uploading a CA above. Use only as a temporary escape hatch on trusted networks."},
```

**Which providers surface the UI fields** (decision D-D): `litellm`, `openai`,
`ollama`, `azure-foundry`, `vertex-ai` — every provider that can point at a
self-hosted / custom HTTPS endpoint. **Not** `claude` (direct `api.anthropic.com`,
no base-URL override) and **not** `bedrock` (public AWS, SDK-managed TLS). The
`HTTPClientFor` helper still honours the keys for any provider whose cfg carries
them, so the mechanism is uniform even where the UI does not surface it.

### 4.2 Req 2 — first-class LiteLLM provider

New module **`providers/llm/litellm/`**, mirroring `providers/llm/openai/`:

- `go.mod` / `go.sum` — module
  `github.com/decisionbox-io/decisionbox/providers/llm/litellm`, `go 1.25.0`,
  `require`/`replace` on `libs/go-common` (copy openai's, minus tiktoken unless
  we add a token counter — we won't initially).
- `provider.go` — `init()` → `RegisterWithMeta("litellm", factory, meta)`:
  - **ConfigFields:** `base_url` (required, placeholder
    `https://litellm.internal:4000`), `model` (`FreeText`), `tls_ca_cert`,
    `tls_skip_verify`.
  - **AuthMethods:** one `api_key` method → `credentials_json` (the LiteLLM
    master/virtual key, sent as `Authorization: Bearer …`). The key is **optional
    at construction** (some LiteLLM proxies run open) — send the header only when a
    key is present. (Divergence from openai, which requires a key.)
  - **`DispatchAnyModelID: true`**, **`PreferLiveModelID: true`** (LiteLLM routes
    any configured model name through one OpenAI-compatible path; the picker must
    save the exact upstream ID).
  - **`DefaultMaxInputTokens: 131072`**, **`DefaultMaxOutputTokens: 65536`**.
  - **`SupportsTools: true`** (reuses the openai-compat wire, which supports
    function calling) — flagged for review as decision D-F.
  - Optional tiny seed catalog omitted initially (rely on live listing + defaults)
    — keeps it lean (Rule 8); can add later if a common set emerges.
  - `Chat`: reuse `openaicompat.BuildRequestBody` + `ParseResponseBody` +
    `ExtractAPIError`, POST `<base>/chat/completions`. `Validate`: `GET
    <base>/models`. Base-URL normalization: trim trailing `/`; if it does not end
    in `/v1`, append `/v1` (LiteLLM serves both, but pinning `/v1` matches the
    OpenAI-compatible surface).
- `list_models.go` — `ListModels` via `GET <base>/models` (LiteLLM's
  OpenAI-compatible endpoint; `/model/info` is a documented alternative). Parse
  `data[].id`; return `[]RemoteModel`. Same shape as `openai/list_models.go`,
  sanitising error bodies with `gollm.SanitizeErrorBody`.
- `catalog.go` — the ctx/token constants used by the meta defaults (or inline).
- Tests (see §6): `provider_test.go`, `list_models_test.go`, `timeout_test.go`,
  `wiring_test.go`, `integration_test.go`, `mock_test.go`.

**Module registration** (the ~8-file provider checklist): add the require+replace
to both `services/{agent,api}/go.mod`, the blank import to
`agentserver.go`, `apiserver.go`, and `cmd/validation-replay/main.go`, the
Dockerfile `COPY` lines, the `Makefile` (`test-go`, `test-llm`, and — because
litellm ships a `go.sum` — the same `go.mod go.sum` COPY form as ollama), and the
`ci.yml` `go-test` coverage line. `go mod tidy` in both services.

**Dashboard:** none required — the provider appears in the generic provider
`Select` and renders its `ConfigFields` automatically. (No Ollama-style bespoke
tile exists; the report confirming this is in §2.)

### 4.3 Req 3 — unknown-model defaults (input 128K, output 64K)

In `libs/go-common/llm/registry.go`:
- `DefaultMaxInputTokens = 32000` → **`131072`**; update the "conservative ~32K"
  comment (`:384-389`) to describe the 128K rationale.
- Add `DefaultMaxOutputTokens = 65536` (new const, "Qwen 3.6 reference" note) and
  replace the two hardcoded `8192` fallbacks — `MaxOutputTokensFor` (`:381`) and
  `GetMaxOutputTokens` (`:680`) — with it (Rule 2). Update the doc comment at
  `:264` ("fall back to the global 8192 default").

**Per-provider audit:**

| Provider | `DefaultMaxInputTokens` | `DefaultMaxOutputTokens` | Action |
|---|---|---|---|
| claude | unset → global | 16384 | input now 128K via global; **output**: raise to 65536 or keep 16384 (decision D-B / risk §9) |
| openai | unset → global | 16384 | same |
| bedrock | unset → global | 16384 | same |
| azure-foundry | unset → global | 16384 | same |
| vertex-ai | unset → global (`vertexEffectiveInputWindow` also falls back to `gollm.DefaultMaxInputTokens`) | 16384 | same; the custom `EffectiveInputWindow` inherits 128K automatically |
| ollama | `ctx128K` (already 128K) | `131072` (already high) | no change |
| litellm (new) | 131072 | 65536 | set at creation |

- **Input:** no per-provider input default is *below* 128K today (only ollama sets
  one, at 128K). Raising the global covers claude/openai/bedrock/azure/vertex,
  which all inherit it. Nothing to lower; no exceptions needed.
- **Output (Can's ask):** the global fallback → 64K covers unregistered providers
  and any provider with no `DefaultMaxOutputTokens`. The five API providers above
  each set their own `16384`, which beats the global. To make "**all** unknown
  models get 64K output" literally true, raise those five to `65536` too — **but**
  that risks a 4xx on hard-cap API models (see §9). **Recommendation:** raise the
  global to 64K now; treat the five API-provider per-model output defaults as a
  **confirm-with-Can** item (D-B). Ollama/LiteLLM (soft `num_predict` / proxy
  ceilings) are safe at 64K+.
- Confirm resolution order is unchanged: `MaxInputTokensFor` / `MaxOutputTokensFor`
  still read catalog → provider default → new global. Catalogued models are
  unaffected (they resolve to their exact caps first).

Repo-wide check done: the only relevant `32000` is `registry.go:389`. The other
`32000`s are Opus per-model **output** caps (`*/catalog.go opus4Max`) and a render
char budget — all unrelated, left alone.

### 4.4 Dashboard changes (custom TLS UI)

- `ui/dashboard/src/components/common/LLMModelField.tsx` — add a
  `field.type === 'boolean'` branch to `DynamicField` rendering a Mantine `Switch`
  bound to `'true'`/`'false'` string values (config is `Record<string,string>`).
  The `tls_ca_cert` textarea already renders via the existing `'textarea'` branch.
  Optional: enhance the CA field with a small "upload .pem" file input that reads
  the file via `FileReader` into the textarea value (satisfies "upload or paste").
- No changes needed to the generic field loop (`LLMFormFields.tsx:120-130`) — new
  fields render automatically; `buildDefaults` seeds `tls_skip_verify: "false"`.
- `ui/dashboard/src/lib/api.ts` — `ConfigField.type` is already `string`; no type
  change. (A `boolean` value is still transported as the string `"true"`/`"false"`
  in `config`.)
- **Insecure warning:** when `tls_skip_verify === 'true'`, render a Mantine
  `Alert` (warning) near the field. Simplest: handle inside the boolean branch or
  adjacent in `LLMFormFields`. (Decision D-C on any deployment-flag gate.)
- **Enterprise overlay:** `LLMModelField.tsx` / `LLMFormFields.tsx` /
  `ProvidersPanel.tsx` are community components; if the enterprise overlay ships
  its own copies, sync them. **Action during build:** grep the enterprise repo
  (not in this container) for overlaid copies before finishing; flag in the PR if a
  sync is owed. (Cannot verify here — enterprise repo absent.)

### 4.5 Where the CA is stored — decision D-A (recommended)

**Recommendation: store `tls_ca_cert` and `tls_skip_verify` as plaintext keys in
`project.LLM.Config`, not as secrets.** Rationale:
- A CA **certificate** is public by definition (it is handed to clients to
  establish trust; it is not a private key). It is not sensitive.
- Plaintext config **flows to the spawned agent automatically** via the
  `project.LLM.Config` merge loops (§2) — which is exactly the issue's stated
  mechanism for "why this reaches the agent." No new secret key, no
  `ResolveCredential` call added at 3+ build sites, no UI "saved-secret" state.
- It renders and persists through the existing generic config path with no new
  plumbing (Rule 8).

This **diverges** from the issue's "stored as a project secret (like `api_key`)"
wording. The alternative (a real secret) is in §10 with its cost. Flagged for Can.

## 5. Files to change (exact edit sites)

**Core (`libs/go-common/llm`):**
- `httpclient.go` **(new)** — `HTTPClientFor`, `HasCustomTLS`, key constants.
- `httpclient_test.go` **(new)** — unit tests (see §6).
- `registry.go` — `:389` input default → 131072; new `DefaultMaxOutputTokens`
  const; `:381` + `:680` output fallback → const; comments `:264`,`:384-389`.
- `registry_test.go` / `registry_input_test.go` — update expectations for the new
  defaults; add cases.

**Providers (client wiring + TLS fields + output default per D-B):**
- `providers/llm/openai/provider.go` (+ `provider_test.go`, `timeout_test.go`)
- `providers/llm/claude/provider.go` (+ tests)
- `providers/llm/ollama/provider.go` (+ tests)
- `providers/llm/azure-foundry/provider.go` (+ tests)
- `providers/llm/vertex-ai/provider.go` (+ tests)
- `providers/llm/bedrock/provider.go` (+ tests; remove dead `httpClient` field)

**New provider module `providers/llm/litellm/`:**
- `go.mod`, `go.sum`, `provider.go`, `list_models.go`, `catalog.go`,
  `provider_test.go`, `list_models_test.go`, `timeout_test.go`, `wiring_test.go`,
  `integration_test.go`, `mock_test.go`.

**Module registration:**
- `services/agent/go.mod`, `services/api/go.mod` (require + replace)
- `services/agent/agentserver/agentserver.go` (blank import),
  `services/api/apiserver/apiserver.go` (blank import),
  `services/agent/cmd/validation-replay/main.go` (blank import)
- `services/agent/Dockerfile`, `services/api/Dockerfile` (COPY go.mod[/go.sum])
- `Makefile` (`test-go`, `test-llm`), `.github/workflows/ci.yml` (`go-test`)

**Dashboard:**
- `ui/dashboard/src/components/common/LLMModelField.tsx` (boolean `Switch`;
  optional CA file-upload; skip-verify warning)
- possibly `ui/dashboard/src/components/projects/LLMFormFields.tsx` (warning Alert
  placement)
- Jest tests for the new field type + warning.

**Docs (Rule 4):**
- `docs/guides/configuring-llm.md` — new **"Custom TLS / private CA"** section
  (CA upload + skip-verify, the model-config fields) + **"LiteLLM"** provider
  section.
- `docs/concepts/providers.md` — add LiteLLM row; note the TLS fields + the 128K/64K
  unknown-model defaults.
- `docs/guides/adding-llm-providers.md` — mention `HTTPClientFor` as the standard
  client builder (so new providers inherit TLS).
- `docs/reference/api.md` — note the new `config_fields` / `boolean` field type if
  the providers response is documented there.
- `README.md` — add LiteLLM to the LLM-provider list.
- `CHANGELOG.md` — `[Unreleased]` entry (Added: LiteLLM provider, custom TLS;
  Changed: unknown-model input 128K / output 64K defaults).
- If a deployment flag is added for skip-verify (D-C):
  `docs/reference/configuration.md` + Helm `values.yaml` comments +
  `docker-compose.yml`.

## 6. Test strategy (Rule 9 — real cases, not just happy paths)

**Unit — `HTTPClientFor` (`libs/go-common/llm/httpclient_test.go`, no Docker):**
- Stand up `httptest.NewTLSServer` (self-signed). Cases:
  - default client (no TLS keys) → request **fails** `x509: unknown authority`
    (proves the gap).
  - `tls_ca_cert = server.Certificate()` PEM → request **succeeds** (CA-append).
  - `tls_skip_verify = "true"` → request **succeeds**.
  - malformed `tls_ca_cert` ("not a pem") → `HTTPClientFor` returns an **error**.
  - neither key → returns a client `== &http.Client{Timeout: timeout}` equivalent
    (default preserved; Timeout honoured).
  - `HasCustomTLS` truth table.

**Unit — registry defaults (`registry_test.go` / `registry_input_test.go`):**
- `GetMaxInputTokens("unregistered", "x") == 131072`; unknown model on a
  registered provider resolves to 131072; catalogued model unchanged.
- `GetMaxOutputTokens` unknown → 65536; catalogued unchanged; per-provider
  override still wins.

**Unit — providers:**
- Each provider: a `*_FactoryWiresTLS` test asserting that a cfg with
  `tls_ca_cert`/`tls_skip_verify` produces a provider whose client verifies
  against a `httptest.NewTLSServer` (reuse the pattern above through the registered
  factory `gollm.NewProvider`), and that a malformed PEM makes the factory error.
  Keep it light per provider (the deep matrix lives in the helper + litellm tests).
- litellm `provider_test.go`: factory validation (base_url required; key optional),
  `Chat` against an `httptest` OpenAI-compat mock, error-body sanitisation,
  timeout wiring, `DispatchAnyModelID`/`PreferLiveModelID` present.
- litellm `list_models_test.go`: parse a `GET /models` OpenAI-shaped body; status
  ≠ 200 → sanitised error.

**Integration — real LiteLLM over HTTPS behind a private CA
(`providers/llm/litellm/integration_test.go`, testcontainers; Docker available):**
- Generate a private CA + server leaf in-test (Go `crypto/x509`/`crypto/tls`),
  write cert+key to a temp dir.
- Run `ghcr.io/berriai/litellm` with `--ssl_keyfile`/`--ssl_certfile` pointed at
  the leaf, and a config using a **`mock_response`** model so no upstream key/egress
  is needed (self-contained). Expose HTTPS on a mapped port.
- Assert the matrix:

  | Case | Expected |
  |---|---|
  | Chat with no CA / no skip | fails `x509: unknown authority` |
  | Chat with `tls_ca_cert` = CA PEM | ✅ succeeds |
  | Chat with `tls_skip_verify=true` | ✅ succeeds |
  | `ListModels` via `/v1/models` through custom TLS | ✅ returns the mock model |
  | Unknown-model budget | `GetMaxInputTokens("litellm","whatever") == 131072` |

- Gate with the same env/Docker guard other integration tests use; add a
  `test-litellm` Makefile target (and fold into `test-llm`).

**Service wiring — "CA reaches the agent" proof:**
- A test (in `services/agent` or an integration test) that builds a `models.Project`
  with `LLM.Config = {tls_ca_cert: <CA PEM>, base_url: <fixture>, model: …}`, runs
  it through `initLLMProvider`, and does a real `Chat` against the TLS LiteLLM
  fixture — proving the **exact path the spawned agent uses** (Mongo project doc →
  `project.LLM.Config` merge → provider). Plus a focused unit assertion that the
  merge loop (`agentserver.go:447-451`) copies `tls_ca_cert` into `llmCfg`.
- **Full discovery run + `/ask` turn against a live warehouse** (issue's matrix
  rows) require the whole agent+warehouse+Mongo harness and are **not**
  CI-runnable cheaply. Plan: cover the LLM-connectivity crux with the provider +
  `initLLMProvider` integration tests above (real TLS proxy), and verify the full
  discovery/`/ask` run **manually** on the Jale dev stack (`jale test`) against the
  LiteLLM+CA fixture. This is called out as a scoping decision, not silently
  dropped.

**Dashboard (`make test-ui`):**
- Jest: `DynamicField` renders a `Switch` for `type: 'boolean'` and round-trips
  `'true'`/`'false'`; the skip-verify warning shows when on; a LiteLLM provider's
  `config_fields` render `tls_ca_cert` + `tls_skip_verify`.

**Local gates before PR:** `make build`, `make test-go`, `make lint-go` (after
`export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`, `make lint-ui`, and the
new `make test-litellm`.

## 7. Phasing (build step order)

1. **Core helper + defaults.** `HTTPClientFor`/`HasCustomTLS` + unit tests;
   registry input/output defaults + tests. Self-contained, no provider churn yet.
2. **Wire TLS into existing providers.** openai → claude → ollama → azure-foundry →
   vertex-ai → bedrock; add the TLS `ConfigFields` to the D-D set; per-provider
   factory tests. `make test-go`/`lint-go` green each step.
3. **LiteLLM module.** Provider + list_models + catalog + unit tests; full module
   registration (go.mod/replace, blank imports, Dockerfiles, Makefile, CI);
   `go mod tidy`; `make build`.
4. **Integration/E2E.** TLS LiteLLM testcontainer + matrix; `initLLMProvider`
   wiring test; `test-litellm` target + CI.
5. **Dashboard.** Boolean field + CA upload + warning; Jest; `make test-ui`/`lint-ui`.
6. **Docs + CHANGELOG.** All of §5's docs.
7. **Verify + review loop.** Full local gates; Codex review rounds; Copilot pass.

## 8. Data / schema / API / UI impact

- **Schema:** none. `project.LLM.Config` is already `map[string]string`; the new
  keys are additive. No migration.
- **API:** `GET /api/v1/providers/llm` gains the `litellm` entry and the new
  `config_fields` (incl. `type: "boolean"`, `type: "textarea"`). Live-models /
  test-connection endpoints unchanged in shape; they now accept the TLS keys via
  the merged config.
- **UI:** one new field type (`boolean`), an optional CA upload affordance, an
  insecure warning, and LiteLLM appearing in the provider dropdown. No layout
  rework.
- **Backwards compatibility:** absent TLS keys ⇒ byte-for-byte the current default
  client. Existing projects are unaffected.

## 9. Risks & mitigations

- **Oversized `max_tokens` 4xx (output-default 64K).** On hard-cap API providers
  (OpenAI/Claude/Bedrock/Azure/Vertex) an unknown model whose real output cap is <
  64K may reject `max_tokens=65536`. Ollama/LiteLLM treat it as a soft ceiling.
  *Mitigation:* raise the **global** output default (covers unregistered/unknown
  providers) but keep per-provider API defaults a **Can-confirmed** decision
  (D-B); document any provider kept at 16384 as an explicit exception (allowed by
  the issue's own "document the exceptions" clause).
- **`InsecureSkipVerify` is genuinely insecure.** *Mitigation:* prominent UI
  warning; recommend CA upload as the secure path; `//nolint:gosec` with rationale;
  optional deployment-flag gate (D-C).
- **Bedrock SDK transport.** Overriding `awsCfg.HTTPClient` unconditionally would
  replace the SDK's tuned retry/timeout transport. *Mitigation:* override **only**
  when `HasCustomTLS(cfg)` (public-AWS default path untouched).
- **CA visible in the project doc / API responses.** Because it is plaintext config
  (D-A). *Mitigation:* acceptable — a CA cert is public. If Can prefers it hidden,
  switch to the secret approach (§10).
- **LiteLLM testcontainer image pull / HTTPS startup flakiness in CI.**
  *Mitigation:* self-contained `mock_response` config (no upstream/egress), health
  wait on the HTTPS port, Docker/env guard consistent with other integration
  targets; helper-level `httptest` unit tests carry the TLS matrix even if the
  container is unavailable.
- **Enterprise dashboard overlay drift** (if it copies the edited components).
  *Mitigation:* check + sync during build; flag on the PR.

## 10. Alternatives considered

- **CA as a real project secret** (issue's literal wording). Requires a new secret
  key (e.g. `llm-tls-ca-cert`) plus `ResolveCredential` wiring at **every** build
  site (agent `initLLMProvider`, API `createLLMProvider`, project live-models
  handler, blurb-LLM paths), UI "saved-secret" state, and env-fallback plumbing —
  substantial surface for a value that is not sensitive. Rejected in favour of
  D-A; re-open if Can wants the CA kept out of the project doc.
- **Env-var `SSL_CERT_FILE` / baking the CA into images.** The issue's own analysis
  rules this out: it does not reach the spawned agent (not in
  `agentForwardedEnvKeys`, CA not in the agent image) and needs a rebuild per
  customer. It remains only as the documented *immediate stopgap* for the current
  PoC, which this feature replaces.
- **LiteLLM as an `openai` alias** (status quo: `openai` + `base_url`). No live
  listing, no dispatch-any-model, no dedicated form — fails the parity acceptance
  criterion. Rejected.
- **A `tls.Config` factory returning a transport** instead of a full client. More
  churn at call sites (each still builds the client) for no gain; a client-returning
  helper matches how providers already construct clients.

## 11. Open decisions for Can (defaults chosen; change on review)

- **D-A — CA storage:** *plaintext `project.LLM.Config` field* (recommended) vs a
  real secret. Plan assumes plaintext.
- **D-B — output-default scope:** global → 64K only, **or** also raise the five API
  providers' per-model output defaults 16384 → 64K (literal "all unknown models",
  with the 4xx risk in §9). Plan assumes **global now + confirm the five**.
- **D-C — skip-verify gating:** UI warning only (recommended) vs also a deployment
  env flag (e.g. `LLM_TLS_ALLOW_SKIP_VERIFY`, mirroring the recent
  `file_ingestion_enabled` flag) that disables the escape hatch. Plan assumes
  warning-only, flag optional.
- **D-D — TLS UI field set:** litellm/openai/ollama/azure-foundry/vertex-ai
  (recommended) vs all providers. Plan assumes the five.
- **D-F — LiteLLM `SupportsTools`:** `true` (openai-compat parity, recommended) vs
  `false` (safe default). Plan assumes `true`.
- **E2E depth:** provider + `initLLMProvider` TLS integration tests in CI, with the
  full discovery/`/ask` run verified manually on the Jale stack (recommended), vs
  building a full agent+warehouse harness in CI. Plan assumes the former.

---

*This is a **PLAN for review** — no implementation is included in this PR.
Implementation follows after approval. — Co-coded with Jale 🤖*

Closes #338
