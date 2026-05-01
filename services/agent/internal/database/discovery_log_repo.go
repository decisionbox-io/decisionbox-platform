// Package database — discovery_log_repo.go
//
// Per-step / per-area / per-result Mongo collections that used to be embedded
// arrays inside the discoveries document. A 97-step exploration on a wide
// warehouse blew past the 16MB BSON document limit ("an inserted document is
// too large"), killing the discovery save. Splitting each log type into its
// own collection (one row per step / area / validation, keyed by the parent
// discovery's _id) keeps every document bounded and lets the dashboard
// paginate the read path.
//
// Collections (see mongodb.go for the constants):
//   - discovery_exploration_steps   — one doc per ExplorationStep
//   - discovery_analysis_steps      — one doc per AnalysisStep
//   - discovery_validation_results  — one doc per ValidationResult
//   - discovery_recommendation_log  — exactly one doc per discovery (unique index on discovery_id; SaveRecommendationLog inserts once per discovery save)
//
// Indexes (see EnsureIndexes for the source of truth):
//   - discovery_exploration_steps:   (discovery_id, step)        + (project_id, created_at desc)
//   - discovery_analysis_steps:      (discovery_id, run_at)
//   - discovery_validation_results:  (discovery_id, validated_at)
//   - discovery_recommendation_log:  (discovery_id) — unique
//
// The dashboard reads by discovery_id, so every list-side index leads
// with discovery_id. The exploration collection has a second
// (project_id, created_at desc) index for the per-project recent-rows
// view; the other collections don't currently need that view. The
// agent inserts via SaveExplorationSteps / SaveAnalysisSteps /
// SaveValidationResults / SaveRecommendationLog after the parent
// DiscoveryResult is saved — those calls are best-effort: a partial
// log persistence failure logs the error but does not roll back the
// discovery.
package database

import (
	"context"
	"fmt"
	"time"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryLogRepository persists and reads the per-step log collections
// that were split out of the discoveries document. Each collection's row
// carries the parent discovery's _id (DiscoveryID) so a single read on the
// dashboard fans out to the right rows without dragging the parent doc
// across the 16MB limit.
type DiscoveryLogRepository struct {
	db *DB
}

// NewDiscoveryLogRepository creates a new repository wrapping the four
// split collections. Indexes are created lazily via EnsureIndexes (called
// at agent / api startup).
func NewDiscoveryLogRepository(db *DB) *DiscoveryLogRepository {
	return &DiscoveryLogRepository{db: db}
}

// ExplorationStepDoc is the wire/storage shape for one row in
// discovery_exploration_steps. Embeds models.ExplorationStep inline so the
// existing field BSON tags (step, action, query, llm_request, ...) stay
// stable while the parent-discovery linkage and a created_at timestamp ride
// alongside.
type ExplorationStepDoc struct {
	ProjectID   string `bson:"project_id" json:"project_id"`
	DiscoveryID string `bson:"discovery_id" json:"discovery_id"`
	RunID       string `bson:"run_id,omitempty" json:"run_id,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`

	models.ExplorationStep `bson:",inline" json:",inline"`
}

// AnalysisStepDoc — one row in discovery_analysis_steps.
type AnalysisStepDoc struct {
	ProjectID   string    `bson:"project_id" json:"project_id"`
	DiscoveryID string    `bson:"discovery_id" json:"discovery_id"`
	RunID       string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`

	models.AnalysisStep `bson:",inline" json:",inline"`
}

// ValidationResultDoc — one row in discovery_validation_results.
type ValidationResultDoc struct {
	ProjectID   string    `bson:"project_id" json:"project_id"`
	DiscoveryID string    `bson:"discovery_id" json:"discovery_id"`
	RunID       string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`

	models.ValidationResult `bson:",inline" json:",inline"`
}

// RecommendationLogDoc — exactly one row per discovery in
// discovery_recommendation_log. The collection has a unique index on
// discovery_id (enforced in EnsureIndexes), and SaveRecommendationLog
// inserts once when the orchestrator finishes a discovery — mirroring
// the singular RecommendationLog field the parent doc used to embed.
type RecommendationLogDoc struct {
	ProjectID   string    `bson:"project_id" json:"project_id"`
	DiscoveryID string    `bson:"discovery_id" json:"discovery_id"`
	RunID       string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	CreatedAt   time.Time `bson:"created_at" json:"created_at"`

	models.RecommendationStep `bson:",inline" json:",inline"`
}

// SaveExplorationSteps bulk-inserts one row per exploration step. Empty
// input is a no-op. The caller passes the parent discovery's _id (after
// the DiscoveryRepository.Save() returned its hex form) plus the run_id
// for telemetry cross-references.
func (r *DiscoveryLogRepository) SaveExplorationSteps(ctx context.Context, projectID, discoveryID, runID string, steps []models.ExplorationStep) error {
	if len(steps) == 0 {
		return nil
	}
	now := time.Now()
	docs := make([]interface{}, 0, len(steps))
	for _, s := range steps {
		docs = append(docs, ExplorationStepDoc{
			ProjectID:       projectID,
			DiscoveryID:     discoveryID,
			RunID:           runID,
			CreatedAt:       now,
			ExplorationStep: s,
		})
	}
	if _, err := r.db.Collection(CollectionDiscoveryExplorationSteps).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("save exploration steps: %w", err)
	}
	applog.WithFields(applog.Fields{
		"project_id":   projectID,
		"discovery_id": discoveryID,
		"run_id":       runID,
		"count":        len(steps),
	}).Debug("Exploration steps persisted to split collection")
	return nil
}

