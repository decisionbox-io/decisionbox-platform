package bedrock

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Pricing constants for Anthropic Claude on Bedrock. Match Anthropic's
// published list price (which Bedrock mirrors via marketplace billing)
// — see https://platform.claude.com/docs/en/about-claude/pricing.
//
// 4.5 / 4.6 / 4.7 share the same Opus tier ($5/$25); the legacy
// Opus 4 / 4.1 retain the older $15/$75 pricing.
const (
	opus47In  = 5.0
	opus47Out = 25.0
	opus46In  = 5.0
	opus46Out = 25.0
	opus45In  = 5.0
	opus45Out = 25.0
	opus41In  = 15.0
	opus41Out = 75.0
	opus4In   = 15.0
	opus4Out  = 75.0

	sonnetIn  = 3.0
	sonnetOut = 15.0
	haikuIn   = 1.0
	haikuOut  = 5.0
)

// Anthropic Claude max-output-token caps on Bedrock match Anthropic's
// published synchronous Messages-API limits. Source:
// https://platform.claude.com/docs/en/docs/about-claude/models/overview
const (
	opus47Max  = 128000
	opus46Max  = 128000
	opus45Max  = 64000
	opus41Max  = 32000
	opus4Max   = 32000
	sonnet4Max = 64000
	haiku4Max  = 64000

	// Context-window caps. Claude 4.x on Bedrock mirrors Anthropic's
	// 200K standard tier. Open-source models on Bedrock vary, so each
	// catalog entry declares its own; the defaults below cover the
	// common case where the entry doesn't override.
	claude4InputWindow = 200000

	qwenInputWindow         = 128000 // Qwen3 family (32K–128K depending on variant; 128K is the upper)
	deepseekR1InputWindow   = 128000 // DeepSeek R1 (Bedrock-published)
	mixtral8x22BInputWindow = 65536  // Mistral Mixtral 8x22B
	mistralLargeInputWindow = 128000 // Mistral Large 2407
	llama3InputWindow       = 128000 // Meta Llama 3.3 70B Instruct
	llama4InputWindow       = 1000000 // Meta Llama 4 Maverick 17B (long-context)
)

