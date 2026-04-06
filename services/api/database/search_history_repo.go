package database

import (
	"context"
	"fmt"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SearchHistoryRepository handles CRUD for the "search_history" collection.
type SearchHistoryRepository struct {
	db *DB
}

func NewSearchHistoryRepository(db *DB) *SearchHistoryRepository {
	return &SearchHistoryRepository{db: db}
}

func (r *SearchHistoryRepository) Save(ctx context.Context, entry *commonmodels.SearchHistory) error {
	_, err := r.db.Collection("search_history").InsertOne(ctx, entry)
	if err != nil {
		return fmt.Errorf("insert search history: %w", err)
	}
	return nil
}

func (r *SearchHistoryRepository) ListByUser(ctx context.Context, userID string, limit int) ([]*commonmodels.SearchHistory, error) {
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))

	cursor, err := r.db.Collection("search_history").Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list search history for user %s: %w", userID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.SearchHistory
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode search history: %w", err)
	}
	return results, nil
}

func (r *SearchHistoryRepository) ListByProject(ctx context.Context, projectID string, limit int) ([]*commonmodels.SearchHistory, error) {
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))

	cursor, err := r.db.Collection("search_history").Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list search history for project %s: %w", projectID, err)
	}
	defer cursor.Close(ctx)

	var results []*commonmodels.SearchHistory
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode search history: %w", err)
	}
	return results, nil
}
