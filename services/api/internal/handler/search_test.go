package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// mockProjectRepoForSearch implements ProjectRepo with embedding config.
type mockProjectRepoForSearch struct {
	project  *models.Project
	projects []*models.Project
}

func (m *mockProjectRepoForSearch) Create(_ context.Context, _ *models.Project) error { return nil }
func (m *mockProjectRepoForSearch) GetByID(_ context.Context, id string) (*models.Project, error) {
	if m.project != nil && m.project.ID == id {
		// Default schema-index status to "ready" so /ask tests that don't
		// care about gating still see 200 responses. Tests covering the
		// gate set SchemaIndexStatus explicitly on the fixture.
		p := *m.project
		if p.SchemaIndexStatus == "" {
			p.SchemaIndexStatus = models.SchemaIndexStatusReady
		}
		return &p, nil
	}
	return nil, context.DeadlineExceeded
}
func (m *mockProjectRepoForSearch) List(_ context.Context, _, _ int) ([]*models.Project, error) {
	if m.projects != nil {
		return m.projects, nil
	}
	return nil, nil
}
func (m *mockProjectRepoForSearch) Update(_ context.Context, _ string, _ *models.Project) error {
	return nil
}
func (m *mockProjectRepoForSearch) Delete(_ context.Context, _ string) error        { return nil }
func (m *mockProjectRepoForSearch) DeleteCascade(_ context.Context, _ string) error { return nil }
func (m *mockProjectRepoForSearch) Count(_ context.Context) (int, error)            { return 0, nil }
func (m *mockProjectRepoForSearch) CountWithWarehouse(_ context.Context) (int, error) {
	return 0, nil
}
func (m *mockProjectRepoForSearch) SetSchemaIndexStatus(_ context.Context, _, _, _ string) error {
	return nil
}

// mockVectorStoreForSearch returns pre-set search results.
type mockVectorStoreForSearch struct {
	results []vectorstore.SearchResult
}

func (m *mockVectorStoreForSearch) Upsert(_ context.Context, _ []vectorstore.Point) error {
	return nil
}
func (m *mockVectorStoreForSearch) Search(_ context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	return m.results, nil
}
func (m *mockVectorStoreForSearch) FindDuplicates(_ context.Context, _ []float64, _, _, _ string, _ float64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (m *mockVectorStoreForSearch) Delete(_ context.Context, _ []string) error      { return nil }
func (m *mockVectorStoreForSearch) HealthCheck(_ context.Context) error              { return nil }
func (m *mockVectorStoreForSearch) EnsureCollection(_ context.Context, _ int) error  { return nil }
func (m *mockVectorStoreForSearch) SearchSchemaIndex(_ context.Context, _ string, _ []float64, _ int) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

// mockSearchHistoryRepo discards all saves.
type mockSearchHistoryRepo struct{}

func (m *mockSearchHistoryRepo) Save(_ context.Context, _ *commonmodels.SearchHistory) error {
	return nil
}
func (m *mockSearchHistoryRepo) ListByUser(_ context.Context, _ string, _ int) ([]*commonmodels.SearchHistory, error) {
	return nil, nil
}
func (m *mockSearchHistoryRepo) ListByProject(_ context.Context, _ string, _ int) ([]*commonmodels.SearchHistory, error) {
	return nil, nil
}

// mockAskSessionRepo implements AskSessionRepo for testing.
type mockAskSessionRepo struct {
	session *commonmodels.AskSession
}

func (m *mockAskSessionRepo) Create(_ context.Context, _ *commonmodels.AskSession) error {
	return nil
}
func (m *mockAskSessionRepo) AppendMessage(_ context.Context, _ string, _ commonmodels.AskSessionMessage) error {
	return nil
}
func (m *mockAskSessionRepo) GetByID(_ context.Context, id string) (*commonmodels.AskSession, error) {
	if m.session != nil && m.session.ID == id {
		return m.session, nil
	}
	return nil, fmt.Errorf("session not found")
}
func (m *mockAskSessionRepo) ListByProject(_ context.Context, _ string, _ int) ([]*commonmodels.AskSession, error) {
	return nil, nil
}
func (m *mockAskSessionRepo) Delete(_ context.Context, _ string) error { return nil }

// mockSecretProviderForSearch returns a pre-set API key.
type mockSecretProviderForSearch struct{}

func (m *mockSecretProviderForSearch) Get(_ context.Context, _, _ string) (string, error) {
	return "test-key", nil
}
func (m *mockSecretProviderForSearch) Set(_ context.Context, _, _, _ string) error   { return nil }
func (m *mockSecretProviderForSearch) Delete(_ context.Context, _, _ string) error   { return nil }
func (m *mockSecretProviderForSearch) List(_ context.Context, _ string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

func init() {
	// Register a mock embedding provider for tests
	goembedding.RegisterWithMeta("test-embedding", func(cfg goembedding.ProviderConfig) (goembedding.Provider, error) {
		return &testEmbeddingProvider{}, nil
	}, goembedding.ProviderMeta{
		ID:   "test-embedding",
		Name: "Test Embedding",
		Models: []goembedding.ModelInfo{
			{ID: "test-model", Dimensions: 3},
		},
	})

	// Register a mock LLM provider for Ask handler tests. The factory
	// returns a fresh testLLMProvider per construction so each request
	// gets its own request recorder — the /ask handler only constructs
	// one provider per call, so per-request isolation is what we want
	// for assertions on the captured ChatRequest.
	gollm.Register("test-llm", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		// Most tests don't read the recorder; the language test does.
		// Storing the latest provider in lastTestLLM lets the language
		// test pull the captured request without threading a recorder
		// through every existing test that doesn't care.
		p := &testLLMProvider{}
		lastTestLLMMu.Lock()
		lastTestLLM = p
		lastTestLLMMu.Unlock()
		return p, nil
	})
}

// lastTestLLM exposes the most recently constructed testLLMProvider so
// tests can assert on the ChatRequest the /ask handler sent without
// having to inject a custom factory at every test site.
var (
	lastTestLLMMu sync.Mutex
	lastTestLLM   *testLLMProvider
)

type testLLMProvider struct {
	mu       sync.Mutex
	requests []gollm.ChatRequest
}

// LastChatRequest returns the most recent ChatRequest captured by Chat,
// or the zero ChatRequest if none has been called yet.
func (t *testLLMProvider) LastChatRequest() gollm.ChatRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.requests) == 0 {
		return gollm.ChatRequest{}
	}
	return t.requests[len(t.requests)-1]
}

