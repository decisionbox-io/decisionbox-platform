package models

import "time"

// Schema-indexing lifecycle states. Stored on Project.SchemaIndexStatus.
// Mirror of services/api/models/schema_index.go — both services read/write
// the same MongoDB collection.
const (
	SchemaIndexStatusPendingIndexing = "pending_indexing"
	SchemaIndexStatusIndexing        = "indexing"
	SchemaIndexStatusReady           = "ready"
	SchemaIndexStatusFailed          = "failed"
)

// Schema-indexing progress phases. Stored on SchemaIndexProgress.Phase.
const (
	SchemaIndexPhaseListingTables    = "listing_tables"
	SchemaIndexPhaseSchemaDiscovery  = "schema_discovery" // per-table columns + samples (the longest leg on big warehouses)
	SchemaIndexPhaseDescribingTables = "describing_tables"
	SchemaIndexPhaseEmbedding        = "embedding"
)

// BlurbLLMConfig picks the LLM used to generate per-table natural-language
// descriptions during schema indexing.
type BlurbLLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// SchemaRetrievalConfig is the per-project knob set for retrieval.
type SchemaRetrievalConfig struct {
	TopK int `bson:"top_k,omitempty" json:"top_k,omitempty"`
}

// SchemaIndexProgress is the live worker-emitted progress document.
type SchemaIndexProgress struct {
	ProjectID    string    `bson:"project_id" json:"project_id"`
	RunID        string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	Phase        string    `bson:"phase" json:"phase"`
	TablesTotal  int       `bson:"tables_total" json:"tables_total"`
	TablesDone   int       `bson:"tables_done" json:"tables_done"`
	StartedAt    time.Time `bson:"started_at" json:"started_at"`
	UpdatedAt    time.Time `bson:"updated_at" json:"updated_at"`
	ErrorMessage string    `bson:"error_message,omitempty" json:"error_message,omitempty"`
}
