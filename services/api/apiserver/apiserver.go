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
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	qdrantstore "github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore/qdrant"
	"github.com/decisionbox-io/decisionbox/services/api/internal/config"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
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

// Run starts the DecisionBox API server.
// Plugins (auth providers, etc.) can register via init() in their
// packages — import them with blank imports before calling Run().
func Run() {
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

	// HTTP server
	handler := server.New(db, healthHandler, secretProvider, authProvider, qdrantProvider)
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

	<-done
	apilog.Info("Shutdown signal received, gracefully stopping")

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