func (t *testLLMProvider) Chat(_ context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req)
	t.mu.Unlock()
	return &gollm.ChatResponse{
		Content: "Based on the insights [1], the answer is clear.",
		Model:   "test-llm-model",
		Usage:   gollm.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}
func (t *testLLMProvider) Validate(_ context.Context) error { return nil }

type testEmbeddingProvider struct{}

func (t *testEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i := range texts {
		result[i] = []float64{0.1, 0.2, 0.3}
	}
	return result, nil
}
func (t *testEmbeddingProvider) Dimensions() int        { return 3 }
func (t *testEmbeddingProvider) ModelName() string       { return "test-model" }
func (t *testEmbeddingProvider) Validate(_ context.Context) error { return nil }

func TestSearchHandler_Search(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"

	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:   "proj-1",
			Name: "Test Project",
			Embedding: goembedding.ProjectConfig{
				Provider: "test-embedding",
				Model:    "test-model",
			},
		},
	}

	insightRepo := &mockInsightRepo{
		insights: []*commonmodels.StandaloneInsight{
			{
				ID:           insightID,
				ProjectID:    "proj-1",
				DiscoveryID:  "disc-1",
				Name:         "High churn",
				Description:  "Players leaving",
				Severity:     "high",
				AnalysisArea: "churn",
				DiscoveredAt: time.Now(),
			},
		},
	}

	vs := &mockVectorStoreForSearch{
		results: []vectorstore.SearchResult{
			{
				ID:    insightID,
				Score: 0.89,
				Payload: map[string]interface{}{
					"type": "insight",
				},
			},
		},
	}

	h := NewSearchHandler(
		projectRepo,
		insightRepo,
		&mockRecommendationRepo{},
		&mockSearchHistoryRepo{},
		&mockAskSessionRepo{},
		&mockSecretProviderForSearch{},
		vs,
	)

	body, _ := json.Marshal(searchRequest{
		Query: "why are players leaving?",
		Limit: 10,
	})

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/search", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Search(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	results := data["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0].(map[string]interface{})
	if result["id"] != insightID {
		t.Errorf("expected ID %s, got %v", insightID, result["id"])
	}
	if result["score"].(float64) < 0.8 {
		t.Errorf("expected score >= 0.8, got %v", result["score"])
	}
	if result["name"] != "High churn" {
		t.Errorf("expected name=High churn, got %v", result["name"])
	}
}

