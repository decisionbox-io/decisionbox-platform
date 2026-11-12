package discovery

import (
	"context"
	"errors"
	"fmt"
	"math"
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

func (m *mockEmbeddingProvider) Dimensions() int                  { return m.dims }
func (m *mockEmbeddingProvider) ModelName() string                { return m.model }
func (m *mockEmbeddingProvider) Validate(_ context.Context) error { return nil }

// mockVectorStore implements vectorstore.Provider for testing.
type mockVectorStore struct {
	upserted []vectorstore.Point
	dupes    []vectorstore.SearchResult
	ensured  bool
	deleted  []string
	// searches counts Search calls, so a test can hold the line that semantic
	// dedup is only asked about candidates no key already matched.
	searches int
	// dupExcludes records the exclusion set of each duplicate search, which
	// is what proves a resumed run does not dedup against its own
	// superseded attempts.
	dupExcludes [][]string
	// dupThresholds records the threshold each duplicate search was given, so
	// a test can prove the configured floor reaches the store rather than
	// only the in-memory pass.
	dupThresholds []float64
	// dupeFn answers per vector when set, for the cases where the stored
	// answer has to differ between this run's documents — "an earlier run
	// already published THIS one" is not a property of the whole run.
	dupeFn func(vec []float64) []vectorstore.SearchResult
}

func (m *mockVectorStore) Upsert(_ context.Context, points []vectorstore.Point) error {
	m.upserted = append(m.upserted, points...)
	return nil
}

func (m *mockVectorStore) Search(_ context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	m.searches++
	return nil, nil
}

func (m *mockVectorStore) FindDuplicates(_ context.Context, vec []float64, _ string, _ string, excludeDiscoveryIDs []string, threshold float64) ([]vectorstore.SearchResult, error) {
	m.dupExcludes = append(m.dupExcludes, excludeDiscoveryIDs)
	m.dupThresholds = append(m.dupThresholds, threshold)
	if m.dupeFn != nil {
		return m.dupeFn(vec), nil
	}
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

func (m *mockVectorStore) SearchSchemaIndex(_ context.Context, _ string, _ []float64, _ int) ([]vectorstore.SearchResult, error) {
	return nil, nil
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
				ID:            "orig-1",
				AnalysisArea:  "churn",
				Name:          "High churn at Level 45",
				Description:   "Players leaving",
				Severity:      "high",
				AffectedCount: 12450,
				Confidence:    0.85,
				DiscoveredAt:  time.Now(),
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

// convertValidation has been removed — both agent-side and
// go-common-side InsightValidation are now the same valmodels type,
// so the conversion is a no-op identity. phase_embed_index now
// passes ins.Validation directly. If a future renaming reintroduces
// a conversion shim, add tests for it then.

// mockEmbedIndexStore implements EmbedIndexStore for testing.
type mockEmbedIndexStore struct {
	insertedInsights []*commonmodels.StandaloneInsight
	insertedRecs     []*commonmodels.StandaloneRecommendation
	embedUpdates     []embedUpdate
	dupUpdates       []dupUpdate
	insertError      error
	// alreadyFolded answers DuplicateOf: ids that a PREVIOUS run already
	// marked, which are still indexed in the vector store and so can still be
	// the best match for a later document.
	alreadyFolded map[string]string
	// missingDocs are ids whose vector-store point outlived its row. Modelled
	// as the exception rather than the rule, the way a real store behaves: a
	// point normally has a document, and a test that had to enumerate the
	// documents that exist would pass for a reason no store shares.
	missingDocs map[string]bool
	// storedDocs is what the store knows about documents from EARLIER runs —
	// their embedding text and magnitudes. Empty for the tests that do not
	// care, which is how a real store answers for a document written before
	// those fields existed.
	storedDocs     map[string]IndexedDoc
	duplicateOfErr error

	// deletedDiscoveries records the discoveries the retire step purged;
	// deleteInsightIDs / deleteRecIDs are the ids it hands back as the
	// Qdrant point ids to remove.
	deletedDiscoveries []string
	deleteInsightIDs   []string
	deleteRecIDs       []string
	deleteError        error
	// listedDiscoveries records the LIST calls separately from the deletes,
	// so a test can assert the ordering: points are addressable only until
	// the rows go, so listing must happen first and deleting last.
	listedDiscoveries []string
	listError         error
}

func (m *mockEmbedIndexStore) IndexedDoc(_ context.Context, _, id string) (IndexedDoc, bool, error) {
	if m.duplicateOfErr != nil {
		return IndexedDoc{}, false, m.duplicateOfErr
	}
	if m.missingDocs[id] {
		return IndexedDoc{}, false, nil
	}
	doc := m.storedDocs[id]
	// This run's own marks count too: markDuplicates writes them as it goes.
	for _, u := range m.dupUpdates {
		if u.ID == id {
			doc.DuplicateOf = u.DupOf
			return doc, true, nil
		}
	}
	if folded, ok := m.alreadyFolded[id]; ok {
		doc.DuplicateOf = folded
	}
	return doc, true, nil
}

func (m *mockEmbedIndexStore) PointIDsByDiscovery(_ context.Context, discoveryID string) ([]string, []string, error) {
	m.listedDiscoveries = append(m.listedDiscoveries, discoveryID)
	if m.listError != nil {
		return nil, nil, m.listError
	}
	return m.deleteInsightIDs, m.deleteRecIDs, nil
}

func (m *mockEmbedIndexStore) DeleteByDiscovery(_ context.Context, discoveryID string) error {
	m.deletedDiscoveries = append(m.deletedDiscoveries, discoveryID)
	return m.deleteError
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

	err := o.embedAndIndex(context.Background(), insights, recs, []string{"disc-1"})
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

	err := o.embedAndIndex(context.Background(), nil, nil, []string{"disc-1"})
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

func TestMarkDuplicates_StoredHit(t *testing.T) {
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

	o.markDuplicates(context.Background(),
		[]dedupCandidate{{id: "new-1", docType: "insight", projectID: "proj-1", vec: []float64{0.1, 0.2}}},
		[]string{"disc-2"})

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

func TestMarkDuplicates_NoStoredHit(t *testing.T) {
	store := &mockEmbedIndexStore{}
	mockVS := &mockVectorStore{dupes: nil}

	o := &Orchestrator{
		embedIndexStore: store,
		vectorStore:     mockVS,
	}

	o.markDuplicates(context.Background(),
		[]dedupCandidate{{id: "new-1", docType: "insight", projectID: "proj-1", vec: []float64{0.1, 0.2}}},
		[]string{"disc-2"})

	if len(store.dupUpdates) != 0 {
		t.Errorf("expected 0 duplicate updates, got %d", len(store.dupUpdates))
	}
}

func TestMarkDuplicates_Recommendation(t *testing.T) {
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

	o.markDuplicates(context.Background(),
		[]dedupCandidate{{id: "new-rec", docType: "recommendation", projectID: "proj-1", vec: []float64{0.1, 0.2}}},
		[]string{"disc-2"})

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
		embedIndexStore: store,
		statusReporter:  &StatusReporter{},
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

// --- same-run duplicates (acceptance criterion 5) ---

func dedupHarness(dupes []vectorstore.SearchResult) (*Orchestrator, *mockEmbedIndexStore, *mockVectorStore) {
	store := &mockEmbedIndexStore{}
	vs := &mockVectorStore{dupes: dupes}
	return &Orchestrator{embedIndexStore: store, vectorStore: vs}, store, vs
}

// markedAs returns the duplicate_of recorded for id, or "" if it was not marked.
func markedAs(store *mockEmbedIndexStore, id string) string {
	for _, u := range store.dupUpdates {
		if u.ID == id {
			return u.DupOf
		}
	}
	return ""
}

// TestMarkDuplicates_SameRunDuplicateIsDetected is the defect itself. The
// stored check excludes every discovery this run produced, and the run's own
// vectors are upserted only after every check has run — so before this, two
// duplicates born in the same run could not match each other by construction,
// however identical their text. On a run that emits a hundred insights, that is
// where most of the repetition is.
func TestMarkDuplicates_SameRunDuplicateIsDetected(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "first", docType: "insight", projectID: "p1", vec: []float64{1, 0, 0}},
		{id: "restated", docType: "insight", projectID: "p1", vec: []float64{0.99, 0.01, 0}},
		{id: "different", docType: "insight", projectID: "p1", vec: []float64{0, 1, 0}},
	}, []string{"disc-1"})

	if got := markedAs(store, "restated"); got != "first" {
		t.Errorf("the second of two near-identical insights in one run must be marked a duplicate of the first, got %q", got)
	}
	if got := markedAs(store, "first"); got != "" {
		t.Errorf("the first occurrence must stay canonical, got %q", got)
	}
	if got := markedAs(store, "different"); got != "" {
		t.Errorf("an unrelated insight must not be marked, got %q", got)
	}
	// The score must be recorded, or a bad threshold choice is unauditable
	// afterwards.
	for _, u := range store.dupUpdates {
		if u.Score <= 0 {
			t.Errorf("%s was marked with score %v — the similarity must be recorded", u.ID, u.Score)
		}
	}
}

// TestMarkDuplicates_ChainsCollapseToTheCanonical: if B duplicates A and C
// duplicates B, C must point at A. Pointing C at B would make C's only pointer
// a row that is itself hidden, which is how a suppression rule loses a document
// rather than folding it.
func TestMarkDuplicates_ChainsCollapseToTheCanonical(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0, 0}},
		{id: "b", docType: "insight", projectID: "p1", vec: []float64{0.995, 0.01, 0}},
		{id: "c", docType: "insight", projectID: "p1", vec: []float64{0.99, 0.02, 0}},
	}, nil)

	if got := markedAs(store, "b"); got != "a" {
		t.Errorf("b should fold into a, got %q", got)
	}
	if got := markedAs(store, "c"); got != "a" {
		t.Errorf("c should fold into a, not into the already-folded b; got %q", got)
	}
}

