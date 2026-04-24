package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
	"github.com/decisionbox-io/decisionbox/services/api/models"

	// Register real providers so GetProviderMeta finds them.
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
)

// --- ListLiveLLMModels (cloud-neutral; POST body with credentials) ---

func TestProvidersHandler_ListLiveLLMModels_UnknownProvider(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/bogus/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "bogus")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestProvidersHandler_ListLiveLLMModels_MissingPathValue(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm//models/live",
		strings.NewReader(`{"config":{}}`))
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModels_InvalidJSON(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/claude/models/live",
		strings.NewReader(`not-json`))
	req.SetPathValue("id", "claude")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// When the factory fails (e.g. missing credentials), the handler should
// still return a 200 with the catalog rows and an embedded live_error.
// The user sees the catalog + a visible error instead of a hard 500.
func TestProvidersHandler_ListLiveLLMModels_FactoryFailureReturnsCatalog(t *testing.T) {
	h := NewProvidersHandler()
	// claude factory requires api_key — send empty body to provoke failure.
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/claude/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "claude")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
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
	if resp.Data.LiveError == "" {
		t.Error("expected live_error when factory fails")
	}
	// Catalog rows must still be returned so the UI has something.
	if len(resp.Data.Models) == 0 {
		t.Error("expected catalog rows in models[] even when upstream fails")
	}
	// All rows should be Source=catalog since no live rows merged.
	for _, m := range resp.Data.Models {
		if m["source"] != "catalog" {
			t.Errorf("row %v has non-catalog source when live fetch failed", m)
		}
	}
}

// --- ListLiveLLMModelsForProject (uses saved secret) ---

type stubProjectRepo struct {
	project *models.Project
	err     error
}

func (r *stubProjectRepo) GetByID(_ context.Context, _ string) (*models.Project, error) {
	return r.project, r.err
}
func (r *stubProjectRepo) Create(context.Context, *models.Project) error            { return nil }
func (r *stubProjectRepo) List(context.Context, int, int) ([]*models.Project, error) { return nil, nil }
func (r *stubProjectRepo) Update(context.Context, string, *models.Project) error     { return nil }
func (r *stubProjectRepo) Delete(context.Context, string) error                      { return nil }
func (r *stubProjectRepo) Count(context.Context) (int, error)                        { return 0, nil }
func (r *stubProjectRepo) CountWithWarehouse(context.Context) (int, error)           { return 0, nil }
func (r *stubProjectRepo) SetSchemaIndexStatus(context.Context, string, string, string) error {
	return nil
}

func TestProvidersHandler_ListLiveLLMModelsForProject_ProjectNotFound(t *testing.T) {
	repo := &stubProjectRepo{project: nil}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/missing/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_ProjectLoadError(t *testing.T) {
	repo := &stubProjectRepo{err: errors.New("db down")}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/x/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "x")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_EmptyProviderField(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{ID: "p"}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no llm provider") {
		t.Errorf("expected actionable error: %s", w.Body.String())
	}
}

func TestProvidersHandler_ListLiveLLMModelsForProject_UnknownProvider(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "bogus-never-registered"},
	}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// With no secret provider wired, the handler must still attempt the
// factory (cloud providers like Bedrock / Vertex use ambient creds);
// the factory will usually fail for api-key providers (Claude, OpenAI),
// in which case we get a 200 with the catalog + live_error.
func TestProvidersHandler_ListLiveLLMModelsForProject_NoSecretProviderReturnsCatalog(t *testing.T) {
	repo := &stubProjectRepo{project: &models.Project{
		ID:  "p",
		LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"},
	}}
	h := NewProvidersHandlerWithProject(repo, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/p/providers/llm/models/live",
		strings.NewReader(`{}`))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()

	h.ListLiveLLMModelsForProject(w, req)
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
	if resp.Data.LiveError == "" {
		t.Error("expected live_error when no secret is available")
	}
	if len(resp.Data.Models) == 0 {
		t.Error("catalog rows must still be returned")
	}
}

// --- Merge logic exercised via the public handler path ---

func TestProvidersHandler_Merge_CatalogOnlyRowsKeptWithNoLive(t *testing.T) {
	// No live rows come back (bad credentials) → every catalog row
	// ends up with Source=catalog and Dispatchable matching its wire.
	h := NewProvidersHandler()
	req := httptest.NewRequest("POST", "/api/v1/providers/llm/openai/models/live",
		strings.NewReader(`{"config":{}}`))
	req.SetPathValue("id", "openai")
	w := httptest.NewRecorder()

	h.ListLiveLLMModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Models []struct {
				ID           string `json:"id"`
				Source       string `json:"source"`
				Wire         string `json:"wire"`
				Dispatchable bool   `json:"dispatchable"`
			} `json:"models"`
			LiveError string `json:"live_error"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	foundDispatchable := false
	for _, m := range resp.Data.Models {
		if m.Source != "catalog" {
			t.Errorf("unexpected source %q for %s when live failed", m.Source, m.ID)
		}
		// Every OpenAI catalog row is wire=openai-compat → dispatchable
		if m.Wire == string(modelcatalog.OpenAICompat) && !m.Dispatchable {
			t.Errorf("row %s has wire=openai-compat but dispatchable=false", m.ID)
		}
		if m.Dispatchable {
			foundDispatchable = true
		}
	}
	if !foundDispatchable {
		t.Error("expected at least one dispatchable catalog row for openai")
	}
}

// --- validateLLMConfig unit checks ---

func TestValidateLLMConfig_ValidForProvider(t *testing.T) {
	// Provider-scoped allowlist accepts anthropic for bedrock.
	_ = gollm.RegisteredProvidersMeta() // force provider init
	msg := validateLLMConfig("bedrock", map[string]string{"wire_override": "anthropic"})
	if msg != "" {
		t.Errorf("expected pass, got: %q", msg)
	}
}

func TestValidateLLMConfig_RejectsUnsupportedWireForProvider(t *testing.T) {
	msg := validateLLMConfig("bedrock", map[string]string{"wire_override": "google-native"})
	if msg == "" {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(msg, "not supported by provider") {
		t.Errorf("message should mention provider-scoped rejection: %q", msg)
	}
}

func TestValidateLLMConfig_EmptyOverridePasses(t *testing.T) {
	if msg := validateLLMConfig("bedrock", map[string]string{}); msg != "" {
		t.Errorf("empty config should pass, got: %q", msg)
	}
	if msg := validateLLMConfig("bedrock", map[string]string{"wire_override": ""}); msg != "" {
		t.Errorf("empty wire_override should pass, got: %q", msg)
	}
}

func TestValidateLLMConfig_GenericFallbackForProvidersWithoutOptions(t *testing.T) {
	// claude provider has no wire_override field, so the fallback
	// generic ParseWire check fires. "anthropic" is a syntactically
	// valid wire → passes; "antropik" → fails.
	if msg := validateLLMConfig("claude", map[string]string{"wire_override": "anthropic"}); msg != "" {
		t.Errorf("generic fallback should accept valid wire, got: %q", msg)
	}
	if msg := validateLLMConfig("claude", map[string]string{"wire_override": "antropik"}); msg == "" {
		t.Error("generic fallback should reject typo")
	}
}
