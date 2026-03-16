package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/domainpack"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"

	// LLM provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"     // registers "claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"     // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"     // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"  // registers "vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"    // registers "bedrock" (stub)

	// Warehouse provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"  // registers "bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"  // registers "redshift"

	// Domain pack registrations
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go" // registers "gaming"
)

func main() {
	var (
		projectID       = flag.String("project-id", "", "Project ID to run discovery for (required)")
		runID           = flag.String("run-id", "", "Discovery run ID for status updates (set by API)")
		areasFlag       = flag.String("areas", "", "Comma-separated analysis areas to run (empty = all)")
		maxSteps        = flag.Int("max-steps", 100, "Maximum exploration steps")
		skipCache       = flag.Bool("skip-cache", false, "Force schema rediscovery")
		includeLog      = flag.Bool("include-log", false, "Include full exploration log")
		testMode        = flag.Bool("test", false, "Test mode - limit analyses for faster testing")
		enableDebugLogs = flag.Bool("enable-debug-logs", true, "Enable detailed debug logging to MongoDB")
		estimateOnly    = flag.Bool("estimate", false, "Estimate cost only (no actual discovery)")
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

	// Parse areas filter
	var selectedAreas []string
	if *areasFlag != "" {
		for _, a := range strings.Split(*areasFlag, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				selectedAreas = append(selectedAreas, a)
			}
		}
	}

	if err := runDiscovery(cfg, *projectID, *runID, selectedAreas, *maxSteps, *skipCache, *includeLog, *testMode, *enableDebugLogs, *estimateOnly); err != nil {
		applog.WithError(err).Fatal("Discovery failed")
	}

	applog.Info("Discovery completed successfully")
}

