package vertexai

import (
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// inferVertexWire is the FamilyInferrer wired into ProviderMeta.
// Recognises a Vertex model ID by its publisher prefix.
//
// Gemini IDs have no publisher prefix in the rawPredict /
// generateContent URL; Claude IDs are the Anthropic form (optionally
// suffixed with @YYYYMMDD).
//
// Third-party MaaS models (Llama / Qwen / DeepSeek / Mistral) are
// published on Vertex via the /endpoints/openapi chat-completions
// endpoint, and Google publishes their chat-capable variants with an
// explicit "-maas" suffix (e.g. meta/llama-3.3-70b-instruct-maas,
// qwen/qwen3-coder-480b-a35b-instruct-maas). Non-chat models share
// the same publisher prefix (meta/sam3, qwen/qwen-image,
// deepseek-ai/deepseek-ocr, …) — they would mis-dispatch as
// OpenAI-compat chat, so we require the -maas suffix to call them
// dispatchable.
//
// Gemini's non-chat variants (gemini-embedding-*, gemini-2.5-*-tts,
// *-image, *-image-preview) are also on the google publisher but are
// filtered at a higher level if they don't accept generateContent —
// we leave them in for now since the agent will surface a clear 400
// at first Chat rather than silently picking a bad default.
func inferVertexWire(model string) gollm.Wire {
	switch {
	case strings.HasPrefix(model, "gemini-"):
		return gollm.WireGoogleNative
	case strings.HasPrefix(model, "claude-"):
		return gollm.WireAnthropic
	case strings.HasPrefix(model, "meta/"),
		strings.HasPrefix(model, "mistral-ai/"),
		strings.HasPrefix(model, "qwen/"),
		strings.HasPrefix(model, "deepseek-ai/"),
		strings.HasPrefix(model, "meta-llama/"):
		if strings.HasSuffix(model, "-maas") {
			return gollm.WireOpenAICompat
		}
		return gollm.WireUnknown
	}
	return gollm.WireUnknown
}
