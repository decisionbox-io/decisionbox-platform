package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeEmbedder returns a fixed-dim vector for any input. Lets cache
// provider tests exercise Search without a real embedding API.
type fakeEmbedder struct {
	dim   int
	err   error
	calls int
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float64, len(texts))
	for i := range texts {
		v := make([]float64, f.dim)
		for j := range v {
			v[j] = 0.1
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) Dimensions() int   { return f.dim }
func (f *fakeEmbedder) ModelName() string { return "fake" }

// emptyEmbedder simulates an embedder that returns zero vectors. Used to
// exercise the "embedder returned no vectors" guard inside Search.
type emptyEmbedder struct{}

func (e *emptyEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	return [][]float64{}, nil
}
func (e *emptyEmbedder) Dimensions() int   { return 0 }
func (e *emptyEmbedder) ModelName() string { return "empty" }

// fakeVectorSearcher implements the package-private vectorSearcher
// interface. Used to exercise the Search code path without standing up a
// real Qdrant container — that integration is covered by the schema_retrieve
// integration test.
type fakeVectorSearcher struct {
	hits []schema_retrieve.Hit
	err  error

	gotProjectID string
	gotVec       []float64
	gotOpts      schema_retrieve.SearchOpts
	calls        int
}

func (f *fakeVectorSearcher) Search(ctx context.Context, projectID string, vec []float64, opts schema_retrieve.SearchOpts) ([]schema_retrieve.Hit, error) {
	f.calls++
	f.gotProjectID = projectID
	f.gotVec = vec
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func sampleSchemas() map[string]models.TableSchema {
	return map[string]models.TableSchema{
		"events.users": {
			TableName: "events.users",
			RowCount:  1000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "INT64", Category: "primary_key"},
				{Name: "email", Type: "STRING", Nullable: true},
			},
			SampleData: []map[string]interface{}{
				{"user_id": 1, "email": "a@example.com"},
				{"user_id": 2, "email": "b@example.com"},
			},
		},
		"events.orders": {
			TableName: "events.orders",
			RowCount:  5000,
			Columns: []models.ColumnInfo{
				{Name: "order_id", Type: "INT64", Category: "primary_key"},
				{Name: "user_id", Type: "INT64"},
				{Name: "total", Type: "DECIMAL", Category: "metric"},
			},
		},
		"events.tracking": { // unique bare name
			TableName: "events.tracking",
			RowCount:  100,
			Columns:   []models.ColumnInfo{{Name: "id"}},
		},
		"warehouse.tracking": { // duplicate bare name → ambiguous
			TableName: "warehouse.tracking",
			RowCount:  50,
			Columns:   []models.ColumnInfo{{Name: "id"}},
		},
	}
}

// providerWithSearcher constructs a CacheSchemaProvider with a fake
// vectorSearcher injected directly. We can't pass the fake through
// CacheSchemaProviderOptions (the public option holds a concrete
// *schema_retrieve.Retriever), so the test reaches into the same package
// to wire the unexported interface field.
func providerWithSearcher(t *testing.T, searcher vectorSearcher, embedder Embedder) *CacheSchemaProvider {
	t.Helper()
	p, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		Schemas:  sampleSchemas(),
		Embedder: embedder,
	})
	if err != nil {
		t.Fatalf("construct provider: %v", err)
	}
	p.searcher = searcher
	return p
}

// ---- Construction ----

func TestNewCacheSchemaProvider_RequiresSchemas(t *testing.T) {
	_, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{})
	if err == nil {
		t.Fatal("expected error when Schemas is nil")
	}
}

func TestNewCacheSchemaProvider_AppliesDefaults(t *testing.T) {
	p, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	if err != nil {
		t.Fatal(err)
	}
	if p.sampleLimit != defaultLookupSampleLimit {
		t.Errorf("sampleLimit = %d, want default", p.sampleLimit)
	}
	if p.columnLimit != defaultLookupColumnLimit {
		t.Errorf("columnLimit = %d, want default", p.columnLimit)
	}
	if len(p.refIndex) == 0 {
		t.Error("refIndex should be built")
	}
	if p.searcher != nil {
		t.Error("searcher should be nil when no Retriever provided")
	}
}

