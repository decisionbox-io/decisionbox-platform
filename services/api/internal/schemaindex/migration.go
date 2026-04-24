package schemaindex

import (
	"context"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// MigratePreExistingProjects sweeps the projects collection on API
// startup and marks every pre-schema-retrieval project (empty
// schema_index_status, warehouse configured) as pending_indexing so
// the worker picks them up on its next tick. One-shot; idempotent —
// projects already in a non-empty state are left alone, and projects
// without a warehouse are left at empty status until they're edited
// to add one.
//
// Plan §4: "projects without a Qdrant collection are marked
// pending_indexing. First interaction that would require schema
// context (running discovery, opening /ask) triggers indexing; user
// sees the progress panel."
//
// Returns (processed, error). processed is the number of projects
// that got transitioned; zero on subsequent boots after migration ran.
func MigratePreExistingProjects(ctx context.Context, projects *database.ProjectRepository) (int, error) {
	if projects == nil {
		return 0, fmt.Errorf("schemaindex: Projects repo is required")
	}
	col := projects.GetCollection()

	filter := bson.M{
		"warehouse.provider": bson.M{"$nin": []any{"", nil}},
		// Missing field OR empty string — both look "unindexed".
		"$or": []bson.M{
			{"schema_index_status": bson.M{"$exists": false}},
			{"schema_index_status": ""},
		},
	}
	cursor, err := col.Find(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("schemaindex: scan legacy projects: %w", err)
	}
	defer cursor.Close(ctx)

	processed := 0
	for cursor.Next(ctx) {
		var p models.Project
		if err := cursor.Decode(&p); err != nil {
			apilog.WithError(err).Warn("schemaindex: decode project during migration")
			continue
		}
		if p.ID == "" {
			continue
		}
		if err := projects.SetSchemaIndexStatus(ctx, p.ID, models.SchemaIndexStatusPendingIndexing, ""); err != nil {
			apilog.WithFields(apilog.Fields{
				"project_id": p.ID,
				"error":      err.Error(),
			}).Warn("schemaindex: migrate project failed")
			continue
		}
		apilog.WithFields(apilog.Fields{
			"project_id": p.ID,
			"name":       p.Name,
		}).Info("schemaindex: migrated pre-existing project → pending_indexing")
		processed++
	}
	if err := cursor.Err(); err != nil && err != mongo.ErrNoDocuments {
		return processed, fmt.Errorf("schemaindex: cursor error during migration: %w", err)
	}
	if processed > 0 {
		apilog.WithField("count", processed).Info("schemaindex: migrated pre-existing projects to pending_indexing")
	}
	return processed, nil
}
