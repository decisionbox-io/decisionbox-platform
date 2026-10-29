// Package llmlayer owns the vocabulary of DecisionBox's LLM layers: the
// named places in the product that call a chat model, and the rules for
// configuring one per layer.
//
// A project configures a set of named LLM provider entries and, for each
// layer, which entry and model that layer uses. Everything that needs to
// agree on the layer keys — the API, the agent, the dashboard, the
// enterprise plugins and the managed-AI gateway aliases — reads them from
// here, so a new layer is added in one place.
//
// The package deliberately depends only on the llm registry and the
// secrets resolver (neither of which imports any other go-common
// package), so it can be imported by every surface without a cycle.
package llmlayer

// Layer keys. These strings are persisted on the project document
// (layer_models), appear in the API, and derive the managed-AI gateway
// aliases, so they are stable identifiers — never rename one without a
// configuration version bump (see ConfigVersion).
//
// Where a layer maps one-to-one onto a discovery phase its key is spelled
// identically to the phase constant (models.Phase*), so the LLM usage log's
// phase field and the layer key are the same string. The two validation
// layers deliberately do NOT match PhaseValidation: one phase now spends two
// models, so the usage log keeps phase=validation while the layer keys
// distinguish the verifier from the refuter.
const (
	// LayerExploration — the discovery agent's autonomous exploration loop.
	LayerExploration = "exploration"
	// LayerAnalysis — insight generation from the picked evidence.
	LayerAnalysis = "analysis"
	// LayerValidationVerifier — the verifier half of the validation pipeline.
	LayerValidationVerifier = "validation_verifier"
	// LayerValidationRefuter — the refuter half of the validation pipeline.
	LayerValidationRefuter = "validation_refuter"
	// LayerRecommendations — recommendation generation.
	LayerRecommendations = "recommendations"
	// LayerAsk — the Ask / Q&A answer model (RAG Ask, agentic Ask, ask-serve).
	LayerAsk = "ask"
	// LayerQuestions — the discovery clarifying-questions loop.
	LayerQuestions = "questions"
	// LayerReflection — the end-of-run reflection that writes the ledger.
	LayerReflection = "reflection"
	// LayerSQLRepair — repairing a query the warehouse rejected.
	LayerSQLRepair = "sql_repair"
	// LayerAskRouter — picking which datasource answers an Ask turn.
	LayerAskRouter = "ask_router"
	// LayerAskSuggestions — the suggested starter questions on insight pages.
	LayerAskSuggestions = "ask_suggestions"
	// LayerTableDescriptions — schema-index table descriptions (the legacy
	// blurb_llm slot).
	LayerTableDescriptions = "table_descriptions"
	// LayerSourceSummary — the per-project business summary distilled from
	// knowledge sources.
	LayerSourceSummary = "source_summary"
	// LayerPackGen — domain pack (Playbook) generation.
	LayerPackGen = "pack_gen"
	// LayerFileIngestion — file ingestion's classifier and modeller. One
	// layer, two jobs: they have separate token budgets but share a model.
	LayerFileIngestion = "file_ingestion"
	// LayerExecSummary — executive summary generation.
	LayerExecSummary = "exec_summary"
	// LayerReportStyle — interpreting a report's requested style.
	LayerReportStyle = "report_style"
)

// Layer groups. The dashboard renders main layers expanded and small
// layers collapsed, so the common case is "one good model, one cheap model
// for the small tasks".
const (
	// GroupMain — the layers whose model choice changes output quality.
	GroupMain = "main"
	// GroupSmall — the cheap, high-volume or mechanical layers.
	GroupSmall = "small"
)

// ConfigVersion is the current layer-configuration generation. A project is
// CONFIGURED for per-layer models when its llm_config_version is at least
// this value; anything lower (including a missing field, which decodes as 0)
// means the project has never been configured and is refused until someone
// visits project settings.
//
// A version rather than a boolean, because a boolean cannot express
// "configured, but for an older vocabulary".
//
// The bump rule, so the gate never degenerates into a nag: ADDING A LAYER
// DOES NOT BUMP THE VERSION. A new layer falls back to the default entry,
// which is the entire point of having a default. Only a change that would
// make an existing configuration *wrong* — a renamed key, a changed meaning,
// a new mandatory field — bumps it.
const ConfigVersion = 1

