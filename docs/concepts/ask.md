---
sidebar_position: 6
title: Ask Feature
---

# Ask: Token-Aware Context Budgeting

The Ask feature (`POST /api/v1/projects/{id}/ask`) runs a RAG flow: it embeds the user's question, retrieves the top-K relevant insights / recommendations / knowledge chunks, and asks the project's LLM provider to synthesize an answer. Multi-turn sessions persist Q&A pairs in `ask_sessions` so a follow-up question carries earlier context.

This page documents how the API keeps multi-turn sessions from overflowing the model's input window.

## Why budgeting matters

Every chat request carries:

1. The current user question.
2. The retrieved RAG context (insight name + description + score per result, plus knowledge-source chunks).
3. The system prompt.
4. The full text of every prior turn in the session (`Question` and `Answer` for each `AskSessionMessage`).
5. A reserved generation budget (`max_tokens`).

By turn 3–5 of a normal session, (1) + (2) + (4) easily run several thousand tokens. Multiplied by long answers stored verbatim in the session, the prompt can exceed the model's input window. Without a budgeting layer, the upstream provider rejects the request with a generic 4xx and the dashboard surfaces "Sorry, I could not answer this question."

The handler now sizes every Ask call against the model's published context window and trims old turns before the request goes upstream.

## How the budget is computed

Each model carries a `MaxInputTokens` in its catalog entry — the upstream-published context window:

| Provider | Model family | `MaxInputTokens` |
|---|---|---|
| Anthropic Claude (direct, Bedrock, Vertex, Azure Foundry) | Claude 4.x (Opus / Sonnet / Haiku) | 200000 |
| OpenAI / Azure Foundry | GPT-5 family | 400000 |
| OpenAI / Azure Foundry | GPT-4.1 family | 1000000 |
| OpenAI / Azure Foundry | GPT-4o family | 128000 |
| OpenAI | o-series (o3 / o4-mini) | 200000 |
| Vertex AI | Gemini 1.5 / 2.x | 1000000 |
| Bedrock | Llama 4 Maverick | 1000000 |
| Bedrock | Llama 3.x, Qwen 3.x, DeepSeek R1, Mistral Large | 128000 |
| Bedrock | Mixtral 8x22B | 65536 |
| Ollama | Llama 4, Qwen 3.x, Llama 3.x, DeepSeek R1, Gemma 3 | 128000 |
| Ollama | Llama 3, Gemma 2 | 8192 |

Unknown models fall back to a global default of **32000 tokens** (deliberately conservative — over-trimming is recoverable, under-trimming and 4xx is not).

The handler then subtracts:

```
budget.Available()
    = ModelMaxInput
    - max_tokens (reserved for generation, default 2048 for /ask)
    - ~600 tokens (flat reserve for the system prompt and scaffolding)
    - safety margin (5% with an exact counter, 15% with the approximate counter)
```

Whatever is left is the **history budget** — how many tokens may be spent on the current question + RAG context + prior session turns. If even the current question plus a single retrieved insight doesn't fit, the handler returns a typed `413 context_overflow` (see "Typed errors" below) rather than letting the provider 4xx.

## Token counters (exact vs approximate)

Different providers expose different token-counting capabilities:

| Provider / model | Counter | Notes |
|---|---|---|
| Anthropic Claude (direct) | `/v1/messages/count_tokens` | Exact. One extra RTT per call. |
| OpenAI (canonical `api.openai.com` endpoint) | [`tiktoken-go`](https://github.com/pkoukk/tiktoken-go) with the model's declared `Encoding` | Exact. Local, no network. `o200k_base` for every current OpenAI model. Unknown models on the canonical endpoint still get tiktoken with the `o200k_base` fallback. |
| OpenAI provider with a custom `base_url` (self-hosted proxy, OpenAI-compatible gateway) | Rune-count approximation | Tokenizer of the upstream is unknown; tiktoken would over- or under-count. The approximation + wider 15% safety margin is the safe choice. |
| Azure AI Foundry — OpenAI-wire models (GPT-5 / 4.1 / 4o family) | `tiktoken-go` with the model's declared `Encoding` | Exact. Same code path as direct OpenAI. |
| Azure AI Foundry — Claude / Mistral / unknown deployment | Rune-count approximation | Foundry fronts Claude through its own wire (no `count_tokens` available); Mistral and custom deployments have no declared encoding. |
| Bedrock | Rune-count approximation (`runes / 4`) | No universal token API on Bedrock. |
| Vertex AI — Gemini (Google-native wire) | `:countTokens` REST endpoint | Exact. One extra RTT per call; uses the same ADC bearer the Chat path uses, does not consume generation quota. |
| Vertex AI — Claude (Anthropic wire) | Rune-count approximation | Vertex's Anthropic publisher does not expose a public count_tokens endpoint. |
| Vertex AI — Llama / Qwen / DeepSeek / Mistral MaaS (OpenAI-compat) | Rune-count approximation | SentencePiece-based tokenizers; tiktoken would be systematically wrong. |
| Ollama | Rune-count approximation | Model-specific tokenizers vary; users running with low `num_ctx` should pick a smaller model. |
| Unknown provider / model | Rune-count approximation | Default fallback. |

The handler picks the right counter automatically. When the provider does not implement `gollm.TokenCounterProvider`, the handler uses `gollm.ApproximateCounter` and widens the safety margin from 5% to 15% to absorb the inaccuracy — under-counting causes the exact 400 the budget layer is designed to prevent, so we err generously toward over-trimming.

## How trimming works

When the session history doesn't fit, the handler walks `session.Messages` newest → oldest and keeps only the suffix that fits:

1. Start with `budget = historyBudget`.
2. For each `(Question, Answer)` pair, sum `tokens(Question) + tokens(Answer)`.
3. If the pair fits, prepend it to the kept list. Otherwise stop.
4. Trim by pairs only — never leave an assistant message without its question (the model rejects orphan answers).
5. The current user question always rides at the end.

If even after trimming the request would overflow the model's window, the handler runs a second-stage RAG shrink: it drops the lowest-scoring insight from the retrieved context and re-checks. With the floor at 1 insight, if it still doesn't fit, the API returns `413 context_overflow`.

This replaces the previous hard-coded "keep last 10 turns" cap, which had no awareness of model context window or message length.

## Typed errors

The /ask endpoint returns structured error codes the dashboard can branch on:

| HTTP | `code` | When | Dashboard surfaces |
|---|---|---|---|
| `412 Precondition Failed` | `embedding_not_configured` | Project has no embedding provider set. | "This project has no embedding provider configured. Add one under project settings → Embedding to enable Ask." |
| `412 Precondition Failed` | `llm_not_configured` | Project has no LLM provider set, or the configured provider failed to instantiate. | "This project has no LLM provider configured. Add one under project settings → LLM to enable Ask." |
| `413 Payload Too Large` | `context_overflow` | Even after trimming, the request exceeds the model's input window. | "This conversation has grown past the model's context window. Start a new chat, or switch to a model with a wider context window." |
| `502 Bad Gateway` | `llm_upstream` | Provider returned a 4xx that is not a context overflow (rate limit, content filter, billing). | "The LLM provider rejected the request. Try again; if it keeps happening, check provider credentials and quota." The sanitised provider message is on `ApiError.details` for any future "what happened" expander, never in the primary copy. |
| `504 Gateway Timeout` | `llm_upstream` | Context cancelled or deadline exceeded. | Same as 502 messaging — try again. |
| `500 Internal Server Error` | `llm_synthesis_failed` | Catch-all for unexpected provider failures. | "The LLM provider failed to answer this question. Try again, or start a new chat." |

The dashboard branches on `code` (machine-readable) rather than the human-readable `error` message — the two are stable across releases; the message can be reworded without breaking UI branching.

Sample response body for a 413:

```json
{
  "error": "this conversation has grown past the model's context window",
  "code": "context_overflow",
  "details": "model=claude-sonnet-4-6 window=200000 question=12 rag=1200 knowledge=0"
}
```

## What changes for users

Most users see no difference — typical 5-to-20-turn sessions fit easily within Claude Sonnet's 200K window or any modern model. The visible behavior change is:

1. Long sessions that previously failed at turn 5–6 with a misleading "embedding not configured" message now succeed (older turns are trimmed to fit).
2. Very long sessions that genuinely can't be made to fit see a specific "start a new chat" message instead of a generic error.
3. Misconfigured projects see exactly which dependency is missing (embedding vs LLM) instead of a generic error.

## What's out of scope for this iteration

- Per-turn summarization (storing a short summary alongside each answer and injecting the summary when the full body doesn't fit).
- Claude-Code-style session compaction (a continuously updated session-level summary that survives trim).
- Telemetry / per-call token logging.

Those are tracked separately and will compose with the budgeting layer described here.