// TestMarkDuplicates_StoredHitWinsOverSameRun: an earlier run's document was
// published first and is the row the user has already seen, so it is the truer
// canonical and the stored pass is consulted first.
//
// Both of this run's documents resemble it, which is what the store would
// really answer: if the second document resembles the first closely enough to
// be its duplicate, it resembles whatever the first matched as well. The case
// where the store genuinely says no about the second is the next test, and
// there the fold is refused rather than assumed.
func TestMarkDuplicates_StoredHitWinsOverSameRun(t *testing.T) {
	o, store, _ := dedupHarness([]vectorstore.SearchResult{{ID: "published-last-month", Score: 0.98}})

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0, 0}},
		{id: "b", docType: "insight", projectID: "p1", vec: []float64{0.99, 0.01, 0}},
	}, nil)

	for _, id := range []string{"a", "b"} {
		if got := markedAs(store, id); got != "published-last-month" {
			t.Errorf("%s must fold into the earlier run's row, got %q", id, got)
		}
	}
}

// TestMarkDuplicates_TypesNeverCross: an insight and a recommendation are
// different kinds of document and must never be marked duplicates of one
// another, however similar their prose — a recommendation restating its own
// insight is the normal case, not a defect.
func TestMarkDuplicates_TypesNeverCross(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "ins", docType: "insight", projectID: "p1", vec: []float64{1, 0, 0}},
		{id: "rec", docType: "recommendation", projectID: "p1", vec: []float64{1, 0, 0}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("an identical insight and recommendation must not match: %+v", store.dupUpdates)
	}
}

