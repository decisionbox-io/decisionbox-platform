package database

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// InsightRepository handles CRUD for the denormalized "insights" collection.
type InsightRepository struct {
	db *DB
}

func NewInsightRepository(db *DB) *InsightRepository {
	return &InsightRepository{db: db}
}

func (r *InsightRepository) Create(ctx context.Context, insight *commonmodels.StandaloneInsight) error {
	_, err := r.db.Collection("insights").InsertOne(ctx, insight)
	if err != nil {
		return fmt.Errorf("insert insight: %w", err)
	}
	return nil
}

func (r *InsightRepository) CreateMany(ctx context.Context, insights []*commonmodels.StandaloneInsight) error {
	if len(insights) == 0 {
		return nil
	}
	docs := make([]interface{}, len(insights))
	for i, ins := range insights {
		docs[i] = ins
	}
	_, err := r.db.Collection("insights").InsertMany(ctx, docs)
	if err != nil {
		return fmt.Errorf("insert insights: %w", err)
	}
	return nil
}

func (r *InsightRepository) GetByID(ctx context.Context, id string) (*commonmodels.StandaloneInsight, error) {
	var insight commonmodels.StandaloneInsight
	err := r.db.Collection("insights").FindOne(ctx, bson.M{"_id": id}).Decode(&insight)
	if err != nil {
		return nil, fmt.Errorf("get insight %s: %w", id, err)
	}
	return &insight, nil
}

func (r *InsightRepository) ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneInsight, error) {
	if limit <= 0 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetSkip(int64(offset))

	cursor, err := r.db.Collection("insights").Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list insights for project %s: %w", projectID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.StandaloneInsight
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode insights: %w", err)
	}
	return results, nil
}

func (r *InsightRepository) ListByDiscovery(ctx context.Context, discoveryID string) ([]*commonmodels.StandaloneInsight, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := r.db.Collection("insights").Find(ctx, bson.M{"discovery_id": discoveryID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list insights for discovery %s: %w", discoveryID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.StandaloneInsight
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode insights: %w", err)
	}
	return results, nil
}

func (r *InsightRepository) CountByProject(ctx context.Context, projectID string) (int64, error) {
	count, err := r.db.Collection("insights").CountDocuments(ctx, bson.M{"project_id": projectID})
	if err != nil {
		return 0, fmt.Errorf("count insights for project %s: %w", projectID, err)
	}
	return count, nil
}

func (r *InsightRepository) UpdateEmbedding(ctx context.Context, id string, embeddingText, embeddingModel string) error {
	_, err := r.db.Collection("insights").UpdateByID(ctx, id, bson.M{
		"$set": bson.M{
			"embedding_text":  embeddingText,
			"embedding_model": embeddingModel,
		},
	})
	if err != nil {
		return fmt.Errorf("update insight embedding %s: %w", id, err)
	}
	return nil
}

func (r *InsightRepository) UpdateDuplicate(ctx context.Context, id string, duplicateOf string, score float64) error {
	_, err := r.db.Collection("insights").UpdateByID(ctx, id, bson.M{
		"$set": bson.M{
			"duplicate_of":     duplicateOf,
			"similarity_score": score,
		},
	})
	if err != nil {
		return fmt.Errorf("update insight duplicate %s: %w", id, err)
	}
	return nil
}

func (r *InsightRepository) GetLatestEmbeddingModel(ctx context.Context, projectID string) (string, error) {
	opts := options.FindOne().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetProjection(bson.M{"embedding_model": 1})

	var result struct {
		EmbeddingModel string `bson:"embedding_model"`
	}
	err := r.db.Collection("insights").FindOne(ctx, bson.M{
		"project_id":     projectID,
		"embedding_model": bson.M{"$ne": ""},
	}, opts).Decode(&result)
	if err != nil {
		return "", nil // not found is ok — no insights indexed yet
	}
	return result.EmbeddingModel, nil
}
