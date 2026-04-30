package bedrock

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestInferBedrockWire exercises the FamilyInferrer used as the third
// resolution tier (after the catalog and wire_override) in
// ProviderMeta.ResolveWire. Future Anthropic Claude variants and any
// OpenAI-compat family on Bedrock should be recognised by prefix even
// when not yet in the catalog.
func TestInferBedrockWire(t *testing.T) {
	tests := []struct {
		id   string
		want gollm.Wire
	}{
		// Anthropic family — all regional inference profiles.
		{"anthropic.claude-sonnet-4-20250514-v1:0", gollm.WireAnthropic},
		{"us.anthropic.claude-sonnet-4-20250514-v1:0", gollm.WireAnthropic},
		{"eu.anthropic.claude-haiku-4-5-v1:0", gollm.WireAnthropic},
		{"apac.anthropic.claude-opus-4-6-v1", gollm.WireAnthropic},
		{"jp.anthropic.claude-haiku-4-5-20251001-v1:0", gollm.WireAnthropic},
		{"au.anthropic.claude-sonnet-4-5-20250929-v1:0", gollm.WireAnthropic},
		{"global.anthropic.claude-opus-4-6-v1", gollm.WireAnthropic},
		// Future unseen Claude variant — still inferred.
		{"anthropic.claude-7-ultra-v1:0", gollm.WireAnthropic},

		// OpenAI-compat families.
		{"qwen.qwen3-next-80b-a3b", gollm.WireOpenAICompat},
		{"deepseek.r1-v1:0", gollm.WireOpenAICompat},
		{"mistral.mixtral-8x22b-v1:0", gollm.WireOpenAICompat},
		{"mistral.mistral-large-2407-v1:0", gollm.WireOpenAICompat},
		{"meta.llama3-3-70b-instruct-v1:0", gollm.WireOpenAICompat},
		{"us.meta.llama4-70b-v1:0", gollm.WireOpenAICompat},
		{"global.deepseek.r1-v1:0", gollm.WireOpenAICompat},

		// Families with no wire implementation — stay unknown so the
		// UI can flag them non-dispatchable.
		{"amazon.nova-2-lite-v1:0", gollm.WireUnknown},
		{"amazon.titan-text-express-v1", gollm.WireUnknown},
		{"amazon.nova-2-multimodal-embeddings-v1:0", gollm.WireUnknown},
		{"cohere.command-r-v1:0", gollm.WireUnknown},
		{"cohere.embed-english-v3", gollm.WireUnknown},
		{"ai21.jamba-1-5-large-v1:0", gollm.WireUnknown},
		{"ai21.jamba-1-5-mini-v1:0", gollm.WireUnknown},

		// Garbage / partial strings.
		{"", gollm.WireUnknown},
		{"anth", gollm.WireUnknown},
		{"not-a-bedrock-id", gollm.WireUnknown},
		{"opus-4-7", gollm.WireUnknown}, // family-only — needs to come via catalog alias, not inferrer.
	}
	for _, tt := range tests {
		if got := inferBedrockWire(tt.id); got != tt.want {
			t.Errorf("inferBedrockWire(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// TestInferBedrockWire_WiredIntoProviderMeta confirms the inferrer is
// wired into the registered ProviderMeta — the third resolution tier
// only fires when the catalog and wire_override both miss, so a
// regression here would silently route fresh-released models to the
// "unknown wire" error path.
func TestInferBedrockWire_WiredIntoProviderMeta(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("bedrock provider not registered")
	}
	if meta.FamilyInferrer == nil {
		t.Fatal("bedrock ProviderMeta.FamilyInferrer is nil")
	}

	// A model not in the catalog should resolve via the inferrer.
	wire, err := meta.ResolveWire("anthropic.claude-99-new-v1:0", gollm.WireUnknown)
	if err != nil {
		t.Fatalf("ResolveWire returned error for known-family unseen model: %v", err)
	}
	if wire != gollm.WireAnthropic {
		t.Errorf("ResolveWire(claude-99-new) = %q, want %q", wire, gollm.WireAnthropic)
	}
}
