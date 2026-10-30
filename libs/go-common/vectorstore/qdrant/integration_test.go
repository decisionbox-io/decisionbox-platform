//go:build integration_qdrant

package qdrant

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
)

var testProvider *Provider

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
		fmt.Fprintf(os.Stderr, "Qdrant container start failed: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx)

	host, err := container.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get container host: %v\n", err)
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "6334")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get container port: %v\n", err)
		os.Exit(1)
	}

	testProvider, err = New(Config{
		Host: host,
		Port: port.Int(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Qdrant provider: %v\n", err)
		os.Exit(1)
	}
	defer testProvider.Close()

	os.Exit(m.Run())
}

func TestIntegrationHealthCheck(t *testing.T) {
	ctx := context.Background()
	err := testProvider.HealthCheck(ctx)
	if err != nil {
		t.Fatalf("HealthCheck failed: %v", err)
	}
}

func TestIntegrationEnsureCollection(t *testing.T) {
	ctx := context.Background()

	// Create collection
	err := testProvider.EnsureCollection(ctx, 128)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	// Idempotent — calling again should not error
	err = testProvider.EnsureCollection(ctx, 128)
	if err != nil {
		t.Fatalf("EnsureCollection (idempotent) failed: %v", err)
	}
}

func TestIntegrationUpsertSearchDelete(t *testing.T) {
	ctx := context.Background()
	dims := 64

	err := testProvider.EnsureCollection(ctx, dims)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	// Generate UUIDs for test points
	id1 := newUUID()
	id2 := newUUID()
	id3 := newUUID()
	projID := "proj-integration"

	// Create test vectors
	vec1 := makeVector(dims, 0.1)
	vec2 := makeVector(dims, 0.9)
	vec3 := makeVector(dims, 0.15) // similar to vec1

	// Upsert points
	err = testProvider.Upsert(ctx, []vectorstore.Point{
		{
			ID:     id1,
			Vector: vec1,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     projID,
				"discovery_id":   "disc-1",
				"severity":       "high",
				"analysis_area":  "churn",
				"embedding_model": "test-model",
				"confidence":     0.85,
			},
		},
		{
			ID:     id2,
			Vector: vec2,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     projID,
				"discovery_id":   "disc-1",
				"severity":       "medium",
				"analysis_area":  "engagement",
				"embedding_model": "test-model",
				"confidence":     0.72,
			},
		},
		{
			ID:     id3,
			Vector: vec3,
			Payload: map[string]interface{}{
				"type":            "recommendation",
				"project_id":     projID,
				"discovery_id":   "disc-1",
				"embedding_model": "test-model",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Search — all results for project
	results, err := testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result should be vec1 itself (highest similarity)
	if results[0].ID != id1 {
		t.Errorf("expected first result to be %s, got %s", id1, results[0].ID)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected score >= 0.99 for identical vector, got %f", results[0].Score)
	}

	// Verify payload is returned
	if results[0].Payload["type"] != "insight" {
		t.Errorf("expected type=insight in payload, got %v", results[0].Payload["type"])
	}
	if results[0].Payload["severity"] != "high" {
		t.Errorf("expected severity=high, got %v", results[0].Payload["severity"])
	}

	// Search — filter by type
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Types:      []string{"insight"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search with type filter failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 insights, got %d", len(results))
	}

	// Search — filter by severity
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Severity:   "high",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search with severity filter failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with severity=high, got %d", len(results))
	}

	// Search — filter by analysis area
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs:   []string{projID},
		AnalysisArea: "churn",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("Search with analysis_area filter failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with area=churn, got %d", len(results))
	}

	// Search — filter by embedding model
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs:     []string{projID},
		EmbeddingModel: "test-model",
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("Search with embedding_model filter failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results with matching model, got %d", len(results))
	}

	// Search — nonexistent project
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs: []string{"nonexistent"},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search for nonexistent project failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for nonexistent project, got %d", len(results))
	}

	// Search — with min score (high threshold should return fewer results)
	results, err = testProvider.Search(ctx, vec1, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		MinScore:   0.99,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search with min_score failed: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result with score >= 0.99, got %d", len(results))
	}
	for _, r := range results {
		if r.Score < 0.99 {
			t.Errorf("result %s has score %f < 0.99", r.ID, r.Score)
		}
	}

	// Delete
	err = testProvider.Delete(ctx, []string{id2})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deletion
	results, err = testProvider.Search(ctx, vec2, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search after delete failed: %v", err)
	}
	for _, r := range results {
		if r.ID == id2 {
			t.Fatal("deleted point should not appear in results")
		}
	}
}

