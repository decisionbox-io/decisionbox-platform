package embedding

import (
	"fmt"
	"sync"
)

// ProviderConfig is a generic key-value configuration passed to provider factories.
// Each provider defines which keys it expects (e.g., "api_key", "model").
type ProviderConfig map[string]string

// ProviderFactory creates a Provider from configuration.
// Provider packages implement this and register it via RegisterWithMeta().
type ProviderFactory func(cfg ProviderConfig) (Provider, error)

// ProviderMeta describes an embedding provider for UI rendering.
type ProviderMeta struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ConfigFields []ConfigField `json:"config_fields"`
	Models       []ModelInfo   `json:"models"`
}

// ConfigField describes a single configuration field.
type ConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Placeholder string `json:"placeholder"`
}

// ModelInfo describes an embedding model offered by a provider.
type ModelInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Dimensions int    `json:"dimensions"`
}

var (
	providersMu  sync.RWMutex
	providers    = make(map[string]ProviderFactory)
	providerMeta = make(map[string]ProviderMeta)
)

// Register makes an embedding provider available by name.
// Provider packages call this in their init() function.
func Register(name string, factory ProviderFactory) {
	providersMu.Lock()
	defer providersMu.Unlock()
	if factory == nil {
		panic("embedding: Register factory is nil for " + name)
	}
	if _, exists := providers[name]; exists {
		panic("embedding: Register called twice for " + name)
	}
	providers[name] = factory
}

// RegisterWithMeta registers an embedding provider with metadata for UI rendering.
func RegisterWithMeta(name string, factory ProviderFactory, meta ProviderMeta) {
	Register(name, factory)
	providersMu.Lock()
	meta.ID = name
	providerMeta[name] = meta
	providersMu.Unlock()
}

// NewProvider creates an embedding provider by name using the registered factory.
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
		return nil, fmt.Errorf("embedding: unknown provider %q (registered: %v)", name, registered)
	}

	return factory(cfg)
}

// RegisteredProviders returns the names of all registered embedding providers.
func RegisteredProviders() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	names := make([]string, 0, len(providers))
	for k := range providers {
		names = append(names, k)
	}
	return names
}

// RegisteredProvidersMeta returns metadata for all registered embedding providers.
func RegisteredProvidersMeta() []ProviderMeta {
	providersMu.RLock()
	defer providersMu.RUnlock()
	metas := make([]ProviderMeta, 0, len(providerMeta))
	for _, m := range providerMeta {
		metas = append(metas, m)
	}
	return metas
}

// GetProviderMeta returns metadata for a specific embedding provider.
func GetProviderMeta(name string) (ProviderMeta, bool) {
	providersMu.RLock()
	defer providersMu.RUnlock()
	m, ok := providerMeta[name]
	return m, ok
}
