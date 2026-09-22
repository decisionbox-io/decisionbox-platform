package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SchemaIndexRunRepository writes the durable, per-(datasource × run) result
// record the agent stamps when a datasource finishes indexing. The agent is
// the only writer (it alone knows the per-datasource outcome + stats); the API
// is the reader (for the dashboard history + roll-up). Both target the same
// project_schema_index_runs collection, whose indexes the API creates on
// startup (InitDatabase) — the agent only writes.
type SchemaIndexRunRepository struct {
	db *DB
}

// NewSchemaIndexRunRepository wires the agent-side repo.
func NewSchemaIndexRunRepository(db *DB) *SchemaIndexRunRepository {
	return &SchemaIndexRunRepository{db: db}
}

func (r *SchemaIndexRunRepository) col() *mongo.Collection {
	return r.db.Collection(CollectionSchemaIndexRuns)
}

// Record upserts the run result, keyed by (project_id, datasource_id, run_id).
// Upsert (not insert) so a retried stamp for the same datasource + run can't
// duplicate the row — the unique index on that triple backs the invariant.
func (r *SchemaIndexRunRepository) Record(ctx context.Context, run *models.SchemaIndexRun) error {
	if run == nil {
		return errors.New("run is required")
	}
	if run.ProjectID == "" {
		return errors.New("projectID is required")
	}
	if run.DatasourceID == "" {
		return errors.New("datasourceID is required")
	}
	if run.RunID == "" {
		return errors.New("runID is required")
	}
	filter := bson.M{
		"project_id":    run.ProjectID,
		"datasource_id": run.DatasourceID,
		"run_id":        run.RunID,
	}
	if _, err := r.col().ReplaceOne(ctx, filter, run, options.Replace().SetUpsert(true)); err != nil {
		return fmt.Errorf("record schema-index run: %w", err)
	}
	return nil
}
