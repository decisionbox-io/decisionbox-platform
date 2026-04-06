package models

import "time"

// Pricing holds all provider pricing data.
// Seeded from registered providers on API startup, stored in MongoDB, editable via UI.
type Pricing struct {
	ID        string                            `bson:"_id,omitempty" json:"id"`
	LLM       map[string]map[string]TokenPrice  `bson:"llm" json:"llm"`             // provider -> model -> price
	Warehouse map[string]WarehousePrice         `bson:"warehouse" json:"warehouse"`  // provider -> price
	UpdatedAt time.Time                         `bson:"updated_at" json:"updated_at"`
}

// TokenPrice holds per-token pricing for an LLM model.
type TokenPrice struct {
	InputPerMillion  float64 `bson:"input_per_million" json:"input_per_million"`
	OutputPerMillion float64 `bson:"output_per_million" json:"output_per_million"`
}

// WarehousePrice holds pricing for a warehouse provider.
type WarehousePrice struct {
	CostModel           string  `bson:"cost_model" json:"cost_model"`                       // per_byte_scanned, per_query, per_hour
	CostPerTBScannedUSD float64 `bson:"cost_per_tb_scanned_usd" json:"cost_per_tb_scanned_usd"`
}

// CostEstimate is the result of a cost estimation.
type CostEstimate struct {
	LLM       LLMCostEstimate       `json:"llm"`
	Warehouse WarehouseCostEstimate `json:"warehouse"`
	TotalUSD  float64               `json:"total_cost_usd"`
	Breakdown CostBreakdown         `json:"breakdown"`
}

type LLMCostEstimate struct {
	Provider              string  `json:"provider"`
	Model                 string  `json:"model"`
	EstimatedInputTokens  int     `json:"estimated_input_tokens"`
	EstimatedOutputTokens int     `json:"estimated_output_tokens"`
	CostUSD               float64 `json:"cost_usd"`
}

type WarehouseCostEstimate struct {
	Provider              string  `json:"provider"`
	EstimatedQueries      int     `json:"estimated_queries"`
	EstimatedBytesScanned int64   `json:"estimated_bytes_scanned"`
	CostUSD               float64 `json:"cost_usd"`
}

type CostBreakdown struct {
	Exploration     float64 `json:"exploration"`
	Analysis        float64 `json:"analysis"`
	Validation      float64 `json:"validation"`
	Recommendations float64 `json:"recommendations"`
}
