package ollama

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestOllamaProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}
}

func TestOllamaProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	if !keys["host"] {
		t.Error("missing host config field")
	}
	if !keys["model"] {
		t.Error("missing model config field")
	}
	// Should NOT have api_key — local models
	if keys["api_key"] {
		t.Error("ollama should not have api_key field")
	}
}

func TestOllamaProvider_ZeroPricing(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")

	pricing, ok := meta.DefaultPricing["_default"]
	if !ok {
		t.Fatal("missing _default pricing")
	}
	if pricing.InputPerMillion != 0 || pricing.OutputPerMillion != 0 {
		t.Errorf("ollama pricing should be zero, got in=%f out=%f",
			pricing.InputPerMillion, pricing.OutputPerMillion)
	}
}

func TestOllamaProvider_FactoryMissingModel(t *testing.T) {
	_, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host": "http://localhost:11434",
	})
	if err == nil {
		t.Error("should error without model")
	}
}

func TestOllamaProvider_FactorySuccess(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_DefaultHost(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_Validate_ServerDown(t *testing.T) {
	p, err := NewOllamaProvider("http://localhost:1", "qwen2.5:0.5b")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Error("Validate should fail when server is unreachable")
	}
}

func TestNewOllamaProvider_InvalidURL(t *testing.T) {
	_, err := NewOllamaProvider("://invalid", "model")
	if err == nil {
		t.Error("should error on invalid URL")
	}
}
