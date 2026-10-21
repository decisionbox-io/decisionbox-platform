// Package database — discovery_log_repo.go (API side, read-only).
//
// Read-only counterpart of the agent's DiscoveryLogRepository. Backs the
// dashboard's paginated log endpoints (GET /api/v1/discoveries/{id}/...).
// The agent owns the writers in services/agent/internal/database/
// discovery_log_repo.go; the API just paginates the rows.
package database

import (
	"context"
	"fmt"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryLogRepository surfaces the four split log collections to the
// API/dashboard. The agent is the only writer; this repository only reads.
type DiscoveryLogRepository struct {
	db *DB
}

// NewDiscoveryLogRepository wraps the four split-log collections.
func NewDiscoveryLogRepository(db *DB) *DiscoveryLogRepository {
	return &DiscoveryLogRepository{db: db}
}

// ListExplorationSteps returns the exploration steps for a discovery,
// ordered by step number ascending. limit <= 0 means "all".
func (r *DiscoveryLogRepository) ListExplorationSteps(ctx context.Context, discoveryID string, limit int) ([]models.ExplorationStep, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "step", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(int64(limit))
	}
	cur, err := r.db.Collection(gomongo.CollectionDiscoveryExplorationSteps).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list exploration steps: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.ExplorationStep, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode exploration steps: %w", err)
	}
	return out, nil
}

// ListAnalysisSteps returns the analysis-area steps for a discovery,
// ordered by run_at ascending.
func (r *DiscoveryLogRepository) ListAnalysisSteps(ctx context.Context, discoveryID string) ([]models.AnalysisStep, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "run_at", Value: 1}})
	cur, err := r.db.Collection(gomongo.CollectionDiscoveryAnalysisSteps).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list analysis steps: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.AnalysisStep, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode analysis steps: %w", err)
	}
	return out, nil
}

// ListValidationResults returns validation rows for a discovery, ordered
// by validated_at ascending.
func (r *DiscoveryLogRepository) ListValidationResults(ctx context.Context, discoveryID string) ([]models.ValidationLogEntry, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "validated_at", Value: 1}})
	cur, err := r.db.Collection(gomongo.CollectionDiscoveryValidationResults).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list validation results: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]models.ValidationLogEntry, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode validation results: %w", err)
	}
	return out, nil
}

