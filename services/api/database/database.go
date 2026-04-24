package database

import (
	"context"
	"fmt"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DB wraps the MongoDB client for the API service.
type DB struct {
	client *gomongo.Client
}

func New(client *gomongo.Client) *DB {
	return &DB{client: client}
}

func (db *DB) Collection(name string) *mongo.Collection {
	return db.client.Collection(name)
}

// --- Project Repository ---

type ProjectRepository struct {
	col *mongo.Collection
}

func NewProjectRepository(db *DB) *ProjectRepository {
	return &ProjectRepository{col: db.Collection("projects")}
}

func (r *ProjectRepository) GetCollection() *mongo.Collection {
	return r.col
}

func (r *ProjectRepository) Create(ctx context.Context, p *models.Project) error {
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	if p.Status == "" {
		p.Status = "active"
	}

	result, err := r.col.InsertOne(ctx, p)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		p.ID = oid.Hex()
	}

	return nil
}

func (r *ProjectRepository) GetByID(ctx context.Context, id string) (*models.Project, error) {
	filter := bson.M{}
	if oid, err := primitive.ObjectIDFromHex(id); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = id
	}

	var p models.Project
	if err := r.col.FindOne(ctx, filter).Decode(&p); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find project: %w", err)
	}
	return &p, nil
}

func (r *ProjectRepository) List(ctx context.Context, limit, offset int) ([]*models.Project, error) {
	if limit <= 0 {
		limit = 50
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetSkip(int64(offset))

	cursor, err := r.col.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer cursor.Close(ctx)

	var projects []*models.Project
	if err := cursor.All(ctx, &projects); err != nil {
		return nil, fmt.Errorf("decode projects: %w", err)
	}
	return projects, nil
}

func (r *ProjectRepository) Update(ctx context.Context, id string, p *models.Project) error {
	filter := bson.M{}
	if oid, err := primitive.ObjectIDFromHex(id); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = id
	}

	p.ID = "" // don't attempt to update _id
	p.UpdatedAt = time.Now()
	update := bson.M{"$set": p}

	result, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

func (r *ProjectRepository) Delete(ctx context.Context, id string) error {
	filter := bson.M{}
	if oid, err := primitive.ObjectIDFromHex(id); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = id
	}

	result, err := r.col.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// Count returns the number of projects in the collection. Used by the
// cloud policy plugin's reconciliation loop to report ground-truth
// tenant counts to the control plane.
func (r *ProjectRepository) Count(ctx context.Context) (int, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count projects: %w", err)
	}
	return int(n), nil
}

// CountWithWarehouse returns the number of projects that have a
// configured warehouse — the data-source unit used by reconciliation.
func (r *ProjectRepository) CountWithWarehouse(ctx context.Context) (int, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{
		"warehouse.provider": bson.M{"$nin": []any{"", nil}},
	})
	if err != nil {
		return 0, fmt.Errorf("count projects with warehouse: %w", err)
	}
	return int(n), nil
}

func (r *ProjectRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "domain", Value: 1}}},
	})
	return err
}

