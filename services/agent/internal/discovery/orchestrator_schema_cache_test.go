package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Unit tests for Orchestrator.discoverSchemas — the single entry point
// the run loop uses to obtain the schemas map. Critically, these guard
// the design rule that there is NO live-warehouse fallback during a
// discovery run: a missing or empty schema cache MUST surface as a hard
// error so the user reaches for /reindex rather than silently waiting
// the ~50 minutes a 1,400-table re-discovery would take.

func TestOrchestrator_discoverSchemas_CacheHit(t *testing.T) {
	cache := &stubCache{hit: fakeSchemas()}
	o := &Orchestrator{
		projectID:     "proj-1",
		schemaCache:   cache,
		warehouseHash: "hash-abc",
	}

	got, err := o.discoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("discoverSchemas: %v", err)
	}
	if len(got) != len(fakeSchemas()) {
		t.Errorf("schema count = %d, want %d", len(got), len(fakeSchemas()))
	}
	if cache.finds != 1 {
		t.Errorf("cache.Find called %d times, want 1", cache.finds)
	}
}

func TestOrchestrator_discoverSchemas_CacheMiss_Errors(t *testing.T) {
	// Empty cache (Find returns nil schemas, no error). Per the new
	// contract this is an upstream invariant violation — discovery
	// should not silently fall back to live warehouse re-discovery.
	cache := &stubCache{hit: nil}
	o := &Orchestrator{
		projectID:     "proj-needs-reindex",
		schemaCache:   cache,
		warehouseHash: "hash-abc",
	}

	_, err := o.discoverSchemas(context.Background())
	if err == nil {
		t.Fatal("discoverSchemas with empty cache should error, got nil")
	}
	// The error must point the user at the recovery path. If this
	// message changes, the dashboard / docs may also need updating.
	if !strings.Contains(err.Error(), "/reindex") {
		t.Errorf("error should reference /reindex recovery path, got: %v", err)
	}
}

func TestOrchestrator_discoverSchemas_FindError_Surfaced(t *testing.T) {
	cache := &stubCache{findErr: errors.New("mongo down")}
	o := &Orchestrator{
		projectID:     "proj-1",
		schemaCache:   cache,
		warehouseHash: "hash-abc",
	}

	_, err := o.discoverSchemas(context.Background())
	if err == nil {
		t.Fatal("expected error when cache.Find fails")
	}
	if !strings.Contains(err.Error(), "mongo down") {
		t.Errorf("underlying error should be wrapped, got: %v", err)
	}
}

func TestOrchestrator_discoverSchemas_NilCache_Errors(t *testing.T) {
	// schemaCache nil means agentserver wiring is broken — fail fast
	// with a programmer-error message rather than panicking deep in
	// the run loop.
	o := &Orchestrator{
		projectID:     "proj-1",
		schemaCache:   nil,
		warehouseHash: "hash-abc",
	}

	_, err := o.discoverSchemas(context.Background())
	if err == nil {
		t.Fatal("expected error when schemaCache is nil")
	}
	if !strings.Contains(err.Error(), "programmer error") {
		t.Errorf("nil-cache should surface as programmer error, got: %v", err)
	}
}

func TestOrchestrator_discoverSchemas_EmptyHash_Errors(t *testing.T) {
	// Empty warehouse hash means agentserver forgot to compute it.
	// We refuse rather than passing "" to the cache (which would match
	// schemas indexed before the hashing scheme existed, if any).
	cache := &stubCache{hit: fakeSchemas()}
	o := &Orchestrator{
		projectID:     "proj-1",
		schemaCache:   cache,
		warehouseHash: "",
	}

	_, err := o.discoverSchemas(context.Background())
	if err == nil {
		t.Fatal("expected error when warehouseHash is empty")
	}
	if cache.finds != 0 {
		t.Errorf("cache.Find should not be called with empty hash, got %d calls", cache.finds)
	}
}
