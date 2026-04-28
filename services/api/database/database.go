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
	db  *DB // kept for cross-collection cascade deletes; nil-safe paths exist
}

func NewProjectRepository(db *DB) *ProjectRepository {
	return &ProjectRepository{col: db.Collection("projects"), db: db}
}

func (r *ProjectRepository) GetCollection() *mongo.Collection {
	return r.col
}

// Create inserts a new project. The `_id` is always a Mongo-generated
// ObjectID — any value the caller put in p.ID is discarded so production
// and tests share a single id shape (see also Update / Delete /
// SetSchemaIndexStatus, which all assume ObjectID).
func (r *ProjectRepository) Create(ctx context.Context, p *models.Project) error {
	p.ID = "" // force Mongo to generate an ObjectID
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	if p.Status == "" {
		p.Status = "active"
	}

	result, err := r.col.InsertOne(ctx, p)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}

	oid, ok := result.InsertedID.(primitive.ObjectID)
	if !ok {
		return fmt.Errorf("insert project: expected ObjectID, got %T", result.InsertedID)
	}
	p.ID = oid.Hex()
	return nil
}

func (r *ProjectRepository) GetByID(ctx context.Context, id string) (*models.Project, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", id, err)
	}

	var p models.Project
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&p); err != nil {
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
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid project id %q: %w", id, err)
	}

	// Zero p.ID so Mongo's $set doesn't try to update the immutable
	// _id field, but restore it on the way out — handlers reuse the
	// passed-in struct as their JSON response, and a wiped id leaks
	// to the dashboard as "" causing the next PUT to /api/v1/projects/
	// → 405. Saw this bite the pack-gen wizard's blurb step on
	// 2026-04-28.
	p.ID = ""
	p.UpdatedAt = time.Now()
	update := bson.M{"$set": p}

	result, err := r.col.UpdateOne(ctx, bson.M{"_id": oid}, update)
	p.ID = id
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

func (r *ProjectRepository) Delete(ctx context.Context, id string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid project id %q: %w", id, err)
	}

	result, err := r.col.DeleteOne(ctx, bson.M{"_id": oid})
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// projectChildCollections is every collection that holds project-scoped
// data keyed directly by `project_id`. The cascade fans out one
// DeleteMany per collection. Adding a new project-scoped collection
// requires adding it here AND to the integration test seed — keep the
// two in sync, otherwise project deletion silently leaks rows.
//
// `feedback` is intentionally absent: it's keyed by `discovery_id`,
// not project_id, and is handled separately via a two-step query.
var projectChildCollections = []string{
	"discoveries",
	"discovery_runs",
	"project_context",
	"insights",
	"recommendations",
	"discovery_debug_logs",
	"ask_sessions",
	"search_history",
	"bookmark_lists",
	"bookmarks",
	"read_marks",
	"project_schema_index_progress",
	"project_schema_cache",
	"project_schema_index_logs",
}

// DeleteCascade removes every Mongo row owned by a project, then the
// project doc itself. Returns nil when the project (and any leftover
// children) are gone — idempotent on a project that's already deleted
// because every step is a DeleteMany / DeleteOne with no row-count
// requirement except the final project doc.
//
// Failure model: best-effort within the cascade. The first child
// delete that errors aborts the rest and returns the error wrapped
// with the failing collection name — the caller can retry safely
// since each step is idempotent. The project doc is deleted LAST so
// a partial cascade leaves the user's view intact (they can re-issue
// delete and finish what didn't land).
//
// Caller must already have:
//   - dropped the Qdrant collection (handled in handler)
//   - swept project secrets via the optional secrets.ProjectDeleter (handler)
//   - confirmed no schema-indexing run is in flight (handler returns 409)
//
// Those live outside the repo because they touch trust boundaries
// (external services, IAM-audited paths) the repo shouldn't reach.
func (r *ProjectRepository) DeleteCascade(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("project id is required")
	}
	if r.db == nil {
		return fmt.Errorf("project repository not wired with DB; cannot cascade")
	}
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid project id %q: %w", id, err)
	}

	// Step 1: collect discovery ids so feedback can be cleaned up by
	// discovery_id without a join. We do this BEFORE deleting the
	// discoveries doc, otherwise feedback orphans.
	discCol := r.db.Collection("discoveries")
	cur, err := discCol.Find(ctx, bson.M{"project_id": id}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return fmt.Errorf("list discoveries for cascade: %w", err)
	}
	// Defer ensures the cursor closes even on a Decode error mid-loop.
	// The Close error is intentionally discarded — by the time Close
	// fires, we've either succeeded (and don't care about teardown) or
	// already returned an error we'd rather report.
	defer func() { _ = cur.Close(ctx) }()
	discoveryIDs := make([]string, 0)
	for cur.Next(ctx) {
		var doc struct {
			ID interface{} `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return fmt.Errorf("decode discovery id: %w", err)
		}
		switch v := doc.ID.(type) {
		case primitive.ObjectID:
			discoveryIDs = append(discoveryIDs, v.Hex())
		case string:
			discoveryIDs = append(discoveryIDs, v)
		}
	}
	if err := cur.Err(); err != nil {
		return fmt.Errorf("iterate discoveries: %w", err)
	}

	// Step 2: feedback by discovery_id (only if there were discoveries).
	if len(discoveryIDs) > 0 {
		if _, err := r.db.Collection("feedback").DeleteMany(ctx, bson.M{"discovery_id": bson.M{"$in": discoveryIDs}}); err != nil {
			return fmt.Errorf("delete feedback: %w", err)
		}
	}

	// Step 3: each project-scoped collection.
	for _, name := range projectChildCollections {
		if _, err := r.db.Collection(name).DeleteMany(ctx, bson.M{"project_id": id}); err != nil {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}

	// Step 4: the project doc itself. If it was already deleted (race
	// or retry) we treat that as success — the goal is "no project
	// with this id exists", not "we performed the deletion".
	if _, err := r.col.DeleteOne(ctx, bson.M{"_id": oid}); err != nil {
		return fmt.Errorf("delete project doc: %w", err)
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
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid project id %q: %w", id, err)
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

	res, err := r.col.UpdateOne(ctx, bson.M{"_id": oid}, update)
	if err != nil {
		return fmt.Errorf("set schema_index_status: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// ResetStaleIndexingProjects flips projects stuck in "indexing" back to
// "pending_indexing" when their updated_at is older than staleAfter.
// Covers the crash-recovery case: the API died mid-run, the agent
// subprocess is gone, and Mongo still says "indexing" forever. Runs
// once on API boot. Returns the number of rows reset.
//
// We use updated_at (which the progress-repo path bumps on every
// IncrementDone) rather than started_at so a genuinely long FINPORT
// rebuild (~6 min) never trips this.
func (r *ProjectRepository) ResetStaleIndexingProjects(ctx context.Context, staleAfter time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-staleAfter)
	filter := bson.M{
		"schema_index_status": models.SchemaIndexStatusIndexing,
		"updated_at":          bson.M{"$lt": cutoff},
	}
	update := bson.M{
		"$set": bson.M{
			"schema_index_status": models.SchemaIndexStatusPendingIndexing,
			"schema_index_error":  "previous indexing run did not complete (process crash or shutdown) — re-queued",
			"updated_at":          time.Now().UTC(),
		},
	}
	res, err := r.col.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, fmt.Errorf("reset stale indexing projects: %w", err)
	}
	return int(res.ModifiedCount), nil
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
		models.SchemaIndexStatusFailed,
		models.SchemaIndexStatusCancelled,
		models.SchemaIndexStatusNeedsReindex:
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
