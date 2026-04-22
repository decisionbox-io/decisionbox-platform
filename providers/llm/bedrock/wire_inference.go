package bedrock

import (
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// bedrockWireByPrefix maps a Bedrock model-ID prefix to the wire that
// model family speaks. Order matters: longer / more-specific prefixes
// come first. Unlisted families (amazon.nova-*, amazon.titan-*,
// cohere.*, ai21.*) have no compatible wire implementation today and
// stay unknown — the UI marks them non-dispatchable.
var bedrockWireByPrefix = []struct {
	prefix string
	wire   modelcatalog.Wire
}{
	{"us.anthropic.", modelcatalog.Anthropic},
	{"eu.anthropic.", modelcatalog.Anthropic},
	{"apac.anthropic.", modelcatalog.Anthropic},
	{"global.anthropic.", modelcatalog.Anthropic},
	{"anthropic.", modelcatalog.Anthropic},

	// Every Qwen / DeepSeek / Mistral / Llama variant on Bedrock today
	// uses the OpenAI Chat Completions body shape.
	{"qwen.", modelcatalog.OpenAICompat},
	{"deepseek.", modelcatalog.OpenAICompat},
	{"mistral.", modelcatalog.OpenAICompat},
	{"meta.", modelcatalog.OpenAICompat},
	// Regional inference profile prefixes for the same families.
	{"us.meta.", modelcatalog.OpenAICompat},
	{"us.mistral.", modelcatalog.OpenAICompat},
	{"us.qwen.", modelcatalog.OpenAICompat},
	{"us.deepseek.", modelcatalog.OpenAICompat},
}

// inferBedrockWire returns the wire a Bedrock model speaks based on its
// ID prefix, or Unknown when the family is not one DecisionBox can
// dispatch with its current wire implementations.
func inferBedrockWire(id string) modelcatalog.Wire {
	for _, p := range bedrockWireByPrefix {
		if strings.HasPrefix(id, p.prefix) {
			return p.wire
		}
	}
	return modelcatalog.Unknown
}

func init() {
	modelcatalog.SetWireInferrer("bedrock", inferBedrockWire)
}
