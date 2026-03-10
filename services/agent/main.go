package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"

	// LLM provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"     // registers "claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"     // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"     // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"  // registers "vertex-ai" (stub)
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"    // registers "bedrock" (stub)

	// Warehouse provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery" // registers "bigquery"

	// Domain pack registrations
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go" // registers "gaming"
)

func main() {
	var (
		projectID       = flag.String("project-id", "", "Project ID to run discovery for (required)")
		maxSteps        = flag.Int("max-steps", 100, "Maximum exploration steps")
		skipCache       = flag.Bool("skip-cache", false, "Force schema rediscovery")
		includeLog      = flag.Bool("include-log", false, "Include full exploration log")
		testMode        = flag.Bool("test", false, "Test mode - limit analyses for faster testing")
		enableDebugLogs = flag.Bool("enable-debug-logs", true, "Enable detailed debug logging to MongoDB")
	)

	flag.Parse()

	if *projectID == "" {
		fmt.Fprintf(os.Stderr, "Error: --project-id is required\n")
		flag.Usage()
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
	defer applog.Sync()

	if *testMode && *maxSteps > 20 {
		*maxSteps = 20
		applog.Info("Test mode enabled - reducing max steps to 20")
	}

	applog.WithFields(applog.Fields{
		"project_id": *projectID,
		"max_steps":  *maxSteps,
		"env":        cfg.Service.Environment,
	}).Info("Starting DecisionBox Agent")

	if err := runDiscovery(cfg, *projectID, *maxSteps, *skipCache, *includeLog, *testMode, *enableDebugLogs); err != nil {
		applog.WithError(err).Fatal("Discovery failed")
	}

	applog.Info("Discovery completed successfully")
}

func runDiscovery(cfg *config.Config, projectID string, maxSteps int, skipCache, includeLog, testMode, enableDebugLogs bool) error {
	ctx := context.Background()

	// Initialize MongoDB
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = cfg.MongoDB.URI
	mongoCfg.Database = cfg.MongoDB.Database
	mongoClient, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	defer mongoClient.Disconnect(ctx)
	applog.Info("Connected to MongoDB")

	db := database.New(mongoClient)

	// Load project config from MongoDB
	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to load project %s: %w", projectID, err)
	}

	applog.WithFields(applog.Fields{
		"project":  project.Name,
		"domain":   project.Domain,
		"category": project.Category,
	}).Info("Project loaded")

	// Load domain pack
	pack, err := domainpack.Get(project.Domain)
	if err != nil {
		return fmt.Errorf("domain pack not found for %q: %w", project.Domain, err)
	}
	dp, ok := domainpack.AsDiscoveryPack(pack)
	if !ok {
		return fmt.Errorf("domain pack %q does not support discovery", project.Domain)
	}

	// Resolve warehouse config: project overrides env vars
	whProvider := cfg.Warehouse.Provider
	whProjectID := cfg.Warehouse.ProjectID
	whDataset := cfg.Warehouse.Dataset
	whLocation := cfg.Warehouse.Location

	if project.Warehouse.Provider != "" {
		whProvider = project.Warehouse.Provider
	}
	if project.Warehouse.ProjectID != "" {
		whProjectID = project.Warehouse.ProjectID
	}
	if project.Warehouse.Dataset != "" {
		whDataset = project.Warehouse.Dataset
	}
	if project.Warehouse.Location != "" {
		whLocation = project.Warehouse.Location
	}

	// Initialize warehouse provider
	warehouseProvider, err := gowarehouse.NewProvider(whProvider, gowarehouse.ProviderConfig{
		"project_id":      whProjectID,
		"dataset":         whDataset,
		"location":        whLocation,
		"timeout_minutes": strconv.Itoa(int(cfg.Warehouse.Timeout.Minutes())),
	})
	if err != nil {
		return fmt.Errorf("failed to create warehouse provider (%s): %w", whProvider, err)
	}
	defer warehouseProvider.Close()
	applog.WithFields(applog.Fields{
		"provider": whProvider,
		"dataset":  whDataset,
	}).Info("Warehouse provider initialized")

	// Resolve LLM config: project overrides env vars
	llmProvider := cfg.LLM.Provider
	llmModel := cfg.LLM.Model
	if project.LLM.Provider != "" {
		llmProvider = project.LLM.Provider
	}
	if project.LLM.Model != "" {
		llmModel = project.LLM.Model
	}

	// Initialize LLM provider
	llm, err := gollm.NewProvider(llmProvider, gollm.ProviderConfig{
		"api_key":          cfg.LLM.APIKey,
		"model":            llmModel,
		"max_retries":      strconv.Itoa(cfg.LLM.MaxRetries),
		"timeout_seconds":  strconv.Itoa(int(cfg.LLM.Timeout.Seconds())),
		"request_delay_ms": strconv.Itoa(cfg.LLM.RequestDelayMs),
	})
	if err != nil {
		return fmt.Errorf("failed to create LLM provider (%s): %w", llmProvider, err)
	}
	applog.WithFields(applog.Fields{
		"provider": llmProvider,
		"model":    llmModel,
	}).Info("LLM provider initialized")

	// Initialize AI client
	aiClient, err := ai.New(cfg, llm)
	if err != nil {
		return fmt.Errorf("failed to create AI client: %w", err)
	}
	if testMode {
		aiClient.SetTestMode(true)
	}

	// Initialize repositories
	contextRepo := database.NewContextRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)
	debugLogRepo := database.NewDebugLogRepository(db, enableDebugLogs)

	if err := contextRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure context indexes")
	}
	if err := discoveryRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure discovery indexes")
	}
	if enableDebugLogs {
		if err := debugLogRepo.EnsureIndexes(ctx); err != nil {
			applog.WithError(err).Warn("Failed to ensure debug log indexes")
		}
	}

	// Create orchestrator
	orchestrator := discovery.NewOrchestrator(discovery.OrchestratorOptions{
		AIClient:        aiClient,
		Warehouse:       warehouseProvider,
		DiscoveryPack:   dp,
		ContextRepo:     contextRepo,
		DiscoveryRepo:   discoveryRepo,
		DebugLogRepo:    debugLogRepo,
		ProjectID:       projectID,
		Domain:          project.Domain,
		Category:        project.Category,
		Profile:         project.Profile,
		FilterField:     project.Warehouse.FilterField,
		FilterValue:     project.Warehouse.FilterValue,
		EnableDebugLogs: enableDebugLogs,
	})

	// Run discovery
	discoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	result, err := orchestrator.RunDiscovery(discoveryCtx, discovery.DiscoveryOptions{
		MaxSteps:              maxSteps,
		SkipSchemaCache:       skipCache,
		IncludeExplorationLog: includeLog,
		TestMode:              testMode,
	})
	if err != nil {
		return fmt.Errorf("discovery run failed: %w", err)
	}

	applog.WithFields(applog.Fields{
		"project_id":      projectID,
		"total_steps":     result.TotalSteps,
		"duration_sec":    result.Duration.Seconds(),
		"schemas":         len(result.Schemas),
		"insights":        len(result.Insights),
		"recommendations": len(result.Recommendations),
	}).Info("Discovery results summary")

	return nil
}