// SaveAnalysisSteps bulk-inserts one row per analysis area. Empty input
// is a no-op.
func (r *DiscoveryLogRepository) SaveAnalysisSteps(ctx context.Context, projectID, discoveryID, runID string, steps []models.AnalysisStep) error {
	if len(steps) == 0 {
		return nil
	}
	now := time.Now()
	docs := make([]interface{}, 0, len(steps))
	for _, s := range steps {
		docs = append(docs, AnalysisStepDoc{
			ProjectID:    projectID,
			DiscoveryID:  discoveryID,
			RunID:        runID,
			CreatedAt:    now,
			AnalysisStep: s,
		})
	}
	if _, err := r.db.Collection(CollectionDiscoveryAnalysisSteps).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("save analysis steps: %w", err)
	}
	applog.WithFields(applog.Fields{
		"project_id":   projectID,
		"discovery_id": discoveryID,
		"run_id":       runID,
		"count":        len(steps),
	}).Debug("Analysis steps persisted to split collection")
	return nil
}

// SaveValidationResults bulk-inserts one row per validation result. Empty
// input is a no-op.
func (r *DiscoveryLogRepository) SaveValidationResults(ctx context.Context, projectID, discoveryID, runID string, results []models.ValidationResult) error {
	if len(results) == 0 {
		return nil
	}
	now := time.Now()
	docs := make([]interface{}, 0, len(results))
	for _, v := range results {
		docs = append(docs, ValidationResultDoc{
			ProjectID:        projectID,
			DiscoveryID:      discoveryID,
			RunID:            runID,
			CreatedAt:        now,
			ValidationResult: v,
		})
	}
	if _, err := r.db.Collection(CollectionDiscoveryValidationResults).InsertMany(ctx, docs, options.InsertMany().SetOrdered(false)); err != nil {
		return fmt.Errorf("save validation results: %w", err)
	}
	applog.WithFields(applog.Fields{
		"project_id":   projectID,
		"discovery_id": discoveryID,
		"run_id":       runID,
		"count":        len(results),
	}).Debug("Validation results persisted to split collection")
	return nil
}

// SaveRecommendationLog inserts the single recommendation-phase log row
// for this discovery. nil step is a no-op (a discovery may finish without
// a recommendation phase if the analysis produced no insights).
func (r *DiscoveryLogRepository) SaveRecommendationLog(ctx context.Context, projectID, discoveryID, runID string, step *models.RecommendationStep) error {
	if step == nil {
		return nil
	}
	doc := RecommendationLogDoc{
		ProjectID:          projectID,
		DiscoveryID:        discoveryID,
		RunID:              runID,
		CreatedAt:          time.Now(),
		RecommendationStep: *step,
	}
	if _, err := r.db.Collection(CollectionDiscoveryRecommendationLog).InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("save recommendation log: %w", err)
	}
	applog.WithFields(applog.Fields{
		"project_id":   projectID,
		"discovery_id": discoveryID,
		"run_id":       runID,
	}).Debug("Recommendation log persisted to split collection")
	return nil
}