func runDiscovery(cfg *config.Config, projectID string, runID string, selectedAreas []string, maxSteps int, skipCache, includeLog, testMode, enableDebugLogs, estimateOnly bool) error {
	ctx := context.Background()

	// Initialize MongoDB
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = cfg.MongoDB.URI
	mongoCfg.Database = cfg.MongoDB.Database
	mongoClient, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()
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

	// Initialize secret provider first — needed for both warehouse and LLM credentials
	secretsCfg := gosecrets.LoadConfig()
	var secretProvider gosecrets.Provider
	if secretsCfg.Provider == "mongodb" || secretsCfg.Provider == "" {
		sp, spErr := mongoSecrets.NewMongoProvider(
			mongoClient.Collection("secrets"),
			secretsCfg.Namespace,
			secretsCfg.EncryptionKey,
		)
		if spErr != nil {
			return fmt.Errorf("failed to create secret provider: %w", spErr)
		}
		secretProvider = sp
	} else {
		sp, spErr := gosecrets.NewProvider(secretsCfg)
		if spErr != nil {
			return fmt.Errorf("failed to create secret provider: %w", spErr)
		}
		secretProvider = sp
	}
	applog.WithField("provider", secretsCfg.Provider).Info("Secret provider initialized")

	// Warehouse config comes from project
	datasets := project.Warehouse.GetDatasets()
	if len(datasets) == 0 {
		return fmt.Errorf("no datasets configured in project")
	}

	whCfg := gowarehouse.ProviderConfig{
		"project_id": project.Warehouse.ProjectID,
		"dataset":    datasets[0],
		"location":   project.Warehouse.Location,
	}
	// Merge provider-specific config (workgroup, database, region for Redshift, etc.)
	for k, v := range project.Warehouse.Config {
		whCfg[k] = v
	}

	// Read warehouse credentials from secret provider (for cross-cloud access)
	whCreds, err := secretProvider.Get(ctx, projectID, "warehouse-credentials")
	if err == nil && whCreds != "" {
		whCfg["credentials_json"] = whCreds
		applog.Info("Warehouse credentials loaded from secret provider")
	} else if err != nil && err != gosecrets.ErrNotFound {
		applog.WithError(err).Warn("Failed to read warehouse credentials from secret provider")
	}

	warehouseProvider, err := gowarehouse.NewProvider(project.Warehouse.Provider, whCfg)
	if err != nil {
		return fmt.Errorf("failed to create warehouse provider (%s): %w", project.Warehouse.Provider, err)
	}
	defer warehouseProvider.Close()
	applog.WithFields(applog.Fields{
		"provider": project.Warehouse.Provider,
		"datasets": datasets,
	}).Info("Warehouse provider initialized")

	// Read LLM API key from secret provider (per-project)
	apiKey := ""
	key, err := secretProvider.Get(ctx, projectID, "llm-api-key")
	if err == nil && key != "" {
		apiKey = key
		applog.Info("LLM API key loaded from secret provider")
	} else if err != nil && err != gosecrets.ErrNotFound {
		applog.WithError(err).Warn("Failed to read LLM API key from secret provider")
	}

	// LLM config comes from project + secrets
	llmCfg := gollm.ProviderConfig{
		"api_key":          apiKey,
		"model":            project.LLM.Model,
		"max_retries":      strconv.Itoa(cfg.LLM.MaxRetries),
		"timeout_seconds":  strconv.Itoa(int(cfg.LLM.Timeout.Seconds())),
		"request_delay_ms": strconv.Itoa(cfg.LLM.RequestDelayMs),
	}
	// Merge provider-specific config from project (e.g., project_id, location for Vertex AI)
	mergedKeys := make([]string, 0)
	for k, v := range project.LLM.Config {
		llmCfg[k] = v
		mergedKeys = append(mergedKeys, k)
	}
	if len(mergedKeys) > 0 {
		applog.WithFields(applog.Fields{
			"provider":    project.LLM.Provider,
			"config_keys": mergedKeys,
		}).Debug("Merged provider-specific config from project")
	}
	llm, err := gollm.NewProvider(project.LLM.Provider, llmCfg)
	if err != nil {
		applog.WithFields(applog.Fields{
			"provider": project.LLM.Provider,
			"error":    err.Error(),
		}).Error("Failed to create LLM provider")
		return fmt.Errorf("failed to create LLM provider (%s): %w", project.LLM.Provider, err)
	}
	applog.WithFields(applog.Fields{
		"provider": project.LLM.Provider,
		"model":    project.LLM.Model,
	}).Info("LLM provider initialized")

	// Initialize AI client
	aiClient, err := ai.New(llm, project.LLM.Model)
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

	// Initialize run repository for status updates
	runRepo := database.NewRunRepository(db)

	// Create orchestrator
	orchestrator := discovery.NewOrchestrator(discovery.OrchestratorOptions{
		AIClient:        aiClient,
		Warehouse:       warehouseProvider,
		DiscoveryPack:   dp,
		ContextRepo:     contextRepo,
		DiscoveryRepo:   discoveryRepo,
		FeedbackRepo:    database.NewFeedbackRepository(db),
		DebugLogRepo:    debugLogRepo,
		RunRepo:         runRepo,
		RunID:           runID,
		ProjectID:       projectID,
		Domain:          project.Domain,
		Category:        project.Category,
		Profile:         project.Profile,
		ProjectPrompts:  project.Prompts,
		Datasets:        datasets,
		FilterField:     project.Warehouse.FilterField,
		FilterValue:     project.Warehouse.FilterValue,
		LLMProvider:     project.LLM.Provider,
		LLMModel:        project.LLM.Model,
		EnableDebugLogs: enableDebugLogs,
	})

	// Estimate mode: calculate costs without running discovery
	if estimateOnly {
		applog.Info("Running cost estimation only")
		estimateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		estimate, err := orchestrator.EstimateCost(estimateCtx, discovery.EstimateOptions{
			MaxSteps:      maxSteps,
			SelectedAreas: selectedAreas,
		})
		if err != nil {
			return fmt.Errorf("cost estimation failed: %w", err)
		}

		// Output estimate as JSON to stdout (API captures this)
		estimateJSON, _ := json.MarshalIndent(estimate, "", "  ")
		fmt.Println(string(estimateJSON))
		return nil
	}

	// Run discovery
	discoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	result, err := orchestrator.RunDiscovery(discoveryCtx, discovery.DiscoveryOptions{
		MaxSteps:              maxSteps,
		SkipSchemaCache:       skipCache,
		IncludeExplorationLog: includeLog,
		TestMode:              testMode,
		SelectedAreas:         selectedAreas,
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
