package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

type recordingSourcesProvider struct {
	chunks  []gosources.Chunk
	gotProj string
	gotQ    string
	gotOpts gosources.RetrieveOpts
}

func (r *recordingSourcesProvider) RetrieveContext(_ context.Context, projectID, query string, opts gosources.RetrieveOpts) ([]gosources.Chunk, error) {
	r.gotProj = projectID
	r.gotQ = query
	r.gotOpts = opts
	return r.chunks, nil
}

// askProjectRepo bundles a project that has embedding configured for /ask tests.
func askProjectRepo() *mockProjectRepoForSearch {
	return &mockProjectRepoForSearch{
		project: &models.Project{
			ID:   "proj-1",
			Name: "Test Project",
			Embedding: goembedding.ProjectConfig{
				Provider: "test-embedding",
				Model:    "test-model",
			},
			LLM: models.LLMConfig{
				Provider: "test-llm",
				Model:    "test-llm-model",
			},
		},
	}
}

func TestAsk_KnowledgeSourcesIncluded(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	insightID := "11111111-1111-4111-8111-111111111111"

	stub := &recordingSourcesProvider{
		chunks: []gosources.Chunk{
			{
				SourceID: "src-1", SourceName: "handbook.pdf", SourceType: "pdf",
				Text: "Retention is measured weekly across all live cohorts.",
				Score: 0.87, Metadata: map[string]string{"page": "12"},
			},
		},
	}
	gosources.SetProviderForTest(stub)

	insightRepo := &mockInsightRepo{
		insights: []*commonmodels.StandaloneInsight{{
			ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1",
			Name: "High churn", Description: "Players leaving", Severity: "high",
			AnalysisArea: "churn", DiscoveredAt: time.Now(),
		}},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{{
		ID: insightID, Score: 0.89,
		Payload: map[string]interface{}{"type": "insight"},
	}}}

	h := NewSearchHandler(askProjectRepo(), insightRepo, &mockRecommendationRepo{},
		&mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)

	body, _ := json.Marshal(askRequest{Question: "How do we measure retention?", Limit: 5})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Ask(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data := resp.Data.(map[string]interface{})
	sources := data["sources"].([]interface{})

	if len(sources) != 2 {
		t.Fatalf("expected 2 sources (1 insight + 1 source_chunk), got %d", len(sources))
	}

	// Find the source_chunk in the response.
	var foundChunk bool
	for _, raw := range sources {
		s := raw.(map[string]interface{})
		if s["type"] == "source_chunk" {
			foundChunk = true
			if s["name"] != "handbook.pdf" {
				t.Errorf("source name = %v, want handbook.pdf", s["name"])
			}
			if s["id"] != "src-1#0" {
				t.Errorf("source id = %v, want src-1#0", s["id"])
			}
		}
	}
	if !foundChunk {
		t.Errorf("expected a source_chunk in sources list, got: %#v", sources)
	}

	// Verify the provider was called with the user's question.
	if stub.gotQ != "How do we measure retention?" {
		t.Errorf("provider received query %q, want %q", stub.gotQ, "How do we measure retention?")
	}
	if stub.gotProj != "proj-1" {
		t.Errorf("provider received project %q, want proj-1", stub.gotProj)
	}
	if stub.gotOpts.Limit != askKnowledgeTopK {
		t.Errorf("provider Limit = %d, want %d", stub.gotOpts.Limit, askKnowledgeTopK)
	}
}

func TestAsk_OnlyKnowledgeSourcesNoInsights(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &recordingSourcesProvider{
		chunks: []gosources.Chunk{
			{SourceID: "src-1", SourceName: "glossary.md", Text: "Cohort: a group of users sharing a signup week.", Score: 0.7},
		},
	}
	gosources.SetProviderForTest(stub)

	// No insight results from Qdrant.
	vs := &mockVectorStoreForSearch{results: nil}

	h := NewSearchHandler(askProjectRepo(), &mockInsightRepo{}, &mockRecommendationRepo{},
		&mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)

	body, _ := json.Marshal(askRequest{Question: "what is a cohort?"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Ask(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := resp.Data.(map[string]interface{})

	answer := data["answer"].(string)
	if strings.Contains(strings.ToLower(answer), "no relevant") {
		t.Errorf("expected synthesized answer when knowledge sources are present; got fallback message: %q", answer)
	}

	sources := data["sources"].([]interface{})
	if len(sources) != 1 {
		t.Fatalf("expected 1 source (knowledge chunk), got %d", len(sources))
	}
	s := sources[0].(map[string]interface{})
	if s["type"] != "source_chunk" {
		t.Errorf("expected type=source_chunk, got %v", s["type"])
	}
}

func TestAsk_NoInsightsNoSourcesReturnsFallbackMessage(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()
	// Default NoOp provider — no chunks.

	vs := &mockVectorStoreForSearch{results: nil}

	h := NewSearchHandler(askProjectRepo(), &mockInsightRepo{}, &mockRecommendationRepo{},
		&mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)

	body, _ := json.Marshal(askRequest{Question: "anything?"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()

	h.Ask(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := resp.Data.(map[string]interface{})
	answer := data["answer"].(string)
	if !strings.Contains(strings.ToLower(answer), "no relevant") {
		t.Errorf("expected fallback message when nothing is retrieved, got: %q", answer)
	}
}

func TestTruncateForSession(t *testing.T) {
	short := "this fits"
	if got := truncateForSession(short); got != short {
		t.Errorf("truncateForSession(short) = %q, want unchanged", got)
	}

	long := strings.Repeat("a", 500)
	got := truncateForSession(long)
	// "…" is a 3-byte UTF-8 rune, so the truncated string is 400 ASCII bytes
	// plus 3 bytes for the ellipsis = 403 bytes total.
	if len(got) != 403 {
		t.Errorf("truncateForSession byte length = %d, want 403", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateForSession(long) should end with ellipsis, got suffix %q", got[len(got)-3:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 400)) {
		t.Errorf("truncateForSession(long) should preserve first 400 chars")
	}

	// Multi-byte runes (Portuguese, CJK): truncation must not split a rune.
	// Each "ç" is 2 bytes, so a 500-rune input is 1000 bytes but only 500 runes.
	multibyte := strings.Repeat("ç", 500)
	mb := truncateForSession(multibyte)
	if !utf8.ValidString(mb) {
		t.Errorf("truncateForSession returned invalid UTF-8 for multi-byte input")
	}
	if mbRunes := []rune(mb); len(mbRunes) != 401 { // 400 runes + "…"
		t.Errorf("truncateForSession multi-byte rune count = %d, want 401", len(mbRunes))
	}
	if !strings.HasSuffix(mb, "…") {
		t.Errorf("truncateForSession(multibyte) should end with ellipsis")
	}
}
