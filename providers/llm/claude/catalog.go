package claude

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// Pricing & cap constants — Anthropic published list price.
// Source: https://platform.claude.com/docs/en/about-claude/pricing
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

	opus47Max  = 128000
	opus46Max  = 128000
	opus45Max  = 64000
	opus41Max  = 32000
	opus4Max   = 32000
	sonnet4Max = 64000
	haiku4Max  = 64000

	// Anthropic-published context windows (input + output combined).
	// Source: https://platform.claude.com/docs/en/docs/about-claude/models/overview
	//
	// The standard tier is 200K across the Claude 4.x line; long-context
	// (1M) is a beta opt-in keyed off a separate header and not yet
	// surfaced here. Until that lands we keep the conservative 200K
	// number — over-trimming is recoverable, under-counting and 4xx is
	// not.
	claude4InputWindow = 200000
)

// buildClaudeCatalog returns Anthropic's published Claude API model
// roster. Aliases cover both the floating "claude-opus-4-7" form and
// the date-stamped snapshot a contract may pin to (e.g.
// "claude-opus-4-5-20251101"), plus the user-friendly short form
// "opus-4-7" per the alias rule.
//
// Source for IDs and aliases:
// https://platform.claude.com/docs/en/docs/about-claude/models/overview
func buildClaudeCatalog() []gollm.ModelEntry {
	models := []gollm.ModelEntry{
		{
			ID:              "claude-opus-4-7",
			Aliases:         []string{"opus-4-7"},
			DisplayName:     "Claude Opus 4.7",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus47Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus47In, OutputPerMillion: opus47Out},
		},
		{
			ID:              "claude-opus-4-6",
			Aliases:         []string{"opus-4-6"},
			DisplayName:     "Claude Opus 4.6",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus46Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus46In, OutputPerMillion: opus46Out},
		},
		{
			ID:              "claude-opus-4-5-20251101",
			Aliases:         []string{"claude-opus-4-5", "opus-4-5"},
			DisplayName:     "Claude Opus 4.5",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus45Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus45In, OutputPerMillion: opus45Out},
		},
		{
			ID:              "claude-opus-4-1-20250805",
			Aliases:         []string{"claude-opus-4-1", "opus-4-1"},
			DisplayName:     "Claude Opus 4.1",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus41Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus41In, OutputPerMillion: opus41Out},
		},
		{
			ID:              "claude-opus-4-20250514",
			Aliases:         []string{"claude-opus-4-0", "claude-opus-4", "opus-4"},
			DisplayName:     "Claude Opus 4 (legacy)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: opus4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: opus4In, OutputPerMillion: opus4Out},
			Lifecycle:       "LEGACY",
		},
		{
			ID:              "claude-sonnet-4-6",
			Aliases:         []string{"sonnet-4-6"},
			DisplayName:     "Claude Sonnet 4.6",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-sonnet-4-5-20250929",
			Aliases:         []string{"claude-sonnet-4-5", "sonnet-4-5"},
			DisplayName:     "Claude Sonnet 4.5",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
		},
		{
			ID:              "claude-sonnet-4-20250514",
			Aliases:         []string{"claude-sonnet-4-0", "claude-sonnet-4", "sonnet-4"},
			DisplayName:     "Claude Sonnet 4 (legacy)",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: sonnet4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: sonnetIn, OutputPerMillion: sonnetOut},
			Lifecycle:       "LEGACY",
		},
		{
			ID:              "claude-haiku-4-5-20251001",
			Aliases:         []string{"claude-haiku-4-5", "haiku-4-5"},
			DisplayName:     "Claude Haiku 4.5",
			Wire:            gollm.WireAnthropic,
			MaxOutputTokens: haiku4Max,
			Pricing:         gollm.TokenPricing{InputPerMillion: haikuIn, OutputPerMillion: haikuOut},
		},
	}
	// Every Claude 4.x model exposes the same 200K standard context
	// window — fill MaxInputTokens uniformly rather than duplicating
	// the value on each entry.
	for i := range models {
		models[i].MaxInputTokens = claude4InputWindow
	}
	return models
}
