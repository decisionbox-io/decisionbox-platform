package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ProviderConfig is a generic key-value configuration passed to provider factories.
// Each provider defines which keys it expects (e.g., "api_key", "model", "timeout").
type ProviderConfig map[string]string

// ProviderFactory creates a Provider from configuration.
// Provider packages implement this and register it via Register().
type ProviderFactory func(cfg ProviderConfig) (Provider, error)

// TokenPricing holds per-token list pricing for an LLM model in USD per
// 1M tokens. Zero values are treated as "unknown" by callers — the
// dashboard hides cost estimates rather than showing misleading $0.
type TokenPricing struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

// Wire identifies the request/response schema a model expects.
//
// A single provider can host many models speaking different wires. For
// example AWS Bedrock serves Claude (Anthropic Messages) alongside
// Qwen / DeepSeek / Mistral / Llama (OpenAI Chat Completions); each
// model's catalog entry declares its wire and the provider's Chat()
// method dispatches on it.
//
// Adding a new wire requires a coordinated change across every
// provider that exposes models speaking it (each provider has a
// dispatch switch).
type Wire string

const (
	// WireUnknown is the zero value. A catalog entry must never carry
	// it — Register panics. ResolveWire returns it together with an
	// actionable error to signal "no rule matched, ask the user".
	WireUnknown Wire = ""

	// WireAnthropic — Anthropic Messages API:
	//   request:  {messages, system, max_tokens, temperature}
	//   response: {content[{type,text}], stop_reason, usage{input_tokens,output_tokens}}
	// Spoken by Claude direct, Claude-on-Bedrock, Claude-on-Vertex,
	// Claude-on-Azure-Foundry.
	WireAnthropic Wire = "anthropic"

	// WireOpenAICompat — OpenAI /chat/completions:
	//   request:  {model, messages[{role,content}], max_tokens, temperature}
	//   response: {choices[{message,finish_reason}], usage{prompt_tokens,completion_tokens}}
	// Spoken by OpenAI direct, GPT-on-Azure-Foundry, Qwen / DeepSeek /
	// Mistral / Llama on Bedrock, Llama / Qwen / DeepSeek / Mistral on
	// Vertex Model Garden MaaS.
	WireOpenAICompat Wire = "openai-compat"

	// WireGoogleNative — Vertex AI generateContent:
	//   request:  {contents[{role,parts[{text}]}], generationConfig}
	//   response: {candidates[{content.parts[{text}],finishReason}], usageMetadata}
	// Spoken by Gemini on Vertex. (Vertex also exposes Gemini through
	// an OpenAI-compatible surface; a catalog entry can pick either
	// wire.)
	WireGoogleNative Wire = "google-native"
)

// Valid reports whether w is a known, non-Unknown wire. Used by
// projects.go to reject typos in `llm.config.wire_override` before
// they reach a provider.
func (w Wire) Valid() bool {
	switch w {
	case WireAnthropic, WireOpenAICompat, WireGoogleNative:
		return true
	}
	return false
}

// ParseWire normalises a user-supplied wire string (case-insensitive,
// trimmed, underscores and spaces accepted in place of dashes) to a
// Wire. Returns WireUnknown when the input does not match a known
// wire — call Valid() on the result to gate error responses.
func ParseWire(s string) Wire {
	normalized := strings.ToLower(strings.TrimSpace(s))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	switch normalized {
	case "anthropic":
		return WireAnthropic
	case "openai-compat", "openai-compatible", "openai":
		return WireOpenAICompat
	case "google-native", "google", "gemini":
		return WireGoogleNative
	}
	return WireUnknown
}

