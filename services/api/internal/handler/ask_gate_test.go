package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Ask() gates on schema_index_status the same way TriggerDiscovery does
// (plan §8.6). These tests lock in the error-shape contract.

func gatedAskRequest(projectID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(askRequest{Question: "anything"})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+projectID+"/ask", bytes.NewReader(body))
	req.SetPathValue("id", projectID)
	return httptest.NewRecorder()
}

func newAskHandlerWithStatus(status, errMsg string) *SearchHandler {
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:                "proj-1",
			Name:              "t",
			Embedding:         goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:               models.LLMConfig{Provider: "test-llm", Model: "test-llm-model"},
			SchemaIndexStatus: status,
			SchemaIndexError:  errMsg,
		},
	}
	vs := &mockVectorStoreForSearch{}
	return NewSearchHandler(projectRepo, &mockInsightRepo{}, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
}

func TestAsk_Gate_PendingIndexing_Returns409(t *testing.T) {
	h := newAskHandlerWithStatus(models.SchemaIndexStatusPendingIndexing, "")
	w := gatedAskRequest("proj-1")
	body, _ := json.Marshal(askRequest{Question: "x"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	h.Ask(w, req)
	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "schema-index/status") {
		t.Errorf("body should point to status endpoint: %s", w.Body.String())
	}
}

func TestAsk_Gate_Indexing_Returns409(t *testing.T) {
	h := newAskHandlerWithStatus(models.SchemaIndexStatusIndexing, "")
	body, _ := json.Marshal(askRequest{Question: "x"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestAsk_Gate_Failed_Returns409WithError(t *testing.T) {
	h := newAskHandlerWithStatus(models.SchemaIndexStatusFailed, "qdrant unreachable")
	body, _ := json.Marshal(askRequest{Question: "x"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)
	if w.Code != 409 {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "qdrant unreachable") {
		t.Errorf("body should include error: %s", w.Body.String())
	}
}
