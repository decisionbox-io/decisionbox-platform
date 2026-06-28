package models

import (
	"fmt"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// InsightValidation is an alias for the shared validation type in
// libs/go-common/models/validation so every model package references
// one struct on the wire.
type InsightValidation = valmodels.InsightValidation

// StandaloneInsight is a denormalized insight document stored in the "insights" collection.
// Each insight has a UUID _id shared with its Qdrant vector point.
// The source discovery is linked via DiscoveryID.
//
// Shared between API (reads) and Agent (writes during Phase 9).
type StandaloneInsight struct {
	ID           string `bson:"_id" json:"id"`
	ProjectID    string `bson:"project_id" json:"project_id"`
	DiscoveryID  string `bson:"discovery_id" json:"discovery_id"`
	Domain       string `bson:"domain" json:"domain"`
	Category     string `bson:"category" json:"category"`

	AnalysisArea  string                 `bson:"analysis_area" json:"analysis_area"`
	Name          string                 `bson:"name" json:"name"`
	Description   string                 `bson:"description" json:"description"`
	// DescriptionMd is the GitHub-Flavored Markdown rendition of Description,
	// authored by the analysis LLM and rendered formatted in the dashboard.
	// Description stays plain text (the raw reduction) for API consumers,
	// previews, and embeddings; DescriptionMd is omitted when the description
	// carries no formatting and on legacy documents.
	DescriptionMd string                 `bson:"description_md,omitempty" json:"description_md,omitempty"`
	Severity      string                 `bson:"severity" json:"severity"`
	AffectedCount int                    `bson:"affected_count" json:"affected_count"`
	RiskScore     float64                `bson:"risk_score" json:"risk_score"`
	Confidence    float64                `bson:"confidence" json:"confidence"`
	Metrics       map[string]interface{} `bson:"metrics,omitempty" json:"metrics,omitempty"`
	Indicators    []string               `bson:"indicators,omitempty" json:"indicators,omitempty"`
	TargetSegment string                 `bson:"target_segment,omitempty" json:"target_segment,omitempty"`
	SourceSteps   []int                  `bson:"source_steps,omitempty" json:"source_steps,omitempty"`
	SQLMetadata   *InsightSQLMetadata    `bson:"sql_metadata,omitempty" json:"sql_metadata,omitempty"`
	Validation    *InsightValidation     `bson:"validation,omitempty" json:"validation,omitempty"`

	// Embedding fields
	EmbeddingText  string `bson:"embedding_text,omitempty" json:"embedding_text,omitempty"`
	EmbeddingModel string `bson:"embedding_model,omitempty" json:"embedding_model,omitempty"`

	// Deduplication
	DuplicateOf     string  `bson:"duplicate_of,omitempty" json:"duplicate_of,omitempty"`
	SimilarityScore float64 `bson:"similarity_score,omitempty" json:"similarity_score,omitempty"`

	DiscoveredAt time.Time `bson:"discovered_at" json:"discovered_at"`
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
}

// InsightSQLMetadata holds the SQL query used to derive an insight.
type InsightSQLMetadata struct {
	Query    string `bson:"query,omitempty" json:"query,omitempty"`
	RowCount int    `bson:"row_count,omitempty" json:"row_count,omitempty"`
}

// BuildEmbeddingText returns the text to embed for semantic search.
func (i *StandaloneInsight) BuildEmbeddingText() string {
	return fmt.Sprintf("%s. %s. Area: %s. Severity: %s. Segment: %s.",
		i.Name, i.Description, i.AnalysisArea, i.Severity, i.TargetSegment)
}
