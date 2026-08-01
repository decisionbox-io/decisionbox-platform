package agentserver

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/askserve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/insightsearch"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// runAskServe starts the ad-hoc data Q&A serve mode: an always-up HTTP service
// that answers natural-language data questions by running a bounded,
// tool-using reasoning loop against each project's warehouse (read-only) and
// schema index. It reuses the agent's existing per-project provider wiring
// (warehouse, LLM, schema retriever) behind a warm connection pool; each
// question runs as a long-lived background job whose progress is persisted to
// Mongo and read back by cursor — the same shape a discovery run uses.
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

	builder := func(buildCtx context.Context, projectID string) (*askserve.ProjectRuntime, error) {
		project, err := projectRepo.GetByID(buildCtx, projectID)
		if err != nil {
			return nil, fmt.Errorf("load project %s: %w", projectID, err)
		}

		warehouse, err := initWarehouseProvider(buildCtx, project, project.PrimaryWarehouseID, secretProvider, projectID)
		if err != nil {
			return nil, err
		}
		// Read-only enforcement: refuse to serve a project whose warehouse
		// credentials can write. This is the security boundary for the
		// data-query path (governance middleware + the tenant filter layer
		// on top).
		if err := warehouse.ValidateReadOnly(buildCtx); err != nil {
			_ = warehouse.Close()
			return nil, fmt.Errorf("warehouse credentials are not read-only: %w", err)
		}
		closers := []func() error{warehouse.Close}

		llm, err := initLLMProvider(buildCtx, cfg, project, secretProvider, projectID)
		if err != nil {
			_ = warehouse.Close()
			return nil, err
		}
		aiClient, err := ai.New(llm, project.LLM.Model)
		if err != nil {
			_ = warehouse.Close()
			return nil, fmt.Errorf("create AI client: %w", err)
		}
		aiClient.SetProvenance(projectID, "", project.LLM.Provider)

		datasets := project.Warehouse.GetDatasets()
		filterClause := buildFilterClause(project.Warehouse.FilterField, project.Warehouse.FilterValue)
		sqlFixer := ai.NewSQLFixer(ai.SQLFixerOptions{
			Client:       aiClient,
			SQLFixPrompt: warehouse.SQLFixPrompt(),
			Dataset:      strings.Join(datasets, ", "),
			Filter:       filterClause,
		})
		executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
			Warehouse:   warehouse,
			SQLFixer:    sqlFixer,
			MaxRetries:  5,
			FilterField: project.Warehouse.FilterField,
			FilterValue: project.Warehouse.FilterValue,
		})

		// One embedder per project, shared by semantic schema search and the
		// insight search tool. Nil when the project has no embedding provider
		// configured — both semantic tools then degrade (schema lookup still
		// works from the cache; search_insights is simply not offered).
		embedder, _ := initEmbeddingProvider(buildCtx, project, secretProvider, projectID)

		// Schema provider (optional). Lookup serves from the cached schemas
		// map; semantic search additionally needs the retriever + embedder.
		var schemaProvider ai.SchemaProvider
		warehouseHash := discovery.WarehouseConfigHash(project.Warehouse)
		schemas, scErr := schemaCache.Find(buildCtx, projectID, warehouseHash)
		switch {
		case scErr != nil:
			applog.WithError(scErr).WithField("project_id", projectID).Warn("ask-serve: schema cache lookup failed — schema tools disabled")
		case len(schemas) == 0:
			applog.WithField("project_id", projectID).Warn("ask-serve: no cached schema for project — run --mode index-schema to enable schema tools")
		default:
			opts := discovery.CacheSchemaProviderOptions{
				ProjectID: projectID,
				Datasets:  datasets,
				Schemas:   schemas,
			}
			// Wire semantic search only when both the retriever and an embedder
			// are present (NewCacheSchemaProvider requires the pair).
			if sharedRetriever != nil && embedder != nil {
				opts.Retriever = sharedRetriever
				opts.Embedder = embedder
			}
			sp, spErr := discovery.NewCacheSchemaProvider(opts)
			if spErr != nil {
				applog.WithError(spErr).WithField("project_id", projectID).Warn("ask-serve: schema provider build failed — schema tools disabled")
			} else {
				schemaProvider = sp
			}
		}

		// Insights provider (optional). insightsearch.New returns nil unless the
		// vector store AND an embedder are both present; keep the interface nil
		// in that case (a typed-nil would defeat the loop's nil check) so the
		// search_insights tool is simply not offered.
		var insightsProvider ai.InsightsProvider
		if is := insightsearch.New(projectID, mongoClient.Database(), vectorStore, embedder); is != nil {
			insightsProvider = is
		}

		return &askserve.ProjectRuntime{
			Executor:         executor,
			SchemaProvider:   schemaProvider,
			InsightsProvider: insightsProvider,
			AIClient:         aiClient,
			Model:            project.LLM.Model,
			Datasets:         datasets,
			Dialect:          warehouse.SQLDialect(),
			FilterField:      project.Warehouse.FilterField,
			FilterValue:      project.Warehouse.FilterValue,
			Closers:          closers,
		}, nil
	}

	serveCfg := askserve.LoadConfig()
	server := askserve.NewServer(serveCfg, builder, mongoClient.Database())

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	applog.WithField("port", serveCfg.Port).Info("Starting ad-hoc data Q&A serve mode")
	return server.Run(sigCtx)
}