// ModelEntry describes one model in a provider's catalog. It is the
// single source of truth for the model's wire format, output-token
// cap, list pricing, and dashboard display.
//
// One ModelEntry can be reached by many ID strings via Aliases — used
// to capture every cloud-side variant of the same underlying model.
// Examples on Bedrock for Claude Opus 4.7:
//
//	ID:      "anthropic.claude-opus-4-7-v1:0"
//	Aliases: ["us.anthropic.claude-opus-4-7-v1:0",
//	          "eu.anthropic.claude-opus-4-7-v1:0",
//	          "apac.anthropic.claude-opus-4-7-v1:0",
//	          "global.anthropic.claude-opus-4-7-v1:0",
//	          // short forms (no -v1:0 suffix)
//	          "us.anthropic.claude-opus-4-7",
//	          "eu.anthropic.claude-opus-4-7",
//	          "global.anthropic.claude-opus-4-7-v1",
//	          "global.anthropic.claude-opus-4-7",
//	          // bare family names users may paste
//	          "claude-opus-4-7",
//	          "opus-4-7"]
type ModelEntry struct {
	// ID is the canonical identifier the dashboard shows and that
	// /api/v1/providers/llm exposes. Should be the form the upstream
	// cloud documents as the primary model name.
	ID string

	// Aliases are alternate identifiers that resolve to this entry.
	// Match is exact, case-sensitive, against the user-supplied
	// model string in ChatRequest.Model. Aliases are NOT serialised
	// to /api/v1/providers/llm (one combobox row per canonical ID).
	Aliases []string

	// DisplayName is the human-readable label rendered by the
	// dashboard. Defaults to ID when empty.
	DisplayName string

	// Wire is the request/response schema this model speaks. Required
	// for multi-wire providers (bedrock, vertex-ai, azure-foundry) so
	// the dispatch switch can route correctly; may be left WireUnknown
	// for single-wire providers (claude, openai, ollama) whose Chat()
	// has no dispatch step. Must be either WireUnknown or Valid().
	Wire Wire

	// MaxOutputTokens is the cap the agent will use when the caller
	// does not specify max_tokens. Required (must be > 0).
	MaxOutputTokens int

	// MaxInputTokens is the upstream-published context window for this
	// model. Callers that assemble multi-turn prompts (e.g. /ask) use
	// it to size the history budget so the request never exceeds the
	// provider's input cap.
	//
	// Optional. Zero means "unknown" — callers fall back to
	// DefaultMaxInputTokens, which is conservative enough that trim
	// happens before any shipped model would 4xx.
	MaxInputTokens int

	// Encoding is the BPE tokenizer name for OpenAI-family models
	// (e.g. "cl100k_base" for gpt-4, "o200k_base" for gpt-4o / gpt-5
	// / o-series). Read by the OpenAI provider's TokenCounter so
	// catalog edits drive tokenizer selection without a code change.
	//
	// Empty for non-OpenAI models — their providers either have a
	// native count API (Anthropic /messages/count_tokens, Vertex
	// countTokens) or fall back to the approximation counter.
	Encoding string

	// Pricing carries list price per 1M tokens. Optional — zero values
	// surface as "unknown" in the dashboard rather than misleading
	// $0.
	Pricing TokenPricing

	// Lifecycle is a free-form status string. Empty for catalog-only
	// rows; populated by the live-list merge with values like
	// "ACTIVE" or "LEGACY" so the dashboard can grey out deprecated
	// models.
	Lifecycle string
}

// matches reports whether id resolves to this entry. Exact,
// case-sensitive match against ID and each alias.
func (e ModelEntry) matches(id string) bool {
	if id == "" {
		return false
	}
	if id == e.ID {
		return true
	}
	for _, a := range e.Aliases {
		if id == a {
			return true
		}
	}
	return false
}

// ModelInfo is the per-model snapshot rendered into the dashboard
// combobox via /api/v1/providers/llm. Built from a ModelEntry; the
// JSON shape stays compatible so the dashboard's TypeScript types
// continue to work without changes.
type ModelInfo struct {
	ID                    string  `json:"id"`
	DisplayName           string  `json:"display_name"`
	Wire                  string  `json:"wire"`
	MaxOutputTokens       int     `json:"max_output_tokens,omitempty"`
	MaxInputTokens        int     `json:"max_input_tokens,omitempty"`
	InputPricePerMillion  float64 `json:"input_price_per_million,omitempty"`
	OutputPricePerMillion float64 `json:"output_price_per_million,omitempty"`
	Lifecycle             string  `json:"lifecycle,omitempty"`
}

// ModelFamilyInferrer maps a model ID to a wire based on a provider-
// local naming convention (typically a prefix table). Used by
// ResolveWire when the model is not in the catalog and no
// wire_override was set — lets a freshly-released model in a known
// family dispatch without any per-model config.
//
// Return WireUnknown when the ID does not match any known family.
type ModelFamilyInferrer func(model string) Wire

