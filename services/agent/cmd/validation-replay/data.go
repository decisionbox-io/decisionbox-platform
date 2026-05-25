package main

import agentmodels "github.com/decisionbox-io/decisionbox/services/agent/internal/models"

// ProjectDoc carries the bare minimum of the project model the CLI
// needs to construct the LLM + warehouse providers. Lives here so the
// MVP doesn't drag in the full agent project loader.
type ProjectDoc struct {
	Name      string         `bson:"name"`
	Domain    string         `bson:"domain"`
	Category  string         `bson:"category"`
	Language  string         `bson:"language"`
	Warehouse WarehouseCfg   `bson:"warehouse"`
	LLM       LLMCfg         `bson:"llm"`
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
