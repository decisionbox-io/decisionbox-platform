package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/internal/config"
	"github.com/decisionbox-io/decisionbox/services/api/internal/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/server"

	// Domain pack registrations
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"

	// LLM provider registrations (for /api/v1/providers/llm listing)
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"

	// Warehouse provider registrations (for /api/v1/providers/warehouse listing)
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// MongoDB
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = cfg.MongoDB.URI
	mongoCfg.Database = cfg.MongoDB.Database
	mongoClient, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connection failed: %v\n", err)
		os.Exit(1)
	}
	defer mongoClient.Disconnect(ctx)

	db := database.New(mongoClient)

	// Initialize database (collections + indexes)
	fmt.Println("Initializing database...")
	if err := database.InitDatabase(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "Database init failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Database initialized")

	// HTTP server
	handler := server.New(db)
	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		fmt.Printf("DecisionBox API starting on :%s\n", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	}()

	<-done
	fmt.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "Shutdown error: %v\n", err)
	}
}
