//go:build integration

package discovery

import (
	"context"
	"math"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// stubIntegSchemaSource implements SchemaSource for the indexer
// integration tests — returns a fixed set of tables without needing a
// real warehouse. Renamed from stubSchemaSource to avoid colliding with
// the untagged unit-test stub of the same purpose in
// schema_indexer_cache_test.go (different field shape).
type stubIntegSchemaSource struct {
	schemas map[string]models.TableSchema
	err     error
}

func (s *stubIntegSchemaSource) DiscoverSchemas(_ context.Context) (map[string]models.TableSchema, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.schemas, nil
}

// memProgress is an in-memory ProgressReporter so we can assert the
// indexer calls the lifecycle methods in the right order without
// needing a real Mongo.
type memProgress struct {
	resetCalls int
	phases     []string
	totals     []int
	increments int
	errors     []string
}

func (p *memProgress) Reset(_ context.Context, _, _ string) error {
	p.resetCalls++
	return nil
}
func (p *memProgress) SetPhase(_ context.Context, _, phase string) error {
	p.phases = append(p.phases, phase)
	return nil
}
func (p *memProgress) SetTotals(_ context.Context, _ string, total int) error {
	p.totals = append(p.totals, total)
	return nil
}
func (p *memProgress) SetCounters(_ context.Context, _ string, total, _ int) error {
	p.totals = append(p.totals, total)
	return nil
}
func (p *memProgress) IncrementDone(_ context.Context, _ string, d int) error {
	p.increments += d
	return nil
}
func (p *memProgress) RecordError(_ context.Context, _, msg string) error {
	p.errors = append(p.errors, msg)
	return nil
}

func startQdrant(t *testing.T) *schema_retrieve.Retriever {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "qdrant/qdrant:v1.13.6",
			ExposedPorts: []string{"6334/tcp"},
			WaitingFor:   wait.ForListeningPort("6334/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("qdrant start: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "6334")
	r, err := schema_retrieve.New(schema_retrieve.Config{Host: host, Port: port.Int()})
	if err != nil {
		t.Fatalf("retriever: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// unitVec produces a unit-length vector at axis i for a d-dim space.
// Cosine distance requires unit-length; anything else skews ranking.
func unitVec(i, d int) []float64 {
	v := make([]float64, d)
	v[i%d] = 1
	// Unit-length is already 1 here, but keep the formula for clarity.
	_ = math.Sqrt
	return v
}

func TestInteg_SchemaIndexer_BuildIndex_EndToEnd(t *testing.T) {
	ctx := context.Background()
	retriever := startQdrant(t)

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
		"sales.events_LOG": {
			TableName: "sales.events_LOG",
			RowCount:  10_000_000,
			Columns:   []models.ColumnInfo{{Name: "id", Type: "INT64"}},
		},
	}
	src := &stubIntegSchemaSource{schemas: schemas}

	llm := &stubLLM{text: "A concise table description grounded in the metadata."}
	gen, err := blurb.New(blurb.Config{LLM: llm, Model: "gpt-4o", Workers: 2})
	if err != nil {
		t.Fatalf("blurb.New: %v", err)
	}
	emb := &stubEmbedder{dim: 3, model: "stub-embedder"}
	progress := &memProgress{}

	si := SchemaIndexer{
		Discovery: src,
		Blurber:   gen,
		Embedder:  emb,
		Retriever: retriever,
		Progress:  progress,
	}
	stats, err := si.BuildIndex(ctx, IndexOptions{
		ProjectID:       "integ-proj-1",
		RunID:           "run-1",
		BlurbModelLabel: "stub/gpt-4o",
		DomainBlurb:     "E-commerce warehouse.",
		Keywords:        []string{"sales", "orders"},
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if stats.Tables != 3 {
		t.Errorf("indexed tables = %d, want 3", stats.Tables)
	}
	if stats.BlurbTokensIn != 3 || stats.BlurbTokensOut != 3 {
		t.Errorf("usage stats: %+v (want in=3, out=3)", stats)
	}
	if progress.resetCalls != 1 {
		t.Errorf("reset called %d times", progress.resetCalls)
	}
	// Phase order: schema_discovery → describing_tables → embedding.
	if len(progress.phases) != 3 ||
		progress.phases[0] != "schema_discovery" ||
		progress.phases[1] != "describing_tables" ||
		progress.phases[2] != "embedding" {
		t.Errorf("phase sequence: %v", progress.phases)
	}
	if progress.totals[0] != 3 {
		t.Errorf("totals = %v", progress.totals)
	}
	if progress.increments != 3 {
		t.Errorf("increments = %d, want 3", progress.increments)
	}

	// Search should find the indexed tables.
	hits, err := retriever.Search(ctx, "integ-proj-1", unitVec(0, 3), schema_retrieve.SearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits after indexing")
	}
	// Payload shape: keywords, row count, blurb_model propagated through.
	for _, h := range hits {
		if h.Blurb.BlurbModel != "stub/gpt-4o" {
			t.Errorf("blurb_model = %q", h.Blurb.BlurbModel)
		}
		if h.Blurb.EmbeddingModel != "stub-embedder" {
			t.Errorf("embedding_model = %q", h.Blurb.EmbeddingModel)
		}
		if len(h.Blurb.Keywords) != 2 {
			t.Errorf("keywords lost: %v", h.Blurb.Keywords)
		}
	}
}

func TestInteg_SchemaIndexer_EmptySchemas_Errors(t *testing.T) {
	ctx := context.Background()
	retriever := startQdrant(t)
	gen, _ := blurb.New(blurb.Config{LLM: &stubLLM{text: "x"}, Model: "gpt-4o"})
	si := SchemaIndexer{
		Discovery: &stubIntegSchemaSource{schemas: map[string]models.TableSchema{}},
		Blurber:   gen,
		Embedder:  &stubEmbedder{dim: 3, model: "stub"},
		Retriever: retriever,
	}
	_, err := si.BuildIndex(ctx, IndexOptions{ProjectID: "p-empty"})
	if err == nil {
		t.Error("empty schema set should error")
	}
}

func TestInteg_SchemaIndexer_DropCollectionOnRetry(t *testing.T) {
	ctx := context.Background()
	retriever := startQdrant(t)
	gen, _ := blurb.New(blurb.Config{LLM: &stubLLM{text: "blurb"}, Model: "gpt-4o", Workers: 1})
	schemas := map[string]models.TableSchema{
		"s.t1": {TableName: "s.t1", RowCount: 1, Columns: []models.ColumnInfo{{Name: "id"}}},
		"s.t2": {TableName: "s.t2", RowCount: 2, Columns: []models.ColumnInfo{{Name: "id"}}},
	}
	si := SchemaIndexer{
		Discovery: &stubIntegSchemaSource{schemas: schemas},
		Blurber:   gen,
		Embedder:  &stubEmbedder{dim: 3, model: "stub"},
		Retriever: retriever,
	}

	// First run: 2 tables indexed.
	if _, err := si.BuildIndex(ctx, IndexOptions{ProjectID: "p-reindex"}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run with a smaller schema set: should drop and rebuild,
	// leaving exactly 1 table.
	si.Discovery = &stubIntegSchemaSource{schemas: map[string]models.TableSchema{
		"s.only": {TableName: "s.only", RowCount: 5, Columns: []models.ColumnInfo{{Name: "id"}}},
	}}
	if _, err := si.BuildIndex(ctx, IndexOptions{ProjectID: "p-reindex"}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	hits, _ := retriever.Search(ctx, "p-reindex", unitVec(0, 3), schema_retrieve.SearchOpts{TopK: 50})
	if len(hits) != 1 {
		t.Errorf("after reindex: %d hits, want 1", len(hits))
	}
	if hits[0].Blurb.Table != "s.only" {
		t.Errorf("retained stale entry: %v", hits[0].Blurb.Table)
	}
}