// ListExplorationStepsByDiscovery returns the exploration steps for a
// discovery, ordered by step number ascending. limit <= 0 means "all".
func (r *DiscoveryLogRepository) ListExplorationStepsByDiscovery(ctx context.Context, discoveryID string, limit int) ([]models.ExplorationStep, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "step", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(int64(limit))
	}
	cur, err := r.db.Collection(CollectionDiscoveryExplorationSteps).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list exploration steps: %w", err)
	}
	defer cur.Close(ctx)

	var docs []ExplorationStepDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode exploration steps: %w", err)
	}
	out := make([]models.ExplorationStep, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ExplorationStep)
	}
	return out, nil
}

// ListAnalysisStepsByDiscovery returns the analysis-area steps for a
// discovery, ordered by run_at ascending.
func (r *DiscoveryLogRepository) ListAnalysisStepsByDiscovery(ctx context.Context, discoveryID string) ([]models.AnalysisStep, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "run_at", Value: 1}})
	cur, err := r.db.Collection(CollectionDiscoveryAnalysisSteps).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list analysis steps: %w", err)
	}
	defer cur.Close(ctx)

	var docs []AnalysisStepDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode analysis steps: %w", err)
	}
	out := make([]models.AnalysisStep, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.AnalysisStep)
	}
	return out, nil
}

// ListValidationResultsByDiscovery returns the validation rows for a
// discovery, ordered by validated_at ascending.
func (r *DiscoveryLogRepository) ListValidationResultsByDiscovery(ctx context.Context, discoveryID string) ([]models.ValidationResult, error) {
	filter := bson.M{"discovery_id": discoveryID}
	opts := options.Find().SetSort(bson.D{{Key: "validated_at", Value: 1}})
	cur, err := r.db.Collection(CollectionDiscoveryValidationResults).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list validation results: %w", err)
	}
	defer cur.Close(ctx)

	var docs []ValidationResultDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode validation results: %w", err)
	}
	out := make([]models.ValidationResult, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ValidationResult)
	}
	return out, nil
}

// GetRecommendationLogByDiscovery returns the recommendation-phase log
// row for a discovery, or nil if there isn't one (the analysis phase
// produced no insights and skipped recommendations).
func (r *DiscoveryLogRepository) GetRecommendationLogByDiscovery(ctx context.Context, discoveryID string) (*models.RecommendationStep, error) {
	var doc RecommendationLogDoc
	err := r.db.Collection(CollectionDiscoveryRecommendationLog).FindOne(ctx, bson.M{"discovery_id": discoveryID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("get recommendation log: %w", err)
	}
	out := doc.RecommendationStep
	return &out, nil
}

// EnsureIndexes creates the (discovery_id, ...) indexes on each split
// collection. Called once at agent / api startup. Idempotent — Mongo
// silently no-ops when an index already exists.
func (r *DiscoveryLogRepository) EnsureIndexes(ctx context.Context) error {
	jobs := []struct {
		coll  string
		index mongo.IndexModel
	}{
		{
			coll: CollectionDiscoveryExplorationSteps,
			index: mongo.IndexModel{Keys: bson.D{
				{Key: "discovery_id", Value: 1},
				{Key: "step", Value: 1},
			}},
		},
		{
			coll: CollectionDiscoveryExplorationSteps,
			index: mongo.IndexModel{Keys: bson.D{
				{Key: "project_id", Value: 1},
				{Key: "created_at", Value: -1},
			}},
		},
		{
			coll: CollectionDiscoveryAnalysisSteps,
			index: mongo.IndexModel{Keys: bson.D{
				{Key: "discovery_id", Value: 1},
				{Key: "run_at", Value: 1},
			}},
		},
		{
			coll: CollectionDiscoveryValidationResults,
			index: mongo.IndexModel{Keys: bson.D{
				{Key: "discovery_id", Value: 1},
				{Key: "validated_at", Value: 1},
			}},
		},
		{
			coll: CollectionDiscoveryRecommendationLog,
			index: mongo.IndexModel{Keys: bson.D{{Key: "discovery_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		},
	}
	for _, j := range jobs {
		if _, err := r.db.Collection(j.coll).Indexes().CreateOne(ctx, j.index); err != nil {
			return fmt.Errorf("ensure index on %s: %w", j.coll, err)
		}
	}
	return nil
}
