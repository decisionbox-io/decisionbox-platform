//go:build integration

package schema_retrieve

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Integration tests run against a real Qdrant container. Shares the
// `integration` build tag with the rest of the agent's integration tests
// so Makefile's test-integration target picks them up.

var testRetriever *Retriever

func TestMain(m *testing.M) {
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
		panic(err)
	}
	defer container.Terminate(ctx)

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "6334")

	testRetriever, err = New(Config{Host: host, Port: port.Int()})
	if err != nil {
		panic(err)
	}
	defer testRetriever.Close()

	os.Exit(m.Run())
}

func TestInteg_HealthCheck(t *testing.T) {
	if err := testRetriever.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestInteg_EnsureCollection_Idempotent(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-ensure"
	t.Cleanup(func() { _ = testRetriever.DropCollection(ctx, projectID) })

	if err := testRetriever.EnsureCollection(ctx, projectID, 128); err != nil {
		t.Fatalf("first EnsureCollection: %v", err)
	}
	if err := testRetriever.EnsureCollection(ctx, projectID, 128); err != nil {
		t.Fatalf("second (idempotent) EnsureCollection: %v", err)
	}
}

func TestInteg_UpsertAndSearch_RoundTrip(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-upsert-search"
	t.Cleanup(func() { _ = testRetriever.DropCollection(ctx, projectID) })

	// Use a tiny 3-dim vector so hand-crafted embeddings are tractable.
	if err := testRetriever.EnsureCollection(ctx, projectID, 3); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	items := []UpsertItem{
		{
			Blurb: TableBlurb{
				Table:          "sales.orders",
				Dataset:        "sales",
				Blurb:          "A table of sales orders placed by customers.",
				Keywords:       []string{"sales", "orders"},
				RowCount:       1_000_000,
				ColumnCount:    14,
				BlurbModel:     "bedrock/qwen.qwen3-32b-v1:0",
				EmbeddingModel: "openai/text-embedding-3-large",
			},
			Vector: normalize([]float64{1, 0, 0}),
		},
		{
			Blurb: TableBlurb{
				Table:    "sales.users",
				Dataset:  "sales",
				Blurb:    "A table of users.",
				Keywords: []string{"users", "customers"},
				RowCount: 50_000,
			},
			Vector: normalize([]float64{0, 1, 0}),
		},
		{
			Blurb: TableBlurb{
				Table:    "analytics.events",
				Dataset:  "analytics",
				Blurb:    "A table of raw clickstream events.",
				Keywords: []string{"events", "analytics"},
				RowCount: 10_000_000,
			},
			Vector: normalize([]float64{0, 0, 1}),
		},
	}
	if err := testRetriever.Upsert(ctx, projectID, items); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Query along (1,0,0) — should rank sales.orders first.
	hits, err := testRetriever.Search(ctx, projectID, normalize([]float64{1, 0, 0}), SearchOpts{TopK: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d", len(hits))
	}
	if hits[0].Blurb.Table != "sales.orders" {
		t.Errorf("top hit = %q", hits[0].Blurb.Table)
	}
	// Payload round-trip.
	if hits[0].Blurb.RowCount != 1_000_000 || hits[0].Blurb.ColumnCount != 14 {
		t.Errorf("payload counts wrong: %+v", hits[0].Blurb)
	}
	if hits[0].Blurb.BlurbModel != "bedrock/qwen.qwen3-32b-v1:0" {
		t.Errorf("blurb_model = %q", hits[0].Blurb.BlurbModel)
	}
	if len(hits[0].Blurb.Keywords) != 2 {
		t.Errorf("keywords = %v", hits[0].Blurb.Keywords)
	}
}

func TestInteg_Upsert_IdempotentByTableName(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-idempotent"
	t.Cleanup(func() { _ = testRetriever.DropCollection(ctx, projectID) })

	_ = testRetriever.EnsureCollection(ctx, projectID, 3)

	first := []UpsertItem{{
		Blurb:  TableBlurb{Table: "t1", Blurb: "first version"},
		Vector: normalize([]float64{1, 0, 0}),
	}}
	_ = testRetriever.Upsert(ctx, projectID, first)

	// Re-upsert with a different blurb — same point ID must overwrite, not duplicate.
	second := []UpsertItem{{
		Blurb:  TableBlurb{Table: "t1", Blurb: "second version"},
		Vector: normalize([]float64{1, 0, 0}),
	}}
	_ = testRetriever.Upsert(ctx, projectID, second)

	hits, _ := testRetriever.Search(ctx, projectID, normalize([]float64{1, 0, 0}), SearchOpts{TopK: 5})
	if len(hits) != 1 {
		t.Errorf("expected 1 point after idempotent upsert, got %d", len(hits))
	}
	if hits[0].Blurb.Blurb != "second version" {
		t.Errorf("blurb not overwritten: %q", hits[0].Blurb.Blurb)
	}
}

func TestInteg_DropCollection_RemovesAllPoints(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-drop"

	_ = testRetriever.EnsureCollection(ctx, projectID, 3)
	_ = testRetriever.Upsert(ctx, projectID, []UpsertItem{{
		Blurb:  TableBlurb{Table: "t", Blurb: "x"},
		Vector: normalize([]float64{1, 0, 0}),
	}})
	if err := testRetriever.DropCollection(ctx, projectID); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}

	// Second drop = no-op.
	if err := testRetriever.DropCollection(ctx, projectID); err != nil {
		t.Errorf("second drop should be no-op: %v", err)
	}

	// Searching after drop: Qdrant returns "collection not found" — that's
	// expected and the agent treats it as "need to index first".
	_, err := testRetriever.Search(ctx, projectID, normalize([]float64{1, 0, 0}), SearchOpts{})
	if err == nil {
		t.Error("Search after drop should error")
	}
}

func TestInteg_DatasetFilter(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-dataset-filter"
	t.Cleanup(func() { _ = testRetriever.DropCollection(ctx, projectID) })

	_ = testRetriever.EnsureCollection(ctx, projectID, 3)
	_ = testRetriever.Upsert(ctx, projectID, []UpsertItem{
		{Blurb: TableBlurb{Table: "sales.a", Dataset: "sales", Blurb: "sales"}, Vector: normalize([]float64{1, 0.1, 0})},
		{Blurb: TableBlurb{Table: "analytics.a", Dataset: "analytics", Blurb: "analytics"}, Vector: normalize([]float64{1, 0.1, 0})},
	})
	hits, err := testRetriever.Search(ctx, projectID, normalize([]float64{1, 0.1, 0}), SearchOpts{
		DatasetFilter: "sales",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (dataset filter)", len(hits))
	}
	if hits[0].Blurb.Dataset != "sales" {
		t.Errorf("wrong dataset returned: %q", hits[0].Blurb.Dataset)
	}
}

func TestInteg_MinRowCountFilter(t *testing.T) {
	ctx := context.Background()
	projectID := "integ-rowcount-filter"
	t.Cleanup(func() { _ = testRetriever.DropCollection(ctx, projectID) })

	_ = testRetriever.EnsureCollection(ctx, projectID, 3)
	_ = testRetriever.Upsert(ctx, projectID, []UpsertItem{
		{Blurb: TableBlurb{Table: "small", RowCount: 50}, Vector: normalize([]float64{1, 0, 0})},
		{Blurb: TableBlurb{Table: "big", RowCount: 100_000}, Vector: normalize([]float64{1, 0, 0})},
	})
	hits, _ := testRetriever.Search(ctx, projectID, normalize([]float64{1, 0, 0}), SearchOpts{MinRowCount: 10_000})
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Blurb.Table != "big" {
		t.Errorf("kept wrong table: %q", hits[0].Blurb.Table)
	}
}

// normalize turns a vector into unit length — cosine distance requires it
// or the distance becomes scale-dependent.
func normalize(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	n := math.Sqrt(sum)
	if n == 0 {
		return v
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
