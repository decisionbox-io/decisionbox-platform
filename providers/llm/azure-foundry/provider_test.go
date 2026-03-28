package azurefoundry

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIsClaude(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6", true},
		{"claude-opus-4-6", true},
		{"claude-haiku-4-5", true},
		{"claude-sonnet-4-5", true},
		{"claude-opus-4-1", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"o3", false},
		{"DeepSeek-V3", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isClaude(tt.model); got != tt.want {
			t.Errorf("isClaude(%q) = %v, want %v", tt.model, got, tt.want)
		}
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
