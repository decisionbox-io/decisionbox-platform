// Package models — CompactResult is a deterministic statistical digest
// of an exploration step's query_result. Produced once at exploration
// time and attached to the step so the analysis phase can render a
// fixed-size summary into the prompt instead of inlining the raw row
// blob (which on ERP-scale runs has produced multi-million-token
// prompts that the LLM rejects outright).
//
// CompactResult lives in libs/go-common so both the agent (which
// computes it) and the API (which serves analysis_log entries to the
// dashboard) can share a single struct definition.
package models

// Constants that govern the digest's shape. Exported so tests and
// callers in other packages can reference them by name rather than
// re-declaring the magic numbers.
const (
	// CompactInlineThreshold is the row-count cap below which the
	// digest stores every row verbatim (AllRows). Above the threshold
	// only HeadRows + TailRows are kept — the LLM still gets boundary
	// rows but never the full blob.
	CompactInlineThreshold = 20

	// TopValueCardinality is the per-column distinct-value cap above
	// which a string column's Top frequency list is omitted. High-
	// cardinality columns (user IDs, free-text fields) have no
	// meaningful "top values" and listing them risks leaking PII into
	// prompts.
	TopValueCardinality = 20

	// HeadTailRowCount is how many rows go into HeadRows and TailRows
	// each, when the result has more than CompactInlineThreshold rows.
	HeadTailRowCount = 5
)

// CompactResult is the digest itself. It serializes via the existing
// BSON / JSON tags so analysis-log payloads written to Mongo and
// shipped to the dashboard share one shape.
type CompactResult struct {
	// RowCount is the total number of rows the warehouse returned for
	// the underlying query. Always populated.
	RowCount int `bson:"row_count" json:"row_count"`

	// Columns carries per-column inferred type + summary statistics.
	// Order matches the warehouse's column order so the LLM can
	// correlate the summary with the SQL projection.
	Columns []ColumnSummary `bson:"columns" json:"columns"`

	// HeadRows is the first HeadTailRowCount rows verbatim. Always
	// present (even when RowCount <= HeadTailRowCount, in which case
	// HeadRows is just the entire result).
	HeadRows []map[string]any `bson:"head_rows" json:"head_rows"`

	// TailRows is the last HeadTailRowCount rows verbatim. Present
	// only when RowCount > 2 * HeadTailRowCount — otherwise HeadRows
	// already covers the entire result and TailRows would duplicate.
	TailRows []map[string]any `bson:"tail_rows,omitempty" json:"tail_rows,omitempty"`

	// AllRows holds every row when RowCount <= CompactInlineThreshold.
	// Small results (cohort splits, top-N, monthly roll-ups) travel
	// verbatim with zero information loss; larger results fall back to
	// HeadRows + TailRows + statistics.
	AllRows []map[string]any `bson:"all_rows,omitempty" json:"all_rows,omitempty"`
}

// ColumnKind is the inferred top-level type of a column's values. The
// builder picks one kind per column by scanning every non-nil value;
// heterogeneous columns become Mixed and skip statistics.
type ColumnKind string

const (
	ColumnKindNumber    ColumnKind = "number"
	ColumnKindString    ColumnKind = "string"
	ColumnKindBoolean   ColumnKind = "boolean"
	ColumnKindTimestamp ColumnKind = "timestamp"
	ColumnKindNull      ColumnKind = "null"
	ColumnKindMixed     ColumnKind = "mixed"
)

// ColumnSummary is the per-column statistical digest. Numeric stats
// are pointers so "no values" and "value 0" are distinguishable in the
// serialized form.
type ColumnSummary struct {
	Name      string     `bson:"name" json:"name"`
	Kind      ColumnKind `bson:"kind" json:"kind"`
	NullCount int        `bson:"null_count" json:"null_count"`
	Distinct  int        `bson:"distinct,omitempty" json:"distinct,omitempty"`

	// Numeric percentiles (float64-coerced). Populated only when
	// Kind == ColumnKindNumber and at least one non-nil value exists.
	Min    *float64 `bson:"min,omitempty"    json:"min,omitempty"`
	P25    *float64 `bson:"p25,omitempty"    json:"p25,omitempty"`
	Median *float64 `bson:"median,omitempty" json:"median,omitempty"`
	P75    *float64 `bson:"p75,omitempty"    json:"p75,omitempty"`
	Max    *float64 `bson:"max,omitempty"    json:"max,omitempty"`

	// Timestamp range: ISO-8601 strings. No median — meaningless for
	// irregular time distributions.
	MinTime string `bson:"min_time,omitempty" json:"min_time,omitempty"`
	MaxTime string `bson:"max_time,omitempty" json:"max_time,omitempty"`

	// Top is the top-3 values + counts for low-cardinality string
	// columns (Distinct <= TopValueCardinality). Empty for high-
	// cardinality columns to avoid leaking user IDs / free-text into
	// the prompt.
	Top []ValueCount `bson:"top,omitempty" json:"top,omitempty"`
}

// ValueCount is one entry of ColumnSummary.Top.
type ValueCount struct {
	Value any `bson:"v" json:"v"`
	Count int `bson:"n" json:"n"`
}
