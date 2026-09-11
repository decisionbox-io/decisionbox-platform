package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// SchemaIndexer runs a single "index this project's schema" pass:
//
//  1. drop any existing per-project Qdrant collection (idempotent)
//  2. delegate table+schema+sample-data discovery to SchemaDiscovery
//     — the exact same path discovery would have used, so any
//     warehouse-specific SampleQueryBuilder support is inherited.
//  3. generate a blurb for every table via the blurb generator
//  4. embed each blurb via the embedding provider
//  5. upsert (vector, blurb, metadata) into Qdrant
//  6. report progress to the project_schema_index_progress collection
//
// Full rebuilds only. A failed run leaves the collection dropped and
// lets the next user-triggered retry start from a clean slate (plan §4
// — "partial progress is thrown away on failure").
//
// The indexer intentionally does NOT write the project lifecycle status
// (pending_indexing → indexing → ready/failed). The API's worker loop
// owns those transitions so ctrl-C on the agent doesn't leave projects
// stuck in "indexing" forever; the worker flips to "failed" on any
// agent exit code that isn't 0.
type SchemaIndexer struct {
	// Discovery supplies the full TableSchema set. The concrete
	// SchemaDiscovery type satisfies this interface trivially; the
	// interface exists so unit tests can plug a fake without touching
	// the warehouse layer.
	Discovery SchemaSource
	Blurber   *blurb.Generator
	Embedder  Embedder
	Retriever *schema_retrieve.Retriever
	Progress  ProgressReporter

	// Cache is optional. When non-nil and a hit is present for the
	// current (ProjectID, WarehouseHash), BuildIndex skips the catalog
	// pass and reuses the stored TableSchema map. Nil keeps the old
	// always-rediscover behaviour (which is also what unit tests use).
	Cache SchemaCache
	// WarehouseHash is computed by the caller (agentserver) from the
	// project's WarehouseConfig via WarehouseConfigHash. Empty hash
	// disables the cache for this run even if Cache is set.
	WarehouseHash string
	// WarehouseID names the warehouse being indexed (multi-warehouse).
	// It scopes the schema cache and the Qdrant points so indexing one
	// warehouse never touches another's catalog. Empty resolves to the
	// project's default/primary warehouse.
	WarehouseID string
}

// SchemaSource is the slim subset of discovery.SchemaDiscovery the
// indexer actually consumes — just "give me all the tables." Defining
// it here (instead of reaching into SchemaDiscovery directly) means a
// future schema-discovery rewrite doesn't cascade into the indexer.
type SchemaSource interface {
	DiscoverSchemas(ctx context.Context) (map[string]models.TableSchema, error)
}

// Embedder is the minimum surface the indexer needs from an embedding
// provider. Matches libs/go-common/embedding.Provider exactly; declared
// here as its own interface so unit tests can inject fakes without
// pulling the whole package in.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Dimensions() int
	ModelName() string
}

// ProgressReporter mirrors services/agent/internal/database.SchemaIndexProgressRepository
// for what the indexer actually needs. Again a small interface for
// testability — the concrete repo satisfies it trivially.
type ProgressReporter interface {
	Reset(ctx context.Context, projectID, runID string) error
	SetPhase(ctx context.Context, projectID, phase string) error
	SetTotals(ctx context.Context, projectID string, total int) error
	SetCounters(ctx context.Context, projectID string, total, done int) error
	IncrementDone(ctx context.Context, projectID string, delta int) error
	// IncrementTokens advances the per-build blurb-LLM token totals
	// atomically.
	IncrementTokens(ctx context.Context, projectID string, inputDelta, outputDelta int) error
	RecordError(ctx context.Context, projectID, msg string) error
}

// IndexOptions feed into a single BuildIndex call.
type IndexOptions struct {
	ProjectID       string
	RunID           string
	BlurbModelLabel string   // human-readable "provider/model" for payload auditing
	DomainBlurb     string   // optional: 1-2 sentence project-pack context for grounding
	Keywords        []string // optional: domain-pack keywords stored on every table
}