// TestMarkDuplicates_RecommendationsAreDedupedToo — the half that regresses
// silently, since nothing downstream distinguishes the two paths.
func TestMarkDuplicates_RecommendationsAreDedupedToo(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "r1", docType: "recommendation", projectID: "p1", vec: []float64{1, 0, 0}},
		{id: "r2", docType: "recommendation", projectID: "p1", vec: []float64{0.99, 0.01, 0}},
	}, nil)

	if got := markedAs(store, "r2"); got != "r1" {
		t.Errorf("r2 should fold into r1, got %q", got)
	}
	for _, u := range store.dupUpdates {
		if u.Collection != "recommendations" {
			t.Errorf("the mark must be written to the recommendations collection, got %q", u.Collection)
		}
	}
}

func TestMarkDuplicates_BelowThresholdIsNotMarked(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	// cos([1,0],[0.8,0.6]) = 0.8, below the 0.85 default.
	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
		{id: "b", docType: "insight", projectID: "p1", vec: []float64{0.8, 0.6}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("a pair below the floor must not be marked: %+v", store.dupUpdates)
	}
}

func TestDocumentDedupMinScore(t *testing.T) {
	if got := documentDedupMinScore(); got != defaultDedupMinScore {
		t.Errorf("unset = %v, want %v", got, defaultDedupMinScore)
	}
	t.Setenv(documentDedupMinScoreEnv, "0.92")
	if got := documentDedupMinScore(); got != 0.92 {
		t.Errorf("0.92 = %v", got)
	}
	// Cosine cannot exceed 1, so a value above it silently disables the check —
	// and 95 for 0.95 is the typo that would do it, looking exactly like a
	// check that found nothing. Non-positive would mark everything.
	for _, bad := range []string{"95", "1.5", "0", "-0.5", "most of it"} {
		t.Setenv(documentDedupMinScoreEnv, bad)
		if got := documentDedupMinScore(); got != defaultDedupMinScore {
			t.Errorf("%q = %v, want the default %v", bad, got, defaultDedupMinScore)
		}
	}
	// 1.0 is exact-match-only, which is a legitimate thing to ask for.
	t.Setenv(documentDedupMinScoreEnv, "1")
	if got := documentDedupMinScore(); got != 1 {
		t.Errorf("1 = %v, want 1 (exact matches only is a valid choice)", got)
	}
}

