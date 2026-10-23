package vertexai

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

// Pricing constants — match Anthropic's published list price for
// Claude on Vertex (Google partner pricing mirrors anthropic.com) and
// Google's published Gemini pricing.
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

// Output-token caps for Anthropic Claude on Vertex match Anthropic's
// published synchronous Messages-API limits.
const (
	opus47Max  = 128000
	opus46Max  = 128000
	opus45Max  = 64000
	opus41Max  = 32000
	opus4Max   = 32000
	sonnet4Max = 64000
	haiku4Max  = 64000
	geminiMax  = 65536
)

// Context-window caps. Claude on Vertex inherits Anthropic's 200K
// standard tier. Gemini 2.x supports 1M input; 1.5 Flash supports 1M
// as well (Google publishes 1M for the entire 1.5 / 2.x line). MaaS
// models vary — set per-entry below.
const (
	claude4InputWindow            = 200000
	gemini1MInputWindow           = 1000000
	llama3MaaSInputWindow         = 128000
	llama4MaverickMaaSInputWindow = 1000000
	llama4ScoutMaaSInputWindow    = 10000000
	qwenMaaSInputWindow           = 256000
	mistralMaaSInputWindow        = 128000
	deepseekMaaSInputWindow       = 128000
	gemma4MaaSInputWindow         = 262144
)

