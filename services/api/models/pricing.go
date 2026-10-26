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
