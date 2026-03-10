package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
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

func TestDomainsHandler_ListDomains(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains", nil)
	w := httptest.NewRecorder()

	h.ListDomains(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	domains := resp.Data.([]interface{})
	if len(domains) == 0 {
		t.Error("should have at least one domain (gaming)")
	}

	gaming := domains[0].(map[string]interface{})
	if gaming["id"] != "gaming" {
		t.Errorf("id = %v", gaming["id"])
	}
	cats := gaming["categories"].([]interface{})
	if len(cats) == 0 {
		t.Error("gaming should have categories")
	}
}

func TestDomainsHandler_ListCategories(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories", nil)
	req.SetPathValue("domain", "gaming")
	w := httptest.NewRecorder()

	h.ListCategories(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDomainsHandler_ListCategories_NotFound(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/nonexistent/categories", nil)
	req.SetPathValue("domain", "nonexistent")
	w := httptest.NewRecorder()

	h.ListCategories(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDomainsHandler_GetProfileSchema(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories/match3/schema", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "match3")
	w := httptest.NewRecorder()

	h.GetProfileSchema(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDomainsHandler_GetAnalysisAreas(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories/match3/areas", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "match3")
	w := httptest.NewRecorder()

	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	areas := resp.Data.([]interface{})
	if len(areas) != 5 {
		t.Errorf("areas = %d, want 5 (3 base + 2 match3)", len(areas))
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
		t.Errorf("LLM providers = %d, want >= 3 (claude, openai, ollama)", len(providers))
	}

	// Verify each provider has metadata
	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == nil || pm["id"] == "" {
			t.Error("provider should have id")
		}
		if pm["name"] == nil || pm["name"] == "" {
			t.Errorf("provider %v should have name", pm["id"])
		}
		if pm["config_fields"] == nil {
			t.Errorf("provider %v should have config_fields", pm["id"])
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
		t.Errorf("warehouse providers = %d, want >= 1 (bigquery)", len(providers))
	}

	// Verify BigQuery has expected config fields
	for _, p := range providers {
		pm := p.(map[string]interface{})
		if pm["id"] == "bigquery" {
			fields := pm["config_fields"].([]interface{})
			if len(fields) < 2 {
				t.Errorf("bigquery should have >= 2 config fields, got %d", len(fields))
			}
			// Check field structure
			field := fields[0].(map[string]interface{})
			if field["key"] == nil {
				t.Error("config field should have key")
			}
			if field["label"] == nil {
				t.Error("config field should have label")
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

	// Find Claude and verify it has api_key + model fields
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
