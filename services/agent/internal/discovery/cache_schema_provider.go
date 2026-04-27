// Package discovery — CacheSchemaProvider implements ai.SchemaProvider
// for the production exploration loop. It serves on-demand schema
// operations entirely from the per-project schema cache (Mongo) and
// the per-project Qdrant collection — no live warehouse traffic.
//
// Design contract:
//
//   - Lookup uses ONLY the in-memory schemas map the orchestrator loaded
//     from the schema cache at run start. No live re-discovery — that
//     is exactly the regression the schema-retrieval architecture
//     prevents. If a model asks for a table that isn't in the map, it
//     comes back as NotFound.
//
//   - Search uses the per-project Qdrant collection populated by the
//     schema indexer. Embedding the query happens inline with the
//     same Embedder the orchestrator already wires for the bootstrap
//     catalog retrieval, so the embedding model used for indexing
//     and querying always matches.
//
// Both operations are read-only and concurrency-safe. The schemas map
// is immutable after construction — the engine reads it; the indexer
// writes a new map for the next run.
package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// vectorSearcher is the minimum surface CacheSchemaProvider needs from
// schema_retrieve.Retriever. Defined here as an interface so unit tests
// can inject a fake without spinning up Qdrant. The production wrapper
// in NewCacheSchemaProvider closes over a real *schema_retrieve.Retriever.
type vectorSearcher interface {
	Search(ctx context.Context, projectID string, vec []float64, opts schema_retrieve.SearchOpts) ([]schema_retrieve.Hit, error)
}

// CacheSchemaProvider implements ai.SchemaProvider against the cached
// schemas map + Qdrant retriever. Construct via NewCacheSchemaProvider
// to get the canonical ref index built once instead of on every Lookup.
type CacheSchemaProvider struct {
	projectID string
	datasets  []string

	// schemas is the per-table metadata loaded from the cache. The map
	// key is the qualified "dataset.table" form the warehouse provider
	// emits via DiscoverSchemas. We DO NOT mutate it — this struct only
	// reads.
	schemas map[string]models.TableSchema

	// refIndex is a fast lookup of qualified-name → canonical key. Built
	// at construction so each Lookup call is O(refs) instead of O(refs ×
	// schemas). Maps both the canonical "dataset.table" and bare "table"
	// forms (when unambiguous) to the canonical key.
	refIndex map[string]string

	// searcher is the vector-search surface. Optional — when nil, Search
	// returns an "unavailable" error so the engine reports the action
	// as unavailable. Lookup keeps working.
	searcher vectorSearcher

	// embedder turns Search queries into vectors for the searcher.
	// Required when searcher is set; nil when searcher is nil.
	embedder Embedder

	// sampleLimit caps the number of sample rows returned per table.
	// 0 → defaultLookupSampleLimit. Lower it for small-context models
	// via NewCacheSchemaProvider's options.
	sampleLimit int

	// columnLimit caps how many columns of a wide table are returned.
	// Wide audit / event tables can have 200+ columns; emitting all
	// of them overshadows the schema and explodes prompt tokens. 0
	// → defaultLookupColumnLimit.
	columnLimit int
}

// CacheSchemaProviderOptions configures NewCacheSchemaProvider.
//
// SampleLimit / ColumnLimit are caller-tunable so a project running on
// a small-context model can dial both down without touching engine
// code. Both default to the package constants when 0.
type CacheSchemaProviderOptions struct {
	ProjectID   string
	Datasets    []string
	Schemas     map[string]models.TableSchema
	Retriever   *schema_retrieve.Retriever
	Embedder    Embedder
	SampleLimit int
	ColumnLimit int
}

const (
	defaultLookupSampleLimit = 3
	defaultLookupColumnLimit = 50
)

// NewCacheSchemaProvider builds a CacheSchemaProvider and indexes the
// schemas map for fast Lookup. Returns an error when the schemas map
// is nil — that's an upstream wiring bug we want to surface immediately
// rather than have it manifest as "no tables found" at run time.
func NewCacheSchemaProvider(opts CacheSchemaProviderOptions) (*CacheSchemaProvider, error) {
	if opts.Schemas == nil {
		return nil, fmt.Errorf("cache_schema_provider: Schemas map is required")
	}
	if opts.Retriever != nil && opts.Embedder == nil {
		return nil, fmt.Errorf("cache_schema_provider: Embedder is required when Retriever is set")
	}

	p := &CacheSchemaProvider{
		projectID:   opts.ProjectID,
		datasets:    append([]string(nil), opts.Datasets...),
		schemas:     opts.Schemas,
		embedder:    opts.Embedder,
		sampleLimit: opts.SampleLimit,
		columnLimit: opts.ColumnLimit,
	}
	// Only wire the searcher when a retriever is configured. Leaving it as
	// a typed-nil interface would break the `p.searcher == nil` check in
	// Search, so we assign through the interface only when non-nil.
	if opts.Retriever != nil {
		p.searcher = opts.Retriever
	}
	if p.sampleLimit <= 0 {
		p.sampleLimit = defaultLookupSampleLimit
	}
	if p.columnLimit <= 0 {
		p.columnLimit = defaultLookupColumnLimit
	}
	p.refIndex = buildRefIndex(opts.Schemas)
	return p, nil
}

