package database

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ReadMarkRepository stores per-user read state for insights and recommendations.
type ReadMarkRepository struct {
	col *mongo.Collection
}

func NewReadMarkRepository(db *DB) *ReadMarkRepository {
	return &ReadMarkRepository{col: db.Collection("read_marks")}
}

// Upsert records that the user has read the target. Repeated calls with the
// same key refresh ReadAt instead of creating duplicates — the unique index
// on (project_id, user_id, target_type, target_id) enforces this.
func (r *ReadMarkRepository) Upsert(ctx context.Context, mark *models.ReadMark) error {
	filter := bson.M{
		"project_id":  mark.ProjectID,
		"user_id":     mark.UserID,
		"target_type": mark.TargetType,
		"target_id":   mark.TargetID,
	}
	mark.ReadAt = time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"project_id":  mark.ProjectID,
			"user_id":     mark.UserID,
			"target_type": mark.TargetType,
			"target_id":   mark.TargetID,
			"read_at":     mark.ReadAt,
		},
	}
	opts := options.Update().SetUpsert(true)
	if _, err := r.col.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("upsert read mark: %w", err)
	}
	return nil
}

// Delete removes the read mark for the given target. Idempotent: returns nil
// when no mark exists (the end state — not-read — is reached either way).
func (r *ReadMarkRepository) Delete(ctx context.Context, projectID, userID, targetType, targetID string) error {
	filter := bson.M{
		"project_id":  projectID,
		"user_id":     userID,
		"target_type": targetType,
		"target_id":   targetID,
	}
	if _, err := r.col.DeleteOne(ctx, filter); err != nil {
		return fmt.Errorf("delete read mark: %w", err)
	}
	return nil
}

// ListReadIDs returns just the target IDs the user has read for the given type,
// scoped by (project_id, user_id). Used by list pages to apply greyed styling
// without fetching the full documents.
func (r *ReadMarkRepository) ListReadIDs(ctx context.Context, projectID, userID, targetType string) ([]string, error) {
	filter := bson.M{
		"project_id":  projectID,
		"user_id":     userID,
		"target_type": targetType,
	}
	opts := options.Find().SetProjection(bson.M{"target_id": 1, "_id": 0})
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list read marks: %w", err)
	}
	defer cursor.Close(ctx)

	ids := make([]string, 0)
	for cursor.Next(ctx) {
		var doc struct {
			TargetID string `bson:"target_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode read mark: %w", err)
		}
		ids = append(ids, doc.TargetID)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate read marks: %w", err)
	}
	return ids, nil
}