func TestSearchHandler_NoVectorStore(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, nil) // no Qdrant

	body, _ := json.Marshal(searchRequest{Query: "test"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/search", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Search(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestSearchHandler_EmptyQuery(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})

	body, _ := json.Marshal(searchRequest{Query: ""})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/search", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Search(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSearchHandler_NoEmbeddingConfig(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:   "proj-1",
			Name: "No Embedding",
		},
	}

	h := NewSearchHandler(projectRepo, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})

	body, _ := json.Marshal(searchRequest{Query: "test"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/search", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Search(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// --- CrossProjectSearch tests ---

func TestCrossProjectSearch_NoVectorStore(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, nil)
	body, _ := json.Marshal(crossSearchRequest{Query: "test", EmbeddingModel: "test-model"})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CrossProjectSearch(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestCrossProjectSearch_MissingModel(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})
	body, _ := json.Marshal(crossSearchRequest{Query: "test"})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CrossProjectSearch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCrossProjectSearch_EmptyQuery(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})
	body, _ := json.Marshal(crossSearchRequest{Query: "", EmbeddingModel: "test-model"})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CrossProjectSearch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCrossProjectSearch_NoMatchingProjects(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		projects: []*models.Project{
			{ID: "proj-1", Name: "P1", Embedding: goembedding.ProjectConfig{Provider: "openai", Model: "other-model"}},
		},
	}
	h := NewSearchHandler(projectRepo, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})
	body, _ := json.Marshal(crossSearchRequest{Query: "test", EmbeddingModel: "test-model"})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CrossProjectSearch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	results := data["results"].([]interface{})
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestCrossProjectSearch_Success(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		projects: []*models.Project{
			{ID: "proj-1", Name: "Project One", Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"}},
			{ID: "proj-2", Name: "Project Two", Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"}},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: "ins-1", Score: 0.85, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: "ins-1", ProjectID: "proj-1", Name: "Cross insight", DiscoveryID: "disc-1"},
	}}
	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(crossSearchRequest{Query: "test", EmbeddingModel: "test-model", Limit: 10})
	req := httptest.NewRequest("POST", "/api/v1/search", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CrossProjectSearch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["projects_searched"].(float64) != 2 {
		t.Errorf("expected 2 projects searched, got %v", data["projects_searched"])
	}
}

// --- ListHistory tests ---

func TestListHistory_Success(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, &mockSearchHistoryRepo{}, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/search/history", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.ListHistory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListHistory_InvalidLimit(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, &mockSearchHistoryRepo{}, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/search/history?limit=abc", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.ListHistory(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// --- ListAskSessions tests ---

func TestListAskSessions_Success(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{}, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/ask/sessions", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.ListAskSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListAskSessions_InvalidLimit(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{}, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/ask/sessions?limit=xyz", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.ListAskSessions(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// --- GetAskSession tests ---

func TestGetAskSession_Success(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "s1", ProjectID: "proj-1", Title: "Test"},
	}, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/ask/sessions/s1", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("sessionId", "s1")
	w := httptest.NewRecorder()
	h.GetAskSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetAskSession_WrongProject(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "s1", ProjectID: "proj-2", Title: "Test"},
	}, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/ask/sessions/s1", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("sessionId", "s1")
	w := httptest.NewRecorder()
	h.GetAskSession(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetAskSession_NotFound(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{}, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/projects/proj-1/ask/sessions/nope", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("sessionId", "nope")
	w := httptest.NewRecorder()
	h.GetAskSession(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// --- DeleteAskSession tests ---

func TestDeleteAskSession_Success(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "s1", ProjectID: "proj-1"},
	}, nil, nil)
	req := httptest.NewRequest("DELETE", "/api/v1/projects/proj-1/ask/sessions/s1", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("sessionId", "s1")
	w := httptest.NewRecorder()
	h.DeleteAskSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteAskSession_WrongProject(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "s1", ProjectID: "proj-2"},
	}, nil, nil)
	req := httptest.NewRequest("DELETE", "/api/v1/projects/proj-1/ask/sessions/s1", nil)
	req.SetPathValue("id", "proj-1")
	req.SetPathValue("sessionId", "s1")
	w := httptest.NewRecorder()
	h.DeleteAskSession(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// --- Ask handler tests ---

func TestAsk_NoVectorStore(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, nil)
	body, _ := json.Marshal(askRequest{Question: "test"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestAsk_EmptyQuestion(t *testing.T) {
	h := NewSearchHandler(nil, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})
	body, _ := json.Marshal(askRequest{Question: ""})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAsk_NoEmbeddingConfig(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{ID: "proj-1", Name: "No Embedding"},
	}
	h := NewSearchHandler(projectRepo, nil, nil, nil, nil, nil, &mockVectorStoreForSearch{})
	body, _ := json.Marshal(askRequest{Question: "test"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", w.Code, w.Body.String())
	}
	// The dashboard branches on Code rather than Error, so verify
	// the typed code is set and stable.
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeEmbeddingNotConfigured {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeEmbeddingNotConfigured)
	}
}

func TestAsk_NoResults(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID: "proj-1", Name: "Test",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm", Model: "test-llm-model"},
		},
	}
	vs := &mockVectorStoreForSearch{results: nil} // no results
	h := NewSearchHandler(projectRepo, &mockInsightRepo{}, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "anything"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if answer, ok := data["answer"].(string); !ok || answer == "" {
		t.Error("expected a 'no results' answer")
	}
}

func TestAsk_Success(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID: "proj-1", Name: "Test",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm", Model: "test-llm-model"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "High churn", Description: "Players leaving", Severity: "high", DiscoveredAt: time.Now()},
	}}
	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "why are players leaving?"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["answer"] == "" {
		t.Error("expected non-empty answer")
	}
	if data["session_id"] == "" {
		t.Error("expected session_id")
	}
	sources := data["sources"].([]interface{})
	if len(sources) != 1 {
		t.Errorf("expected 1 source, got %d", len(sources))
	}
}

func TestAsk_SessionProjectMismatch(t *testing.T) {
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID: "proj-1", Name: "Test",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm", Model: "test-llm-model"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: "ins-1", Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: "ins-1", ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "Test", Description: "Desc"},
	}}
	sessionRepo := &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "wrong-session", ProjectID: "proj-2"},
	}
	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, sessionRepo, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "test", SessionID: "wrong-session"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for session project mismatch, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAsk_SystemPromptIncludesLanguageDirective locks in PR #188's
