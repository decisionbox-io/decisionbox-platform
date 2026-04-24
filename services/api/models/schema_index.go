package models

import "time"

// Schema-indexing lifecycle states. Stored on Project.SchemaIndexStatus.
//
// Transitions:
//
//	pending_indexing ─┬─> indexing ─┬─> ready    (success)
//	                  │             └─> failed   (error)
//	                  └── (user-triggered reindex → back to pending_indexing)
//
// Discovery and /ask are gated on status == ready.
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
// descriptions (blurbs) during schema indexing. Separate from the analysis
// LLM because blurb quality is orthogonal to analysis quality: a cheap
// multilingual model (e.g. Qwen3-32B on Bedrock) can outperform an Opus
// on retrieval recall while costing two orders of magnitude less.
//
// Credentials flow through the same `llm-api-key` secret when the blurb
// and analysis provider match. When they differ, a separate
// `blurb-llm-api-key` secret holds the blurb provider's key.
type BlurbLLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// SchemaRetrievalConfig is the per-project knob set for retrieval.
// All fields have env-var defaults in the agent; anything zero here
// falls back to those defaults.
type SchemaRetrievalConfig struct {
	// TopK is how many tables to pull into Level 1 per discovery/analysis
	// step. Zero means "use the env default" (SCHEMA_RETRIEVAL_TOP_K).
	TopK int `bson:"top_k,omitempty" json:"top_k,omitempty"`
}

// SchemaIndexProgress is a live worker-emitted progress document.
// One row per project in the project_schema_index_progress collection,
// upserted by (project_id) so the dashboard can poll it at 2s intervals
// without pagination. Reset on every new indexing run.
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
