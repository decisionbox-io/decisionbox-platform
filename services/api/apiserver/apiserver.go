// Package apiserver contains the API server startup logic.
// Exported as Run() so that custom builds can import it and register
// additional plugins (auth providers, etc.) via init() before calling Run().
package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	goversion "github.com/decisionbox-io/decisionbox/libs/go-common/version"
	qdrantstore "github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore/qdrant"
	"github.com/decisionbox-io/decisionbox/services/api/internal/backfill"
	"github.com/decisionbox-io/decisionbox/services/api/internal/config"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/handler"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/internal/schemaindex"
	"github.com/decisionbox-io/decisionbox/services/api/internal/server"

	// Secret provider registrations
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure"

	// LLM provider registrations (for /api/v1/providers/llm listing)
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/azure-foundry"

	// Warehouse provider registrations (for /api/v1/providers/warehouse listing)
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/databricks"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/mssql"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/postgres"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/snowflake"

	// Embedding provider registrations (for /api/v1/providers/embedding listing)
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/azure-openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/voyage"
)

// Run starts the DecisionBox API server, OR dispatches a CLI subcommand
// if one is provided as the first argument.
//
// Plugins (auth providers, etc.) can register via init() in their
// packages — import them with blank imports before calling Run().
//
// Subcommands handled here so any caller of Run() (community main.go,
// enterprise cmd/api/main.go, future custom builds) inherits CLI tooling
// without per-binary main.go drift:
//
//	decisionbox-api backfill-embeddings [flags]
//
// When a subcommand is invoked, Run() routes to it and returns without
// starting the HTTP server.
func Run() {
	if len(os.Args) > 1 && os.Args[1] == "backfill-embeddings" {
		backfill.RunBackfillEmbeddings(os.Args[2:])
		return
	}

	cfg, err := config.Load()
	if err != nil {
		apilog.WithError(err).Error("Failed to load config")
		os.Exit(1)
	}

	apilog.WithFields(apilog.Fields{
		"port":    cfg.Server.Port,
		"mongodb": cfg.MongoDB.Database,
	}).Info("Starting DecisionBox API")

	ctx := context.Background()

	// MongoDB
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = cfg.MongoDB.URI
	mongoCfg.Database = cfg.MongoDB.Database

	apilog.WithField("database", cfg.MongoDB.Database).Debug("Connecting to MongoDB")
	mongoClient, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		apilog.WithError(err).Error("MongoDB connection failed")
		os.Exit(1)
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()
	apilog.Info("Connected to MongoDB")

	db := database.New(mongoClient)

	// Initialize database (collections + indexes)
	apilog.Info("Initializing database collections and indexes")
	if err := database.InitDatabase(ctx, db); err != nil {
		apilog.WithError(err).Error("Database initialization failed")
		_ = mongoClient.Disconnect(ctx)
		os.Exit(1) //nolint:gocritic // startup failure, explicit disconnect above
	}
	apilog.Info("Database initialized")

	// Telemetry (anonymous usage metrics — disable with TELEMETRY_ENABLED=false)
	installID := telemetry.GetOrCreateInstallID(ctx, mongoClient.Database())
	telemetry.Init(installID, goversion.Version, "api")
	defer telemetry.Shutdown()
	if telemetry.IsEnabled() {
		apilog.Info("Anonymous telemetry enabled (disable: TELEMETRY_ENABLED=false)")
	}

	// Health checker with MongoDB dependency
	healthHandler := health.NewHandler(database.NewMongoHealthChecker(db))

	// Secret provider
	secretsCfg := gosecrets.LoadConfig()
	var secretProvider gosecrets.Provider
	if secretsCfg.Provider == "mongodb" || secretsCfg.Provider == "" {
		sp, err := mongoSecrets.NewMongoProvider(
			mongoClient.Collection("secrets"),
			secretsCfg.Namespace,
			secretsCfg.EncryptionKey,
		)
		if err != nil {
			apilog.WithError(err).Error("Failed to create MongoDB secret provider")
			os.Exit(1)
		}
		secretProvider = sp
		apilog.WithField("namespace", secretsCfg.Namespace).Info("Secret provider: MongoDB encrypted")
	} else {
		sp, err := gosecrets.NewProvider(secretsCfg)
		if err != nil {
			apilog.WithError(err).Error("Failed to create secret provider")
			os.Exit(1)
		}
		secretProvider = sp
		apilog.WithFields(apilog.Fields{
			"provider":  secretsCfg.Provider,
			"namespace": secretsCfg.Namespace,
		}).Info("Secret provider initialized")
	}

	// Auth provider (NoAuth by default, plugins can register via auth.RegisterProvider)
	authProvider := auth.GetProvider()

	// Qdrant (optional — nil if not configured)
	var qdrantProvider vectorstore.Provider
	if cfg.Qdrant.URL != "" {
		qp, closeQdrant, err := initQdrant(ctx, cfg)
		if err != nil {
			apilog.WithError(err).Warn("Qdrant initialization failed — vector search disabled")
		} else {
			qdrantProvider = qp
			defer closeQdrant()
			apilog.WithField("url", cfg.Qdrant.URL).Info("Connected to Qdrant")
		}
	} else {
		apilog.Info("Qdrant not configured — vector search disabled")
	}

	// Make shared infrastructure available to enterprise plugins
	RegisterServices(&Services{
		DB:             db,
		SecretProvider: secretProvider,
		VectorStore:    qdrantProvider,
	})

	// Activate the knowledge sources provider if an enterprise plugin
	// registered a factory. No-op when only the community build is loaded.
	if err := gosources.Configure(ctx, gosources.Dependencies{
		Mongo:          mongoClient.Database(),
		Vectorstore:    qdrantProvider,
		SecretProvider: secretProvider,
	}); err != nil {
		apilog.WithError(err).Warn("Knowledge sources provider configuration failed; /ask and discovery prompts will not include source context")
	}

	// Schema-index worker + /reindex dropper: both need Qdrant. Without
	// it the worker is disabled (discovery will 409 until QDRANT_URL is
	// set), and /reindex returns a nil dropper so the handler falls back
	// on the worker's own pre-run drop.
	var schemaIndexCancel context.CancelFunc
	var schemaDropper *schemaindex.QdrantDropper
	var indexWorker *schemaindex.Worker
	if cfg.Qdrant.URL != "" {
		host, port := parseQdrantHostPort(cfg.Qdrant.URL)
		dropper, err := schemaindex.NewQdrantDropper(host, port, cfg.Qdrant.APIKey, false)
		if err != nil {
			apilog.WithError(err).Error("schemaindex: failed to connect dropper")
			os.Exit(1)
		}
		schemaDropper = dropper
		defer func() { _ = dropper.Close() }()

		runnerCfg := runner.LoadConfig()
		r, err := runner.New(runnerCfg)
		if err != nil {
			apilog.WithError(err).Error("schemaindex: failed to create runner")
			os.Exit(1)
		}
		worker, err := schemaindex.New(schemaindex.WorkerConfig{
			Projects: database.NewProjectRepository(db),
			Progress: database.NewSchemaIndexProgressRepository(db),
			Logs:     database.NewSchemaIndexLogRepository(db),
			Runner:   r,
		})
		if err != nil {
			apilog.WithError(err).Error("schemaindex: failed to create worker")
			os.Exit(1)
		}
		indexWorker = worker
		// One-shot migration for projects that existed before schema
		// indexing shipped. Flips warehouse-configured, unindexed
		// projects to pending_indexing so the worker picks them up.
		// Idempotent — subsequent restarts find zero matches.
		if n, mErr := schemaindex.MigratePreExistingProjects(ctx, database.NewProjectRepository(db)); mErr != nil {
			apilog.WithError(mErr).Warn("schemaindex: migration sweep failed; existing projects can be unblocked via POST /reindex")
		} else if n > 0 {
			apilog.WithField("migrated_projects", n).Info("schemaindex: migration sweep completed")
		}

		// Crash-recovery: projects stuck in "indexing" for more than
		// 2 hours are assumed to have lost their agent subprocess (API
		// crash, host reboot, OOM). Flip them back to pending_indexing
		// so the worker re-queues them, instead of leaving the user
		// with a forever-spinning banner. Threshold generous enough
		// that real 30-min FINPORT rebuilds don't trip it.
		if n, rErr := database.NewProjectRepository(db).ResetStaleIndexingProjects(ctx, 2*time.Hour); rErr != nil {
			apilog.WithError(rErr).Warn("schemaindex: stale-indexing sweep failed")
		} else if n > 0 {
			apilog.WithField("reset_projects", n).Info("schemaindex: reset stale indexing rows to pending_indexing")
		}
		var workerCtx context.Context
		workerCtx, schemaIndexCancel = context.WithCancel(ctx)
		go worker.Start(workerCtx)
	} else {
		apilog.Warn("Qdrant not configured — schema-index worker disabled (discovery will be blocked until Qdrant is set)")
	}

	// HTTP server
	handler := server.New(db, healthHandler, secretProvider, authProvider, droppersAsHandlerInterface(schemaDropper), indexCancellerOrNil(indexWorker), qdrantProvider)
	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      ApplyGlobalMiddlewares(handler),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		apilog.WithField("port", cfg.Server.Port).Info("HTTP server listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			apilog.WithError(err).Error("HTTP server error")
			os.Exit(1)
		}
	}()

	_, isNoAuth := authProvider.(*auth.NoAuthProvider)
	telemetry.TrackServerStarted(map[string]any{
		"deployment_method": deploymentMethod(),
		"auth_enabled":      !isNoAuth,
		"vector_search":     qdrantProvider != nil,
	})

	<-done
	apilog.Info("Shutdown signal received, gracefully stopping")
	telemetry.TrackServerStopped()

	// Stop the schema-index worker first so no new agent subprocesses
	// get spawned while we're draining HTTP. Any in-flight subprocess
	// is left to exit on its own — the worker detaches ctx for the
	// final status transition.
	if schemaIndexCancel != nil {
		schemaIndexCancel()
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		apilog.WithError(err).Error("Shutdown error")
	}
	apilog.Info("Server stopped")
}

