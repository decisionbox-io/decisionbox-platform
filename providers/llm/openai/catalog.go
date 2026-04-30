package openai

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// OpenAI direct API model catalog.
//
// Pricing source: https://openai.com/pricing (USD per 1M tokens).
// Output-token caps follow OpenAI's published per-model maximum
// completion tokens — for reasoning models (o3, o4-mini) the
// generation budget includes reasoning tokens, so the cap is set
// generously so the agent doesn't truncate before the final answer.
//
// Date-stamped snapshot IDs (e.g. "gpt-4o-2024-08-06") are accepted
// as aliases of the floating model name so contract pinning works
// without forcing a re-save when OpenAI rolls a new snapshot.
func buildOpenAICatalog() []gollm.ModelEntry {
	return []gollm.ModelEntry{
		{
			ID:              "gpt-5",
			Aliases:         []string{"gpt-5-2025-09-01"},
			DisplayName:     "GPT-5",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			Pricing:         gollm.TokenPricing{InputPerMillion: 5.0, OutputPerMillion: 15.0},
		},
		{
			ID:              "gpt-5-mini",
			DisplayName:     "GPT-5 Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.30, OutputPerMillion: 1.20},
		},
		{
			ID:              "gpt-4.1",
			Aliases:         []string{"gpt-4.1-2025-04-14"},
			DisplayName:     "GPT-4.1",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "gpt-4.1-mini",
			Aliases:         []string{"gpt-4.1-mini-2025-04-14"},
			DisplayName:     "GPT-4.1 Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.40, OutputPerMillion: 1.60},
		},
		{
			ID:              "gpt-4o",
			Aliases:         []string{"gpt-4o-2024-08-06", "gpt-4o-2024-11-20"},
			DisplayName:     "GPT-4o",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.50, OutputPerMillion: 10.0},
		},
		{
			ID:              "gpt-4o-mini",
			Aliases:         []string{"gpt-4o-mini-2024-07-18"},
			DisplayName:     "GPT-4o Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "o3",
			Aliases:         []string{"o3-2025-04-16"},
			DisplayName:     "o3",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 100000,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "o4-mini",
			Aliases:         []string{"o4-mini-2025-04-16"},
			DisplayName:     "o4-mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 100000,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.10, OutputPerMillion: 4.40},
		},
	}
}