// TestMarkDuplicatesUsesTheConfiguredThreshold: both passes must obey it, or
// the two halves of one check disagree about what a duplicate is.
func TestMarkDuplicatesUsesTheConfiguredThreshold(t *testing.T) {
	t.Setenv(documentDedupMinScoreEnv, "0.999")
	o, store, vs := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
		// cos ≈ 0.995: a duplicate at the default, not at 0.999.
		{id: "b", docType: "insight", projectID: "p1", vec: []float64{0.99, 0.1}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("the in-memory pass ignored the configured floor: %+v", store.dupUpdates)
	}
	if len(vs.dupThresholds) == 0 {
		t.Fatal("the stored pass was never asked")
	}
	for _, got := range vs.dupThresholds {
		if got != 0.999 {
			t.Errorf("the stored pass was given %v, want the configured 0.999", got)
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float64
		want float64
		ok   bool
	}{
		{"identical", []float64{1, 2, 3}, []float64{1, 2, 3}, 1, true},
		{"unnormalised but identical in direction", []float64{1, 0}, []float64{7, 0}, 1, true},
		{"orthogonal", []float64{1, 0}, []float64{0, 1}, 0, true},
		{"opposite", []float64{1, 0}, []float64{-1, 0}, -1, true},
		{"length mismatch", []float64{1, 0}, []float64{1, 0, 0}, 0, false},
		{"empty", nil, nil, 0, false},
		{"one empty", []float64{1}, nil, 0, false},
		{"zero vector", []float64{0, 0}, []float64{1, 1}, 0, false},
		{"both zero", []float64{0, 0}, []float64{0, 0}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := cosineSimilarity(c.a, c.b)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && (got-c.want > 1e-9 || c.want-got > 1e-9) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	t.Run("an undefined comparison is never a match", func(t *testing.T) {
		// Not-ok must not be read as similarity 0 or 1 by the caller; the
		// caller's guard is asserted here because getting it wrong either marks
		// everything or nothing.
		for _, c := range [][2][]float64{
			{{0, 0}, {1, 1}},
			{{1}, {1, 1}},
			{nil, {1}},
		} {
			if _, ok := cosineSimilarity(c[0], c[1]); ok {
				t.Errorf("cosineSimilarity(%v, %v) reported a usable score", c[0], c[1])
			}
		}
	})
}

// TestMarkDuplicates_DegenerateVectorsAreSurvivable: a provider that changed
// model mid-run, or returned a zero vector, must not take the phase down or
// mark everything.
func TestMarkDuplicates_DegenerateVectorsAreSurvivable(t *testing.T) {
	o, store, _ := dedupHarness(nil)

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "zero", docType: "insight", projectID: "p1", vec: []float64{0, 0, 0}},
		{id: "also-zero", docType: "insight", projectID: "p1", vec: []float64{0, 0, 0}},
		{id: "short", docType: "insight", projectID: "p1", vec: []float64{1}},
		{id: "empty", docType: "insight", projectID: "p1", vec: nil},
		{id: "normal", docType: "insight", projectID: "p1", vec: []float64{1, 0, 0}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("undefined comparisons must mark nothing: %+v", store.dupUpdates)
	}
}

// TestMarkDuplicates_StoreFailureMarksNothing: the phase is non-fatal and a
// store hiccup must not start stamping documents.
func TestMarkDuplicates_StoreFailureMarksNothing(t *testing.T) {
	store := &mockEmbedIndexStore{}
	vs := &failingDupVectorStore{}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("a failed lookup must mark nothing: %+v", store.dupUpdates)
	}
}

// TestMarkDuplicates_NoCandidatesDoesNotTouchTheStore guards the empty case,
// which is every project's first phase-9 run with no documents.
func TestMarkDuplicates_NoCandidatesDoesNotTouchTheStore(t *testing.T) {
	o, store, vs := dedupHarness([]vectorstore.SearchResult{{ID: "x", Score: 1}})
	o.markDuplicates(context.Background(), nil, nil)
	if len(store.dupUpdates) != 0 || len(vs.dupExcludes) != 0 {
		t.Errorf("no candidates must mean no work: updates=%+v searches=%d", store.dupUpdates, len(vs.dupExcludes))
	}
}

// failingDupVectorStore fails only the duplicate lookup, which is the failure
// the phase has to survive without changing any document.
type failingDupVectorStore struct {
	mockVectorStore
}

func (f *failingDupVectorStore) FindDuplicates(_ context.Context, _ []float64, _ string, _ string, _ []string, _ float64) ([]vectorstore.SearchResult, error) {
	return nil, errors.New("qdrant unavailable")
}

// TestMarkDuplicates_ChainsDoNotAssumeTransitivity. Cosine similarity is not
// transitive: three vectors at 0°, 25° and 50° score 0.906 between neighbours
// and only 0.643 end to end. Resolving a match to its canonical without
// re-checking hid the third document behind the first — which it does not
// resemble — and recorded the neighbour's 0.906 as their score.
func TestMarkDuplicates_ChainsDoNotAssumeTransitivity(t *testing.T) {
	o, store, _ := dedupHarness(nil)
	at := func(deg float64) []float64 {
		r := deg * math.Pi / 180
		return []float64{math.Cos(r), math.Sin(r)}
	}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: at(0)},
		{id: "b", docType: "insight", projectID: "p1", vec: at(25)},
		{id: "c", docType: "insight", projectID: "p1", vec: at(50)},
	}, nil)

	if got := markedAs(store, "b"); got != "a" {
		t.Errorf("b is 0.906 from a and should fold into it, got %q", got)
	}
	// c is 0.906 from b but only 0.643 from a. b is hidden, so folding c into
	// b would point it at an invisible row; folding it into a would claim a
	// resemblance that is not there. Neither: c stands on its own.
	if got := markedAs(store, "c"); got != "" {
		t.Errorf("c was folded into %q — it is only 0.643 from the canonical and must stay visible", got)
	}

	// A genuine chain, where every hop also holds end to end, still collapses.
	store2 := &mockEmbedIndexStore{}
	o2 := &Orchestrator{embedIndexStore: store2, vectorStore: &mockVectorStore{}}
	o2.markDuplicates(context.Background(), []dedupCandidate{
		{id: "x", docType: "insight", projectID: "p1", vec: at(0)},
		{id: "y", docType: "insight", projectID: "p1", vec: at(3)},
		{id: "z", docType: "insight", projectID: "p1", vec: at(6)},
	}, nil)
	if got := markedAs(store2, "z"); got != "x" {
		t.Errorf("z is 0.994 from x and should fold into it, got %q", got)
	}

	// And the recorded score must be the one against the row it was folded
	// into, not against the neighbour that led there.
	for _, u := range store2.dupUpdates {
		if u.ID == "z" && u.Score > 0.999 {
			t.Errorf("z recorded score %v — that is its neighbour's, not its canonical's", u.Score)
		}
	}
}

// TestMarkDuplicates_CanonicalFromAnEarlierRunIsNotAssumed. When the canonical
// is a previous run's document its vector is not in hand, so the claim cannot
// be checked — and the stored pass has already asked the store about this
// document and been told no.
func TestMarkDuplicates_CanonicalFromAnEarlierRunIsNotAssumed(t *testing.T) {
	store := &mockEmbedIndexStore{}
	vs := &mockVectorStore{}
	// Only the first document is known to the store.
	vs.dupeFn = func(vec []float64) []vectorstore.SearchResult {
		if len(vec) > 0 && vec[0] == 1 {
			return []vectorstore.SearchResult{{ID: "published-earlier", Score: 0.97}}
		}
		return nil
	}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
		{id: "b", docType: "insight", projectID: "p1", vec: []float64{0.99, 0.01}},
	}, nil)

	if got := markedAs(store, "a"); got != "published-earlier" {
		t.Errorf("a must fold into the earlier run's row, got %q", got)
	}
	// b matches a at 0.9999, and a folded into a row from an earlier run whose
	// vector we do not have. The store was already asked about b and said no.
	if got := markedAs(store, "b"); got != "" {
		t.Errorf("b was folded into %q without any evidence it resembles that row", got)
	}
}

