package vertexai

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestVertexAIProvider_MaaSCatalog_UsesMaaSSuffix is the regression
// for the issue Copilot flagged on PR #193: Vertex Model Garden
// chat-capable endpoints require a "-maas" suffix; the bare
// publisher ID (e.g. "mistral-ai/mistral-large-2411-001") is not the
// chat endpoint. Every MaaS row in the catalog must use the suffixed
// ID as canonical so the live-list merge and dispatch agree.
func TestVertexAIProvider_MaaSCatalog_UsesMaaSSuffix(t *testing.T) {
	meta, _ := gollm.GetProviderMeta(providerName)
	for _, e := range meta.Models {
		if e.Wire != gollm.WireOpenAICompat {
			continue
		}
		// MaaS publishers we ship with — every chat-capable entry
		// must carry the -maas suffix on the canonical ID.
		for _, prefix := range []string{
			"meta/", "mistral-ai/", "qwen/", "deepseek-ai/",
		} {
			if strings.HasPrefix(e.ID, prefix) && !strings.HasSuffix(e.ID, "-maas") {
				t.Errorf("MaaS canonical ID %q is missing the -maas suffix; bare publisher IDs are not chat-capable", e.ID)
			}
		}
	}
}

func TestVertexAIProvider_Dispatch_UncataloguedActionableError(t *testing.T) {
	p := &VertexAIProvider{
		projectID:  "test-project",
		location:   "us-east5",
		model:      "vendor/future-model-2099",
		auth:       &gcpAuth{tokenSource: &mockTokenSource{token: "test"}},
		httpClient: &http.Client{Timeout: time.Second},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "vendor/future-model-2099",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for uncatalogued model")
	}
	for _, want := range []string{"vertex-ai", "vendor/future-model-2099", "wire_override"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestVertexAIProvider_Factory_MissingProjectID(t *testing.T) {
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"location": "us-east5",
		"model":    "gemini-2.5-pro",
	})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
	if !strings.Contains(err.Error(), "project_id is required") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestVertexAIProvider_Factory_MissingModel(t *testing.T) {
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": "my-project",
		"location":   "us-east5",
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestVertexAIProvider_Factory_InvalidWireOverride(t *testing.T) {
	// ADC is probed inside the factory and may fail on a CI runner with no
	// GCP credentials; the wire_override validation runs before that, so
	// the "invalid wire_override" error is what we expect regardless.
	_, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id":    "my-project",
		"model":         "gemini-2.5-pro",
		"wire_override": "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid wire_override")
	}
	if !strings.Contains(err.Error(), "invalid wire_override") {
		t.Errorf("error = %q, should mention invalid wire_override", err.Error())
	}
}

func TestVertexAIProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("vertex-ai not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}
	if len(meta.Models) == 0 {
		t.Fatal("catalog empty")
	}
	if p, ok := meta.PricingFor("gemini-2.5-pro"); !ok || p.InputPerMillion != 1.25 {
		t.Errorf("gemini-2.5-pro pricing = %+v ok=%v", p, ok)
	}
	// Claude Opus 4.6 must resolve to 128k via canonical ID, alias,
	// or family-only short form.
	for _, model := range []string{
		"claude-opus-4-6",
		"claude-opus-4-6@20251101",
		"opus-4-6",
	} {
		if got := gollm.GetMaxOutputTokens(providerName, model); got != opus46Max {
			t.Errorf("GetMaxOutputTokens(%q) = %d, want %d", model, got, opus46Max)
		}
	}
	// Opus 4.7 is the newest — every alias resolves to 128k.
	for _, model := range []string{"claude-opus-4-7", "opus-4-7"} {
		if got := gollm.GetMaxOutputTokens(providerName, model); got != opus47Max {
			t.Errorf("GetMaxOutputTokens(%q) = %d, want %d", model, got, opus47Max)
		}
	}
	if got := gollm.GetMaxOutputTokens(providerName, "gemini-2.5-flash"); got != geminiMax {
		t.Errorf("GetMaxOutputTokens(gemini-2.5-flash) = %d, want %d", got, geminiMax)
	}
	// Versioned Gemini stable variants resolve via aliases.
	if got := gollm.GetMaxOutputTokens(providerName, "gemini-2.5-pro-002"); got != geminiMax {
		t.Errorf("GetMaxOutputTokens(gemini-2.5-pro-002) = %d, want %d", got, geminiMax)
	}
	// Unknown model falls back to provider default.
	if got := gollm.GetMaxOutputTokens(providerName, "vendor.unknown-2099"); got != 16384 {
		t.Errorf("GetMaxOutputTokens default = %d, want 16384", got)
	}
}

func TestVertexAIProvider_ConfigFields(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("vertex-ai")
	if !ok {
		t.Fatal("vertex-ai not registered")
	}
	fieldKeys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		fieldKeys[f.Key] = true
	}
	for _, want := range []string{"project_id", "location", "model", "wire_override"} {
		if !fieldKeys[want] {
			t.Errorf("missing %s config field", want)
		}
	}
	if fieldKeys["api_key"] {
		t.Error("vertex-ai should not have api_key field — uses GCP ADC")
	}
}

// contains is a small helper used by multiple test files in this package.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestVertexAIProvider_Dispatch_WireOverrideAnthropic(t *testing.T) {
	// Uncatalogued Claude variant with wire_override=anthropic should
	// route to the Anthropic path (we verify by checking the endpoint
	// the rewrite transport sees).
	testSrv := newMockAnthropicServer(t, "hi", "vendor-claude-future", 1, 1)
	defer testSrv.Close()

	p := newTestProviderWithURL(testSrv.URL, "vendor-claude-future")
	p.wireOverride = gollm.WireAnthropic

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:    "vendor-claude-future",
		Messages: []gollm.Message{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("content = %q", resp.Content)
	}
}
