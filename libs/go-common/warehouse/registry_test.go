package warehouse

import (
	"context"
	"sync"
	"testing"
)

// mockWarehouseProvider is a minimal Provider implementation for registry tests.
type mockWarehouseProvider struct {
	dataset string
}

func (m *mockWarehouseProvider) Query(_ context.Context, _ string, _ map[string]interface{}) (*QueryResult, error) {
	return &QueryResult{}, nil
}

func (m *mockWarehouseProvider) ListTables(_ context.Context) ([]string, error) {
	return nil, nil
}

func (m *mockWarehouseProvider) ListTablesInDataset(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (m *mockWarehouseProvider) GetTableSchema(_ context.Context, _ string) (*TableSchema, error) {
	return nil, nil
}

func (m *mockWarehouseProvider) GetTableSchemaInDataset(_ context.Context, _, _ string) (*TableSchema, error) {
	return nil, nil
}

func (m *mockWarehouseProvider) GetDataset() string {
	return m.dataset
}

func (m *mockWarehouseProvider) SQLDialect() string {
	return "Mock SQL"
}

func (m *mockWarehouseProvider) SQLFixPrompt() string {
	return ""
}

func (m *mockWarehouseProvider) ValidateReadOnly(_ context.Context) error {
	return nil
}

func (m *mockWarehouseProvider) HealthCheck(_ context.Context) error {
	return nil
}

func (m *mockWarehouseProvider) Close() error {
	return nil
}

// registrations use sync.Once to be safe with -count=N and -parallel.
var (
	whRegisterMeta     sync.Once
	whRegisterSuccess  sync.Once
	whRegisterList     sync.Once
	whRegisterMetaList sync.Once
	whRegisterCfg      sync.Once
)

func TestRegisterWithMeta(t *testing.T) {
	name := "test-wh-register-with-meta"
	whRegisterMeta.Do(func() {
		RegisterWithMeta(name, func(_ ProviderConfig) (Provider, error) {
			return &mockWarehouseProvider{dataset: "analytics"}, nil
		}, ProviderMeta{
			Name:        "Test Warehouse",
			Description: "a test warehouse provider",
			ConfigFields: []ConfigField{
				{Key: "project_id", Label: "Project ID", Required: true, Type: "string"},
				{Key: "dataset", Label: "Dataset", Required: true, Type: "string"},
			},
			DefaultPricing: &WarehousePricing{
				CostModel:           "per_byte_scanned",
				CostPerTBScannedUSD: 5.0,
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
	if got.Name != "Test Warehouse" {
		t.Errorf("ProviderMeta.Name = %q, want %q", got.Name, "Test Warehouse")
	}
	if len(got.ConfigFields) != 2 {
		t.Fatalf("len(ConfigFields) = %d, want 2", len(got.ConfigFields))
	}
	if got.DefaultPricing == nil || got.DefaultPricing.CostPerTBScannedUSD != 5.0 {
		t.Error("DefaultPricing not set correctly")
	}
}

func TestNewProvider_Success(t *testing.T) {
	name := "test-wh-new-provider-success"
	whRegisterSuccess.Do(func() {
		Register(name, func(cfg ProviderConfig) (Provider, error) {
			return &mockWarehouseProvider{dataset: cfg["dataset"]}, nil
		})
	})

	provider, err := NewProvider(name, ProviderConfig{"dataset": "my_dataset"})
	if err != nil {
		t.Fatalf("NewProvider(%q) returned error: %v", name, err)
	}
	if provider == nil {
		t.Fatal("NewProvider returned nil provider")
	}
	if provider.GetDataset() != "my_dataset" {
		t.Errorf("GetDataset() = %q, want %q", provider.GetDataset(), "my_dataset")
	}
}

func TestNewProvider_UnknownName(t *testing.T) {
	_, err := NewProvider("nonexistent-warehouse-xyz", ProviderConfig{})
	if err == nil {
		t.Fatal("NewProvider with unknown name should return error")
	}
}

func TestRegisteredProviders(t *testing.T) {
	name := "test-wh-registered-providers"
	whRegisterList.Do(func() {
		Register(name, func(_ ProviderConfig) (Provider, error) {
			return &mockWarehouseProvider{}, nil
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
		t.Errorf("RegisteredProviders() did not include %q", name)
	}
}

func TestRegisteredProvidersMeta(t *testing.T) {
	name := "test-wh-registered-providers-meta"
	whRegisterMetaList.Do(func() {
		RegisterWithMeta(name, func(_ ProviderConfig) (Provider, error) {
			return &mockWarehouseProvider{}, nil
		}, ProviderMeta{
			Name: "Meta Warehouse Test",
		})
	})

	metas := RegisteredProvidersMeta()
	found := false
	for _, m := range metas {
		if m.ID == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("RegisteredProvidersMeta() did not include provider %q", name)
	}
}

func TestGetProviderMeta_NotFound(t *testing.T) {
	_, ok := GetProviderMeta("nonexistent-wh-meta-provider")
	if ok {
		t.Error("GetProviderMeta for unregistered provider should return false")
	}
}

func TestRegister_PanicOnDuplicate(t *testing.T) {
	name := "test-wh-panic-duplicate"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{}, nil
	}
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
		want := "warehouse: Register called twice for " + name
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
	}()

	Register("test-wh-panic-nil-factory", nil)
}

func TestNewProvider_FactoryReceivesConfig(t *testing.T) {
	name := "test-wh-factory-receives-config"
	whRegisterCfg.Do(func() {
		Register(name, func(cfg ProviderConfig) (Provider, error) {
			return &mockWarehouseProvider{dataset: cfg["dataset"]}, nil
		})
	})

	provider, err := NewProvider(name, ProviderConfig{"dataset": "analytics"})
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if provider.GetDataset() != "analytics" {
		t.Errorf("GetDataset() = %q, want %q", provider.GetDataset(), "analytics")
	}
}
