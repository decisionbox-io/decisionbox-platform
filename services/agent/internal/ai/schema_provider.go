// Package-level docstring lives on exploration.go. This file defines the
// SchemaProvider abstraction the exploration engine uses to serve
// on-demand schema operations (lookup_schema, search_tables) initiated
// by the LLM during a run.
//
// Why an interface here rather than a concrete dependency?
//
//   - Production: a discovery-package adapter wraps the schemas map +
//     the Qdrant retriever. The engine does not need to know either type.
//   - Tests: scripted fakes stand in. Driving the engine through the
//     interface keeps unit tests free of database/qdrant containers.
//   - Future: a /ask handler may reuse the engine with a request-scoped
//     provider that scopes search results to a different time window;
//     keeping the surface narrow makes that swap easy.
//
// Types in this file are intentionally lightweight value objects, not
// re-exports of models.TableSchema. Decoupling the public exploration
// API from the discovery model keeps the engine independent of agent-
// internal types.

package ai

import "context"

// MaxLookupTablesPerCall caps how many tables a single lookup_schema
// action may name. Higher values cause unbounded prompt growth in one
// turn — 10 already maps to ~8–10K tokens of L1 detail in the worst
// case, which is the right ceiling for keeping conversation tokens
// linear in tables-touched-per-call rather than tables-named.
const MaxLookupTablesPerCall = 10

// DefaultMaxLookupsPerRun caps how many lookup_schema actions an entire
// exploration run may issue, summed across steps. Overrideable per
// engine via ExplorationEngineOptions.MaxLookupsPerRun. The 30-call
// budget is generous — typical exploration touches 10–20 tables — and
// stops a misbehaving model from spamming lookups instead of querying.
const DefaultMaxLookupsPerRun = 30

// DefaultMaxSearchesPerRun caps how many search_tables actions a run
// may issue. Searches are cheaper than lookups (one Qdrant round-trip
// + ~K rows of metadata), so the budget is higher; lower than the
// lookup budget would create awkward "I want to search but can't" UX.
const DefaultMaxSearchesPerRun = 30

// DefaultSearchTopK is the default top-K for search_tables when the
// LLM doesn't specify one. Matches the value used by the bootstrap
// catalog retrieval path so model behaviour is consistent whether the
// LLM is browsing the upfront catalog or searching mid-run.
const DefaultSearchTopK = 10

// MaxSearchTopK caps an LLM-supplied K to keep result messages bounded.
// 30 hits are already ~3K tokens; anything larger is almost certainly
// the model abusing the action.
const MaxSearchTopK = 30

// SchemaProvider serves on-demand schema operations to the exploration
// engine. The engine never holds the schemas map or the retriever
// directly — it goes through this provider so production wiring and
// test fakes are interchangeable.
//
// All methods are read-only and safe to call concurrently. The
// implementation is responsible for thread-safety; the engine itself
// is single-goroutine within a run, but request-scoped wrappers may
// share state across runs.
type SchemaProvider interface {
	// Lookup returns L1 detail (columns + sample rows + row count)
	// for each requested fully-qualified table reference. The order
	// of entries in Tables follows the order of `refs` (after
	// deduplication). Refs that don't match a known table are
	// reported via NotFound — the implementation MUST NOT return
	// an error for partial misses, only for hard failures (e.g.
	// the cache itself is unreachable).
	//
	// Refs are accepted in either "dataset.table" qualified form
	// or bare "table" form. The implementation rehydrates the
	// canonical qualified form before matching.
	Lookup(ctx context.Context, refs []string) (LookupResult, error)

	// Search returns the top-K most relevant tables for a natural-
	// language query. Implementations rank semantically against
	// the per-project schema embedding index (Qdrant in production).
	// k <= 0 means "use DefaultSearchTopK"; values above MaxSearchTopK
	// are clamped to MaxSearchTopK.
	//
	// An empty result is not an error — it just means the query
	// didn't match anything indexed. Hard failures (Qdrant down,
	// embedding API offline) surface as errors and the engine
	// reports them back to the LLM as a "search unavailable"
	// user message rather than failing the run.
	Search(ctx context.Context, query string, k int) ([]SearchHit, error)
}

// LookupResult is what Lookup returns. Fields are independent so the
// engine can render a coherent message even on partial success: the
// useful tables go in Tables, the unknown refs go in NotFound, and
// Truncated tells the model it asked for more than the per-call cap.
type LookupResult struct {
	// Tables is the L1 detail for each successfully resolved ref,
	// in request order. Empty when every ref was unknown.
	Tables []LookupTable

	// NotFound carries refs the implementation could not resolve
	// (typo, dropped table, dataset mismatch). The engine surfaces
	// this back to the model so it can self-correct.
	NotFound []string

	// Truncated is true when the caller asked for more tables than
	// MaxLookupTablesPerCall. The provider returns the first N (in
	// request order) and signals truncation here so the model knows
	// to issue a follow-up call rather than assuming everything
	// arrived.
	Truncated bool
}

// LookupTable is a single table's L1 detail. Mirror of the relevant
// subset of models.TableSchema so the ai package doesn't import the
// agent's discovery model directly.
type LookupTable struct {
	Table      string                   // qualified "dataset.table"
	RowCount   int64                    // -1 when unknown
	Columns    []LookupColumn
	SampleRows []map[string]interface{}
}

// LookupColumn is one column's metadata. Category is optional ("",
// "primary_key", "time", "metric", "dimension") — when present, the
// renderer surfaces it as a hint so the model picks aggregation /
// grouping columns correctly.
type LookupColumn struct {
	Name     string
	Type     string
	Nullable bool
	Category string
}

// SearchHit is one result from Search. Score is the post-rerank value
// the underlying retriever returns; the engine renders it back to the
// LLM as a relative ranking signal. Blurb is the natural-language
// description the schema indexer wrote for this table — typically
// 2–4 sentences.
type SearchHit struct {
	Table    string
	Blurb    string
	RowCount int64
	Score    float64
}
