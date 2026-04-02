// Package agentserver contains the discovery agent startup logic.
// Exported as Run() so that custom builds can import it and register
// additional plugins (warehouse middleware, etc.) via init() before calling Run().
package agentserver

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
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"   // registers "gcp"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"   // registers "aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure" // registers "azure"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"

	// LLM provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"         // registers "claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"         // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"         // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"      // registers "vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"        // registers "bedrock" (stub)
	_ "github.com/decisionbox-io/decisionbox/providers/llm/azure-foundry"  // registers "azure-foundry"

	// Warehouse provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"   // registers "bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/databricks" // registers "databricks"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/postgres"   // registers "postgres"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"   // registers "redshift"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/snowflake"  // registers "snowflake"

	// Domain pack registrations
	_ "github.com/decisionbox-io/decisionbox/domain-packs/ecommerce/go"   // registers "ecommerce"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go"      // registers "gaming"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/social/go"      // registers "social"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/system-test/go" // registers "system-test" (env-gated)
)

// Run starts the DecisionBox discovery agent.
// Plugins (warehouse middleware, etc.) can register via init() in their
// packages — import them with blank imports before calling Run().
func Run() {
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
		testConnection  = flag.String("test-connection", "", "Test provider connection: 'warehouse' or 'llm'")
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

	// Test connection mode runs before logger init — its own logging is minimal
	if *testConnection != "" {
		applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
		if err := runTestConnection(cfg, *projectID, *testConnection); err != nil {
			applog.WithError(err).Error("Test connection failed")
			applog.Sync()
			result, _ := json.Marshal(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			fmt.Println(string(result))
			os.Exit(1)
		}
		applog.Sync()
		return
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

// --- Shared provider initialization helpers ---
// Used by both runDiscovery and runTestConnection to avoid duplication.

func initMongoDB(ctx context.Context, cfg *config.Config) (*gomongo.Client, error) {
	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = cfg.MongoDB.URI
	mongoCfg.Database = cfg.MongoDB.Database
	client, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	applog.Info("Connected to MongoDB")
	return client, nil
}

func initSecretProvider(mongoClient *gomongo.Client) (gosecrets.Provider, error) {
	secretsCfg := gosecrets.LoadConfig()
	if secretsCfg.Provider == "mongodb" || secretsCfg.Provider == "" {
		sp, err := mongoSecrets.NewMongoProvider(
			mongoClient.Collection("secrets"),
			secretsCfg.Namespace,
			secretsCfg.EncryptionKey,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create secret provider: %w", err)
		}
		applog.WithField("provider", "mongodb").Info("Secret provider initialized")
		return sp, nil
	}
	sp, err := gosecrets.NewProvider(secretsCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create secret provider: %w", err)
	}
	applog.WithField("provider", secretsCfg.Provider).Info("Secret provider initialized")
	return sp, nil
}

func initWarehouseProvider(ctx context.Context, project *models.Project, secretProvider gosecrets.Provider, projectID string) (gowarehouse.Provider, error) {
	if project.Warehouse.Provider == "" {
		return nil, fmt.Errorf("no warehouse provider configured")
	}

	datasets := project.Warehouse.GetDatasets()
	if len(datasets) == 0 {
		return nil, fmt.Errorf("no datasets configured in project")
	}

	whCfg := gowarehouse.ProviderConfig{
		"project_id": project.Warehouse.ProjectID,
		"dataset":    datasets[0],
		"location":   project.Warehouse.Location,
	}
	for k, v := range project.Warehouse.Config {
		whCfg[k] = v
	}

	whCreds, err := secretProvider.Get(ctx, projectID, "warehouse-credentials")
	if err == nil && whCreds != "" {
		whCfg["credentials_json"] = whCreds
		applog.Info("Warehouse credentials loaded from secret provider")
	} else if err != nil && err != gosecrets.ErrNotFound {
		applog.WithError(err).Warn("Failed to read warehouse credentials from secret provider")
	}

	provider, err := gowarehouse.NewProvider(project.Warehouse.Provider, whCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create warehouse provider (%s): %w", project.Warehouse.Provider, err)
	}
	provider = gowarehouse.ApplyMiddleware(provider)

	applog.WithFields(applog.Fields{
		"provider": project.Warehouse.Provider,
		"datasets": datasets,
	}).Info("Warehouse provider initialized")

	return provider, nil
}

func initLLMProvider(ctx context.Context, cfg *config.Config, project *models.Project, secretProvider gosecrets.Provider, projectID string) (gollm.Provider, error) {
	if project.LLM.Provider == "" {
		return nil, fmt.Errorf("no LLM provider configured")
	}

	apiKey := ""
	key, err := secretProvider.Get(ctx, projectID, "llm-api-key")
	if err == nil && key != "" {
		apiKey = key
		applog.Info("LLM API key loaded from secret provider")
	} else if err != nil && err != gosecrets.ErrNotFound {
		applog.WithError(err).Warn("Failed to read LLM API key from secret provider")
	}

	llmCfg := gollm.ProviderConfig{
		"api_key":          apiKey,
		"model":            project.LLM.Model,
		"max_retries":      strconv.Itoa(cfg.LLM.MaxRetries),
		"timeout_seconds":  strconv.Itoa(int(cfg.LLM.Timeout.Seconds())),
		"request_delay_ms": strconv.Itoa(cfg.LLM.RequestDelayMs),
	}
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

	provider, err := gollm.NewProvider(project.LLM.Provider, llmCfg)
	if err != nil {
		applog.WithFields(applog.Fields{
			"provider": project.LLM.Provider,
			"error":    err.Error(),
		}).Error("Failed to create LLM provider")
		return nil, fmt.Errorf("failed to create LLM provider (%s): %w", project.LLM.Provider, err)
	}

	applog.WithFields(applog.Fields{
		"provider": project.LLM.Provider,
		"model":    project.LLM.Model,
	}).Info("LLM provider initialized")

	return provider, nil
}

// --- Test connection ---

func runTestConnection(cfg *config.Config, projectID, target string) error {
	if target != "warehouse" && target != "llm" {
		return fmt.Errorf("invalid test target %q: must be 'warehouse' or 'llm'", target)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set project ID in context for warehouse middleware (e.g. governance)
	ctx = gowarehouse.WithProjectID(ctx, projectID)

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)
	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("failed to load project %s: %w", projectID, err)
	}

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	switch target {
	case "warehouse":
		provider, err := initWarehouseProvider(ctx, project, secretProvider, projectID)
		if err != nil {
			return err
		}
		defer provider.Close()

		if err := provider.HealthCheck(ctx); err != nil {
			return fmt.Errorf("warehouse health check failed: %w", err)
		}

		datasets := project.Warehouse.GetDatasets()
		out, err := json.Marshal(map[string]interface{}{
			"success":  true,
			"provider": project.Warehouse.Provider,
			"datasets": datasets,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(out))

	case "llm":
		// For test connection, use max_retries=1 and no request delay
		testCfg := *cfg
		testCfg.LLM.MaxRetries = 1
		testCfg.LLM.RequestDelayMs = 0

		provider, err := initLLMProvider(ctx, &testCfg, project, secretProvider, projectID)
		if err != nil {
			return err
		}

		if err := provider.Validate(ctx); err != nil {
			return err
		}

		out, err := json.Marshal(map[string]interface{}{
			"success":  true,
			"provider": project.LLM.Provider,
			"model":    project.LLM.Model,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(out))

	}

	return nil
}

// --- Discovery ---

func runDiscovery(cfg *config.Config, projectID string, runID string, selectedAreas []string, maxSteps int, skipCache, includeLog, testMode, enableDebugLogs, estimateOnly bool) error {
	ctx := context.Background()

	// Set project ID in context for warehouse middleware (e.g. governance)
	ctx = gowarehouse.WithProjectID(ctx, projectID)

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

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

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	warehouseProvider, err := initWarehouseProvider(ctx, project, secretProvider, projectID)
	if err != nil {
		return err
	}
	defer warehouseProvider.Close()

	llm, err := initLLMProvider(ctx, cfg, project, secretProvider, projectID)
	if err != nil {
		return err
	}

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

	datasets := project.Warehouse.GetDatasets()

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
