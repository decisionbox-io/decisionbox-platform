package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// CatalogSource supplies a source's queryable items when that source has no
// tables. Narrowed to what the indexer needs so a test can supply one without
// a warehouse; production wires warehouse.CatalogSource through.
type CatalogSource interface {
	Catalog(ctx context.Context) ([]gowarehouse.CatalogItem, error)
}

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

	// Catalog is optional. When non-nil the source describes itself with a
	// catalog of items rather than a set of tables, and BuildIndex takes the
	// catalog path: no table discovery (this source has none, and asking
	// would fail the run), and no blurb generation (the source supplies its
	// own descriptions, so the LLM pass is both unnecessary and a cost).
	Catalog CatalogSource

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
	Tables         int
	Dropped        int
	BlurbTokensIn  int
	BlurbTokensOut int
	Duration       time.Duration
}

// BuildIndex runs the full schema-indexing pipeline. See type doc for
// side effects and ordering.
func (si *SchemaIndexer) BuildIndex(ctx context.Context, opts IndexOptions) (*Stats, error) {
	if opts.ProjectID == "" {
		return nil, errors.New("schema_indexer: ProjectID is required")
	}
	// Discovery and Blurber belong to the table path. A catalog source has no
	// tables to discover and brings its own descriptions, so requiring them
	// would demand two collaborators that would never be called.
	if si.Catalog == nil {
		if si.Discovery == nil {
			return nil, errors.New("schema_indexer: Discovery is required")
		}
		if si.Blurber == nil {
			return nil, errors.New("schema_indexer: Blurber is required")
		}
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

	if si.Catalog != nil {
		return si.buildCatalogIndex(ctx, opts, start)
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
	applog.WithFields(applog.Fields{
		"tables":     len(schemas),
		"elapsed":    time.Since(discoveryStart).String(),
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
	applog.WithField("elapsed", time.Since(blurbStart).String()).Info("schema_indexer: blurb generation complete")

	// 5. Embed + upsert.
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
	for i, b := range blurbs {
		if b.Err != nil || b.Blurb == "" {
			continue
		}
		kept = append(kept, idxBlurb{i: i, blurb: b})
		texts = append(texts, b.Blurb)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("schema_indexer: no usable blurbs to embed")
	}

	vectors, err := si.Embedder.Embed(ctx, texts)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "embed: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: embed: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("schema_indexer: embedder returned %d vectors for %d blurbs", len(vectors), len(texts))
	}

	items := make([]schema_retrieve.UpsertItem, 0, len(kept))
	var blurbIn, blurbOut int
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
		blurbIn += k.blurb.InputTokens
		blurbOut += k.blurb.OutputTokens
	}
	// Stamp the running totals onto the progress doc so the dashboard
	// can show "tokens spent on this schema-index" without re-deriving
	// it from per-blurb forensics. One IncrementTokens call covers the
	// whole build because blurbs are generated in one parallel pass —
	// there is no streaming-mid-build requirement today.
	//
	// Failure semantics:
	//   - blurb.Generate returns err (whole batch failed) → we returned
	//     early above, so the progress doc stays at the Reset() zeros.
	//   - Individual blurbs failed but Generate returned ok → only the
	//     successful blurbs feed blurbIn/blurbOut (loop above skips
	//     entries with Err != nil), and those tokens are stamped here.
	//   - Embedding or Qdrant upsert below fails → the totals stamped
	//     here are preserved, so users still see what the blurb LLM
	//     consumed even when the index itself was never written.
	if si.Progress != nil && (blurbIn > 0 || blurbOut > 0) {
		if err := si.Progress.IncrementTokens(ctx, opts.ProjectID, blurbIn, blurbOut); err != nil {
			applog.WithError(err).Warn("schema_indexer: IncrementTokens failed (non-fatal)")
		}
	}
	applog.WithFields(applog.Fields{"points": len(items)}).Info("schema_indexer: phase=qdrant_upsert")
	if err := si.Retriever.Upsert(ctx, opts.ProjectID, si.WarehouseID, items); err != nil {
		si.recordErr(ctx, opts.ProjectID, "qdrant upsert: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: qdrant upsert: %w", err)
	}
	applog.WithFields(applog.Fields{
		"tables":           len(items),
		"total_elapsed":    time.Since(start).String(),
		"blurb_tokens_in":  blurbIn,
		"blurb_tokens_out": blurbOut,
	}).Info("schema_indexer: BuildIndex complete")

	return &Stats{
		Tables:         len(items),
		Dropped:        len(schemas) - len(items),
		BlurbTokensIn:  blurbIn,
		BlurbTokensOut: blurbOut,
		Duration:       time.Since(start),
	}, nil
}

// retractCatalog withdraws whatever a previous run indexed for this
// datasource, for use when the current run has established there is nothing
// usable to replace it with.
//
// Best-effort by design. It runs on a failing path, so an error here is logged
// rather than returned: replacing the reason the run failed with the reason
// the cleanup failed would hide the problem the operator needs to see. What it
// must not do is nothing at all — stale points and stale refs together are
// exactly what makes a removed item keep answering.
func (si *SchemaIndexer) retractCatalog(ctx context.Context, projectID string) {
	if si.Retriever != nil {
		if err := si.Retriever.DeleteWarehousePoints(ctx, projectID, si.WarehouseID); err != nil {
			applog.WithError(err).Warn("schema_indexer: could not clear this datasource's prior vector points; removed items may stay searchable until the next successful index")
		}
	}
	if cc, ok := si.Cache.(CatalogCache); ok && si.WarehouseHash != "" {
		if err := cc.SaveCatalog(ctx, projectID, si.WarehouseID, si.WarehouseHash, nil); err != nil {
			applog.WithError(err).Warn("schema_indexer: could not retract this datasource's cached catalog; removed items may stay authorised until the next successful index")
		}
	}
}

// indexableCatalogItems keeps what is worth indexing and counts what is not.
//
// Each exclusion has its own reason. An item the credential cannot query is
// real and catalogued at the source, but indexing it would only produce
// queries that fail. An item with no ref could never be written into a query.
// An item with no text has nothing to embed, so it would match everything or
// nothing.
//
// An item with no KIND is the subtle one, and the most damaging. The empty
// kind is reserved for tables, so such an item indexes happily and is then
// filtered against the table schema map — which a catalog source has none of
// — and every hit for it is discarded. The datasource finishes indexing as
// ready and returns nothing, with no error at any layer. Dropping it here
// turns that into a count, and into a failed run when it is all of them.
//
// A duplicate ref is dropped rather than overwriting, so the first
// description of an item wins deterministically.
func indexableCatalogItems(items []gowarehouse.CatalogItem) (kept []gowarehouse.CatalogItem, dropped int) {
	seen := make(map[string]bool, len(items))
	kept = make([]gowarehouse.CatalogItem, 0, len(items))
	for _, it := range items {
		if it.Kind == gowarehouse.ItemKindTable ||
			it.Ref == "" || it.Text == "" || !it.Queryable() || seen[it.Ref] {
			continue
		}
		seen[it.Ref] = true
		kept = append(kept, it)
	}
	return kept, len(items) - len(kept)
}

// buildCatalogIndex is BuildIndex for a source that describes itself with a
// catalog of items instead of a set of tables.
//
// It is a separate path rather than a branch threaded through the table path
// because the two share only their last step. There is no discovery leg (the
// source has none, and asking it to list tables fails), and no blurb leg (the
// source ships its own descriptions, so an LLM pass would spend tokens
// rewriting text that is already better than what it would produce, and make
// indexing non-idempotent for no gain).
//
// Everything from embedding onwards is deliberately the same as the table
// path, including clearing only this warehouse's points, so a catalog source
// coexists with warehouses in one project collection.
func (si *SchemaIndexer) buildCatalogIndex(ctx context.Context, opts IndexOptions, start time.Time) (*Stats, error) {
	if si.Progress != nil {
		if err := si.Progress.SetPhase(ctx, opts.ProjectID, models.SchemaIndexPhaseSchemaDiscovery); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetPhase schema_discovery failed")
		}
	}
	applog.Info("schema_indexer: phase=read_catalog")

	items, err := si.Catalog.Catalog(ctx)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "read catalog: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: read catalog: %w", err)
	}

	indexable, dropped := indexableCatalogItems(items)

	applog.WithFields(applog.Fields{
		"catalog_items": len(items),
		"indexable":     len(indexable),
		"dropped":       dropped,
	}).Info("schema_indexer: catalog read")

	if len(indexable) == 0 {
		// An empty index is not a usable source, and returning success here
		// would leave the datasource reporting "ready" with nothing in it —
		// the model would then never retrieve anything from it and no error
		// would say why.
		// Failing is not enough on its own. This run has established that the
		// source currently offers nothing usable, so whatever a previous run
		// indexed is no longer true. Leaving it would keep every consumer
		// treating removed or now-inaccessible items as current, and the
		// failure the operator sees would not describe what the system is
		// still doing. Retract first, then fail.
		si.retractCatalog(ctx, opts.ProjectID)
		si.recordErr(ctx, opts.ProjectID, "catalog contained no indexable items")
		return nil, fmt.Errorf("schema_indexer: catalog contained no indexable items (%d returned, all unusable) — check the credential's access to this source", len(items))
	}

	if si.Progress != nil {
		if err := si.Progress.SetTotals(ctx, opts.ProjectID, len(indexable)); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetTotals failed")
		}
	}

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
	if err := si.Retriever.DeleteWarehousePoints(ctx, opts.ProjectID, si.WarehouseID); err != nil {
		si.recordErr(ctx, opts.ProjectID, "clear warehouse points: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: clear warehouse points: %w", err)
	}

	if si.Progress != nil {
		if err := si.Progress.SetPhase(ctx, opts.ProjectID, models.SchemaIndexPhaseEmbedding); err != nil {
			applog.WithError(err).Warn("schema_indexer: SetPhase embedding failed")
		}
	}
	applog.Info("schema_indexer: phase=embedding")

	texts := make([]string, 0, len(indexable))
	for _, it := range indexable {
		texts = append(texts, it.Text)
	}
	vectors, err := si.Embedder.Embed(ctx, texts)
	if err != nil {
		si.recordErr(ctx, opts.ProjectID, "embed: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: embed: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("schema_indexer: embedder returned %d vectors for %d catalog items", len(vectors), len(texts))
	}

	points := make([]schema_retrieve.UpsertItem, 0, len(indexable))
	for i, it := range indexable {
		points = append(points, schema_retrieve.UpsertItem{
			Blurb: schema_retrieve.TableBlurb{
				// The ref is what a query must name, stored verbatim. No
				// dataset qualification: this source has none, and prefixing
				// one would store a name it does not have.
				Table:          it.Ref,
				Kind:           it.Kind,
				Blurb:          it.Text,
				Keywords:       opts.Keywords,
				EmbeddingModel: si.Embedder.ModelName(),
			},
			Vector: vectors[i],
		})
	}

	applog.WithField("points", len(points)).Info("schema_indexer: phase=qdrant_upsert")
	if err := si.Retriever.Upsert(ctx, opts.ProjectID, si.WarehouseID, points); err != nil {
		si.recordErr(ctx, opts.ProjectID, "qdrant upsert: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: qdrant upsert: %w", err)
	}
	// Record what this source currently offers. The vector index alone
	// cannot answer "is this datasource indexed, and with what?" — a
	// consumer reading it has no way to tell a live point from one left
	// behind by a datasource that was since removed, because re-indexing
	// only clears the warehouses it still indexes. The cache entry, keyed by
	// the current config hash, is that authority.
	//
	// Best-effort, and deliberately after the upsert: a failure here leaves a
	// usable index that consumers will treat as unindexed until the next run,
	// which is the safe direction. Losing the points would not be.
	if cc, ok := si.Cache.(CatalogCache); ok && si.WarehouseHash != "" {
		// The items rather than their refs: the cache records which of them are
		// dimensions, and that is only knowable here.
		if err := cc.SaveCatalog(ctx, opts.ProjectID, si.WarehouseID, si.WarehouseHash, indexable); err != nil {
			applog.WithError(err).Warn("schema_indexer: catalog cache save failed; consumers will treat this datasource as unindexed until the next run")
		}
	}

	// Close out progress. The catalog path has no per-item leg to report
	// against — one metadata read, one embedding batch, one upsert — so the
	// counter completes in a single step rather than climbing. Leaving it
	// unset is the one thing that would be wrong: the run finishes and the
	// progress UI keeps showing 0 of N, which reads as a stalled index rather
	// than a finished one.
	if si.Progress != nil {
		if err := si.Progress.SetCounters(ctx, opts.ProjectID, len(points), len(points)); err != nil {
			applog.WithError(err).Warn("schema_indexer: completing catalog progress counters failed (non-fatal)")
		}
	}

	applog.WithFields(applog.Fields{
		"items":         len(points),
		"dropped":       dropped,
		"total_elapsed": time.Since(start).String(),
	}).Info("schema_indexer: BuildIndex complete (catalog)")

	return &Stats{
		Tables:   len(points),
		Dropped:  dropped,
		Duration: time.Since(start),
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
