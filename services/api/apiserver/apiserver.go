// Package apiserver contains the API server startup logic.
// Exported as Run() so that custom builds can import it and register
// additional plugins (auth providers, etc.) via init() before calling Run().
package apiserver

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/internal/config"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	"github.com/decisionbox-io/decisionbox/services/api/internal/server"

	// Secret provider registrations
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure"

	// Domain pack registrations
	_ "github.com/decisionbox-io/decisionbox/domain-packs/ecommerce/go"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/social/go"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/system-test/go" // env-gated

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

	// HTTP server
	handler := server.New(db, healthHandler, secretProvider, authProvider)
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
