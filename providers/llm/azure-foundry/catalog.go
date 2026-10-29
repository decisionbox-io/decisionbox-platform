package azurefoundry

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Claude Opus 4.8 and the Claude 5 line share a 1M-token context window and a
// 128K max output. List prices per 1M tokens. Haiku 5.5 is tiered by prompt
// length: the constants are its prices for prompts up to 100K tokens; longer
// prompts bill at 5x.
// Sources: https://platform.claude.com/docs/en/about-claude/pricing and
// https://platform.claude.com/docs/en/about-claude/models/overview
const (
	opus48In    = 5.0
	opus48Out   = 25.0
	opus5In     = 5.0
	opus5Out    = 25.0
	opus55In    = 4.0
	opus55Out   = 20.0
	sonnet5In   = 2.0
	sonnet5Out  = 10.0
	sonnet55In  = 2.0
	sonnet55Out = 10.0
	haiku55In   = 0.10
	haiku55Out  = 0.50

	claude5Max         = 128000
	claude5InputWindow = 1000000

	// Opus 5.5 and Sonnet 5.5 read cached prompt tokens at 5% of base input
	// (other Claude models at 10%, see claudeCacheReadMultiplier).
	opus55CacheRead   = 0.20
	sonnet55CacheRead = 0.10

	// Haiku 5.5 bills a request whose prompt exceeds 100K tokens at 5x
	// every rate.
	haiku55LongContextThreshold  = 100000
	haiku55LongContextMultiplier = 5.0
)

// Anthropic prompt-cache pricing as multiples of base input price (5-minute
// cache): writing a prefix costs 1.25x, reading it back 0.1x. Filled onto
// every Claude entry that does not set its own cache prices.
// Source: https://platform.claude.com/docs/en/about-claude/pricing
const (
	claudeCacheWriteMultiplier = 1.25
	claudeCacheReadMultiplier  = 0.1
)

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
		// Opus 5.5 and Sonnet 5.5 think on every request (adaptive thinking
		// cannot be disabled) and reject a forced tool_choice with a 400.
		// Haiku 5.5 thinks only adaptively, accepts a forced tool choice
		// (verified on Bedrock 2026-10-22) and is not flagged Reasoning, so
		// it stays usable as the fast table-description (blurb) model.
		{
			ID:              "claude-opus-4-8",
			Aliases:         []string{"opus-4-8"},
			DisplayName:     "Claude Opus 4.8 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus48In, OutputPerMillion: opus48Out},
		},
		{
			ID:              "claude-opus-5",
			Aliases:         []string{"opus-5"},
			DisplayName:     "Claude Opus 5 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus5In, OutputPerMillion: opus5Out},
		},
		{
			ID:                      "claude-opus-5-5",
			Aliases:                 []string{"opus-5-5"},
			DisplayName:             "Claude Opus 5.5 (Azure Foundry)",
			Wire:                    gollm.WireAnthropic,
			MaxOutputTokens:         claude5Max,
			MaxInputTokens:          claude5InputWindow,
			Pricing:                 gollm.TokenPricing{InputPerMillion: opus55In, OutputPerMillion: opus55Out, CacheReadPerMillion: opus55CacheRead},
			Reasoning:               true,
			RejectsForcedToolChoice: true,
		},
		{
			ID:              "claude-sonnet-5",
			Aliases:         []string{"sonnet-5"},
			DisplayName:     "Claude Sonnet 5 (Azure Foundry)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnet5In, OutputPerMillion: sonnet5Out},
		},
		{
			ID:                      "claude-sonnet-5-5",
			Aliases:                 []string{"sonnet-5-5"},
			DisplayName:             "Claude Sonnet 5.5 (Azure Foundry)",
			Wire:                    gollm.WireAnthropic,
			MaxOutputTokens:         claude5Max,
			MaxInputTokens:          claude5InputWindow,
			Pricing:                 gollm.TokenPricing{InputPerMillion: sonnet55In, OutputPerMillion: sonnet55Out, CacheReadPerMillion: sonnet55CacheRead},
			Reasoning:               true,
			RejectsForcedToolChoice: true,
			AdaptiveThinking:        true,
		},
		{
			ID:               "claude-haiku-5-5",
			Aliases:          []string{"haiku-5-5"},
			DisplayName:      "Claude Haiku 5.5 (Azure Foundry)",
			Wire:             gollm.WireAnthropic,
			MaxOutputTokens:  claude5Max,
			MaxInputTokens:   claude5InputWindow,
			Pricing:          gollm.TokenPricing{InputPerMillion: haiku55In, OutputPerMillion: haiku55Out, LongContextThreshold: haiku55LongContextThreshold, LongContextMultiplier: haiku55LongContextMultiplier},
			Reasoning:        true,
			AdaptiveThinking: true,
		},
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
	// Prompt-cache prices for Claude entries that do not set their own.
	for i := range models {
		pr := &models[i].Pricing
		if models[i].Wire != gollm.WireAnthropic || pr.InputPerMillion == 0 {
			continue
		}
		if pr.CacheWritePerMillion == 0 {
			pr.CacheWritePerMillion = pr.InputPerMillion * claudeCacheWriteMultiplier
		}
		if pr.CacheReadPerMillion == 0 {
			pr.CacheReadPerMillion = pr.InputPerMillion * claudeCacheReadMultiplier
		}
	}
	return models
}
