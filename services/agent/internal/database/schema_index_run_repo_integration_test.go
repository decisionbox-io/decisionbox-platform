//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The agent is the writer of the durable per-datasource run record. These tests
// exercise Record's upsert-by-(project,datasource,run) semantics against a real
// Mongo, matching how the API side will read the same collection back.
func TestAgentInteg_SchemaIndexRun_Record(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(db)

	run := &models.SchemaIndexRun{
		ProjectID:       "proj-run-1",
		DatasourceID:    "wh_a",
		DatasourceName:  "Redshift",
		RunID:           "run-1",
		Kind:            models.SchemaIndexRunKindTables,
		ObjectsIndexed:  42,
		BlurbsGenerated: 40,
		Status:          models.SchemaIndexStatusReady,
		PhaseDurations:  map[string]int64{models.SchemaIndexPhaseSchemaDiscovery: 3000},
		TokensIn:        100,
		TokensOut:       200,
		StartedAt:       time.Now().Add(-30 * time.Second).UTC(),
		FinishedAt:      time.Now().UTC(),
	}
	if err := r.Record(ctx, run); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var got models.SchemaIndexRun
	if err := db.Collection(CollectionSchemaIndexRuns).
		FindOne(ctx, bson.M{"project_id": "proj-run-1", "datasource_id": "wh_a", "run_id": "run-1"}).
		Decode(&got); err != nil {
		t.Fatalf("raw find: %v", err)
	}
	if got.ObjectsIndexed != 42 || got.BlurbsGenerated != 40 {
		t.Errorf("counts wrong: %+v", got)
	}
	if got.Status != models.SchemaIndexStatusReady {
		t.Errorf("status = %q", got.Status)
	}
	if got.PhaseDurations[models.SchemaIndexPhaseSchemaDiscovery] != 3000 {
		t.Errorf("phase_durations = %+v", got.PhaseDurations)
	}
}

// A retried stamp for the same (project, datasource, run) must overwrite, not
// duplicate — the unique index backs this invariant.
func TestAgentInteg_SchemaIndexRun_UpsertIdempotent(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(db)

	base := &models.SchemaIndexRun{
		ProjectID: "proj-run-2", DatasourceID: "default", RunID: "run-x",
		Kind: models.SchemaIndexRunKindTables, Status: models.SchemaIndexStatusFailed,
		Error: "first try", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}
	if err := r.Record(ctx, base); err != nil {
		t.Fatalf("Record #1: %v", err)
	}
	// Re-stamp the same triple with a success result.
	base.Status = models.SchemaIndexStatusReady
	base.Error = ""
	base.ObjectsIndexed = 7
	if err := r.Record(ctx, base); err != nil {
		t.Fatalf("Record #2: %v", err)
	}

	count, err := db.Collection(CollectionSchemaIndexRuns).
		CountDocuments(ctx, bson.M{"project_id": "proj-run-2", "datasource_id": "default", "run_id": "run-x"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 doc after re-record, got %d", count)
	}
	var got models.SchemaIndexRun
	if err := db.Collection(CollectionSchemaIndexRuns).
		FindOne(ctx, bson.M{"project_id": "proj-run-2", "run_id": "run-x"}).Decode(&got); err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Status != models.SchemaIndexStatusReady || got.ObjectsIndexed != 7 || got.Error != "" {
		t.Errorf("re-record should overwrite, got %+v", got)
	}
}

func TestAgentInteg_SchemaIndexRun_Validation(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(db)

	cases := []*models.SchemaIndexRun{
		nil,
		{DatasourceID: "d", RunID: "r"},                 // no project
		{ProjectID: "p", RunID: "r"},                    // no datasource
		{ProjectID: "p", DatasourceID: "d"},             // no run
	}
	for i, c := range cases {
		if err := r.Record(ctx, c); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}
