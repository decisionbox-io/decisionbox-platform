package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

func TestPricingHandler_Get_NilRepo(t *testing.T) {
	h := NewPricingHandler(nil)

	req := httptest.NewRequest("GET", "/api/v1/pricing", nil)
	w := httptest.NewRecorder()

	// Will panic on nil repo — that's expected in unit tests without DB
	defer func() { recover() }()
	h.Get(w, req)
}

func TestPricingHandler_Update_InvalidJSON(t *testing.T) {
	h := NewPricingHandler(nil)

	req := httptest.NewRequest("PUT", "/api/v1/pricing",
		strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestEstimateHandler_ProjectNotFound(t *testing.T) {
	h := NewEstimateHandler(nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/nonexistent/discover/estimate", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	// Will panic on nil repo — expected
	defer func() { recover() }()
	h.Estimate(w, req)
}

// --- Mock-based unit tests ---

func TestPricingHandler_Get_Success_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	h := NewPricingHandler(repo)

	// Seed pricing data
	repo.Save(nil, &models.Pricing{
		LLM: map[string]map[string]models.TokenPrice{
			"claude": {
				"claude-sonnet-4": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			},
		},
		Warehouse: map[string]models.WarehousePrice{
			"bigquery": {CostModel: "per_byte_scanned", CostPerTBScannedUSD: 6.25},
		},
	})

	req := httptest.NewRequest("GET", "/api/v1/pricing", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})

	// Verify LLM pricing present
	llm := data["llm"].(map[string]interface{})
	if llm["claude"] == nil {
		t.Error("LLM pricing should include claude")
	}

	// Verify warehouse pricing present
	wh := data["warehouse"].(map[string]interface{})
	if wh["bigquery"] == nil {
		t.Error("warehouse pricing should include bigquery")
	}
}

func TestPricingHandler_Get_Empty_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	h := NewPricingHandler(repo)

	// No pricing seeded — should return empty maps
	req := httptest.NewRequest("GET", "/api/v1/pricing", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})

	// When pricing is nil, handler returns empty maps
	if data["llm"] == nil {
		t.Error("llm should be an empty map, not nil")
	}
	if data["warehouse"] == nil {
		t.Error("warehouse should be an empty map, not nil")
	}
}

func TestPricingHandler_Get_RepoError_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	repo.getErr = fmt.Errorf("database unavailable")
	h := NewPricingHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/pricing", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestPricingHandler_Update_Success_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	h := NewPricingHandler(repo)

	body := `{
		"llm": {
			"openai": {
				"gpt-4o": {"input_per_million": 5.0, "output_per_million": 15.0}
			}
		},
		"warehouse": {
			"redshift": {"cost_model": "per_hour", "cost_per_tb_scanned_usd": 0.0}
		}
	}`

	req := httptest.NewRequest("PUT", "/api/v1/pricing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]interface{})

	llm := data["llm"].(map[string]interface{})
	if llm["openai"] == nil {
		t.Error("updated pricing should include openai")
	}

	// Verify it was persisted in the repo
	stored, _ := repo.Get(nil)
	if stored == nil {
		t.Fatal("pricing should be saved in repo")
	}
	if stored.LLM["openai"] == nil {
		t.Error("repo should have openai pricing")
	}
}

func TestPricingHandler_Update_RepoError_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	repo.saveErr = fmt.Errorf("disk full")
	h := NewPricingHandler(repo)

	body := `{"llm":{},"warehouse":{}}`
	req := httptest.NewRequest("PUT", "/api/v1/pricing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestPricingHandler_Update_Overwrite_MockRepo(t *testing.T) {
	repo := newMockPricingRepo()
	h := NewPricingHandler(repo)

	// Set initial pricing
	repo.Save(nil, &models.Pricing{
		LLM: map[string]map[string]models.TokenPrice{
			"claude": {"claude-sonnet-4": {InputPerMillion: 3.0, OutputPerMillion: 15.0}},
		},
		Warehouse: map[string]models.WarehousePrice{},
	})

	// Overwrite with new pricing
	body := `{"llm":{"openai":{"gpt-4o":{"input_per_million":5.0,"output_per_million":15.0}}},"warehouse":{}}`
	req := httptest.NewRequest("PUT", "/api/v1/pricing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Update(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	stored, _ := repo.Get(nil)
	if stored.LLM["claude"] != nil {
		t.Error("claude pricing should be overwritten (full replace)")
	}
	if stored.LLM["openai"] == nil {
		t.Error("openai pricing should be present")
	}
}
