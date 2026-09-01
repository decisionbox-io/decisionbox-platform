package agentserver

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/askserve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/insightsearch"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// runAskServe starts the ad-hoc data Q&A serve mode: an always-up HTTP service
// that answers natural-language data questions by running a bounded,
// tool-using reasoning loop against each project's warehouse(s) (read-only) and
// schema index. It reuses the agent's existing per-project provider wiring
// (warehouse, LLM, schema retriever) behind a warm connection pool; each
// question runs as a long-lived background job whose progress is persisted to
// Mongo and read back by cursor — the same shape a discovery run uses.
//
// Multi-warehouse: a project may own several SQL datasources. Schema knowledge
// (lookup + cross-datasource search) is loaded eagerly for every datasource —
// it needs no live connection — while the warehouse SQL connections are built
// lazily per datasource on first query, so a turn only pays to connect to the
// datasources it actually touches and one broken datasource never blocks the
// rest.
func runAskServe(cfg *config.Config) error {
	ctx := context.Background()

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)
	projectRepo := database.NewProjectRepository(db)
	schemaCache := database.NewSchemaCacheRepository(db)

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	// One shared schema retriever (Qdrant) reused across projects — lookups
	// resolve from the cached schemas map regardless, and only semantic search
	// needs it. A missing retriever degrades search, not the whole service.
	sharedRetriever, rErr := newSchemaRetriever(cfg)
	if rErr != nil {
		applog.WithError(rErr).Warn("ask-serve: schema retriever unavailable — semantic table search disabled")
		sharedRetriever = nil
	} else {
		defer func() { _ = sharedRetriever.Close() }()
	}

	// One shared vector store (Qdrant) for the insight/recommendation index,
	// reused across projects for the search_insights tool. A missing store
	// degrades that one tool, not the whole service.
	vectorStore, vsCleanup, vsErr := initQdrant(ctx, cfg)
	if vsErr != nil {
		applog.WithError(vsErr).Warn("ask-serve: vector store unavailable — search_insights disabled")
		vectorStore = nil
	} else {
		defer vsCleanup()
	}

	serveCfg := askserve.LoadConfig()

	builder := func(buildCtx context.Context, projectID string) (*askserve.ProjectRuntime, error) {
		project, err := projectRepo.GetByID(buildCtx, projectID)
		if err != nil {
			return nil, fmt.Errorf("load project %s: %w", projectID, err)
		}

		warehouses := project.EffectiveWarehouses()
		if len(warehouses) == 0 {
			return nil, fmt.Errorf("project %s has no configured warehouse", projectID)
		}
		primaryID := warehouseID(project.PrimaryWarehouse())

		// Shared reasoning LLM (datasource-agnostic).
		llm, err := initLLMProvider(buildCtx, cfg, project, secretProvider, projectID)
		if err != nil {
			return nil, err
		}
		aiClient, err := ai.New(llm, project.LLM.Model)
		if err != nil {
			return nil, fmt.Errorf("create AI client: %w", err)
		}
		aiClient.SetProvenance(projectID, "", project.LLM.Provider)

		// One embedder per project, shared by semantic schema search and the
		// insight search tool. Nil when the project has no embedding provider;
		// both semantic tools then degrade (lookup still works from the cache).
		embedder, _ := initEmbeddingProvider(buildCtx, project, secretProvider, projectID)

		// --- Eager, connection-free schema knowledge across all datasources. ---
		// Per-datasource lookup providers + a table set per datasource for the
		// cross-datasource span searcher to validate hits against.
		lookups := make(map[string]ai.SchemaProvider, len(warehouses))
		labels := make(map[string]string, len(warehouses))
		whTables := make(map[string]map[string]bool, len(warehouses))
		for _, wh := range warehouses {
			whID := warehouseID(wh)
			labels[whID] = wh.Label
			schemas, scErr := schemaCache.Find(buildCtx, projectID, whID, discovery.WarehouseConfigHash(wh))
			if scErr != nil {
				applog.WithError(scErr).WithField("project_id", projectID).WithField("datasource_id", whID).
					Warn("ask-serve: schema cache lookup failed — schema tools disabled for this datasource")
				continue
			}
			// A catalog source has no tables, so an empty schemas map does not
			// mean "not indexed" for it — its index is a list of item refs
			// held separately. Skipping on the table cache alone is what made
			// such a datasource invisible to schema search even after a
			// successful index run.
			catalogRefs, ccErr := discovery.CatalogRefsFor(buildCtx, schemaCache, projectID, whID, discovery.WarehouseConfigHash(wh))
			if ccErr != nil {
				applog.WithError(ccErr).WithField("project_id", projectID).WithField("datasource_id", whID).
					Warn("ask-serve: catalog cache lookup failed — this datasource's catalog is treated as unindexed")
				catalogRefs = nil
			}
			if len(schemas) == 0 && len(catalogRefs) == 0 {
				// Not indexed yet — query_data still works against it.
				continue
			}
			opts := discovery.CacheSchemaProviderOptions{
				ProjectID:   projectID,
				WarehouseID: whID,
				Datasets:    wh.GetDatasets(),
				Schemas:     schemas,
				// Keyed by datasource: this provider searches only its own
				// warehouse, but the authority is shaped the same either way so
				// a shared ref name can never let one datasource vouch for
				// another.
				CatalogRefs: map[string][]string{whID: catalogRefs},
			}
			if sharedRetriever != nil && embedder != nil {
				opts.Retriever = sharedRetriever
				opts.Embedder = embedder
			}
			sp, spErr := discovery.NewCacheSchemaProvider(opts)
			if spErr != nil {
				applog.WithError(spErr).WithField("project_id", projectID).WithField("datasource_id", whID).
					Warn("ask-serve: schema provider build failed — schema tools disabled for this datasource")
				continue
			}
			lookups[whID] = sp
			// The span search validates every hit against this set, so it must
			// name everything the datasource legitimately offers — its tables
			// AND, for a catalog source, its item refs. Omitting the refs
			// silently drops every catalog hit from the cross-datasource view,
			// which is the one the router reads.
			whTables[whID] = discovery.SearchAuthority(schemas, catalogRefs)
		}

		// Cross-datasource span searcher: one unfiltered Qdrant search over the
		// project collection, each hit tagged with its owning datasource (from
		// the Phase-1 warehouse_id payload) and validated against that
		// datasource's cached table set. nil when no retriever/embedder or no
		// datasource is indexed.
		var span func(ctx context.Context, query string, k int) ([]askserve.TaggedHit, error)
		if sharedRetriever != nil && embedder != nil && len(lookups) > 0 {
			span = func(ctx context.Context, query string, k int) ([]askserve.TaggedHit, error) {
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
				vecs, err := embedder.Embed(ctx, []string{q})
				if err != nil {
					return nil, fmt.Errorf("embed search query: %w", err)
				}
				if len(vecs) == 0 || len(vecs[0]) == 0 {
					return nil, fmt.Errorf("embedder returned no vectors for query")
				}
				hits, err := sharedRetriever.Search(ctx, projectID, vecs[0], schema_retrieve.SearchOpts{
					TopK:          k,
					RowCountPrior: 0.05,
				})
				if err != nil {
					return nil, fmt.Errorf("qdrant search: %w", err)
				}
				out := make([]askserve.TaggedHit, 0, len(hits))
				for _, h := range hits {
					wid := h.Blurb.WarehouseID
					if wid == "" {
						wid = models.DefaultWarehouseID
					}
					tset, known := whTables[wid]
					if !known || !tset[h.Blurb.Table] {
						// Stale index for a removed/unindexed datasource, or a
						// table no longer in the cached schema — treat the cache
						// as the authority so search and lookup stay consistent.
						continue
					}
					out = append(out, askserve.TaggedHit{
						DatasourceID:    wid,
						Kind:            h.Blurb.Kind,
						DatasourceLabel: labels[wid],
						Table:           h.Blurb.Table,
						Blurb:           h.Blurb.Blurb,
						RowCount:        h.Blurb.RowCount,
						Score:           h.Score,
					})
				}
				return out, nil
			}
		}

		schemaRouter := askserve.NewSchemaRouter(askserve.SchemaRouterOptions{
			Lookups: lookups,
			Labels:  labels,
			Primary: primaryID,
			Span:    span,
		})

		// --- Datasource descriptors for the prompt (no live connection). ---
		datasources := make([]askserve.DatasourceInfo, 0, len(warehouses))
		for _, wh := range orderPrimaryFirst(warehouses, primaryID) {
			var card *askserve.DatasourceCard
			if wh.Card != nil {
				card = &askserve.DatasourceCard{
					SubjectAreas: wh.Card.SubjectAreas,
					KeyEntities:  wh.Card.KeyEntities,
					KeyMetrics:   wh.Card.KeyMetrics,
				}
			}
			// Capability descriptor, resolved by provider slug from the
			// registry — declared at registration, so this needs no
			// connection and no credentials. An unregistered slug yields the
			// zero descriptor, which resolves to SQL / entity-shaped /
			// anchoring: what was true before the descriptor existed.
			capability := gowarehouse.Capability{}
			if meta, ok := gowarehouse.GetProviderMeta(wh.Provider); ok {
				capability = meta.Capability
				// Resolve the Dialect fallback here so downstream consumers
				// read one already-resolved language.
				capability.QueryLanguage = meta.Language()
			}
			datasources = append(datasources, askserve.DatasourceInfo{
				ID:          warehouseID(wh),
				Label:       wh.Label,
				Description: wh.Description,
				// Dialect hint only — the authoritative dialect + SQL-fix prompt
				// bind from the live connection at query time.
				Dialect:     wh.Provider,
				Datasets:    wh.GetDatasets(),
				FilterField: wh.FilterField,
				FilterValue: wh.FilterValue,
				Card:        card,
				Capability:  capability,
			})
		}

		// --- Lazy per-datasource warehouse connection builder. ---
		// Opens a read-only connection + wires the self-healing executor for one
		// datasource, on first query against it. ValidateReadOnly is the security
		// boundary for the data-query path (governance middleware + tenant filter
		// on top).
		warehouseBuild := func(connCtx context.Context, dsID string) (*askserve.WarehouseConn, error) {
			wh, ok := project.WarehouseByID(dsID)
			if !ok {
				return nil, fmt.Errorf("unknown datasource %q", dsID)
			}
			cctx := connCtx
			if serveCfg.ConnectTimeout > 0 {
				var cancel context.CancelFunc
				cctx, cancel = context.WithTimeout(connCtx, serveCfg.ConnectTimeout)
				defer cancel()
			}
			wp, err := initWarehouseProvider(cctx, project, dsID, secretProvider, projectID)
			if err != nil {
				return nil, err
			}
			if err := wp.ValidateReadOnly(cctx); err != nil {
				_ = wp.Close()
				return nil, fmt.Errorf("datasource %q credentials are not read-only: %w", dsID, err)
			}
			datasets := wh.GetDatasets()
			sqlFixer := ai.NewSQLFixer(ai.SQLFixerOptions{
				Client:       aiClient,
				SQLFixPrompt: wp.SQLFixPrompt(),
				Dataset:      strings.Join(datasets, ", "),
				Filter:       buildFilterClause(wh.FilterField, wh.FilterValue),
			})
			executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
				Warehouse:   wp,
				SQLFixer:    sqlFixer,
				MaxRetries:  5,
				FilterField: wh.FilterField,
				FilterValue: wh.FilterValue,
			})
			return &askserve.WarehouseConn{Executor: executor, Closers: []func() error{wp.Close}}, nil
		}

		// Insights provider (optional, project-level). insightsearch.New returns
		// nil unless the vector store AND an embedder are both present; keep the
		// interface nil in that case so the search_insights tool is not offered.
		var insightsProvider ai.InsightsProvider
		if is := insightsearch.New(projectID, mongoClient.Database(), vectorStore, embedder); is != nil {
			insightsProvider = is
		}

		return askserve.NewProjectRuntime(askserve.ProjectRuntimeOptions{
			AIClient:         aiClient,
			Model:            project.LLM.Model,
			InsightsProvider: insightsProvider,
			Schema:           schemaRouter,
			Datasources:      datasources,
			PrimaryID:        primaryID,
			Build:            warehouseBuild,
		}), nil
	}

	server := askserve.NewServer(serveCfg, builder, mongoClient.Database())

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	applog.WithField("port", serveCfg.Port).Info("Starting ad-hoc data Q&A serve mode")
	return server.Run(sigCtx)
}

// warehouseID returns the warehouse's id, normalising the empty id (a legacy /
// single-warehouse primary) to the reserved "default" so it matches the
// per-warehouse schema-cache and Qdrant keys written by the indexer.
func warehouseID(wh models.WarehouseConfig) string {
	if wh.ID == "" {
		return models.DefaultWarehouseID
	}
	return wh.ID
}

// orderPrimaryFirst returns the warehouses with the primary first, preserving
// the relative order of the rest — so the prompt leads with the default
// datasource.
func orderPrimaryFirst(warehouses []models.WarehouseConfig, primaryID string) []models.WarehouseConfig {
	out := make([]models.WarehouseConfig, 0, len(warehouses))
	for _, wh := range warehouses {
		if warehouseID(wh) == primaryID {
			out = append(out, wh)
		}
	}
	for _, wh := range warehouses {
		if warehouseID(wh) != primaryID {
			out = append(out, wh)
		}
	}
	return out
}
