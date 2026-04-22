package azurefoundry

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
)

// alias to keep the long name from dominating the test body
var modelcatalog_OpenAICompat = modelcatalog.OpenAICompat

// Dispatch is now catalog-driven — see modelcatalog registry tests for
// (cloud, model) → wire coverage. The provider-level check that used to
// pattern-match "claude-" is gone. Here we assert the provider routes a
// known Claude-on-Azure model ID to the Anthropic path and a known GPT
// model to the OpenAI path.

func TestAzureFoundryProvider_Dispatch_CatalogClaude(t *testing.T) {
	// Without a mock server, the HTTP call will fail — we just want to
	// observe that the dispatch picked the Anthropic path. An uncatalogued
	// error would be caught before any HTTP attempt.
	p := &AzureFoundryProvider{
		endpoint:   "http://127.0.0.1:1",
		apiKey:     "test-key",
		model:      "claude-sonnet-4-6",
		httpClient: &http.Client{Timeout: 200 * time.Millisecond},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from unreachable endpoint")
	}
	if strings.Contains(err.Error(), "not in catalog") {
		t.Errorf("claude-sonnet-4-6 should be catalogued, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "anthropic") && !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error %q did not go through the Anthropic path", err.Error())
	}
}

func TestAzureFoundryProvider_Dispatch_CatalogGPT(t *testing.T) {
	p := &AzureFoundryProvider{
		endpoint:   "http://127.0.0.1:1",
		apiKey:     "test-key",
		model:      "gpt-4o",
		httpClient: &http.Client{Timeout: 200 * time.Millisecond},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from unreachable endpoint")
	}
	if strings.Contains(err.Error(), "not in catalog") {
		t.Errorf("gpt-4o should be catalogued, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "openai") && !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error %q did not go through the OpenAI path", err.Error())
	}
}

func TestAzureFoundryProvider_Dispatch_UncataloguedActionableError(t *testing.T) {
	p := &AzureFoundryProvider{
		endpoint:   "http://127.0.0.1:1",
		apiKey:     "test-key",
		model:      "DeepSeek-V3",
		httpClient: &http.Client{Timeout: 200 * time.Millisecond},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for uncatalogued model")
	}
	for _, want := range []string{"azure-foundry", "DeepSeek-V3", "wire_override"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestAzureFoundryProvider_Dispatch_WireOverrideOpenAICompat(t *testing.T) {
	// Uncatalogued non-Claude model with wire_override=openai-compat
	// should go through the openai path (we detect by path fragment).
	p := &AzureFoundryProvider{
		endpoint:     "http://127.0.0.1:1",
		apiKey:       "test-key",
		model:        "custom-ft-model",
		wireOverride: modelcatalog_OpenAICompat,
		httpClient:   &http.Client{Timeout: 200 * time.Millisecond},
	}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from unreachable endpoint")
	}
	if strings.Contains(err.Error(), "not in catalog") {
		t.Errorf("wire_override should override catalog miss, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "openai") && !strings.Contains(err.Error(), "request failed") {
		t.Errorf("error %q did not route through openai-compat", err.Error())
	}
}

func TestAzureFoundryProvider_ChatDefaultModel(t *testing.T) {
	p := &AzureFoundryProvider{
		endpoint:   "https://nonexistent.services.ai.azure.com",
		apiKey:     "test-key",
		model:      "claude-sonnet-4-6",
		httpClient: &http.Client{Timeout: 1 * time.Second},
	}

	// Empty model in request should fall back to provider default.
	// Will fail on HTTP (no server), but verifies routing.
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "test"}},
	})

	// Should fail on HTTP, not on model routing
	if err != nil && strings.Contains(err.Error(), "unsupported model") {
		t.Error("should route to claude, not fail on model check")
	}
}

func TestAzureFoundryProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("azure-foundry")
	if !ok {
		t.Fatal("azure-foundry not registered")
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
	if _, ok := meta.DefaultPricing["claude-sonnet-4-6"]; !ok {
		t.Error("missing claude-sonnet-4-6 pricing")
	}
	if _, ok := meta.DefaultPricing["gpt-4o"]; !ok {
		t.Error("missing gpt-4o pricing")
	}

	// MaxOutputTokens
	if meta.MaxOutputTokens == nil {
		t.Fatal("MaxOutputTokens should not be nil")
	}
	if len(meta.MaxOutputTokens) != 9 {
		t.Errorf("MaxOutputTokens has %d entries, want 9", len(meta.MaxOutputTokens))
	}
	if meta.MaxOutputTokens["claude-opus-4-6"] != 128000 {
		t.Errorf("MaxOutputTokens[claude-opus-4-6] = %d, want 128000", meta.MaxOutputTokens["claude-opus-4-6"])
	}
	if meta.MaxOutputTokens["claude-haiku-4-5"] != 64000 {
		t.Errorf("MaxOutputTokens[claude-haiku-4-5] = %d, want 64000", meta.MaxOutputTokens["claude-haiku-4-5"])
	}

	// Verify GetMaxOutputTokens helper
	if got := gollm.GetMaxOutputTokens("azure-foundry", "gpt-4o"); got != 16384 {
		t.Errorf("GetMaxOutputTokens(azure-foundry, gpt-4o) = %d, want 16384", got)
	}
	// Verify _default fallback
	if got := gollm.GetMaxOutputTokens("azure-foundry", "unknown-model"); got != 16384 {
		t.Errorf("GetMaxOutputTokens(azure-foundry, unknown-model) = %d, want 16384 (_default)", got)
	}
}

func TestAzureFoundryProvider_Validate_NoServer(t *testing.T) {
	p := &AzureFoundryProvider{
		endpoint:   "https://nonexistent.services.ai.azure.com",
		apiKey:     "test-key",
		model:      "claude-sonnet-4-6",
		httpClient: &http.Client{Timeout: 1 * time.Second},
	}
	err := p.Validate(context.Background())
	if err == nil {
		t.Error("Validate should fail with no server")
	}
}

func TestAzureFoundryProvider_ConfigFields(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("azure-foundry")
	if !ok {
		t.Fatal("azure-foundry not registered")
	}

	fieldKeys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		fieldKeys[f.Key] = true
	}

	if !fieldKeys["endpoint"] {
		t.Error("missing endpoint config field")
	}
	if !fieldKeys["api_key"] {
		t.Error("missing api_key config field")
	}
	if !fieldKeys["model"] {
		t.Error("missing model config field")
	}
}

func TestAzureFoundryProvider_Factory_MissingEndpoint(t *testing.T) {
	_, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"api_key": "test-key",
		"model":   "claude-sonnet-4-6",
	})
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if !strings.Contains(err.Error(), "endpoint is required") {
		t.Errorf("error = %q, should mention endpoint is required", err.Error())
	}
}

func TestAzureFoundryProvider_Factory_MissingAPIKey(t *testing.T) {
	_, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": "https://test.services.ai.azure.com",
		"model":    "claude-sonnet-4-6",
	})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
	if !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("error = %q, should mention api_key is required", err.Error())
	}
}

func TestAzureFoundryProvider_Factory_MissingModel(t *testing.T) {
	_, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": "https://test.services.ai.azure.com",
		"api_key":  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("error = %q, should mention model is required", err.Error())
	}
}

func TestAzureFoundryProvider_Factory_StripsTrailingSlash(t *testing.T) {
	provider, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": "https://test.services.ai.azure.com/",
		"api_key":  "test-key",
		"model":    "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := provider.(*AzureFoundryProvider)
	if p.endpoint != "https://test.services.ai.azure.com" {
		t.Errorf("endpoint = %q, trailing slash should be stripped", p.endpoint)
	}
}

func TestAzureFoundryProvider_Factory_DefaultTimeout(t *testing.T) {
	provider, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": "https://test.services.ai.azure.com",
		"api_key":  "test-key",
		"model":    "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := provider.(*AzureFoundryProvider)
	expected := 300 * time.Second
	if p.httpClient.Timeout != expected {
		t.Errorf("timeout = %v, want %v (default)", p.httpClient.Timeout, expected)
	}
}

func TestAzureFoundryProvider_Factory_CustomTimeout(t *testing.T) {
	provider, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint":        "https://test.services.ai.azure.com",
		"api_key":         "test-key",
		"model":           "claude-sonnet-4-6",
		"timeout_seconds": "60",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := provider.(*AzureFoundryProvider)
	expected := 60 * time.Second
	if p.httpClient.Timeout != expected {
		t.Errorf("timeout = %v, want %v", p.httpClient.Timeout, expected)
	}
}
