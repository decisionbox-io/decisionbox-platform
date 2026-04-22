// Package modelcatalog is a central registry of LLM models exposed by cloud
// providers (Bedrock, Vertex AI, Azure AI Foundry) together with the wire
// format each model speaks.
//
// The registry lets a single provider implementation host many models that
// use different wire formats. For example AWS Bedrock serves Claude models
// (Anthropic Messages wire) alongside Qwen / DeepSeek / Mistral / Llama
// models (OpenAI /chat/completions wire); the Bedrock provider dispatches
// on the wire returned by LookupWire rather than pattern-matching model
// names. Adding support for a new model that uses an already-implemented
// wire is then a single Register call — no provider code change.
//
// The catalog is populated at process start via init() calls in catalog.go.
// External callers can Register additional entries (future: MongoDB-backed
// overrides) but must do so before any provider Chat call.
package modelcatalog

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Wire identifies the request/response schema a model expects. Adding a new
// wire requires a coordinated change in the providers that expose models
// using that wire (each provider has a dispatch switch on Wire).
type Wire string

const (
	// Unknown is returned by LookupWire when the (cloud, model) pair is not
	// in the catalog. Provider code must either apply a wire_override from
	// project config or fail fast with an actionable error; it must not
	// silently fall back to a default wire.
	Unknown Wire = ""

	// Anthropic is the Anthropic Messages API wire:
	//   request:  {messages, system, max_tokens, temperature}
	//   response: {content[{type,text}], stop_reason, usage{input_tokens,output_tokens}}
	// Used by Claude directly, Claude-on-Bedrock, Claude-on-Vertex,
	// Claude-on-Azure.
	Anthropic Wire = "anthropic"

	// OpenAICompat is the OpenAI /chat/completions wire:
	//   request:  {model, messages[{role,content}], max_tokens, temperature}
	//   response: {choices[{message,finish_reason}], usage{prompt_tokens,completion_tokens}}
	// Used by OpenAI, GPT-on-Azure, Qwen/DeepSeek/Mistral/Llama-on-Bedrock,
	// Llama/Qwen/DeepSeek/Mistral on Vertex Model Garden MaaS endpoints.
	OpenAICompat Wire = "openai-compat"

	// GoogleNative is the Vertex AI generateContent wire:
	//   request:  {contents[{role,parts[{text}]}], generationConfig}
	//   response: {candidates[{content.parts[{text}],finishReason}], usageMetadata}
	// Used by Gemini models on Vertex AI. Note: Gemini also exposes an
	// OpenAI-compatible surface; a catalog row can pick either wire.
	GoogleNative Wire = "google-native"
)

// Valid reports whether w is a known, non-Unknown wire. Used by config
// parsers to reject typos in wire_override before they reach a provider.
func (w Wire) Valid() bool {
	switch w {
	case Anthropic, OpenAICompat, GoogleNative:
		return true
	}
	return false
}

// ParseWire normalizes a user-provided wire string (case-insensitive, trimmed,
// underscores and spaces accepted in place of dashes) to a Wire. Returns
// Unknown if the input does not match a known wire.
func ParseWire(s string) Wire {
	normalized := strings.ToLower(strings.TrimSpace(s))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	switch normalized {
	case "anthropic":
		return Anthropic
	case "openai-compat", "openai-compatible", "openai":
		return OpenAICompat
	case "google-native", "google", "gemini":
		return GoogleNative
	}
	return Unknown
}

// Entry is one row in the catalog.
type Entry struct {
	// Cloud is the provider ID the model is hosted on — the string a user
	// sets in project.llm.provider. Must match a registered LLM provider
	// name, but the catalog does not validate that (providers are
	// registered separately and may be compiled out).
	Cloud string

	// ID is the cloud-specific model identifier the caller sends in the
	// wire request (e.g. "anthropic.claude-sonnet-4-20250514-v1:0" on
	// Bedrock, "gemini-2.0-flash" on Vertex, "gpt-5" on Azure). Case
	// sensitive because cloud APIs are case sensitive.
	ID string

	// Wire is the request/response schema this model speaks. Required.
	Wire Wire

	// DisplayName is a human-readable label shown in the dashboard. Not
	// required for dispatch; defaults to ID if empty.
	DisplayName string

	// MaxOutputTokens is the ceiling the agent will cap max_tokens to when
	// a caller does not specify one. Zero means "no catalog guidance";
	// callers fall back to the provider's _default.
	MaxOutputTokens int

	// InputPricePerMillion and OutputPricePerMillion are the list prices
	// in USD per 1M tokens, used for cost estimation in the dashboard.
	// Zero values are treated as "unknown" by callers (no estimate shown).
	InputPricePerMillion  float64
	OutputPricePerMillion float64
}

// Key returns the composite lookup key for an entry.
func (e Entry) Key() string {
	return e.Cloud + "/" + e.ID
}

var (
	mu         sync.RWMutex
	entries    = make(map[string]Entry)
	inferrers  = make(map[string]WireInferrer)
)

