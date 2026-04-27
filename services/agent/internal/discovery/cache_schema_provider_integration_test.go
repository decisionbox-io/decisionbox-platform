//go:build integration

package discovery

import (
	"context"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestInteg_CacheSchemaProvider_SearchAgainstRealQdrant exercises the
// production wiring of CacheSchemaProvider.Search against a real Qdrant
// container (started via the existing startQdrant helper in
// schema_indexer_integration_test.go). The unit tests in
// cache_schema_provider_test.go cover Search behaviour via a fake
// vectorSearcher; this test verifies that a *schema_retrieve.Retriever
// satisfies the interface when wired through the public constructor and
// that hits flow back as ai.SearchHit values.
func TestInteg_CacheSchemaProvider_SearchAgainstRealQdrant(t *testing.T) {
	ctx := context.Background()
	retriever := startQdrant(t)

	const projectID = "integ-cache-search-1"

	schemas := map[string]models.TableSchema{
		"sales.orders": {
			TableName: "sales.orders",
			RowCount:  1_000_000,
			Columns: []models.ColumnInfo{
				{Name: "order_id", Type: "INT64", Category: "primary_key"},
				{Name: "customer_id", Type: "INT64"},
			},
			KeyColumns: []string{"order_id", "customer_id"},
		},
		"sales.users": {
			TableName: "sales.users",
			RowCount:  50_000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "INT64", Category: "primary_key"},
			},
			KeyColumns: []string{"user_id"},
		},
	}

	// Index the fixtures into Qdrant via the existing indexer pipeline.
	// stubLLM + stubEmbedder produce deterministic output so the test
	// depends on neither network nor billed APIs.
	llm := &stubLLM{text: "Indexed table description for search test."}
	gen, err := blurb.New(blurb.Config{LLM: llm, Model: "stub-blurb", Workers: 2})
	if err != nil {
		t.Fatalf("blurb.New: %v", err)
	}
	emb := &stubEmbedder{dim: 3, model: "stub-embedder"}

	si := SchemaIndexer{
		Discovery: &stubIntegSchemaSource{schemas: schemas},
		Blurber:   gen,
		Embedder:  emb,
		Retriever: retriever,
		Progress:  &memProgress{},
	}
	if _, err := si.BuildIndex(ctx, IndexOptions{
		ProjectID:       projectID,
		RunID:           "integ-run-1",
		BlurbModelLabel: "stub/blurb",
		DomainBlurb:     "Sales warehouse fixtures.",
		Keywords:        []string{"sales"},
	}); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// Construct the production CacheSchemaProvider against the real
	// retriever — this exercises the same code path the orchestrator
	// uses at run start.
	provider, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID: projectID,
		Schemas:   schemas,
		Retriever: retriever,
		Embedder:  emb,
	})
	if err != nil {
		t.Fatalf("NewCacheSchemaProvider: %v", err)
	}

	hits, err := provider.Search(ctx, "sales orders by customer", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected ≥1 hit from indexed fixtures, got 0")
	}

	// Hits should reference one of the fixture tables and carry the
	// payload metadata the indexer wrote (row count, blurb).
	tables := make(map[string]bool, len(hits))
	for _, h := range hits {
		tables[h.Table] = true
		if h.Blurb == "" {
			t.Errorf("blurb empty on hit %+v", h)
		}
		if h.RowCount <= 0 {
			t.Errorf("row count not propagated: %+v", h)
		}
	}
	if !tables["sales.orders"] && !tables["sales.users"] {
		// stubEmbedder collapses every text to the same vector, so any
		// hit qualifies as long as it's one of the fixture tables.
		t.Errorf("hits referenced unknown tables: %v", tables)
	}
}

// TestInteg_CacheSchemaProvider_LookupNoQdrantNeeded confirms Lookup
// works even when Qdrant is configured — the path is in-memory only and
// must NOT make a network call. We assert by exercising it after
// indexing to ensure the schemas map (not the vector index) is the
// source of truth for L1 detail.
func TestInteg_CacheSchemaProvider_LookupNoQdrantNeeded(t *testing.T) {
	ctx := context.Background()
	retriever := startQdrant(t)

	schemas := map[string]models.TableSchema{
		"warehouse.users": {
			TableName: "warehouse.users",
			RowCount:  9999,
			Columns: []models.ColumnInfo{
				{Name: "id", Type: "INT64", Category: "primary_key"},
				{Name: "email", Type: "STRING", Nullable: true},
			},
			SampleData: []map[string]interface{}{
				{"id": 1, "email": "a@example.com"},
			},
		},
	}

	provider, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID: "lookup-test",
		Schemas:   schemas,
		Retriever: retriever,
		Embedder:  &stubEmbedder{dim: 3, model: "stub"},
	})
	if err != nil {
		t.Fatalf("NewCacheSchemaProvider: %v", err)
	}

	res, err := provider.Lookup(ctx, []string{"warehouse.users"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(res.Tables) != 1 || res.Tables[0].Table != "warehouse.users" {
		t.Fatalf("Lookup returned wrong table: %+v", res.Tables)
	}
	if res.Tables[0].RowCount != 9999 {
		t.Errorf("row count not propagated, got %d", res.Tables[0].RowCount)
	}
	if len(res.Tables[0].Columns) != 2 {
		t.Errorf("columns not propagated: %+v", res.Tables[0].Columns)
	}
	if len(res.Tables[0].SampleRows) != 1 {
		t.Errorf("samples not propagated: %+v", res.Tables[0].SampleRows)
	}

	// Sanity: NotFound stays empty for a known table; ambiguous /
	// missing refs land in NotFound. We re-verify both branches in the
	// integration setup so a regression in the index-aware constructor
	// can't quietly mask the unit-test guarantees.
	res, _ = provider.Lookup(ctx, []string{"warehouse.users", "missing.table"})
	if len(res.Tables) != 1 {
		t.Errorf("expected 1 found table, got %d", len(res.Tables))
	}
	if len(res.NotFound) != 1 || !strings.HasPrefix(res.NotFound[0], "missing") {
		t.Errorf("expected missing.table in NotFound, got %v", res.NotFound)
	}
}