// Descriptor describes one layer for the UI and the API. The dashboard holds
// no copy of this list: it reads GET /api/v1/llm/layers.
type Descriptor struct {
	// Key is the persisted layer key (one of the Layer* constants).
	Key string `json:"key"`
	// Label is the short human-readable name.
	Label string `json:"label"`
	// Description is one sentence on what this layer's model does.
	Description string `json:"description"`
	// Group is GroupMain or GroupSmall.
	Group string `json:"group"`
	// RefusesReasoningModels reports whether this layer has a pre-flight
	// rule against reasoning-class models (see CheckLayerModel). Sent to the
	// dashboard as data so the model picker can warn before a save.
	RefusesReasoningModels bool `json:"refuses_reasoning_models"`
}

// layers is the ordered layer set: main layers first, in pipeline order,
// then the small ones.
var layers = []Descriptor{
	{
		Key:         LayerExploration,
		Label:       "Exploration",
		Description: "The discovery agent's autonomous exploration of the warehouse.",
		Group:       GroupMain,
	},
	{
		Key:         LayerAnalysis,
		Label:       "Analysis",
		Description: "Turning explored evidence into insights.",
		Group:       GroupMain,
	},
	{
		Key:         LayerValidationVerifier,
		Label:       "Validation — verifier",
		Description: "Checking whether an insight is supported by the data.",
		Group:       GroupMain,
	},
	{
		Key:         LayerValidationRefuter,
		Label:       "Validation — refuter",
		Description: "Arguing against an insight to test it.",
		Group:       GroupMain,
	},
	{
		Key:         LayerRecommendations,
		Label:       "Recommendations",
		Description: "Turning validated insights into recommended actions.",
		Group:       GroupMain,
	},
	{
		Key:         LayerAsk,
		Label:       "Ask",
		Description: "Answering questions about the data and the findings.",
		Group:       GroupMain,
	},
	{
		Key:         LayerQuestions,
		Label:       "Clarifying questions",
		Description: "The questions a run asks the analyst about what it was unsure of.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerReflection,
		Label:       "Ledger reflection",
		Description: "Consolidating each run into the compounding Discovery Ledger.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerSQLRepair,
		Label:       "SQL repair",
		Description: "Repairing a query the warehouse rejected.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerAskRouter,
		Label:       "Ask routing",
		Description: "Picking which data source answers an Ask turn.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerAskSuggestions,
		Label:       "Ask suggestions",
		Description: "The suggested starter questions on insight and recommendation pages.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerTableDescriptions,
		Label:       "Table descriptions",
		Description: "The searchable description written for each table during schema indexing.",
		Group:       GroupSmall,
		// Blurb prompts are short and the budget is small, so a model that
		// routes its output through a thinking channel returns nothing.
		RefusesReasoningModels: true,
	},
	{
		Key:         LayerSourceSummary,
		Label:       "Knowledge-source summary",
		Description: "The business summary distilled from the project's knowledge sources.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerPackGen,
		Label:       "Playbook generation",
		Description: "Generating a Playbook (domain pack) from the schema and sources.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerFileIngestion,
		Label:       "File ingestion",
		Description: "Classifying uploaded files and modelling them into tables.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerExecSummary,
		Label:       "Executive summary",
		Description: "The executive summary written at the end of a run.",
		Group:       GroupSmall,
	},
	{
		Key:         LayerReportStyle,
		Label:       "Report style",
		Description: "Interpreting the style a requested report should be written in.",
		Group:       GroupSmall,
	},
}

// Layers returns the ordered layer descriptors. The returned slice is a
// copy, so a caller cannot mutate the package's table.
func Layers() []Descriptor {
	out := make([]Descriptor, len(layers))
	copy(out, layers)
	return out
}

// Keys returns the ordered layer keys.
func Keys() []string {
	out := make([]string, 0, len(layers))
	for _, d := range layers {
		out = append(out, d.Key)
	}
	return out
}

// Get returns the descriptor for a layer key. The bool is false for an
// unknown key, which callers turn into a 400 rather than guessing.
func Get(key string) (Descriptor, bool) {
	for _, d := range layers {
		if d.Key == key {
			return d, true
		}
	}
	return Descriptor{}, false
}

// IsLayer reports whether key names a known layer.
func IsLayer(key string) bool {
	_, ok := Get(key)
	return ok
}