// RecommendationLogEntry is the read-only view returned by GET
// /api/v1/discoveries/{id}/recommendation-log. Mirrors the relevant subset
// of the agent's RecommendationStep — full LLM dialog (Prompt / Response)
// stays out by default to keep the dashboard payload bounded; the agent's
// full tooling can fall back to the underlying collection directly.
//
// RunAt is time.Time (not string) because the agent persists `run_at` as a
// BSON datetime. Decoding a datetime into a string would fail at read time
// and return 500s on the endpoint; JSON marshalling a time.Time emits an
// RFC3339 string, so the dashboard sees the same wire shape.
type RecommendationLogEntry struct {
	RunAt        time.Time `bson:"run_at" json:"run_at"`
	InsightCount int       `bson:"insight_count" json:"insight_count"`
	TokensIn     int       `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut    int       `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	DurationMs   int64     `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	// Recommendation drop telemetry mirrored from the agent's
	// RecommendationStep so the dashboard surfaces "N dropped due to
	// invalid related_insight_ids" on the recommendation-log endpoint
	// without forcing operators to query MongoDB directly. Zero values
	// are omitted to keep clean runs from inflating the payload.
	// RecommendationsDropped is the total; the per-reason fields break
	// it down (see issue #237 for the slug-style hallucination that
	// motivates RecommendationsDroppedUnknownID).
	RecommendationsDropped           int `bson:"recommendations_dropped,omitempty" json:"recommendations_dropped,omitempty"`
	RecommendationsDroppedMissingIDs int `bson:"recommendations_dropped_missing_ids,omitempty" json:"recommendations_dropped_missing_ids,omitempty"`
	RecommendationsDroppedUnknownID  int `bson:"recommendations_dropped_unknown_id,omitempty" json:"recommendations_dropped_unknown_id,omitempty"`

	// RecommendationsDroppedParse and RecommendationParseRetries mirror the
	// agent's parse-failure telemetry (issue #342): recommendations skipped
	// because their JSON could not be decoded, and how many corrective
	// re-prompts the phase issued. Status carries a machine-readable reason
	// for an empty result (e.g. "recommendation_parse_error",
	// "skipped_no_eligible_insights") so the dashboard renders why the section
	// is empty instead of a generic "none found". Zero/empty values omitted.
	RecommendationsDroppedParse int    `bson:"recommendations_dropped_parse,omitempty" json:"recommendations_dropped_parse,omitempty"`
	RecommendationParseRetries  int    `bson:"recommendation_parse_retries,omitempty" json:"recommendation_parse_retries,omitempty"`
	Status                      string `bson:"status,omitempty" json:"status,omitempty"`

	// Citation telemetry (#347): how many times the phase had to re-ask for
	// related_insight_ids, and how many citations the server recovered or
	// backfilled when it could not. Mirrored for the reason every field here
	// is — and these three were persisted and unmirrored until the field
	// inventory was checked rather than assumed, which is how the first two
	// got missed.
	RecommendationCitationRetries      int `bson:"recommendation_citation_retries,omitempty" json:"recommendation_citation_retries,omitempty"`
	RecommendationsCitationsSalvaged   int `bson:"recommendations_citations_salvaged,omitempty" json:"recommendations_citations_salvaged,omitempty"`
	RecommendationsCitationsBackfilled int `bson:"recommendations_citations_backfilled,omitempty" json:"recommendations_citations_backfilled,omitempty"`

	// Plan/expand telemetry mirrored from the agent's RecommendationStep, for
	// the reason the counters above are: absent from this struct, BSON drops
	// it and the endpoint cannot report it however non-zero it was in the
	// database. These are the fields that say what a short set was -- a short
	// plan, or a plan that lost entries on the way out -- and that is
	// invisible in Error, which stays empty on a run where some expansions
	// failed and others did not.
	RecommendationsPlanned            int                      `bson:"recommendations_planned,omitempty" json:"recommendations_planned,omitempty"`
	RecommendationsExpanded           int                      `bson:"recommendations_expanded,omitempty" json:"recommendations_expanded,omitempty"`
	RecommendationsExpandFailed       int                      `bson:"recommendations_expand_failed,omitempty" json:"recommendations_expand_failed,omitempty"`
	RecommendationsExpandSkipped      int                      `bson:"recommendations_expand_skipped,omitempty" json:"recommendations_expand_skipped,omitempty"`
	RecommendationPlanRetries         int                      `bson:"recommendation_plan_retries,omitempty" json:"recommendation_plan_retries,omitempty"`
	RecommendationsSingleCallFallback bool                     `bson:"recommendations_single_call_fallback,omitempty" json:"recommendations_single_call_fallback,omitempty"`
	Calls                             []RecommendationCallInfo `bson:"calls,omitempty" json:"calls,omitempty"`

	// Figure telemetry from the recommendation phase, mirrored for the reason every
	// other field here is: absent from this struct, BSON drops it and the endpoint
	// cannot report it however non-zero it was in the database. FiguresInlined is
	// the one to watch -- prose that stated a number instead of referencing an
	// insight figure is prose nothing checked.
	FiguresSettled    int `bson:"figures_settled,omitempty" json:"figures_settled,omitempty"`
	FiguresAdopted    int `bson:"figures_adopted,omitempty" json:"figures_adopted,omitempty"`
	FiguresResolved   int `bson:"figures_resolved,omitempty" json:"figures_resolved,omitempty"`
	FiguresUnresolved int `bson:"figures_unresolved,omitempty" json:"figures_unresolved,omitempty"`
	FiguresInlined    int `bson:"figures_inlined,omitempty" json:"figures_inlined,omitempty"`

	Error string `bson:"error,omitempty" json:"error,omitempty"`
}

// RecommendationCallInfo is one LLM call the recommendation phase made, as the
// endpoint reports it. The phase makes a plan call and one call per planned
// recommendation, so Prompt/Response (which this view omits anyway) describe
// one exchange out of up to a dozen and the totals above are sums -- this is
// where a failure can be read back to the call that produced it.
//
// StopReason is the provider's own word; Truncated and Degenerate are what the
// agent concluded from it together with the token count and the text.
// ResponseChars sits next to TokensOut because the ratio between them tells a
// long answer from a repeating run.
type RecommendationCallInfo struct {
	Kind          string `bson:"kind" json:"kind"`
	Index         int    `bson:"index,omitempty" json:"index,omitempty"`
	Attempt       int    `bson:"attempt,omitempty" json:"attempt,omitempty"`
	StopReason    string `bson:"stop_reason,omitempty" json:"stop_reason,omitempty"`
	Truncated     bool   `bson:"truncated,omitempty" json:"truncated,omitempty"`
	Degenerate    bool   `bson:"degenerate,omitempty" json:"degenerate,omitempty"`
	TokensIn      int    `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut     int    `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	DurationMs    int64  `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
	ResponseChars int    `bson:"response_chars,omitempty" json:"response_chars,omitempty"`
	Error         string `bson:"error,omitempty" json:"error,omitempty"`
}

// GetRecommendationLog returns the recommendation-phase row for a
// discovery, or nil if there isn't one.
func (r *DiscoveryLogRepository) GetRecommendationLog(ctx context.Context, discoveryID string) (*RecommendationLogEntry, error) {
	var entry RecommendationLogEntry
	err := r.db.Collection(gomongo.CollectionDiscoveryRecommendationLog).FindOne(ctx, bson.M{"discovery_id": discoveryID}).Decode(&entry)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("get recommendation log: %w", err)
	}
	return &entry, nil
}
