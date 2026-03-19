package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"

	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/social/go"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
)

func init() {
	// Domain pack reads areas.json from filesystem.
	// Go test runs from the package directory, so we need to find the repo root.
	// Walk up from services/api/internal/handler/ to find domain-packs/
	wd, _ := os.Getwd()
	// Walk up 4 levels: handler -> internal -> api -> services -> repo root
	root := filepath.Join(wd, "..", "..", "..", "..")
	os.Setenv("DOMAIN_PACK_PATH", filepath.Join(root, "domain-packs"))
}

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
	if len(domains) < 2 {
		t.Errorf("should have at least 2 domains (gaming, social), got %d", len(domains))
	}

	// Find gaming domain (order is not guaranteed)
	var gaming map[string]interface{}
	for _, d := range domains {
		dm := d.(map[string]interface{})
		if dm["id"] == "gaming" {
			gaming = dm
			break
		}
	}
	if gaming == nil {
		t.Fatal("gaming domain not found in response")
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

	if resp.Data == nil {
		t.Fatalf("resp.Data is nil — areas.json may not be found. DOMAIN_PACK_PATH=%s, body=%s",
			os.Getenv("DOMAIN_PACK_PATH"), w.Body.String())
	}

	areas, ok := resp.Data.([]interface{})
	if !ok {
		t.Fatalf("resp.Data is not array: %T", resp.Data)
	}
	if len(areas) != 5 {
		t.Errorf("areas = %d, want 5 (3 base + 2 match3)", len(areas))
	}

	ids := make(map[string]bool)
	for _, a := range areas {
		am := a.(map[string]interface{})
		ids[am["id"].(string)] = true
	}
	for _, expected := range []string{"churn", "engagement", "monetization", "levels", "boosters"} {
		if !ids[expected] {
			t.Errorf("missing area: %s", expected)
		}
	}
}

func TestDomainsHandler_GetAnalysisAreas_BaseOnly(t *testing.T) {
	h := NewDomainsHandler()
	req := httptest.NewRequest("GET", "/api/v1/domains/gaming/categories//areas", nil)
	req.SetPathValue("domain", "gaming")
	req.SetPathValue("category", "")
	w := httptest.NewRecorder()

	h.GetAnalysisAreas(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	areas := resp.Data.([]interface{})
	if len(areas) != 3 {
		t.Errorf("base areas = %d, want 3", len(areas))
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
	// All LLM providers should register default pricing
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