// buildBedrockCatalog returns every Bedrock model DecisionBox ships
// support for, with comprehensive aliases covering every cross-region
// inference profile (us. / eu. / apac. / jp. / au. / global.), every
// Bedrock version-suffix variant (-v1:0, -v1, no suffix), and the
// short family forms users may paste in (claude-opus-4-7, opus-4-7).
//
// Adding a new model is a single ModelEntry — alias generation is
// programmatic so the matrix stays in sync as AWS adds new geos.
func buildBedrockCatalog() []gollm.ModelEntry {
	models := []gollm.ModelEntry{
		// --- Anthropic wire — Claude on Bedrock ---
		{
			ID:              "anthropic.claude-opus-4-7-v1:0",
			Aliases:         claudeAliasesFor("opus-4-7"),
			DisplayName:     "Claude Opus 4.7 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus47Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus47In, OutputPerMillion: opus47Out},
		},
		{
			ID:              "anthropic.claude-opus-4-6-v1:0",
			Aliases:         claudeAliasesFor("opus-4-6"),
			DisplayName:     "Claude Opus 4.6 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus46Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus46In, OutputPerMillion: opus46Out},
		},
		{
			ID:              "anthropic.claude-opus-4-5-20251101-v1:0",
			Aliases:         claudeAliasesFor("opus-4-5-20251101"),
			DisplayName:     "Claude Opus 4.5 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus45Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus45In, OutputPerMillion: opus45Out},
		},
		{
			ID:              "anthropic.claude-opus-4-1-20250805-v1:0",
			Aliases:         claudeAliasesFor("opus-4-1-20250805"),
			DisplayName:     "Claude Opus 4.1 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus41Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus41In, OutputPerMillion: opus41Out},
		},
		{
			ID:              "anthropic.claude-opus-4-20250514-v1:0",
			Aliases:         claudeAliasesFor("opus-4-20250514"),
			DisplayName:     "Claude Opus 4 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus4In, OutputPerMillion: opus4Out},
		},
		{
			ID:              "anthropic.claude-sonnet-4-6-v1:0",
			Aliases:         claudeAliasesFor("sonnet-4-6"),
			DisplayName:     "Claude Sonnet 4.6 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "anthropic.claude-sonnet-4-5-20250929-v1:0",
			Aliases:         claudeAliasesFor("sonnet-4-5-20250929"),
			DisplayName:     "Claude Sonnet 4.5 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "anthropic.claude-sonnet-4-20250514-v1:0",
			Aliases:         claudeAliasesFor("sonnet-4-20250514"),
			DisplayName:     "Claude Sonnet 4 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "anthropic.claude-haiku-4-5-20251001-v1:0",
			Aliases:         claudeAliasesFor("haiku-4-5-20251001"),
			DisplayName:     "Claude Haiku 4.5 (Bedrock)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: haiku4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: haikuIn, OutputPerMillion: haikuOut},
		},

		// --- OpenAI-compat wire — Qwen / DeepSeek / Mistral / Llama on Bedrock ---
		{
			ID:              "qwen.qwen3-next-80b-a3b",
			Aliases:         openSourceAliasesFor("qwen.qwen3-next-80b-a3b"),
			DisplayName:     "Qwen3-next 80B A3B (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  qwenInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.22, OutputPerMillion: 0.88},
		},
		{
			ID:              "qwen.qwen3-coder-30b-a3b-v1:0",
			Aliases:         openSourceAliasesFor("qwen.qwen3-coder-30b-a3b-v1:0"),
			DisplayName:     "Qwen3 Coder 30B A3B (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  qwenInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.18, OutputPerMillion: 0.72},
		},
		{
			ID:              "qwen.qwen3-32b-v1:0",
			Aliases:         openSourceAliasesFor("qwen.qwen3-32b-v1:0"),
			DisplayName:     "Qwen3 32B (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  qwenInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.18, OutputPerMillion: 0.72},
		},
		{
			ID:              "deepseek.r1-v1:0",
			Aliases:         openSourceAliasesFor("deepseek.r1-v1:0"),
			DisplayName:     "DeepSeek R1 (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  deepseekR1InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.35, OutputPerMillion: 5.40},
		},
		{
			ID:              "mistral.mixtral-8x22b-v1:0",
			Aliases:         openSourceAliasesFor("mistral.mixtral-8x22b-v1:0"),
			DisplayName:     "Mixtral 8x22B (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  mixtral8x22BInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.60, OutputPerMillion: 1.80},
		},
		{
			ID:              "mistral.mistral-large-2407-v1:0",
			Aliases:         openSourceAliasesFor("mistral.mistral-large-2407-v1:0"),
			DisplayName:     "Mistral Large 2407 (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 4096,
			MaxInputTokens:  mistralLargeInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 3.0, OutputPerMillion: 9.0},
		},
		{
			ID:              "meta.llama3-3-70b-instruct-v1:0",
			Aliases:         openSourceAliasesFor("meta.llama3-3-70b-instruct-v1:0"),
			DisplayName:     "Llama 3.3 70B Instruct (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  llama3InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.72, OutputPerMillion: 0.72},
		},
		{
			ID:              "meta.llama4-maverick-17b-instruct-v1:0",
			Aliases:         openSourceAliasesFor("meta.llama4-maverick-17b-instruct-v1:0"),
			DisplayName:     "Llama 4 Maverick 17B (Bedrock)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  llama4InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.35, OutputPerMillion: 1.40},
		},
	}
	// Claude 4.x entries on Bedrock share the same standard 200K
	// context window — fill once rather than duplicate per row.
	for i := range models {
		if models[i].Wire == gollm.WireAnthropic && models[i].MaxInputTokens == 0 {
			models[i].MaxInputTokens = claude4InputWindow
		}
	}
	return models
}