// ProviderMeta describes a provider for UI rendering and dispatch.
type ProviderMeta struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	ConfigFields []ConfigField `json:"config_fields"`

	// Models is the provider's authoritative model catalog. Each
	// entry can be matched by its canonical ID or any registered
	// alias. Empty for providers without a fixed catalog (Ollama
	// loads models out-of-band from the local server).
	//
	// Not serialised directly — the /api/v1/providers/llm response
	// carries Models() output instead so aliases stay internal.
	Models []ModelEntry `json:"-"`

	// DefaultMaxOutputTokens is the cap returned for IDs that match
	// no entry in Models. Should reflect a sane floor for the
	// provider — high enough that long-form generations do not
	// truncate, low enough that no shipped model rejects it as
	// exceeding the upstream cap. 0 means "fall back to the global
	// 8192 default".
	DefaultMaxOutputTokens int `json:"default_max_output_tokens,omitempty"`

	// DefaultMaxInputTokens is the context-window cap returned for IDs
	// that match no entry in Models. 0 means "fall back to the global
	// DefaultMaxInputTokens" (set conservatively so callers truncate
	// rather than overshoot an unknown model's upstream window).
	DefaultMaxInputTokens int `json:"default_max_input_tokens,omitempty"`

	// FamilyInferrer, when non-nil, is consulted by ResolveWire when
	// the catalog misses and no wire_override is set. Provider-local;
	// no central registry. Typically a prefix scan — see the bedrock
	// / vertex-ai / azure-foundry providers for examples.
	FamilyInferrer ModelFamilyInferrer `json:"-"`

	// SupportsTools declares whether the provider's Chat method
	// honours ChatRequest.Tools. When false, callers with a
	// tool-dependent flow (e.g. /ask function-calling) must pick a
	// different provider or skip tools.
	SupportsTools bool `json:"supports_tools"`
}

// FindModel returns the catalog entry whose ID or alias matches the
// given model string. Returns (zero, false) when no entry matches.
func (m ProviderMeta) FindModel(model string) (ModelEntry, bool) {
	for _, e := range m.Models {
		if e.matches(model) {
			return e, true
		}
	}
	return ModelEntry{}, false
}

// ResolveWire is the dispatch helper called by every multi-wire
// provider's Chat() method.
//
// Resolution order:
//  1. ModelEntry.Wire from the catalog (canonical ID or alias).
//  2. wireOverride from project config — user explicit, beats the
//     family-inferrer fallback.
//  3. ProviderMeta.FamilyInferrer — recognises unseen models in known
//     families by prefix.
//  4. WireUnknown + actionable error.
//
// The provider's dispatch switch is responsible for rejecting wires
// it does not implement; ResolveWire returns whichever wire the
// catalog declares regardless of provider support.
func (m ProviderMeta) ResolveWire(model string, wireOverride Wire) (Wire, error) {
	if e, ok := m.FindModel(model); ok && e.Wire != WireUnknown {
		return e.Wire, nil
	}
	if wireOverride != WireUnknown {
		return wireOverride, nil
	}
	if m.FamilyInferrer != nil {
		if w := m.FamilyInferrer(model); w != WireUnknown {
			return w, nil
		}
	}
	return WireUnknown, fmt.Errorf(
		"%s: model %q not in catalog and no llm.config.wire_override set; "+
			"set wire_override to one of: %s, %s, %s",
		m.ID, model, WireAnthropic, WireOpenAICompat, WireGoogleNative,
	)
}

// MaxOutputTokensFor returns the cap for the given model ID.
// Resolution: catalog (ID + aliases) → DefaultMaxOutputTokens →
// global 8192.
func (m ProviderMeta) MaxOutputTokensFor(model string) int {
	if e, ok := m.FindModel(model); ok && e.MaxOutputTokens > 0 {
		return e.MaxOutputTokens
	}
	if m.DefaultMaxOutputTokens > 0 {
		return m.DefaultMaxOutputTokens
	}
	return 8192
}

// DefaultMaxInputTokens is the global fallback context-window size
// returned when neither the catalog nor the provider's
// DefaultMaxInputTokens supplies one. Chosen conservatively (~32K) so
// callers under-fill rather than overshoot an unknown model's
// upstream window.
const DefaultMaxInputTokens = 32000

// MaxInputTokensFor returns the context-window size for the given
// model ID. Resolution: catalog (ID + aliases) → ProviderMeta
// DefaultMaxInputTokens → package DefaultMaxInputTokens.
func (m ProviderMeta) MaxInputTokensFor(model string) int {
	if e, ok := m.FindModel(model); ok && e.MaxInputTokens > 0 {
		return e.MaxInputTokens
	}
	if m.DefaultMaxInputTokens > 0 {
		return m.DefaultMaxInputTokens
	}
	return DefaultMaxInputTokens
}

