package vertexai

import (
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// inferVertexWire maps a Vertex model ID to the wire it speaks based on
// the publisher prefix. Gemini IDs have no publisher prefix in the
// rawPredict / generateContent URL; Claude IDs are the Anthropic form
// (optionally suffixed with @YYYYMMDD).
//
// Third-party MaaS models (Llama / Qwen / DeepSeek / Mistral) are
// published on Vertex via the /endpoints/openapi chat-completions
// endpoint, and Google publishes their chat-capable variants with an
// explicit "-maas" suffix (e.g. meta/llama-3.3-70b-instruct-maas,
// qwen/qwen3-coder-480b-a35b-instruct-maas). Many non-chat models
// share the same publisher prefix (meta/sam3, qwen/qwen-image,
// deepseek-ai/deepseek-ocr, meta/faster-r-cnn, …) — they would be
// mis-dispatched as OpenAI-compat chat, so we require the -maas
// suffix to call them dispatchable.
//
// Gemini's non-chat variants (gemini-embedding-*, gemini-2.5-*-tts,
// *-image, *-image-preview) are also on the google publisher but are
// filtered at a higher level if they don't accept generateContent —
// we leave them in for now since the agent will surface a clear 400
// at first Chat rather than silently picking a bad default.
func inferVertexWire(id string) modelcatalog.Wire {
	switch {
	case strings.HasPrefix(id, "gemini-"):
		return modelcatalog.GoogleNative
	case strings.HasPrefix(id, "claude-"):
		return modelcatalog.Anthropic
	case strings.HasPrefix(id, "meta/"),
		strings.HasPrefix(id, "mistral-ai/"),
		strings.HasPrefix(id, "qwen/"),
		strings.HasPrefix(id, "deepseek-ai/"),
		strings.HasPrefix(id, "meta-llama/"):
		if strings.HasSuffix(id, "-maas") {
			return modelcatalog.OpenAICompat
		}
		return modelcatalog.Unknown
	}
	return modelcatalog.Unknown
}

func init() {
	modelcatalog.SetWireInferrer("vertex-ai", inferVertexWire)
}
