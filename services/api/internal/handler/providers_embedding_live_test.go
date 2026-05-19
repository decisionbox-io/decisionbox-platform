package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"

	// Register real embedding providers so GetProviderMeta finds them.
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/voyage"
)

// modelProjectWithoutEmbedding returns a project fixture with no
// embedding provider — used to drive the "400 on empty provider" path.
func modelProjectWithoutEmbedding() *models.Project {
	return &models.Project{ID: "p1", Name: "t", Domain: "gaming", Category: "match3"}
}

// These tests mirror the LLM live-list tests for the new embedding
// variant (ListLiveEmbeddingModels / ListLiveEmbeddingModelsForProject).
// Same contract: factory failures degrade to catalog + embedded
// live_error; unknown providers 404; malformed body 400.

// --- ListLiveEmbeddingModels (cloud-neutral) ---

func TestProvidersHandler_ListLiveEmbeddingModels_UnknownProvider(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/embedding/bogus/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "bogus")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModels(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestProvidersHandler_ListLiveEmbeddingModels_MissingPathValue(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/embedding//models/live",
		strings.NewReader(`{"config":{}}`))
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestProvidersHandler_ListLiveEmbeddingModels_InvalidJSON(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/embedding/openai/models/live",
		strings.NewReader(`not-json`))
	req.SetPathValue("id", "openai")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// Factory failure (e.g. OpenAI factory rejecting empty model) is
// treated as "this provider can't list right now" — the response is
// 200 with an empty models list and NO live_error. The user can pick
// from the static catalog or type a free-text model name; surfacing
// "model is required" as a live_error here would confuse the
// dashboard's "Load models" UX, which exists precisely to discover
// models before one is picked. Real upstream errors from a working
// list call (rate-limited, network down) still propagate via the
// ListModels() return path.
func TestProvidersHandler_ListLiveEmbeddingModels_FactoryFailureReturnsEmpty(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/embedding/openai/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "openai")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Models    []map[string]any `json:"models"`
			LiveError string           `json:"live_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.LiveError != "" {
		t.Errorf("expected no live_error on factory rejection, got %q", resp.Data.LiveError)
	}
	if len(resp.Data.Models) != 0 {
		t.Errorf("expected empty models list when provider can't list, got %d rows", len(resp.Data.Models))
	}
}

// A provider that doesn't implement ModelLister (voyage today) must
// return 200 with an empty models list and no live_error — ListModels
// being absent is a normal graceful-degradation case, not an error.
// The dashboard will render the shipped catalog from ProviderMeta in
// that case.
func TestProvidersHandler_ListLiveEmbeddingModels_ProviderWithoutLister_EmptyNoError(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/embedding/voyage/models/live",
		strings.NewReader(`{"config":{"credentials_json":"pa-test","model":"voyage-3"}}`))
	req.SetPathValue("id", "voyage")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Models    []map[string]any `json:"models"`
			LiveError string           `json:"live_error"`
		} `json:"data"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Data.LiveError != "" {
		t.Errorf("no-lister providers must not surface a live_error, got %q", resp.Data.LiveError)
	}
	if len(resp.Data.Models) != 0 {
		t.Errorf("no-lister providers must return an empty live list, got %d rows", len(resp.Data.Models))
	}
}

// --- ListLiveEmbeddingModelsForProject (uses saved secret) ---

func TestProvidersHandler_ListLiveEmbeddingModelsForProject_ProjectNotFound(t *testing.T) {
	repo := &stubProjectRepo{project: nil}
	h := NewProvidersHandlerWithProject(repo, nil)
	req := httptest.NewRequest("POST", "/api/v1/projects/missing/providers/embedding/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModelsForProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestProvidersHandler_ListLiveEmbeddingModelsForProject_EmptyProviderField(t *testing.T) {
	// Project with no embedding provider configured → 400, not a crash.
	repo := &stubProjectRepo{project: modelProjectWithoutEmbedding()}
	h := NewProvidersHandlerWithProject(repo, nil)
	req := httptest.NewRequest("POST", "/api/v1/projects/p1/providers/embedding/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListLiveEmbeddingModelsForProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}
