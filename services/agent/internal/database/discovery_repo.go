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
func (r *DiscoveryRepository) UpdateInsightValidation(ctx context.Context, discoveryIDHex, insightID string, validation interface{}) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": insightID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid},
		bson.M{"$set": bson.M{"insights.$[el].validation": validation, "updated_at": time.Now()}},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update insight validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s not found", discoveryIDHex)
	}
	return nil
}

// UpdateRecommendationValidation is the recommendation twin of
// UpdateInsightValidation. Same positional array-filter contract.
func (r *DiscoveryRepository) UpdateRecommendationValidation(ctx context.Context, discoveryIDHex, recID string, validation interface{}) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": recID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid},
		bson.M{"$set": bson.M{"recommendations.$[el].validation": validation, "updated_at": time.Now()}},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update recommendation validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s not found", discoveryIDHex)
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
			"project_id":     1,
			"discovery_date": 1,
			"run_type":       1,
			"areas_requested": 1,
			"insights":       1,
			"recommendations": 1,
			"summary":        1,
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
	}

	_, err := r.collection.Indexes().CreateMany(ctx, indexes)
	return err
}
