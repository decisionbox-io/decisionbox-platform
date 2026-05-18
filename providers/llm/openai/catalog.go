package openai

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Tiktoken encoding name for OpenAI's modern lineup. GPT-4o /
// GPT-4.1 / GPT-5 / o-series all tokenize with o200k_base; the
// TokenCounter reads this from each catalog entry's Encoding field
// to pick the right BPE for an exact count.
//
// New encodings ship with new model generations — catalog entries
// must update Encoding alongside ID when that happens, otherwise
// counts will drift silently.
const encO200KBase = "o200k_base"

// Published context-window sizes (input + output combined). Sources:
//   - GPT-5 family: 400K context (https://platform.openai.com/docs/models/gpt-5)
//   - GPT-4.1 family: 1M context (https://platform.openai.com/docs/models/gpt-4.1)
//   - GPT-4o family: 128K context (https://platform.openai.com/docs/models/gpt-4o)
//   - o-series (o3/o4-mini): 200K context
const (
	gpt5InputWindow    = 400000
	gpt41InputWindow   = 1000000
	gpt4oInputWindow   = 128000
	oSeriesInputWindow = 200000
)

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
			MaxInputTokens:  gpt5InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 5.0, OutputPerMillion: 15.0},
		},
		{
			ID:              "gpt-5-mini",
			DisplayName:     "GPT-5 Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			MaxInputTokens:  gpt5InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.30, OutputPerMillion: 1.20},
		},
		{
			ID:              "gpt-5-nano",
			DisplayName:     "GPT-5 Nano",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			MaxInputTokens:  gpt5InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.05, OutputPerMillion: 0.40},
		},
		{
			ID:              "gpt-4.1",
			Aliases:         []string{"gpt-4.1-2025-04-14"},
			DisplayName:     "GPT-4.1",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  gpt41InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "gpt-4.1-mini",
			Aliases:         []string{"gpt-4.1-mini-2025-04-14"},
			DisplayName:     "GPT-4.1 Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  gpt41InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.40, OutputPerMillion: 1.60},
		},
		{
			ID:              "gpt-4.1-nano",
			Aliases:         []string{"gpt-4.1-nano-2025-04-14"},
			DisplayName:     "GPT-4.1 Nano",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  gpt41InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.10, OutputPerMillion: 0.40},
		},
		{
			ID:              "gpt-4o",
			Aliases:         []string{"gpt-4o-2024-08-06", "gpt-4o-2024-11-20"},
			DisplayName:     "GPT-4o",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			MaxInputTokens:  gpt4oInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.50, OutputPerMillion: 10.0},
		},
		{
			ID:              "gpt-4o-mini",
			Aliases:         []string{"gpt-4o-mini-2024-07-18"},
			DisplayName:     "GPT-4o Mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 16384,
			MaxInputTokens:  gpt4oInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "o3",
			Aliases:         []string{"o3-2025-04-16"},
			DisplayName:     "o3",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 100000,
			MaxInputTokens:  oSeriesInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "o4-mini",
			Aliases:         []string{"o4-mini-2025-04-16"},
			DisplayName:     "o4-mini",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 100000,
			MaxInputTokens:  oSeriesInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.10, OutputPerMillion: 4.40},
		},
	}
}

// FallbackEncoding is the BPE the OpenAI TokenCounter falls back to
// when a model has no catalog entry (e.g. a freshly released
// snapshot, or a custom proxy ID). o200k_base matches every modern
// OpenAI model and is the safest default.
const FallbackEncoding = encO200KBase