// EncodingFor returns the BPE encoding name declared for the given
// model, or empty when none is declared. Empty means the caller
// should not use tiktoken for this model — it has either a native
// counter (Anthropic, Vertex) or must use the approximation counter.
func (m ProviderMeta) EncodingFor(model string) string {
	if e, ok := m.FindModel(model); ok {
		return e.Encoding
	}
	return ""
}

// PricingFor returns the entry's pricing and ok=true when the model
// is in the catalog. Returns the zero TokenPricing and ok=false
// otherwise (use to gate cost-estimate UI: hide rather than show $0).
func (m ProviderMeta) PricingFor(model string) (TokenPricing, bool) {
	if e, ok := m.FindModel(model); ok {
		return e.Pricing, true
	}
	return TokenPricing{}, false
}

// CatalogModels returns the catalog as []ModelInfo for
// /api/v1/providers/llm. One row per canonical ID — aliases stay
// internal to the resolver. Sorted by ID for deterministic output.
func (m ProviderMeta) CatalogModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(m.Models))
	for _, e := range m.Models {
		display := e.DisplayName
		if display == "" {
			display = e.ID
		}
		out = append(out, ModelInfo{
			ID:                    e.ID,
			DisplayName:           display,
			Wire:                  string(e.Wire),
			MaxOutputTokens:       e.MaxOutputTokens,
			MaxInputTokens:        e.MaxInputTokens,
			InputPricePerMillion:  e.Pricing.InputPerMillion,
			OutputPricePerMillion: e.Pricing.OutputPerMillion,
			Lifecycle:             e.Lifecycle,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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
	// ProviderMeta.CatalogModels() when Key=="model"). Users can pick a
	// listed value or type their own. When false and Options is non-empty
	// the UI renders a strict select.
	FreeText bool `json:"free_text,omitempty"`
}

// ConfigOption is one entry in a dropdown-style ConfigField.
type ConfigOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// providerMetaJSON mirrors ProviderMeta with the catalog rendered as
// CatalogModels() output. Used for JSON marshalling so the response
// shape stays "{...meta, models: [...]}", but with one row per
// canonical ID and aliases hidden.
type providerMetaJSON struct {
	ID                     string        `json:"id"`
	Name                   string        `json:"name"`
	Description            string        `json:"description"`
	ConfigFields           []ConfigField `json:"config_fields"`
	Models                 []ModelInfo   `json:"models,omitempty"`
	DefaultMaxOutputTokens int           `json:"default_max_output_tokens,omitempty"`
	DefaultMaxInputTokens  int           `json:"default_max_input_tokens,omitempty"`
	SupportsTools          bool          `json:"supports_tools"`
}

// MarshalJSON renders ProviderMeta with the catalog flattened to
// ModelInfo so the dashboard sees one row per canonical ID. Without
// this hook json.Marshal would omit Models entirely (it's tagged
// json:"-") or — if we exposed it — would emit aliases too, doubling
// the combobox.
func (m ProviderMeta) MarshalJSON() ([]byte, error) {
	return json.Marshal(providerMetaJSON{
		ID:                     m.ID,
		Name:                   m.Name,
		Description:            m.Description,
		ConfigFields:           m.ConfigFields,
		Models:                 m.CatalogModels(),
		DefaultMaxOutputTokens: m.DefaultMaxOutputTokens,
		DefaultMaxInputTokens:  m.DefaultMaxInputTokens,
		SupportsTools:          m.SupportsTools,
	})
}

var (
	providersMu  sync.RWMutex
	providers    = make(map[string]ProviderFactory)
	providerMeta = make(map[string]ProviderMeta)
)

// Register makes a provider available by name.
//
// Provider packages call this in their init() function:
//
//	func init() {
//	    llm.Register("openai", func(cfg llm.ProviderConfig) (llm.Provider, error) {
//	        timeout := llm.ResolveHTTPTimeout(cfg, 5*time.Minute)
//	        return NewOpenAIProvider(cfg["api_key"], cfg["model"], cfg["base_url"], timeout), nil
//	    })
//	}
//
// Services then select the provider via project.llm.provider (or
// LLM_PROVIDER for the agent run mode).
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

// RegisterWithMeta registers a provider with metadata. Validates
// every catalog entry up-front: empty ID, empty/duplicate alias,
// unknown wire, or zero MaxOutputTokens panic so a typo in a seed
// fails noisily during init() rather than at first request time.
func RegisterWithMeta(name string, factory ProviderFactory, meta ProviderMeta) {
	if name == "" {
		panic("llm: RegisterWithMeta with empty name")
	}
	validateMeta(name, meta)
	Register(name, factory)
	providersMu.Lock()
	meta.ID = name
	providerMeta[name] = meta
	providersMu.Unlock()
}

// validateMeta enforces the invariants every ProviderMeta must hold.
// Panics on violation — these are programmer errors caught at init().
func validateMeta(name string, meta ProviderMeta) {
	seenIDs := make(map[string]struct{}, len(meta.Models))
	seenAliases := make(map[string]string, len(meta.Models)) // alias -> owner ID
	for i, e := range meta.Models {
		if e.ID == "" {
			panic(fmt.Sprintf("llm: %s.Models[%d] has empty ID", name, i))
		}
		if e.Wire != WireUnknown && !e.Wire.Valid() {
			panic(fmt.Sprintf("llm: %s.Models[%q] has invalid wire %q", name, e.ID, e.Wire))
		}
		if e.MaxOutputTokens <= 0 {
			panic(fmt.Sprintf("llm: %s.Models[%q] has non-positive MaxOutputTokens %d", name, e.ID, e.MaxOutputTokens))
		}
		if _, dup := seenIDs[e.ID]; dup {
			panic(fmt.Sprintf("llm: %s.Models has duplicate ID %q", name, e.ID))
		}
		seenIDs[e.ID] = struct{}{}
		// An alias must be unique across the whole catalog and must
		// not collide with any canonical ID — both invariants the
		// resolver depends on for a deterministic FindModel.
		if owner, dup := seenAliases[e.ID]; dup {
			panic(fmt.Sprintf("llm: %s.Models — ID %q already registered as alias on %q", name, e.ID, owner))
		}
		for _, a := range e.Aliases {
			if a == "" {
				panic(fmt.Sprintf("llm: %s.Models[%q] has empty alias", name, e.ID))
			}
			if a == e.ID {
				panic(fmt.Sprintf("llm: %s.Models[%q] alias duplicates ID", name, e.ID))
			}
			if _, dup := seenIDs[a]; dup {
				panic(fmt.Sprintf("llm: %s.Models — alias %q on %q collides with another canonical ID", name, a, e.ID))
			}
			if owner, dup := seenAliases[a]; dup {
				panic(fmt.Sprintf("llm: %s.Models — alias %q registered on both %q and %q", name, a, owner, e.ID))
			}
			seenAliases[a] = e.ID
		}
	}
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
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID < metas[j].ID })
	return metas
}

