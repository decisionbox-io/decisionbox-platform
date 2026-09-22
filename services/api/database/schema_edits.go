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

// Audit-trail query bounds. DefaultSchemaEditLimit applies when the caller
// doesn't specify one; MaxSchemaEditLimit caps a crafted ?limit= so it can't
// ask for an unbounded scan.
const (
	DefaultSchemaEditLimit = 100
	MaxSchemaEditLimit     = 500
)

// SchemaEditRepository is the append-only audit log of manual schema edits
// (blurb rewrites, keyword/column changes, table removals) made through the
// schema editor. One document per edit in project_schema_edits. The record
// outlives the edit itself — manual edits are wiped by the next re-index, but
// this trail lets a user review + re-apply what they changed, and drives the
// "N manual edits will be lost" warning before a re-index / cache clear.
type SchemaEditRepository struct {
	col *mongo.Collection
}

// NewSchemaEditRepository wires the repo against project_schema_edits.
func NewSchemaEditRepository(db *DB) *SchemaEditRepository {
	return &SchemaEditRepository{col: db.Collection("project_schema_edits")}
}

// Record appends one edit to the audit trail. At is stamped server-side when
// zero so callers can't backdate a record.
func (r *SchemaEditRepository) Record(ctx context.Context, edit models.SchemaEdit) error {
	if edit.ProjectID == "" {
		return errors.New("projectID is required")
	}
	if edit.Action == "" {
		return errors.New("action is required")
	}
	if edit.At.IsZero() {
		edit.At = time.Now().UTC()
	}
	// Store a hex-string _id we generate ourselves rather than letting Mongo
	// assign a BSON ObjectID: SchemaEdit.ID is a Go string, and decoding an
	// ObjectID back into a string field depends on the driver registry. A
	// self-generated hex id round-trips as a plain string on every read.
	edit.ID = primitive.NewObjectID().Hex()
	if _, err := r.col.InsertOne(ctx, edit); err != nil {
		return fmt.Errorf("record schema edit: %w", err)
	}
	return nil
}

// List returns a project's manual schema edits, newest first. datasourceID ==
// "" lists every datasource; a non-empty value filters to one. limit <= 0 uses
// DefaultSchemaEditLimit; anything above MaxSchemaEditLimit is clamped. Returns
// an empty (non-nil) slice when nothing matches.
func (r *SchemaEditRepository) List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaEdit, error) {
	if projectID == "" {
		return nil, errors.New("projectID is required")
	}
	if limit <= 0 {
		limit = DefaultSchemaEditLimit
	}
	if limit > MaxSchemaEditLimit {
		limit = MaxSchemaEditLimit
	}
	filter := bson.M{"project_id": projectID}
	if datasourceID != "" {
		filter["datasource_id"] = datasourceID
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "at", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list schema edits: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	edits := make([]models.SchemaEdit, 0)
	if err := cur.All(ctx, &edits); err != nil {
		return nil, fmt.Errorf("decode schema edits: %w", err)
	}
	return edits, nil
}

// CountSince returns how many edits a project has recorded strictly after
// `since`. A zero `since` counts every edit (project never indexed).
// datasourceID scopes the count to one data source when non-empty (empty counts
// project-wide). When `actions` is non-empty the count is restricted to those
// edit actions — the re-index warning counts only the actions a re-index
// actually discards (blurb/keyword edits), while the cache-clear warning counts
// all actions. Backs the pre-reset warnings.
func (r *SchemaEditRepository) CountSince(ctx context.Context, projectID, datasourceID string, since time.Time, actions ...string) (int, error) {
	if projectID == "" {
		return 0, errors.New("projectID is required")
	}
	filter := bson.M{"project_id": projectID}
	if datasourceID != "" {
		filter["datasource_id"] = datasourceID
	}
	if !since.IsZero() {
		filter["at"] = bson.M{"$gt": since}
	}
	if len(actions) > 0 {
		filter["action"] = bson.M{"$in": actions}
	}
	n, err := r.col.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count schema edits: %w", err)
	}
	return int(n), nil
}
