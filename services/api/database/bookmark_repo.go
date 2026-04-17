package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ErrBookmarkNotFound is returned when a bookmark lookup finds no match.
var ErrBookmarkNotFound = errors.New("bookmark not found")

// BookmarkRepository handles CRUD for individual bookmarks inside lists.
type BookmarkRepository struct {
	col *mongo.Collection
}

func NewBookmarkRepository(db *DB) *BookmarkRepository {
	return &BookmarkRepository{col: db.Collection("bookmarks")}
}

// Add inserts a bookmark. If the same (list_id, target_type, target_id) already
// exists, the existing bookmark is returned unchanged — the operation is idempotent.
// The caller is responsible for verifying the list exists and belongs to the user
// (see BookmarkListRepository.GetByID) before calling Add.
func (r *BookmarkRepository) Add(ctx context.Context, bm *models.Bookmark) (*models.Bookmark, error) {
	existing, err := r.findByComposite(ctx, bm.ListID, bm.TargetType, bm.TargetID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	bm.CreatedAt = time.Now().UTC()
	result, err := r.col.InsertOne(ctx, bm)
	if err != nil {
		// Race: another writer inserted between our find and insert.
		// The unique index enforces at-most-one; re-read and return it.
		if mongo.IsDuplicateKeyError(err) {
			existing, findErr := r.findByComposite(ctx, bm.ListID, bm.TargetType, bm.TargetID)
			if findErr != nil {
				return nil, findErr
			}
			if existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("insert bookmark: %w", err)
	}
	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		bm.ID = oid.Hex()
	}
	return bm, nil
}

func (r *BookmarkRepository) findByComposite(ctx context.Context, listID, targetType, targetID string) (*models.Bookmark, error) {
	filter := bson.M{
		"list_id":     listID,
		"target_type": targetType,
		"target_id":   targetID,
	}
	var existing models.Bookmark
	err := r.col.FindOne(ctx, filter).Decode(&existing)
	if err == nil {
		return &existing, nil
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	return nil, fmt.Errorf("find bookmark: %w", err)
}

// ListByList returns every bookmark in a list, newest first.
// Does not enforce user scoping — the caller must verify list ownership first.
func (r *BookmarkRepository) ListByList(ctx context.Context, listID string) ([]*models.Bookmark, error) {
	filter := bson.M{"list_id": listID}
	cursor, err := r.col.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer cursor.Close(ctx)

	bms := make([]*models.Bookmark, 0)
	if err := cursor.All(ctx, &bms); err != nil {
		return nil, fmt.Errorf("decode bookmarks: %w", err)
	}
	return bms, nil
}

// Delete removes a bookmark by its ID, scoped by (project_id, user_id, list_id).
// Returns ErrBookmarkNotFound when nothing matches.
func (r *BookmarkRepository) Delete(ctx context.Context, projectID, userID, listID, bookmarkID string) error {
	filter := bson.M{
		"project_id": projectID,
		"user_id":    userID,
		"list_id":    listID,
	}
	if oid, err := primitive.ObjectIDFromHex(bookmarkID); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = bookmarkID
	}

	result, err := r.col.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrBookmarkNotFound
	}
	return nil
}

// ListsContaining returns the list IDs that contain a bookmark for the given
// target, scoped by (project_id, user_id). Powers the "Add to list" checkmark UI.
func (r *BookmarkRepository) ListsContaining(ctx context.Context, projectID, userID, targetType, targetID string) ([]string, error) {
	filter := bson.M{
		"project_id":  projectID,
		"user_id":     userID,
		"target_type": targetType,
		"target_id":   targetID,
	}
	cursor, err := r.col.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("find containing lists: %w", err)
	}
	defer cursor.Close(ctx)

	var bms []*models.Bookmark
	if err := cursor.All(ctx, &bms); err != nil {
		return nil, fmt.Errorf("decode bookmarks: %w", err)
	}
	listIDs := make([]string, 0, len(bms))
	for _, bm := range bms {
		listIDs = append(listIDs, bm.ListID)
	}
	return listIDs, nil
}
