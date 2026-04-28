//go:build integration

package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"sync"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai" // registers "openai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// startRunStepQdrant boots a fresh Qdrant testcontainer and returns
// a pb.Client wired to it. Cleanup is registered via t.Cleanup.
func startRunStepQdrant(t *testing.T) *pb.Client {
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
	client, err := pb.NewClient(&pb.Config{Host: host, Port: port.Int()})
	if err != nil {
		t.Fatalf("pb.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// fixedDimEmbedder hashes the input text into a deterministic
// unit vector. Two identical texts hash to the same vector;
// different texts produce different but stable vectors. Lets the
// integration test exercise the full Upsert + Search round-trip
// without an external embedding API.
type fixedDimEmbedder struct {
	dim int
}

func (e *fixedDimEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	if e.dim == 0 {
		e.dim = 8
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		out[i] = hashToUnitVector(t, e.dim)
	}
	return out, nil
}
func (e *fixedDimEmbedder) Dimensions() int   { return e.dim }
func (e *fixedDimEmbedder) ModelName() string { return "fake-fixed-dim-embedder" }

func hashToUnitVector(s string, d int) []float64 {
	h := sha256.Sum256([]byte(s))
	v := make([]float64, d)
	for i := 0; i < d; i++ {
		off := (i * 4) % len(h)
		raw := binary.BigEndian.Uint32(h[off : off+4])
		v[i] = float64(int32(raw)) / float64(math.MaxInt32) //nolint:gosec
	}
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	n := math.Sqrt(sum)
	if n == 0 {
		v[0] = 1
		return v
	}
	for i := range v {
		v[i] /= n
	}
	return v
}

func TestIntegration_RunStepIndex_FullCycle(t *testing.T) {
	ctx := context.Background()
	client := startRunStepQdrant(t)
	embedder := &fixedDimEmbedder{dim: 8}

	idx, err := NewRunStepIndex(client, embedder, "RUN_FULLCYCLE")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}

	steps := makeIntegSteps(30)
	for _, s := range steps {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatalf("Upsert step %d: %v", s.Step, err)
		}
	}

	hits, err := idx.Search(ctx, "churn rate per cohort", RunStepIndexSearchOpts{TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("expected hits, got 0")
	}

	if err := idx.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// Second drop must be no-op (idempotent cleanup).
	if err := idx.Drop(ctx); err != nil {
		t.Errorf("second Drop should be no-op: %v", err)
	}
}

func TestIntegration_RunStepIndex_ConcurrentUpserts(t *testing.T) {
	ctx := context.Background()
	client := startRunStepQdrant(t)
	embedder := &fixedDimEmbedder{dim: 8}

	idx, err := NewRunStepIndex(client, embedder, "RUN_CONCURRENT")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Drop(ctx) })

	const N = 30
	steps := makeIntegSteps(N)

	var wg sync.WaitGroup
	errs := make(chan error, N)
	for _, s := range steps {
		wg.Add(1)
		go func(step models.ExplorationStep) {
			defer wg.Done()
			if err := idx.Upsert(ctx, step); err != nil {
				errs <- err
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent upsert: %v", err)
	}

	hits, err := idx.Search(ctx, "step 17 retention", RunStepIndexSearchOpts{TopK: N})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) < N-2 {
		// Tiny margin for any race between final upsert and search.
		t.Errorf("expected ~%d retrievable, got %d", N, len(hits))
	}
}

// TestIntegration_RunStepIndex_MultilingualQuery exercises the
// production wiring with a real OpenAI embedding model so a Turkish
// area query retrieves English-described steps. Skipped without
// INTEGRATION_TEST_OPENAI_API_KEY in the environment.
func TestIntegration_RunStepIndex_MultilingualQuery(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set; multilingual coverage requires a real embedding model")
	}

	ctx := context.Background()
	client := startRunStepQdrant(t)

	embedder, err := goembedding.NewProvider("openai", goembedding.ProviderConfig{
		"api_key": apiKey,
		"model":   "text-embedding-3-large",
	})
	if err != nil {
		t.Fatalf("openai embedder: %v", err)
	}

	idx, err := NewRunStepIndex(client, embedder, "RUN_MULTILINGUAL")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Drop(ctx) })

	// Seed steps with English purposes that mirror the production
	// "Portakal Bahçem" pattern: shop conversion, customer churn,
	// session metrics.
	english := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1", QueryPurpose: "calculate weekly conversion rate from cart to purchase"},
		{Step: 2, Action: "query_data", Query: "SELECT 2", QueryPurpose: "investigate customer churn by signup cohort"},
		{Step: 3, Action: "query_data", Query: "SELECT 3", QueryPurpose: "session duration distribution by device type"},
		{Step: 4, Action: "query_data", Query: "SELECT 4", QueryPurpose: "shipping costs as percentage of order value"},
	}
	for _, s := range english {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatalf("Upsert step %d: %v", s.Step, err)
		}
	}

	// Turkish area query for the conversion topic.
	hits, err := idx.Search(ctx, "dönüşüm oranı analizi", RunStepIndexSearchOpts{TopK: 4})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("multilingual query returned no hits at all")
	}
	// The top hit must be the conversion step (1). text-embedding-3-large
	// is multilingual; if it were monolingual, churn / sessions could
	// rank above conversion.
	if hits[0].Step != 1 {
		t.Errorf("multilingual top hit: got step %d (purpose=%q), want step 1", hits[0].Step, hits[0].Purpose)
	}
}

// makeIntegSteps fabricates N steps with stable, distinct content so
// the index has something semantically diverse to rank against.
func makeIntegSteps(n int) []models.ExplorationStep {
	out := make([]models.ExplorationStep, n)
	purposes := []string{
		"compute churn rate per cohort",
		"investigate retention by week",
		"daily revenue trend",
		"top users by purchase count",
		"session duration distribution",
		"error rate by feature flag",
	}
	for i := 0; i < n; i++ {
		out[i] = models.ExplorationStep{
			Step:         i + 1,
			Action:       "query_data",
			Query:        "SELECT " + strconv.Itoa(i) + " AS x",
			QueryPurpose: purposes[i%len(purposes)] + " step " + strconv.Itoa(i+1),
			RowCount:     i,
		}
	}
	return out
}
