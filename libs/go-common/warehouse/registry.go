package warehouse

import (
	"fmt"
	"sync"
)

// ProviderConfig is a generic key-value configuration passed to provider factories.
// Each provider defines which keys it expects (e.g., "project_id", "dataset", "location").
type ProviderConfig map[string]string

// ProviderFactory creates a Provider from configuration.
// Provider packages implement this and register it via Register().
type ProviderFactory func(cfg ProviderConfig) (Provider, error)

// WarehousePricing holds pricing info for a warehouse provider.
type WarehousePricing struct {
	CostModel           string  `json:"cost_model"`              // "per_byte_scanned", "per_query", "per_hour"
	CostPerTBScannedUSD float64 `json:"cost_per_tb_scanned_usd"` // for per_byte_scanned model
}

// ProviderMeta describes a provider for UI rendering and documentation.
// Providers register this alongside their factory via RegisterWithMeta().
type ProviderMeta struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	ConfigFields   []ConfigField     `json:"config_fields"`
	AuthMethods    []AuthMethod      `json:"auth_methods,omitempty"`
	DefaultPricing *WarehousePricing  `json:"default_pricing,omitempty"`
}

// AuthMethod describes an authentication option for a warehouse provider.
// Each provider declares its supported auth methods. The UI renders a
// selector and shows auth-method-specific fields. The selected method ID
// is stored in the project config as "auth_method".
type AuthMethod struct {
	ID          string        `json:"id"`          // e.g. "adc", "sa_key", "password", "assume_role"
	Name        string        `json:"name"`        // display name: "Application Default Credentials"
	Description string        `json:"description"` // help text
	Fields      []ConfigField `json:"fields"`      // fields specific to this auth method
}

// ConfigField describes a single configuration field for a provider.
// The UI renders a form dynamically from these fields.
type ConfigField struct {
	Key         string `json:"key"`          // config key: "project_id", "dataset"
	Label       string `json:"label"`        // display label: "GCP Project ID"
	Description string `json:"description"`  // help text
	Required    bool   `json:"required"`
	Type        string `json:"type"`         // "string", "number", "boolean", "credential" (stored as secret, not in project config)
	Default     string `json:"default"`      // default value
	Placeholder string `json:"placeholder"`  // placeholder text
}

var (
	providersMu sync.RWMutex
	providers   = make(map[string]ProviderFactory)
	providerMeta = make(map[string]ProviderMeta)
)

// Register makes a provider available by name.
// Provider packages call this in their init() function:
//
//	func init() {
//	    warehouse.Register("clickhouse", func(cfg warehouse.ProviderConfig) (warehouse.Provider, error) {
//	        return NewClickHouseProvider(cfg["dsn"], cfg["database"])
//	    })
//	}
//
// Services then select the provider via WAREHOUSE_PROVIDER env var.
func Register(name string, factory ProviderFactory) {
	providersMu.Lock()
	defer providersMu.Unlock()
	if factory == nil {
		panic("warehouse: Register factory is nil for " + name)
	}
	if _, exists := providers[name]; exists {
		panic("warehouse: Register called twice for " + name)
	}
	providers[name] = factory
}

// NewProvider creates a provider by name using the registered factory.
// Returns an error if the provider name is not registered.
//
// Usage in services:
//
//	provider, err := warehouse.NewProvider("bigquery", warehouse.ProviderConfig{
//	    "project_id": os.Getenv("WAREHOUSE_PROJECT_ID"),
//	    "dataset":    os.Getenv("WAREHOUSE_DATASET"),
//	})
func NewProvider(name string, cfg ProviderConfig) (Provider, error) {
	providersMu.RLock()
	factory, exists := providers[name]
	providersMu.RUnlock()

	if !exists {
		registered := make([]string, 0, len(providers))
		providersMu.RLock()
		for k := range providers {
			registered = append(registered, k)
		}
		providersMu.RUnlock()
		return nil, fmt.Errorf("warehouse: unknown provider %q (registered: %v)", name, registered)
	}

	return factory(cfg)
}

// RegisterWithMeta registers a provider with metadata for UI rendering.
func RegisterWithMeta(name string, factory ProviderFactory, meta ProviderMeta) {
	Register(name, factory)
	providersMu.Lock()
	meta.ID = name
	providerMeta[name] = meta
	providersMu.Unlock()
}

// RegisteredProviders returns the names of all registered providers.
func RegisteredProviders() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	names := make([]string, 0, len(providers))
	for k := range providers {
		names = append(names, k)
	}
	return names
}

// RegisteredProvidersMeta returns metadata for all registered providers.
func RegisteredProvidersMeta() []ProviderMeta {
	providersMu.RLock()
	defer providersMu.RUnlock()
	metas := make([]ProviderMeta, 0, len(providerMeta))
	for _, m := range providerMeta {
		metas = append(metas, m)
	}
	return metas
}

// GetProviderMeta returns metadata for a specific provider.
func GetProviderMeta(name string) (ProviderMeta, bool) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	m, ok := providerMeta[name]
	return m, ok
}
