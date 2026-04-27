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
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrBookmarkListNotFound is returned when a list lookup scoped by
// (project_id, user_id) finds no match. Handlers translate this to 404.
var ErrBookmarkListNotFound = errors.New("bookmark list not found")

// BookmarkListRepository handles CRUD for named bookmark lists.
type BookmarkListRepository struct {
	col         *mongo.Collection
	bookmarkCol *mongo.Collection
}

func NewBookmarkListRepository(db *DB) *BookmarkListRepository {
	return &BookmarkListRepository{
		col:         db.Collection("bookmark_lists"),
		bookmarkCol: db.Collection("bookmarks"),
	}
}

// Create inserts a new list and populates its generated ID and timestamps.
func (r *BookmarkListRepository) Create(ctx context.Context, list *models.BookmarkList) error {
	now := time.Now().UTC()
	list.CreatedAt = now
	list.UpdatedAt = now

	result, err := r.col.InsertOne(ctx, list)
	if err != nil {
		return fmt.Errorf("insert bookmark list: %w", err)
	}
	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		list.ID = oid.Hex()
	}
	return nil
}

// GetByID returns a list scoped by (project_id, user_id). Returns
// ErrBookmarkListNotFound if the list doesn't exist or isn't owned by the caller.
func (r *BookmarkListRepository) GetByID(ctx context.Context, projectID, userID, listID string) (*models.BookmarkList, error) {
	filter, err := listFilter(projectID, userID, listID)
	if err != nil {
		return nil, ErrBookmarkListNotFound
	}
	var list models.BookmarkList
	if err := r.col.FindOne(ctx, filter).Decode(&list); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrBookmarkListNotFound
		}
		return nil, fmt.Errorf("find bookmark list: %w", err)
	}
	count, err := r.bookmarkCol.CountDocuments(ctx, bson.M{"list_id": list.ID})
	if err != nil {
		return nil, fmt.Errorf("count bookmarks: %w", err)
	}
	list.ItemCount = count
	return &list, nil
}

// List returns all lists for (project_id, user_id), newest-updated first.
// Each list is populated with an accurate ItemCount.
func (r *BookmarkListRepository) List(ctx context.Context, projectID, userID string) ([]*models.BookmarkList, error) {
	filter := bson.M{"project_id": projectID, "user_id": userID}
	// Tiebreaker on _id desc: BSON DateTime has only millisecond
	// resolution, so a Create+Update issued within the same tick can
	// share updated_at — the storage-order fallback would be
	// non-deterministic. ObjectID embeds insertion time so it gives a
	// stable secondary order without changing the primary semantic.
	opts := options.Find().SetSort(bson.D{
		{Key: "updated_at", Value: -1},
		{Key: "_id", Value: -1},
	})

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list bookmark lists: %w", err)
	}
	defer cursor.Close(ctx)

	lists := make([]*models.BookmarkList, 0)
	if err := cursor.All(ctx, &lists); err != nil {
		return nil, fmt.Errorf("decode bookmark lists: %w", err)
	}

	for _, l := range lists {
		count, err := r.bookmarkCol.CountDocuments(ctx, bson.M{"list_id": l.ID})
		if err != nil {
			return nil, fmt.Errorf("count bookmarks: %w", err)
		}
		l.ItemCount = count
	}
	return lists, nil
}

// UpdateFields is the patch payload for partial updates on a list.
// Nil fields are left untouched; non-nil fields overwrite.
type UpdateFields struct {
	Name        *string
	Description *string
	Color       *string
}

// Update applies a partial update to the list, scoped by (project_id, user_id).
// Returns ErrBookmarkListNotFound if no matching list exists.
func (r *BookmarkListRepository) Update(ctx context.Context, projectID, userID, listID string, patch UpdateFields) (*models.BookmarkList, error) {
	filter, err := listFilter(projectID, userID, listID)
	if err != nil {
		return nil, ErrBookmarkListNotFound
	}

	set := bson.M{"updated_at": time.Now().UTC()}
	if patch.Name != nil {
		set["name"] = *patch.Name
	}
	if patch.Description != nil {
		set["description"] = *patch.Description
	}
	if patch.Color != nil {
		set["color"] = *patch.Color
	}

	result, err := r.col.UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return nil, fmt.Errorf("update bookmark list: %w", err)
	}
	if result.MatchedCount == 0 {
		return nil, ErrBookmarkListNotFound
	}
	return r.GetByID(ctx, projectID, userID, listID)
}

// Delete removes the list and cascades deletion of every bookmark in it.
// Scoped by (project_id, user_id).
func (r *BookmarkListRepository) Delete(ctx context.Context, projectID, userID, listID string) error {
	filter, err := listFilter(projectID, userID, listID)
	if err != nil {
		return ErrBookmarkListNotFound
	}
	result, err := r.col.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete bookmark list: %w", err)
	}
	if result.DeletedCount == 0 {
		return ErrBookmarkListNotFound
	}
	if _, err := r.bookmarkCol.DeleteMany(ctx, bson.M{"list_id": listID}); err != nil {
		return fmt.Errorf("cascade delete bookmarks: %w", err)
	}
	return nil
}

// listFilter builds a (project_id, user_id, _id) filter, handling hex vs raw _id.
func listFilter(projectID, userID, listID string) (bson.M, error) {
	filter := bson.M{"project_id": projectID, "user_id": userID}
	if oid, err := primitive.ObjectIDFromHex(listID); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = listID
	}
	if listID == "" {
		return nil, errors.New("empty list id")
	}
	return filter, nil
}
