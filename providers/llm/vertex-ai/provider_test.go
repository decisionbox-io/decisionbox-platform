package vertexai

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

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
	meta, ok := gollm.GetProviderMeta("vertex-ai")
	if !ok {
		t.Fatal("vertex-ai not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}
	if len(meta.DefaultPricing) == 0 {
		t.Error("no default pricing")
	}
	if _, ok := meta.DefaultPricing["gemini-2.5-pro"]; !ok {
		t.Error("missing gemini-2.5-pro pricing")
	}
	if meta.MaxOutputTokens["claude-opus-4-6"] != 128000 {
		t.Errorf("MaxOutputTokens[claude-opus-4-6] = %d", meta.MaxOutputTokens["claude-opus-4-6"])
	}
	if got := gollm.GetMaxOutputTokens("vertex-ai", "gemini-2.5-flash"); got != 65536 {
		t.Errorf("GetMaxOutputTokens(vertex-ai, gemini-2.5-flash) = %d", got)
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
	p.wireOverride = modelcatalog.Anthropic

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
