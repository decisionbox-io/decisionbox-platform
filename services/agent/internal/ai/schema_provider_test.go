package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSchemaProvider is a scripted SchemaProvider for engine tests.
// Values are returned in the order set by the test. Lookup mirrors
// requested refs by default; Search returns canned hits or errors.
type fakeSchemaProvider struct {
	// Per-call hooks that override the default echo behaviour.
	lookupFn func(ctx context.Context, refs []string) (LookupResult, error)
	searchFn func(ctx context.Context, query string, k int) ([]SearchHit, error)

	lookupCalls int
	searchCalls int
}

func (f *fakeSchemaProvider) Lookup(ctx context.Context, refs []string) (LookupResult, error) {
	f.lookupCalls++
	if f.lookupFn != nil {
		return f.lookupFn(ctx, refs)
	}
	tables := make([]LookupTable, 0, len(refs))
	for _, r := range refs {
		tables = append(tables, LookupTable{
			Table: r,
			Columns: []LookupColumn{
				{Name: "id", Type: "INT64", Nullable: false, Category: "primary_key"},
			},
			RowCount: 100,
		})
	}
	return LookupResult{Tables: tables}, nil
}

func (f *fakeSchemaProvider) Search(ctx context.Context, query string, k int) ([]SearchHit, error) {
	f.searchCalls++
	if f.searchFn != nil {
		return f.searchFn(ctx, query, k)
	}
	return []SearchHit{
		{Table: "ds.users", Blurb: "core users table", RowCount: 1_000, Score: 0.92},
		{Table: "ds.orders", Blurb: "order events", RowCount: 5_000, Score: 0.81},
	}, nil
}

// ---- Constants ----

func TestSchemaProvider_Constants(t *testing.T) {
	// These constants are part of the prompt contract — domain pack
	// prompts state the same numbers verbatim. If you change either,
	// you MUST update every domain-pack exploration prompt and the
	// docs/architecture/agent-on-demand-schema.md page in the same
	// commit.
	if MaxLookupTablesPerCall != 10 {
		t.Errorf("MaxLookupTablesPerCall = %d, want 10 (prompt contract)", MaxLookupTablesPerCall)
	}
	if DefaultMaxLookupsPerRun != 30 {
		t.Errorf("DefaultMaxLookupsPerRun = %d, want 30 (prompt contract)", DefaultMaxLookupsPerRun)
	}
	if DefaultMaxSearchesPerRun != 30 {
		t.Errorf("DefaultMaxSearchesPerRun = %d, want 30 (prompt contract)", DefaultMaxSearchesPerRun)
	}
	if DefaultSearchTopK != 10 {
		t.Errorf("DefaultSearchTopK = %d, want 10", DefaultSearchTopK)
	}
	if MaxSearchTopK != 30 {
		t.Errorf("MaxSearchTopK = %d, want 30", MaxSearchTopK)
	}
}

// ---- Fake provider sanity (so engine tests trust the fake) ----

func TestFakeSchemaProvider_Lookup_DefaultEcho(t *testing.T) {
	p := &fakeSchemaProvider{}
	res, err := p.Lookup(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("wanted 2 echoed tables, got %d", len(res.Tables))
	}
	if res.Tables[0].Table != "a" || res.Tables[1].Table != "b" {
		t.Errorf("order not preserved: %+v", res.Tables)
	}
	if p.lookupCalls != 1 {
		t.Errorf("lookupCalls = %d, want 1", p.lookupCalls)
	}
}

func TestFakeSchemaProvider_Search_DefaultHits(t *testing.T) {
	p := &fakeSchemaProvider{}
	hits, err := p.Search(context.Background(), "users", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("wanted 2 default hits, got %d", len(hits))
	}
	if p.searchCalls != 1 {
		t.Error("searchCalls should bump on each call")
	}
}

func TestFakeSchemaProvider_HookOverridesDefault(t *testing.T) {
	wantErr := errors.New("provider down")
	p := &fakeSchemaProvider{
		lookupFn: func(ctx context.Context, refs []string) (LookupResult, error) {
			return LookupResult{}, wantErr
		},
	}
	_, err := p.Lookup(context.Background(), []string{"x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("hook not honoured: got %v", err)
	}
}

// ---- LookupResult / LookupTable helpers ----

func TestLookupResult_NotFound_Stringifiable(t *testing.T) {
	r := LookupResult{NotFound: []string{"db.missing", "db.gone"}}
	got := strings.Join(r.NotFound, ",")
	if got != "db.missing,db.gone" {
		t.Errorf("NotFound formatting drifted: %q", got)
	}
}
