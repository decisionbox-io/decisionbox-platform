package llm

import (
	"fmt"
	"sync"
)

// ProviderConfig is a generic key-value configuration passed to provider factories.
// Each provider defines which keys it expects (e.g., "api_key", "model", "timeout").
type ProviderConfig map[string]string

// ProviderFactory creates a Provider from configuration.
// Provider packages implement this and register it via Register().
type ProviderFactory func(cfg ProviderConfig) (Provider, error)

// TokenPricing holds per-token pricing for an LLM model.
type TokenPricing struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// ProviderMeta describes a provider for UI rendering.
type ProviderMeta struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	Description     string                    `json:"description"`
	ConfigFields    []ConfigField             `json:"config_fields"`
	DefaultPricing  map[string]TokenPricing   `json:"default_pricing,omitempty"`   // model -> pricing
	MaxOutputTokens map[string]int            `json:"max_output_tokens,omitempty"` // model -> max output tokens
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

var (
	providersMu sync.RWMutex
	providers   = make(map[string]ProviderFactory)
	providerMeta = make(map[string]ProviderMeta)
)

// Register makes a provider available by name.
// Provider packages call this in their init() function:
//
//	func init() {
//	    llm.Register("openai", func(cfg llm.ProviderConfig) (llm.Provider, error) {
//	        return NewOpenAIProvider(cfg["api_key"], cfg["model"])
//	    })
//	}
//
// Services then select the provider via LLM_PROVIDER env var.
func Register(name string, factory ProviderFactory) {
	providersMu.Lock()
	defer providersMu.Unlock()
	if factory == nil {
		panic("llm: Register factory is nil for " + name)
	}
	if _, exists := providers[name]; exists {
		panic("llm: Register called twice for " + name)
	}
	providers[name] = factory
}

// NewProvider creates a provider by name using the registered factory.
// Returns an error if the provider name is not registered.
//
// Usage in services:
//
//	provider, err := llm.NewProvider("claude", llm.ProviderConfig{
//	    "api_key": os.Getenv("LLM_API_KEY"),
//	    "model":   "claude-sonnet-4-20250514",
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
		return nil, fmt.Errorf("llm: unknown provider %q (registered: %v)", name, registered)
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

// GetMaxOutputTokens returns the max output tokens for a provider+model combination.
// Falls back to "_default" key, then to 8192 if not configured.
func GetMaxOutputTokens(providerName, model string) int {
	providersMu.RLock()
	defer providersMu.RUnlock()
	meta, ok := providerMeta[providerName]
	if !ok || meta.MaxOutputTokens == nil {
		return 8192
	}
	if limit, ok := meta.MaxOutputTokens[model]; ok {
		return limit
	}
	if limit, ok := meta.MaxOutputTokens["_default"]; ok {
		return limit
	}
	return 8192
}
