package discovery

import (
	"context"
	"fmt"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoEmbedIndexStore implements EmbedIndexStore using MongoDB.
type MongoEmbedIndexStore struct {
	db *database.DB
}

// NewMongoEmbedIndexStore creates a new MongoDB-backed EmbedIndexStore.
func NewMongoEmbedIndexStore(db *database.DB) *MongoEmbedIndexStore {
	return &MongoEmbedIndexStore{db: db}
}

func (s *MongoEmbedIndexStore) InsertInsights(ctx context.Context, insights []*commonmodels.StandaloneInsight) error {
	if len(insights) == 0 {
		return nil
	}
	docs := make([]interface{}, len(insights))
	for i, ins := range insights {
		docs[i] = ins
	}
	_, err := s.db.Collection("insights").InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("insert insights: %w", err)
	}
	return nil
}

func (s *MongoEmbedIndexStore) InsertRecommendations(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error {
	if len(recs) == 0 {
		return nil
	}
	docs := make([]interface{}, len(recs))
	for i, rec := range recs {
		docs[i] = rec
	}
	_, err := s.db.Collection("recommendations").InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("insert recommendations: %w", err)
	}
	return nil
}

// DeleteByDiscovery implements EmbedIndexStore.
func (s *MongoEmbedIndexStore) DeleteByDiscovery(ctx context.Context, discoveryID string) ([]string, []string, error) {
	if discoveryID == "" {
		return nil, nil, fmt.Errorf("delete standalone docs: discovery_id is required")
	}
	insightIDs, err := s.deleteByDiscovery(ctx, "insights", discoveryID)
	if err != nil {
		return nil, nil, err
	}
	recIDs, err := s.deleteByDiscovery(ctx, "recommendations", discoveryID)
	if err != nil {
		// The insight rows are already gone. Return their ids anyway so the
		// caller can still delete the matching vectors — leaving points
		// behind for rows that no longer exist is the worse outcome.
		return insightIDs, nil, err
	}
	return insightIDs, recIDs, nil
}

// deleteByDiscovery reads the ids of one collection's rows for a discovery,
// then deletes them. Read-then-delete rather than the reverse because the ids
// are the Qdrant point ids and are unrecoverable once the rows are gone.
func (s *MongoEmbedIndexStore) deleteByDiscovery(ctx context.Context, collection, discoveryID string) ([]string, error) {
	filter := bson.M{"discovery_id": discoveryID}
	cursor, err := s.db.Collection(collection).Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("list %s for discovery %s: %w", collection, discoveryID, err)
	}
	defer cursor.Close(ctx) //nolint:errcheck

	ids := make([]string, 0)
	for cursor.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode %s id for discovery %s: %w", collection, discoveryID, err)
		}
		if doc.ID != "" {
			ids = append(ids, doc.ID)
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor %s for discovery %s: %w", collection, discoveryID, err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := s.db.Collection(collection).DeleteMany(ctx, filter); err != nil {
		return ids, fmt.Errorf("delete %s for discovery %s: %w", collection, discoveryID, err)
	}
	return ids, nil
}

func (s *MongoEmbedIndexStore) UpdateEmbedding(ctx context.Context, collection, id, embeddingText, embeddingModel string) error {
	_, err := s.db.Collection(collection).UpdateByID(ctx, id, map[string]interface{}{
		"$set": map[string]interface{}{
			"embedding_text":  embeddingText,
			"embedding_model": embeddingModel,
		},
	})
	if err != nil {
		return fmt.Errorf("update embedding for %s/%s: %w", collection, id, err)
	}
	return nil
}

func (s *MongoEmbedIndexStore) UpdateDuplicate(ctx context.Context, collection, id, duplicateOf string, score float64) error {
	_, err := s.db.Collection(collection).UpdateByID(ctx, id, map[string]interface{}{
		"$set": map[string]interface{}{
			"duplicate_of":     duplicateOf,
			"similarity_score": score,
		},
	})
	if err != nil {
		return fmt.Errorf("update duplicate for %s/%s: %w", collection, id, err)
	}
	return nil
}