// GetProviderMeta returns metadata for a specific provider.
func GetProviderMeta(name string) (ProviderMeta, bool) {
	providersMu.RLock()
	m, ok := providerMeta[name]
	providersMu.RUnlock()
	return m, ok
}

// GetMaxOutputTokens returns the max output tokens for a (provider,
// model) combination. Resolution:
//  1. ProviderMeta.MaxOutputTokensFor — catalog (ID + aliases) →
//     DefaultMaxOutputTokens.
//  2. Global 8192 fallback (provider not registered, or no
//     DefaultMaxOutputTokens).
func GetMaxOutputTokens(providerName, model string) int {
	providersMu.RLock()
	meta, ok := providerMeta[providerName]
	providersMu.RUnlock()
	if !ok {
		return 8192
	}
	return meta.MaxOutputTokensFor(model)
}

// GetMaxInputTokens returns the context-window size for a (provider,
// model) combination. Resolution:
//  1. ProviderMeta.MaxInputTokensFor — catalog (ID + aliases) →
//     ProviderMeta DefaultMaxInputTokens → package DefaultMaxInputTokens.
//  2. Package DefaultMaxInputTokens fallback (provider not registered).
func GetMaxInputTokens(providerName, model string) int {
	providersMu.RLock()
	meta, ok := providerMeta[providerName]
	providersMu.RUnlock()
	if !ok {
		return DefaultMaxInputTokens
	}
	return meta.MaxInputTokensFor(model)
}

// GetEncoding returns the BPE encoding name declared for a
// (provider, model) combination, or empty when none is declared
// (caller should not use tiktoken — either use the provider's native
// counter or fall back to the approximation counter).
func GetEncoding(providerName, model string) string {
	providersMu.RLock()
	meta, ok := providerMeta[providerName]
	providersMu.RUnlock()
	if !ok {
		return ""
	}
	return meta.EncodingFor(model)
}
