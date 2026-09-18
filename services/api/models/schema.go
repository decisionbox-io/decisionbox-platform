package models

import "time"

// TableSchema and ColumnInfo mirror the agent-side shapes in
// services/agent/internal/models/discovery.go — the agent writes them into the
// project_schema_cache collection during indexing and the API reads them back
// for the schema editor (Data Warehouse → Advanced). Keep the BSON tags in
// lockstep with the agent definitions; a drift silently drops fields on decode.

// TableSchema is a warehouse table's cached schema.
type TableSchema struct {
	TableName    string                   `bson:"table_name" json:"table_name"`
	RowCount     int64                    `bson:"row_count" json:"row_count"`
	Columns      []ColumnInfo             `bson:"columns" json:"columns"`
	KeyColumns   []string                 `bson:"key_columns" json:"key_columns"`
	Metrics      []string                 `bson:"metrics" json:"metrics"`
	Dimensions   []string                 `bson:"dimensions" json:"dimensions"`
	SampleData   []map[string]interface{} `bson:"sample_data,omitempty" json:"sample_data,omitempty"`
	DiscoveredAt time.Time                `bson:"discovered_at" json:"discovered_at"`
}

// ColumnInfo is a single column's cached metadata.
type ColumnInfo struct {
	Name     string `bson:"name" json:"name"`
	Type     string `bson:"type" json:"type"`
	Nullable bool   `bson:"nullable" json:"nullable"`
	Category string `bson:"category" json:"category"` // primary_key, time, metric, dimension
}

// Manual schema-edit actions, stored on SchemaEdit.Action. Kept as an
// enumerated set so the dashboard can label + group the audit trail.
const (
	SchemaEditActionBlurb    = "blurb_edit"
	SchemaEditActionKeywords = "keywords_edit"
	SchemaEditActionColumns  = "columns_edit"
	SchemaEditActionDelete   = "table_delete"
)

// SchemaEdit is a durable audit record of one manual change a user made to the
// indexed schema (a blurb rewrite, keyword change, column removal, or table
// removal) via the schema editor. One document per edit in the
// project_schema_edits collection; append-only (no TTL).
//
// Manual edits themselves are ephemeral — the next re-index rediscovers from
// the warehouse and overwrites both the Mongo schema cache and the Qdrant
// blurbs. This record is what survives: it lets a user see what they changed
// (and copy the text back) after a re-index, and it powers the "you have N
// manual edits that will be lost" warning shown before a re-index / cache clear.
type SchemaEdit struct {
	ID           string `bson:"_id,omitempty" json:"id,omitempty"`
	ProjectID    string `bson:"project_id" json:"project_id"`
	DatasourceID string `bson:"datasource_id" json:"datasource_id"`
	// Table is the qualified table key (the schema_key the agent stored, e.g.
	// "dbo.orders"); Column is set only for a per-column note (unused today —
	// column removal records the full before/after set on a columns_edit).
	Table  string `bson:"table" json:"table"`
	Action string `bson:"action" json:"action"`
	// Before / After are human-readable snapshots so the trail is directly
	// copy-pasteable: the blurb text for a blurb edit, the comma-joined column
	// or keyword set for those edits, and a short summary for a table removal.
	Before string `bson:"before,omitempty" json:"before,omitempty"`
	After  string `bson:"after,omitempty" json:"after,omitempty"`
	// Actor is the authenticated user's email; "" in dev / unauthenticated mode.
	Actor string    `bson:"actor,omitempty" json:"actor,omitempty"`
	At    time.Time `bson:"at" json:"at"`
}
