package llm

import (
	"context"
	"fmt"
	"sync"
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

// registrations use sync.Once to be safe with -count=N and -parallel.
var (
	registerMeta     sync.Once
	registerSuccess  sync.Once
	registerList     sync.Once
	registerMetaList sync.Once
	registerCfg      sync.Once
	registerPricing  sync.Once
)

func TestRegisterWithMeta(t *testing.T) {
	name := "test-register-with-meta"
	registerMeta.Do(func() {
		RegisterWithMeta(name, func(_ ProviderConfig) (Provider, error) {
			return &mockProvider{}, nil
		}, ProviderMeta{
			Name:        "Test Provider",
			Description: "a test provider",
			ConfigFields: []ConfigField{
				{Key: "api_key", Label: "API Key", Required: true, Type: "string"},
			},
		})
	})

	got, ok := GetProviderMeta(name)
	if !ok {
		t.Fatalf("GetProviderMeta(%q) returned false", name)
	}
	if got.ID != name {
		t.Errorf("ProviderMeta.ID = %q, want %q", got.ID, name)
	}
	if got.Name != "Test Provider" {
		t.Errorf("ProviderMeta.Name = %q, want %q", got.Name, "Test Provider")
	}
	if len(got.ConfigFields) != 1 {
		t.Fatalf("len(ConfigFields) = %d, want 1", len(got.ConfigFields))
	}
}

func TestNewProvider_Success(t *testing.T) {
	name := "test-new-provider-success"
	registerSuccess.Do(func() {
		Register(name, func(_ ProviderConfig) (Provider, error) {
			return &mockProvider{}, nil
		})
	})

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
	registerList.Do(func() {
		Register(name, func(_ ProviderConfig) (Provider, error) {
			return &mockProvider{}, nil
		})
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
	registerMetaList.Do(func() {
		RegisterWithMeta(name, func(_ ProviderConfig) (Provider, error) {
			return &mockProvider{}, nil
		}, ProviderMeta{
			Name:        "Meta Test",
			Description: "for meta list test",
			ConfigFields: []ConfigField{
				{Key: "token", Label: "Token", Required: true, Type: "string"},
			},
		})
	})

	metas := RegisteredProvidersMeta()
	found := false
	for _, m := range metas {
		if m.ID == name {
			found = true
			if m.Name != "Meta Test" {
				t.Errorf("ProviderMeta.Name = %q, want %q", m.Name, "Meta Test")
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
	// First registration (safe with sync.Once pattern not needed — panic test is one-shot)
	func() {
		defer func() { recover() }()
		Register(name, factory)
	}()

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
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register with nil factory should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %v", r)
		}
		want := "llm: Register factory is nil for test-panic-nil-factory"
		if msg != want {
			t.Errorf("panic message = %q, want %q", msg, want)
		}
	}()

	Register("test-panic-nil-factory", nil)
}

func TestNewProvider_FactoryReceivesConfig(t *testing.T) {
	name := "test-factory-receives-config"
	registerCfg.Do(func() {
		Register(name, func(cfg ProviderConfig) (Provider, error) {
			if cfg["api_key"] == "" {
				return nil, fmt.Errorf("api_key required")
			}
			return &mockProvider{}, nil
		})
	})

	// Factory should receive the config and validate it
	_, err := NewProvider(name, ProviderConfig{"api_key": "secret-key"})
	if err != nil {
		t.Fatalf("NewProvider with valid config returned error: %v", err)
	}
}

func TestRegisterWithMeta_DefaultPricing(t *testing.T) {
	name := "test-meta-pricing"
	registerPricing.Do(func() {
		RegisterWithMeta(name, func(_ ProviderConfig) (Provider, error) {
			return &mockProvider{}, nil
		}, ProviderMeta{
			Name:        "Priced Provider",
			Description: "provider with pricing",
			DefaultPricing: map[string]TokenPricing{
				"model-a": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			},
		})
	})

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
