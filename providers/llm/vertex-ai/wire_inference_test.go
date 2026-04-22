package vertexai

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func TestInferVertexWire(t *testing.T) {
	tests := []struct {
		id   string
		want modelcatalog.Wire
	}{
		// Google-native Gemini
		{"gemini-2.5-pro", modelcatalog.GoogleNative},
		{"gemini-2.5-flash", modelcatalog.GoogleNative},
		{"gemini-1.5-pro", modelcatalog.GoogleNative},
		// Unseen future Gemini variant
		{"gemini-3.0-xl", modelcatalog.GoogleNative},

		// Anthropic Claude on Vertex
		{"claude-opus-4-6@20251101", modelcatalog.Anthropic},
		{"claude-sonnet-4-6@20251101", modelcatalog.Anthropic},
		{"claude-haiku-4-5@20251001", modelcatalog.Anthropic},
		{"claude-sonnet-4-20250514", modelcatalog.Anthropic},

		// Model-Garden MaaS — OpenAI-compat
		{"meta/llama-3.3-70b-instruct-maas", modelcatalog.OpenAICompat},
		{"mistral-ai/mistral-large-2411-001", modelcatalog.OpenAICompat},
		{"qwen/qwen3-coder-480b-a35b-instruct-maas", modelcatalog.OpenAICompat},
		{"deepseek-ai/deepseek-r1", modelcatalog.OpenAICompat},

		// Unlisted publishers
		{"cohere/command-r-plus", modelcatalog.Unknown},
		{"aws/titan-text", modelcatalog.Unknown},

		// Empty / garbage
		{"", modelcatalog.Unknown},
		{"random-id", modelcatalog.Unknown},
	}
	for _, tt := range tests {
		if got := inferVertexWire(tt.id); got != tt.want {
			t.Errorf("inferVertexWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestInferVertexWire_RegisteredIntoCatalog(t *testing.T) {
	if got := modelcatalog.InferWire("vertex-ai", "gemini-99-new"); got != modelcatalog.GoogleNative {
		t.Errorf("InferWire(vertex-ai, gemini-99-new) = %q", got)
	}
	if got := modelcatalog.InferWire("vertex-ai", "meta/llama-6-new"); got != modelcatalog.OpenAICompat {
		t.Errorf("InferWire(vertex-ai, meta/llama-6-new) = %q", got)
	}
	if got := modelcatalog.InferWire("vertex-ai", "cohere/anything"); got != modelcatalog.Unknown {
		t.Errorf("InferWire(vertex-ai, cohere/anything) = %q, want Unknown", got)
	}
}
