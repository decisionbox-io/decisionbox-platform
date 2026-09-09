package models

import (
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

type Project struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Domain      string `bson:"domain" json:"domain"`
	Category    string `bson:"category" json:"category"`

	// Warehouse is the LEGACY single-warehouse field. It is retained for
	// backward compatibility: every community reader still uses it, and it
	// is dual-written to the primary warehouse. New multi-warehouse code
	// reads through EffectiveWarehouses()/PrimaryWarehouse(), which fall
	// back to this field when Warehouses is empty (no data migration
	// needed — legacy projects are synthesised as a single "default"
	// warehouse at read time).
	Warehouse WarehouseConfig `bson:"warehouse" json:"warehouse"`

	// Warehouses is the full set of SQL data sources attached to the
	// project (enterprise multi-warehouse). Empty for legacy /
	// single-warehouse projects, which are handled by the accessors
	// falling back to the Warehouse field.
	Warehouses []WarehouseConfig `bson:"warehouses,omitempty" json:"warehouses,omitempty"`

	// PrimaryWarehouseID names the default warehouse for a turn / the
	// default discovery view / the Ask fallback. Empty resolves to the
	// first entry (or the synthesised "default" for legacy projects).
	PrimaryWarehouseID string `bson:"primary_warehouse_id,omitempty" json:"primary_warehouse_id,omitempty"`

	LLM       LLMConfig                 `bson:"llm" json:"llm"`
	BlurbLLM  *BlurbLLMConfig           `bson:"blurb_llm,omitempty" json:"blurb_llm,omitempty"`
	Embedding goembedding.ProjectConfig `bson:"embedding,omitempty" json:"embedding,omitempty"`

	Profile map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`
	Prompts *ProjectPrompts        `bson:"prompts,omitempty" json:"prompts,omitempty"`

	// Language is the human-readable output language for insights,
	// recommendations, and /ask answers. Substituted into prompts as
	// {{LANGUAGE}}. Empty is treated as "English" by EffectiveLanguage —
	// see the keep-technical-fields-English clause in domain pack
	// base_context.md so SQL, column names, and JSON keys stay portable
	// regardless of the chosen output language.
	Language string `bson:"language,omitempty" json:"language,omitempty"`

	// State tracks the project's lifecycle stage. Empty is equivalent
	// to ProjectStateReady — see EffectiveState. Plugins MAY decode
	// extra state strings via their own model types; the community
	// API treats unknown values as opaque (no behavior gating beyond
	// the EffectiveState mapping).
	State string `bson:"state,omitempty" json:"state,omitempty"`

	// BusinessSummary is an LLM-generated 2–4-paragraph summary of
	// what the customer's business actually does, distilled from the
	// indexed knowledge sources. Refreshed whenever a source goes
	// to status=ready (so the summary stays current as users add or
	// remove documents). Discovery, /ask, and plugin prompt-builders
	// all pull this string in as the primary "what is this project"
	// anchor — it dramatically outperforms passing raw chunks for
	// LLMs that otherwise defer to a noisy ERP-framework schema.
	// Empty until the first source is indexed.
	BusinessSummary          string     `bson:"business_summary,omitempty" json:"business_summary,omitempty"`
	BusinessSummaryUpdatedAt *time.Time `bson:"business_summary_updated_at,omitempty" json:"business_summary_updated_at,omitempty"`
	BusinessSummaryError     string     `bson:"business_summary_error,omitempty" json:"business_summary_error,omitempty"`

	Status string `bson:"status" json:"status"`

	// LastRunStatus, LastRunAt, and LastRunCompletedAt summarise the
	// project's most recent discovery run. They are NOT stored on the
	// project document — they are derived at read time (the List/Get
	// handlers join the latest discovery_runs row, see
	// ProjectsHandler.enrichLastRun) and so carry `bson:"-"`. All three
	// are empty/nil for a project that has never started a run.
	//
	//   - LastRunStatus: the run's lifecycle status — "pending",
	//     "running", "completed", "failed", or "cancelled".
	//   - LastRunAt: when the most recent run started. The dashboard
	//     project cards count "Running — <elapsed>" up from this.
	//   - LastRunCompletedAt: when it finished; nil while it is still
	//     running (or never completed). Drives the "Completed
	//     <timestamp>" label on completed runs.
	LastRunStatus      string     `bson:"-" json:"last_run_status,omitempty"`
	LastRunAt          *time.Time `bson:"-" json:"last_run_at,omitempty"`
	LastRunCompletedAt *time.Time `bson:"-" json:"last_run_completed_at,omitempty"`

	SchemaIndexStatus    string     `bson:"schema_index_status,omitempty" json:"schema_index_status,omitempty"`
	SchemaIndexError     string     `bson:"schema_index_error,omitempty" json:"schema_index_error,omitempty"`
	SchemaIndexUpdatedAt *time.Time `bson:"schema_index_updated_at,omitempty" json:"schema_index_updated_at,omitempty"`

	// ValidationEnabled is the per-project toggle for the LLM-native
	// verifier + refuter pipeline. Nil means "use the deployment
	// default" (true). When false, the discovery orchestrator skips
	// Phase 4.5 / 5.5 and stamps every insight + recommendation with
	// combined: "validation_disabled". The dashboard's manual-run path
	// resolves this same field at click time. The agent reads its own
	// copy of this field via the agent's models.Project — keep the two
	// definitions in sync.
	ValidationEnabled *bool `bson:"validation_enabled,omitempty" json:"validation_enabled,omitempty"`

	// ClarifyingQuestionsEnabled is the per-project toggle for the discovery
	// clarifying-questions loop (the agent asks the analyst about anything it
	// was uncertain about, and the answers feed the next run). Nil means "use
	// the deployment default" (true) — default-on, users opt out in Settings.
	// The agent reads its own copy of this field via the agent's models.Project
	// — keep the two definitions in sync.
	ClarifyingQuestionsEnabled *bool `bson:"clarifying_questions_enabled,omitempty" json:"clarifying_questions_enabled,omitempty"`

	// ReflectionEnabled is the per-project toggle for the end-of-run reflection /
	// Discovery Ledger phase (the agent consolidates each run into a persistent,
	// compounding ledger so the next run builds on it). Nil means "use the
	// deployment default" (true) — default-on, users opt out in Settings. The
	// deployment-availability flag DISCOVERY_REFLECTION_ENABLED (default off) is
	// the other gate. Keep in sync with the agent's models.Project copy.
	ReflectionEnabled *bool `bson:"reflection_enabled,omitempty" json:"reflection_enabled,omitempty"`

	// SmartOverflowEnabled is the per-project toggle for the analysis picker's
	// smart budget-overflow handling (dedup + "also examined" breadcrumb +
	// tighter re-compaction of survivors instead of plainly dropping the
	// lowest-scored steps). Nil means "use the default" (true). It only changes
	// behaviour when picked evidence exceeds the model-window budget, so it is
	// inert on big-window models. The agent reads its own copy of this field via
	// the agent's models.Project — keep the two definitions in sync.
	SmartOverflowEnabled *bool `bson:"smart_overflow_enabled,omitempty" json:"smart_overflow_enabled,omitempty"`

	// ReasoningEnabled is the model-agnostic per-project "Enable reasoning"
	// toggle. Nil / false = off (= today, opt-in). When true the discovery run
	// treats the model as reasoning-effective for every provider (exploration
	// output headroom + ReasoningEffort=on). The agent reads its own copy via
	// the agent's models.Project — keep the two definitions in sync.
	ReasoningEnabled *bool `bson:"reasoning_enabled,omitempty" json:"reasoning_enabled,omitempty"`

	// AskSuggestionsEnabled is the per-project toggle for LLM-generated suggested
	// starter questions on insight / recommendation pages (the "Ask about this"
	// nudge). It makes an automatic LLM call on page entry, so it is user-
	// controllable; nil means "use the default" (true) — default-on, users opt
	// out in Settings. API-only (Q&A time) — the discovery agent does not read it.
	AskSuggestionsEnabled *bool `bson:"ask_suggestions_enabled,omitempty" json:"ask_suggestions_enabled,omitempty"`

	// RecommendationVerdicts is the per-project set of validation verdicts that
	// make an insight eligible for recommendation generation. Empty / unset →
	// default {confirmed, supported} (today's hardcoded IsTerminalPositive
	// filter). Selectable values: confirmed, supported, partial, unverifiable,
	// rejected. The agent reads its own copy via the agent's models.Project —
	// keep the two definitions in sync.
	RecommendationVerdicts []string `bson:"recommendation_verdicts,omitempty" json:"recommendation_verdicts,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

type ProjectPrompts struct {
	Exploration     string                        `bson:"exploration" json:"exploration"`
	Recommendations string                        `bson:"recommendations" json:"recommendations"`
	BaseContext     string                        `bson:"base_context" json:"base_context"`
	AnalysisAreas   map[string]AnalysisAreaConfig `bson:"analysis_areas" json:"analysis_areas"`
}

type AnalysisAreaConfig struct {
	Name        string   `bson:"name" json:"name"`
	Description string   `bson:"description" json:"description"`
	Keywords    []string `bson:"keywords" json:"keywords"`
	Prompt      string   `bson:"prompt" json:"prompt"`
	IsBase      bool     `bson:"is_base" json:"is_base"`
	IsCustom    bool     `bson:"is_custom" json:"is_custom"`
	Priority    int      `bson:"priority" json:"priority"`
	Enabled     bool     `bson:"enabled" json:"enabled"`
}

type WarehouseConfig struct {
	// ID is the stable identifier of this warehouse within the project
	// ("default" for the primary of a migrated/legacy project, or a
	// generated id like "wh_snowflake" for additional warehouses). It
	// keys the per-warehouse secret, schema cache, Qdrant points, and
	// discovery scope, so it must be unique per project and immutable
	// after creation. Empty is treated as "default" by the accessors.
	ID string `bson:"id,omitempty" json:"id,omitempty"`
	// Label is the human-readable name shown in the UI ("Snowflake — Sales").
	Label string `bson:"label,omitempty" json:"label,omitempty"`
	// Description is the warehouse-card headline — the primary routing
	// signal used to pick the right datasource for a question.
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	// Card is the structured routing card (subject areas / key entities /
	// key metrics), authored in the UI and auto-filled by packgen.
	Card *WarehouseCard `bson:"card,omitempty" json:"card,omitempty"`
	// Domain is the per-warehouse domain-pack binding (moved from
	// Project.Domain for multi-warehouse; packgen generates one pack per
	// warehouse). Empty falls back to Project.Domain for the primary.
	Domain string `bson:"domain,omitempty" json:"domain,omitempty"`
	// Category is the per-warehouse domain-pack category id — the pack's
	// first category, picked when this datasource's pack is accepted.
	// Empty falls back to Project.Category for the primary / legacy
	// warehouse. Discovery scoped to this datasource reads it.
	Category string `bson:"category,omitempty" json:"category,omitempty"`
	// Prompts are the per-warehouse discovery prompts, seeded from this
	// datasource's domain pack when its pack is accepted (mirrors how the
	// primary seeds Project.Prompts). Empty falls back to Project.Prompts
	// for the primary / legacy warehouse. Discovery scoped to this
	// datasource reads these instead of the project-level prompts.
	Prompts *ProjectPrompts `bson:"prompts,omitempty" json:"prompts,omitempty"`
	// Profile is the per-warehouse project profile, seeded from this
	// datasource's domain pack when its pack is accepted. Empty falls back
	// to Project.Profile for the primary / legacy warehouse.
	Profile map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`

	Provider    string            `bson:"provider" json:"provider"`
	ProjectID   string            `bson:"project_id,omitempty" json:"project_id,omitempty"`
	Datasets    []string          `bson:"datasets" json:"datasets"`
	Location    string            `bson:"location,omitempty" json:"location,omitempty"`
	FilterField string            `bson:"filter_field,omitempty" json:"filter_field,omitempty"`
	FilterValue string            `bson:"filter_value,omitempty" json:"filter_value,omitempty"`
	Config      map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: workgroup, database, region, cluster_id, etc.
}

// WarehouseCard is the structured "what this warehouse holds" summary
// used as the primary routing signal (design §5.5.2). Authored in the
// Data Sources UI and auto-filled by a packgen summarisation pass.
type WarehouseCard struct {
	SubjectAreas []string `bson:"subject_areas,omitempty" json:"subject_areas,omitempty"`
	KeyEntities  []string `bson:"key_entities,omitempty" json:"key_entities,omitempty"`
	KeyMetrics   []string `bson:"key_metrics,omitempty" json:"key_metrics,omitempty"`
}

// DefaultWarehouseID is the reserved id of the primary warehouse for
// legacy / migrated single-warehouse projects. The per-warehouse secret
// key for this id is the legacy "warehouse-credentials" (no migration),
// see warehouse.CredentialsKey.
const DefaultWarehouseID = "default"

type LLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: project_id, location, host, etc.
}

// Project lifecycle state — the only value community knows about.
// Plugins may decode additional state strings via their own model
// types and may gate behavior on those; the community API treats
// non-"ready"/non-empty values as opaque and refuses discovery
// transitions.
const (
	// ProjectStateReady — normal project, discovery + analysis flows
	// behave the same as for any other project.
	ProjectStateReady = "ready"
)

// EffectiveState returns the state the runtime should treat the project
// as being in. Empty State is mapped to ProjectStateReady so legacy
// projects (created before this feature shipped) continue to work
// without a backfill migration.
func (p *Project) EffectiveState() string {
	if p.State == "" {
		return ProjectStateReady
	}
	return p.State
}

// EffectiveLanguage returns the configured output language for narrative
// fields (insight text, recommendation text, /ask answers). Empty is
// mapped to "English" so legacy projects keep their pre-feature
// behavior without a backfill migration.
func (p *Project) EffectiveLanguage() string {
	if p.Language == "" {
		return "English"
	}
	return p.Language
}

// EffectiveValidationEnabled resolves the per-project validation
// toggle. Nil pointer → true (default-on for legacy projects). The
// matching helper on the agent's models.Project must stay in sync.
func (p *Project) EffectiveValidationEnabled() bool {
	if p.ValidationEnabled == nil {
		return true
	}
	return *p.ValidationEnabled
}

// EffectiveClarifyingQuestionsEnabled resolves the per-project clarifying-
// questions toggle. Nil pointer → true (default-on; users opt out). The
// matching helper on the agent's models.Project must stay in sync.
func (p *Project) EffectiveClarifyingQuestionsEnabled() bool {
	if p.ClarifyingQuestionsEnabled == nil {
		return true
	}
	return *p.ClarifyingQuestionsEnabled
}

// EffectiveReflectionEnabled resolves the per-project reflection / Discovery
// Ledger toggle. Nil pointer → true (default-on; users opt out). The matching
// helper on the agent's models.Project must stay in sync.
func (p *Project) EffectiveReflectionEnabled() bool {
	if p.ReflectionEnabled == nil {
		return true
	}
	return *p.ReflectionEnabled
}

// EffectiveAskSuggestionsEnabled resolves the per-project suggested-questions
// toggle. Nil pointer → true (default-on; users opt out because it makes
// automatic LLM calls).
func (p *Project) EffectiveAskSuggestionsEnabled() bool {
	if p.AskSuggestionsEnabled == nil {
		return true
	}
	return *p.AskSuggestionsEnabled
}

// EffectiveSmartOverflowEnabled resolves the per-project smart-overflow toggle.
// Nil pointer → true (default-on). The matching helper on the agent's
// models.Project must stay in sync.
func (p *Project) EffectiveSmartOverflowEnabled() bool {
	if p.SmartOverflowEnabled == nil {
		return true
	}
	return *p.SmartOverflowEnabled
}

// EffectiveReasoningEnabled resolves the per-project reasoning toggle. Nil → false
// (opt-in; default matches today). The matching helper on the agent's
// models.Project must stay in sync.
func (p *Project) EffectiveReasoningEnabled() bool {
	if p.ReasoningEnabled == nil {
		return false
	}
	return *p.ReasoningEnabled
}

// EffectiveRecommendationVerdicts resolves the per-project set of validation
// verdicts eligible for recommendation generation. Sanitises the stored
// strings and falls back to {confirmed, supported} when unset/empty — today's
// hardcoded IsTerminalPositive filter. The matching helper on the agent's
// models.Project must stay in sync.
func (p *Project) EffectiveRecommendationVerdicts() []valmodels.Status {
	parsed := valmodels.ParseStatuses(p.RecommendationVerdicts)
	if len(parsed) == 0 {
		return valmodels.DefaultRecommendationVerdicts()
	}
	return parsed
}

// EffectiveWarehouses returns the project's warehouses, synthesising a
// single "default" warehouse from the legacy Warehouse field when the
// Warehouses slice is empty. This lets multi-warehouse code treat legacy
// / single-warehouse projects uniformly with no data migration. Returns
// nil when the project has no warehouse configured at all.
func (p *Project) EffectiveWarehouses() []WarehouseConfig {
	if len(p.Warehouses) > 0 {
		return p.Warehouses
	}
	if p.Warehouse.Provider != "" {
		wh := p.Warehouse
		// The legacy singular warehouse IS the default/primary datasource; force
		// its id to the reserved default (never honour a stray incoming
		// warehouse.id). This keeps its credentials on the legacy
		// "warehouse-credentials" key — CredentialsKey(default) == that key — so
		// existing single-warehouse projects keep working; a non-default id would
		// send the agent to a namespaced key the UI/API never wrote.
		wh.ID = DefaultWarehouseID
		return []WarehouseConfig{wh}
	}
	return nil
}

// PrimaryWarehouse returns the project's primary warehouse — the entry
// whose ID matches PrimaryWarehouseID, or the first warehouse when the
// primary id is unset/unknown. Returns a zero WarehouseConfig (Provider
// == "") when the project has no warehouse configured.
func (p *Project) PrimaryWarehouse() WarehouseConfig {
	whs := p.EffectiveWarehouses()
	if len(whs) == 0 {
		return WarehouseConfig{}
	}
	if p.PrimaryWarehouseID != "" {
		for _, w := range whs {
			// Normalize an empty stored id to the reserved default before
			// comparing, matching WarehouseByID — else a project whose primary
			// is an id-less default entry (with PrimaryWarehouseID="default")
			// would miss here and fall back to the first warehouse.
			wid := w.ID
			if wid == "" {
				wid = DefaultWarehouseID
			}
			if wid == p.PrimaryWarehouseID {
				return w
			}
		}
	}
	return whs[0]
}

// WarehouseByID returns the warehouse with the given id. An empty id
// resolves to the primary warehouse. The bool is false when no matching
// warehouse exists (or the project has none configured).
func (p *Project) WarehouseByID(id string) (WarehouseConfig, bool) {
	if id == "" {
		wh := p.PrimaryWarehouse()
		return wh, wh.Provider != ""
	}
	for _, w := range p.EffectiveWarehouses() {
		wid := w.ID
		if wid == "" {
			wid = DefaultWarehouseID
		}
		if wid == id {
			return w, true
		}
	}
	return WarehouseConfig{}, false
}
