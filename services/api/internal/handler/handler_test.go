package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/ollama"
)

func TestHealthCheck(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	HealthCheck(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})
	if data["status"] != "ok" {
		t.Errorf("status = %v", data["status"])
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Header().Get("Content-Type") != "application/json" {
		t.Error("missing Content-Type header")
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "something broke")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != "something broke" {
		t.Errorf("error = %q", resp.Error)
	}
}

func TestDecodeJSON(t *testing.T) {
	body := strings.NewReader(`{"name": "test"}`)
	req := httptest.NewRequest("POST", "/", body)

	var data struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(req, &data); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if data.Name != "test" {
		t.Errorf("name = %q", data.Name)
	}
}

func TestDecodeJSON_Invalid(t *testing.T) {
	body := strings.NewReader(`{invalid}`)
	req := httptest.NewRequest("POST", "/", body)

	var data struct{}
	if err := decodeJSON(req, &data); err == nil {
		t.Error("should error on invalid JSON")
	}
}

// --- Validate Domain Pack ---

func TestValidateDomainPack_Valid(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("valid pack should pass: %v", err)
	}
}

func TestValidateDomainPack_MissingSlug(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Slug = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require slug")
	}
}

func TestValidateDomainPack_MissingCategories(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Categories = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require at least one category")
	}
}

func TestValidateDomainPack_MissingBaseContext(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.BaseContext = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require base_context")
	}
}

func TestValidateDomainPack_MissingProfileTemplate(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Prompts.Base.BaseContext = "no profile variable"
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require {{PROFILE}} in base_context")
	}
}

func TestValidateDomainPack_MissingAnalysisAreas(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require at least one base analysis area")
	}
}

func TestValidateDomainPack_AreaMissingPrompt(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].Prompt = ""
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require prompt in analysis area")
	}
}

func TestValidateDomainPack_AreaMissingKeywords(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.AnalysisAreas.Base[0].Keywords = nil
	if err := ValidateDomainPack(pack); err == nil {
		t.Error("should require keywords in analysis area")
	}
}

// --- Provider Endpoints ---

func TestProvidersHandler_ListLLM(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("GET", "/api/v1/providers/llm", nil)
	w := httptest.NewRecorder()

	h.ListLLMProviders(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	providers := resp.Data.([]interface{})
	if len(providers) < 3 {
		t.Errorf("LLM providers = %d, want >= 3", len(providers))
	}

	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == nil || pm["id"] == "" {
			t.Error("provider should have id")
		}
		if pm["name"] == nil || pm["name"] == "" {
			t.Errorf("provider %v should have name", pm["id"])
		}
	}
}

func TestProvidersHandler_ListWarehouse(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("GET", "/api/v1/providers/warehouse", nil)
	w := httptest.NewRecorder()

	h.ListWarehouseProviders(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	providers := resp.Data.([]interface{})
	if len(providers) < 1 {
		t.Errorf("warehouse providers = %d, want >= 1", len(providers))
	}

	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == "bigquery" {
			fields := pm["config_fields"].([]interface{})
			if len(fields) < 2 {
				t.Errorf("bigquery should have >= 2 config fields")
			}
		}
	}
}

func TestProvidersHandler_ListEmbedding(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("GET", "/api/v1/providers/embedding", nil)
	w := httptest.NewRecorder()

	h.ListEmbeddingProviders(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	providers := resp.Data.([]interface{})
	if len(providers) < 2 {
		t.Errorf("embedding providers = %d, want >= 2 (openai, ollama)", len(providers))
	}

	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == "openai" {
			models := pm["models"].([]interface{})
			if len(models) < 2 {
				t.Errorf("openai should have >= 2 models")
			}
		}
	}
}

func TestProvidersHandler_LLMProviderHasConfigFields(t *testing.T) {
	h := NewProvidersHandler()
	req := httptest.NewRequest("GET", "/api/v1/providers/llm", nil)
	w := httptest.NewRecorder()

	h.ListLLMProviders(w, req)

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	providers := resp.Data.([]interface{})

	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == "claude" {
			fields := pm["config_fields"].([]interface{})
			keys := make(map[string]bool)
			for _, f := range fields {
				fm := f.(map[string]interface{})
				keys[fm["key"].(string)] = true
			}
			if !keys["api_key"] {
				t.Error("claude should have api_key config field")
			}
			if !keys["model"] {
				t.Error("claude should have model config field")
			}
		}
	}
}

// --- Process Tracker ---

func TestDiscoveriesHandler_HasRunner(t *testing.T) {
	r := runner.NewSubprocessRunner()
	h := &DiscoveriesHandler{agentRunner: r}
	if h.agentRunner == nil {
		t.Error("handler should have agent runner")
	}
}

func TestLLMProviders_HavePricing(t *testing.T) {
	for _, meta := range gollm.RegisteredProvidersMeta() {
		if len(meta.DefaultPricing) == 0 {
			t.Errorf("LLM provider %q has no DefaultPricing", meta.ID)
		}
	}
}

func TestWarehouseProviders_HavePricing(t *testing.T) {
	for _, meta := range gowarehouse.RegisteredProvidersMeta() {
		if meta.DefaultPricing == nil {
			t.Errorf("warehouse provider %q has no DefaultPricing", meta.ID)
		}
	}
}

func TestLLMProvider_ClaudePricing(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("claude")
	if !ok {
		t.Fatal("claude provider not registered")
	}
	if _, ok := meta.DefaultPricing["claude-sonnet-4"]; !ok {
		t.Error("claude-sonnet-4 pricing missing")
	}
	if _, ok := meta.DefaultPricing["claude-opus-4"]; !ok {
		t.Error("claude-opus-4 pricing missing")
	}
	sonnet := meta.DefaultPricing["claude-sonnet-4"]
	if sonnet.InputPerMillion <= 0 || sonnet.OutputPerMillion <= 0 {
		t.Errorf("sonnet pricing invalid: %+v", sonnet)
	}
}