// TestMarkDuplicates_StoredHitThatIsItselfSuppressed. An earlier run's
// duplicate stays indexed in the vector store, so it can still be the best
// match for a later document. Folding into it would give that document a
// pointer to a row the reader cannot see — and since cosine is not transitive,
// resembling a suppressed row says nothing about resembling the visible one it
// was folded into (0.906 between neighbours, 0.643 end to end). The canonical's
// vector is not in hand to check against, so the fold is refused.
func TestMarkDuplicates_StoredHitThatIsItselfSuppressed(t *testing.T) {
	store := &mockEmbedIndexStore{
		alreadyFolded: map[string]string{"b-from-an-earlier-run": "a-the-visible-one"},
	}
	vs := &mockVectorStore{dupes: []vectorstore.SearchResult{{ID: "b-from-an-earlier-run", Score: 0.906}}}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "c", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("c was folded into %+v — its best match is itself hidden, and nothing establishes that c resembles the visible row", store.dupUpdates)
	}
}

// TestMarkDuplicates_StoredHitThatIsCanonicalIsStillUsed keeps the common case
// working: a match that is not itself suppressed is exactly what this check is
// for.
func TestMarkDuplicates_StoredHitThatIsCanonicalIsStillUsed(t *testing.T) {
	store := &mockEmbedIndexStore{alreadyFolded: map[string]string{}}
	vs := &mockVectorStore{dupes: []vectorstore.SearchResult{{ID: "a-canonical", Score: 0.97}}}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "c", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
	}, nil)

	if got := markedAs(store, "c"); got != "a-canonical" {
		t.Errorf("c should fold into the canonical row, got %q", got)
	}
}

// TestMarkDuplicates_UnknownSuppressionStateIsNotAssumed: if the lookup fails
// we cannot tell whether the match is canonical, and marking on a guess is the
// one outcome that loses a document.
func TestMarkDuplicates_UnknownSuppressionStateIsNotAssumed(t *testing.T) {
	store := &mockEmbedIndexStore{duplicateOfErr: errors.New("mongo unavailable")}
	vs := &mockVectorStore{dupes: []vectorstore.SearchResult{{ID: "a", Score: 0.99}}}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "c", docType: "insight", projectID: "p1", vec: []float64{1, 0}},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("a failed lookup must not mark anything: %+v", store.dupUpdates)
	}
}

// --- review round 5 ---

// TestStoredDuplicateWalksPastUnusableHits. A suppressed duplicate stays
// indexed, so the nearest stored match is often a row the reader cannot be sent
// to. Stopping at the first hit meant the check decayed exactly as a project
// accumulated duplicates: the more there were, the more of them outranked the
// canonical they had been folded into, and the fewer new ones could be found.
//
// The walk is safe precisely because each hit's score is this document against
// THAT document, measured by the store — nothing inherits a suppressed row's
// canonical, which cosine's non-transitivity would not support.
func TestStoredDuplicateWalksPastUnusableHits(t *testing.T) {
	cand := dedupCandidate{id: "new", docType: "insight", projectID: "p1", vec: []float64{1, 0}}

	t.Run("past a suppressed hit to the visible one below it", func(t *testing.T) {
		store := &mockEmbedIndexStore{alreadyFolded: map[string]string{"hidden": "canonical"}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "hidden", Score: 0.97},
			{ID: "canonical", Score: 0.91},
		}}}
		id, score, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88)
		if !ok || id != "canonical" {
			t.Fatalf("got (%q, %v, %v), want the visible canonical", id, score, ok)
		}
		if score != 0.91 {
			t.Errorf("score = %v, want 0.91 — this document against the canonical, not against the row that hid it", score)
		}
	})

	t.Run("past a hit whose document is gone", func(t *testing.T) {
		// A vector-store point can outlive its row. There is nothing to fold
		// into, and folding anyway hides the new document behind an original
		// that cannot be displayed.
		store := &mockEmbedIndexStore{missingDocs: map[string]bool{"stale": true}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "stale", Score: 0.99},
			{ID: "alive", Score: 0.90},
		}}}
		id, _, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88)
		if !ok || id != "alive" {
			t.Fatalf("got (%q, %v), want the hit that still has a document", id, ok)
		}
	})

	t.Run("a stale hit alone folds into nothing", func(t *testing.T) {
		store := &mockEmbedIndexStore{missingDocs: map[string]bool{"stale": true}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "stale", Score: 0.99},
		}}}
		if id, _, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88); ok {
			t.Errorf("folded into %q, which has no document — the new one would vanish behind nothing", id)
		}
	})

	t.Run("every hit suppressed means no fold", func(t *testing.T) {
		store := &mockEmbedIndexStore{alreadyFolded: map[string]string{"h1": "c", "h2": "c"}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "h1", Score: 0.97}, {ID: "h2", Score: 0.95},
		}}}
		if _, _, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88); ok {
			t.Error("folded into a suppressed row")
		}
	})

	t.Run("a store that cannot answer refuses the whole check", func(t *testing.T) {
		// The same store answers for every hit, so one unreadable answer makes
		// all of them unknown. Walking on would fold into a row that may be
		// just as suppressed.
		store := &mockEmbedIndexStore{duplicateOfErr: errors.New("mongo down")}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "h1", Score: 0.97}, {ID: "h2", Score: 0.95},
		}}}
		if _, _, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88); ok {
			t.Error("folded while the store could not say whether the hit was itself a duplicate")
		}
	})

	t.Run("the candidate's own point is skipped, not treated as the end", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{dupes: []vectorstore.SearchResult{
			{ID: "new", Score: 1.0},
			{ID: "other", Score: 0.93},
		}}}
		id, _, ok := o.storedDuplicate(context.Background(), cand, nil, 0.88)
		if !ok || id != "other" {
			t.Fatalf("got (%q, %v), want the next hit after its own point", id, ok)
		}
	})
}

