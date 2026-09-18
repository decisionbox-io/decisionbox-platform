//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// SchemaEditRepository is the append-only audit trail of manual schema edits.
// These tests exercise Record, List (filter/order/limit), and CountSince
// against a real Mongo via the shared testDB fixture.
func TestInteg_SchemaEdits_RecordListCountSince(t *testing.T) {
	ctx := context.Background()
	r := NewSchemaEditRepository(testDB)
	proj := "proj-edits-integ-1"
	t.Cleanup(func() {
		_, _ = testDB.Collection("project_schema_edits").
			DeleteMany(ctx, bson.M{"project_id": bson.M{"$in": []string{proj, "other"}}})
	})

	base := time.Now().UTC().Add(-time.Hour)
	mustRecord := func(ds, table, action string, at time.Time) {
		t.Helper()
		if err := r.Record(ctx, models.SchemaEdit{
			ProjectID: proj, DatasourceID: ds, Table: table, Action: action,
			Before: "old", After: "new", Actor: "jale@decisionbox.io", At: at,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	mustRecord("default", "dbo.orders", models.SchemaEditActionBlurb, base.Add(1*time.Minute))
	mustRecord("default", "dbo.orders", models.SchemaEditActionColumns, base.Add(3*time.Minute))
	mustRecord("wh_b", "dbo.sales", models.SchemaEditActionDelete, base.Add(2*time.Minute))
	// A different project's edit must never leak in.
	if err := r.Record(ctx, models.SchemaEdit{ProjectID: "other", Table: "x", Action: models.SchemaEditActionBlurb, At: base}); err != nil {
		t.Fatalf("Record other: %v", err)
	}

	t.Run("list all, newest first, scoped to project", func(t *testing.T) {
		got, err := r.List(ctx, proj, "", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d edits, want 3", len(got))
		}
		if got[0].Action != models.SchemaEditActionColumns || got[2].Action != models.SchemaEditActionBlurb {
			t.Errorf("order wrong: %s ... %s", got[0].Action, got[2].Action)
		}
		if got[0].ID == "" {
			t.Error("expected Mongo-assigned _id on read")
		}
	})

	t.Run("filter by datasource", func(t *testing.T) {
		got, err := r.List(ctx, proj, "wh_b", 0)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].Table != "dbo.sales" {
			t.Fatalf("datasource filter wrong: %+v", got)
		}
	})

	t.Run("limit clamps", func(t *testing.T) {
		got, err := r.List(ctx, proj, "", 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("limit=1 returned %d", len(got))
		}
	})

	t.Run("count since a cutoff", func(t *testing.T) {
		// After base+2m30s → only the base+3m columns edit qualifies.
		n, err := r.CountSince(ctx, proj, "", base.Add(150*time.Second))
		if err != nil {
			t.Fatalf("CountSince: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountSince mid = %d, want 1", n)
		}
		// Zero time counts them all (project never indexed).
		all, err := r.CountSince(ctx, proj, "", time.Time{})
		if err != nil {
			t.Fatalf("CountSince zero: %v", err)
		}
		if all != 3 {
			t.Fatalf("CountSince zero = %d, want 3", all)
		}
	})

	t.Run("count scoped to a datasource", func(t *testing.T) {
		// wh_b has one edit (the table_delete); the default warehouse has two.
		n, err := r.CountSince(ctx, proj, "wh_b", time.Time{})
		if err != nil {
			t.Fatalf("CountSince datasource: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountSince wh_b = %d, want 1", n)
		}
		def, err := r.CountSince(ctx, proj, "default", time.Time{})
		if err != nil {
			t.Fatalf("CountSince default: %v", err)
		}
		if def != 2 {
			t.Fatalf("CountSince default = %d, want 2", def)
		}
	})

	t.Run("count restricted to actions", func(t *testing.T) {
		// Two blurb/keyword-class actions exist project-wide: a blurb_edit and a
		// columns_edit — restrict to blurb only → 1.
		n, err := r.CountSince(ctx, proj, "", time.Time{}, models.SchemaEditActionBlurb)
		if err != nil {
			t.Fatalf("CountSince actions: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountSince blurb-only = %d, want 1", n)
		}
	})
}