func initQdrant(ctx context.Context, cfg *config.Config) (vectorstore.Provider, func(), error) {
	host := cfg.Qdrant.URL
	port := 6334
	if parts := strings.SplitN(host, ":", 2); len(parts) == 2 {
		host = parts[0]
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}

	provider, err := qdrantstore.New(qdrantstore.Config{
		Host:   host,
		Port:   port,
		APIKey: cfg.Qdrant.APIKey,
	})
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to create Qdrant client: %w", err)
	}

	if err := provider.HealthCheck(ctx); err != nil {
		if closeErr := provider.Close(); closeErr != nil {
			apilog.WithError(closeErr).Warn("Failed to close Qdrant client after health check failure")
		}
		return nil, func() {}, fmt.Errorf("qdrant health check failed: %w", err)
	}

	return provider, func() {
		if err := provider.Close(); err != nil {
			apilog.WithError(err).Warn("Failed to close Qdrant client")
		}
	}, nil
}

// deploymentMethod infers the deployment method from environment signals.
// droppersAsHandlerInterface adapts a nullable concrete *QdrantDropper
// to the handler.CollectionDropper interface. When the dropper is nil
// we pass nil (not a non-nil interface wrapping a nil pointer) so the
// handler's `if h.dropper != nil` check works.
func droppersAsHandlerInterface(d *schemaindex.QdrantDropper) handler.CollectionDropper {
	if d == nil {
		return nil
	}
	return d
}

// indexCancellerOrNil avoids the typed-nil trap when the schema-index
// worker didn't start (no Qdrant). Same dance as droppersAsHandlerInterface.
func indexCancellerOrNil(w *schemaindex.Worker) handler.IndexCanceller {
	if w == nil {
		return nil
	}
	return w
}

// parseQdrantHostPort splits the QDRANT_URL env var into (host, port).
// Accepts "host", "host:port", and bare IPv4 forms. Returns port=6334
// when unspecified (Qdrant's gRPC default).
func parseQdrantHostPort(url string) (string, int) {
	host := url
	port := 6334
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		if p, err := strconv.Atoi(host[idx+1:]); err == nil {
			port = p
			host = host[:idx]
		}
	}
	return host, port
}

func deploymentMethod() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "kubernetes"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return "binary"
}
