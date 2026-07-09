package connectors

import (
	"fmt"
	"sync"
)

// Config is a generic key-value configuration passed to connector factories —
// the instance-level app-registration credentials an operator supplies once per
// deployment (e.g. "client_id", "client_secret", "api_key", "app_id"). Each
// connector defines which keys it expects.
type Config map[string]string

// Factory creates a Connector from configuration. Connector packages implement
// this and register it via RegisterWithMeta().
type Factory func(cfg Config) (Connector, error)

// ProviderMeta describes a connector for UI rendering (the instance-admin
// app-registration form).
type ProviderMeta struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ConfigFields []ConfigField `json:"config_fields"`
}

// ConfigField describes a single instance-level configuration field.
type ConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Type        string `json:"type"` // "string" | "credential"
	Placeholder string `json:"placeholder"`
	// BrowserSafe marks fields a config endpoint may return to the dashboard at
	// runtime (e.g. client_id, api_key, app_id — the native picker needs them
	// client-side). Non-browser-safe fields (client_secret) are backend-only and
	// must never be returned by a config GET.
	BrowserSafe bool `json:"browser_safe"`
}

var (
	registryMu  sync.RWMutex
	factories   = make(map[string]Factory)
	factoryMeta = make(map[string]ProviderMeta)
)

// Register makes a connector available by name.
// Connector packages call this in their init() function.
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if factory == nil {
		panic("connectors: Register factory is nil for " + name)
	}
	if _, exists := factories[name]; exists {
		panic("connectors: Register called twice for " + name)
	}
	factories[name] = factory
}

// RegisterWithMeta registers a connector with metadata for UI rendering.
func RegisterWithMeta(name string, factory Factory, meta ProviderMeta) {
	Register(name, factory)
	registryMu.Lock()
	meta.ID = name
	factoryMeta[name] = meta
	registryMu.Unlock()
}

// NewConnector creates a connector by name using the registered factory.
// Returns a clear error (not a panic) when no connector of that kind is
// registered — so a platform-only build, which registers none, degrades
// gracefully.
func NewConnector(name string, cfg Config) (Connector, error) {
	registryMu.RLock()
	factory, exists := factories[name]
	registryMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("connectors: unknown connector %q (registered: %v)", name, RegisteredConnectors())
	}
	return factory(cfg)
}

// RegisteredConnectors returns the names of all registered connectors.
func RegisteredConnectors() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for k := range factories {
		names = append(names, k)
	}
	return names
}

// RegisteredMeta returns metadata for all registered connectors.
func RegisteredMeta() []ProviderMeta {
	registryMu.RLock()
	defer registryMu.RUnlock()
	metas := make([]ProviderMeta, 0, len(factoryMeta))
	for _, m := range factoryMeta {
		metas = append(metas, m)
	}
	return metas
}

// GetMeta returns metadata for a specific connector.
func GetMeta(name string) (ProviderMeta, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	m, ok := factoryMeta[name]
	return m, ok
}
