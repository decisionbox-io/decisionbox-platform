package bedrock

import (
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

// bedrockUsesMaxCompletionTokens reports whether a Bedrock model should get
// `max_completion_tokens` instead of `max_tokens`. Every OpenAI model on
// Bedrock (gpt-5.x, gpt-6, gpt-oss) accepts the new name, and gpt-5.6 /
// gpt-6 onwards reject `max_tokens` with a 400 ValidationException, so the
// switch is made for the whole "openai." publisher. Region qualifiers ("us.")
// are stripped first so cross-region inference profiles resolve the same way.
func bedrockUsesMaxCompletionTokens(model string) bool {
	m := strings.ToLower(model)
	for _, prefix := range claudeRegionPrefixes {
		if strings.HasPrefix(m, prefix) {
			m = strings.TrimPrefix(m, prefix)
			break
		}
	}
	return strings.HasPrefix(m, "openai.")
}
