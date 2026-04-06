package database

import (
	"context"
	"fmt"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AskSessionRepository handles CRUD for the "ask_sessions" collection.
type AskSessionRepository struct {
	db *DB
}

func NewAskSessionRepository(db *DB) *AskSessionRepository {
	return &AskSessionRepository{db: db}
}

func (r *AskSessionRepository) Create(ctx context.Context, session *commonmodels.AskSession) error {
	session.MessageCount = len(session.Messages)
	session.CreatedAt = time.Now()
	session.UpdatedAt = time.Now()
	_, err := r.db.Collection("ask_sessions").InsertOne(ctx, session)
	if err != nil {
		return fmt.Errorf("insert ask session: %w", err)
	}
	return nil
}

func (r *AskSessionRepository) AppendMessage(ctx context.Context, sessionID string, msg commonmodels.AskSessionMessage) error {
	_, err := r.db.Collection("ask_sessions").UpdateOne(ctx,
		bson.M{"_id": sessionID},
		bson.M{
			"$push": bson.M{"messages": msg},
			"$inc":  bson.M{"message_count": 1},
			"$set":  bson.M{"updated_at": time.Now()},
		},
	)
	if err != nil {
		return fmt.Errorf("append message to session %s: %w", sessionID, err)
	}
	return nil
}

func (r *AskSessionRepository) GetByID(ctx context.Context, sessionID string) (*commonmodels.AskSession, error) {
	var session commonmodels.AskSession
	err := r.db.Collection("ask_sessions").FindOne(ctx, bson.M{"_id": sessionID}).Decode(&session)
	if err != nil {
		return nil, fmt.Errorf("get ask session %s: %w", sessionID, err)
	}
	return &session, nil
}

func (r *AskSessionRepository) ListByProject(ctx context.Context, projectID string, limit int) ([]*commonmodels.AskSession, error) {
	if limit <= 0 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"_id":           1,
			"project_id":    1,
			"user_id":       1,
			"title":         1,
			"message_count": 1,
			"created_at":    1,
			"updated_at":    1,
		})

	cursor, err := r.db.Collection("ask_sessions").Find(ctx, bson.M{"project_id": projectID}, opts)
	if err != nil {
		return nil, fmt.Errorf("list ask sessions for project %s: %w", projectID, err)
	}
	defer cursor.Close(ctx)

	var sessions []*commonmodels.AskSession
	if err := cursor.All(ctx, &sessions); err != nil {
		return nil, fmt.Errorf("decode ask sessions: %w", err)
	}
	return sessions, nil
}

func (r *AskSessionRepository) Delete(ctx context.Context, sessionID string) error {
	_, err := r.db.Collection("ask_sessions").DeleteOne(ctx, bson.M{"_id": sessionID})
	if err != nil {
		return fmt.Errorf("delete ask session %s: %w", sessionID, err)
	}
	return nil
}