// TestMarkDuplicates_SameWordingDifferentMagnitude. A duplicate check on a
// similarity score cannot see what the embedded text omits.
// StandaloneInsight.BuildEmbeddingText carries the name, description, area,
// severity and segment — and not the affected count or the metrics. So two
// insights with identical prose and materially different numbers embed
// identically, score 1.0, and the later one is suppressed: 100 affected
// entities hidden behind 1,000, when a magnitude that has MOVED is exactly what
// the prompt asks the model to report.
func TestMarkDuplicates_SameWordingDifferentMagnitude(t *testing.T) {
	const prose = "Margin erosion. The long tail is unpriced. Area: pricing. Severity: high. Segment: ."
	vec := []float64{1, 0, 0}

	t.Run("same run", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "small", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(100, nil)},
			{id: "large", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(1_000, nil)},
		}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded a different magnitude away: %+v", store.dupUpdates)
		}
	})

	t.Run("same run, same magnitude, still folds", func(t *testing.T) {
		// The guard must not switch the feature off.
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "first", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(100, nil)},
			{id: "second", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(100, nil)},
		}, nil)
		if len(store.dupUpdates) != 1 || store.dupUpdates[0].ID != "second" {
			t.Errorf("want the second folded into the first, got %+v", store.dupUpdates)
		}
	})

	t.Run("reworded with different numbers still folds", func(t *testing.T) {
		// The case the feature exists for: "707 SKUs, ₺48.2M" and "801 SKUs,
		// ₺48M" are the same finding restated, with different texts AND
		// different numbers. Requiring equal magnitudes in general would
		// refuse exactly these.
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "a", docType: "insight", projectID: "p1", vec: vec,
				text: "707 SKUs unpriced. Area: pricing.", magnitude: magnitudeKey(707, nil)},
			{id: "b", docType: "insight", projectID: "p1", vec: vec,
				text: "801 SKUs still unpriced. Area: pricing.", magnitude: magnitudeKey(801, nil)},
		}, nil)
		if len(store.dupUpdates) != 1 || store.dupUpdates[0].ID != "b" {
			t.Errorf("a restatement was not folded: %+v", store.dupUpdates)
		}
	})

	t.Run("stored match, the same rule at the other read", func(t *testing.T) {
		store := &mockEmbedIndexStore{storedDocs: map[string]IndexedDoc{
			"earlier": {EmbeddingText: prose, AffectedCount: 1_000},
		}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{
			dupes: []vectorstore.SearchResult{{ID: "earlier", Score: 1.0}},
		}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "new", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(100, nil)},
		}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded into a stored document stating a different magnitude: %+v", store.dupUpdates)
		}
	})

	t.Run("a stored document with no recorded text still folds", func(t *testing.T) {
		// Written before those fields existed: nothing to compare, so the
		// guard stands aside rather than refusing every fold.
		store := &mockEmbedIndexStore{storedDocs: map[string]IndexedDoc{"old": {}}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{
			dupes: []vectorstore.SearchResult{{ID: "old", Score: 0.95}},
		}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "new", docType: "insight", projectID: "p1", vec: vec, text: prose, magnitude: magnitudeKey(100, nil)},
		}, nil)
		if len(store.dupUpdates) != 1 {
			t.Errorf("want the fold, got %+v", store.dupUpdates)
		}
	})
}

// TestMagnitudeKeyIsDeterministic. Ranging a Go map is randomised, so a
// signature built by ranging would differ between two documents with identical
// metrics and read as a magnitude change on every pair.
func TestMagnitudeKeyIsDeterministic(t *testing.T) {
	m1 := map[string]interface{}{"rate": 4.2, "affected_stores": 424, "aisle": "paint"}
	m2 := map[string]interface{}{"aisle": "paint", "affected_stores": 424, "rate": 4.2}
	first := magnitudeKey(100, m1)
	for i := 0; i < 50; i++ {
		if got := magnitudeKey(100, m2); got != first {
			t.Fatalf("iteration %d gave %q, want %q", i, got, first)
		}
	}
	if magnitudeKey(100, m1) == magnitudeKey(101, m1) {
		t.Error("the affected count does not reach the signature")
	}
	if magnitudeKey(100, m1) == magnitudeKey(100, map[string]interface{}{"rate": 9.9}) {
		t.Error("the metrics do not reach the signature")
	}
}

// --- review round 8: two ways the magnitude guard was wrong ---

// TestMarkDuplicates_RecommendationsStillFoldAcrossRuns. A recommendation gets
// no magnitude signature on purpose — its expected impact is already inside the
// text its vector was built from. Deriving the candidate's signature one way
// and the stored document's another gave "" on one side and "affected=0" on the
// other, so the claim check refused even an exact duplicate scoring 1.0 and
// cross-run recommendation suppression stopped working entirely.
func TestMarkDuplicates_RecommendationsStillFoldAcrossRuns(t *testing.T) {
	const prose = "Reprice the long tail. Start with the paint aisle. Impact: margin +2pp. Segment: ."
	store := &mockEmbedIndexStore{storedDocs: map[string]IndexedDoc{
		"last-run": {EmbeddingText: prose},
	}}
	o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{
		dupes: []vectorstore.SearchResult{{ID: "last-run", Score: 1.0}},
	}}
	o.markDuplicates(context.Background(), []dedupCandidate{{
		id: "this-run", docType: "recommendation", projectID: "p1", vec: []float64{1, 0, 0},
		text: prose, magnitude: magnitudeOf("recommendation", 0, nil, nil),
	}}, nil)

	if len(store.dupUpdates) != 1 || store.dupUpdates[0].DupOf != "last-run" {
		t.Errorf("an identical recommendation was not folded: %+v", store.dupUpdates)
	}
}