// buildVertexCatalog returns every Vertex AI model DecisionBox ships
// support for. Three wires coexist: GoogleNative for Gemini,
// Anthropic for Claude (via publishers/anthropic), OpenAICompat for
// MaaS endpoints (Llama / Qwen / DeepSeek / Mistral).
//
// Anthropic Claude canonical-ID convention follows the Anthropic
// docs' "GCP Vertex AI ID" column verbatim:
//   - Latest models (Opus 4.7, Opus 4.6, Sonnet 4.6) use the
//     *floating* ID as canonical (`claude-opus-4-7`) — Anthropic
//     recommends the floating form for these and the dated snapshot
//     becomes the alias.
//   - Legacy / single-snapshot models (Opus 4.5, Opus 4.1, Opus 4,
//     Sonnet 4.5, Sonnet 4, Haiku 4.5) use the *dated* ID as
//     canonical (`claude-opus-4-5@20251101`) — Anthropic locks the
//     snapshot and the floating form becomes the alias.
//
// The mismatch between the two schemes is not an inconsistency on
// our side — it mirrors what Anthropic's docs page lists as each
// model's primary identifier on Vertex.
func buildVertexCatalog() []gollm.ModelEntry {
	models := []gollm.ModelEntry{
		// --- Google-native — Gemini ---
		{
			ID:              "gemini-2.5-pro",
			Aliases:         []string{"gemini-2.5-pro-001", "gemini-2.5-pro-002"},
			DisplayName:     "Gemini 2.5 Pro (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			MaxInputTokens:  gemini1MInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.25, OutputPerMillion: 10.0},
		},
		{
			ID:              "gemini-2.5-flash",
			Aliases:         []string{"gemini-2.5-flash-001", "gemini-2.5-flash-002"},
			DisplayName:     "Gemini 2.5 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			MaxInputTokens:  gemini1MInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "gemini-2.0-flash",
			Aliases:         []string{"gemini-2.0-flash-001", "gemini-2.0-flash-002"},
			DisplayName:     "Gemini 2.0 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			MaxInputTokens:  gemini1MInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.10, OutputPerMillion: 0.40},
		},
		{
			ID:              "gemini-1.5-pro",
			Aliases:         []string{"gemini-1.5-pro-001", "gemini-1.5-pro-002"},
			DisplayName:     "Gemini 1.5 Pro (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			MaxInputTokens:  gemini1MInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.25, OutputPerMillion: 5.0},
		},
		{
			ID:              "gemini-1.5-flash",
			Aliases:         []string{"gemini-1.5-flash-001", "gemini-1.5-flash-002"},
			DisplayName:     "Gemini 1.5 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			MaxInputTokens:  gemini1MInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.075, OutputPerMillion: 0.30},
		},

		// --- Anthropic wire — Claude on Vertex ---
		// Opus 5.5 and Sonnet 5.5 think on every request (adaptive thinking
		// cannot be disabled) and reject a forced tool_choice with a 400.
		// Haiku 5.5 thinks only adaptively, accepts a forced tool choice
		// (verified on Bedrock 2026-10-22) and is not flagged Reasoning, so
		// it stays usable as the fast table-description (blurb) model.
		{
			ID:              "claude-opus-4-8",
			Aliases:         []string{"opus-4-8"},
			DisplayName:     "Claude Opus 4.8 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus48In, OutputPerMillion: opus48Out},
		},
		{
			ID:              "claude-opus-5",
			Aliases:         []string{"opus-5"},
			DisplayName:     "Claude Opus 5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus5In, OutputPerMillion: opus5Out},
		},
		{
			ID:                      "claude-opus-5-5",
			Aliases:                 []string{"opus-5-5"},
			DisplayName:             "Claude Opus 5.5 (Vertex)",
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
			DisplayName:     "Claude Sonnet 5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnet5In, OutputPerMillion: sonnet5Out},
		},
		{
			ID:                      "claude-sonnet-5-5",
			Aliases:                 []string{"sonnet-5-5"},
			DisplayName:             "Claude Sonnet 5.5 (Vertex)",
			Wire:                    gollm.WireAnthropic,
			MaxOutputTokens:         claude5Max,
			MaxInputTokens:          claude5InputWindow,
			Pricing:                 gollm.TokenPricing{InputPerMillion: sonnet55In, OutputPerMillion: sonnet55Out, CacheReadPerMillion: sonnet55CacheRead},
			Reasoning:               true,
			RejectsForcedToolChoice: true,
		},
		{
			ID:              "claude-haiku-5-5",
			Aliases:         []string{"haiku-5-5"},
			DisplayName:     "Claude Haiku 5.5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: claude5Max,
			MaxInputTokens:  claude5InputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: haiku55In, OutputPerMillion: haiku55Out, LongContextThreshold: haiku55LongContextThreshold, LongContextMultiplier: haiku55LongContextMultiplier},
		},
		{
			ID:              "claude-opus-4-7",
			Aliases:         []string{"opus-4-7"},
			DisplayName:     "Claude Opus 4.7 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus47Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus47In, OutputPerMillion: opus47Out},
		},
		{
			ID:              "claude-opus-4-6",
			Aliases:         []string{"claude-opus-4-6@20251101", "opus-4-6"},
			DisplayName:     "Claude Opus 4.6 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus46Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus46In, OutputPerMillion: opus46Out},
		},
		{
			ID:              "claude-opus-4-5@20251101",
			Aliases:         []string{"claude-opus-4-5", "opus-4-5"},
			DisplayName:     "Claude Opus 4.5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus45Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus45In, OutputPerMillion: opus45Out},
		},
		{
			ID:              "claude-opus-4-1@20250805",
			Aliases:         []string{"claude-opus-4-1", "opus-4-1"},
			DisplayName:     "Claude Opus 4.1 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus41Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus41In, OutputPerMillion: opus41Out},
		},
		{
			ID:              "claude-opus-4@20250514",
			Aliases:         []string{"claude-opus-4", "opus-4"},
			DisplayName:     "Claude Opus 4 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus4In, OutputPerMillion: opus4Out},
		},
		{
			ID:              "claude-sonnet-4-6",
			Aliases:         []string{"claude-sonnet-4-6@20251101", "sonnet-4-6"},
			DisplayName:     "Claude Sonnet 4.6 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-sonnet-4-5@20250929",
			Aliases:         []string{"claude-sonnet-4-5", "sonnet-4-5"},
			DisplayName:     "Claude Sonnet 4.5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-sonnet-4@20250514",
			Aliases:         []string{"claude-sonnet-4", "sonnet-4"},
			DisplayName:     "Claude Sonnet 4 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-haiku-4-5@20251001",
			Aliases:         []string{"claude-haiku-4-5", "haiku-4-5"},
			DisplayName:     "Claude Haiku 4.5 (Vertex)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: haiku4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: haikuIn, OutputPerMillion: haikuOut},
		},

		// --- OpenAI-compat — Vertex Model Garden MaaS ---
		{
			ID:              "meta/llama-3.3-70b-instruct-maas",
			DisplayName:     "Llama 3.3 70B Instruct (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  llama3MaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.72, OutputPerMillion: 0.72},
		},
		{
			// Llama 4 Maverick + Scout MaaS endpoints use the
			// "-maas"-suffixed publisher IDs and are served only from
			// us-east5 (global / us-central1 return 404). Vertex caps
			// max output at 8192 for both — a higher maxOutputTokens
			// 400s with "supported range ... to 8193 (exclusive)".
			ID:              "meta/llama-4-maverick-17b-128e-instruct-maas",
			DisplayName:     "Llama 4 Maverick 17B 128E (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  llama4MaverickMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.35, OutputPerMillion: 1.15},
		},
		{
			ID:              "meta/llama-4-scout-17b-16e-instruct-maas",
			DisplayName:     "Llama 4 Scout 17B 16E (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			MaxInputTokens:  llama4ScoutMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.25, OutputPerMillion: 0.70},
		},
		{
			ID:              "qwen/qwen3-coder-480b-a35b-instruct-maas",
			DisplayName:     "Qwen3 Coder 480B A35B (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  qwenMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 8.0},
		},
		{
			// Vertex Model Garden MaaS chat-capable endpoint for
			// Mistral Large requires the "-maas" suffix; the bare
			// `mistral-ai/mistral-large-2411-001` ID is the publisher
			// listing, not the chat endpoint.
			ID:              "mistral-ai/mistral-large-2411-001-maas",
			Aliases:         []string{"mistral-ai/mistral-large-2411-001"},
			DisplayName:     "Mistral Large 2411 (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 4096,
			MaxInputTokens:  mistralMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 3.0, OutputPerMillion: 9.0},
		},
		{
			// Same: DeepSeek R1's chat-capable MaaS endpoint is the
			// "-maas"-suffixed snapshot ID.
			ID:              "deepseek-ai/deepseek-r1-0528-maas",
			Aliases:         []string{"deepseek-ai/deepseek-r1"},
			DisplayName:     "DeepSeek R1 (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  deepseekMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.35, OutputPerMillion: 5.40},
			Reasoning:       true,
		},
		{
			// Same: DeepSeek V3.2's chat-capable MaaS endpoint is the
			// "-maas"-suffixed ID. 128K context, 32K max output.
			ID:              "deepseek-ai/deepseek-v3.2-maas",
			Aliases:         []string{"deepseek-ai/deepseek-v3.2"},
			DisplayName:     "DeepSeek V3.2 (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
			MaxInputTokens:  deepseekMaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.56, OutputPerMillion: 1.68},
		},
		{
			// Gemma 4 on Vertex MaaS speaks the OpenAI-compat wire and
			// is served from global. Max output 128K; Vertex does not
			// enforce a lower cap (a 200K maxOutputTokens request is
			// accepted). Emits a reasoning trace, so Reasoning=true.
			ID:              "google/gemma-4-26b-a4b-it-maas",
			DisplayName:     "Gemma 4 26B A4B (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 128000,
			MaxInputTokens:  gemma4MaaSInputWindow,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.06, OutputPerMillion: 0.33},
			Reasoning:       true,
		},
	}
	// Claude 4.x on Vertex shares the 200K standard context window —
	// fill once rather than duplicate the value on every entry.
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
