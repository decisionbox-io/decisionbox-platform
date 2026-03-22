package llm

import (
	"context"
	"testing"
)

// mockProvider is a minimal Provider implementation for registry tests.
type mockProvider struct{}

func (m *mockProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Content: "test"}, nil
}

func (m *mockProvider) Validate(_ context.Context) error {
	return nil
}

func TestRegisterWithMeta(t *testing.T) {
	name := "test-register-with-meta"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}
	meta := ProviderMeta{
		Name:        "Test Provider",
		Description: "a test provider",
		ConfigFields: []ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "string"},
		},
	}

	RegisterWithMeta(name, factory, meta)

	got, ok := GetProviderMeta(name)
	if !ok {
		t.Fatalf("GetProviderMeta(%q) returned false, want true", name)
	}
	if got.ID != name {
		t.Errorf("ProviderMeta.ID = %q, want %q", got.ID, name)
	}
	if got.Name != "Test Provider" {
		t.Errorf("ProviderMeta.Name = %q, want %q", got.Name, "Test Provider")
	}
	if got.Description != "a test provider" {
		t.Errorf("ProviderMeta.Description = %q, want %q", got.Description, "a test provider")
	}
	if len(got.ConfigFields) != 1 {
		t.Fatalf("len(ConfigFields) = %d, want 1", len(got.ConfigFields))
	}
	if got.ConfigFields[0].Key != "api_key" {
		t.Errorf("ConfigFields[0].Key = %q, want %q", got.ConfigFields[0].Key, "api_key")
	}
}

func TestNewProvider_Success(t *testing.T) {
	name := "test-new-provider-success"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}
	Register(name, factory)

	provider, err := NewProvider(name, ProviderConfig{"model": "test-model"})
	if err != nil {
		t.Fatalf("NewProvider(%q) returned error: %v", name, err)
	}
	if provider == nil {
		t.Fatal("NewProvider returned nil provider")
	}

	resp, err := provider.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("Chat() returned error: %v", err)
	}
	if resp.Content != "test" {
		t.Errorf("Chat().Content = %q, want %q", resp.Content, "test")
	}
}

func TestNewProvider_UnknownName(t *testing.T) {
	_, err := NewProvider("nonexistent-provider-xyz", ProviderConfig{})
	if err == nil {
		t.Fatal("NewProvider with unknown name should return error")
	}

	want := `llm: unknown provider "nonexistent-provider-xyz"`
	if len(err.Error()) < len(want) {
		t.Fatalf("error too short: %q", err.Error())
	}
	if err.Error()[:len(want)] != want {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestRegisteredProviders(t *testing.T) {
	name := "test-registered-providers"
	Register(name, func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	})

	names := RegisteredProviders()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RegisteredProviders() did not include %q, got %v", name, names)
	}
}

func TestRegisteredProvidersMeta(t *testing.T) {
	name := "test-registered-providers-meta"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}
	meta := ProviderMeta{
		Name:        "Meta Test",
		Description: "for meta list test",
		ConfigFields: []ConfigField{
			{Key: "token", Label: "Token", Required: true, Type: "string"},
		},
	}
	RegisterWithMeta(name, factory, meta)

	metas := RegisteredProvidersMeta()
	found := false
	for _, m := range metas {
		if m.ID == name {
			found = true
			if m.Name != "Meta Test" {
				t.Errorf("ProviderMeta.Name = %q, want %q", m.Name, "Meta Test")
			}
			if len(m.ConfigFields) != 1 {
				t.Errorf("len(ConfigFields) = %d, want 1", len(m.ConfigFields))
			}
			break
		}
	}
	if !found {
		t.Errorf("RegisteredProvidersMeta() did not include provider %q", name)
	}
}

func TestGetProviderMeta_NotFound(t *testing.T) {
	_, ok := GetProviderMeta("nonexistent-meta-provider")
	if ok {
		t.Error("GetProviderMeta for unregistered provider should return false")
	}
}

func TestRegister_PanicOnDuplicate(t *testing.T) {
	name := "test-panic-duplicate"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}
	Register(name, factory)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register with duplicate name should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %v", r)
		}
		want := "llm: Register called twice for " + name
		if msg != want {
			t.Errorf("panic message = %q, want %q", msg, want)
		}
	}()

	Register(name, factory)
}

func TestRegister_PanicOnNilFactory(t *testing.T) {
	name := "test-panic-nil-factory"

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register with nil factory should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %v", r)
		}
		want := "llm: Register factory is nil for " + name
		if msg != want {
			t.Errorf("panic message = %q, want %q", msg, want)
		}
	}()

	Register(name, nil)
}

func TestNewProvider_FactoryReceivesConfig(t *testing.T) {
	name := "test-factory-receives-config"
	var receivedCfg ProviderConfig
	factory := func(cfg ProviderConfig) (Provider, error) {
		receivedCfg = cfg
		return &mockProvider{}, nil
	}
	Register(name, factory)

	cfg := ProviderConfig{
		"api_key": "secret-key",
		"model":   "test-model-v2",
	}
	_, err := NewProvider(name, cfg)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if receivedCfg["api_key"] != "secret-key" {
		t.Errorf("factory received api_key = %q, want %q", receivedCfg["api_key"], "secret-key")
	}
	if receivedCfg["model"] != "test-model-v2" {
		t.Errorf("factory received model = %q, want %q", receivedCfg["model"], "test-model-v2")
	}
}

func TestRegisterWithMeta_DefaultPricing(t *testing.T) {
	name := "test-meta-pricing"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockProvider{}, nil
	}
	meta := ProviderMeta{
		Name:        "Priced Provider",
		Description: "provider with pricing",
		DefaultPricing: map[string]TokenPricing{
			"model-a": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
		},
	}
	RegisterWithMeta(name, factory, meta)

	got, ok := GetProviderMeta(name)
	if !ok {
		t.Fatalf("GetProviderMeta(%q) returned false", name)
	}
	pricing, exists := got.DefaultPricing["model-a"]
	if !exists {
		t.Fatal("DefaultPricing missing model-a entry")
	}
	if pricing.InputPerMillion != 3.0 {
		t.Errorf("InputPerMillion = %f, want 3.0", pricing.InputPerMillion)
	}
	if pricing.OutputPerMillion != 15.0 {
		t.Errorf("OutputPerMillion = %f, want 15.0", pricing.OutputPerMillion)
	}
}
