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

// Schema-indexing progress phases. Stored on SchemaIndexProgress.Phase
// and used as keys in SchemaIndexRun.PhaseDurations.
const (
	SchemaIndexPhaseListingTables    = "listing_tables"
	SchemaIndexPhaseSchemaDiscovery  = "schema_discovery" // per-table columns + samples (the longest leg on big warehouses)
	SchemaIndexPhaseDescribingTables = "describing_tables"
	SchemaIndexPhaseEmbedding        = "embedding"
)

// SchemaIndexRunKindTables is the object kind stamped on a run record for a
// classic table-indexing pass. Generic so a future object kind slots in.
const SchemaIndexRunKindTables = "tables"

// SchemaIndexRun is a durable, per-(datasource × index run) result record the
// agent stamps when a datasource finishes indexing (success or failure). One
// document per (project_id, datasource_id, run_id) in project_schema_index_runs;
// append-only (no TTL) — the audit record, unlike the reset-every-run
// SchemaIndexProgress. API-side mirror lives in
// services/api/models/schema_index.go — keep the two in sync.
type SchemaIndexRun struct {
	ProjectID       string           `bson:"project_id" json:"project_id"`
	DatasourceID    string           `bson:"datasource_id" json:"datasource_id"`
	DatasourceName  string           `bson:"datasource_name,omitempty" json:"datasource_name,omitempty"`
	RunID           string           `bson:"run_id" json:"run_id"`
	Kind            string           `bson:"kind" json:"kind"`
	ObjectsIndexed  int              `bson:"objects_indexed" json:"objects_indexed"`
	BlurbsGenerated int              `bson:"blurbs_generated" json:"blurbs_generated"`
	Status          string           `bson:"status" json:"status"`
	Error           string           `bson:"error,omitempty" json:"error,omitempty"`
	PhaseDurations  map[string]int64 `bson:"phase_durations,omitempty" json:"phase_durations,omitempty"`
	TokensIn        int              `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
	TokensOut       int              `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
	StartedAt       time.Time        `bson:"started_at" json:"started_at"`
	FinishedAt      time.Time        `bson:"finished_at" json:"finished_at"`
}

// BlurbLLMConfig picks the LLM used to generate per-table natural-language
// descriptions during schema indexing.
type BlurbLLMConfig struct {
	Provider string            `bson:"provider" json:"provider"`
	Model    string            `bson:"model" json:"model"`
	Config   map[string]string `bson:"config,omitempty" json:"config,omitempty"`
}

// SchemaIndexProgress is the live worker-emitted progress document.
//
// There is exactly one Mongo doc per project (upserted / overwritten on
// each build), so "tokens spent on the most recent schema-index" is a
// one-document read. `project_schema_index_logs` records per-line
// stdout/stderr (not per-build totals), so the progress doc is the
// home. Per-blurb tokens are summed onto this single doc via
// IncrementTokens; the Qdrant payload is untouched.
type SchemaIndexProgress struct {
	ProjectID    string    `bson:"project_id" json:"project_id"`
	RunID        string    `bson:"run_id,omitempty" json:"run_id,omitempty"`
	Phase        string    `bson:"phase" json:"phase"`
	TablesTotal  int       `bson:"tables_total" json:"tables_total"`
	TablesDone   int       `bson:"tables_done" json:"tables_done"`
	StartedAt    time.Time `bson:"started_at" json:"started_at"`
	UpdatedAt    time.Time `bson:"updated_at" json:"updated_at"`
	ErrorMessage string    `bson:"error_message,omitempty" json:"error_message,omitempty"`

	// Per-build blurb-LLM token totals, summed across every successful
	// blurb call in the current build. Reset to zero on Reset();
	// accumulated via IncrementTokens.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
}
