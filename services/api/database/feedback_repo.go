package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// FeedbackRepository handles feedback CRUD operations.
type FeedbackRepository struct {
	col *mongo.Collection
}

func NewFeedbackRepository(db *DB) *FeedbackRepository {
	return &FeedbackRepository{col: db.Collection("feedback")}
}

// Upsert creates or updates feedback for a specific target.
// One feedback per (discovery_id, target_type, target_id).
func (r *FeedbackRepository) Upsert(ctx context.Context, fb *models.Feedback) (*models.Feedback, error) {
	filter := bson.M{
		"discovery_id": fb.DiscoveryID,
		"target_type":  fb.TargetType,
		"target_id":    fb.TargetID,
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"project_id":   fb.ProjectID,
			"discovery_id": fb.DiscoveryID,
			"target_type":  fb.TargetType,
			"target_id":    fb.TargetID,
			"rating":       fb.Rating,
			"comment":      fb.Comment,
			"created_at":   now,
		},
	}

	opts := options.Update().SetUpsert(true)
	result, err := r.col.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return nil, fmt.Errorf("upsert feedback: %w", err)
	}

	fb.CreatedAt = now
	if result.UpsertedID != nil {
		fb.ID = result.UpsertedID.(primitive.ObjectID).Hex()
	} else {
		// Fetch the existing document to get its ID
		var existing models.Feedback
		if err := r.col.FindOne(ctx, filter).Decode(&existing); err == nil {
			fb.ID = existing.ID
		}
	}

	return fb, nil
}

// ListByDiscovery returns all feedback for a discovery run.
func (r *FeedbackRepository) ListByDiscovery(ctx context.Context, discoveryID string) ([]*models.Feedback, error) {
	cursor, err := r.col.Find(ctx, bson.M{"discovery_id": discoveryID})
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	defer cursor.Close(ctx)

	results := make([]*models.Feedback, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode feedback: %w", err)
	}
	return results, nil
}

// Delete removes a feedback entry by ID.
func (r *FeedbackRepository) Delete(ctx context.Context, id string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid feedback ID: %w", err)
	}
	_, err = r.col.DeleteOne(ctx, bson.M{"_id": oid})
	if err != nil {
		return fmt.Errorf("delete feedback: %w", err)
	}
	return nil
}