func TestNewCacheSchemaProvider_RespectsExplicitLimits(t *testing.T) {
	p, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		Schemas:     sampleSchemas(),
		SampleLimit: 7,
		ColumnLimit: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.sampleLimit != 7 {
		t.Errorf("sampleLimit = %d, want 7", p.sampleLimit)
	}
	if p.columnLimit != 11 {
		t.Errorf("columnLimit = %d, want 11", p.columnLimit)
	}
}

func TestNewCacheSchemaProvider_CopiesDatasets(t *testing.T) {
	src := []string{"a", "b"}
	p, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		Schemas:  sampleSchemas(),
		Datasets: src,
	})
	if err != nil {
		t.Fatal(err)
	}
	src[0] = "mutated"
	if p.datasets[0] != "a" {
		t.Errorf("datasets should be defensively copied, got %v", p.datasets)
	}
}

// ---- Lookup ----

func TestCacheSchemaProvider_Lookup_Qualified(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, err := p.Lookup(context.Background(), []string{"events.users", "events.orders"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(res.Tables))
	}
	if res.Tables[0].Table != "events.users" {
		t.Errorf("Tables[0] = %q, want events.users (order should match request)", res.Tables[0].Table)
	}
	if res.Tables[0].RowCount != 1000 {
		t.Errorf("RowCount = %d, want 1000", res.Tables[0].RowCount)
	}
	if len(res.Tables[0].SampleRows) != 2 {
		t.Errorf("SampleRows len = %d, want 2", len(res.Tables[0].SampleRows))
	}
}

func TestCacheSchemaProvider_Lookup_BareNameUnambiguous(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, err := p.Lookup(context.Background(), []string{"users"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tables) != 1 || res.Tables[0].Table != "events.users" {
		t.Errorf("bare 'users' should resolve to events.users, got %+v", res.Tables)
	}
}

func TestCacheSchemaProvider_Lookup_BareNameAmbiguous_FallsToNotFound(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"tracking"})
	if len(res.Tables) != 0 {
		t.Errorf("ambiguous bare name should not resolve, got %+v", res.Tables)
	}
	if len(res.NotFound) != 1 || res.NotFound[0] != "tracking" {
		t.Errorf("ambiguous ref should appear in NotFound, got %v", res.NotFound)
	}
}

func TestCacheSchemaProvider_Lookup_CaseInsensitiveFallback(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"EVENTS.USERS"})
	if len(res.Tables) != 1 || res.Tables[0].Table != "events.users" {
		t.Errorf("case-insensitive fallback should resolve, got tables=%+v notFound=%v", res.Tables, res.NotFound)
	}
}

func TestCacheSchemaProvider_Lookup_NotFound(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"does.not.exist"})
	if len(res.Tables) != 0 {
		t.Errorf("should resolve nothing, got %+v", res.Tables)
	}
	if len(res.NotFound) != 1 {
		t.Errorf("missing ref should land in NotFound, got %v", res.NotFound)
	}
}

func TestCacheSchemaProvider_Lookup_EmptyAndWhitespaceRefsLandInNotFound(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"", "   "})
	if len(res.Tables) != 0 {
		t.Errorf("blank refs should resolve nothing, got %+v", res.Tables)
	}
	if len(res.NotFound) != 2 {
		t.Errorf("blank refs should land in NotFound, got %v", res.NotFound)
	}
}

func TestCacheSchemaProvider_Lookup_TruncatesAtPerCallCap(t *testing.T) {
	schemas := map[string]models.TableSchema{}
	refs := make([]string, ai.MaxLookupTablesPerCall+5)
	for i := range refs {
		name := "ds.t" + string(rune('a'+i))
		schemas[name] = models.TableSchema{TableName: name, Columns: []models.ColumnInfo{{Name: "id"}}}
		refs[i] = name
	}
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: schemas})
	res, err := p.Lookup(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("Truncated flag should be set when refs exceed per-call cap")
	}
	if len(res.Tables) > ai.MaxLookupTablesPerCall {
		t.Errorf("returned %d tables, expected ≤%d", len(res.Tables), ai.MaxLookupTablesPerCall)
	}
}

func TestCacheSchemaProvider_Lookup_DedupesWithinCall(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"events.users", "events.users", "events.users"})
	if len(res.Tables) != 1 {
		t.Errorf("duplicate refs should dedup within one call, got %d", len(res.Tables))
	}
}

