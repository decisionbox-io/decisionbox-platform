package discovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Unit tests for SchemaIndexer.resolveSchemas — the cache-aware branch
// of the discover_schemas phase. Extracted from BuildIndex so it can be
// exercised without a live Qdrant.
//
// Covers: nil cache, empty hash disables cache, cache hit, cache miss
// → save, Find error → fallthrough, Save error → run continues,
// DiscoverSchemas error surfaces, zero-schema discovery skips save.

// --- stubs ------------------------------------------------------------

type stubSchemaSource struct {
	result    map[string]models.TableSchema
	err       error
	callCount int32
}

func (s *stubSchemaSource) DiscoverSchemas(_ context.Context) (map[string]models.TableSchema, error) {
	atomic.AddInt32(&s.callCount, 1)
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func (s *stubSchemaSource) calls() int32 {
	return atomic.LoadInt32(&s.callCount)
}

type stubCache struct {
	hit      map[string]models.TableSchema
	findErr  error
	saveErr  error
	finds    int32
	saves    int32
	savedKey struct {
		projectID, hash string
		schemas         map[string]models.TableSchema
	}
}

func (c *stubCache) Find(_ context.Context, projectID, hash string) (map[string]models.TableSchema, error) {
	atomic.AddInt32(&c.finds, 1)
	if c.findErr != nil {
		return nil, c.findErr
	}
	if c.hit == nil {
		return nil, nil
	}
	// Return a shallow copy so the caller can't mutate the stored hit
	// accidentally between test cases.
	out := make(map[string]models.TableSchema, len(c.hit))
	for k, v := range c.hit {
		out[k] = v
	}
	return out, nil
}

func (c *stubCache) Save(_ context.Context, projectID, hash string, schemas map[string]models.TableSchema) error {
	atomic.AddInt32(&c.saves, 1)
	c.savedKey.projectID = projectID
	c.savedKey.hash = hash
	c.savedKey.schemas = schemas
	return c.saveErr
}

func fakeSchemas() map[string]models.TableSchema {
	return map[string]models.TableSchema{
		"dbo.orders": {
			TableName:    "dbo.orders",
			RowCount:     42,
			Columns:      []models.ColumnInfo{{Name: "id", Type: "INTEGER"}},
			DiscoveredAt: time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		},
	}
}

// --- tests ------------------------------------------------------------

func TestResolveSchemas_NoCache_RunsDiscovery(t *testing.T) {
	src := &stubSchemaSource{result: fakeSchemas()}
	si := &SchemaIndexer{Discovery: src}

	got, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fromCache {
		t.Error("fromCache should be false when Cache is nil")
	}
	if len(got) != 1 {
		t.Errorf("got %d schemas, want 1", len(got))
	}
	if src.calls() != 1 {
		t.Errorf("DiscoverSchemas calls = %d, want 1", src.calls())
	}
}

func TestResolveSchemas_EmptyHashDisablesCache(t *testing.T) {
	// Cache wired but no hash → behave exactly like nil cache. Guards
	// against a boot path that wires the cache but fails to compute
	// the hash; we MUST NOT query the cache with an empty hash.
	src := &stubSchemaSource{result: fakeSchemas()}
	cache := &stubCache{hit: fakeSchemas()} // would hit if consulted
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: ""}

	_, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fromCache {
		t.Error("fromCache should be false when WarehouseHash is empty")
	}
	if cache.finds != 0 {
		t.Errorf("cache.Find called %d times with empty hash; must be 0", cache.finds)
	}
	if cache.saves != 0 {
		t.Errorf("cache.Save called %d times with empty hash; must be 0", cache.saves)
	}
	if src.calls() != 1 {
		t.Errorf("DiscoverSchemas calls = %d, want 1", src.calls())
	}
}

func TestResolveSchemas_CacheHit_SkipsDiscovery(t *testing.T) {
	src := &stubSchemaSource{result: fakeSchemas()}
	cache := &stubCache{hit: fakeSchemas()}
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-a"}

	got, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !fromCache {
		t.Error("fromCache should be true on cache hit")
	}
	if _, ok := got["dbo.orders"]; !ok {
		t.Errorf("expected hit contents, got %+v", got)
	}
	if src.calls() != 0 {
		t.Errorf("DiscoverSchemas called %d times on cache hit; must be 0", src.calls())
	}
	if cache.saves != 0 {
		t.Error("cache.Save must not be called on a hit")
	}
}