// Stats is what BuildIndex returns on success.
type Stats struct {
	Tables int
	// Blurbs is the number of usable blurbs generated (those that were
	// embedded + upserted). Equal to Tables in today's pipeline — every
	// upserted point carries a blurb — but reported separately so the
	// per-datasource run record stays honest if the two ever diverge.
	Blurbs         int
	Dropped        int
	BlurbTokensIn  int
	BlurbTokensOut int
	Duration       time.Duration
	// PhaseDurations maps a phase name (models.SchemaIndexPhase*) to the
	// wall-clock time spent in it. Listing is folded into schema_discovery
	// (DiscoverSchemas lists tables internally, so there is no separately
	// measurable listing leg here).
	PhaseDurations map[string]time.Duration
}

// BuildIndex runs the full schema-indexing pipeline. See type doc for
// side effects and ordering.
func (si *SchemaIndexer) BuildIndex(ctx context.Context, opts IndexOptions) (*Stats, error) {
	if opts.ProjectID == "" {
		return nil, errors.New("schema_indexer: ProjectID is required")
	}
	if si.Discovery == nil {
		return nil, errors.New("schema_indexer: Discovery is required")
	}
	if si.Blurber == nil {
		return nil, errors.New("schema_indexer: Blurber is required")
	}
	if si.Embedder == nil {
		return nil, errors.New("schema_indexer: Embedder is required")
	}
	if si.Retriever == nil {
		return nil, errors.New("schema_indexer: Retriever is required")
	}

	start := time.Now()
	applog.WithFields(applog.Fields{
		"project_id":  opts.ProjectID,
		"run_id":      opts.RunID,
		"blurb_model": opts.BlurbModelLabel,
	}).Info("schema_indexer: BuildIndex starting")

	// 0. Progress reset. Worker loop has already flipped status to
	// "indexing"; Reset clears any counters left over from a prior failed
	// run for the same project.
	if si.Progress != nil {
		if err := si.Progress.Reset(ctx, opts.ProjectID, opts.RunID); err != nil {
			return nil, fmt.Errorf("schema_indexer: progress reset: %w", err)
		}
	}

	// 1. (Point clearing is per-warehouse and deferred to just before the
	// write phase — see below. We deliberately do NOT drop the whole
	// collection here: it is shared by every warehouse in the project, so
	// dropping it would wipe the other warehouses' vectors. The old points
	// for THIS warehouse are deleted after EnsureCollection, so a failed
	// discovery leaves the previous index searchable instead of empty.)

	// 2. Discover tables + schemas. When the cache has a hit for the
	// current warehouse hash we skip the catalog pass entirely — every
	// subsequent blurb LLM / embedding / Qdrant step stays the same, so
	// the only thing we sidestep is the slow MSSQL/BigQuery/Snowflake
	// introspection. The cache is best-effort: any failure falls
	// through to fresh discovery and logs a warning.
	discoveryStart := time.Now()
	applog.Info("schema_indexer: phase=discover_schemas (this may take minutes on ERP-scale warehouses)")
	if si.Progress != nil {
		if err := si.Progress.SetPhase(ctx, opts.ProjectID, models.SchemaIndexPhaseSchemaDiscovery); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetPhase schema_discovery failed")
		}
	}

	schemas, fromCache, err := si.resolveSchemas(ctx, opts)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "discover schemas: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: discover schemas: %w", err)
	}
	// On a cache HIT, resolveSchemas returned the stored map WITHOUT running
	// SchemaDiscovery — so its index-time ListTables filter never ran, and a
	// re-index would blurb + embed every previously-cached table even after the
	// scope narrowed. Apply the registered cached-schema filters here to narrow
	// the stale cache to the current scope. We do this ONLY on a cache hit: on a
	// fresh discovery the ListTables filters already governed the set (that is
	// the documented index-time hook), so re-applying the run-time cached-schema
	// hook would be redundant and would wrongly let a CachedSchema-only plugin
	// constrain the physical index build. The filter can only remove keys, never
	// add (widening needs the cache invalidated — the caller's re-scope path does
	// that); a filter error fails the run closed rather than indexing an unscoped
	// catalog. No-op in the community build.
	if fromCache && len(schemas) > 0 {
		keys := make([]string, 0, len(schemas))
		for k := range schemas {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic input, mirroring Orchestrator.discoverSchemas
		kept, ferr := agentplugin.ApplyCachedSchemaFilters(ctx, opts.ProjectID, keys)
		if ferr != nil {
			si.recordErr(ctx, opts.ProjectID, "cached-schema filter: "+ferr.Error())
			return nil, fmt.Errorf("schema_indexer: cached-schema filter: %w", ferr)
		}
		// A filter may only SHRINK the catalog. Reject any returned key that
		// wasn't in the input (a stale/typo'd scope entry, or a misbehaving
		// plugin) — failing closed rather than silently building a partial index
		// over the remaining tables. Same contract Orchestrator.discoverSchemas
		// enforces on the run-time path.
		inputSet := make(map[string]struct{}, len(keys))
		for _, k := range keys {
			inputSet[k] = struct{}{}
		}
		for _, k := range kept {
			if _, ok := inputSet[k]; !ok {
				si.recordErr(ctx, opts.ProjectID, "cached-schema filter invented key: "+k)
				return nil, fmt.Errorf("schema_indexer: cached-schema filter returned %q which was not in the cached input; filters may only shrink the catalog", k)
			}
		}
		// Rebuild the map from the kept keys. Rebuilding from the validated kept
		// set (not trusting length) means a same-length-but-different result
		// can't slip a dropped table back into the index.
		keptSet := make(map[string]struct{}, len(kept))
		for _, k := range kept {
			keptSet[k] = struct{}{}
		}
		filtered := make(map[string]models.TableSchema, len(kept))
		for k, v := range schemas {
			if _, ok := keptSet[k]; ok {
				filtered[k] = v
			}
		}
		schemas = filtered
	}
	schemaDiscoveryDur := time.Since(discoveryStart)
	applog.WithFields(applog.Fields{
		"tables":     len(schemas),
		"elapsed":    schemaDiscoveryDur.String(),
		"from_cache": fromCache,
	}).Info("schema_indexer: phase=discover_schemas complete")
	if len(schemas) == 0 {
		return nil, fmt.Errorf("schema_indexer: no tables discovered — check datasets and warehouse permissions")
	}

	// 3. Provision Qdrant with the embedder's dimension count. If the
	// caller swapped embedding models, the DropCollection above cleared
	// the old dimension so this creates a fresh collection. Resolve the
	// dimension robustly: the provider's declared size when it knows it,
	// otherwise a probe embedding — so a model the catalog doesn't know
	// (e.g. the decisionbox-embed-model gateway alias, which would report 0)
	// still sizes its collection correctly. Done before the long blurb phase
	// so a Qdrant/embedding misconfig fails fast.
	dims, err := resolveEmbeddingDimensions(ctx, si.Embedder)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "resolve dimensions: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: resolve dimensions: %w", err)
	}
	applog.WithField("dimensions", dims).Info("schema_indexer: phase=ensure_collection")
	if err := si.Retriever.EnsureCollection(ctx, opts.ProjectID, dims); err != nil {
		si.recordErr(ctx, opts.ProjectID, "ensure collection: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: ensure collection: %w", err)
	}

	// Clear only THIS warehouse's prior points before re-writing them, so a
	// re-index of one warehouse leaves every other warehouse's vectors
	// intact (must-fix #2). No-op on a freshly (re)created collection.
	if err := si.Retriever.DeleteWarehousePoints(ctx, opts.ProjectID, si.WarehouseID); err != nil {
		si.recordErr(ctx, opts.ProjectID, "clear warehouse points: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: clear warehouse points: %w", err)
	}

	if si.Progress != nil {
		// Reset counters for the blurb phase — describing_tables has its
		// own 0→N progression separate from the schema-discovery leg.
		if err := si.Progress.SetCounters(ctx, opts.ProjectID, len(schemas), 0); err != nil {
			applog.WithError(err).Warn("schema_indexer: reset counters for describing_tables failed")
		}
		if err := si.Progress.SetPhase(ctx, opts.ProjectID, models.SchemaIndexPhaseDescribingTables); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetPhase describing_tables failed")
		}
	}
	applog.WithField("tables", len(schemas)).Info("schema_indexer: phase=describing_tables (blurb generation)")

	// 4. Build blurb inputs. DiscoverSchemas keys the map as
	// "dataset.table" (single-project) or "dataproject.dataset.table"
	// (cross-project BigQuery); we split the dataset back out so the Blurb
	// input and the retrieval metadata carry the real dataset (not the data
	// project).
	type orderedRef struct {
		dataset string
		schema  models.TableSchema
	}
	refs := make([]orderedRef, 0, len(schemas))
	inputs := make([]blurb.Input, 0, len(schemas))
	for qualified, s := range schemas {
		dataset := datasetFromQualified(qualified)
		refs = append(refs, orderedRef{dataset: dataset, schema: s})
		inputs = append(inputs, blurb.Input{
			Dataset:         dataset,
			Schema:          s,
			DomainPackBlurb: opts.DomainBlurb,
		})
	}

	progressCB := func(_ int) {
		if si.Progress != nil {
			if err := si.Progress.IncrementDone(ctx, opts.ProjectID, 1); err != nil {
				applog.WithError(err).Debug("schema_indexer: IncrementDone failed (non-fatal)")
			}
		}
	}
	blurbStart := time.Now()
	blurbs, err := si.Blurber.Generate(ctx, inputs, progressCB)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "blurb generation: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: blurb generation: %w", err)
	}
	describingDur := time.Since(blurbStart)
	applog.WithField("elapsed", describingDur.String()).Info("schema_indexer: blurb generation complete")

	// 5. Embed + upsert.
	embedStart := time.Now()
	if si.Progress != nil {
		if err := si.Progress.SetPhase(ctx, opts.ProjectID, models.SchemaIndexPhaseEmbedding); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetPhase embedding failed")
		}
	}
	applog.Info("schema_indexer: phase=embedding")

	type idxBlurb struct {
		i     int
		blurb blurb.Output
	}
	var kept []idxBlurb
	texts := make([]string, 0, len(blurbs))
	var blurbIn, blurbOut int
	for i, b := range blurbs {
		if b.Err != nil || b.Blurb == "" {
			continue
		}
		kept = append(kept, idxBlurb{i: i, blurb: b})
		texts = append(texts, b.Blurb)
		blurbIn += b.InputTokens
		blurbOut += b.OutputTokens
	}

	// Stamp the running blurb-token totals onto the progress doc now (before
	// embed/upsert) so the dashboard can show "tokens spent on this
	// schema-index" without re-deriving it. One IncrementTokens call covers the
	// whole build because blurbs are generated in one parallel pass. Summed
	// over successful blurbs only (the loop above skips Err/empty entries).
	if si.Progress != nil && (blurbIn > 0 || blurbOut > 0) {
		if err := si.Progress.IncrementTokens(ctx, opts.ProjectID, blurbIn, blurbOut); err != nil {
			applog.WithError(err).Warn("schema_indexer: IncrementTokens failed (non-fatal)")
		}
	}

	// partialStats carries what the build already spent (blurb tokens + the
	// phases it completed), returned ALONGSIDE an error on the post-blurb
	// failure paths. The caller stamps it into the durable failure record, so a
	// run that dies in embed/upsert still preserves its token + phase-duration
	// audit data — the live progress doc is reset on the next run, so the
	// durable record is the only lasting home for it. embedding is included
	// only once that phase has a meaningful duration (success path).
	partialStats := func() *Stats {
		return &Stats{
			Blurbs:         len(kept),
			BlurbTokensIn:  blurbIn,
			BlurbTokensOut: blurbOut,
			Duration:       time.Since(start),
			PhaseDurations: map[string]time.Duration{
				models.SchemaIndexPhaseSchemaDiscovery:  schemaDiscoveryDur,
				models.SchemaIndexPhaseDescribingTables: describingDur,
			},
		}
	}

	if len(texts) == 0 {
		return partialStats(), fmt.Errorf("schema_indexer: no usable blurbs to embed")
	}

	vectors, err := si.Embedder.Embed(ctx, texts)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "embed: "+err.Error())
		return partialStats(), fmt.Errorf("schema_indexer: embed: %w", err)
	}
	if len(vectors) != len(texts) {
		return partialStats(), fmt.Errorf("schema_indexer: embedder returned %d vectors for %d blurbs", len(vectors), len(texts))
	}

	items := make([]schema_retrieve.UpsertItem, 0, len(kept))
	for j, k := range kept {
		ref := refs[k.i]
		items = append(items, schema_retrieve.UpsertItem{
			Blurb: schema_retrieve.TableBlurb{
				Table:          ref.schema.TableName,
				Dataset:        ref.dataset,
				Blurb:          k.blurb.Blurb,
				Keywords:       opts.Keywords,
				RowCount:       ref.schema.RowCount,
				ColumnCount:    len(ref.schema.Columns),
				BlurbModel:     opts.BlurbModelLabel,
				EmbeddingModel: si.Embedder.ModelName(),
			},
			Vector: vectors[j],
		})
	}
	applog.WithFields(applog.Fields{"points": len(items)}).Info("schema_indexer: phase=qdrant_upsert")
	if err := si.Retriever.Upsert(ctx, opts.ProjectID, si.WarehouseID, items); err != nil {
		si.recordErr(ctx, opts.ProjectID, "qdrant upsert: "+err.Error())
		return partialStats(), fmt.Errorf("schema_indexer: qdrant upsert: %w", err)
	}
	embeddingDur := time.Since(embedStart)
	applog.WithFields(applog.Fields{
		"tables":           len(items),
		"total_elapsed":    time.Since(start).String(),
		"blurb_tokens_in":  blurbIn,
		"blurb_tokens_out": blurbOut,
	}).Info("schema_indexer: BuildIndex complete")

	return &Stats{
		Tables:         len(items),
		Blurbs:         len(kept),
		Dropped:        len(schemas) - len(items),
		BlurbTokensIn:  blurbIn,
		BlurbTokensOut: blurbOut,
		Duration:       time.Since(start),
		PhaseDurations: map[string]time.Duration{
			models.SchemaIndexPhaseSchemaDiscovery:  schemaDiscoveryDur,
			models.SchemaIndexPhaseDescribingTables: describingDur,
			models.SchemaIndexPhaseEmbedding:        embeddingDur,
		},
	}, nil
}

