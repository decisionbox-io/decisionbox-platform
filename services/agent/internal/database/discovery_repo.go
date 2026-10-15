package database

import (
	"context"
	"fmt"
	"time"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryRepository manages DiscoveryResult persistence.
type DiscoveryRepository struct {
	collection *mongo.Collection
}

// NewDiscoveryRepository creates a new discovery repository.
func NewDiscoveryRepository(client *DB) *DiscoveryRepository {
	return &DiscoveryRepository{
		collection: client.Collection(CollectionDiscoveries),
	}
}

// Save inserts a discovery result.
func (r *DiscoveryRepository) Save(ctx context.Context, result *models.DiscoveryResult) error {
	result.CreatedAt = time.Now()
	result.UpdatedAt = time.Now()

	applog.WithFields(applog.Fields{
		"project_id": result.ProjectID,
		"insights":   len(result.Insights),
		"steps":      result.TotalSteps,
	}).Debug("Saving discovery result to MongoDB")

	res, err := r.collection.InsertOne(ctx, result)
	if err != nil {
		applog.WithError(err).Error("Failed to save discovery result")
		return fmt.Errorf("failed to save discovery result: %w", err)
	}

	// Populate the ID so downstream consumers (Phase 9) can reference this discovery.
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		result.ID = oid.Hex()
	}

	applog.WithField("project_id", result.ProjectID).Info("Discovery result saved")
	return nil
}

// GetByID retrieves a discovery by its ObjectID hex string. Used by
// the validate-doc agent mode to load the target discovery + its
// embedded insights / recommendations.
func (r *DiscoveryRepository) GetByID(ctx context.Context, idHex string) (*models.DiscoveryResult, error) {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, fmt.Errorf("invalid discovery id %q: %w", idHex, err)
	}
	var result models.DiscoveryResult
	if err := r.collection.FindOne(ctx, bson.M{"_id": oid}).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("get discovery by id: %w", err)
	}
	return &result, nil
}

// UpdateInsightValidation writes the new Validation pointer onto the
// matching embedded insight via positional array-filter, leaving the
// other insights' validation untouched. The element filter scopes the
// write so a concurrent discovery save doesn't clobber sibling docs.
//
// The main filter includes `insights.id` so a missing or wrong
// insightID surfaces as MatchedCount=0 — without that predicate the
// UpdateOne still matches the discovery doc, updates `updated_at`,
// touches no array element, and returns nil (silent no-op on bad
// IDs).
func (r *DiscoveryRepository) UpdateInsightValidation(ctx context.Context, discoveryIDHex, insightID string, validation interface{}) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": insightID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid, "insights.id": insightID},
		bson.M{"$set": bson.M{"insights.$[el].validation": validation, "updated_at": time.Now()}},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update insight validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s or insight %s not found", discoveryIDHex, insightID)
	}
	return nil
}

// UpdateRecommendationValidation is the recommendation twin of
// UpdateInsightValidation. Same positional array-filter contract +
// the same `recommendations.id` predicate on the main filter so a
// missing recID returns an error instead of silently no-op'ing the
// array filter while updating `updated_at`.
func (r *DiscoveryRepository) UpdateRecommendationValidation(ctx context.Context, discoveryIDHex, recID string, validation interface{}) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": recID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid, "recommendations.id": recID},
		bson.M{"$set": bson.M{"recommendations.$[el].validation": validation, "updated_at": time.Now()}},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update recommendation validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s or recommendation %s not found", discoveryIDHex, recID)
	}
	return nil
}

// GetLatest retrieves the most recent discovery for a project.
func (r *DiscoveryRepository) GetLatest(ctx context.Context, projectID string) (*models.DiscoveryResult, error) {
	filter := bson.M{"project_id": projectID}
	opts := options.FindOne().SetSort(bson.D{{Key: "discovery_date", Value: -1}})

	var result models.DiscoveryResult
	err := r.collection.FindOne(ctx, filter, opts).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest discovery: %w", err)
	}
	return &result, nil
}

// ListRecent returns the last N discoveries for a project (lightweight — only summary fields).
func (r *DiscoveryRepository) ListRecent(ctx context.Context, projectID string, limit int) ([]*models.DiscoveryResult, error) {
	if limit <= 0 {
		limit = 5
	}
	applog.WithFields(applog.Fields{
		"project_id": projectID,
		"limit":      limit,
	}).Debug("Fetching recent discoveries for context")
	filter := bson.M{"project_id": projectID}
	opts := options.Find().
		SetSort(bson.D{{Key: "discovery_date", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"project_id":      1,
			"discovery_date":  1,
			"run_type":        1,
			"areas_requested": 1,
			"insights":        1,
			"recommendations": 1,
			"summary":         1,
			// Projected because the caller filters this list by it: a
			// resumed run must not be told not to re-tread its OWN partial
			// result. Omitting it here would silently make that filter
			// match nothing.
			"run_id": 1,
		})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list recent discoveries: %w", err)
	}
	defer cursor.Close(ctx)

	results := make([]*models.DiscoveryResult, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode recent discoveries: %w", err)
	}
	return results, nil
}

// ListIDsByRun returns the _ids of every discovery produced by one run,
// newest first.
//
// A run produces exactly one result on the happy path. It produces more only
// when it was resumed after a previous attempt had already saved a partial
// result — which is precisely the case the caller needs to find, so it can
// retire the superseded documents once the new attempt has landed.
//
// Documents written before run_id existed carry none, so they never match —
// the right answer for a historical result.
func (r *DiscoveryRepository) ListIDsByRun(ctx context.Context, runID string) ([]string, error) {
	if runID == "" {
		return nil, nil
	}
	cursor, err := r.collection.Find(ctx,
		bson.M{"run_id": runID},
		options.Find().
			SetProjection(bson.M{"_id": 1}).
			SetSort(bson.D{{Key: "discovery_date", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list discoveries for run %s: %w", runID, err)
	}
	defer cursor.Close(ctx) //nolint:errcheck

	ids := make([]string, 0, 2)
	for cursor.Next(ctx) {
		var doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode discovery id for run %s: %w", runID, err)
		}
		ids = append(ids, doc.ID.Hex())
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor discoveries for run %s: %w", runID, err)
	}
	return ids, nil
}

// DeleteByID removes one discovery document. Used to retire the partial
// result a superseded attempt of a resumed run left behind.
func (r *DiscoveryRepository) DeleteByID(ctx context.Context, idHex string) error {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return fmt.Errorf("invalid discovery ID %q: %w", idHex, err)
	}
	if _, err := r.collection.DeleteOne(ctx, bson.M{"_id": oid}); err != nil {
		return fmt.Errorf("delete discovery %s: %w", idHex, err)
	}
	return nil
}

// EnsureIndexes creates necessary indexes.
func (r *DiscoveryRepository) EnsureIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "project_id", Value: 1},
				{Key: "discovery_date", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "created_at", Value: -1},
			},
		},
		// Resolves "which discoveries did this run produce" — the retire
		// step of a resumed run, and the filter that keeps a run's own
		// partial result out of its previous-discovery context.
		{
			Keys: bson.D{
				{Key: "run_id", Value: 1},
			},
		},
	}

	_, err := r.collection.Indexes().CreateMany(ctx, indexes)
	return err
}
