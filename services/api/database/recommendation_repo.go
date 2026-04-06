package database

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// RecommendationRepository handles CRUD for the denormalized "recommendations" collection.
type RecommendationRepository struct {
	db *DB
}

func NewRecommendationRepository(db *DB) *RecommendationRepository {
	return &RecommendationRepository{db: db}
}

func (r *RecommendationRepository) Create(ctx context.Context, rec *commonmodels.StandaloneRecommendation) error {
	_, err := r.db.Collection("recommendations").InsertOne(ctx, rec)
	if err != nil {
		return fmt.Errorf("insert recommendation: %w", err)
	}
	return nil
}

func (r *RecommendationRepository) CreateMany(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error {
	if len(recs) == 0 {
		return nil
	}
	docs := make([]interface{}, len(recs))
	for i, rec := range recs {
		docs[i] = rec
	}
	_, err := r.db.Collection("recommendations").InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("insert recommendations: %w", err)
	}
	return nil
}

func (r *RecommendationRepository) GetByID(ctx context.Context, id string) (*commonmodels.StandaloneRecommendation, error) {
	var rec commonmodels.StandaloneRecommendation
	err := r.db.Collection("recommendations").FindOne(ctx, bson.M{"_id": id}).Decode(&rec)
	if err != nil {
		return nil, fmt.Errorf("get recommendation %s: %w", id, err)
	}
	return &rec, nil
}

func (r *RecommendationRepository) ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneRecommendation, error) {
	if limit <= 0 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetSkip(int64(offset))

	cursor, err := r.db.Collection("recommendations").Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list recommendations for project %s: %w", projectID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.StandaloneRecommendation
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode recommendations: %w", err)
	}
	return results, nil
}

func (r *RecommendationRepository) ListByDiscovery(ctx context.Context, discoveryID string) ([]*commonmodels.StandaloneRecommendation, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := r.db.Collection("recommendations").Find(ctx, bson.M{"discovery_id": discoveryID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list recommendations for discovery %s: %w", discoveryID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.StandaloneRecommendation
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode recommendations: %w", err)
	}
	return results, nil
}

func (r *RecommendationRepository) CountByProject(ctx context.Context, projectID string) (int64, error) {
	count, err := r.db.Collection("recommendations").CountDocuments(ctx, bson.M{"project_id": projectID})
	if err != nil {
		return 0, fmt.Errorf("count recommendations for project %s: %w", projectID, err)
	}
	return count, nil
}

func (r *RecommendationRepository) UpdateEmbedding(ctx context.Context, id string, embeddingText, embeddingModel string) error {
	_, err := r.db.Collection("recommendations").UpdateByID(ctx, id, bson.M{
		"$set": bson.M{
			"embedding_text":  embeddingText,
			"embedding_model": embeddingModel,
		},
	})
	if err != nil {
		return fmt.Errorf("update recommendation embedding %s: %w", id, err)
	}
	return nil
}

func (r *RecommendationRepository) UpdateDuplicate(ctx context.Context, id string, duplicateOf string, score float64) error {
	_, err := r.db.Collection("recommendations").UpdateByID(ctx, id, bson.M{
		"$set": bson.M{
			"duplicate_of":     duplicateOf,
			"similarity_score": score,
		},
	})
	if err != nil {
		return fmt.Errorf("update recommendation duplicate %s: %w", id, err)
	}
	return nil
}
