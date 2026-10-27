# Configuring LLM Providers

A project can use **several providers at once** and pick a **provider and model for each layer** of the product. See [Providers and models per layer](#providers-and-models-per-layer) below; everything after it is per-provider setup, unchanged.

DecisionBox supports seven LLM providers. Cloud providers (Bedrock, Vertex AI, Azure AI Foundry) speak multiple wire formats and dispatch per model through their inline catalog — see [Model catalog and wire formats](#model-catalog-and-wire-formats) below.

Any HTTPS LLM endpoint fronted by a private / internal CA can be trusted without rebuilding an image — see [Custom TLS (private CA)](#custom-tls-private-ca) below.

## Provider Comparison

| Provider | Models | Auth | Best For |
|----------|--------|------|----------|
| **Claude (Anthropic)** | Claude Opus 4.8 / 5 / 5.5, Sonnet 5 / 5.5, Haiku 5.5, and the Claude 4.x line | API key | Best quality. Direct access, simple setup. |
| **OpenAI** | GPT-5, GPT-4.1, GPT-4o, o3, o4-mini | API key | Widely used. Good alternative. |
| **Ollama** | Llama 3.1, Qwen 2.5, Mistral, any GGUF | None (local) | Free, private, no API key needed. |
| **Vertex AI** | Gemini, Claude, Llama MaaS, Qwen MaaS, DeepSeek MaaS, Mistral MaaS | GCP ADC | GCP users. Managed billing, IAM auth. |
| **AWS Bedrock** | Claude, Qwen, DeepSeek, Mistral, Llama | AWS credentials | AWS users. Managed billing, IAM auth. |
| **Azure AI Foundry** | Claude, GPT-5 / GPT-4.1 / GPT-4o, Mistral | API key | Azure users. Managed billing, Azure RBAC. |
| **LiteLLM** | Any model the proxy routes | API key (optional) | Self-hosted OpenAI-compatible gateway; one endpoint for many upstreams. |

## Claude (Direct Anthropic API)

The simplest setup and highest quality results.

### 1. Get an API Key

Sign up at [console.anthropic.com](https://console.anthropic.com) and create an API key.

### 2. Configure in Dashboard

1. Create a project (or edit existing) → select **Claude (Anthropic)** as LLM provider
2. Enter model name: `claude-sonnet-4-6` (recommended) or `claude-opus-4-6` (most capable)
3. Go to **Settings → AI Provider** → set **API Key** to your `sk-ant-...` key

### 3. Model Options

| Model | Quality | Speed | Cost |
|-------|---------|-------|------|
| `claude-opus-5-5` | Highest | Slow | $4/$20 per million tokens |
| `claude-opus-4-8` | Highest | Slow | $5/$25 per million tokens |
| `claude-sonnet-5-5` | High | Fast | $2/$10 per million tokens |
| `claude-sonnet-4-6` | High | Fast | $3/$15 per million tokens |
| `claude-haiku-5-5` | Good | Fastest | $0.10/$0.50 per million tokens (prompts up to 100K tokens; 5x above) |

**Recommendation:** Start with Sonnet for a balance of quality and cost. Use Opus for complex datasets.

Claude Opus 4.8 and the Claude 5 line have a 1M-token context window and 128K max output.
Opus 5.5 and Sonnet 5.5 think on every request and reject a forced tool choice; DecisionBox handles both (see [Prompt caching and Claude 5.x](#prompt-caching-and-claude-5x)).

## OpenAI

### 1. Get an API Key

Sign up at [platform.openai.com](https://platform.openai.com) and create an API key.

### 2. Configure in Dashboard

1. Select **OpenAI** as LLM provider
2. Enter model name: `gpt-4o` (recommended) or `gpt-4o-mini` (cheaper)
3. Go to **Settings → AI Provider** → set **API Key** to your `sk-...` key

## Ollama (Local Models)

Run models locally — free, private, no API key needed. Good for testing and development.

### 1. Install Ollama

```bash
# macOS/Linux
curl -fsSL https://ollama.com/install.sh | sh

# Pull a model
ollama pull llama3.1:70b     # Large, high quality
ollama pull qwen2.5:32b      # Good alternative
ollama pull llama3.1:8b      # Small, fast, for testing
```

### 2. Configure in Dashboard

1. Select **Ollama** as LLM provider
2. Enter model name: `llama3.1:70b`
3. No API key needed

**Note:** Ollama runs on `http://localhost:11434` by default. If running in Docker, use `http://host.docker.internal:11434` or the host network.

### Context window (`num_ctx`) and reasoning models

DecisionBox always sends `truncate=false` on Chat requests, so an
oversize prompt fails fast with a clear error rather than being
silently trimmed. The per-request `num_ctx` is only forwarded when
you set the project's optional **Context window (num_ctx)** field —
otherwise the Ollama server's `OLLAMA_CONTEXT_LENGTH` (or model
default) applies. This stays out of your way on tight-VRAM hosts.

**If you see `context length exceeded` errors from Ollama**: the
prompt-budgeting layer trims to the catalog's published window for
your model, but your Ollama server is configured for a smaller
window than the catalog. Either raise `OLLAMA_CONTEXT_LENGTH` on
the Ollama host to match your needs, or set the project's
**Context window (num_ctx)** field to your server's effective
limit — that value is used both for the per-request `num_ctx`
override and for budgeting, so the two stay aligned.

Two things to know:

- **Memory grows with `num_ctx`.** The Ollama server allocates a KV
  cache sized for `num_ctx` regardless of how much of it the
  current prompt actually uses. A 31B-class model in `bf16` quant
  needs ~67 GB just for weights; adding a 128k context can grow
  resident VRAM by another ~5 GB. If you want the model's full
  architectural window, set the **Context window (num_ctx)** field
  on the project — but verify the host can hold the larger KV
  cache first.
- **Reasoning models burn output budget on hidden thinking.** Gemma
  4, Gemma 3, DeepSeek R1, and Qwen 3 emit a chain-of-thought before
  the answer, and those tokens count against `num_predict`. The
  catalog already raises the output cap to 131072 for these
  families so the answer fits alongside the reasoning; no operator
  action needed. The model's `Message.Thinking` is surfaced on
  `ChatResponse.Reasoning` for callers that want to inspect it.

To explicitly opt out of reasoning on a per-call basis, callers set
`ChatRequest.ReasoningEffort = "off"`. Other documented values:
`"on"`, `"low"`, `"medium"`, `"high"`, and the default (`""`) which
leaves the model's own behavior unchanged. Effort values other than
`"off"` are silently ignored on models the provider reports as
non-reasoning so the request doesn't 400 against an upstream that
rejects `think=true` on a non-thinking model.

#### Enable reasoning (model-agnostic, project setting)

Projects expose an **Enable reasoning** toggle under **Settings →
Advanced** (off by default — behavior is unchanged unless you turn it
on). It is **model-agnostic**: it applies to whatever model the project
uses, on any provider — a reasoning model routed through a LiteLLM
proxy (Kimi, qwen3, DeepSeek-R1), an Ollama thinking model, etc.

When on, discovery (a) gives the model **extra, window-budgeted
exploration output headroom** so a long hidden chain-of-thought doesn't
truncate the step's action (see `EXPLORATION_MAX_OUTPUT_TOKENS`), and
(b) requests reasoning (`ReasoningEffort=on`) on every LLM call.
Providers that wire native thinking act on the request; others ignore
the param and simply benefit from the headroom.

For **Ollama** specifically, native thinking is enabled only after
DecisionBox confirms the model supports it, by reading the model's own
capabilities from `/api/show` (the `thinking` capability) — so a
freshly-pulled `qwen3` / `deepseek-r1` reasons even if it is not in the
built-in catalog, a non-reasoning model silently ignores the request,
and if a server still rejects thinking the request is retried once
without it.

You do not need the toggle for models the catalog already flags as
reasoning — those get the exploration headroom automatically. It exists
so uncatalogued reasoning models can be opted in without a code
release.

### Quality Considerations

Local models are significantly less capable than Claude or GPT-4o for complex data analysis. They work for:
- Testing your setup
- Privacy-sensitive environments
- Development and prompt iteration

For production discoveries, use Claude or GPT-4o.

## Vertex AI (Google Cloud)

Access Gemini, Claude, and third-party Model-Garden models (Llama, Qwen, DeepSeek, Mistral) through Google's managed platform. Uses GCP IAM for authentication (no API keys).

### 1. Prerequisites

- GCP project with Vertex AI API enabled
- Model of choice enabled in [Model Garden](https://console.cloud.google.com/vertex-ai/model-garden)
- Application Default Credentials configured:

```bash
gcloud auth application-default login
# Or use a service account with Vertex AI User role
```

### 2. Configure in Dashboard

1. Select **Vertex AI** as LLM provider
2. Enter model name — examples from the shipped catalog:
   - Gemini: `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.0-flash`
   - Claude: `claude-opus-4-6@20251101`, `claude-sonnet-4@20250514`
   - Llama MaaS: `meta/llama-3.3-70b-instruct-maas`
   - Qwen MaaS: `qwen/qwen3-coder-480b-a35b-instruct-maas`
3. Set provider-specific config:
   - **Project ID**: Your GCP project ID
   - **Location**: Region where the model is enabled (e.g., `us-east5` for Claude, `us-central1` for Gemini, `global` also supported)
   - **Endpoint ID** (optional): leave blank for Model Garden models; set it to target a model you deployed yourself — see [Custom (user-deployed) endpoints](#custom-user-deployed-endpoints)

### 3. No API Key Needed

Vertex AI uses GCP Application Default Credentials (ADC). No LLM API key secret is needed.

### Model Name Format

- **Gemini** uses plain IDs: `gemini-2.5-pro`, `gemini-2.5-flash`
- **Claude-on-Vertex** uses `@` for versioning: `claude-opus-4-6@20251101`, `claude-sonnet-4@20250514`
- **Model Garden MaaS** uses publisher-prefixed IDs: `meta/llama-3.3-70b-instruct-maas`, `qwen/qwen3-coder-480b-a35b-instruct-maas`

The provider looks up the model in the catalog and routes to the correct wire format — you do not need to tell DecisionBox which wire to use.

### Custom (user-deployed) endpoints

The Model Garden MaaS model IDs above (Llama / Qwen / DeepSeek / Mistral) live on Vertex's shared MaaS endpoint, which DecisionBox reaches at:

```
https://{location}-aiplatform.googleapis.com/v1beta1/projects/{project}/locations/{location}/endpoints/openapi/chat/completions
```

For `location=global`, the host is `aiplatform.googleapis.com` with no region prefix (this applies to every Vertex URL shape, including the endpoint path below).

If you deployed a model yourself — a self-fine-tuned Qwen, a quantised Llama variant, a Model Garden one-click deploy, or anything not in Model Garden MaaS — it lives on a **Vertex endpoint** with its own ID. Set the **Endpoint ID** field on the Vertex AI provider config to that ID and DecisionBox routes chat requests to the endpoint instead of the shared MaaS path.

Find the ID under **Vertex AI → Online prediction → Endpoints** in the GCP console, or with:

```bash
gcloud ai endpoints list --region={location} --project={project}
```

When Endpoint ID is set:

- The chat path becomes `.../endpoints/{endpoint_id}/chat/completions` (no `/openapi/` segment).
- The dashboard **hides the Model field** — a deployed endpoint serves its own model, so there is nothing to pick. DecisionBox sends an empty model and the endpoint uses what it has deployed. (Endpoints that validate the model name are uncommon for one-click deploys; if you run one, it must accept an empty model.)
- Authentication is identical to the rest of the provider (GCP ADC or service-account key). No `wire_override` is needed — a deployed endpoint always uses the OpenAI chat-completions wire.

#### Dedicated endpoints are auto-detected

Most deployed endpoints — including every Model Garden one-click deploy — are **dedicated**: Vertex serves them on a per-endpoint DNS name, not the shared `aiplatform.googleapis.com` host, and rejects predictions sent to the shared host. The dedicated DNS embeds an internal identifier that cannot be derived from your project, so DecisionBox looks the endpoint up once (via the Vertex management API, using the same credentials) to discover it, then sends predictions there. That lookup needs the `aiplatform.endpoints.get` permission, which `roles/aiplatform.user` already grants alongside prediction — a service account that can call the model can resolve the DNS. Non-dedicated endpoints are served on the regional `aiplatform.googleapis.com` host. You do not configure any of this — only the Endpoint ID.

The endpoint must:

- Have a model **deployed** and finished provisioning. If it reports no serving DNS yet (no model deployed, or still provisioning), DecisionBox returns a clear error.
- Serve the **OpenAI `/chat/completions` wire** (the standard OpenAI-compatible serving container, e.g. vLLM). Endpoints that speak only Vertex's native `:predict` API are not supported.

Leave Endpoint ID blank to use the shared Model Garden MaaS endpoint with publisher-prefixed model IDs as described above.

#### Example

| Field | Value |
|-------|-------|
| Project ID | `my-gcp-project` |
| Location | `us-central1` |
| Endpoint ID | `mg-endpoint-306f661d-e4c1-4169-8705-92bc60ff2def` |
| Model | *(hidden — not needed)* |

DecisionBox resolves the endpoint's serving host and posts the chat request to `.../endpoints/mg-endpoint-306f661d-e4c1-4169-8705-92bc60ff2def/chat/completions` on that host, letting the endpoint use its own deployed model.

## AWS Bedrock

Access Claude, Qwen, DeepSeek, Mistral, and Llama through AWS's managed platform. Uses AWS IAM for authentication.

### 1. Prerequisites

- AWS account with Bedrock access
- Model access enabled in [Bedrock Model Access](https://console.aws.amazon.com/bedrock/home#/modelaccess)
- AWS credentials configured:

```bash
aws configure
# Or use IAM role / instance profile
```

### 2. Configure in Dashboard

1. Select **AWS Bedrock** as LLM provider
2. Enter model name — examples from the shipped catalog:
   - Claude: `anthropic.claude-sonnet-4-6-v1:0`, `global.anthropic.claude-opus-4-6-v1`
   - Qwen: `qwen.qwen3-next-80b-a3b`
   - DeepSeek: `deepseek.r1-v1:0`
   - Mistral: `mistral.mixtral-8x22b-v1:0`
   - Llama: `meta.llama3-3-70b-instruct-v1:0`
3. Set provider-specific config:
   - **Region**: AWS region (e.g., `us-east-1`)

### 3. No API Key Needed

Bedrock uses AWS credentials (IAM role, env vars, or `~/.aws/credentials`). No LLM API key secret is needed.

### Model Name Format

Bedrock model IDs typically follow `<vendor>.<model>-v<n>`. Some newer regional-inference models use a `us.` or `global.` prefix — use the exact string AWS gives you.

The provider looks up the model in the catalog and routes to the correct wire (Anthropic Messages for Claude, OpenAI /chat/completions for everyone else).

## Timeout Configuration

The default LLM timeout is 300 seconds (5 minutes). For very large prompts (many previous insights, large schemas), you may need more time:

```bash
# In docker-compose or env
LLM_TIMEOUT=600s   # 10 minutes
```

Or set per-project in the dashboard (not yet available — use env var for now).

## Azure AI Foundry

Access Claude, OpenAI GPT, and Mistral models through Microsoft Azure's managed AI platform.
Billing goes through your Azure subscription via the Microsoft Marketplace.

### 1. Create a Foundry Resource

1. Navigate to [ai.azure.com](https://ai.azure.com/)
2. Create a Foundry resource or select an existing one
3. Deploy a model (e.g., `claude-sonnet-4-6`, `gpt-5`, `gpt-4o`) under **Models + endpoints**
4. Copy the endpoint URL and API key from **Keys and Endpoint**

### 2. Configure in Dashboard

1. Select **Azure AI Foundry** as LLM provider
2. Enter the **Endpoint URL** (e.g., `https://my-resource.services.ai.azure.com`)
3. Enter the **deployment name** as Model (e.g., `claude-sonnet-4-6`, `gpt-5`, `gpt-4o`)
4. Go to **Settings → AI Provider** → set **API Key** to your Azure API key

### 3. Available Models

| Model | Deployment Name | Wire |
|-------|----------------|------|
| Claude Opus 4.6 | `claude-opus-4-6` | Anthropic |
| Claude Sonnet 4.6 | `claude-sonnet-4-6` | Anthropic |
| Claude Haiku 4.5 | `claude-haiku-4-5` | Anthropic |
| GPT-5 | `gpt-5` | OpenAI-compat |
| GPT-5 Mini | `gpt-5-mini` | OpenAI-compat |
| GPT-4.1 | `gpt-4.1` | OpenAI-compat |
| GPT-4o | `gpt-4o` | OpenAI-compat |
| Mistral Large 2411 | `mistral-large-2411` | OpenAI-compat |

The provider looks the deployment name up in its catalog (canonical ID, then aliases, then prefix-based family inferrer) and routes to the right wire.

### 4. Authentication

Azure AI Foundry supports API key authentication.
The API key is set per-project via the dashboard's AI Provider settings tab.

For production on AKS, you can also use Entra ID (Azure AD) with managed identity, but this requires custom configuration outside DecisionBox.

## LiteLLM

[LiteLLM](https://github.com/BerriAI/litellm) is an OpenAI-compatible proxy that fronts many upstream models behind one endpoint and one key.
DecisionBox talks to it as a first-class provider — a dedicated config form, live model listing, and dispatch-any-model handling (parity with Ollama).
This is the recommended way to reach a self-hosted gateway, especially on-prem behind a private CA.

### 1. Point at your proxy

1. Select **LiteLLM** as the LLM provider.
2. Enter the **LiteLLM proxy URL** as `base_url` (e.g. `https://litellm.internal:4000`). The OpenAI-compatible `/v1` routes are derived from it: `/v1` is appended unless the URL path already carries a version segment, so a hosted OpenAI-compatible API whose base has a path (e.g. `https://api.example.com/v1/openai`) is used as given.
3. If the proxy requires a master or virtual key, set it as the **LiteLLM key** under **Settings → AI Provider** (sent as a `Bearer` token). Leave it blank for an open proxy.

### 2. Pick a model

Click **Load models** to query the proxy's live `GET /v1/models` list and choose one, or type any model name the proxy is configured to route.
LiteLLM routes any configured model name through one OpenAI-compatible path, so the exact name the proxy exposes is saved and used verbatim.

### 3. Unknown-model budgets

A model that is not in any shipped catalog resolves to the default context/output window — **128K input / 64K output** — so long-form generations do not truncate. See [Model catalog and wire formats](#model-catalog-and-wire-formats).

## Custom TLS (private CA)

An LLM endpoint served over HTTPS behind a **private or internal CA** (a common on-prem setup for LiteLLM, Ollama, an OpenAI-compatible gateway, or a self-deployed Vertex/Azure endpoint) is not trusted by the system certificate store, so DecisionBox would otherwise fail the connection with `x509: certificate signed by unknown authority`.

Two per-project options close that gap. Both live in the project's LLM config, so they reach the API, `/ask`, **and the spawned discovery agent** through the same config the provider is built from — no environment variables, no image rebuild.

| Field | Effect |
|---|---|
| **Custom CA certificate (PEM)** (`tls_ca_cert`) | The pasted / uploaded PEM CA is **appended** to the system trust store for this endpoint. The secure, recommended option. |
| **Disable TLS verification** (`tls_skip_verify`) | Skips certificate verification entirely. **Insecure** — an escape hatch for a trusted network only; prefer uploading the CA. |

### Configure in the dashboard

1. On the LLM provider (LiteLLM, OpenAI, Ollama, Azure AI Foundry, or Vertex AI), open the provider config.
2. Paste the CA certificate into **Custom CA certificate (PEM)**, or click **Load from file…** to upload a `.pem` / `.crt`.
3. Click **Load models** or **Test connection** — a malformed certificate is rejected immediately, and a private-CA endpoint now connects.

The CA certificate is public material (not a private key), so it is stored in the project's LLM config rather than the secret store.
Only enable **Disable TLS verification** when you cannot obtain the CA and the network is trusted; the dashboard shows a warning while it is on.

## Model catalog and wire formats

Every LLM provider declares its catalog inline as `ProviderMeta.Models []ModelEntry`. Each entry carries a **wire format** — the request/response schema the model expects:

| Wire | What it is | Used by |
|---|---|---|
| `anthropic` | Anthropic Messages API (`{messages, system, max_tokens}` → `{content[], stop_reason, usage}`) | Claude direct, Claude on Bedrock, Claude on Vertex, Claude on Azure Foundry |
| `openai-compat` | OpenAI `/chat/completions` (`{model, messages, max_tokens}` → `{choices[], usage}`) | OpenAI direct, Azure Foundry GPT, Bedrock Qwen/DeepSeek/Mistral/Llama, Vertex MaaS |
| `google-native` | Vertex `generateContent` (`{contents[{parts}], generationConfig}` → `{candidates[], usageMetadata}`) | Gemini on Vertex |

You do **not** pick the wire — the provider looks up the model in its catalog. Each `ModelEntry` can be reached by its canonical ID *or* any of its registered aliases, so the same row covers cross-region inference profiles (`us.` / `eu.` / `apac.` / `jp.` / `au.` / `global.` on Bedrock), date-stamped snapshot variants (`@20251101` on Vertex), and family-only short forms (`opus-4-7`, `sonnet-4-6`).

Adding a new model that uses an existing wire is one `ModelEntry` in the provider's `catalog.go`; no provider code change.

### `wire_override` — for uncatalogued models

If you want to use a model that is not yet in the shipped catalog (for example, a newly released Bedrock preview, or a custom fine-tune deployment), DecisionBox returns a clear error at agent-run time listing the valid wires. To route the model anyway, set `llm.config.wire_override` in the project config to one of `anthropic`, `openai-compat`, or `google-native`.

Example (API request to create a project):

```json
{
  "name": "my project",
  "domain": "gaming",
  "category": "match3",
  "llm": {
    "provider": "bedrock",
    "model": "vendor.some-2027-model-v1:0",
    "config": {
      "region": "us-east-1",
      "wire_override": "openai-compat"
    }
  }
}
```

A typo in `wire_override` is rejected at project-save time with HTTP 400. Once saved, the agent uses the override for every dispatch until the model is added to the catalog (at which point the override becomes unnecessary).

## Providers and models per layer

A project holds a set of named **provider entries** — connections, each with its own credentials — and chooses a **(provider entry, model)** pair for each **layer**: the named places DecisionBox calls a model.

**Settings → LLM Providers** manages the connections. **Settings → Models** chooses the models: one default pair that every layer uses, plus an override for any layer that wants something else. The common case is three controls — provider, model, reasoning — and a project never has to touch the rest.

### The layers

`GET /api/v1/llm/layers` is the authoritative list; the dashboard reads it rather than holding a copy. There are seventeen, grouped by whether the model choice changes what people see.

| Group | Layers |
|---|---|
| Main | `exploration`, `analysis`, `validation_verifier`, `validation_refuter`, `recommendations`, `ask` |
| Small | `questions`, `reflection`, `sql_repair`, `ask_router`, `ask_suggestions`, `table_descriptions`, `source_summary`, `pack_gen`, `file_ingestion`, `exec_summary`, `report_style` |

Where a layer matches a discovery phase it is spelled the same, so the `LLM call usage` log's `phase` field and the layer key are the same string. Validation is the exception: one phase now spends two models, so the log keeps `phase=validation` while `validation_verifier` and `validation_refuter` are configured separately. That pair is where the model matters most — verdicts depend heavily on it, and swapping it changes what users see as verified.

### Why mix models

Measured on a production ERP warehouse, with prompt caching on and the same judge scoring every run:

| Pipeline | Fully supported | Cost per run | Wall clock |
|---|---|---|---|
| One high-end model everywhere | 40–52% (avg 46%) | $13.46 | 46 min |
| High-end model, cheap model on the small layers | 40.5% | $11.71 | 26 min |
| Open-weight model exploring, high-end analysing and validating, cheap on the small layers | 38.5–61% (avg 49%) | ~$10.10 | 25–27 min |

A quarter off the cost and half the wall clock, with no loss in finding quality. The third row mixes **providers**, not just models, which is why a project can hold several connections.

### Token budgets, reasoning and prompt caching are per layer

Each layer's context window and output ceiling come from **its own** model, not the default one — including the `max_input_tokens` / `max_output_tokens` overrides, which a layer sets for itself.

Reasoning and prompt caching are per layer for the same reason: in a mixed pipeline one project-wide switch would span providers where it is inert on some and meaningful on others.

One asymmetry worth knowing. Reasoning is requested **per call**, so a per-layer setting costs nothing. Prompt caching is applied when the **connection is built**, so two layers on the same provider entry with different caching settings are two connections — one extra HTTP client on that entry. The settings page says so where the toggle is.

### Credentials

Each provider entry has its own credential, stored in the secret store and never in the project document.

| Entry | Secret key |
|---|---|
| `default` | `llm-credentials` (environment fallback `LLM_API_KEY`) |
| `blurb` | `blurb-llm-credentials` → `BLURB_LLM_API_KEY` → `llm-credentials` → `LLM_API_KEY` |
| anything else | `llm-credentials-<hex of the id>` |

The reserved `default` entry keeps the key a single-provider project already uses, so configuring layers moves no secret. A non-reserved entry has **no shared environment fallback**: it reads only its own key, plus an `env_var` named in the entry's config. The reason is concrete — an OpenAI-compatible entry sends whatever credential it is given as a bearer token to its own `base_url`, so a transient secret-store failure must not be able to send the default entry's key to a third party.

An `env_var` must name a variable starting **`DBX_LLM_`**, and any other name is refused on save and ignored on read. The entry's config and its `base_url` are written by whoever can edit the project, who is not necessarily the operator who owns the pod — so without the prefix, naming `MONGODB_URI` beside an endpoint of your choosing would read it out of the process environment and send it to that endpoint as a bearer token. The prefix leaves the choice with the operator, who decides what to export:

```yaml
env:
  - name: DBX_LLM_ACME_KEY        # nameable from a project document
    valueFrom: { secretKeyRef: { name: acme-llm, key: api-key } }
```

The reserved entries keep their fixed, unprefixed `LLM_API_KEY` and `BLURB_LLM_API_KEY`, which no project document chooses.

### The default row uses the default entry

`layer_models.default` must name the reserved `default` provider entry. To change what every layer inherits, edit that entry; to put some layers on another connection, give those layers their own rows. The restriction is what keeps the legacy mirror honest: it copies the default row's endpoint into `project.llm`, while the credential an older agent image reads for `project.llm` is fixed at `llm-credentials` — the reserved entry's key. A default row pointing elsewhere would send one entry's credential to another entry's endpoint.

Table descriptions are the exception that proves the rule: that layer MAY use a secondary entry, because a cheap model on another provider is exactly why a project has one. The `blurb_llm` mirror is then left **empty** rather than filled in, since an older image resolves it from `blurb-llm-credentials` → `llm-credentials` and has never heard of a namespaced key. It falls back to the analysis slot — the wrong model, but a call authorised for the endpoint receiving it. Put that layer on the reserved `blurb` entry instead and the mirror is written, because the credential already lives under the key the older image reads.

### Two layers on one connection, two credentials

Table descriptions resolve `blurb-llm-credentials` before the default key, which means a project can have two layers on one provider entry authenticating as different principals. Everything that groups layers by the deployment they reach — the provider instances a run builds, the live probes a support bundle makes — folds the resolved credential into that decision. A project whose blurb chain falls through to the default key still builds one provider instance; a project with a real blurb credential builds two, which is what it had before per-layer models existed.

### The routes that collect the configuration are never gated

The project guard sends an unconfigured project to its settings page before anything else in it can be used — except the two routes whose own steps collect that configuration: **settings** itself, and the **pack-generation wizard**. A draft created by the wizard has no LLM yet by construction, so gating it would redirect the wizard to settings before its provider step could render.

### Existing projects need one visit to settings

A project created before per-layer models is **not** configured for them, and discovery is refused until someone saves its configuration. The values arrive pre-filled from what the project already uses, so it is a review-and-confirm.

This applies to **scheduled runs too**: a schedule on an unconfigured project fails until the visit happens, and records why in its run history. There is one gate for every way work starts rather than a second path that runs on a configuration nobody reviewed.

The legacy `llm` and `blurb_llm` fields keep being written as a mirror of the default and table-description layers, so an agent, sources-worker or fileingestion-worker image older than the API keeps reading a truthful single-provider setup.

### Per-layer rules

Exactly one layer has a pre-flight rule: `table_descriptions` refuses a reasoning-class model. A model that thinks on every request returns an empty description inside that layer's small budget, and an index run fires one call per table. The rule reaches the dashboard as data (`refuses_reasoning_models`), so the picker warns before a save the API would reject.

A model that refuses a request at run time — some models decline pack generation — is a different thing: it is not knowable before the call, so it surfaces as an error naming the model and the layer to change, not as a denylist.

## Prompt caching and Claude 5.x

Discovery is multi-turn: every exploration step re-sends the system prompt and the whole conversation so far, and every verifier / refuter round re-sends its system prompt.
On Claude — direct, Bedrock, Vertex AI and Azure AI Foundry alike — DecisionBox marks a prompt-cache breakpoint on the system prompt and on the last message of every multi-turn request, so each call reads the previous call's prefix at the cache-read price (10% of base input on most Claude models, 5% on Opus 5.5 and Sonnet 5.5) and writes only the new tail.
Single-message requests (analysis, recommendations) mark only the shared system prompt, so they never pay the cache-write premium for a prefix nothing reads back.

Prompt caching is **on by default**.
Measured on a production-scale ERP warehouse with the same model and settings on the same day, it cut the exploration phase's cost by about 95% and the whole run's by about three quarters, with an unchanged quality profile.
To turn it off, clear **Prompt caching** on the layer in **Settings → Models** — only needed if your account or region does not support caching. It is a per-layer setting because a mixed pipeline spans providers where caching is inert on some and meaningful on others; see [Providers and models per layer](#providers-and-models-per-layer) for the one cost that comes with flipping it for a single layer.

OpenAI-compatible providers (OpenAI, LiteLLM, vLLM and hosted OpenAI-compatible APIs) cache repeated prefixes automatically on the server side; nothing needs to be set.

Every LLM call's usage — logged as `LLM call usage` and sent to the usage hook — carries the cached part of the prompt (`cache_read_tokens`, `cache_write_tokens`), hidden reasoning tokens (`reasoning_tokens`), the stop reason, and the discovery **phase** the call belongs to (exploration, analysis, validation, recommendations, questions, reflection), so cost can be attributed per phase.
`input_tokens` is always the total prompt size, cached part included.

**Claude Opus 5.5 and Sonnet 5.5.**
They reject a forced tool choice (`tool_choice` of type `tool` / `any`) with a 400, so for those models DecisionBox asks for structured output with an `auto` tool choice plus an instruction naming the tool — the replacement Anthropic documents.
They also refuse (stop reason `refusal`, no output) any conversation that asks for a JSON field named `thinking`, which is why the exploration and Ask action key is `rationale`.
Thinking cannot be disabled on Opus 5.5 and counts towards `max_tokens`; the catalog marks these models as reasoning models, so the exploration step gets the larger reasoning output budget.
A refusal reaches the caller as an error (`gollm.ErrModelRefused`) from every Claude provider, never as an empty reply.
Opus 5.5 and Haiku 5.5 sometimes send an array inside a structured answer as one JSON-encoded string; where the schema expects an array or object, the provider decodes it before the caller parses the reply.

## Context window and output limits

Discovery budgets the tokens it generates against the model's context window so `input + output` never exceeds it — otherwise a large analysis prompt plus a fixed output request is rejected with a hard `maximum context length` 400 and the run degrades to Partial.

DecisionBox does not need the model to be in its catalog to do this. It resolves the window in priority order — **operator override → self-calibration → live auto-detection → catalog → default** — so an arbitrary customer model works out of the box:

- **Auto-detection (no typing).** When you click **Load models**, DecisionBox reads the real window/output from the provider where it is exposed — LiteLLM (`GET /model/info`), Ollama (`GET /api/show` `context_length`), and OpenAI-compatible gateways that report `max_model_len` (e.g. vLLM). Selecting such a model **prefills** the two fields below with the detected values.
- **Manual override.** The **Max input tokens** (`max_input_tokens`) and **Max output tokens** (`max_output_tokens`) fields let you set the real limits for any model DecisionBox cannot detect. The override always wins. Leave them blank to use the detected/catalogued value or the conservative default (128K input, 64K output). They are **per layer**, because the ceiling belongs to the model that layer runs.
- **Self-calibration.** If a request still overflows (an under-estimated window on an unknown model), the model's error states its true window. The agent recomputes a fitting `max_tokens`, retries once, and records the window (in the `llm_model_windows` collection) so later runs for the same model budget correctly up front — you never have to look the number up. A window is recorded against the **provider entry and model** that learned it, so a window learned on one layer's cheap model never shrinks another layer's budget, and two connections to different deployments that happen to name the same model alias do not contaminate each other.

For most catalogued models and most gateways you never touch these fields. Set `max_input_tokens` explicitly only for a model that is uncatalogued *and* whose gateway does not report its window.

## Next Steps

- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Adding LLM Providers](adding-llm-providers.md) — Add a whole new cloud or a new wire
- [Configuring Warehouses](configuring-warehouse.md) — Data warehouse setup
