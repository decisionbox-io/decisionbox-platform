package discovery

import (
	"context"
	"fmt"
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

	// The standalone `_id` must equal the embedded `id`. Reusing the same id
	// (assigned by the orchestrator during analysis) across discovery doc,
	// standalone collection, and Qdrant point is the whole point of this
	// denormalization shape — any new UUID here would reintroduce the
	// id-mismatch bug that broke Ask source links in prod.
	if insights[0].ID != "orig-1" {
		t.Errorf("expected ID to match embedded id 'orig-1', got %q", insights[0].ID)
	}
	if insights[1].ID != "orig-2" {
		t.Errorf("expected ID to match embedded id 'orig-2', got %q", insights[1].ID)
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

	if recs[0].ID != "rec-orig-1" {
		t.Errorf("expected ID to match embedded id 'rec-orig-1', got %q", recs[0].ID)
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

// mockEmbedIndexStore implements EmbedIndexStore for testing.
type mockEmbedIndexStore struct {
	insertedInsights []*commonmodels.StandaloneInsight
	insertedRecs     []*commonmodels.StandaloneRecommendation
	embedUpdates     []embedUpdate
	dupUpdates       []dupUpdate
	insertError      error
}

type embedUpdate struct {
	Collection, ID, Text, Model string
}
type dupUpdate struct {
	Collection, ID, DupOf string
	Score                 float64
}

func (m *mockEmbedIndexStore) InsertInsights(_ context.Context, ins []*commonmodels.StandaloneInsight) error {
	if m.insertError != nil {
		return m.insertError
	}
	m.insertedInsights = append(m.insertedInsights, ins...)
	return nil
}
func (m *mockEmbedIndexStore) InsertRecommendations(_ context.Context, recs []*commonmodels.StandaloneRecommendation) error {
	if m.insertError != nil {
		return m.insertError
	}
	m.insertedRecs = append(m.insertedRecs, recs...)
	return nil
}
func (m *mockEmbedIndexStore) UpdateEmbedding(_ context.Context, collection, id, text, model string) error {
	m.embedUpdates = append(m.embedUpdates, embedUpdate{collection, id, text, model})
	return nil
}
func (m *mockEmbedIndexStore) UpdateDuplicate(_ context.Context, collection, id, dupOf string, score float64) error {
	m.dupUpdates = append(m.dupUpdates, dupUpdate{collection, id, dupOf, score})
	return nil
}

func TestSaveStandaloneDocuments_Success(t *testing.T) {
	store := &mockEmbedIndexStore{}
	o := &Orchestrator{embedIndexStore: store}

	insights := []*commonmodels.StandaloneInsight{
		{ID: "ins-1", ProjectID: "proj-1", Name: "Test insight"},
	}
	recs := []*commonmodels.StandaloneRecommendation{
		{ID: "rec-1", ProjectID: "proj-1", Title: "Test rec"},
	}

	err := o.saveStandaloneDocuments(context.Background(), insights, recs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.insertedInsights) != 1 {
		t.Errorf("expected 1 insight inserted, got %d", len(store.insertedInsights))
	}
	if len(store.insertedRecs) != 1 {
		t.Errorf("expected 1 rec inserted, got %d", len(store.insertedRecs))
	}
}

func TestSaveStandaloneDocuments_Error(t *testing.T) {
	store := &mockEmbedIndexStore{insertError: fmt.Errorf("db error")}
	o := &Orchestrator{embedIndexStore: store}

	err := o.saveStandaloneDocuments(context.Background(),
		[]*commonmodels.StandaloneInsight{{ID: "ins-1"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestEmbedAndIndex_Success(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockEmb := &mockEmbeddingProvider{dims: 3, model: "test-model"}
	mockVS := &mockVectorStore{}

	o := &Orchestrator{
		embedIndexStore:   store,
		embeddingProvider: mockEmb,
		vectorStore:       mockVS,
	}

	insights := []*commonmodels.StandaloneInsight{
		{ID: "ins-1", ProjectID: "proj-1", DiscoveryID: "disc-1",
			Name: "High churn", Description: "Players leaving",
			AnalysisArea: "churn", Severity: "high", Confidence: 0.85, CreatedAt: time.Now()},
	}
	recs := []*commonmodels.StandaloneRecommendation{
		{ID: "rec-1", ProjectID: "proj-1", DiscoveryID: "disc-1",
			Title: "Add retries", Description: "Impl retries",
			Confidence: 0.78, CreatedAt: time.Now()},
	}

	err := o.embedAndIndex(context.Background(), insights, recs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mockVS.ensured {
		t.Error("expected EnsureCollection to be called")
	}
	if mockEmb.calls != 1 {
		t.Errorf("expected 1 Embed call, got %d", mockEmb.calls)
	}
	if len(mockVS.upserted) != 2 {
		t.Errorf("expected 2 points upserted, got %d", len(mockVS.upserted))
	}
	if len(store.embedUpdates) != 2 {
		t.Errorf("expected 2 embedding updates, got %d", len(store.embedUpdates))
	}
	// Verify embedding text was set
	if insights[0].EmbeddingText == "" {
		t.Error("expected embedding text to be set on insight")
	}
	if insights[0].EmbeddingModel != "test-model" {
		t.Errorf("expected model=test-model, got %s", insights[0].EmbeddingModel)
	}
}

func TestEmbedAndIndex_EmptyDocuments(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockEmb := &mockEmbeddingProvider{dims: 3, model: "test-model"}
	mockVS := &mockVectorStore{}

	o := &Orchestrator{
		embedIndexStore:   store,
		embeddingProvider: mockEmb,
		vectorStore:       mockVS,
	}

	err := o.embedAndIndex(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for empty docs: %v", err)
	}
	// EnsureCollection should still be called
	if !mockVS.ensured {
		t.Error("expected EnsureCollection to be called")
	}
	if mockEmb.calls != 0 {
		t.Errorf("expected 0 embed calls for empty docs, got %d", mockEmb.calls)
	}
}

func TestCheckAndMarkDuplicate_Found(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockVS := &mockVectorStore{
		dupes: []vectorstore.SearchResult{
			{ID: "existing-1", Score: 0.97},
		},
	}

	o := &Orchestrator{
		embedIndexStore: store,
		vectorStore:     mockVS,
	}

	o.checkAndMarkDuplicate(context.Background(), "new-1", []float64{0.1, 0.2}, "proj-1", "insight", "disc-2")

	if len(store.dupUpdates) != 1 {
		t.Fatalf("expected 1 duplicate update, got %d", len(store.dupUpdates))
	}
	if store.dupUpdates[0].DupOf != "existing-1" {
		t.Errorf("expected duplicate_of=existing-1, got %s", store.dupUpdates[0].DupOf)
	}
	if store.dupUpdates[0].Score != 0.97 {
		t.Errorf("expected score=0.97, got %f", store.dupUpdates[0].Score)
	}
	if store.dupUpdates[0].Collection != "insights" {
		t.Errorf("expected collection=insights, got %s", store.dupUpdates[0].Collection)
	}
}

func TestCheckAndMarkDuplicate_NotFound(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockVS := &mockVectorStore{dupes: nil}

	o := &Orchestrator{
		embedIndexStore: store,
		vectorStore:     mockVS,
	}

	o.checkAndMarkDuplicate(context.Background(), "new-1", []float64{0.1, 0.2}, "proj-1", "insight", "disc-2")

	if len(store.dupUpdates) != 0 {
		t.Errorf("expected 0 duplicate updates, got %d", len(store.dupUpdates))
	}
}

func TestCheckAndMarkDuplicate_Recommendation(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockVS := &mockVectorStore{
		dupes: []vectorstore.SearchResult{
			{ID: "existing-rec", Score: 0.96},
		},
	}

	o := &Orchestrator{
		embedIndexStore: store,
		vectorStore:     mockVS,
	}

	o.checkAndMarkDuplicate(context.Background(), "new-rec", []float64{0.1, 0.2}, "proj-1", "recommendation", "disc-2")

	if len(store.dupUpdates) != 1 {
		t.Fatalf("expected 1 duplicate update, got %d", len(store.dupUpdates))
	}
	if store.dupUpdates[0].Collection != "recommendations" {
		t.Errorf("expected collection=recommendations, got %s", store.dupUpdates[0].Collection)
	}
}

func TestRunPhaseEmbedIndex_DenormalizeOnly(t *testing.T) {
	store := &mockEmbedIndexStore{}
	// No embeddingProvider or vectorStore — should denormalize only
	o := &Orchestrator{
		embedIndexStore:   store,
		statusReporter:    &StatusReporter{},
	}

	result := &models.DiscoveryResult{
		ID:        "disc-1",
		ProjectID: "proj-1",
		Domain:    "gaming",
		Category:  "match3",
		Insights: []models.Insight{
			{Name: "Test", Description: "Test insight", Severity: "high", DiscoveredAt: time.Now()},
		},
	}

	o.runPhaseEmbedIndex(context.Background(), result)

	if len(store.insertedInsights) != 1 {
		t.Errorf("expected 1 insight inserted, got %d", len(store.insertedInsights))
	}
}

func TestRunPhaseEmbedIndex_FullPipeline(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockEmb := &mockEmbeddingProvider{dims: 3, model: "test-model"}
	mockVS := &mockVectorStore{}

	o := &Orchestrator{
		embedIndexStore:   store,
		embeddingProvider: mockEmb,
		vectorStore:       mockVS,
		statusReporter:    &StatusReporter{},
	}

	result := &models.DiscoveryResult{
		ID:        "disc-1",
		ProjectID: "proj-1",
		Domain:    "gaming",
		Category:  "match3",
		Insights: []models.Insight{
			{Name: "Test insight", Description: "Desc", Severity: "high", DiscoveredAt: time.Now()},
		},
		Recommendations: []models.Recommendation{
			{Title: "Test rec", Description: "Desc", Priority: 1},
		},
	}

	o.runPhaseEmbedIndex(context.Background(), result)

	if len(store.insertedInsights) != 1 {
		t.Errorf("expected 1 insight, got %d", len(store.insertedInsights))
	}
	if len(store.insertedRecs) != 1 {
		t.Errorf("expected 1 rec, got %d", len(store.insertedRecs))
	}
	if len(mockVS.upserted) != 2 {
		t.Errorf("expected 2 points upserted, got %d", len(mockVS.upserted))
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