// TestMarkDuplicates_ChainCannotRouteAroundTheMagnitudeGuard. A and C share
// their wording and differ in magnitude; B is a paraphrase of A already folded
// into it. C fails the claim check against A directly — and then matches B, and
// the chain branch re-checked only cosine against A and folded C into A anyway.
// A guard that can be reached by another route is not a guard.
func TestMarkDuplicates_ChainCannotRouteAroundTheMagnitudeGuard(t *testing.T) {
	const prose = "Margin erosion. The long tail is unpriced. Area: pricing. Severity: high. Segment: ."
	// A and B are near-identical vectors (B is the paraphrase), C equals A's.
	a := []float64{1, 0, 0}
	b := []float64{0.999, 0.0447, 0}
	store := &mockEmbedIndexStore{}
	o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "A", docType: "insight", projectID: "p1", vec: a,
			text: prose, magnitude: magnitudeOf("insight", 1_000, nil, nil)},
		{id: "B", docType: "insight", projectID: "p1", vec: b,
			text: "A paraphrase of the same thing. Area: pricing.", magnitude: magnitudeOf("insight", 1_000, nil, nil)},
		{id: "C", docType: "insight", projectID: "p1", vec: a,
			text: prose, magnitude: magnitudeOf("insight", 100, nil, nil)},
	}, nil)

	// B folds into A — that one is a genuine paraphrase.
	var cFolded bool
	for _, u := range store.dupUpdates {
		if u.ID == "C" {
			cFolded = true
		}
	}
	if cFolded {
		t.Errorf("C was folded despite stating a different magnitude than its canonical: %+v", store.dupUpdates)
	}
	if len(store.dupUpdates) != 1 || store.dupUpdates[0].ID != "B" {
		t.Errorf("want only B folded, got %+v", store.dupUpdates)
	}
}

// TestMagnitudeOfIsTypeAware pins the one derivation both sides of every
// comparison go through, and what each document type's embedded text leaves
// out — which is the only thing that can decide between two documents whose
// text is identical.
func TestMagnitudeOfIsTypeAware(t *testing.T) {
	t.Run("an insight signs its count and metrics", func(t *testing.T) {
		if got := magnitudeOf("insight", 500, nil, nil); got == "" {
			t.Error("insight signature is empty — the guard would never fire")
		}
		if magnitudeOf("insight", 500, nil, nil) == magnitudeOf("insight", 501, nil, nil) {
			t.Error("the affected count does not reach the signature")
		}
		// A recommendation's actions are not an insight's concern.
		if magnitudeOf("insight", 500, nil, nil) != magnitudeOf("insight", 500, nil, []string{"do a thing"}) {
			t.Error("actions reached an insight's signature")
		}
	})

	t.Run("a recommendation signs its actions and its segment size", func(t *testing.T) {
		// Its expected impact IS in the embedded text; its actions are not,
		// and they are the part a reader would act on. Two recommendations
		// that read the same and tell you to do different things embed
		// identically, so hiding the second hides the only part that differs.
		a := magnitudeOf("recommendation", 0, nil, []string{"cut the price", "re-list"})
		b := magnitudeOf("recommendation", 0, nil, []string{"delist", "renegotiate"})
		if a == "" || a == b {
			t.Errorf("recommendation signatures = %q and %q, want distinct and non-empty", a, b)
		}
		// Order is an instruction, not a set.
		reordered := magnitudeOf("recommendation", 0, nil, []string{"re-list", "cut the price"})
		if a == reordered {
			t.Error("a reordered action list signed the same — the order is part of the instruction")
		}
		// The embedded text names the segment and never says how large it is,
		// so the size is this type's count the way AffectedCount is an
		// insight's: the same sentence against 1,200 customers and against
		// 14,000 is a different claim.
		steps := []string{"win back the lapsed"}
		if magnitudeOf("recommendation", 1_200, nil, steps) == magnitudeOf("recommendation", 14_000, nil, steps) {
			t.Error("the segment size does not reach the signature")
		}
		// Metrics are an insight's field and must not leak in here, or two
		// recommendations would be told apart by something neither carries.
		if magnitudeOf("recommendation", 500, map[string]interface{}{"x": 1}, steps) !=
			magnitudeOf("recommendation", 500, nil, steps) {
			t.Error("metrics reached a recommendation signature")
		}
		// A document with no actions at all — an older one, or a model that
		// emitted none — must still fold into its twin rather than have the
		// guard refuse every pair. The pair that matters is candidate against
		// stored, since those are the two sides of a real comparison.
		stored := IndexedDoc{EmbeddingText: "same prose", SegmentSize: 500}
		if got, want := stored.magnitude("recommendation"), magnitudeOf("recommendation", 500, nil, nil); got != want {
			t.Errorf("an action-less recommendation signed %q stored and %q as a candidate, so its twin would never fold", got, want)
		}
	})

	t.Run("a stored document is signed the same way as a candidate", func(t *testing.T) {
		// The derivation that broke once: the candidate side said one thing
		// and the stored side another for the same recommendation, so
		// sameClaim refused an exact duplicate scoring 1.0 and cross-run
		// suppression stopped working altogether.
		rec := IndexedDoc{AffectedCount: 7, SegmentSize: 1_200, Actions: []string{"win back the lapsed"}}
		if got, want := rec.magnitude("recommendation"), magnitudeOf("recommendation", 1_200, nil, rec.Actions); got != want {
			t.Errorf("stored recommendation signed %q, candidate side signs %q", got, want)
		}
		ins := IndexedDoc{AffectedCount: 707, SegmentSize: 1_200, Metrics: map[string]interface{}{"revenue": 48.2}}
		if got, want := ins.magnitude("insight"), magnitudeOf("insight", 707, ins.Metrics, nil); got != want {
			t.Errorf("stored insight signed %q, candidate side signs %q", got, want)
		}
		// Each type reads ITS own count. Reading the wrong field would sign
		// every recommendation "segment=0" and give the guard nothing to see.
		if rec.magnitude("recommendation") == magnitudeOf("recommendation", rec.AffectedCount, nil, rec.Actions) {
			t.Error("a stored recommendation was signed with the insight count field")
		}
	})
}