// resolveSchemas returns the TableSchema map to index, tagging whether
// it came from the cache. The cache is strictly best-effort: Find
// errors degrade to a fresh discovery (logged + continue) and Save
// errors don't fail the run (the next run just rediscovers).
//
// Extracted as its own method so it can be unit-tested without a live
// Qdrant — the rest of BuildIndex needs a real *schema_retrieve.Retriever.
func (si *SchemaIndexer) resolveSchemas(ctx context.Context, opts IndexOptions) (map[string]models.TableSchema, bool, error) {
	cacheActive := si.Cache != nil && si.WarehouseHash != ""

	if cacheActive {
		hit, cacheErr := si.Cache.Find(ctx, opts.ProjectID, si.WarehouseID, si.WarehouseHash)
		if cacheErr != nil {
			applog.WithError(cacheErr).Warn("schema_indexer: schema-cache lookup failed; falling through to fresh discovery")
		} else if len(hit) > 0 {
			applog.WithField("tables", len(hit)).Info("schema_indexer: schema cache hit — skipping catalog pass")
			return hit, true, nil
		}
	}

	schemas, err := si.Discovery.DiscoverSchemas(ctx)
	if err != nil {
		return nil, false, err
	}
	if cacheActive && len(schemas) > 0 {
		if err := si.Cache.Save(ctx, opts.ProjectID, si.WarehouseID, si.WarehouseHash, schemas); err != nil {
			applog.WithError(err).Warn("schema_indexer: schema-cache save failed; next run will rediscover")
		}
	}
	return schemas, false, nil
}

func (si *SchemaIndexer) recordErr(ctx context.Context, projectID, msg string) {
	if si.Progress == nil {
		return
	}
	if err := si.Progress.RecordError(ctx, projectID, msg); err != nil {
		applog.WithError(err).Debug("schema_indexer: RecordError failed (non-fatal)")
	}
}

// datasetFromQualified extracts the dataset component from a qualified
// schema-map key. The key is "dataset.table" for single-project warehouses
// and "dataproject.dataset.table" for cross-project BigQuery, so the dataset
// is always the segment immediately before the table — between the last two
// dots, or before the only dot. Returns "" for a bare (dot-less) name.
// Dependency-free (no strings.Split) since this runs per table in the
// indexing hot loop.
func datasetFromQualified(s string) string {
	last, prev := -1, -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			prev, last = last, i
		}
	}
	if last < 0 {
		return ""
	}
	return s[prev+1 : last]
}
