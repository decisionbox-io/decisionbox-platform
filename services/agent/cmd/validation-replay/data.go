package main

import (
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// ProjectDoc carries the bare minimum of the project model the CLI
// needs to construct the LLM + warehouse providers. Lives here so the
// MVP doesn't drag in the full agent project loader.
type ProjectDoc struct {
	Name      string       `bson:"name"`
	Domain    string       `bson:"domain"`
	Category  string       `bson:"category"`
	Language  string       `bson:"language"`
	Warehouse WarehouseCfg `bson:"warehouse"`
	LLM       LLMCfg       `bson:"llm"`
	// BlurbLLM is decoded because llmlayer reads it when resolving the
	// table-description layer of a project that predates per-layer models.
	BlurbLLM *LLMCfg `bson:"blurb_llm"`
	// The per-layer configuration (#407). Decoded here — not just llm —
	// because a project whose validation layers point at another provider
	// entry would otherwise be REPLAYED ON THE WRONG MODEL, which makes the
	// replay worthless for the thing it exists to answer.
	LLMProviders     []llmlayer.ProviderEntry       `bson:"llm_providers"`
	LayerModels      map[string]llmlayer.LayerModel `bson:"layer_models"`
	LLMConfigVersion int                            `bson:"llm_config_version"`
	// PromptCacheEnabled and ReasoningEnabled must be decoded, or replay
	// would resolve them from the legacy config alone and diverge from the
	// run it is replaying — in both directions, since each field can override
	// a legacy value either way.
	PromptCacheEnabled *bool `bson:"prompt_cache_enabled"`
	ReasoningEnabled   *bool `bson:"reasoning_enabled"`
}

// llmLayerConfig packages the decoded document for llmlayer, which owns the
// legacy-versus-plural reading rules. The replay resolves its layers through
// exactly the same code the agent does, so a replay cannot diverge from the
// run by resolving a model differently.
func (p *ProjectDoc) llmLayerConfig() llmlayer.ProjectConfig {
	if p == nil {
		return llmlayer.ProjectConfig{}
	}
	cfg := llmlayer.ProjectConfig{
		Providers:   p.LLMProviders,
		LayerModels: p.LayerModels,
		Version:     p.LLMConfigVersion,
		Legacy: llmlayer.Legacy{
			Provider:    p.LLM.Provider,
			Model:       p.LLM.Model,
			Config:      p.LLM.Config,
			Reasoning:   p.ReasoningEnabled,
			PromptCache: p.PromptCacheEnabled,
		},
	}
	if p.BlurbLLM != nil {
		cfg.Legacy.Blurb = &llmlayer.LegacyBlurb{
			Provider: p.BlurbLLM.Provider,
			Model:    p.BlurbLLM.Model,
			Config:   p.BlurbLLM.Config,
		}
	}
	return cfg
}

type WarehouseCfg struct {
	Provider    string            `bson:"provider"`
	ProjectID   string            `bson:"project_id"`
	Datasets    []string          `bson:"datasets"`
	Location    string            `bson:"location"`
	FilterField string            `bson:"filter_field"`
	FilterValue string            `bson:"filter_value"`
	Config      map[string]string `bson:"config"`
}

type LLMCfg struct {
	Provider string            `bson:"provider"`
	Model    string            `bson:"model"`
	Config   map[string]string `bson:"config"`
}

// DiscoveryDoc decodes the persisted discoveries collection shape.
// Insight + Recommendation use the production agentmodels types so
// the verifier package sees identical fields whether the bundle came
// from a live orchestrator run or from the CLI's Mongo replay.
type DiscoveryDoc struct {
	ID              string                       `bson:"_id"`
	ProjectID       string                       `bson:"project_id"`
	Insights        []agentmodels.Insight        `bson:"insights"`
	Recommendations []agentmodels.Recommendation `bson:"recommendations"`
}
