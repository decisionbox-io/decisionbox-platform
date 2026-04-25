package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

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

	// 1. Drop the old collection first so a failed half-written index
	// can't poison a subsequent search. Idempotent — missing → no-op.
	applog.Info("schema_indexer: phase=drop_collection")
	if err := si.Retriever.DropCollection(ctx, opts.ProjectID); err != nil {
		si.recordErr(ctx, opts.ProjectID, "drop prior collection: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: drop prior collection: %w", err)
	}

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
	// the old dimension so this creates a fresh collection.
	applog.WithField("dimensions", si.Embedder.Dimensions()).Info("schema_indexer: phase=ensure_collection")
	if err := si.Retriever.EnsureCollection(ctx, opts.ProjectID, si.Embedder.Dimensions()); err != nil {
		si.recordErr(ctx, opts.ProjectID, "ensure collection: "+err.Error())
		return nil, fmt.Errorf("schema_indexer: ensure collection: %w", err)
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
	// "dataset.table"; we split that back out so the Blurb input carries
	// the dataset.
	type orderedRef struct {
		dataset string
		schema  models.TableSchema
	}
	refs := make([]orderedRef, 0, len(schemas))
	inputs := make([]blurb.Input, 0, len(schemas))
	for qualified, s := range schemas {
		dataset := ""
		if i := indexDot(qualified); i > 0 {
			dataset = qualified[:i]
		}
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
	applog.WithFields(applog.Fields{"points": len(items)}).Info("schema_indexer: phase=qdrant_upsert")
	if err := si.Retriever.Upsert(ctx, opts.ProjectID, items); err != nil {
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
		hit, cacheErr := si.Cache.Find(ctx, opts.ProjectID, si.WarehouseHash)
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
		if err := si.Cache.Save(ctx, opts.ProjectID, si.WarehouseHash, schemas); err != nil {
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

// indexDot returns the index of the first '.' in s (or -1). Used to
// split "dataset.table" → dataset. strings.Index would work too but
// this stays dependency-free inside a hot loop.
func indexDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
