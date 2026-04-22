package bedrock

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

func TestBedrockProvider_Dispatch_CatalogAnthropic(t *testing.T) {
	// A catalogued Claude model should route to the Anthropic wire.
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("ok", "anthropic.claude-sonnet-4-20250514-v1:0", "end_turn", 1, 1),
	}
	p := &BedrockProvider{
		client:     mock,
		model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestBedrockProvider_Dispatch_CatalogOpenAICompat(t *testing.T) {
	// A catalogued Qwen model should route to the OpenAICompat wire.
	openaiBody := []byte(`{"id":"x","model":"qwen.qwen3-next-80b-a3b",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi from qwen"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`)
	mock := &mockBedrockClient{responseBody: openaiBody}
	p := &BedrockProvider{
		client:     mock,
		model:      "qwen.qwen3-next-80b-a3b",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hi from qwen" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Usage.InputTokens != 4 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestBedrockProvider_Dispatch_UncataloguedActionableError(t *testing.T) {
	// An uncatalogued model without a wire_override must return an error
	// that names the provider, the model, and the wire_override hint.
	p := &BedrockProvider{
		model:      "vendor.future-model-2099",
		httpClient: &http.Client{},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "vendor.future-model-2099",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for uncatalogued model")
	}
	msg := err.Error()
	for _, want := range []string{"bedrock", "vendor.future-model-2099", "wire_override"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestBedrockProvider_Dispatch_WireOverrideWhenUncatalogued(t *testing.T) {
	// An uncatalogued model with a wire_override should route per the override.
	openaiBody := []byte(`{"model":"vendor.future-2099",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	mock := &mockBedrockClient{responseBody: openaiBody}
	p := &BedrockProvider{
		client:       mock,
		model:        "vendor.future-2099",
		wireOverride: modelcatalog.OpenAICompat,
		httpClient:   &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestBedrockProvider_Factory_RejectsGoogleNativeWireOverride(t *testing.T) {
	// google-native is a valid Wire value but no implementation exists
	// on Bedrock. The factory should reject it at save time rather than
	// letting the user hit a confusing dispatch-time error.
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model":         "vendor.gemini-on-bedrock",
		"wire_override": "google-native",
	})
	if err == nil {
		t.Fatal("expected factory to reject google-native wire_override on Bedrock")
	}
	if !strings.Contains(err.Error(), "invalid wire_override") {
		t.Errorf("error = %q, should mention invalid wire_override", err.Error())
	}
}

func TestBedrockProvider_DefaultModel_UsedWhenRequestOmits(t *testing.T) {
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("ok", "anthropic.claude-sonnet-4-20250514-v1:0", "end_turn", 1, 1),
	}
	p := &BedrockProvider{
		client:     mock,
		model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestBedrockProvider_Factory_MissingModel(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestBedrockProvider_Factory_InvalidWireOverride(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model":         "anthropic.claude-sonnet-4-20250514-v1:0",
		"wire_override": "bogus-wire",
	})
	if err == nil {
		t.Fatal("expected error for invalid wire_override")
	}
	if !strings.Contains(err.Error(), "invalid wire_override") {
		t.Errorf("error = %q", err.Error())
	}
	// The message should list the Bedrock-supported choices so users
	// can self-serve. google-native is intentionally omitted because
	// Bedrock has no implementation for it.
	for _, want := range []string{"anthropic", "openai-compat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should list wire %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "google-native") {
		t.Errorf("error should not list google-native (not implemented on Bedrock): %q", err.Error())
	}
}

func TestBedrockProvider_Factory_AcceptsValidWireOverride(t *testing.T) {
	for _, wo := range []string{"anthropic", "openai-compat"} {
		prov, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
			"model":         "vendor.custom",
			"wire_override": wo,
		})
		if err != nil {
			t.Fatalf("wire_override=%q: unexpected error %v", wo, err)
		}
		if prov == nil {
			t.Fatalf("wire_override=%q: nil provider", wo)
		}
	}
}

func TestBedrockProvider_Factory_EmptyWireOverrideAllowed(t *testing.T) {
	_, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model": "anthropic.claude-sonnet-4-20250514-v1:0",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBedrockProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("bedrock")
	if !ok {
		t.Fatal("bedrock not registered")
	}
	if meta.Description == "" {
		t.Error("missing provider description")
	}
	if len(meta.DefaultPricing) == 0 {
		t.Error("no default pricing")
	}
	if meta.MaxOutputTokens["claude-opus-4-6"] != 128000 {
		t.Errorf("MaxOutputTokens[claude-opus-4-6] = %d", meta.MaxOutputTokens["claude-opus-4-6"])
	}
	if got := gollm.GetMaxOutputTokens("bedrock", "claude-unknown"); got != 16384 {
		t.Errorf("GetMaxOutputTokens default = %d", got)
	}
}

func TestBedrockProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("bedrock")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	for _, want := range []string{"region", "model", "wire_override"} {
		if !keys[want] {
			t.Errorf("missing %s config field", want)
		}
	}
}

func TestBedrockProvider_Validate_UncataloguedUninferableModel(t *testing.T) {
	// Use a model ID whose prefix doesn't match any entry in the
	// bedrockWireByPrefix table (amazon.nova-* and cohere.* are in the
	// live-returned list but no wire implementation exists for them).
	// Validate must hit the "not in catalog" error before any AWS call.
	p := &BedrockProvider{
		model:      "amazon.nova-2-lite-v1:0",
		httpClient: &http.Client{},
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Error("Validate should fail for uncatalogued, uninferable model with no wire_override")
	}
}

func TestBedrockProvider_Dispatch_InferredWireForUncataloguedClaude(t *testing.T) {
	// A never-seen Claude variant with the canonical "anthropic." prefix
	// should be inferred as Anthropic wire and dispatch successfully,
	// even without a catalog entry or wire_override.
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("ok", "anthropic.claude-99-new-v1:0", "end_turn", 1, 1),
	}
	p := &BedrockProvider{
		client:     mock,
		model:      "anthropic.claude-99-new-v1:0",
		httpClient: &http.Client{},
	}
	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("inferred wire should allow dispatch, got %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}
}

// legacy helper kept from original file for the mock tests.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
