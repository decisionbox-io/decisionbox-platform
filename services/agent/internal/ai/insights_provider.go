package ai

import "context"

// InsightsProvider backs the search_insights tool: a semantic search over the
// project's discovered insights and recommendations. It mirrors SchemaProvider
// — the ask-serve loop holds at most one per project, and a nil provider simply
// means the tool is not offered (the project has no embedder or no indexed
// insights). Implementations rank semantically against the project's insight
// embedding index.
type InsightsProvider interface {
	// SearchInsights returns the top-k insights/recommendations most relevant to
	// the natural-language query, ranked by semantic similarity.
	SearchInsights(ctx context.Context, query string, k int) ([]InsightHit, error)
}

// InsightHit is one insight or recommendation surfaced by SearchInsights. It is
// the agent-facing projection of a StandaloneInsight / StandaloneRecommendation
// — enough for the model to ground an answer and for the dashboard to render a
// citation.
type InsightHit struct {
	ID            string
	Type          string // "insight" or "recommendation"
	Name          string
	Description   string
	Severity      string
	AnalysisArea  string
	AffectedCount int
	Score         float64
	// DiscoveryID links the hit back to the discovery run that produced it, so a
	// persisted citation can resolve the source run on history reload.
	DiscoveryID string
}
