package ollama

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// MaxInputTokens here is the model's *upstream-published* native
// context — what the model architecture supports. Budgeting call-
// sites use this to size multi-turn prompt history; the Ollama
// provider does NOT automatically forward it as `num_ctx`, because
// per-request `num_ctx` is an override on Ollama (not capped by
// OLLAMA_CONTEXT_LENGTH) and a catalog-driven default would force
// large KV-cache allocations on hosts that intentionally run at a
// lower server window. Operators raise `num_ctx` per-project when
// they actually want the full window. The provider always pins
// `truncate=false` so an oversize prompt fails with a clear server
// error instead of being silently trimmed.
const (
	ctx1M   = 1048576 // Llama 4 Scout's published native window
	ctx256K = 262144  // Gemma 4 (26B/31B), Qwen3-Coder
	ctx128K = 131072
	ctx64K  = 65536
	ctx32K  = 32768
	ctx16K  = 16384
	ctx8K   = 8192
	ctx4K   = 4096
	ctx2K   = 2048
)

// Output-token caps for popular Ollama model families. Where a model
// publishes a documented generation limit we use it; otherwise the cap
// defaults to 16384, capped at the context window for smaller models.
// (A few long-standing entries predate this and keep their original
// model-card / practical caps.) The agent caps requests at these so a
// poorly-specified prompt doesn't truncate before the final answer.
// Pricing is zero — Ollama runs locally so the user pays for compute.
//
// Wire is WireUnknown for every Ollama entry: Ollama's Chat()
// dispatches through ollamaapi directly with no wire switch, so the
// field carries no dispatch meaning and the dashboard shows no wire
// badge.
//
// Each family entry's Aliases cover both the bare name and the most
// common Ollama tags (`:latest`, `:<size>`, etc.). Users can paste
// any tag and the resolver finds the right cap; a tag that doesn't
// match falls through to DefaultMaxOutputTokens.
func buildOllamaCatalog() []gollm.ModelEntry {
	return []gollm.ModelEntry{
		// Qwen 3.6 / 3.5 — model card lists max_tokens=81920; 64k
		// matches the hosted Qwen-Plus tier and leaves headroom.
		{
			ID:              "qwen3.6",
			Aliases:         []string{"qwen3.6:latest", "qwen3.6:35b-a3b"},
			DisplayName:     "Qwen 3.6",
			MaxOutputTokens: 65536,
			MaxInputTokens:  ctx128K,
		},
		{
			ID:              "qwen3.5",
			Aliases:         []string{"qwen3.5:latest", "qwen3.5:122b"},
			DisplayName:     "Qwen 3.5",
			MaxOutputTokens: 65536,
			MaxInputTokens:  ctx128K,
		},

		// DeepSeek R1 — reasoning-only model. Hidden chain-of-thought
		// counts against num_predict, so the cap must cover both the
		// reasoning and the final answer; 128k matches the uncatalogued
		// default and lets long-form completions land cleanly. Aliases
		// cover every size on the Ollama library so a user-pulled tag
		// resolves to the Reasoning:true row and IsReasoningModel keeps
		// returning true at request time (exact match, no fuzzy fallback).
		{
			ID: "deepseek-r1",
			Aliases: []string{
				"deepseek-r1:latest",
				"deepseek-r1:1.5b",
				"deepseek-r1:7b",
				"deepseek-r1:8b",
				"deepseek-r1:14b",
				"deepseek-r1:32b",
				"deepseek-r1:70b",
				"deepseek-r1:671b",
			},
			DisplayName:     "DeepSeek R1",
			MaxOutputTokens: ollamaDefaultMaxOutputTokens,
			MaxInputTokens:  ctx128K,
			Reasoning:       true,
		},

		// Qwen 3 — thinking mode is on by default; reasoning tokens are
		// emitted before the answer and consume the same num_predict
		// budget. Cap matches the uncatalogued default so reasoning
		// plus answer fit. Aliases cover every size on the Ollama
		// library; without them, a small pulled tag (qwen3:0.6b /
		// :4b / :8b) would fall through to the unknown-row path where
		// IsReasoningModel returns false and the gate strips Think for
		// any non-Off ReasoningEffort.
		{
			ID: "qwen3",
			Aliases: []string{
				"qwen3:latest",
				"qwen3:0.6b",
				"qwen3:1.7b",
				"qwen3:4b",
				"qwen3:8b",
				"qwen3:14b",
				"qwen3:30b-a3b",
				"qwen3:32b",
				"qwen3:235b",
				"qwen3:235b-a22b",
			},
			DisplayName:     "Qwen 3",
			MaxOutputTokens: ollamaDefaultMaxOutputTokens,
			MaxInputTokens:  ctx128K,
			Reasoning:       true,
		},

		// DeepSeek V3.
		{
			ID:              "deepseek-v3",
			Aliases:         []string{"deepseek-v3:latest", "deepseek-v3.2"},
			DisplayName:     "DeepSeek V3",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx64K,
		},

		// Qwen 2.5 — model card sets max_new_tokens=16384.
		{
			ID: "qwen2.5",
			Aliases: []string{
				"qwen2.5:latest",
				"qwen2.5:32b",
				"qwen2.5:72b",
				"qwen2.5-coder",
				"qwen2.5-coder:32b",
			},
			DisplayName:     "Qwen 2.5",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},

		// Gemma 3 — reasoning-capable; reasoning tokens count against
		// num_predict, so raise the cap so a reasoning-on response
		// can still emit a meaningful answer alongside its thinking.
		// Aliases cover every size on the Ollama library so a pulled
		// tag (gemma3:1b / :4b / :12b / :27b) still resolves to the
		// Reasoning:true row.
		{
			ID:              "gemma3",
			Aliases:         []string{"gemma3:latest", "gemma3:1b", "gemma3:4b", "gemma3:12b", "gemma3:27b"},
			DisplayName:     "Gemma 3",
			MaxOutputTokens: ollamaDefaultMaxOutputTokens,
			MaxInputTokens:  ctx128K,
			Reasoning:       true,
		},

		// Llama 4 — huge context (1M+ on Scout), 8k practical output.
		{
			ID: "llama4",
			Aliases: []string{
				"llama4:latest",
				"llama4:scout",
				"llama4:maverick",
			},
			DisplayName:     "Llama 4",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx1M,
		},

		// Llama 3.x — 128k context, 8k practical output. Each shipped
		// size is listed so the resolver finds them without a fuzzy
		// fallback.
		{
			ID: "llama3.3",
			Aliases: []string{
				"llama3.3:latest",
				"llama3.3:70b",
			},
			DisplayName:     "Llama 3.3",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx128K,
		},
		{
			ID: "llama3.2",
			Aliases: []string{
				"llama3.2:latest",
				"llama3.2:1b",
				"llama3.2:3b",
			},
			DisplayName:     "Llama 3.2",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx128K,
		},
		{
			ID: "llama3.1",
			Aliases: []string{
				"llama3.1:latest",
				"llama3.1:8b",
				"llama3.1:70b",
				"llama3.1:405b",
			},
			DisplayName:     "Llama 3.1",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx128K,
		},
		{
			ID: "llama3",
			Aliases: []string{
				"llama3:latest",
				"llama3:8b",
				"llama3:70b",
			},
			DisplayName:     "Llama 3",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx8K,
		},

		// Gemma 2 — 8k context, output capped at 8k.
		{
			ID: "gemma2",
			Aliases: []string{
				"gemma2:latest",
				"gemma2:2b",
				"gemma2:9b",
				"gemma2:27b",
			},
			DisplayName:     "Gemma 2",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx8K,
		},

		// Gemma 4 (small) — e2b/e4b/latest ship a 128k window. Thinking
		// is on by default and consumes generation budget; the cap is
		// raised so reasoning-on calls don't truncate the answer.
		{
			ID:              "gemma4",
			Aliases:         []string{"gemma4:latest", "gemma4:e2b", "gemma4:e4b"},
			DisplayName:     "Gemma 4 (small)",
			MaxOutputTokens: ollamaDefaultMaxOutputTokens,
			MaxInputTokens:  ctx128K,
			Reasoning:       true,
		},
		// Gemma 4 (26B/31B) — medium models publish a 256k window. Aliases
		// cover the bare sizes plus the standard instruct quants
		// (bf16 / q4_K_M / q8_0) so the common pulled tags resolve.
		// Thinking is on by default and consumes generation budget.
		{
			ID: "gemma4:31b",
			Aliases: []string{
				"gemma4:26b",
				"gemma4:31b-it-bf16",
				"gemma4:31b-it-q4_K_M",
				"gemma4:31b-it-q8_0",
				"gemma4:26b-it-bf16",
				"gemma4:26b-it-q4_K_M",
				"gemma4:26b-it-q8_0",
			},
			DisplayName:     "Gemma 4 (26B/31B)",
			MaxOutputTokens: ollamaDefaultMaxOutputTokens,
			MaxInputTokens:  ctx256K,
			Reasoning:       true,
		},

		// Qwen3-Coder — 256k native window (extendable to 1M).
		{
			ID:              "qwen3-coder",
			Aliases:         []string{"qwen3-coder:latest", "qwen3-coder:30b", "qwen3-coder:480b"},
			DisplayName:     "Qwen3 Coder",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx256K,
		},
		// Qwen 2 — 128k on the 7b/72b tiers.
		{
			ID:              "qwen2",
			Aliases:         []string{"qwen2:latest", "qwen2:0.5b", "qwen2:1.5b", "qwen2:7b", "qwen2:72b"},
			DisplayName:     "Qwen 2",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},

		// GPT-OSS (OpenAI open-weight) — 128k context.
		{
			ID:              "gpt-oss",
			Aliases:         []string{"gpt-oss:latest", "gpt-oss:20b", "gpt-oss:120b"},
			DisplayName:     "GPT-OSS",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},
		// Phi-4 — 16k context.
		{
			ID:              "phi4",
			Aliases:         []string{"phi4:latest", "phi4:14b"},
			DisplayName:     "Phi 4",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx16K,
		},

		// Mistral 7B — 32k context (v0.3).
		{
			ID:              "mistral",
			Aliases:         []string{"mistral:latest", "mistral:7b"},
			DisplayName:     "Mistral 7B",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx32K,
		},
		// Mistral NeMo — 128k context (12B, built with NVIDIA).
		{
			ID:              "mistral-nemo",
			Aliases:         []string{"mistral-nemo:latest", "mistral-nemo:12b"},
			DisplayName:     "Mistral NeMo",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},
		// Mistral Small — 128k context (22b/24b).
		{
			ID:              "mistral-small",
			Aliases:         []string{"mistral-small:latest", "mistral-small:22b", "mistral-small:24b"},
			DisplayName:     "Mistral Small",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},

		// Code Llama — 16k practical context.
		{
			ID:              "codellama",
			Aliases:         []string{"codellama:latest", "codellama:7b", "codellama:13b", "codellama:34b", "codellama:70b"},
			DisplayName:     "Code Llama",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx16K,
		},
		// CodeGemma — 8k context.
		{
			ID:              "codegemma",
			Aliases:         []string{"codegemma:latest", "codegemma:2b", "codegemma:7b"},
			DisplayName:     "CodeGemma",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx8K,
		},
		// DeepSeek Coder — 16k context.
		{
			ID:              "deepseek-coder",
			Aliases:         []string{"deepseek-coder:latest", "deepseek-coder:1.3b", "deepseek-coder:6.7b", "deepseek-coder:33b"},
			DisplayName:     "DeepSeek Coder",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx16K,
		},

		// Dolphin 3 (Llama 3.1 8B base) — 128k context.
		{
			ID:              "dolphin3",
			Aliases:         []string{"dolphin3:latest", "dolphin3:8b"},
			DisplayName:     "Dolphin 3",
			MaxOutputTokens: 16384,
			MaxInputTokens:  ctx128K,
		},

		// Gemma 1 — 8k context.
		{
			ID:              "gemma",
			Aliases:         []string{"gemma:latest", "gemma:2b", "gemma:7b"},
			DisplayName:     "Gemma",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx8K,
		},
		// Llama 2 — 4k context.
		{
			ID:              "llama2",
			Aliases:         []string{"llama2:latest", "llama2:7b", "llama2:13b", "llama2:70b"},
			DisplayName:     "Llama 2",
			MaxOutputTokens: 4096,
			MaxInputTokens:  ctx4K,
		},
		// SmolLM2 — 8k context.
		{
			ID:              "smollm2",
			Aliases:         []string{"smollm2:latest", "smollm2:135m", "smollm2:360m", "smollm2:1.7b"},
			DisplayName:     "SmolLM2",
			MaxOutputTokens: 8192,
			MaxInputTokens:  ctx8K,
		},
		// OLMo 2 — 4k context.
		{
			ID:              "olmo2",
			Aliases:         []string{"olmo2:latest", "olmo2:7b", "olmo2:13b"},
			DisplayName:     "OLMo 2",
			MaxOutputTokens: 4096,
			MaxInputTokens:  ctx4K,
		},
		// TinyLlama — 2k context.
		{
			ID:              "tinyllama",
			Aliases:         []string{"tinyllama:latest", "tinyllama:1.1b"},
			DisplayName:     "TinyLlama",
			MaxOutputTokens: 2048,
			MaxInputTokens:  ctx2K,
		},
	}
}