// Lookup resolves each ref against the schemas map and returns L1
// detail. Refs that don't match any known table land in NotFound.
// Per-call truncation (MaxLookupTablesPerCall) is enforced here as
// well as in the engine — keeping the rule duplicated lets a future
// caller use the provider without the engine's wrapper.
func (p *CacheSchemaProvider) Lookup(ctx context.Context, refs []string) (ai.LookupResult, error) {
	// Honour cancellation cheaply — the rest of the method is in-memory.
	select {
	case <-ctx.Done():
		return ai.LookupResult{}, ctx.Err()
	default:
	}

	truncated := false
	if len(refs) > ai.MaxLookupTablesPerCall {
		refs = refs[:ai.MaxLookupTablesPerCall]
		truncated = true
	}

	tables := make([]ai.LookupTable, 0, len(refs))
	notFound := make([]string, 0)
	seen := make(map[string]struct{}, len(refs))

	for _, raw := range refs {
		canonical, ok := p.resolveRef(raw)
		if !ok {
			notFound = append(notFound, raw)
			continue
		}
		if _, dup := seen[canonical]; dup {
			// Deduped within a single call (model named the same table twice).
			continue
		}
		seen[canonical] = struct{}{}
		tables = append(tables, p.toLookupTable(canonical, p.schemas[canonical]))
	}

	return ai.LookupResult{
		Tables:    tables,
		NotFound:  notFound,
		Truncated: truncated,
	}, nil
}

// Search runs a semantic query against the per-project Qdrant
// collection and returns the top hits. Results are clamped to k
// regardless of how many oversample candidates the retriever pulls.
func (p *CacheSchemaProvider) Search(ctx context.Context, query string, k int) ([]ai.SearchHit, error) {
	if p.searcher == nil {
		return nil, fmt.Errorf("schema search not available: retriever not configured")
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	if k <= 0 {
		k = ai.DefaultSearchTopK
	}
	if k > ai.MaxSearchTopK {
		k = ai.MaxSearchTopK
	}

	vec, err := p.embedder.Embed(ctx, []string{q})
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	if len(vec) == 0 || len(vec[0]) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors for query")
	}

	hits, err := p.searcher.Search(ctx, p.projectID, vec[0], schema_retrieve.SearchOpts{
		TopK:          k,
		RowCountPrior: 0.05,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}

	out := make([]ai.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, ai.SearchHit{
			Table:    h.Blurb.Table,
			Blurb:    h.Blurb.Blurb,
			RowCount: h.Blurb.RowCount,
			Score:    h.Score,
		})
	}
	return out, nil
}

// resolveRef matches an LLM-supplied ref against the canonical schemas
// map key. Tries the qualified form first, then falls back to bare
// "table" matching when there's exactly one dataset that contains a
// table by that name. Returns (canonicalKey, true) on a hit, ("", false)
// when the ref can't be resolved unambiguously.
func (p *CacheSchemaProvider) resolveRef(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if k, ok := p.refIndex[raw]; ok {
		return k, true
	}
	// Fallback: the LLM may have lowercased the dataset (BigQuery is
	// case-sensitive on dataset name; Snowflake on table name). Try a
	// case-insensitive match — only accept when there's exactly one
	// hit, otherwise it's ambiguous and we'd rather surface NotFound
	// than silently pick the wrong table.
	lc := strings.ToLower(raw)
	var matches []string
	for k := range p.refIndex {
		if strings.ToLower(k) == lc {
			matches = append(matches, p.refIndex[k])
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

// toLookupTable converts a discovery TableSchema to the lightweight
// ai.LookupTable shape, applying the per-table column cap. Columns are
// returned in their declared order (already preserved by the warehouse
// providers); category hints (primary_key / time / metric / dimension)
// pass through untouched.
func (p *CacheSchemaProvider) toLookupTable(canonical string, ts models.TableSchema) ai.LookupTable {
	cols := make([]ai.LookupColumn, 0, len(ts.Columns))
	for i, c := range ts.Columns {
		if i >= p.columnLimit {
			break
		}
		cols = append(cols, ai.LookupColumn{
			Name:     c.Name,
			Type:     c.Type,
			Nullable: c.Nullable,
			Category: c.Category,
		})
	}

	samples := ts.SampleData
	if len(samples) > p.sampleLimit {
		samples = samples[:p.sampleLimit]
	}

	return ai.LookupTable{
		Table:      canonical,
		RowCount:   ts.RowCount,
		Columns:    cols,
		SampleRows: samples,
	}
}

// buildRefIndex maps both qualified ("dataset.table") and bare ("table")
// forms to the canonical schemas-map key. Bare-form ambiguity is
// recorded by leaving the bare key OUT of the map entirely — the
// fallback in resolveRef already handles case-insensitive disambiguation,
// and we don't want a bare "users" to silently pick the wrong dataset.
func buildRefIndex(schemas map[string]models.TableSchema) map[string]string {
	idx := make(map[string]string, len(schemas)*2)
	bareCount := make(map[string]int, len(schemas))
	bareTo := make(map[string]string, len(schemas))

	// Canonical qualified form always indexes to itself.
	keys := make([]string, 0, len(schemas))
	for k := range schemas {
		keys = append(keys, k)
		idx[k] = k
	}
	sort.Strings(keys) // deterministic

	for _, k := range keys {
		// Bare table is everything after the LAST dot — same convention
		// the warehouse providers use when building the qualified key.
		bare := k
		if dot := strings.LastIndex(k, "."); dot >= 0 {
			bare = k[dot+1:]
		}
		bareCount[bare]++
		bareTo[bare] = k
	}
	for bare, n := range bareCount {
		if n == 1 {
			idx[bare] = bareTo[bare]
		}
	}
	return idx
}