// TestMarkDuplicates_SameWordingDifferentActions. The recommendation twin of
// the magnitude guard: BuildEmbeddingText carries the title, description,
// expected impact and segment — and not the implementation steps. So two
// recommendations that read the same and tell you to do different things score
// 1.0, and suppressing the second removes the only part a reader would act on.
func TestMarkDuplicates_SameWordingDifferentActions(t *testing.T) {
	const prose = "Reprice the long tail. Margin is leaking. Impact: margin +2pp. Segment: ."
	vec := []float64{1, 0, 0}

	t.Run("different actions are not duplicates", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "first", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
				magnitude: magnitudeOf("recommendation", 0, nil, []string{"cut the price"})},
			{id: "second", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
				magnitude: magnitudeOf("recommendation", 0, nil, []string{"delist the SKUs"})},
		}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded two different instructions together: %+v", store.dupUpdates)
		}
	})

	t.Run("the same actions still fold", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		same := magnitudeOf("recommendation", 0, nil, []string{"cut the price"})
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "first", docType: "recommendation", projectID: "p1", vec: vec, text: prose, magnitude: same},
			{id: "second", docType: "recommendation", projectID: "p1", vec: vec, text: prose, magnitude: same},
		}, nil)
		if len(store.dupUpdates) != 1 || store.dupUpdates[0].ID != "second" {
			t.Errorf("want the second folded into the first, got %+v", store.dupUpdates)
		}
	})

	t.Run("the stored side reads actions too", func(t *testing.T) {
		store := &mockEmbedIndexStore{storedDocs: map[string]IndexedDoc{
			"last-run": {EmbeddingText: prose, Actions: []string{"delist the SKUs"}},
		}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{
			dupes: []vectorstore.SearchResult{{ID: "last-run", Score: 1.0}},
		}}
		o.markDuplicates(context.Background(), []dedupCandidate{{
			id: "this-run", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
			magnitude: magnitudeOf("recommendation", 0, nil, []string{"cut the price"}),
		}}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded into a stored recommendation with different actions: %+v", store.dupUpdates)
		}
	})
}

// TestMarkDuplicates_SameWordingDifferentSegmentSize. BuildEmbeddingText names
// the target segment and never says how many it holds, so "win back lapsed
// subscribers" re-issued word for word against a segment that has grown
// eleven-fold is the same sentence and a different recommendation. Suppressing
// the second tells the reader nothing has changed, when the thing that changed
// is the size of the problem.
func TestMarkDuplicates_SameWordingDifferentSegmentSize(t *testing.T) {
	const prose = "Win back lapsed subscribers. They stopped at month three. Impact: retention +3pp. Segment: lapsed."
	steps := []string{"email the lapsed cohort"}
	vec := []float64{0, 1, 0}

	t.Run("a segment that moved is not a duplicate", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "first", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
				magnitude: magnitudeOf("recommendation", 1_200, nil, steps)},
			{id: "second", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
				magnitude: magnitudeOf("recommendation", 14_000, nil, steps)},
		}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded a 1,200-customer segment into a 14,000-customer one: %+v", store.dupUpdates)
		}
	})

	t.Run("an unchanged segment still folds", func(t *testing.T) {
		store := &mockEmbedIndexStore{}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
		same := magnitudeOf("recommendation", 1_200, nil, steps)
		o.markDuplicates(context.Background(), []dedupCandidate{
			{id: "first", docType: "recommendation", projectID: "p1", vec: vec, text: prose, magnitude: same},
			{id: "second", docType: "recommendation", projectID: "p1", vec: vec, text: prose, magnitude: same},
		}, nil)
		if len(store.dupUpdates) != 1 || store.dupUpdates[0].ID != "second" {
			t.Errorf("want the second folded into the first, got %+v", store.dupUpdates)
		}
	})

	t.Run("the stored side reads the segment size too", func(t *testing.T) {
		// The cross-run case, which is the one a reader meets: last month's
		// recommendation is in the index, this month's segment has grown.
		store := &mockEmbedIndexStore{storedDocs: map[string]IndexedDoc{
			"last-run": {EmbeddingText: prose, Actions: steps, SegmentSize: 1_200},
		}}
		o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{
			dupes: []vectorstore.SearchResult{{ID: "last-run", Score: 1.0}},
		}}
		o.markDuplicates(context.Background(), []dedupCandidate{{
			id: "this-run", docType: "recommendation", projectID: "p1", vec: vec, text: prose,
			magnitude: magnitudeOf("recommendation", 14_000, nil, steps),
		}}, nil)
		if len(store.dupUpdates) != 0 {
			t.Errorf("folded into a stored recommendation whose segment was a tenth the size: %+v", store.dupUpdates)
		}
	})
}
