# Configuring LLM Providers

DecisionBox supports seven LLM providers. Cloud providers (Bedrock, Vertex AI, Azure AI Foundry) speak multiple wire formats and dispatch per model through their inline catalog — see [Model catalog and wire formats](#model-catalog-and-wire-formats) below.

Any HTTPS LLM endpoint fronted by a private / internal CA can be trusted without rebuilding an image — see [Custom TLS (private CA)](#custom-tls-private-ca) below.

## Provider Comparison

| Provider | Models | Auth | Best For |
|----------|--------|------|----------|
| **Claude (Anthropic)** | Claude Sonnet 4, Opus 4, Haiku 4.5 | API key | Best quality. Direct access, simple setup. |
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
| `claude-opus-4-6` | Highest | Slow | $5/$25 per million tokens |
| `claude-sonnet-4-6` | High | Fast | $3/$15 per million tokens |
| `claude-haiku-4-5` | Good | Fastest | $1/$5 per million tokens |

**Recommendation:** Start with Sonnet for a balance of quality and cost. Use Opus for complex datasets.

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
`"off"` are silently ignored on models the catalog flags as
non-reasoning so the request doesn't 400 against an upstream that
rejects `think=true` on a non-thinking model.

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
2. Enter the **LiteLLM proxy URL** as `base_url` (e.g. `https://litellm.internal:4000`). The OpenAI-compatible `/v1` routes are derived from it.
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

## Next Steps

- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Adding LLM Providers](adding-llm-providers.md) — Add a whole new cloud or a new wire
- [Configuring Warehouses](configuring-warehouse.md) — Data warehouse setup
