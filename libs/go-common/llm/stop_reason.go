package llm

import "strings"

// Reading "the response was cut off at the output limit" off a ChatResponse.
//
// Every provider reports it, and every provider spells it differently:
//
//   - OpenAI-compatible wires (OpenAI, Azure OpenAI, LiteLLM, vLLM, Ollama)
//     report finish_reason "length".
//   - The Anthropic wire (Claude direct, Bedrock, Vertex-Anthropic,
//     Azure-Foundry-Claude) reports stop_reason "max_tokens".
//   - Vertex / Gemini reports finishReason "MAX_TOKENS", upper-cased.
//
// Callers that generate structured output need this: a reply cut off at the
// cap is not malformed output the model can be re-prompted out of, it is a
// reply that needs a smaller request. Telling the two apart from the text
// alone is guesswork; the stop reason says so directly.

// StopReasonMaxTokens and StopReasonLength are the two wire spellings of the
// output-limit stop reason, lower-cased. Vertex's upper-cased "MAX_TOKENS"
// folds onto the first.
const (
	StopReasonMaxTokens = "max_tokens"
	StopReasonLength    = "length"
)

// IsLengthStopReason reports whether stopReason means the provider stopped
// generating because the response reached the requested output limit, in any
// of the spellings above. Case- and space-insensitive, so an upper-cased or
// padded reason is still recognised. An empty or unknown reason is false:
// providers that report nothing must be judged on the token count instead,
// never guessed at from the reason.
func IsLengthStopReason(stopReason string) bool {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case StopReasonMaxTokens, StopReasonLength:
		return true
	}
	return false
}

// isToolStopReason reports whether stopReason is a provider's "the model
// called a tool" reason — the only reason a folded structured-output reply is
// allowed to rewrite (see NormalizeStructuredToolResponse).
func isToolStopReason(stopReason string) bool {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "tool_use", "tool_calls":
		return true
	}
	return false
}
