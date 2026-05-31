package models

import (
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

type Project struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Domain      string `bson:"domain" json:"domain"`
	Category    string `bson:"category" json:"category"`

	Warehouse WarehouseConfig           `bson:"warehouse" json:"warehouse"`
	LLM       LLMConfig                 `bson:"llm" json:"llm"`
	BlurbLLM  *BlurbLLMConfig           `bson:"blurb_llm,omitempty" json:"blurb_llm,omitempty"`
	Embedding goembedding.ProjectConfig `bson:"embedding,omitempty" json:"embedding,omitempty"`
	Schedule  ScheduleConfig            `bson:"schedule" json:"schedule"`

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
	Provider    string            `bson:"provider" json:"provider"`
	ProjectID   string            `bson:"project_id,omitempty" json:"project_id,omitempty"`
	Datasets    []string          `bson:"datasets" json:"datasets"`
	Location    string            `bson:"location,omitempty" json:"location,omitempty"`
	FilterField string            `bson:"filter_field,omitempty" json:"filter_field,omitempty"`
	FilterValue string            `bson:"filter_value,omitempty" json:"filter_value,omitempty"`
	Config      map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: workgroup, database, region, cluster_id, etc.
}

type LLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: project_id, location, host, etc.
}

type ScheduleConfig struct {
	Enabled  bool   `bson:"enabled" json:"enabled"`
	CronExpr string `bson:"cron_expr" json:"cron_expr"`
	MaxSteps int    `bson:"max_steps" json:"max_steps"`
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