func TestCacheSchemaProvider_Lookup_AppliesColumnLimit(t *testing.T) {
	cols := make([]models.ColumnInfo, 10)
	for i := range cols {
		cols[i] = models.ColumnInfo{Name: "c" + string(rune('a'+i)), Type: "STRING"}
	}
	schemas := map[string]models.TableSchema{
		"ds.wide": {TableName: "ds.wide", Columns: cols},
	}
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		Schemas: schemas, ColumnLimit: 3,
	})
	res, _ := p.Lookup(context.Background(), []string{"ds.wide"})
	if len(res.Tables[0].Columns) != 3 {
		t.Errorf("columns = %d, want 3 (capped)", len(res.Tables[0].Columns))
	}
}

func TestCacheSchemaProvider_Lookup_AppliesSampleLimit(t *testing.T) {
	rows := make([]map[string]interface{}, 5)
	for i := range rows {
		rows[i] = map[string]interface{}{"id": i}
	}
	schemas := map[string]models.TableSchema{
		"ds.t": {
			TableName:  "ds.t",
			Columns:    []models.ColumnInfo{{Name: "id"}},
			SampleData: rows,
		},
	}
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		Schemas: schemas, SampleLimit: 2,
	})
	res, _ := p.Lookup(context.Background(), []string{"ds.t"})
	if len(res.Tables[0].SampleRows) != 2 {
		t.Errorf("sample rows = %d, want 2", len(res.Tables[0].SampleRows))
	}
}

func TestCacheSchemaProvider_Lookup_PreservesColumnCategoryHints(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	res, _ := p.Lookup(context.Background(), []string{"events.users"})
	if len(res.Tables) != 1 {
		t.Fatalf("got %d tables", len(res.Tables))
	}
	cols := res.Tables[0].Columns
	if len(cols) < 2 {
		t.Fatalf("got %d columns, want ≥2", len(cols))
	}
	if cols[0].Category != "primary_key" {
		t.Errorf("primary_key hint dropped: %+v", cols[0])
	}
	if !cols[1].Nullable {
		t.Errorf("nullable flag dropped: %+v", cols[1])
	}
}

func TestCacheSchemaProvider_Lookup_HonoursContextCancellation(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Lookup(ctx, []string{"events.users"})
	if err == nil {
		t.Fatal("expected error when context already cancelled")
	}
}

// ---- resolveRef edge cases (unit, internal-only) ----

func TestCacheSchemaProvider_resolveRef_TrimsWhitespace(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	if k, ok := p.resolveRef("  events.users  "); !ok || k != "events.users" {
		t.Errorf("whitespace not trimmed: got (%q,%v)", k, ok)
	}
}

func TestCacheSchemaProvider_resolveRef_EmptyString(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	if _, ok := p.resolveRef(""); ok {
		t.Error("empty ref should not resolve")
	}
}

// ---- Search ----

func TestCacheSchemaProvider_Search_NoSearcher(t *testing.T) {
	p, _ := NewCacheSchemaProvider(CacheSchemaProviderOptions{Schemas: sampleSchemas()})
	_, err := p.Search(context.Background(), "users", 5)
	if err == nil {
		t.Fatal("expected error when no retriever is configured")
	}
}

func TestCacheSchemaProvider_Search_EmptyQuery(t *testing.T) {
	p := providerWithSearcher(t, &fakeVectorSearcher{}, &fakeEmbedder{dim: 4})
	_, err := p.Search(context.Background(), "   ", 5)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestCacheSchemaProvider_Search_EmbedderError(t *testing.T) {
	p := providerWithSearcher(t, &fakeVectorSearcher{}, &fakeEmbedder{dim: 4, err: errors.New("api down")})
	_, err := p.Search(context.Background(), "users", 5)
	if err == nil {
		t.Fatal("expected error when embedder fails")
	}
}

func TestCacheSchemaProvider_Search_EmbedderReturnsNoVectors(t *testing.T) {
	p := providerWithSearcher(t, &fakeVectorSearcher{}, &emptyEmbedder{})
	_, err := p.Search(context.Background(), "users", 5)
	if err == nil {
		t.Fatal("expected error when embedder returns no vectors")
	}
}

func TestCacheSchemaProvider_Search_SearcherError(t *testing.T) {
	searcher := &fakeVectorSearcher{err: errors.New("qdrant down")}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	_, err := p.Search(context.Background(), "users", 5)
	if err == nil {
		t.Fatal("expected error when searcher fails")
	}
}

func TestCacheSchemaProvider_Search_ReturnsHits(t *testing.T) {
	searcher := &fakeVectorSearcher{
		hits: []schema_retrieve.Hit{
			{Blurb: schema_retrieve.TableBlurb{Table: "events.users", Blurb: "core users", RowCount: 1000}, Score: 0.95},
			{Blurb: schema_retrieve.TableBlurb{Table: "events.orders", Blurb: "orders log", RowCount: 5000}, Score: 0.81},
		},
	}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	p.projectID = "proj-xyz"

	out, err := p.Search(context.Background(), "  users  ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d hits, want 2", len(out))
	}
	if out[0].Table != "events.users" || out[0].Blurb != "core users" || out[0].RowCount != 1000 || out[0].Score != 0.95 {
		t.Errorf("hit[0] mismatch: %+v", out[0])
	}
	if searcher.gotProjectID != "proj-xyz" {
		t.Errorf("projectID forwarded as %q, want proj-xyz", searcher.gotProjectID)
	}
	if searcher.gotOpts.TopK != 5 {
		t.Errorf("topK forwarded as %d, want 5", searcher.gotOpts.TopK)
	}
	if searcher.gotOpts.RowCountPrior == 0 {
		t.Errorf("RowCountPrior should be set, got %f", searcher.gotOpts.RowCountPrior)
	}
}

