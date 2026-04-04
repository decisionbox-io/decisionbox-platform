package embedding

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	dims  int
	model string
}

func (m *mockProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i := range texts {
		result[i] = make([]float64, m.dims)
	}
	return result, nil
}

func (m *mockProvider) Dimensions() int        { return m.dims }
func (m *mockProvider) ModelName() string       { return m.model }
func (m *mockProvider) Validate(_ context.Context) error { return nil }

// resetRegistry clears the global registry for test isolation.
func resetRegistry() {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers = make(map[string]ProviderFactory)
	providerMeta = make(map[string]ProviderMeta)
}

func TestRegister(t *testing.T) {
	resetRegistry()

	factory := func(cfg ProviderConfig) (Provider, error) {
		return &mockProvider{dims: 1536, model: "test-model"}, nil
	}

	Register("test-provider", factory)

	names := RegisteredProviders()
	if len(names) != 1 || names[0] != "test-provider" {
		t.Fatalf("expected [test-provider], got %v", names)
	}
}

func TestRegisterNilFactory(t *testing.T) {
	resetRegistry()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil factory")
		}
		msg := fmt.Sprintf("%v", r)
		if !strings.Contains(msg, "nil") {
			t.Fatalf("expected panic message about nil, got: %s", msg)
		}
	}()

	Register("nil-factory", nil)
}

func TestRegisterDuplicate(t *testing.T) {
	resetRegistry()

	factory := func(cfg ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}

	Register("dup", factory)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
		msg := fmt.Sprintf("%v", r)
		if !strings.Contains(msg, "twice") {
			t.Fatalf("expected panic message about twice, got: %s", msg)
		}
	}()

	Register("dup", factory)
}

func TestRegisterWithMeta(t *testing.T) {
	resetRegistry()

	factory := func(cfg ProviderConfig) (Provider, error) {
		return &mockProvider{dims: 1536, model: "text-embedding-3-small"}, nil
	}

	meta := ProviderMeta{
		Name:        "OpenAI",
		Description: "OpenAI text embeddings",
		ConfigFields: []ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "string"},
		},
		Models: []ModelInfo{
			{ID: "text-embedding-3-small", Name: "Embedding 3 Small", Dimensions: 1536},
		},
	}

	RegisterWithMeta("openai", factory, meta)

	// Verify provider registered
	names := RegisteredProviders()
	if len(names) != 1 || names[0] != "openai" {
		t.Fatalf("expected [openai], got %v", names)
	}

	// Verify metadata
	metas := RegisteredProvidersMeta()
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].ID != "openai" {
		t.Fatalf("expected ID openai, got %s", metas[0].ID)
	}
	if metas[0].Name != "OpenAI" {
		t.Fatalf("expected Name OpenAI, got %s", metas[0].Name)
	}
	if len(metas[0].Models) != 1 || metas[0].Models[0].Dimensions != 1536 {
		t.Fatalf("expected model with 1536 dims, got %v", metas[0].Models)
	}
}

func TestGetProviderMeta(t *testing.T) {
	resetRegistry()

	factory := func(cfg ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}

	RegisterWithMeta("test", factory, ProviderMeta{
		Name:        "Test",
		Description: "Test provider",
	})

	meta, ok := GetProviderMeta("test")
	if !ok {
		t.Fatal("expected provider meta to be found")
	}
	if meta.Name != "Test" {
		t.Fatalf("expected Name Test, got %s", meta.Name)
	}

	_, ok = GetProviderMeta("nonexistent")
	if ok {
		t.Fatal("expected provider meta not found for nonexistent")
	}
}

func TestNewProvider(t *testing.T) {
	resetRegistry()

	Register("test", func(cfg ProviderConfig) (Provider, error) {
		apiKey := cfg["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return &mockProvider{dims: 768, model: cfg["model"]}, nil
	})

	// Success case
	p, err := NewProvider("test", ProviderConfig{
		"api_key": "test-key",
		"model":   "test-model",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Dimensions() != 768 {
		t.Fatalf("expected 768 dims, got %d", p.Dimensions())
	}
	if p.ModelName() != "test-model" {
		t.Fatalf("expected test-model, got %s", p.ModelName())
	}

	// Missing config
	_, err = NewProvider("test", ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}

	// Unknown provider
	_, err = NewProvider("unknown", ProviderConfig{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected 'unknown provider' in error, got: %s", err.Error())
	}
}

func TestRegisteredProviders(t *testing.T) {
	resetRegistry()

	Register("a", func(cfg ProviderConfig) (Provider, error) { return &mockProvider{}, nil })
	Register("b", func(cfg ProviderConfig) (Provider, error) { return &mockProvider{}, nil })
	Register("c", func(cfg ProviderConfig) (Provider, error) { return &mockProvider{}, nil })

	names := RegisteredProviders()
	if len(names) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(names))
	}

	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, expected := range []string{"a", "b", "c"} {
		if !nameSet[expected] {
			t.Fatalf("expected provider %q in list", expected)
		}
	}
}
