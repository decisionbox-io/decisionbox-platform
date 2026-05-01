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
	Error        string    `bson:"error,omitempty" json:"error,omitempty"`
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
