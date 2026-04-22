# Configuring LLM Providers

> **Version**: 0.4.0

DecisionBox supports six LLM providers. Cloud providers (Bedrock, Vertex AI, Azure AI Foundry) speak multiple wire formats and dispatch per model through a central model catalog — see [Model catalog and wire formats](#model-catalog-and-wire-formats) below.

## Provider Comparison

| Provider | Models | Auth | Best For |
|----------|--------|------|----------|
| **Claude (Anthropic)** | Claude Sonnet 4, Opus 4, Haiku 4.5 | API key | Best quality. Direct access, simple setup. |
| **OpenAI** | GPT-5, GPT-4.1, GPT-4o, o3, o4-mini | API key | Widely used. Good alternative. |
| **Ollama** | Llama 3.1, Qwen 2.5, Mistral, any GGUF | None (local) | Free, private, no API key needed. |
| **Vertex AI** | Gemini, Claude, Llama MaaS, Qwen MaaS, DeepSeek MaaS, Mistral MaaS | GCP ADC | GCP users. Managed billing, IAM auth. |
| **AWS Bedrock** | Claude, Qwen, DeepSeek, Mistral, Llama | AWS credentials | AWS users. Managed billing, IAM auth. |
| **Azure AI Foundry** | Claude, GPT-5 / GPT-4.1 / GPT-4o, Mistral | API key | Azure users. Managed billing, Azure RBAC. |

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
| `claude-opus-4-6` | Highest | Slow | $15/$75 per million tokens |
| `claude-sonnet-4-6` | High | Fast | $3/$15 per million tokens |
| `claude-haiku-4-5` | Good | Fastest | $0.80/$4 per million tokens |

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

### 3. No API Key Needed

Vertex AI uses GCP Application Default Credentials (ADC). No LLM API key secret is needed.

### Model Name Format

- **Gemini** uses plain IDs: `gemini-2.5-pro`, `gemini-2.5-flash`
- **Claude-on-Vertex** uses `@` for versioning: `claude-opus-4-6@20251101`, `claude-sonnet-4@20250514`
- **Model Garden MaaS** uses publisher-prefixed IDs: `meta/llama-3.3-70b-instruct-maas`, `qwen/qwen3-coder-480b-a35b-instruct-maas`

The provider looks up the model in the catalog and routes to the correct wire format — you do not need to tell DecisionBox which wire to use.

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

The provider looks the deployment name up in the model catalog and routes to the right wire.

### 4. Authentication

Azure AI Foundry supports API key authentication.
The API key is set per-project via the dashboard's AI Provider settings tab.

For production on AKS, you can also use Entra ID (Azure AD) with managed identity, but this requires custom configuration outside DecisionBox.

## Model catalog and wire formats

Every cloud provider carries a central model catalog (`libs/go-common/llm/modelcatalog`) that maps `(cloud, model-id)` to a **wire format** — the request/response schema the model expects:

| Wire | What it is | Used by |
|---|---|---|
| `anthropic` | Anthropic Messages API (`{messages, system, max_tokens}` → `{content[], stop_reason, usage}`) | Claude on Anthropic API, Bedrock, Vertex, Azure |
| `openai-compat` | OpenAI `/chat/completions` (`{model, messages, max_tokens}` → `{choices[], usage}`) | OpenAI direct, Azure GPT, Bedrock Qwen/DeepSeek/Mistral/Llama, Vertex MaaS |
| `google-native` | Vertex `generateContent` (`{contents[{parts}], generationConfig}` → `{candidates[], usageMetadata}`) | Gemini on Vertex |

You do **not** pick the wire — the provider looks up the model in the catalog. Adding a new model that uses an existing wire requires a single `Register` call in `libs/go-common/llm/modelcatalog/catalog.go`; no provider code change.

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