func TestResolveSchemas_CacheMiss_RunsDiscoveryAndSaves(t *testing.T) {
	src := &stubSchemaSource{result: fakeSchemas()}
	cache := &stubCache{hit: nil} // cold
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-b"}

	_, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fromCache {
		t.Error("fromCache should be false on a miss")
	}
	if src.calls() != 1 {
		t.Errorf("DiscoverSchemas calls = %d, want 1", src.calls())
	}
	if cache.saves != 1 {
		t.Errorf("cache.Save calls = %d, want 1", cache.saves)
	}
	if cache.savedKey.hash != "hash-b" {
		t.Errorf("Save called with hash=%q, want %q", cache.savedKey.hash, "hash-b")
	}
	if cache.savedKey.projectID != "p" {
		t.Errorf("Save called with projectID=%q, want %q", cache.savedKey.projectID, "p")
	}
	if _, ok := cache.savedKey.schemas["dbo.orders"]; !ok {
		t.Errorf("Save called with unexpected schemas: %+v", cache.savedKey.schemas)
	}
}

func TestResolveSchemas_FindError_FallsThroughToDiscovery(t *testing.T) {
	// Transient Mongo error on Find must NOT fail the run — we still
	// have a working Discovery path.
	src := &stubSchemaSource{result: fakeSchemas()}
	cache := &stubCache{findErr: errors.New("mongo temporarily unavailable")}
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-c"}

	got, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v (Find error must not surface)", err)
	}
	if fromCache {
		t.Error("fromCache should be false when Find returned an error")
	}
	if len(got) != 1 {
		t.Errorf("got %d schemas, want 1", len(got))
	}
	if src.calls() != 1 {
		t.Errorf("DiscoverSchemas calls = %d, want 1", src.calls())
	}
	if cache.saves != 1 {
		t.Errorf("cache.Save should still be attempted after Find error, got %d saves", cache.saves)
	}
}

func TestResolveSchemas_SaveError_RunContinues(t *testing.T) {
	// Save errors are best-effort — next run just rediscovers. The
	// current run MUST complete successfully.
	src := &stubSchemaSource{result: fakeSchemas()}
	cache := &stubCache{saveErr: errors.New("disk full")}
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-d"}

	got, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v (Save error must not surface)", err)
	}
	if fromCache {
		t.Error("fromCache should be false on a miss")
	}
	if len(got) != 1 {
		t.Errorf("got %d schemas, want 1", len(got))
	}
}

func TestResolveSchemas_DiscoveryError_Surfaces(t *testing.T) {
	// A real Discovery error is a run-killing failure — the caller
	// records it and fails the run.
	wantErr := errors.New("warehouse auth rejected")
	src := &stubSchemaSource{err: wantErr}
	cache := &stubCache{}
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-e"}

	_, _, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected discovery error to surface")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrap of %v", err, wantErr)
	}
	if cache.saves != 0 {
		t.Error("cache.Save must not be called when discovery fails")
	}
}

func TestResolveSchemas_EmptyDiscovery_SkipsSave(t *testing.T) {
	// Zero-schema discovery happens for a legitimately empty dataset
	// or a permission issue that didn't trigger a hard error. Saving
	// an empty cache entry would poison future runs; the indexer
	// leaves the cache untouched and lets the downstream "no tables
	// discovered" check fail the run.
	src := &stubSchemaSource{result: map[string]models.TableSchema{}}
	cache := &stubCache{}
	si := &SchemaIndexer{Discovery: src, Cache: cache, WarehouseHash: "hash-f"}

	got, fromCache, err := si.resolveSchemas(context.Background(), IndexOptions{ProjectID: "p"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fromCache {
		t.Error("fromCache should be false")
	}
	if len(got) != 0 {
		t.Errorf("got %d schemas, want 0", len(got))
	}
	if cache.saves != 0 {
		t.Error("cache.Save must not be called when discovery returned 0 tables")
	}
}

func TestResolveSchemas_ContextCancelled_PropagatesThroughDiscovery(t *testing.T) {
	// Cancellation during a miss must propagate — the worker relies
	// on ctx.Err to decide whether to mark the project failed or
	// orphaned-stale.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := &stubSchemaSource{err: context.Canceled}
	si := &SchemaIndexer{Discovery: src}

	_, _, err := si.resolveSchemas(ctx, IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected context cancellation to surface via discovery error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
