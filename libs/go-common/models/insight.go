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
	ID          string `bson:"_id" json:"id"`
	ProjectID   string `bson:"project_id" json:"project_id"`
	DiscoveryID string `bson:"discovery_id" json:"discovery_id"`
	// Datasource is the warehouse id the insight was discovered from
	// (multi-warehouse). Empty for single-warehouse / legacy insights. Lets the
	// exec summary attribute a finding to the datasource it came from.
	Datasource string `bson:"datasource,omitempty" json:"datasource,omitempty"`
	Domain     string `bson:"domain" json:"domain"`
	Category   string `bson:"category" json:"category"`

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
	// StandsOnAdjudication marks a finding the consolidation adjudicator
	// decided must not be folded: it asked for the survivor's description to
	// be rewritten to carry this finding's figure, segment or caveat, and
	// nothing rewrites a survivor — so folding it would suppress exactly that
	// detail behind text which does not state it.
	//
	// A property of the FINDING rather than of a pair, which is what makes it
	// hold. The same decision expressed as a protected pair had to be
	// consulted at every site that writes a mark, with ids that change as
	// marks are written: review reproduced three ways past it — a fold
	// redirected through a third document, the community fallback choosing a
	// different canonical, and a pause and resume, after which the pairs were
	// gone because they only ever lived in memory. This travels with the
	// insight, through the area draft and the checkpoint, so none of those
	// reach it.
	//
	// The cost is that such a finding is not folded into anything for the rest
	// of the run, where the pair rule would have left it foldable into some
	// other survivor. That is a duplicate left visible, which the end-of-run
	// check can still collapse; the error it replaces is a finding hidden
	// behind text that misstates it.
	StandsOnAdjudication bool `bson:"stands_on_adjudication,omitempty" json:"stands_on_adjudication,omitempty"`
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
	return InsightEmbeddingText(i.Name, i.Description, i.AnalysisArea, i.Severity, i.TargetSegment)
}

// InsightEmbeddingText renders what an insight is, as the duplicate check and
// the semantic index see it.
//
// A free function over the five fields rather than a method, because the
// in-flight insight the agent carries mid-run and the denormalised
// StandaloneInsight persisted at the end of a run are different types holding
// the same five values — and a duplicate check that runs on one of them while
// the index is built from the other has to be comparing the same derivation.
// Two Sprintfs of the same shape in two packages is exactly how the two come
// to disagree about whether two documents are the same document: the same
// discipline magnitudeOf exists for on the quantitative side.
//
// Changing this format changes every stored embedding's meaning, so a change
// here is a re-index, not an edit.
func InsightEmbeddingText(name, description, area, severity, segment string) string {
	return fmt.Sprintf("%s. %s. Area: %s. Severity: %s. Segment: %s.",
		name, description, area, severity, segment)
}