// WireInferrer is a cloud-specific function that maps a model ID to a
// wire based on naming conventions (typically a prefix table). Used for
// models that are not explicitly registered in the catalog but follow a
// known family pattern — for example a newly-released Claude variant on
// Bedrock that still starts with "anthropic." speaks the Anthropic wire
// even if we haven't seen it before.
//
// Return Unknown when the ID does not match any known family; callers
// fall through to the user-supplied wire_override or return an
// actionable error.
type WireInferrer func(model string) Wire

// SetWireInferrer registers a wire-inference function for a cloud.
// Providers call this from their init() after RegisterWithMeta so the
// catalog package is the single entry point for dispatch resolution.
// Panics on empty cloud or nil fn to catch programmer errors at startup
// rather than at request time. Replacing an existing inferrer is
// allowed — the last registration wins.
func SetWireInferrer(cloud string, fn WireInferrer) {
	if cloud == "" {
		panic("modelcatalog: SetWireInferrer with empty cloud")
	}
	if fn == nil {
		panic("modelcatalog: SetWireInferrer with nil fn")
	}
	mu.Lock()
	defer mu.Unlock()
	inferrers[cloud] = fn
}

// InferWire returns the wire a (cloud, model) pair speaks based on the
// registered prefix table, or Unknown if the cloud has no inferrer or
// the model does not match a known family.
func InferWire(cloud, model string) Wire {
	mu.RLock()
	fn, ok := inferrers[cloud]
	mu.RUnlock()
	if !ok {
		return Unknown
	}
	return fn(model)
}

// Register adds or replaces a catalog entry. Panics on invalid input
// (empty Cloud/ID, or an unknown Wire) because seed registrations happen
// at init() time — a typo must fail noisily in tests, not at request
// time on a production agent. Thread-safe.
func Register(e Entry) {
	if e.Cloud == "" {
		panic("modelcatalog: Register with empty Cloud")
	}
	if e.ID == "" {
		panic("modelcatalog: Register with empty ID")
	}
	if !e.Wire.Valid() {
		panic(fmt.Sprintf("modelcatalog: Register(%s/%s) with invalid wire %q", e.Cloud, e.ID, e.Wire))
	}
	if e.DisplayName == "" {
		e.DisplayName = e.ID
	}
	mu.Lock()
	defer mu.Unlock()
	entries[e.Key()] = e
}

// Lookup returns the catalog entry for (cloud, id), or ok=false if not
// registered. Callers should not mutate the returned Entry.
func Lookup(cloud, id string) (Entry, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := entries[cloud+"/"+id]
	return e, ok
}

// LookupWire is a convenience wrapper over Lookup that returns only the
// wire. Returns Unknown if the entry is not in the catalog — callers must
// not treat Unknown as a default; it is specifically actionable via
// wire_override.
func LookupWire(cloud, id string) Wire {
	if e, ok := Lookup(cloud, id); ok {
		return e.Wire
	}
	return Unknown
}

// ListByCloud returns every catalog entry for the given cloud, sorted by ID
// for deterministic output (the dashboard renders this list directly).
func ListByCloud(cloud string) []Entry {
	mu.RLock()
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Cloud == cloud {
			out = append(out, e)
		}
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Clouds returns every cloud that has at least one catalog entry, sorted.
func Clouds() []string {
	mu.RLock()
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		seen[e.Cloud] = struct{}{}
	}
	mu.RUnlock()
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// All returns every registered entry, sorted by (Cloud, ID). Primarily for
// tests and for the /providers endpoint's catalog dump.
func All() []Entry {
	mu.RLock()
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cloud != out[j].Cloud {
			return out[i].Cloud < out[j].Cloud
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ResolveWire is the shared dispatch helper used by every provider's
// Chat() method.
//
// Resolution order:
//  1. Catalog entry for (cloud, model) — authoritative.
//  2. wireOverride from project config — user explicit, trumps inference.
//  3. Cloud-specific wire inferrer (prefix tables registered via
//     SetWireInferrer) — lets new models in a known family dispatch
//     without any per-model config. For example an unseen
//     "anthropic.claude-6-foo-v1:0" on Bedrock still matches the
//     "anthropic." prefix → Anthropic wire.
//  4. Unknown + actionable error naming the cloud, model, and valid
//     wire_override values.
//
// Keeping the error format in one place is what makes the
// wire_override hint discoverable.
func ResolveWire(cloud, model string, wireOverride Wire) (Wire, error) {
	if w := LookupWire(cloud, model); w != Unknown {
		return w, nil
	}
	if wireOverride != Unknown {
		return wireOverride, nil
	}
	if w := InferWire(cloud, model); w != Unknown {
		return w, nil
	}
	return Unknown, fmt.Errorf(
		"%s: model %q not in catalog and no llm.wire_override set; "+
			"set wire_override in project config to one of: %s, %s, %s",
		cloud, model, Anthropic, OpenAICompat, GoogleNative,
	)
}
