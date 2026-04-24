package database

import (
	"context"
	"testing"
)

// Unit tests for validation branches that don't need Mongo. Full behaviour
// (upsert, $inc atomicity, ClaimNextPendingIndex FIFO, phase gating) is
// covered by integration tests in schema_index_progress_integration_test.go.

func TestSchemaIndexProgress_Validation_EmptyProjectID(t *testing.T) {
	r := &SchemaIndexProgressRepository{} // col is unused because we return before touching it
	ctx := context.Background()

	cases := []struct {
		name string
		run  func() error
	}{
		{"Reset", func() error { return r.Reset(ctx, "", "run-1") }},
		{"SetPhase", func() error { return r.SetPhase(ctx, "", "listing_tables") }},
		{"UpdateTables", func() error { return r.UpdateTables(ctx, "", 10, 5) }},
		{"IncrementDone", func() error { return r.IncrementDone(ctx, "", 1) }},
		{"RecordError", func() error { return r.RecordError(ctx, "", "boom") }},
		{"Get", func() error { _, err := r.Get(ctx, ""); return err }},
		{"Delete", func() error { return r.Delete(ctx, "") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			if err == nil {
				t.Fatalf("%s with empty projectID should error", c.name)
			}
		})
	}
}

func TestSchemaIndexProgress_SetPhase_InvalidPhase(t *testing.T) {
	r := &SchemaIndexProgressRepository{}
	err := r.SetPhase(context.Background(), "proj-1", "bogus-phase")
	if err == nil {
		t.Fatal("invalid phase should error")
	}
}

func TestSchemaIndexProgress_UpdateTables_NegativeValues(t *testing.T) {
	r := &SchemaIndexProgressRepository{}
	if err := r.UpdateTables(context.Background(), "p", -1, 0); err == nil {
		t.Error("negative total should error")
	}
	if err := r.UpdateTables(context.Background(), "p", 10, -1); err == nil {
		t.Error("negative done should error")
	}
}

func TestSchemaIndexProgress_IncrementDone_ZeroOrNegative_IsNoop(t *testing.T) {
	r := &SchemaIndexProgressRepository{}
	// Zero delta: returns nil without touching Mongo.
	if err := r.IncrementDone(context.Background(), "p", 0); err != nil {
		t.Errorf("zero delta should be no-op, got %v", err)
	}
	if err := r.IncrementDone(context.Background(), "p", -5); err != nil {
		t.Errorf("negative delta should be no-op, got %v", err)
	}
}

func TestIsValidSchemaIndexPhase(t *testing.T) {
	valid := []string{"listing_tables", "describing_tables", "embedding"}
	for _, v := range valid {
		if !isValidSchemaIndexPhase(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"", "done", "unknown", "LISTING_TABLES"} {
		if isValidSchemaIndexPhase(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

func TestIsValidSchemaIndexStatus(t *testing.T) {
	valid := []string{"pending_indexing", "indexing", "ready", "failed"}
	for _, v := range valid {
		if !isValidSchemaIndexStatus(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"", "done", "Ready", "PENDING"} {
		if isValidSchemaIndexStatus(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}