func TestCacheSchemaProvider_Search_DefaultsTopKWhenZero(t *testing.T) {
	searcher := &fakeVectorSearcher{}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	if _, err := p.Search(context.Background(), "users", 0); err != nil {
		t.Fatal(err)
	}
	if searcher.gotOpts.TopK != ai.DefaultSearchTopK {
		t.Errorf("topK = %d, want default %d", searcher.gotOpts.TopK, ai.DefaultSearchTopK)
	}
}

func TestCacheSchemaProvider_Search_ClampsTopKAtMax(t *testing.T) {
	searcher := &fakeVectorSearcher{}
	p := providerWithSearcher(t, searcher, &fakeEmbedder{dim: 4})
	if _, err := p.Search(context.Background(), "users", ai.MaxSearchTopK*10); err != nil {
		t.Fatal(err)
	}
	if searcher.gotOpts.TopK != ai.MaxSearchTopK {
		t.Errorf("topK = %d, want clamped to %d", searcher.gotOpts.TopK, ai.MaxSearchTopK)
	}
}

func TestCacheSchemaProvider_Search_ForwardsEmbedderVector(t *testing.T) {
	searcher := &fakeVectorSearcher{}
	embedder := &fakeEmbedder{dim: 7}
	p := providerWithSearcher(t, searcher, embedder)
	if _, err := p.Search(context.Background(), "users", 3); err != nil {
		t.Fatal(err)
	}
	if len(searcher.gotVec) != 7 {
		t.Errorf("vector len = %d, want 7", len(searcher.gotVec))
	}
	if embedder.calls != 1 {
		t.Errorf("embedder calls = %d, want 1", embedder.calls)
	}
}

// ---- buildRefIndex (internal, but its rules drive Lookup correctness) ----

func TestBuildRefIndex_SkipsAmbiguousBareNames(t *testing.T) {
	idx := buildRefIndex(sampleSchemas())
	// "tracking" exists twice — must NOT appear in the index, otherwise
	// Lookup would silently pick one dataset.
	if _, ok := idx["tracking"]; ok {
		t.Errorf("ambiguous bare 'tracking' should be skipped, idx[%q]=%q", "tracking", idx["tracking"])
	}
	// "users" is unique — must appear.
	if got := idx["users"]; got != "events.users" {
		t.Errorf("idx[users] = %q, want events.users", got)
	}
}

func TestBuildRefIndex_QualifiedAlwaysIndexes(t *testing.T) {
	idx := buildRefIndex(sampleSchemas())
	for k := range sampleSchemas() {
		if got := idx[k]; got != k {
			t.Errorf("qualified %q should map to itself, got %q", k, got)
		}
	}
}

func TestBuildRefIndex_HandlesKeysWithoutDot(t *testing.T) {
	schemas := map[string]models.TableSchema{
		"singleton": {TableName: "singleton", Columns: []models.ColumnInfo{{Name: "id"}}},
	}
	idx := buildRefIndex(schemas)
	if got := idx["singleton"]; got != "singleton" {
		t.Errorf("dot-less key should index to itself, got %q", got)
	}
}
