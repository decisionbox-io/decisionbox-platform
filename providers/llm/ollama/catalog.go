package ollama

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Output-token caps for popular Ollama model families. Values come
// from each model card's documented synchronous generation limit; the
// agent caps requests at these so a poorly-specified prompt doesn't
// truncate before the final answer. Pricing is zero — Ollama runs
// locally so the user pays for compute, not tokens.
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
		},
		{
			ID:              "qwen3.5",
			Aliases:         []string{"qwen3.5:latest", "qwen3.5:122b"},
			DisplayName:     "Qwen 3.5",
			MaxOutputTokens: 65536,
		},

		// DeepSeek R1 — reasoning chains need the long tail.
		{
			ID: "deepseek-r1",
			Aliases: []string{
				"deepseek-r1:latest",
				"deepseek-r1:14b",
				"deepseek-r1:32b",
				"deepseek-r1:70b",
				"deepseek-r1:671b",
			},
			DisplayName:     "DeepSeek R1",
			MaxOutputTokens: 32768,
		},

		// Qwen 3 — tech report recommends 32k for standard output.
		{
			ID: "qwen3",
			Aliases: []string{
				"qwen3:latest",
				"qwen3:30b-a3b",
				"qwen3:32b",
				"qwen3:235b",
				"qwen3:235b-a22b",
			},
			DisplayName:     "Qwen 3",
			MaxOutputTokens: 32768,
		},

		// DeepSeek V3.
		{
			ID:              "deepseek-v3",
			Aliases:         []string{"deepseek-v3:latest", "deepseek-v3.2"},
			DisplayName:     "DeepSeek V3",
			MaxOutputTokens: 16384,
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
		},

		// Gemma 3 — paid-tier providers expose 16k output.
		{
			ID:              "gemma3",
			Aliases:         []string{"gemma3:latest", "gemma3:27b"},
			DisplayName:     "Gemma 3",
			MaxOutputTokens: 16384,
		},

		// Llama 4 — huge context, 8k practical output.
		{
			ID: "llama4",
			Aliases: []string{
				"llama4:latest",
				"llama4:scout",
				"llama4:maverick",
			},
			DisplayName:     "Llama 4",
			MaxOutputTokens: 8192,
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
		},
	}
}