// SetSchemaIndexStatus transitions a project through the indexing lifecycle
// (pending_indexing → indexing → ready | failed). Stamps
// schema_index_updated_at on success transitions so "last indexed" timers
// are accurate, and sets/clears schema_index_error on failed/ready.
//
// This is the only entry point that writes schema_index_status — handlers
// and the worker loop call it instead of hand-rolling UpdateOne. Prevents
// drift between lifecycle transitions.
func (r *ProjectRepository) SetSchemaIndexStatus(ctx context.Context, id, status, errMsg string) error {
	if !isValidSchemaIndexStatus(status) {
		return fmt.Errorf("invalid schema_index_status: %q", status)
	}

	filter := bson.M{}
	if oid, err := primitive.ObjectIDFromHex(id); err == nil {
		filter["_id"] = oid
	} else {
		filter["_id"] = id
	}

	now := time.Now().UTC()
	set := bson.M{
		"schema_index_status": status,
		"updated_at":          now,
	}
	// Only stamp `schema_index_updated_at` on terminal success. Failure keeps
	// the prior timestamp so the UI can still show "last indexed 3h ago"
	// while the current attempt is in failed state.
	if status == models.SchemaIndexStatusReady {
		set["schema_index_updated_at"] = now
	}

	update := bson.M{"$set": set}

	switch status {
	case models.SchemaIndexStatusFailed:
		update["$set"].(bson.M)["schema_index_error"] = errMsg
	case models.SchemaIndexStatusReady, models.SchemaIndexStatusPendingIndexing, models.SchemaIndexStatusIndexing:
		// Entering a non-failed state → clear any prior error message so the
		// UI doesn't show a stale banner.
		update["$unset"] = bson.M{"schema_index_error": ""}
	}

	res, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("set schema_index_status: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// ClaimNextPendingIndex atomically picks one project in
// pending_indexing state and transitions it to indexing, so the
// single-node worker loop can safely poll without racing against a user
// clicking "Re-index" at the same moment. Returns (nil, nil) when nothing
// is pending.
func (r *ProjectRepository) ClaimNextPendingIndex(ctx context.Context) (*models.Project, error) {
	now := time.Now().UTC()
	filter := bson.M{"schema_index_status": models.SchemaIndexStatusPendingIndexing}
	update := bson.M{
		"$set": bson.M{
			"schema_index_status": models.SchemaIndexStatusIndexing,
			"updated_at":          now,
		},
		"$unset": bson.M{"schema_index_error": ""},
	}
	opts := options.FindOneAndUpdate().
		SetReturnDocument(options.After).
		SetSort(bson.D{{Key: "updated_at", Value: 1}}) // FIFO: oldest pending first

	var p models.Project
	if err := r.col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&p); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("claim next pending index: %w", err)
	}
	return &p, nil
}

func isValidSchemaIndexStatus(s string) bool {
	switch s {
	case models.SchemaIndexStatusPendingIndexing,
		models.SchemaIndexStatusIndexing,
		models.SchemaIndexStatusReady,
		models.SchemaIndexStatusFailed:
		return true
	}
	return false
}

// --- Discovery Repository ---

type DiscoveryRepository struct {
	col *mongo.Collection
}

func NewDiscoveryRepository(db *DB) *DiscoveryRepository {
	return &DiscoveryRepository{col: db.Collection("discoveries")}
}

func (r *DiscoveryRepository) GetByID(ctx context.Context, id string) (*models.DiscoveryResult, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, nil
	}

	var result models.DiscoveryResult
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

func (r *DiscoveryRepository) GetLatest(ctx context.Context, projectID string) (*models.DiscoveryResult, error) {
	filter := bson.M{"project_id": projectID}
	opts := options.FindOne().SetSort(bson.D{{Key: "discovery_date", Value: -1}})

	var result models.DiscoveryResult
	if err := r.col.FindOne(ctx, filter, opts).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find discovery: %w", err)
	}
	return &result, nil
}

func (r *DiscoveryRepository) GetByDate(ctx context.Context, projectID string, date time.Time) (*models.DiscoveryResult, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	filter := bson.M{
		"project_id":    projectID,
		"discovery_date": bson.M{"$gte": startOfDay, "$lt": endOfDay},
	}

	var result models.DiscoveryResult
	if err := r.col.FindOne(ctx, filter).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find discovery: %w", err)
	}
	return &result, nil
}

func (r *DiscoveryRepository) List(ctx context.Context, projectID string, limit int) ([]*models.DiscoveryResult, error) {
	if limit <= 0 {
		limit = 30
	}

	filter := bson.M{"project_id": projectID}
	opts := options.Find().
		SetSort(bson.D{{Key: "discovery_date", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"exploration_log":    0,
			"analysis_log":      0,
			"recommendation_log": 0,
			"validation_log":    0,
		})

	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list discoveries: %w", err)
	}
	defer cursor.Close(ctx)

	results := make([]*models.DiscoveryResult, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode discoveries: %w", err)
	}
	return results, nil
}
