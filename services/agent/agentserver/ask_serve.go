package agentserver

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	gomutation "github.com/decisionbox-io/decisionbox/libs/go-common/askmutation"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
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
	"go.mongodb.org/mongo-driver/mongo"
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

	// Activate the knowledge-sources provider if an enterprise plugin registered a
	// factory. No-op when only the community build is loaded (the search_knowledge
	// tool is then not offered). Historically Configure was called only on the
	// discovery path, so ask-serve saw only the no-op — this wires it here too.
	if err := gosources.Configure(ctx, gosources.Dependencies{
		Mongo:          mongoClient.Database(),
		Vectorstore:    vectorStore,
		SecretProvider: secretProvider,
	}); err != nil {
		applog.WithError(err).Warn("ask-serve: knowledge sources provider configuration failed — search_knowledge disabled")
	}
	knowledgeConfigured := gosources.IsConfigured()

	// Mutation tools (write actions such as save_note) registered by an enterprise
	// plugin. Empty on a community build, so the loop offers no write tool. Built
	// once with the shared Mongo handle bound; the registry set is process-global.
	mutationTools := buildMutationTools(mongoClient.Database())

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
			if len(schemas) == 0 {
				// Not indexed yet — query_data still works against it.
				continue
			}
			opts := discovery.CacheSchemaProviderOptions{
				ProjectID:   projectID,
				WarehouseID: whID,
				Datasets:    wh.GetDatasets(),
				Schemas:     schemas,
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
			tset := make(map[string]bool, len(schemas))
			for tbl := range schemas {
				tset[tbl] = true
			}
			whTables[whID] = tset
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

		// Knowledge provider (uploaded documents + operator notes) — only when an
		// enterprise sources provider is actually configured, so a community build
		// (no-op provider) does not offer a search_knowledge tool that always
		// returns nothing.
		var knowledgeProvider askserve.KnowledgeProvider
		if knowledgeConfigured {
			knowledgeProvider = &sourcesKnowledgeAdapter{projectID: projectID}
		}

		return askserve.NewProjectRuntime(askserve.ProjectRuntimeOptions{
			AIClient:          aiClient,
			Model:             project.LLM.Model,
			InsightsProvider:  insightsProvider,
			KnowledgeProvider: knowledgeProvider,
			MutationTools:     mutationTools,
			Schema:            schemaRouter,
			Datasources:       datasources,
			PrimaryID:         primaryID,
			BusinessSummary:   project.BusinessSummary,
			Build:             warehouseBuild,
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

// buildMutationTools adapts the registered go-common askmutation tools to the
// askserve.MutationTool shape, binding the shared Mongo handle into each Run
// closure so the loop stays storage-agnostic. Returns nil when no plugin
// registered any (community build) — the loop then offers no write tool.
func buildMutationTools(db *mongo.Database) []askserve.MutationTool {
	registered := gomutation.Tools()
	if len(registered) == 0 {
		return nil
	}
	out := make([]askserve.MutationTool, 0, len(registered))
	for _, t := range registered {
		t := t // capture per-iteration
		out = append(out, askserve.MutationTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Run: func(ctx context.Context, in askserve.MutationInput) (askserve.MutationOutput, error) {
				res, err := t.Run(ctx, gomutation.Request{
					ProjectID: in.ProjectID,
					SessionID: in.SessionID,
					TurnID:    in.TurnID,
					CallerSub: in.CallerSub,
					Args:      in.Args,
					Mongo:     db,
				})
				if err != nil {
					return askserve.MutationOutput{}, err
				}
				return askserve.MutationOutput{ProposalID: res.ProposalID, Output: res.Output}, nil
			},
		})
	}
	return out
}

// sourcesKnowledgeAdapter adapts the go-common knowledge-sources provider to the
// askserve.KnowledgeProvider the ask loop consumes, scoped to one project. The
// underlying provider is registered by the enterprise sources plugin and
// activated by gosources.Configure; a community-only build never activates it,
// so this adapter is not constructed and the search_knowledge tool is absent.
// It budgets documents and operator notes INDEPENDENTLY via two retrievals so
// one never starves the other (a note-heavy project can't crowd out the semantic
// document matches, and vice versa) — one tool still covers both.
type sourcesKnowledgeAdapter struct {
	projectID string
}

func (a *sourcesKnowledgeAdapter) RetrieveKnowledge(ctx context.Context, query string, k int) ([]askserve.KnowledgeChunk, error) {
	if k <= 0 {
		k = ai.DefaultSearchTopK
	}
	if k > ai.MaxSearchTopK {
		k = ai.MaxSearchTopK
	}
	toChunk := func(c gosources.Chunk) askserve.KnowledgeChunk {
		return askserve.KnowledgeChunk{
			SourceID:   c.SourceID,
			Position:   c.Position,
			SourceName: c.SourceName,
			SourceType: c.SourceType,
			Text:       c.Text,
			Score:      c.Score,
		}
	}

	// Documents: DocumentsOnly makes Limit an exact upper bound on purely semantic
	// document matches — so the top-k documents are ALWAYS returned regardless of
	// how many notes the project has.
	docChunks, err := gosources.GetProvider().RetrieveContext(ctx, a.projectID, query, gosources.RetrieveOpts{Limit: k, DocumentsOnly: true})
	if err != nil {
		return nil, err
	}
	out := make([]askserve.KnowledgeChunk, 0, len(docChunks)+maxKnowledgeNotes)

	// Notes: a second note-inclusive pass, from which we keep only the note chunks
	// (SourceType "note"), a small always-include budget. Best-effort — a failure
	// here still returns the documents.
	noteChunks, nErr := gosources.GetProvider().RetrieveContext(ctx, a.projectID, query, gosources.RetrieveOpts{Limit: maxKnowledgeNotes})
	if nErr == nil {
		n := 0
		for _, c := range noteChunks {
			if n >= maxKnowledgeNotes {
				break
			}
			if strings.EqualFold(c.SourceType, "note") {
				out = append(out, toChunk(c))
				n++
			}
		}
	}
	for _, c := range docChunks {
		out = append(out, toChunk(c))
	}
	return out, nil
}

// maxKnowledgeNotes bounds how many pinned operator notes search_knowledge
// returns alongside the top-k document matches, so always-include note guidance
// never crowds out (or is crowded out by) the semantic document results.
const maxKnowledgeNotes = 5

