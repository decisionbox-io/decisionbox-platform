package models

import (
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// Project represents a DecisionBox project configuration.
// Stored in MongoDB "projects" collection.
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

	// Prompts — editable by the user. Seeded from domain pack defaults on creation.
	// Agent reads prompts from here (not from the domain pack binary).
	Prompts *ProjectPrompts `bson:"prompts,omitempty" json:"prompts,omitempty"`

	Status        string     `bson:"status" json:"status"`
	LastRunAt     *time.Time `bson:"last_run_at,omitempty" json:"last_run_at,omitempty"`
	LastRunStatus string     `bson:"last_run_status,omitempty" json:"last_run_status,omitempty"`

	SchemaIndexStatus    string     `bson:"schema_index_status,omitempty" json:"schema_index_status,omitempty"`
	SchemaIndexError     string     `bson:"schema_index_error,omitempty" json:"schema_index_error,omitempty"`
	SchemaIndexUpdatedAt *time.Time `bson:"schema_index_updated_at,omitempty" json:"schema_index_updated_at,omitempty"`

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
	IsBase      bool     `bson:"is_base" json:"is_base"`           // true = came from domain pack
	IsCustom    bool     `bson:"is_custom" json:"is_custom"`       // true = user-created
	Priority    int      `bson:"priority" json:"priority"`
	Enabled     bool     `bson:"enabled" json:"enabled"`           // user can disable areas
}

// WarehouseConfig holds data warehouse connection settings.
type WarehouseConfig struct {
	Provider  string `bson:"provider" json:"provider"`
	ProjectID string `bson:"project_id,omitempty" json:"project_id,omitempty"`
	Location  string `bson:"location,omitempty" json:"location,omitempty"`

	Datasets []string `bson:"datasets" json:"datasets"`

	FilterField string            `bson:"filter_field,omitempty" json:"filter_field,omitempty"`
	FilterValue string            `bson:"filter_value,omitempty" json:"filter_value,omitempty"`
	Config      map[string]string `bson:"config,omitempty" json:"config,omitempty"` // provider-specific: workgroup, database, region, cluster_id, etc.
}

func (w *WarehouseConfig) GetDatasets() []string {
	return w.Datasets
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
