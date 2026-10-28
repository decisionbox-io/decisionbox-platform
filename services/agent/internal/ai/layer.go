package ai

import (
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// LayerLLM is one LLM layer's fully resolved target: the provider entry it
// came from, the model, the provider instance, this layer's own ai.Client and
// the token budget resolved for that model.
//
// It lives here rather than in the agent server because the orchestrator,
// ask-serve and the validation agent all consume it and all already import
// this package for ai.Client. The agent server builds the set; everything
// downstream reads it.
type LayerLLM struct {
	// Layer is the layer key this was resolved for (llmlayer.Layer*).
	Layer string
	// ProviderID is the project's provider-entry id (normalised, never "").
	// Part of the calibration identity: two entries pointing at different
	// deployments can name the same model alias.
	ProviderID string
	// Provider is the provider slug.
	Provider string
	// Model is the model id sent on every request.
	Model string
	// Config is the merged provider-entry + layer config the provider was
	// built from. Budget overrides (max_input_tokens / max_output_tokens),
	// wire_override and endpoint_id are read from here.
	Config gollm.ProviderConfig
	// LLM is the provider instance. SHARED between layers that resolve to the
	// same provider entry and merged config — every provider treats its
	// construction-time model as a fallback only, and Client sets
	// ChatRequest.Model on every call.
	LLM gollm.Provider
	// Client is this layer's own ai.Client. Not shared, because it carries the
	// model, the phase tag, provenance, the reasoning flag and mutable
	// step/debug/capture state — and it is a small struct, so a client per
	// layer makes per-layer attribution and per-layer reasoning free.
	Client *Client
	// Window and OutputCap are the token budget resolved for this model.
	// Shared between layers on the same (entry, model, config) target, since
	// the live model-info probe is per model.
	Window    int
	OutputCap int
	// Reasoning is this layer's resolved reasoning setting, already applied to
	// Client via SetReasoning.
	Reasoning bool
}

// LayerSet resolves a layer key to its LLM. For must never return nil: an
// unknown or unresolvable layer falls back to the default, because a run that
// dies on a layer key typo is worse than one that runs on the default model.
type LayerSet interface {
	For(layer string) *LayerLLM
}
