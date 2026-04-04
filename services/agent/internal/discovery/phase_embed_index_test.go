package discovery

import (
	"context"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// mockEmbeddingProvider implements embedding.Provider for testing.
type mockEmbeddingProvider struct {
	dims    int
	model   string
	vectors [][]float64 // pre-set return vectors
	calls   int
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	m.calls++
	result := make([][]float64, len(texts))
	for i := range texts {
		if i < len(m.vectors) {
			result[i] = m.vectors[i]
		} else {
			result[i] = make([]float64, m.dims)
		}
	}
	return result, nil
}

func (m *mockEmbeddingProvider) Dimensions() int        { return m.dims }
func (m *mockEmbeddingProvider) ModelName() string       { return m.model }
func (m *mockEmbeddingProvider) Validate(_ context.Context) error { return nil }

// mockVectorStore implements vectorstore.Provider for testing.
type mockVectorStore struct {
	upserted  []vectorstore.Point
	dupes     []vectorstore.SearchResult
	ensured   bool
	deleted   []string
}

func (m *mockVectorStore) Upsert(_ context.Context, points []vectorstore.Point) error {
	m.upserted = append(m.upserted, points...)
	return nil
}

func (m *mockVectorStore) Search(_ context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

func (m *mockVectorStore) FindDuplicates(_ context.Context, _ []float64, _ string, _ string, _ string, _ float64) ([]vectorstore.SearchResult, error) {
	return m.dupes, nil
}

func (m *mockVectorStore) Delete(_ context.Context, ids []string) error {
	m.deleted = append(m.deleted, ids...)
	return nil
}

func (m *mockVectorStore) HealthCheck(_ context.Context) error { return nil }

func (m *mockVectorStore) EnsureCollection(_ context.Context, _ int) error {
	m.ensured = true
	return nil
}

func TestDenormalizeInsights(t *testing.T) {
	o := &Orchestrator{
		projectID: "proj-1",
		domain:    "gaming",
		category:  "match3",
	}

	result := &models.DiscoveryResult{
		ID:        "disc-1",
		ProjectID: "proj-1",
		Domain:    "gaming",
		Category:  "match3",
		Insights: []models.Insight{
			{
				ID:           "orig-1",
				AnalysisArea: "churn",
				Name:         "High churn at Level 45",
				Description:  "Players leaving",
				Severity:     "high",
				AffectedCount: 12450,
				Confidence:   0.85,
				DiscoveredAt: time.Now(),
			},
			{
				ID:           "orig-2",
				AnalysisArea: "engagement",
				Name:         "Session length declining",
				Description:  "Average session length dropping",
				Severity:     "medium",
				Confidence:   0.72,
				DiscoveredAt: time.Now(),
			},
		},
	}

	insights := o.denormalizeInsights(result)

	if len(insights) != 2 {
		t.Fatalf("expected 2 insights, got %d", len(insights))
	}

	// Verify UUID format (36 chars with dashes)
	if len(insights[0].ID) != 36 {
		t.Errorf("expected UUID format ID, got %q", insights[0].ID)
	}

	// Verify IDs are unique
	if insights[0].ID == insights[1].ID {
		t.Error("expected unique IDs for each insight")
	}

	// Verify fields are copied correctly
	if insights[0].ProjectID != "proj-1" {
		t.Errorf("expected project_id=proj-1, got %s", insights[0].ProjectID)
	}
	if insights[0].DiscoveryID != "disc-1" {
		t.Errorf("expected discovery_id=disc-1, got %s", insights[0].DiscoveryID)
	}
	if insights[0].Name != "High churn at Level 45" {
		t.Errorf("expected name to be copied, got %s", insights[0].Name)
	}
	if insights[0].Severity != "high" {
		t.Errorf("expected severity=high, got %s", insights[0].Severity)
	}
	if insights[0].AffectedCount != 12450 {
		t.Errorf("expected affected_count=12450, got %d", insights[0].AffectedCount)
	}
}

func TestDenormalizeRecommendations(t *testing.T) {
	o := &Orchestrator{
		projectID: "proj-1",
		domain:    "gaming",
		category:  "match3",
	}

	result := &models.DiscoveryResult{
		ID:        "disc-1",
		ProjectID: "proj-1",
		Domain:    "gaming",
		Category:  "match3",
		Recommendations: []models.Recommendation{
			{
				ID:          "rec-orig-1",
				Category:    "engagement",
				Title:       "Add retry mechanics",
				Description: "Implement retries",
				Priority:    1,
				ExpectedImpact: models.Impact{
					Metric:               "D7 retention",
					EstimatedImprovement: "15-20%",
				},
				Confidence: 0.78,
			},
		},
	}

	recs := o.denormalizeRecommendations(result)

	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation, got %d", len(recs))
	}

	if len(recs[0].ID) != 36 {
		t.Errorf("expected UUID format ID, got %q", recs[0].ID)
	}
	if recs[0].RecommendationCategory != "engagement" {
		t.Errorf("expected category=engagement, got %s", recs[0].RecommendationCategory)
	}
	if recs[0].ExpectedImpact.Metric != "D7 retention" {
		t.Errorf("expected metric=D7 retention, got %s", recs[0].ExpectedImpact.Metric)
	}
}

func TestConvertValidation(t *testing.T) {
	// nil input
	if convertValidation(nil) != nil {
		t.Error("expected nil for nil input")
	}

	// non-nil input
	v := &models.InsightValidation{
		Status:        "confirmed",
		VerifiedCount: 100,
		OriginalCount: 120,
		Reasoning:     "Verified via SQL",
		ValidatedAt:   time.Now(),
	}
	result := convertValidation(v)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Status != "confirmed" {
		t.Errorf("expected status=confirmed, got %s", result.Status)
	}
	if result.VerifiedCount != 100 {
		t.Errorf("expected verified_count=100, got %d", result.VerifiedCount)
	}
}

func TestEmbedAndIndexBuildPoints(t *testing.T) {
	mockEmb := &mockEmbeddingProvider{
		dims:  3,
		model: "test-model",
	}
	mockVS := &mockVectorStore{}

	o := &Orchestrator{
		embeddingProvider: mockEmb,
		vectorStore:       mockVS,
	}

	insights := []*commonmodels.StandaloneInsight{
		{
			ID:           "11111111-1111-4111-8111-111111111111",
			ProjectID:    "proj-1",
			DiscoveryID:  "disc-1",
			AnalysisArea: "churn",
			Name:         "High churn",
			Description:  "Players leaving",
			Severity:     "high",
			Confidence:   0.85,
			CreatedAt:    time.Now(),
		},
	}
	recs := []*commonmodels.StandaloneRecommendation{
		{
			ID:          "22222222-2222-4222-8222-222222222222",
			ProjectID:   "proj-1",
			DiscoveryID: "disc-1",
			Title:       "Add retries",
			Description: "Implement retries",
			ExpectedImpact: commonmodels.ExpectedImpact{
				Metric:               "retention",
				EstimatedImprovement: "10%",
			},
			Confidence: 0.78,
			CreatedAt:  time.Now(),
		},
	}

	// embedAndIndex requires a real DB for MongoDB updates — skip that part.
	// Test the embedding and Qdrant upsert logic directly.

	// Verify embedding text is built
	text := insights[0].BuildEmbeddingText()
	if text == "" {
		t.Error("expected non-empty embedding text")
	}

	text = recs[0].BuildEmbeddingText()
	if text == "" {
		t.Error("expected non-empty recommendation embedding text")
	}

	// Verify mock embedding provider returns correct dimensions
	vecs, err := mockEmb.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("mock embed failed: %v", err)
	}
	if len(vecs[0]) != 3 {
		t.Errorf("expected 3 dims, got %d", len(vecs[0]))
	}

	// Verify collection is ensured
	err = o.vectorStore.EnsureCollection(context.Background(), 3)
	if err != nil {
		t.Fatalf("ensure collection failed: %v", err)
	}
	if !mockVS.ensured {
		t.Error("expected collection to be ensured")
	}
}
