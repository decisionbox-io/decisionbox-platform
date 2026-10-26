package llmlayer

import (
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// DefaultLayer is the reserved key of the DEFAULT row in a project's
// layer_models map: the (provider entry, model) pair every layer uses unless
// it has a row of its own.
//
// It is a reserved key rather than a field on the provider entry because an
// entry is a CONNECTION — the same provider types and connection fields the
// project has today — while a model is a per-layer choice. Keeping it here
// means "uses the default" is exactly "no row for this layer", "apply to all"
// is "write the default row and drop the overrides", and the dual-write to
// the legacy project.llm block has one unambiguous source.
//
// It is deliberately NOT a member of Layers(): nothing resolves a model FOR
// the default layer, and a UI iterating the layer set must not render it
// twice. Use ValidateLayerKey to accept it where it is legal.
const DefaultLayer = "default"

// ProviderEntry is one named LLM provider entry on a project.
//
// Config holds CONNECTION fields — region, base_url, auth_method, project_id,
// custom TLS, and so on. Model-scoped settings belong on the LayerModel, which
// is merged over this map.
//
// Defined here, and used directly as the field type by both copies of
// models.Project, so the API and the agent cannot drift on a shape they both
// decode from the same document.
type ProviderEntry struct {
	// ID is the stable identifier of this entry within the project. The
	// reserved ids "default" and "blurb" map to the legacy secret keys; any
	// other id gets a namespaced one. Empty is read as "default".
	ID string `bson:"id,omitempty" json:"id,omitempty"`
	// Label is the human-readable name shown in the UI ("Bedrock — Opus").
	Label string `bson:"label,omitempty" json:"label,omitempty"`
	// Provider is the provider slug ("bedrock", "claude", "litellm", …).
	Provider string `bson:"provider" json:"provider"`
	// Config holds this entry's connection fields.
	Config map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// LayerModel is one layer's stored (provider entry, model) choice.
//
// Config holds MODEL-SCOPED settings and is merged OVER the provider entry's
// Config: max_input_tokens, max_output_tokens, wire_override, endpoint_id,
// reasoning_enabled and prompt_cache. Those six must vary per layer — the
// budgets because the issue requires per-layer budgets, wire_override and
// endpoint_id because they describe a particular model or deployment, and the
// last two because a mixed pipeline makes one project-wide toggle span
// providers where it is inert on some and meaningful on others.
type LayerModel struct {
	// ProviderID names the entry this layer uses. Empty is read as "default".
	ProviderID string `bson:"provider_id,omitempty" json:"provider_id,omitempty"`
	// Model is the model id to send. May be empty only when an endpoint is
	// named, since an endpoint serves its own model.
	Model string `bson:"model,omitempty" json:"model,omitempty"`
	// Config holds this layer's model-scoped settings.
	Config map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// LegacyBlurb is the legacy project.blurb_llm override.
type LegacyBlurb struct {
	Provider string
	Model    string
	Config   map[string]string
}

// Legacy is a project's single-provider configuration — the fields it carried
// before per-layer models, which remain the pre-configuration read path, the
// settings pre-fill, and the dual-write mirror that keeps older agent,
// sources-worker and fileingestion-worker images truthful.
type Legacy struct {
	// Provider, Model and Config are project.llm.
	Provider string
	Model    string
	Config   map[string]string
	// Blurb is project.blurb_llm; nil when the project has no override.
	Blurb *LegacyBlurb
	// Reasoning and PromptCache are the project-level toggles. Nil means
	// "not set here".
	Reasoning   *bool
	PromptCache *bool
}

// ProjectConfig is everything needed to resolve a project's layers. Both
// copies of models.Project build one of these and delegate, so the
// legacy-versus-plural reading rules exist exactly once.
type ProjectConfig struct {
	// Providers is project.llm_providers.
	Providers []ProviderEntry
	// LayerModels is project.layer_models, keyed by layer (plus the reserved
	// DefaultLayer row).
	LayerModels map[string]LayerModel
	// Version is project.llm_config_version.
	Version int
	// Legacy is the single-provider configuration.
	Legacy Legacy
}

// Configured reports whether this project has been configured for per-layer
// models at the current vocabulary. A project that has not been is refused at
// StartRun and by every layer resolver, and the dashboard sends the user to
// settings.
func (c ProjectConfig) Configured() bool {
	return c.Version >= ConfigVersion
}

// EffectiveProviders returns the project's provider entries, synthesising
// them from the legacy fields when llm_providers is empty.
//
// The legacy synthesis is a "default" entry from project.llm plus — when the
// project has a blurb override with a provider — a separate "blurb" entry
// from project.blurb_llm. The blurb entry is separate even when the provider
// slugs match, because its Config REPLACES rather than merges with the
// analysis config today: falling through to project.llm.Config worked only by
// accident when both used the same provider, and a Vertex-analysis /
// Bedrock-blurb mix fed GCP auth_method values into the AWS factory.
//
// Returns nil when the project has no LLM provider configured at all.
func (c ProjectConfig) EffectiveProviders() []ProviderEntry {
	if len(c.Providers) > 0 {
		out := make([]ProviderEntry, 0, len(c.Providers))
		for _, e := range c.Providers {
			e.ID = NormalizeProviderID(e.ID)
			out = append(out, e)
		}
		return out
	}

	var out []ProviderEntry
	if c.Legacy.Provider != "" {
		out = append(out, ProviderEntry{
			ID:       DefaultProviderID,
			Provider: c.Legacy.Provider,
			Config:   c.Legacy.Config,
		})
	}
	if b := c.Legacy.Blurb; b != nil && b.Provider != "" {
		out = append(out, ProviderEntry{
			ID:       BlurbProviderID,
			Provider: b.Provider,
			Config:   b.Config,
		})
	}
	return out
}

// ProviderByID returns the entry with the given id; an empty id resolves to
// the reserved default.
func (c ProjectConfig) ProviderByID(id string) (ProviderEntry, bool) {
	want := NormalizeProviderID(id)
	for _, e := range c.EffectiveProviders() {
		if e.ID == want {
			return e, true
		}
	}
	return ProviderEntry{}, false
}

// DefaultProvider returns the reserved default entry.
//
// Falls back to the first entry when a project has no "default" id — a
// hand-edited or partially written document. Being kind here rather than
// failing matters: the alternative is a run that cannot start for a reason
// the user cannot see.
func (c ProjectConfig) DefaultProvider() ProviderEntry {
	entries := c.EffectiveProviders()
	for _, e := range entries {
		if e.ID == DefaultProviderID {
			return e
		}
	}
	if len(entries) > 0 {
		return entries[0]
	}
	return ProviderEntry{}
}

// EffectiveLayerModel resolves one layer's stored (provider entry, model)
// pair, with PLURAL-OR-LEGACY semantics — never mixed.
//
// When layer_models is non-empty it is AUTHORITATIVE: a layer with a row uses
// it, a layer without one inherits the DefaultLayer row, and the legacy
// blurb_llm mirror is NOT consulted. Without that rule, resetting a
// table-description override would rediscover the stale mirror and silently
// reinstate the model just reset.
//
// When layer_models is empty the legacy fields are read, reproducing all three
// branches of the agent's resolveBlurbLLM for table descriptions:
//
//  1. no blurb provider — ignore the blurb object's model and config
//     entirely and use project.llm;
//  2. a blurb provider with a blank model and NO endpoint — keep the blurb
//     entry but INHERIT project.llm's model (the long-standing convenience);
//  3. an endpoint-backed blurb config — pass its model through verbatim,
//     blank or explicit, because an endpoint serves its own model and must
//     never be sent the analysis one.
func (c ProjectConfig) EffectiveLayerModel(layer string) LayerModel {
	if len(c.LayerModels) > 0 {
		if lm, ok := c.LayerModels[layer]; ok {
			lm.ProviderID = NormalizeProviderID(lm.ProviderID)
			return lm
		}
		if layer != DefaultLayer {
			if lm, ok := c.LayerModels[DefaultLayer]; ok {
				lm.ProviderID = NormalizeProviderID(lm.ProviderID)
				return lm
			}
		}
		// Configured, but with no default row — fall back to the legacy
		// analysis pair rather than resolving to nothing.
		return LayerModel{ProviderID: DefaultProviderID, Model: c.Legacy.Model}
	}

	if layer != LayerTableDescriptions {
		return LayerModel{ProviderID: DefaultProviderID, Model: c.Legacy.Model}
	}

	b := c.Legacy.Blurb
	if b == nil || b.Provider == "" {
		return LayerModel{ProviderID: DefaultProviderID, Model: c.Legacy.Model}
	}
	model := b.Model
	if model == "" && strings.TrimSpace(b.Config[EndpointIDConfigKey]) == "" {
		model = c.Legacy.Model
	}
	return LayerModel{ProviderID: BlurbProviderID, Model: model}
}

// SpecUnchecked resolves a layer to a LayerSpec WITHOUT the configuration
// gate.
//
// Used by the paths that must work on an unconfigured project: the settings
// pre-fill, the per-layer connection test, schema indexing and the support
// bundle. Everything that runs product work uses Spec.
func (c ProjectConfig) SpecUnchecked(layer string) (LayerSpec, error) {
	lm := c.EffectiveLayerModel(layer)
	entry, ok := c.ProviderByID(lm.ProviderID)
	fellBack := ""
	if !ok {
		// A layer naming a deleted entry falls back to the default with a
		// warning rather than failing the run. The API refuses the delete
		// that would cause this, so reaching it means the document was
		// edited outside the handler.
		fellBack = lm.ProviderID
		entry = c.DefaultProvider()
	}
	if entry.Provider == "" {
		return LayerSpec{}, ErrNoProviderConfigured
	}

	cfg := make(map[string]string, len(entry.Config)+len(lm.Config))
	for k, v := range entry.Config {
		cfg[k] = v
	}
	for k, v := range lm.Config {
		cfg[k] = v
	}

	return LayerSpec{
		Layer:                  layer,
		ProviderID:             NormalizeProviderID(entry.ID),
		Provider:               entry.Provider,
		Model:                  lm.Model,
		Config:                 cfg,
		PromptCache:            resolveBoolConfig(cfg, gollm.PromptCacheKey, c.Legacy.PromptCache, true),
		Reasoning:              resolveBoolConfig(cfg, gollm.ReasoningEnabledKey, c.Legacy.Reasoning, false),
		FellBackFromProviderID: fellBack,
	}, nil
}

// Spec resolves a layer to a LayerSpec, refusing an unconfigured project.
//
// Every product entry point — discovery, Ask, pack generation, file
// ingestion, the executive summary, report style, the knowledge-source
// summary — goes through here, so there is ONE check and ONE message.
func (c ProjectConfig) Spec(layer string) (LayerSpec, error) {
	if !c.Configured() {
		return LayerSpec{}, ErrLLMNotConfigured
	}
	return c.SpecUnchecked(layer)
}

// SeedFromLegacy materialises a full layer configuration from the project's
// current single-provider setup: the analysis pair as the default row, the
// resolved table-description pair as its own row when it differs, and both
// the reasoning and prompt-cache toggles written onto the default row.
//
// It is what makes the forced trip through settings a review-and-confirm
// rather than a seventeen-row data-entry exercise. The handler calls it to
// pre-fill the settings page and to build the first configured write.
//
// Returns nil, nil when the project has no provider to seed from.
func (c ProjectConfig) SeedFromLegacy() ([]ProviderEntry, map[string]LayerModel) {
	entries := c.EffectiveProviders()
	if len(entries) == 0 {
		return nil, nil
	}
	// The legacy config is PARTITIONED, not duplicated: its connection
	// fields stay on the entry and its model-scoped fields move to the row.
	// Left whole on the entry, an existing max_output_tokens or wire_override
	// would keep applying while the settings page — which reads the row —
	// showed the control blank, and clearing that control could not remove
	// it.
	for i := range entries {
		if entries[i].ID == DefaultProviderID {
			entries[i].Config = ConnectionConfig(entries[i].Config)
		}
	}

	defaultRow := LayerModel{
		ProviderID: DefaultProviderID,
		Model:      c.Legacy.Model,
		Config:     ModelConfig(c.Legacy.Config),
	}
	if defaultRow.Config == nil {
		defaultRow.Config = map[string]string{}
	}
	// Carry the two project-level toggles onto the default row, which is what
	// moving them per layer means for an existing project: the value the
	// operator chose keeps applying, now as the default every layer inherits.
	defaultRow.Config[gollm.PromptCacheKey] = boolString(c.resolvedPromptCache())
	if c.resolvedReasoning() {
		defaultRow.Config[gollm.ReasoningEnabledKey] = "true"
	}

	layers := map[string]LayerModel{DefaultLayer: defaultRow}

	// Table descriptions keep whatever they resolve to today, including the
	// three-branch blurb semantics, and get their own row only when that
	// differs from the default pair.
	blurb := c.EffectiveLayerModel(LayerTableDescriptions)
	if blurb.ProviderID != defaultRow.ProviderID || blurb.Model != defaultRow.Model {
		row := LayerModel{
			ProviderID: blurb.ProviderID,
			Model:      blurb.Model,
			Config:     ModelConfig(blurb.Config),
		}
		// A blurb override's own connection fields belong to the entry
		// EffectiveProviders synthesised for it, not to the row.
		layers[LayerTableDescriptions] = row
		for i := range entries {
			if entries[i].ID == blurb.ProviderID {
				entries[i].Config = ConnectionConfig(entries[i].Config)
			}
		}
	}

	return entries, layers
}

// LayersUsingProvider returns the layer keys whose resolved row names the
// given provider entry. Drives the in-use guard that refuses to delete an
// entry a layer still points at, naming the layers.
func (c ProjectConfig) LayersUsingProvider(providerID string) []string {
	want := NormalizeProviderID(providerID)
	var out []string
	for key, lm := range c.LayerModels {
		if NormalizeProviderID(lm.ProviderID) == want {
			out = append(out, key)
		}
	}
	return out
}

func (c ProjectConfig) resolvedPromptCache() bool {
	// Matches EffectivePromptCacheEnabled: the project field when set, else
	// the legacy llm.config entry, else on.
	if c.Legacy.PromptCache != nil {
		return *c.Legacy.PromptCache
	}
	return !strings.EqualFold(strings.TrimSpace(c.Legacy.Config[gollm.PromptCacheKey]), "false")
}

func (c ProjectConfig) resolvedReasoning() bool {
	if c.Legacy.Reasoning != nil && *c.Legacy.Reasoning {
		return true
	}
	// The orchestrator ORs the project field with the config entry today.
	return gollm.ReasoningEnabled(c.Legacy.Config)
}

// ValidateLayerKey accepts a layer key, or the reserved DefaultLayer row.
// Anything else is a client error — a typo in a layer key would otherwise be
// stored and silently ignored forever.
func ValidateLayerKey(key string) bool {
	return key == DefaultLayer || IsLayer(key)
}

// resolveBoolConfig resolves a per-layer boolean: the layer's merged config
// when it names the key, else the project-level field, else the default.
//
// Presence matters, so this cannot be gollm.ReasoningEnabled alone — that
// reads absent as false, which would silently switch off a setting the
// project field had turned on.
func resolveBoolConfig(cfg map[string]string, key string, projectField *bool, fallback bool) bool {
	if raw, ok := cfg[key]; ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "on", "yes":
			return true
		case "false", "0", "off", "no":
			return false
		}
		// An unrecognised value is treated as if the key were absent rather
		// than given a third meaning, so a typo falls back to the project
		// field instead of silently flipping a layer.
	}
	if projectField != nil {
		return *projectField
	}
	return fallback
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ModelScopedKeys are the config keys that describe the MODEL rather than the
// connection, so they belong on a layer row rather than on a provider entry.
//
// The split is what lets several layers share one connection: a key left on
// the entry applies to every layer that names it, which is right for a region
// or a base_url and wrong for an output cap.
var ModelScopedKeys = []string{
	"max_input_tokens",
	"max_output_tokens",
	"wire_override",
	gollm.PromptCacheKey,
	gollm.ReasoningEnabledKey,
}

// ModelConfig returns the model-scoped half of a config map, or nil when
// there is none.
func ModelConfig(cfg map[string]string) map[string]string {
	if len(cfg) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, k := range ModelScopedKeys {
		if v, ok := cfg[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ConnectionConfig returns the connection half of a config map: everything
// that is not model-scoped, bar `model` (the row's own field) and
// `credentials_json` (which never belongs in a project document at all).
func ConnectionConfig(cfg map[string]string) map[string]string {
	if len(cfg) == 0 {
		return nil
	}
	modelScoped := make(map[string]bool, len(ModelScopedKeys))
	for _, k := range ModelScopedKeys {
		modelScoped[k] = true
	}
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if modelScoped[k] || k == "model" || k == "credentials_json" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
