package models

import (
	"fmt"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// StandaloneRecommendation is a denormalized recommendation document stored in the "recommendations" collection.
// Each recommendation has a UUID _id shared with its Qdrant vector point.
// The source discovery is linked via DiscoveryID.
//
// Shared between API (reads) and Agent (writes during Phase 9).
type StandaloneRecommendation struct {
	ID          string `bson:"_id" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	DiscoveryID string `bson:"discovery_id" json:"discovery_id"`
	Domain      string `bson:"domain" json:"domain"`
	Category    string `bson:"category" json:"category"`

	RecommendationCategory string          `bson:"recommendation_category" json:"recommendation_category"`
	Title                  string          `bson:"title" json:"title"`
	Description            string          `bson:"description" json:"description"`
	// DescriptionMd is the GitHub-Flavored Markdown rendition of Description.
	// Description stays plain text; DescriptionMd is omitted when the
	// description carries no formatting and on legacy documents.
	DescriptionMd          string          `bson:"description_md,omitempty" json:"description_md,omitempty"`
	Priority               int             `bson:"priority" json:"priority"`
	TargetSegment          string          `bson:"target_segment" json:"target_segment"`
	SegmentSize            int             `bson:"segment_size" json:"segment_size"`
	ExpectedImpact         ExpectedImpact  `bson:"expected_impact" json:"expected_impact"`
	Actions                []string        `bson:"actions" json:"actions"`
	RelatedInsightIDs      []string        `bson:"related_insight_ids,omitempty" json:"related_insight_ids,omitempty"`
	Confidence             float64         `bson:"confidence" json:"confidence"`

	// Validation is the verifier+refuter verdict attached after
	// Phase 5.5. Nil on legacy docs and on docs written before this
	// plan landed; populated by the orchestrator for every
	// recommendation that fits within the per-run validation cap.
	Validation *valmodels.InsightValidation `bson:"validation,omitempty" json:"validation,omitempty"`

	// Embedding fields
	EmbeddingText  string `bson:"embedding_text,omitempty" json:"embedding_text,omitempty"`
	EmbeddingModel string `bson:"embedding_model,omitempty" json:"embedding_model,omitempty"`

	// Deduplication
	DuplicateOf     string  `bson:"duplicate_of,omitempty" json:"duplicate_of,omitempty"`
	SimilarityScore float64 `bson:"similarity_score,omitempty" json:"similarity_score,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// ExpectedImpact describes the expected outcome of a recommendation.
type ExpectedImpact struct {
	Metric               string `bson:"metric" json:"metric"`
	EstimatedImprovement string `bson:"estimated_improvement" json:"estimated_improvement"`
	Direction            string `bson:"direction,omitempty" json:"direction,omitempty"`
	Reasoning            string `bson:"reasoning,omitempty" json:"reasoning,omitempty"`
}

// BuildEmbeddingText returns the text to embed for semantic search.
func (r *StandaloneRecommendation) BuildEmbeddingText() string {
	return fmt.Sprintf("%s. %s. Impact: %s %s. Segment: %s.",
		r.Title, r.Description, r.ExpectedImpact.Metric,
		r.ExpectedImpact.EstimatedImprovement, r.TargetSegment)
}
