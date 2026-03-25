# Configuring LLM Providers

> **Version**: 0.1.0

DecisionBox supports five LLM providers. This guide covers setting up each one.

## Provider Comparison

| Provider | Models | Auth | Best For |
|----------|--------|------|----------|
| **Claude (Anthropic)** | Claude Sonnet 4, Opus 4, Haiku 4.5 | API key | Best quality. Direct access, simple setup. |
| **OpenAI** | GPT-4o, GPT-4o-mini | API key | Widely used. Good alternative. |
| **Ollama** | Llama 3.1, Qwen 2.5, Mistral, any GGUF | None (local) | Free, private, no API key needed. |
| **Vertex AI** | Claude + Gemini (via Google) | GCP ADC | GCP users. Managed billing, IAM auth. |
| **AWS Bedrock** | Claude + Llama + Mistral (via AWS) | AWS credentials | AWS users. Managed billing, IAM auth. |

## Claude (Direct Anthropic API)

The simplest setup and highest quality results.

### 1. Get an API Key

Sign up at [console.anthropic.com](https://console.anthropic.com) and create an API key.

### 2. Configure in Dashboard

1. Create a project (or edit existing) → select **Claude (Anthropic)** as LLM provider
2. Enter model name: `claude-sonnet-4-20250514` (recommended) or `claude-opus-4-20250514` (most capable)
3. Go to **Settings → AI Provider** → set **API Key** to your `sk-ant-...` key

### 3. Model Options

| Model | Quality | Speed | Cost |
|-------|---------|-------|------|
| `claude-opus-4-20250514` | Highest | Slow | $15/$75 per million tokens |
| `claude-sonnet-4-20250514` | High | Fast | $3/$15 per million tokens |
| `claude-haiku-4-5-20251001` | Good | Fastest | $0.80/$4 per million tokens |

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

Access Claude and Gemini through Google's managed platform. Uses GCP IAM for authentication (no API keys).

### 1. Prerequisites

- GCP project with Vertex AI API enabled
- Claude and/or Gemini models enabled in [Model Garden](https://console.cloud.google.com/vertex-ai/model-garden)
- Application Default Credentials configured:

```bash
gcloud auth application-default login
# Or use a service account with Vertex AI User role
```

### 2. Configure in Dashboard

1. Select **Vertex AI** as LLM provider
2. Enter model name:
   - Claude: `claude-sonnet-4-20250514` or `claude-haiku-4-5@20251001`
   - Gemini: `gemini-2.5-pro` or `gemini-2.5-flash`
3. Set provider-specific config:
   - **Project ID**: Your GCP project ID
   - **Location**: Region where the model is enabled (e.g., `us-east5` for Claude, `us-central1` for Gemini)

### 3. No API Key Needed

Vertex AI uses GCP Application Default Credentials (ADC). No LLM API key secret is needed.

### Model Name Format

- Claude on Vertex: `claude-sonnet-4-20250514` or `claude-haiku-4-5@20251001` (with `@` for versioned models)
- Gemini on Vertex: `gemini-2.5-pro`, `gemini-2.5-flash`

The provider automatically routes to the correct API format based on model name prefix (`claude-*` → Anthropic rawPredict, `gemini-*` → Google generateContent).

## AWS Bedrock

Access Claude, Llama, and Mistral through AWS's managed platform. Uses AWS IAM for authentication.

### 1. Prerequisites

- AWS account with Bedrock access
- Claude model access enabled in [Bedrock Model Access](https://console.aws.amazon.com/bedrock/home#/modelaccess)
- AWS credentials configured:

```bash
aws configure
# Or use IAM role / instance profile
```

### 2. Configure in Dashboard

1. Select **AWS Bedrock** as LLM provider
2. Enter model name: `us.anthropic.claude-sonnet-4-20250514-v1:0`
3. Set provider-specific config:
   - **Region**: AWS region (e.g., `us-east-1`)

### 3. No API Key Needed

Bedrock uses AWS credentials (IAM role, env vars, or `~/.aws/credentials`). No LLM API key secret is needed.

### Model Name Format

Bedrock model IDs are different from direct Anthropic IDs:

| Model | Bedrock Model ID |
|-------|-----------------|
| Claude Sonnet 4 | `us.anthropic.claude-sonnet-4-20250514-v1:0` |
| Claude Opus 4 | `us.anthropic.claude-opus-4-20250514-v1:0` |
| Claude Haiku 4.5 | `us.anthropic.claude-haiku-4-5-20251001-v1:0` |

The `us.` prefix is an inference profile ID required for newer models.

## Timeout Configuration

The default LLM timeout is 300 seconds (5 minutes). For very large prompts (many previous insights, large schemas), you may need more time:

```bash
# In docker-compose or env
LLM_TIMEOUT=600s   # 10 minutes
```

Or set per-project in the dashboard (not yet available — use env var for now).

## Next Steps

- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Adding LLM Providers](adding-llm-providers.md) — Add support for a new LLM
- [Configuring Warehouses](configuring-warehouse.md) — Data warehouse setup
