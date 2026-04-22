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

		// Model-Garden MaaS — OpenAI-compat (require -maas suffix)
		{"meta/llama-3.3-70b-instruct-maas", modelcatalog.OpenAICompat},
		{"qwen/qwen3-coder-480b-a35b-instruct-maas", modelcatalog.OpenAICompat},
		{"deepseek-ai/deepseek-r1-0528-maas", modelcatalog.OpenAICompat},
		{"mistral-ai/mistral-large-2411-001-maas", modelcatalog.OpenAICompat},

		// Non-chat models sharing the same publishers: computer
		// vision, embeddings, OCR — must NOT be marked dispatchable.
		{"meta/sam3", modelcatalog.Unknown},
		{"meta/faster-r-cnn", modelcatalog.Unknown},
		{"meta/imagebind", modelcatalog.Unknown},
		{"qwen/qwen-image", modelcatalog.Unknown},
		{"qwen/qwen3-embedding", modelcatalog.Unknown},
		{"deepseek-ai/deepseek-ocr", modelcatalog.Unknown},
		// No -maas on plain chat variants published under Model Garden
		{"mistral-ai/mistral-large-2411-001", modelcatalog.Unknown},
		{"deepseek-ai/deepseek-r1", modelcatalog.Unknown},

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
	if got := modelcatalog.InferWire("vertex-ai", "meta/llama-6-new-maas"); got != modelcatalog.OpenAICompat {
		t.Errorf("InferWire(vertex-ai, meta/llama-6-new-maas) = %q", got)
	}
	// Same publisher without -maas must be Unknown (not chat).
	if got := modelcatalog.InferWire("vertex-ai", "meta/sam3"); got != modelcatalog.Unknown {
		t.Errorf("InferWire(vertex-ai, meta/sam3) = %q, want Unknown", got)
	}
	if got := modelcatalog.InferWire("vertex-ai", "cohere/anything"); got != modelcatalog.Unknown {
		t.Errorf("InferWire(vertex-ai, cohere/anything) = %q, want Unknown", got)
	}
}
