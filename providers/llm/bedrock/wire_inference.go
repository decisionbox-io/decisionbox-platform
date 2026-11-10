package bedrock

import (
	"strconv"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// inferBedrockWire is the FamilyInferrer wired into ProviderMeta as
// the third resolution tier after the catalog and wire_override.
// Recognises a Bedrock model ID by its publisher prefix so that a
// freshly-released Claude / Qwen / DeepSeek / Mistral / Llama variant
// dispatches correctly even when the catalog has no row for it yet.
//
// Unlisted families (amazon.nova-*, amazon.titan-*, cohere.*, ai21.*)
// have no compatible wire on Bedrock and stay WireUnknown — the UI
// marks them non-dispatchable rather than guessing a wrong wire.
//
// Order matters: cross-region prefixes (us./eu./apac./jp./au./global.)
// are checked first so e.g. "us.anthropic." is recognised before the
// bare "anthropic." prefix.
func inferBedrockWire(model string) gollm.Wire {
	for _, prefix := range claudeRegionPrefixes {
		if strings.HasPrefix(model, prefix+"anthropic.") {
			return gollm.WireAnthropic
		}
		for _, openSource := range bedrockOpenSourcePrefixes {
			if strings.HasPrefix(model, prefix+openSource) {
				return gollm.WireOpenAICompat
			}
		}
	}
	if strings.HasPrefix(model, "anthropic.") {
		return gollm.WireAnthropic
	}
	for _, openSource := range bedrockOpenSourcePrefixes {
		if strings.HasPrefix(model, openSource) {
			return gollm.WireOpenAICompat
		}
	}
	return gollm.WireUnknown
}

// bedrockOpenSourcePrefixes are the publisher prefixes Bedrock uses
// for non-Anthropic foundation models that speak the OpenAI Chat
// Completions wire today. Matches what AWS exposes via
// ListFoundationModels.
var bedrockOpenSourcePrefixes = []string{"qwen.", "deepseek.", "mistral.", "meta."}

// bedrockUsesMaxCompletionTokens reports whether an OpenAI GPT model on
// Bedrock requires `max_completion_tokens` instead of `max_tokens`. The split
// is per generation, not per family: gpt-5.4 / gpt-5.5 profiles accept only
// `max_tokens`, while gpt-5.6 and gpt-6 onwards reject it with a 400
// ValidationException ("Unsupported parameter: 'max_tokens'"). Region
// qualifiers ("us.") and the "openai." publisher prefix are stripped before
// the version is read, so both the bare model ID and every cross-region
// inference profile resolve the same way. Anything that is not a versioned
// "gpt-" ID (gpt-oss, Qwen, DeepSeek, …) keeps `max_tokens`.
func bedrockUsesMaxCompletionTokens(model string) bool {
	m := strings.ToLower(model)
	for _, prefix := range claudeRegionPrefixes {
		if strings.HasPrefix(m, prefix) {
			m = strings.TrimPrefix(m, prefix)
			break
		}
	}
	m = strings.TrimPrefix(m, "openai.")
	rest, ok := strings.CutPrefix(m, "gpt-")
	if !ok {
		return false
	}
	major, rest := leadingInt(rest)
	if major < 0 {
		return false
	}
	minor := 0
	if after, ok := strings.CutPrefix(rest, "."); ok {
		if n, _ := leadingInt(after); n >= 0 {
			minor = n
		}
	}
	return major > 5 || (major == 5 && minor >= 6)
}

// leadingInt parses the run of ASCII digits at the start of s and returns it
// with the remainder of s. It returns -1 when s does not start with a digit.
func leadingInt(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return -1, s
	}
	return n, s[i:]
}
