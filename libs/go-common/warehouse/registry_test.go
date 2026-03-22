package warehouse

import (
	"context"
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

func TestRegisterWithMeta(t *testing.T) {
	name := "test-wh-register-with-meta"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{dataset: "analytics"}, nil
	}
	meta := ProviderMeta{
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
	}

	RegisterWithMeta(name, factory, meta)

	got, ok := GetProviderMeta(name)
	if !ok {
		t.Fatalf("GetProviderMeta(%q) returned false, want true", name)
	}
	if got.ID != name {
		t.Errorf("ProviderMeta.ID = %q, want %q", got.ID, name)
	}
	if got.Name != "Test Warehouse" {
		t.Errorf("ProviderMeta.Name = %q, want %q", got.Name, "Test Warehouse")
	}
	if got.Description != "a test warehouse provider" {
		t.Errorf("ProviderMeta.Description = %q, want %q", got.Description, "a test warehouse provider")
	}
	if len(got.ConfigFields) != 2 {
		t.Fatalf("len(ConfigFields) = %d, want 2", len(got.ConfigFields))
	}
	if got.ConfigFields[0].Key != "project_id" {
		t.Errorf("ConfigFields[0].Key = %q, want %q", got.ConfigFields[0].Key, "project_id")
	}
	if got.ConfigFields[1].Key != "dataset" {
		t.Errorf("ConfigFields[1].Key = %q, want %q", got.ConfigFields[1].Key, "dataset")
	}
	if got.DefaultPricing == nil {
		t.Fatal("DefaultPricing is nil")
	}
	if got.DefaultPricing.CostModel != "per_byte_scanned" {
		t.Errorf("CostModel = %q, want %q", got.DefaultPricing.CostModel, "per_byte_scanned")
	}
	if got.DefaultPricing.CostPerTBScannedUSD != 5.0 {
		t.Errorf("CostPerTBScannedUSD = %f, want 5.0", got.DefaultPricing.CostPerTBScannedUSD)
	}
}

func TestNewProvider_Success(t *testing.T) {
	name := "test-wh-new-provider-success"
	factory := func(cfg ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{dataset: cfg["dataset"]}, nil
	}
	Register(name, factory)

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
	if provider.SQLDialect() != "Mock SQL" {
		t.Errorf("SQLDialect() = %q, want %q", provider.SQLDialect(), "Mock SQL")
	}
}

func TestNewProvider_UnknownName(t *testing.T) {
	_, err := NewProvider("nonexistent-warehouse-xyz", ProviderConfig{})
	if err == nil {
		t.Fatal("NewProvider with unknown name should return error")
	}

	want := `warehouse: unknown provider "nonexistent-warehouse-xyz"`
	if len(err.Error()) < len(want) {
		t.Fatalf("error too short: %q", err.Error())
	}
	if err.Error()[:len(want)] != want {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

func TestRegisteredProviders(t *testing.T) {
	name := "test-wh-registered-providers"
	Register(name, func(_ ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{}, nil
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
	name := "test-wh-registered-providers-meta"
	factory := func(_ ProviderConfig) (Provider, error) {
		return &mockWarehouseProvider{}, nil
	}
	meta := ProviderMeta{
		Name:        "Meta Warehouse Test",
		Description: "for meta list test",
		ConfigFields: []ConfigField{
			{Key: "host", Label: "Host", Required: true, Type: "string"},
		},
	}
	RegisterWithMeta(name, factory, meta)

	metas := RegisteredProvidersMeta()
	found := false
	for _, m := range metas {
		if m.ID == name {
			found = true
			if m.Name != "Meta Warehouse Test" {
				t.Errorf("ProviderMeta.Name = %q, want %q", m.Name, "Meta Warehouse Test")
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
		want := "warehouse: Register called twice for " + name
		if msg != want {
			t.Errorf("panic message = %q, want %q", msg, want)
		}
	}()

	Register(name, factory)
}

func TestRegister_PanicOnNilFactory(t *testing.T) {
	name := "test-wh-panic-nil-factory"

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register with nil factory should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %v", r)
		}
		want := "warehouse: Register factory is nil for " + name
		if msg != want {
			t.Errorf("panic message = %q, want %q", msg, want)
		}
	}()

	Register(name, nil)
}

func TestNewProvider_FactoryReceivesConfig(t *testing.T) {
	name := "test-wh-factory-receives-config"
	var receivedCfg ProviderConfig
	factory := func(cfg ProviderConfig) (Provider, error) {
		receivedCfg = cfg
		return &mockWarehouseProvider{}, nil
	}
	Register(name, factory)

	cfg := ProviderConfig{
		"project_id": "my-project",
		"dataset":    "analytics",
		"location":   "us-central1",
	}
	_, err := NewProvider(name, cfg)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	if receivedCfg["project_id"] != "my-project" {
		t.Errorf("factory received project_id = %q, want %q", receivedCfg["project_id"], "my-project")
	}
	if receivedCfg["dataset"] != "analytics" {
		t.Errorf("factory received dataset = %q, want %q", receivedCfg["dataset"], "analytics")
	}
	if receivedCfg["location"] != "us-central1" {
		t.Errorf("factory received location = %q, want %q", receivedCfg["location"], "us-central1")
	}
}
