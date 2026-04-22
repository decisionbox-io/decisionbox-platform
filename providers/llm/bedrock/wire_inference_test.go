package bedrock

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func TestInferBedrockWire(t *testing.T) {
	tests := []struct {
		id   string
		want modelcatalog.Wire
	}{
		// Anthropic family — all regional inference profiles
		{"anthropic.claude-sonnet-4-20250514-v1:0", modelcatalog.Anthropic},
		{"us.anthropic.claude-sonnet-4-20250514-v1:0", modelcatalog.Anthropic},
		{"eu.anthropic.claude-haiku-4-5-v1:0", modelcatalog.Anthropic},
		{"apac.anthropic.claude-opus-4-6-v1", modelcatalog.Anthropic},
		{"global.anthropic.claude-opus-4-6-v1", modelcatalog.Anthropic},
		// Future unseen Claude variant — still inferred
		{"anthropic.claude-7-ultra-v1:0", modelcatalog.Anthropic},

		// OpenAI-compat families
		{"qwen.qwen3-next-80b-a3b", modelcatalog.OpenAICompat},
		{"deepseek.r1-v1:0", modelcatalog.OpenAICompat},
		{"mistral.mixtral-8x22b-v1:0", modelcatalog.OpenAICompat},
		{"mistral.mistral-large-2407-v1:0", modelcatalog.OpenAICompat},
		{"meta.llama3-3-70b-instruct-v1:0", modelcatalog.OpenAICompat},
		{"us.meta.llama4-70b-v1:0", modelcatalog.OpenAICompat},

		// Families with no wire implementation — stay unknown so the
		// UI can flag them non-dispatchable.
		{"amazon.nova-2-lite-v1:0", modelcatalog.Unknown},
		{"amazon.titan-text-express-v1", modelcatalog.Unknown},
		{"amazon.nova-2-multimodal-embeddings-v1:0", modelcatalog.Unknown},
		{"cohere.command-r-v1:0", modelcatalog.Unknown},
		{"cohere.embed-english-v3", modelcatalog.Unknown},
		{"ai21.jamba-1-5-large-v1:0", modelcatalog.Unknown},
		{"ai21.jamba-1-5-mini-v1:0", modelcatalog.Unknown},

		// Garbage / partial strings
		{"", modelcatalog.Unknown},
		{"anth", modelcatalog.Unknown},
		{"not-a-bedrock-id", modelcatalog.Unknown},
	}
	for _, tt := range tests {
		if got := inferBedrockWire(tt.id); got != tt.want {
			t.Errorf("inferBedrockWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestInferBedrockWire_RegisteredIntoCatalog(t *testing.T) {
	// Round-trip through the catalog: the inferrer was registered in
	// init() so InferWire on the "bedrock" cloud should route through
	// our table.
	if got := modelcatalog.InferWire("bedrock", "anthropic.claude-99-new-v1:0"); got != modelcatalog.Anthropic {
		t.Errorf("modelcatalog.InferWire(bedrock, claude-99) = %q", got)
	}
	if got := modelcatalog.InferWire("bedrock", "qwen.qwen5"); got != modelcatalog.OpenAICompat {
		t.Errorf("modelcatalog.InferWire(bedrock, qwen5) = %q", got)
	}
	if got := modelcatalog.InferWire("bedrock", "amazon.nova-3"); got != modelcatalog.Unknown {
		t.Errorf("modelcatalog.InferWire(bedrock, nova-3) = %q, want Unknown", got)
	}
}