// language wiring: the /ask system prompt must (a) name the project's
// effective language verbatim so the model writes the answer in it,
// (b) explicitly tell the model not to mirror retrieved-context
// language, and (c) keep technical-token / citation-marker instructions
// intact. Without this contract, swapping Language has no observable
// effect on /ask answers and the feature is silently broken.
func TestAsk_SystemPromptIncludesLanguageDirective(t *testing.T) {
	cases := []struct {
		name     string
		language string
		expect   string // substring expected verbatim in SystemPrompt
	}{
		{name: "explicit Turkish", language: "Turkish", expect: "Turkish"},
		{name: "explicit Japanese", language: "Japanese", expect: "Japanese"},
		// Empty Language must resolve to "English" via EffectiveLanguage —
		// legacy projects keep the default behavior.
		{name: "legacy empty resolves English", language: "", expect: "English"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			insightID := "11111111-1111-4111-8111-111111111111"
			projectRepo := &mockProjectRepoForSearch{
				project: &models.Project{
					ID: "proj-1", Name: "Test", Language: tc.language,
					Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
					LLM:       models.LLMConfig{Provider: "test-llm", Model: "test-llm-model"},
				},
			}
			vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
				{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
			}}
			insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
				{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high", DiscoveredAt: time.Now()},
			}}
			h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)

			body, _ := json.Marshal(askRequest{Question: "why?"})
			req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
			req.SetPathValue("id", "proj-1")
			w := httptest.NewRecorder()
			h.Ask(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
			}

			lastTestLLMMu.Lock()
			provider := lastTestLLM
			lastTestLLMMu.Unlock()
			if provider == nil {
				t.Fatal("no testLLMProvider was constructed by /ask")
			}
			got := provider.LastChatRequest()
			if got.SystemPrompt == "" {
				t.Fatal("SystemPrompt was empty — handler did not pass a system prompt")
			}
			if !strings.Contains(got.SystemPrompt, tc.expect) {
				t.Errorf("SystemPrompt missing language %q\nfull prompt: %q", tc.expect, got.SystemPrompt)
			}
			// Translate-don't-mirror clause: this is the key prompt-injection /
			// cross-lingual-pollution guard. If a future refactor drops it,
			// the model would mirror the retrieved context's language.
			if !strings.Contains(got.SystemPrompt, "translate") || !strings.Contains(got.SystemPrompt, "do not mirror") {
				t.Errorf("SystemPrompt missing translate/don't-mirror clause: %q", got.SystemPrompt)
			}
			// Citation contract must survive the language wiring.
			if !strings.Contains(got.SystemPrompt, "[1]") || !strings.Contains(got.SystemPrompt, "[s1]") {
				t.Errorf("SystemPrompt missing citation markers: %q", got.SystemPrompt)
			}
			// Technical-token English clause — ensures SQL/identifiers don't
			// get translated when language is non-English.
			if !strings.Contains(strings.ToLower(got.SystemPrompt), "english") {
				t.Errorf("SystemPrompt missing keep-technical-tokens-English clause: %q", got.SystemPrompt)
			}
		})
	}
}
