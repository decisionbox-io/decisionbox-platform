package vertexai

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

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
	return []gollm.ModelEntry{
		// --- Google-native — Gemini ---
		{
			ID:              "gemini-2.5-pro",
			Aliases:         []string{"gemini-2.5-pro-001", "gemini-2.5-pro-002"},
			DisplayName:     "Gemini 2.5 Pro (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.25, OutputPerMillion: 10.0},
		},
		{
			ID:              "gemini-2.5-flash",
			Aliases:         []string{"gemini-2.5-flash-001", "gemini-2.5-flash-002"},
			DisplayName:     "Gemini 2.5 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60},
		},
		{
			ID:              "gemini-2.0-flash",
			Aliases:         []string{"gemini-2.0-flash-001", "gemini-2.0-flash-002"},
			DisplayName:     "Gemini 2.0 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.10, OutputPerMillion: 0.40},
		},
		{
			ID:              "gemini-1.5-pro",
			Aliases:         []string{"gemini-1.5-pro-001", "gemini-1.5-pro-002"},
			DisplayName:     "Gemini 1.5 Pro (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.25, OutputPerMillion: 5.0},
		},
		{
			ID:              "gemini-1.5-flash",
			Aliases:         []string{"gemini-1.5-flash-001", "gemini-1.5-flash-002"},
			DisplayName:     "Gemini 1.5 Flash (Vertex)",
			Wire:            gollm.WireGoogleNative,
			MaxOutputTokens: geminiMax,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.075, OutputPerMillion: 0.30},
		},

		// --- Anthropic wire — Claude on Vertex ---
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
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.72, OutputPerMillion: 0.72},
		},
		{
			ID:              "meta/llama-4-maverick-17b-instruct-maas",
			DisplayName:     "Llama 4 Maverick 17B (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 8192,
			Pricing:         gollm.TokenPricing{InputPerMillion: 0.35, OutputPerMillion: 1.40},
		},
		{
			ID:              "qwen/qwen3-coder-480b-a35b-instruct-maas",
			DisplayName:     "Qwen3 Coder 480B A35B (Vertex MaaS)",
			Wire:            gollm.WireOpenAICompat,
			MaxOutputTokens: 32768,
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
			Pricing:         gollm.TokenPricing{InputPerMillion: 1.35, OutputPerMillion: 5.40},
		},
	}
}
