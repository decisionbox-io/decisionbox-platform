package models

import "time"

// DomainPack represents a domain-specific analysis pack stored in MongoDB.
// Domain packs define the prompts, analysis areas, categories, and profile
// schemas used by the AI discovery agent.
type DomainPack struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	Slug        string `bson:"slug" json:"slug"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
	Version     string `bson:"version" json:"version"`
	Author      string `bson:"author,omitempty" json:"author,omitempty"`
	SourceURL   string `bson:"source_url,omitempty" json:"source_url,omitempty"`
	IsPublished bool   `bson:"is_published" json:"is_published"`

	Categories    []PackCategory    `bson:"categories" json:"categories"`
	Prompts       PackPrompts       `bson:"prompts" json:"prompts"`
	AnalysisAreas PackAnalysisAreas `bson:"analysis_areas" json:"analysis_areas"`
	ProfileSchema PackProfileSchema `bson:"profile_schema" json:"profile_schema"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// PackCategory is a sub-type within a domain (e.g., "match3" within gaming).
type PackCategory struct {
	ID          string `bson:"id" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
}

// PackPrompts holds all prompt templates organized by base and category.
type PackPrompts struct {
	Base       BasePrompts                    `bson:"base" json:"base"`
	Categories map[string]CategoryPrompts     `bson:"categories" json:"categories"`
}

// BasePrompts contains the three required prompt templates.
type BasePrompts struct {
	BaseContext     string `bson:"base_context" json:"base_context"`
	Exploration     string `bson:"exploration" json:"exploration"`
	Recommendations string `bson:"recommendations" json:"recommendations"`
}

// CategoryPrompts contains category-specific prompt overrides.
type CategoryPrompts struct {
	ExplorationContext string `bson:"exploration_context,omitempty" json:"exploration_context,omitempty"`
}

// PackAnalysisAreas holds analysis areas organized by base and category.
type PackAnalysisAreas struct {
	Base       []PackAnalysisArea              `bson:"base" json:"base"`
	Categories map[string][]PackAnalysisArea   `bson:"categories" json:"categories"`
}

// PackAnalysisArea defines a single analysis area with its inline prompt.
type PackAnalysisArea struct {
	ID          string   `bson:"id" json:"id"`
	Name        string   `bson:"name" json:"name"`
	Description string   `bson:"description" json:"description"`
	Keywords    []string `bson:"keywords" json:"keywords"`
	Priority    int      `bson:"priority" json:"priority"`
	Prompt      string   `bson:"prompt" json:"prompt"`
}

// PackProfileSchema holds JSON Schema for project profile forms.
type PackProfileSchema struct {
	Base       map[string]interface{}            `bson:"base" json:"base"`
	Categories map[string]map[string]interface{} `bson:"categories" json:"categories"`
}
