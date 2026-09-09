package models

import (
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// Project represents a DecisionBox project configuration.
// Stored in MongoDB "projects" collection.
type Project struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Domain      string `bson:"domain" json:"domain"`
	Category    string `bson:"category" json:"category"`

	// Warehouse is the LEGACY single-warehouse field, dual-written to the
	// primary and read by all existing agent code. Multi-warehouse code
	// reads through EffectiveWarehouses()/PrimaryWarehouse(), which fall
	// back to this field when Warehouses is empty (no data migration
	// needed). Keep in sync with the API model.
	Warehouse WarehouseConfig `bson:"warehouse" json:"warehouse"`

	// Warehouses is the full set of SQL data sources (enterprise
	// multi-warehouse). Empty for legacy / single-warehouse projects.
	Warehouses []WarehouseConfig `bson:"warehouses,omitempty" json:"warehouses,omitempty"`

	// PrimaryWarehouseID names the default warehouse. Empty resolves to
	// the first entry (or the synthesised "default" for legacy projects).
	PrimaryWarehouseID string `bson:"primary_warehouse_id,omitempty" json:"primary_warehouse_id,omitempty"`

	LLM       LLMConfig                 `bson:"llm" json:"llm"`
	BlurbLLM  *BlurbLLMConfig           `bson:"blurb_llm,omitempty" json:"blurb_llm,omitempty"`
	Embedding goembedding.ProjectConfig `bson:"embedding,omitempty" json:"embedding,omitempty"`

	Profile map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`

	// Prompts — editable by the user. Seeded from domain pack defaults on creation.
	// Agent reads prompts from here (not from the domain pack binary).
	Prompts *ProjectPrompts `bson:"prompts,omitempty" json:"prompts,omitempty"`

	// Language is the human-readable output language for narrative
	// fields (insight names/descriptions, recommendation titles, etc).
	// Substituted into prompts as {{LANGUAGE}}. Empty resolves to
	// "English" via EffectiveLanguage so legacy projects keep their
	// pre-feature behavior.
	Language string `bson:"language,omitempty" json:"language,omitempty"`

	// State tracks the project's lifecycle stage. Empty State is treated
	// as ProjectStateReady — see EffectiveState. Plugins may decode
	// additional state strings via their own model types.
	State string `bson:"state,omitempty" json:"state,omitempty"`

	Status        string     `bson:"status" json:"status"`
	LastRunAt     *time.Time `bson:"last_run_at,omitempty" json:"last_run_at,omitempty"`
	LastRunStatus string     `bson:"last_run_status,omitempty" json:"last_run_status,omitempty"`

	SchemaIndexStatus    string     `bson:"schema_index_status,omitempty" json:"schema_index_status,omitempty"`
	SchemaIndexError     string     `bson:"schema_index_error,omitempty" json:"schema_index_error,omitempty"`
	SchemaIndexUpdatedAt *time.Time `bson:"schema_index_updated_at,omitempty" json:"schema_index_updated_at,omitempty"`

	// ValidationEnabled is the per-project toggle for the LLM-native
	// verifier + refuter pipeline (Phase 4.5 / 5.5 of the orchestrator).
	// Nil pointer means "use the deployment default" — see
	// EffectiveValidationEnabled. When false, the orchestrator skips
	// validation and stamps every insight + recommendation with
	// combined: "validation_disabled" (cost saver). The user can still
	// trigger manual validation per item from the dashboard; that path
	// reads the same field via EffectiveValidationEnabled at click time.
	ValidationEnabled *bool `bson:"validation_enabled,omitempty" json:"validation_enabled,omitempty"`

	// ClarifyingQuestionsEnabled is the per-project toggle for the discovery
	// clarifying-questions loop. Nil pointer means "use the deployment default"
	// — see EffectiveClarifyingQuestionsEnabled (default-on; users opt out in
	// Settings). When false, the orchestrator skips question generation. The
	// api's models.Project holds the matching field — keep the two in sync.
	ClarifyingQuestionsEnabled *bool `bson:"clarifying_questions_enabled,omitempty" json:"clarifying_questions_enabled,omitempty"`

	// ReflectionEnabled is the per-project toggle for the end-of-run reflection /
	// Discovery Ledger phase. Nil pointer means "use the deployment default" —
	// see EffectiveReflectionEnabled (default-on; users opt out in Settings). The
	// api's models.Project holds the matching field — keep the two in sync.
	ReflectionEnabled *bool `bson:"reflection_enabled,omitempty" json:"reflection_enabled,omitempty"`

	// SmartOverflowEnabled is the per-project toggle for the analysis picker's
	// smart budget-overflow handling (dedup + "also examined" breadcrumb +
	// tighter re-compaction of survivors, instead of plainly dropping the
	// lowest-scored steps). Nil pointer means "use the default" — see
	// EffectiveSmartOverflowEnabled (default on). It only changes behaviour when
	// picked evidence exceeds the model-window budget, so on big-window models
	// it is inert regardless of the setting.
	SmartOverflowEnabled *bool `bson:"smart_overflow_enabled,omitempty" json:"smart_overflow_enabled,omitempty"`

	// ReasoningEnabled is the model-agnostic per-project "Enable reasoning"
	// toggle. Nil / false means "off" (= today, no reasoning param, no
	// exploration headroom) — reasoning is opt-in, so unlike the validation /
	// smart-overflow toggles this defaults OFF. When true the model is treated
	// as reasoning-effective for every provider: it gets window-budgeted
	// exploration output headroom (so a long hidden chain-of-thought doesn't
	// truncate the action) and the request carries ReasoningEffort=on (which
	// providers that wire native thinking, e.g. Ollama, act on — capability-
	// gated — and others ignore).
	ReasoningEnabled *bool `bson:"reasoning_enabled,omitempty" json:"reasoning_enabled,omitempty"`

	// RecommendationVerdicts is the per-project set of validation verdicts
	// that make an insight eligible for recommendation generation. Empty /
	// unset means "use the default" ({confirmed, supported} — today's
	// hardcoded IsTerminalPositive filter). Selectable values are the five
	// user-facing per-claim verdicts (confirmed, supported, partial,
	// unverifiable, rejected); insights whose validation never ran
	// (validation_disabled / nil) stay eligible fail-open regardless. The
	// agent reads its own copy of this field via the agent's models.Project —
	// keep the API models.Project definition in sync.
	RecommendationVerdicts []string `bson:"recommendation_verdicts,omitempty" json:"recommendation_verdicts,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// ProjectPrompts holds all prompts for a project.
// Seeded from domain pack defaults. Editable by the user.
type ProjectPrompts struct {
	// Exploration is the main autonomous exploration system prompt.
	Exploration string `bson:"exploration" json:"exploration"`

	// Recommendations is the prompt for generating actionable recommendations.
	Recommendations string `bson:"recommendations" json:"recommendations"`

	// BaseContext is shared context prepended to exploration, analysis, and recommendation prompts.
	BaseContext string `bson:"base_context" json:"base_context"`

	// AnalysisAreas maps area ID to its config + prompt.
	// Includes both domain pack defaults and user-added custom areas.
	AnalysisAreas map[string]AnalysisAreaConfig `bson:"analysis_areas" json:"analysis_areas"`
}

// AnalysisAreaConfig holds the configuration for a single analysis area.
// Stored per-project so users can edit prompts and add custom areas.
type AnalysisAreaConfig struct {
	Name        string   `bson:"name" json:"name"`
	Description string   `bson:"description" json:"description"`
	Keywords    []string `bson:"keywords" json:"keywords"`
	Prompt      string   `bson:"prompt" json:"prompt"`
	IsBase      bool     `bson:"is_base" json:"is_base"`     // true = came from domain pack
	IsCustom    bool     `bson:"is_custom" json:"is_custom"` // true = user-created
	Priority    int      `bson:"priority" json:"priority"`
	Enabled     bool     `bson:"enabled" json:"enabled"` // user can disable areas
}

// WarehouseConfig holds data warehouse connection settings.
// Keep in sync with the API model's WarehouseConfig.
type WarehouseConfig struct {
	// ID is the stable, immutable identifier of this warehouse within the
	// project ("default" for a legacy/migrated primary). Keys the
	// per-warehouse secret, schema cache, Qdrant points, and scope.
	ID string `bson:"id,omitempty" json:"id,omitempty"`
	// Label is the human-readable UI name.
	Label string `bson:"label,omitempty" json:"label,omitempty"`
	// Description is the warehouse-card headline (primary routing signal).
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	// Card is the structured routing card (subject areas/entities/metrics).
	Card *WarehouseCard `bson:"card,omitempty" json:"card,omitempty"`
	// Domain is the per-warehouse domain-pack binding.
	Domain string `bson:"domain,omitempty" json:"domain,omitempty"`
	// Category is the per-warehouse domain-pack category id, seeded when
	// this datasource's pack is accepted. Empty falls back to
	// Project.Category. Keep in sync with the API model.
	Category string `bson:"category,omitempty" json:"category,omitempty"`
	// Prompts are the per-warehouse discovery prompts, seeded from this
	// datasource's domain pack at accept. Empty falls back to
	// Project.Prompts. Discovery scoped to this datasource reads these.
	// Keep in sync with the API model.
	Prompts *ProjectPrompts `bson:"prompts,omitempty" json:"prompts,omitempty"`
	// Profile is the per-warehouse project profile, seeded at accept.
	// Empty falls back to Project.Profile. Keep in sync with the API model.
	Profile map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`

	Provider  string `bson:"provider" json:"provider"`
	ProjectID string `bson:"project_id,omitempty" json:"project_id,omitempty"`
	Location  string `bson:"location,omitempty" json:"location,omitempty"`

	Datasets []string `bson:"datasets" json:"datasets"`

	FilterField string            `bson:"filter_field,omitempty" json:"filter_field,omitempty"`
	FilterValue string            `bson:"filter_value,omitempty" json:"filter_value,omitempty"`
	Config      map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: workgroup, database, region, cluster_id, etc.
}

// WarehouseCard mirrors the API model — the structured routing card.
type WarehouseCard struct {
	SubjectAreas []string `bson:"subject_areas,omitempty" json:"subject_areas,omitempty"`
	KeyEntities  []string `bson:"key_entities,omitempty" json:"key_entities,omitempty"`
	KeyMetrics   []string `bson:"key_metrics,omitempty" json:"key_metrics,omitempty"`
}

// DefaultWarehouseID is the reserved id of the primary warehouse for
// legacy / single-warehouse projects (secret under the legacy
// "warehouse-credentials" key — see warehouse.CredentialsKey).
const DefaultWarehouseID = "default"

func (w *WarehouseConfig) GetDatasets() []string {
	return w.Datasets
}

type LLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: project_id, location, host, etc.
}

// Project lifecycle state — the only value the agent knows about.
// Mirrored from the API model so both processes can refer to it by
// name without importing across module boundaries.
const (
	ProjectStateReady = "ready"
)

// EffectiveState returns the state the runtime should treat the project
// as being in. Empty State is mapped to ProjectStateReady so legacy
// projects (created before pack generation existed) continue to work.
func (p *Project) EffectiveState() string {
	if p.State == "" {
		return ProjectStateReady
	}
	return p.State
}

// EffectiveLanguage returns the configured output language for narrative
// fields. Empty is mapped to "English" so legacy projects keep their
// pre-feature behavior without a backfill migration.
func (p *Project) EffectiveLanguage() string {
	if p.Language == "" {
		return "English"
	}
	return p.Language
}

// EffectiveValidationEnabled resolves the per-project validation toggle.
// Returns true when the field is unset (default-on for legacy projects
// that pre-date the toggle, and for any project the user has not
// explicitly opted out of). Returns the stored value when set.
func (p *Project) EffectiveValidationEnabled() bool {
	if p.ValidationEnabled == nil {
		return true
	}
	return *p.ValidationEnabled
}

// EffectiveClarifyingQuestionsEnabled resolves the per-project clarifying-
// questions toggle. Returns true when unset (default-on for legacy projects and
// any project the user has not explicitly opted out of); returns the stored
// value when set. The api's models.Project holds the matching helper.
func (p *Project) EffectiveClarifyingQuestionsEnabled() bool {
	if p.ClarifyingQuestionsEnabled == nil {
		return true
	}
	return *p.ClarifyingQuestionsEnabled
}

// EffectiveReflectionEnabled resolves the per-project reflection / Discovery
// Ledger toggle. Returns true when unset (default-on) and the stored value when
// set. The api's models.Project holds the matching helper.
func (p *Project) EffectiveReflectionEnabled() bool {
	if p.ReflectionEnabled == nil {
		return true
	}
	return *p.ReflectionEnabled
}

// EffectiveSmartOverflowEnabled resolves the per-project smart-overflow toggle.
// Returns true when unset (default-on for legacy projects and any project the
// user has not explicitly opted out of); returns the stored value when set.
func (p *Project) EffectiveSmartOverflowEnabled() bool {
	if p.SmartOverflowEnabled == nil {
		return true
	}
	return *p.SmartOverflowEnabled
}

// EffectiveReasoningEnabled resolves the per-project reasoning toggle. Returns
// false when unset — reasoning is opt-in, so the default matches today's
// behaviour (no reasoning param, no exploration headroom).
func (p *Project) EffectiveReasoningEnabled() bool {
	if p.ReasoningEnabled == nil {
		return false
	}
	return *p.ReasoningEnabled
}

// EffectiveRecommendationVerdicts resolves the per-project set of validation
// verdicts that make an insight eligible for recommendation generation.
// Sanitises the stored strings (case-insensitive, unknowns/dupes dropped)
// and falls back to the default {confirmed, supported} when unset or empty —
// so legacy projects reproduce today's hardcoded IsTerminalPositive filter
// exactly. The matching helper on the API's models.Project must stay in sync.
func (p *Project) EffectiveRecommendationVerdicts() []valmodels.Status {
	parsed := valmodels.ParseStatuses(p.RecommendationVerdicts)
	if len(parsed) == 0 {
		return valmodels.DefaultRecommendationVerdicts()
	}
	return parsed
}

// EffectiveWarehouses returns the project's warehouses, synthesising a
// single "default" warehouse from the legacy Warehouse field when the
// Warehouses slice is empty (no data migration needed). Returns nil when
// no warehouse is configured. Keep in sync with the API model.
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

// PrimaryWarehouse returns the primary warehouse (matching
// PrimaryWarehouseID, else the first). Zero WarehouseConfig when none.
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

// WarehouseByID returns the warehouse with the given id; an empty id
// resolves to the primary. The bool is false when no match exists.
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
