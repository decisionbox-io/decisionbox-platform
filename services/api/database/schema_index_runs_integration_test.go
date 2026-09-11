//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The API reads the durable per-datasource run records the agent writes. These
// tests exercise List's filtering, ordering, and limit clamping against a real
// Mongo via the shared testDB fixture.
func insertRun(t *testing.T, ctx context.Context, run models.SchemaIndexRun) {
	t.Helper()
	if _, err := testDB.Collection("project_schema_index_runs").InsertOne(ctx, run); err != nil {
		t.Fatalf("insert run: %v", err)
	}
}

func TestInteg_SchemaIndexRuns_ListFilterOrderLimit(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(testDB)
	proj := "proj-runs-integ-1"
	t.Cleanup(func() {
		// Remove both this project's rows AND the cross-project "other" sentinel
		// inserted below — the unique (project_id, datasource_id, run_id) index
		// would otherwise make a rerun (go test -count=2) fail on re-insert.
		_, _ = testDB.Collection("project_schema_index_runs").
			DeleteMany(ctx, bson.M{"project_id": bson.M{"$in": []string{proj, "other"}}})
	})

	base := time.Now().UTC().Add(-time.Hour)
	// Two datasources, interleaved finish times.
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_a", RunID: "a1", Status: models.SchemaIndexStatusReady, ObjectsIndexed: 10, FinishedAt: base.Add(1 * time.Minute)})
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_b", RunID: "b1", Status: models.SchemaIndexStatusReady, ObjectsIndexed: 20, FinishedAt: base.Add(2 * time.Minute)})
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_a", RunID: "a2", Status: models.SchemaIndexStatusFailed, Error: "boom", FinishedAt: base.Add(3 * time.Minute)})
	// Another project's run must never leak in.
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: "other", DatasourceID: "wh_a", RunID: "x", Status: models.SchemaIndexStatusReady, FinishedAt: base.Add(4 * time.Minute)})

	t.Run("all datasources, newest finished_at first", func(t *testing.T) {
		got, err := r.List(ctx, proj, "", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d runs, want 3 (scoped to project)", len(got))
		}
		if got[0].RunID != "a2" || got[1].RunID != "b1" || got[2].RunID != "a1" {
			t.Errorf("order wrong: %s, %s, %s", got[0].RunID, got[1].RunID, got[2].RunID)
		}
	})

	t.Run("filter by datasource", func(t *testing.T) {
		got, err := r.List(ctx, proj, "wh_a", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d, want 2 for wh_a", len(got))
		}
		for _, run := range got {
			if run.DatasourceID != "wh_a" {
				t.Errorf("leaked datasource %q", run.DatasourceID)
			}
		}
	})

	t.Run("limit clamps the result set", func(t *testing.T) {
		got, err := r.List(ctx, proj, "", 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].RunID != "a2" {
			t.Fatalf("limit=1 should yield just the newest, got %+v", got)
		}
	})

	t.Run("unknown datasource yields empty non-nil slice", func(t *testing.T) {
		got, err := r.List(ctx, proj, "nope", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("expected empty non-nil slice, got %#v", got)
		}
	})
}

func TestInteg_SchemaIndexRuns_ListValidation(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(testDB)
	if _, err := r.List(ctx, "", "", 0); err == nil {
		t.Error("List with empty projectID should error")
	}
	if _, err := r.LatestByDatasource(ctx, ""); err == nil {
		t.Error("LatestByDatasource with empty projectID should error")
	}
}

// LatestByDatasource must return exactly one (newest) run per datasource, even
// when older runs for a datasource outnumber the newest of another — the
// project-page roll-up depends on no datasource being dropped.
func TestInteg_SchemaIndexRuns_LatestByDatasource(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaIndexRunRepository(testDB)
	proj := "proj-runs-integ-latest"
	t.Cleanup(func() {
		_, _ = testDB.Collection("project_schema_index_runs").DeleteMany(ctx, bson.M{"project_id": proj})
	})

	base := time.Now().UTC().Add(-time.Hour)
	// wh_a: three runs; wh_b: one older run. The roll-up must still surface wh_b.
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_a", RunID: "a1", Status: models.SchemaIndexStatusReady, ObjectsIndexed: 10, FinishedAt: base.Add(1 * time.Minute)})
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_a", RunID: "a2", Status: models.SchemaIndexStatusReady, ObjectsIndexed: 20, FinishedAt: base.Add(5 * time.Minute)})
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_a", RunID: "a3", Status: models.SchemaIndexStatusFailed, Error: "x", FinishedAt: base.Add(9 * time.Minute)})
	insertRun(t, ctx, models.SchemaIndexRun{ProjectID: proj, DatasourceID: "wh_b", RunID: "b1", Status: models.SchemaIndexStatusReady, ObjectsIndexed: 99, FinishedAt: base.Add(2 * time.Minute)})

	got, err := r.LatestByDatasource(ctx, proj)
	if err != nil {
		t.Fatalf("LatestByDatasource: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want one per datasource (2): %+v", len(got), got)
	}
	byDS := map[string]models.SchemaIndexRun{}
	for _, run := range got {
		byDS[run.DatasourceID] = run
	}
	if byDS["wh_a"].RunID != "a3" {
		t.Errorf("wh_a latest = %q, want a3 (newest finished_at)", byDS["wh_a"].RunID)
	}
	if byDS["wh_b"].RunID != "b1" || byDS["wh_b"].ObjectsIndexed != 99 {
		t.Errorf("wh_b latest = %+v, want b1/99", byDS["wh_b"])
	}
	// Output ordered newest finished_at first → wh_a (a3) before wh_b (b1).
	if got[0].DatasourceID != "wh_a" {
		t.Errorf("order: got[0] = %q, want wh_a first", got[0].DatasourceID)
	}
}
