//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
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
		_, _ = testDB.Collection("project_schema_index_runs").DeleteMany(ctx, map[string]string{"project_id": proj})
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
}
