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

	Warehouse WarehouseConfig `bson:"warehouse" json:"warehouse"`
	LLM       LLMConfig       `bson:"llm" json:"llm"`
	BlurbLLM  *BlurbLLMConfig `bson:"blurb_llm,omitempty" json:"blurb_llm,omitempty"`
	Embedding goembedding.ProjectConfig `bson:"embedding,omitempty" json:"embedding,omitempty"`
	Schedule  ScheduleConfig  `bson:"schedule" json:"schedule"`

	Profile map[string]interface{} `bson:"profile,omitempty" json:"profile,omitempty"`
	Prompts *ProjectPrompts        `bson:"prompts,omitempty" json:"prompts,omitempty"`

	// State tracks the project's lifecycle stage. Empty (the legacy
	// default for projects created before pack generation existed) is
	// equivalent to ProjectStateReady — see EffectiveState. New projects
	// get a non-empty value at creation time.
	State string `bson:"state,omitempty" json:"state,omitempty"`

	// GeneratePack carries the user's intent to auto-generate a domain
	// pack for this project. Only meaningful when State is one of the
	// pack_generation_* values; cleared on transition to
	// ProjectStateReady.
	GeneratePack *GeneratePackConfig `bson:"generate_pack,omitempty" json:"generate_pack,omitempty"`

	// PackGenLastError records the most recent generation failure
	// (3-retry-exceeded validator failure or LLM error). Set when the
	// orchestrator reverts state to pack_generation_pending after a
	// failed Generate call; cleared on the next successful Generate.
	// Surfaces in the dashboard wizard so users can adjust feedback
	// and retry without leaving the page.
	PackGenLastError string `bson:"pack_gen_last_error,omitempty" json:"pack_gen_last_error,omitempty"`

	Status        string     `bson:"status" json:"status"`
	LastRunAt     *time.Time `bson:"last_run_at,omitempty" json:"last_run_at,omitempty"`
	LastRunStatus string     `bson:"last_run_status,omitempty" json:"last_run_status,omitempty"`

	SchemaIndexStatus    string     `bson:"schema_index_status,omitempty" json:"schema_index_status,omitempty"`
	SchemaIndexError     string     `bson:"schema_index_error,omitempty" json:"schema_index_error,omitempty"`
	SchemaIndexUpdatedAt *time.Time `bson:"schema_index_updated_at,omitempty" json:"schema_index_updated_at,omitempty"`

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

// Project lifecycle states. The state machine for the pack-generation
// flow is:
//
//	   create with generate_pack.enabled=true
//	    │
//	    ▼
//	pack_generation_pending ── user fills wizard ──▶ pack_generation
//	    │                                              │
//	 cancelled (DELETE)                          generation done
//	                                                   │
//	                                                   ▼
//	                                        pack_generation_done
//	                                                   │
//	                                       user clicks "Start discovery"
//	                                                   │
//	                                                   ▼
//	                                                ready
//
// Existing projects created before this feature have State == "" and are
// treated as ready by EffectiveState. New non-pack-gen projects get
// State == "ready" explicitly so the value is always meaningful for
// future code paths.
const (
	// ProjectStatePackGenerationPending — the wizard has created the
	// project shell; the user is filling in knowledge sources, warehouse
	// config, and providers. Discovery cannot run in this state.
	ProjectStatePackGenerationPending = "pack_generation_pending"

	// ProjectStatePackGeneration — the agent is actively generating the
	// domain pack (--mode=pack-gen). Discovery cannot run in this state.
	ProjectStatePackGeneration = "pack_generation"

	// ProjectStatePackGenerationDone — generation finished; the draft
	// pack is awaiting the user's "Start discovery" gate. Discovery
	// cannot run in this state.
	ProjectStatePackGenerationDone = "pack_generation_done"

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

// GeneratePackConfig holds the user's pack-generation intent for a project.
//
// Carried as a pointer on Project so the field is omitted from documents
// where it doesn't apply rather than serialized as a zero value.
type GeneratePackConfig struct {
	// Enabled is the discriminator. When false (or the field is nil),
	// the project is a regular project and the generator never runs.
	Enabled bool `bson:"enabled" json:"enabled"`

	// PackName is the human-readable name of the pack to be generated
	// ("Acme Gaming"). Required when Enabled is true.
	PackName string `bson:"pack_name" json:"pack_name"`

	// PackSlug is the unique slug of the pack to be generated
	// ("acme-gaming"). Required when Enabled is true. Must match
	// ^[a-z][a-z0-9-]*$ and must not collide with an existing pack.
	PackSlug string `bson:"pack_slug" json:"pack_slug"`

	// Description is an optional one-paragraph user-supplied summary of
	// the customer's domain. Empty string when the user did not supply
	// one.
	Description string `bson:"description,omitempty" json:"description,omitempty"`
}
