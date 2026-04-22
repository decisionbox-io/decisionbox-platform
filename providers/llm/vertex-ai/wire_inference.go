package vertexai

import (
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// inferVertexWire maps a Vertex model ID to the wire it speaks based on
// the publisher prefix. Gemini IDs have no publisher prefix in the
// rawPredict / generateContent URL; Claude IDs are the Anthropic form
// (optionally suffixed with @YYYYMMDD); everything else carries a
// "publisher/" prefix in the live list response format we emit.
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
		return modelcatalog.OpenAICompat
	}
	return modelcatalog.Unknown
}

func init() {
	modelcatalog.SetWireInferrer("vertex-ai", inferVertexWire)
}
