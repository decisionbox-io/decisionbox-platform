package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCostEstimate_Fields(t *testing.T) {
	e := CostEstimate{
		LLM: LLMCostEstimate{
			Provider: "claude", Model: "claude-sonnet-4",
			EstimatedInputTokens: 100000, EstimatedOutputTokens: 25000,
			CostUSD: 0.675,
		},
		Warehouse: WarehouseCostEstimate{
			Provider: "bigquery", EstimatedQueries: 50,
			EstimatedBytesScanned: 5368709120, CostUSD: 0.03,
		},
		TotalUSD: 0.705,
		Breakdown: CostBreakdown{
			Exploration: 0.35, Analysis: 0.20,
			Validation: 0.05, Recommendations: 0.075,
		},
	}

	if e.LLM.Provider != "claude" {
		t.Error("LLM provider not set")
	}
	if e.Warehouse.EstimatedQueries != 50 {
		t.Error("warehouse queries not set")
	}
	if e.TotalUSD != 0.705 {
		t.Error("total not set")
	}
	if e.Breakdown.Exploration != 0.35 {
		t.Error("breakdown exploration not set")
	}
}

func TestCostEstimate_ZeroCost(t *testing.T) {
	// Ollama (free) + local warehouse = zero cost
	e := CostEstimate{
		LLM:      LLMCostEstimate{Provider: "ollama", CostUSD: 0},
		Warehouse: WarehouseCostEstimate{CostUSD: 0},
		TotalUSD:  0,
	}

	if e.TotalUSD != 0 {
		t.Error("should be zero for free providers")
	}
}

func TestLLMCostEstimate_CachedInputTokensJSON(t *testing.T) {
	b, err := json.Marshal(LLMCostEstimate{EstimatedInputTokens: 100, EstimatedCachedInputTokens: 80})
	if err != nil {
		t.Fatal(err)
	}
	var back LLMCostEstimate
	if err := json.Unmarshal(b, &back); err != nil || back.EstimatedCachedInputTokens != 80 {
		t.Fatalf("round trip = %+v (%v) from %s", back, err, b)
	}
	b, _ = json.Marshal(LLMCostEstimate{EstimatedInputTokens: 100})
	if strings.Contains(string(b), "estimated_cached_input_tokens") {
		t.Errorf("zero cached tokens should be omitted: %s", b)
	}
}
