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
)

// buildAzureFoundryCatalog returns Azure AI Foundry models with
// their wire and caps. Customer deployment names on Foundry can
// rename the underlying model — the resolver matches on whatever
// the user types so a deployment called "claude-haiku-4-5-prod"
// still resolves through the FamilyInferrer.
func buildAzureFoundryCatalog() []gollm.ModelEntry {
	return []gollm.ModelEntry{
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
			Pricing:         gollm.TokenPricing{InputPerMillion: 5.0, OutputPerMillion: 15.0},
		},
		{
			ID:              "gpt-5-mini",
			DisplayName:     "GPT-5 Mini (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt5Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.30, OutputPerMillion: 1.20},
		},
		{
			ID:              "gpt-4.1",
			DisplayName:     "GPT-4.1 (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt41Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			ID:              "gpt-4o",
			DisplayName:     "GPT-4o (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt4oMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.50, OutputPerMillion: 10.0},
		},
		{
			ID:              "gpt-4o-mini",
			DisplayName:     "GPT-4o Mini (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: gpt4oMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "mistral-large-2411",
			DisplayName:     "Mistral Large 2411 (Azure Foundry)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 4096,
			Pricing:         gollm.TokenPricing{InputPerMillion: 3.0, OutputPerMillion: 9.0},
		},
	}
}