func TestIntegrationFindDuplicates(t *testing.T) {
	ctx := context.Background()
	dims := 64

	err := testProvider.EnsureCollection(ctx, dims)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	origID := newUUID()
	projID := "proj-dup-test"

	// Insert an insight from discovery-1
	vec := makeVector(dims, 0.5)
	err = testProvider.Upsert(ctx, []vectorstore.Point{
		{
			ID:     origID,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":         "insight",
				"project_id":   projID,
				"discovery_id": "disc-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Search for duplicates from discovery-2 with same vector
	results, err := testProvider.FindDuplicates(ctx, vec, projID, "insight", []string{"disc-2"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 duplicate, got %d", len(results))
	}
	if results[0].ID != origID {
		t.Errorf("expected %s, got %s", origID, results[0].ID)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected score >= 0.99 for identical vector, got %f", results[0].Score)
	}

	// Same discovery should be excluded
	results, err = testProvider.FindDuplicates(ctx, vec, projID, "insight", []string{"disc-1"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates with exclusion failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results (same discovery excluded), got %d", len(results))
	}

	// Several discoveries excluded at once — what a resumed run passes, so
	// its own superseded attempts cannot come back as duplicates of the
	// result it is about to save.
	results, err = testProvider.FindDuplicates(ctx, vec, projID, "insight",
		[]string{"disc-2", "disc-1", "disc-3"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates with several exclusions failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results when the matching discovery is one of several excluded, got %d", len(results))
	}

	// And an exclusion set that does NOT name the match still finds it, so
	// the widening cannot quietly suppress genuine duplicates.
	results, err = testProvider.FindDuplicates(ctx, vec, projID, "insight",
		[]string{"disc-2", "disc-3"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates with unrelated exclusions failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result when the match is not excluded, got %d", len(results))
	}

	// An empty set excludes nothing rather than everything.
	results, err = testProvider.FindDuplicates(ctx, vec, projID, "insight", nil, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates with no exclusions failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with no exclusions, got %d", len(results))
	}

	// Different project should not match
	results, err = testProvider.FindDuplicates(ctx, vec, "proj-other", "insight", []string{"disc-2"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates for different project failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for different project, got %d", len(results))
	}

	// Different type should not match
	results, err = testProvider.FindDuplicates(ctx, vec, projID, "recommendation", []string{"disc-2"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates for different type failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for different type, got %d", len(results))
	}

	// Dissimilar vector should not exceed threshold
	dissimilarVec := makeVector(dims, -0.5)
	results, err = testProvider.FindDuplicates(ctx, dissimilarVec, projID, "insight", []string{"disc-2"}, 0.95)
	if err != nil {
		t.Fatalf("FindDuplicates with dissimilar vector failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for dissimilar vector, got %d (score: %f)", len(results), results[0].Score)
	}
}

func TestIntegrationUpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	dims := 64

	err := testProvider.EnsureCollection(ctx, dims)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	id := newUUID()
	projID := "proj-idempotent-" + newUUID()[:8]
	vec := makeVector(dims, 0.3)
	point := vectorstore.Point{
		ID:     id,
		Vector: vec,
		Payload: map[string]interface{}{
			"type":       "insight",
			"project_id": projID,
			"version":    "v1",
		},
	}

	// First upsert
	err = testProvider.Upsert(ctx, []vectorstore.Point{point})
	if err != nil {
		t.Fatalf("First upsert failed: %v", err)
	}

	// Second upsert with updated payload
	point.Payload["version"] = "v2"
	err = testProvider.Upsert(ctx, []vectorstore.Point{point})
	if err != nil {
		t.Fatalf("Second upsert failed: %v", err)
	}

	// Search should return only one result (not two)
	results, err := testProvider.Search(ctx, vec, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (idempotent upsert), got %d", len(results))
	}
}

func TestIntegrationMultipleCollections(t *testing.T) {
	ctx := context.Background()

	// Create collections with different dimensions
	err := testProvider.EnsureCollection(ctx, 32)
	if err != nil {
		t.Fatalf("EnsureCollection(32) failed: %v", err)
	}
	err = testProvider.EnsureCollection(ctx, 48)
	if err != nil {
		t.Fatalf("EnsureCollection(48) failed: %v", err)
	}

	id32 := newUUID()
	id48 := newUUID()
	projID := "proj-multi-" + newUUID()[:8]

	// Insert into 32-dim collection
	vec32 := makeVector(32, 0.7)
	err = testProvider.Upsert(ctx, []vectorstore.Point{
		{
			ID:     id32,
			Vector: vec32,
			Payload: map[string]interface{}{
				"type":       "insight",
				"project_id": projID,
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert to 32-dim failed: %v", err)
	}

	// Insert into 48-dim collection
	vec48 := makeVector(48, 0.8)
	err = testProvider.Upsert(ctx, []vectorstore.Point{
		{
			ID:     id48,
			Vector: vec48,
			Payload: map[string]interface{}{
				"type":       "insight",
				"project_id": projID,
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert to 48-dim failed: %v", err)
	}

	// Search 32-dim collection should not return 48-dim point
	results, err := testProvider.Search(ctx, vec32, vectorstore.SearchOpts{
		ProjectIDs: []string{projID},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search 32-dim failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result in 32-dim collection, got %d", len(results))
	}
	if results[0].ID != id32 {
		t.Errorf("expected %s, got %s", id32, results[0].ID)
	}
}

func TestIntegrationCrossProjectSearch(t *testing.T) {
	ctx := context.Background()
	dims := 64

	err := testProvider.EnsureCollection(ctx, dims)
	if err != nil {
		t.Fatalf("EnsureCollection failed: %v", err)
	}

	idA := newUUID()
	idB := newUUID()
	idC := newUUID()
	projA := "proj-cross-a-" + newUUID()[:8]
	projB := "proj-cross-b-" + newUUID()[:8]
	projC := "proj-cross-c-" + newUUID()[:8]

	vec := makeVector(dims, 0.6)
	err = testProvider.Upsert(ctx, []vectorstore.Point{
		{
			ID:     idA,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     projA,
				"embedding_model": "test-model",
			},
		},
		{
			ID:     idB,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     projB,
				"embedding_model": "test-model",
			},
		},
		{
			ID:     idC,
			Vector: vec,
			Payload: map[string]interface{}{
				"type":            "insight",
				"project_id":     projC,
				"embedding_model": "different-model",
			},
		},
	})
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Cross-project search for all three, filtered by model
	results, err := testProvider.Search(ctx, vec, vectorstore.SearchOpts{
		ProjectIDs:     []string{projA, projB, projC},
		EmbeddingModel: "test-model",
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("Cross-project search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (same model), got %d", len(results))
	}
	for _, r := range results {
		if r.Payload["embedding_model"] != "test-model" {
			t.Errorf("unexpected model in result: %v", r.Payload["embedding_model"])
		}
	}
}

// newUUID generates a random UUID v4 string for test IDs.
func newUUID() string {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic(err)
	}
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// makeVector creates a normalized test vector of given dimensions with a seed value.
func makeVector(dims int, seed float64) []float64 {
	vec := make([]float64, dims)
	norm := 0.0
	for i := range dims {
		val := seed + float64(i)*0.01
		vec[i] = val
		norm += val * val
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}
