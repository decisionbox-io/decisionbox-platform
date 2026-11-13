package models

import (
	"time"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

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

	// Shape is the source shape this pack was written for. A pack is not
	// shape-neutral: its exploration prompt and analysis areas assume a source
	// organised one way, and teaching a generator from a pack written for the
	// other shape produces something that reads right and asks for queries the
	// source cannot answer.
	//
	// Absent means ShapeEntities, via EffectiveShape. Every pack written before
	// this field existed targets a table-shaped source, and a pack for any other
	// shape cannot be authored without saying so — the same direction the
	// provider descriptor's own default takes, for the same reason.
	Shape warehouse.SourceShape `bson:"shape,omitempty" json:"shape,omitempty"`

	Categories    []PackCategory    `bson:"categories" json:"categories"`
	Prompts       PackPrompts       `bson:"prompts" json:"prompts"`
	AnalysisAreas PackAnalysisAreas `bson:"analysis_areas" json:"analysis_areas"`
	ProfileSchema PackProfileSchema `bson:"profile_schema" json:"profile_schema"`

	// RiskTaxonomy is the business-risk classes this pack's domain has, and
	// how much a finding in each is worth relative to one in an unweighted
	// area. Each analysis area maps to one or more of them (or to the
	// engine's global set), which is where an area's severity weight and its
	// risk flags come from.
	//
	// Absent is legal and is not the same as empty-of-meaning: a pack that
	// declares no taxonomy leaves every area at the engine's default weight
	// and can still map areas to the global cross-cutting ids. Nothing
	// changes for a pack written before this field existed.
	RiskTaxonomy []PackRiskCategory `bson:"risk_taxonomy,omitempty" json:"risk_taxonomy,omitempty"`

	// Per-pack LLM token usage, summed across every generation attempt
	// (initial + auto-fix retries) the synthesiser made for this pack.
	// Populated by the enterprise pack generator when a pack is first
	// persisted; community filesystem-loaded packs leave it absent.
	// omitempty so packs that pre-date this tracking render as absent
	// rather than zero, preserving the "unknown vs. zero spent"
	// distinction.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// EffectiveShape returns the source shape this pack was written for,
// defaulting to ShapeEntities when the pack declares none.
//
// Read it rather than the field: a pack persisted before shape was recorded
// carries no value, and treating that as "no shape" rather than "the shape
// everything used to be" would exclude the entire existing corpus from any
// decision that branches on it.
func (p DomainPack) EffectiveShape() warehouse.SourceShape {
	if p.Shape != "" {
		return p.Shape
	}
	return warehouse.ShapeEntities
}

// PackRiskCategory is one entry in a pack's risk taxonomy.
//
// An alias rather than a struct of its own: the taxonomy a pack declares is
// seeded onto the project, mirrored into the agent's own project model and
// read by the scoring engine, and the engine cannot import any of the three.
// One type means a field added to it reaches every copy.
type PackRiskCategory = gomodels.RiskCategory

// PackCategory is a sub-type within a domain (e.g., "match3" within gaming).
type PackCategory struct {
	ID          string `bson:"id" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
}

// PackPrompts holds all prompt templates organized by base and category.
type PackPrompts struct {
	Base       BasePrompts                `bson:"base" json:"base"`
	Categories map[string]CategoryPrompts `bson:"categories" json:"categories"`
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
	Base       []PackAnalysisArea            `bson:"base" json:"base"`
	Categories map[string][]PackAnalysisArea `bson:"categories" json:"categories"`
}

// PackAnalysisArea defines a single analysis area with its inline prompt.
type PackAnalysisArea struct {
	ID          string   `bson:"id" json:"id"`
	Name        string   `bson:"name" json:"name"`
	Description string   `bson:"description" json:"description"`
	Keywords    []string `bson:"keywords" json:"keywords"`
	Priority    int      `bson:"priority" json:"priority"`
	Prompt      string   `bson:"prompt" json:"prompt"`

	// RiskCategories are the risk classes a finding in this area belongs to:
	// ids from the pack's own RiskTaxonomy, or from the engine's global
	// cross-cutting set. They decide two things -- the area's severity weight
	// (the largest of their weights) and the risk flags every insight in the
	// area carries.
	//
	// Several, not one, because an area is genuinely several things at once:
	// a consent-tracking area is a compliance exposure AND a data-quality
	// one, and a reader filtering for either has to find it.
	RiskCategories []string `bson:"risk_categories,omitempty" json:"risk_categories,omitempty"`

	// SeverityWeight overrides the weight this area's categories would give
	// it. For the area a business weights differently from the rest of its
	// category -- which is a statement about the area, so it is stored on the
	// area rather than forcing a category to be split in two.
	//
	// A POINTER, because the legal range excludes zero: a plain float64 makes
	// a pack that STATES `severity_weight: 0` indistinguishable from one that
	// states nothing, so the rule that rejects it would never see it.
	SeverityWeight *float64 `bson:"severity_weight,omitempty" json:"severity_weight,omitempty"`
}

// PackProfileSchema holds JSON Schema for project profile forms.
type PackProfileSchema struct {
	Base       map[string]interface{}            `bson:"base" json:"base"`
	Categories map[string]map[string]interface{} `bson:"categories" json:"categories"`
}
