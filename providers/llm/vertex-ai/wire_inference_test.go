package vertexai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestInferVertexWire(t *testing.T) {
	tests := []struct {
		id   string
		want gollm.Wire
	}{
		// Google-native Gemini
		{"gemini-2.5-pro", gollm.WireGoogleNative},
		{"gemini-2.5-flash", gollm.WireGoogleNative},
		{"gemini-1.5-pro", gollm.WireGoogleNative},
		// Unseen future Gemini variant
		{"gemini-3.0-xl", gollm.WireGoogleNative},

		// Anthropic Claude on Vertex
		{"claude-opus-4-6@20251101", gollm.WireAnthropic},
		{"claude-sonnet-4-6@20251101", gollm.WireAnthropic},
		{"claude-haiku-4-5@20251001", gollm.WireAnthropic},
		{"claude-sonnet-4-20250514", gollm.WireAnthropic},
		// Future unseen Claude variant
		{"claude-99-new@20991231", gollm.WireAnthropic},

		// Model-Garden MaaS — OpenAI-compat (require -maas suffix)
		{"meta/llama-3.3-70b-instruct-maas", gollm.WireOpenAICompat},
		{"qwen/qwen3-coder-480b-a35b-instruct-maas", gollm.WireOpenAICompat},
		{"deepseek-ai/deepseek-r1-0528-maas", gollm.WireOpenAICompat},
		{"mistral-ai/mistral-large-2411-001-maas", gollm.WireOpenAICompat},
		// Future unseen MaaS variant from a known publisher
		{"meta/llama-6-new-maas", gollm.WireOpenAICompat},

		// Non-chat models sharing the same publishers: computer
		// vision, embeddings, OCR — must NOT be marked dispatchable.
		{"meta/sam3", gollm.WireUnknown},
		{"meta/faster-r-cnn", gollm.WireUnknown},
		{"meta/imagebind", gollm.WireUnknown},
		{"qwen/qwen-image", gollm.WireUnknown},
		{"qwen/qwen3-embedding", gollm.WireUnknown},
		{"deepseek-ai/deepseek-ocr", gollm.WireUnknown},
		// No -maas on plain chat variants published under Model Garden
		{"mistral-ai/mistral-large-2411-001", gollm.WireUnknown},
		{"deepseek-ai/deepseek-r1", gollm.WireUnknown},

		// Unlisted publishers
		{"cohere/command-r-plus", gollm.WireUnknown},
		{"aws/titan-text", gollm.WireUnknown},

		// Empty / garbage
		{"", gollm.WireUnknown},
		{"random-id", gollm.WireUnknown},
		// Family-only short forms — must NOT be inferred (resolved
		// via catalog alias instead, so the right wire+cap binds).
		{"opus-4-7", gollm.WireUnknown},
	}
	for _, tt := range tests {
		if got := inferVertexWire(tt.id); got != tt.want {
			t.Errorf("inferVertexWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestInferVertexWire_WiredIntoProviderMeta(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("vertex-ai not registered")
	}
	if meta.FamilyInferrer == nil {
		t.Fatal("FamilyInferrer is nil")
	}

	wire, err := meta.ResolveWire("gemini-99-new", gollm.WireUnknown)
	if err != nil || wire != gollm.WireGoogleNative {
		t.Errorf("ResolveWire(gemini-99-new) = (%q, %v)", wire, err)
	}
	wire, err = meta.ResolveWire("meta/llama-6-new-maas", gollm.WireUnknown)
	if err != nil || wire != gollm.WireOpenAICompat {
		t.Errorf("ResolveWire(meta/llama-6-new-maas) = (%q, %v)", wire, err)
	}
	// Same publisher without -maas: catalog miss, inferrer returns
	// Unknown, no wire_override → error.
	if _, err := meta.ResolveWire("meta/sam3", gollm.WireUnknown); err == nil {
		t.Error("ResolveWire(meta/sam3) should error — non-chat MaaS publisher row")
	}
	if _, err := meta.ResolveWire("cohere/anything", gollm.WireUnknown); err == nil {
		t.Error("ResolveWire(cohere/anything) should error — unlisted publisher")
	}
}
