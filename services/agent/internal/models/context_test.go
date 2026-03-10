package models

import (
	"testing"
	"time"
)

func TestNewProjectContext(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	if ctx.ProjectID != "proj-123" {
		t.Errorf("ProjectID = %q, want %q", ctx.ProjectID, "proj-123")
	}
	if ctx.TotalDiscoveries != 0 {
		t.Errorf("TotalDiscoveries = %d, want 0", ctx.TotalDiscoveries)
	}
	if len(ctx.KnownSchemas) != 0 {
		t.Errorf("KnownSchemas should be empty")
	}
	if len(ctx.SuccessfulQueries) != 0 {
		t.Errorf("SuccessfulQueries should be empty")
	}
	if len(ctx.FailedQueries) != 0 {
		t.Errorf("FailedQueries should be empty")
	}
	if len(ctx.HistoricalPatterns) != 0 {
		t.Errorf("HistoricalPatterns should be empty")
	}
	if len(ctx.Notes) != 0 {
		t.Errorf("Notes should be empty")
	}
	if ctx.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestAddSuccessfulQuery(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	ctx.AddSuccessfulQuery(QueryHistory{
		Query:   "SELECT * FROM test",
		Purpose: "test query",
	})

	if len(ctx.SuccessfulQueries) != 1 {
		t.Fatalf("len = %d, want 1", len(ctx.SuccessfulQueries))
	}
	if !ctx.SuccessfulQueries[0].Success {
		t.Error("query should be marked as success")
	}
}

func TestAddSuccessfulQueryLimit(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	for i := 0; i < 150; i++ {
		ctx.AddSuccessfulQuery(QueryHistory{Query: "SELECT 1"})
	}

	if len(ctx.SuccessfulQueries) != 100 {
		t.Errorf("len = %d, want 100 (should trim to last 100)", len(ctx.SuccessfulQueries))
	}
}

func TestAddFailedQuery(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	ctx.AddFailedQuery(QueryHistory{
		Query: "SELECT invalid",
		Error: "syntax error",
	})

	if len(ctx.FailedQueries) != 1 {
		t.Fatalf("len = %d, want 1", len(ctx.FailedQueries))
	}
	if ctx.FailedQueries[0].Success {
		t.Error("query should be marked as failure")
	}
}

func TestAddFailedQueryLimit(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	for i := 0; i < 100; i++ {
		ctx.AddFailedQuery(QueryHistory{Query: "SELECT invalid"})
	}

	if len(ctx.FailedQueries) != 50 {
		t.Errorf("len = %d, want 50 (should trim to last 50)", len(ctx.FailedQueries))
	}
}

func TestAddNote(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	ctx.AddNote("schema", "sessions table has 1M rows", 0.8)

	if len(ctx.Notes) != 1 {
		t.Fatalf("len = %d, want 1", len(ctx.Notes))
	}
	if ctx.Notes[0].Category != "schema" {
		t.Errorf("category = %q, want %q", ctx.Notes[0].Category, "schema")
	}
	if ctx.Notes[0].Relevance != 0.8 {
		t.Errorf("relevance = %f, want 0.8", ctx.Notes[0].Relevance)
	}
}

func TestAddNoteLimit(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	for i := 0; i < 250; i++ {
		ctx.AddNote("test", "note", 0.5)
	}

	if len(ctx.Notes) != 200 {
		t.Errorf("len = %d, want 200 (should trim to last 200)", len(ctx.Notes))
	}
}

func TestRecordDiscoverySuccess(t *testing.T) {
	ctx := NewProjectContext("proj-123")
	ctx.ConsecutiveFailures = 3

	ctx.RecordDiscovery(true)

	if ctx.TotalDiscoveries != 1 {
		t.Errorf("TotalDiscoveries = %d, want 1", ctx.TotalDiscoveries)
	}
	if ctx.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 (should reset on success)", ctx.ConsecutiveFailures)
	}
	if ctx.LastDiscoveryDate.IsZero() {
		t.Error("LastDiscoveryDate should be set")
	}
}

func TestRecordDiscoveryFailure(t *testing.T) {
	ctx := NewProjectContext("proj-123")

	ctx.RecordDiscovery(false)
	ctx.RecordDiscovery(false)

	if ctx.TotalDiscoveries != 2 {
		t.Errorf("TotalDiscoveries = %d, want 2", ctx.TotalDiscoveries)
	}
	if ctx.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", ctx.ConsecutiveFailures)
	}
}

func TestUpdatedAtChanges(t *testing.T) {
	ctx := NewProjectContext("proj-123")
	initial := ctx.UpdatedAt

	time.Sleep(time.Millisecond)
	ctx.AddNote("test", "note", 0.5)

	if !ctx.UpdatedAt.After(initial) {
		t.Error("UpdatedAt should be updated after AddNote")
	}
}
