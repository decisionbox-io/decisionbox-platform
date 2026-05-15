package azurefoundry

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Pricing & cap constants — Anthropic on Azure Marketplace mirrors
// Anthropic's published list price; OpenAI on Foundry mirrors OpenAI
// list. Sources: platform.claude.com/docs/en/about-claude/pricing
// and openai.com/pricing.
const (
	opus47In  = 5.0
	opus47Out = 25.0
	opus46In  = 5.0
	opus46Out = 25.0
	opus45In  = 5.0
	opus45Out = 25.0
	opus41In  = 15.0
	opus41Out = 75.0
	sonnetIn  = 3.0
	sonnetOut = 15.0
	haikuIn   = 1.0
	haikuOut  = 5.0

	opus47Max  = 128000
	opus46Max  = 128000
	opus45Max  = 64000
	opus41Max  = 32000
	sonnet4Max = 64000
	haiku4Max  = 64000

	gpt5Max  = 16384
	gpt4oMax = 16384
	gpt41Max = 32768

	// Context-window caps. Anthropic on Foundry mirrors Anthropic's
	// 200K standard tier; OpenAI on Foundry mirrors OpenAI's per-model
	// window; Mistral Large on Foundry follows the upstream 128K.
	claude4InputWindow      = 200000
	gpt5InputWindow         = 400000
	gpt41InputWindow        = 1000000
	gpt4oInputWindow        = 128000
	mistralLargeInputWindow = 128000

	// Tiktoken encoding for OpenAI models on Foundry — matches the
	// direct-OpenAI catalog. The provider's TokenCounter reads this
	// when assembling an exact count for an OpenAI-wire model.
	encO200KBase = "o200k_base"
)

// buildAzureFoundryCatalog returns Azure AI Foundry models with
// their wire and caps. Customer deployment names on Foundry can
// rename the underlying model — the resolver matches on whatever
// the user types so a deployment called "claude-haiku-4-5-prod"
// still resolves through the FamilyInferrer.
func buildAzureFoundryCatalog() []gollm.ModelEntry {
	models := []gollm.ModelEntry{
		// --- Anthropic Claude on Foundry ---
		{
			ID:              "claude-opus-4-7",
			Aliases:         []string{"opus-4-7"},
			DisplayName:     "Claude Opus 4.7 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus47Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus47In, OutputPerMillion: opus47Out},
		},
		{
			ID:              "claude-opus-4-6",
			Aliases:         []string{"opus-4-6"},
			DisplayName:     "Claude Opus 4.6 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus46Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus46In, OutputPerMillion: opus46Out},
		},
		{
			ID:              "claude-opus-4-5",
			Aliases:         []string{"opus-4-5"},
			DisplayName:     "Claude Opus 4.5 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus45Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus45In, OutputPerMillion: opus45Out},
		},
		{
			ID:              "claude-opus-4-1",
			Aliases:         []string{"opus-4-1"},
			DisplayName:     "Claude Opus 4.1 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus41Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus41In, OutputPerMillion: opus41Out},
		},
		{
			ID:              "claude-sonnet-4-6",
			Aliases:         []string{"sonnet-4-6"},
			DisplayName:     "Claude Sonnet 4.6 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-sonnet-4-5",
			Aliases:         []string{"sonnet-4-5"},
			DisplayName:     "Claude Sonnet 4.5 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-haiku-4-5",
			Aliases:         []string{"haiku-4-5"},
			DisplayName:     "Claude Haiku 4.5 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: haiku4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: haikuIn, OutputPerMillion: haikuOut},
		},

		// --- OpenAI on Foundry ---
		{
			ID:              "gpt-5",
			DisplayName:     "GPT-5 (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt5Max,
			MaxInputTokens:  gpt5InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 5.0, OutputPerMillion: 15.0},
		},
		{
			ID:              "gpt-5-mini",
			DisplayName:     "GPT-5 Mini (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt5Max,
			MaxInputTokens:  gpt5InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.30, OutputPerMillion: 1.20},
		},
		{
			ID:              "gpt-4.1",
			DisplayName:     "GPT-4.1 (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt41Max,
			MaxInputTokens:  gpt41InputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "gpt-4o",
			DisplayName:     "GPT-4o (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt4oMax,
			MaxInputTokens:  gpt4oInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.50, OutputPerMillion: 10.0},
		},
		{
			ID:              "gpt-4o-mini",
			DisplayName:     "GPT-4o Mini (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt4oMax,
			MaxInputTokens:  gpt4oInputWindow,
			Encoding:        encO200KBase,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "mistral-large-2411",
			DisplayName:     "Mistral Large 2411 (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 4096,
			MaxInputTokens:  mistralLargeInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 3.0, OutputPerMillion: 9.0},
		},
	}
	// Claude 4.x on Foundry mirrors Anthropic's 200K standard tier —
	// fill once rather than duplicate on every Anthropic-wire entry.
	for i := range models {
		if models[i].Wire == gollm.WireAnthropic && models[i].MaxInputTokens == 0 {
			models[i].MaxInputTokens = claude4InputWindow
		}
	}
	return models
}
