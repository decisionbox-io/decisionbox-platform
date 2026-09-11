package models

import "time"

// Schema-indexing lifecycle states. Stored on Project.SchemaIndexStatus.
//
// Transitions:
//
//	pending_indexing ─┬─> indexing ─┬─> ready      (success)
//	                  │             ├─> failed     (error)
//	                  │             └─> cancelled  (user cancel from the dashboard)
//	                  └── (user-triggered reindex → back to pending_indexing)
//
//	ready / failed / cancelled / "" ─> needs_reindex
//	    via Settings → Advanced → "Clear schema cache". Cache + Qdrant are
//	    dropped; the project sits in needs_reindex until the user manually
//	    clicks Reindex (which flips it to pending_indexing). The worker
//	    explicitly does NOT auto-claim needs_reindex — the user wants to
//	    pick the moment (e.g. wait for VPN, off-peak hours).
//
// Discovery and /ask are gated on status == ready.
const (
	SchemaIndexStatusPendingIndexing = "pending_indexing"
	SchemaIndexStatusIndexing        = "indexing"
	SchemaIndexStatusReady           = "ready"
	SchemaIndexStatusFailed          = "failed"
	SchemaIndexStatusCancelled       = "cancelled"
	SchemaIndexStatusNeedsReindex    = "needs_reindex"
)

// Schema-indexing progress phases. Stored on SchemaIndexProgress.Phase
// and used as keys in SchemaIndexRun.PhaseDurations.
const (
	SchemaIndexPhaseListingTables    = "listing_tables"
	SchemaIndexPhaseSchemaDiscovery  = "schema_discovery" // per-table columns + samples (the longest leg on big warehouses)
	SchemaIndexPhaseDescribingTables = "describing_tables"
	SchemaIndexPhaseEmbedding        = "embedding"
)

// SchemaIndexRunKindTables is the object kind stamped on a run record for a
// classic table-indexing pass. Kept generic (SchemaIndexRun.Kind) so a future
// non-table object kind slots in without a schema change.
const SchemaIndexRunKindTables = "tables"

// SchemaIndexRun is a durable, per-(datasource × index run) result record,
// stamped by the agent when a datasource finishes indexing (success or
// failure) and read by the dashboard's per-datasource history + project
// roll-up. One document per (project_id, datasource_id, run_id) in the
// project_schema_index_runs collection; append-only (no TTL) because it is the
// audit record — unlike the single-doc, reset-every-run SchemaIndexProgress.
//
// Agent-side mirror lives in services/agent/internal/models/schema_index.go —
// both services read/write the same collection, so keep the two in sync.
type SchemaIndexRun struct {
	ProjectID      string `bson:"project_id" json:"project_id"`
	DatasourceID   string `bson:"datasource_id" json:"datasource_id"`
	DatasourceName string `bson:"datasource_name,omitempty" json:"datasource_name,omitempty"`
	RunID          string `bson:"run_id" json:"run_id"`
	// Kind is the indexed object kind — "tables" today (SchemaIndexRunKindTables).
	Kind string `bson:"kind" json:"kind"`
	// ObjectsIndexed is how many objects (tables) landed in the index for this
	// datasource; BlurbsGenerated is how many blurbs were produced.
	ObjectsIndexed  int `bson:"objects_indexed" json:"objects_indexed"`
	BlurbsGenerated int `bson:"blurbs_generated" json:"blurbs_generated"`
	// Status is "ready" or "failed" (SchemaIndexStatusReady / *Failed).
	Status string `bson:"status" json:"status"`
	// Error is the failure reason; empty on success.
	Error string `bson:"error,omitempty" json:"error,omitempty"`
	// PhaseDurations maps a phase name (SchemaIndexPhase*) to its elapsed
	// milliseconds. A map (not fixed columns) so new phases slot in freely.
	PhaseDurations map[string]int64 `bson:"phase_durations,omitempty" json:"phase_durations,omitempty"`
	// TokensIn / TokensOut are the blurb-LLM token totals for this datasource.
	TokensIn   int       `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut  int       `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	StartedAt  time.Time `bson:"started_at" json:"started_at"`
	FinishedAt time.Time `bson:"finished_at" json:"finished_at"`
}

// BlurbLLMConfig picks the LLM used to generate per-table natural-language
// descriptions (blurbs) during schema indexing. Separate from the analysis
// LLM because blurb quality is orthogonal to analysis quality: a cheap
// multilingual model (e.g. Qwen3-32B on Bedrock) can outperform an Opus
// on retrieval recall while costing two orders of magnitude less.
//
// Credentials flow through the same `llm-credentials` secret when the blurb
// and analysis provider match. When they differ, a separate
// `blurb-llm-credentials` secret holds the blurb provider's key.
type BlurbLLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// SchemaIndexProgress is a live worker-emitted progress document.
// One row per project in the project_schema_index_progress collection,
// upserted by (project_id) so the dashboard can poll it at 2s intervals
// without pagination. Reset on every new indexing run.
//
// API-side mirror of the per-build blurb-LLM token totals; the agent
// writes during a build, the API reads when serving the schema-index
// status endpoint.
type SchemaIndexProgress struct {
	ProjectID    string    `bson:"project_id" json:"project_id"`
	RunID        string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	Phase        string    `bson:"phase" json:"phase"`
	TablesTotal  int       `bson:"tables_total" json:"tables_total"`
	TablesDone   int       `bson:"tables_done" json:"tables_done"`
	StartedAt    time.Time `bson:"started_at" json:"started_at"`
	UpdatedAt    time.Time `bson:"updated_at" json:"updated_at"`
	ErrorMessage string    `bson:"error_message,omitempty" json:"error_message,omitempty"`

	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
}
