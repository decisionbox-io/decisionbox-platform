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
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Description     string                  `json:"description"`
	ConfigFields    []ConfigField           `json:"config_fields"`
	DefaultPricing  map[string]TokenPricing `json:"default_pricing,omitempty"`   // model -> pricing
	MaxOutputTokens map[string]int          `json:"max_output_tokens,omitempty"` // model -> max output tokens

	// Models is a snapshot of the model catalog filtered to this provider.
	// The dashboard renders this as a combobox for the "model" field
	// (selectable + free-text for models not yet catalogued). When a
	// provider has no catalog entries the slice is empty and the UI
	// falls back to plain text input.
	Models []ModelInfo `json:"models,omitempty"`
}

// ModelInfo is the per-model catalog snapshot the dashboard needs to
// render a combobox with details. It is a deliberately small subset of
// modelcatalog.Entry to keep the /api/v1/providers/llm response compact;
// richer metadata (deprecation, release date, etc.) can be added here
// without another endpoint.
type ModelInfo struct {
	ID                    string  `json:"id"`
	DisplayName           string  `json:"display_name"`
	Wire                  string  `json:"wire"` // "anthropic" | "openai-compat" | "google-native"
	MaxOutputTokens       int     `json:"max_output_tokens,omitempty"`
	InputPricePerMillion  float64 `json:"input_price_per_million,omitempty"`
	OutputPricePerMillion float64 `json:"output_price_per_million,omitempty"`

	// Lifecycle is a free-form status string from the upstream list
	// endpoint — empty for catalog-only rows. Known values include
	// "ACTIVE" and "LEGACY" (Bedrock). The dashboard uses this to
	// grey out deprecated models.
	Lifecycle string `json:"lifecycle,omitempty"`
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

	// Options, when non-empty, tells the UI to render this field as a
	// select with the given value/label pairs. Use with Type="string" for
	// a plain dropdown or with FreeText=true for a combobox.
	Options []ConfigOption `json:"options,omitempty"`

	// FreeText, when true, tells the UI to render a combobox — a text
	// input plus an autocomplete datalist built from Options (or from
	// ProviderMeta.Models when Key=="model"). Users can pick a listed
	// value or type their own. When false and Options is non-empty the
	// UI renders a strict select.
	FreeText bool `json:"free_text,omitempty"`
}

// ConfigOption is one entry in a dropdown-style ConfigField.
type ConfigOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
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
// Each meta is enriched with its catalog Models snapshot if a lookup has
// been wired up (see SetProviderModelsLookup).
func RegisteredProvidersMeta() []ProviderMeta {
	providersMu.RLock()
	metas := make([]ProviderMeta, 0, len(providerMeta))
	for _, m := range providerMeta {
		metas = append(metas, m)
	}
	providersMu.RUnlock()
	lookup := providerModelsLookup
	if lookup != nil {
		for i := range metas {
			if metas[i].Models == nil {
				metas[i].Models = lookup(metas[i].ID)
			}
		}
	}
	return metas
}

// GetProviderMeta returns metadata for a specific provider. The returned
// meta is enriched with its catalog Models snapshot if a lookup has been
// wired up (see SetProviderModelsLookup).
func GetProviderMeta(name string) (ProviderMeta, bool) {
	providersMu.RLock()
	m, ok := providerMeta[name]
	providersMu.RUnlock()
	if !ok {
		return m, false
	}
	if lookup := providerModelsLookup; lookup != nil && m.Models == nil {
		m.Models = lookup(name)
	}
	return m, true
}

// maxTokensCatalogLookup, when non-nil, is consulted first by
// GetMaxOutputTokens. libs/go-common/llm/modelcatalog wires itself here in
// init() so the catalog becomes the single source of truth for model
// ceilings while keeping the llm package free of circular imports.
//
// Set this only once at init time. The helper is not safe for
// concurrent swapping at runtime.
var maxTokensCatalogLookup func(cloud, model string) (int, bool)

// SetMaxTokensCatalogLookup wires a catalog lookup into GetMaxOutputTokens.
// Intended to be called from the modelcatalog package's init(); external
// callers should not use it.
func SetMaxTokensCatalogLookup(fn func(cloud, model string) (int, bool)) {
	maxTokensCatalogLookup = fn
}

// providerModelsLookup, when non-nil, returns the model catalog snapshot
// for a given provider. Wired up by the modelcatalog package in init().
// Looked up lazily by GetProviderMeta / RegisteredProvidersMeta so we
// don't depend on init-order between modelcatalog and providers — either
// one can run first.
var providerModelsLookup func(provider string) []ModelInfo

// SetProviderModelsLookup wires a catalog lookup into provider metadata.
// Intended to be called from the modelcatalog package's init(); external
// callers should not use it.
func SetProviderModelsLookup(fn func(provider string) []ModelInfo) {
	providerModelsLookup = fn
}

// GetMaxOutputTokens returns the max output tokens for a provider+model
// combination. The resolution order is:
//  1. Catalog entry for (provider, model), if present and non-zero —
//     single source of truth for shipped models.
//  2. ProviderMeta.MaxOutputTokens map for the exact model — historical
//     per-provider override, kept for backwards compatibility and for
//     provider-local models (e.g. Ollama's user-chosen GGUFs).
//  3. ProviderMeta.MaxOutputTokens["_default"] — per-provider fallback.
//  4. Global fallback of 8192.
func GetMaxOutputTokens(providerName, model string) int {
	if lookup := maxTokensCatalogLookup; lookup != nil {
		if limit, ok := lookup(providerName, model); ok && limit > 0 {
			return limit
		}
	}
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
