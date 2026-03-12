package database

import (
	"context"
	"fmt"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// FeedbackEntry represents a user feedback record (read-only in agent).
type FeedbackEntry struct {
	DiscoveryID string `bson:"discovery_id" json:"discovery_id"`
	TargetType  string `bson:"target_type" json:"target_type"`
	TargetID    string `bson:"target_id" json:"target_id"`
	Rating      string `bson:"rating" json:"rating"`
	Comment     string `bson:"comment,omitempty" json:"comment,omitempty"`
}

// FeedbackRepository reads user feedback (written by the API).
type FeedbackRepository struct {
	collection *mongo.Collection
}

func NewFeedbackRepository(client *DB) *FeedbackRepository {
	return &FeedbackRepository{
		collection: client.Collection(CollectionFeedback),
	}
}

// ListByDiscoveryIDs returns all feedback for a set of discovery IDs.
func (r *FeedbackRepository) ListByDiscoveryIDs(ctx context.Context, discoveryIDs []string) ([]FeedbackEntry, error) {
	if len(discoveryIDs) == 0 {
		return nil, nil
	}

	applog.WithField("discovery_count", len(discoveryIDs)).Debug("Fetching feedback for recent discoveries")

	filter := bson.M{"discovery_id": bson.M{"$in": discoveryIDs}}
	cursor, err := r.collection.Find(ctx, filter)
	if err != nil {
		applog.WithError(err).Warn("Failed to fetch feedback")
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	defer cursor.Close(ctx)

	results := make([]FeedbackEntry, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode feedback: %w", err)
	}

	applog.WithFields(applog.Fields{
		"feedback_count":  len(results),
		"discovery_count": len(discoveryIDs),
	}).Debug("Feedback loaded for context awareness")

	return results, nil
}
