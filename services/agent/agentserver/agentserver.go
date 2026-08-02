// Package agentserver contains the discovery agent startup logic.
// Exported as Run() so that custom builds can import it and register
// additional plugins (warehouse middleware, etc.) via init() before calling Run().
package agentserver

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	pb "github.com/qdrant/go-client/qdrant"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/libs/go-common/notify"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/telemetry"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	qdrantstore "github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore/qdrant"
	goversion "github.com/decisionbox-io/decisionbox/libs/go-common/version"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws"   // registers "aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/azure" // registers "azure"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp"   // registers "gcp"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"

	// LLM provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/llm/azure-foundry" // registers "azure-foundry"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"       // registers "bedrock" (stub)
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"        // registers "claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"        // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"        // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"     // registers "vertex-ai"

	// Warehouse provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"   // registers "bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/databricks" // registers "databricks"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/mssql"      // registers "mssql"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/postgres"   // registers "postgres"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"   // registers "redshift"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/snowflake"  // registers "snowflake"

	// Embedding provider registrations
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/azure-openai" // registers "azure-openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/bedrock"      // registers "bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/ollama"       // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/openai"       // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/vertex-ai"    // registers "vertex-ai"
	_ "github.com/decisionbox-io/decisionbox/providers/embedding/voyage"       // registers "voyage"
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
		minSteps        = flag.Int("min-steps", 0, "Minimum exploration steps before accepting a done signal (0 = no floor). If the LLM says 'done' before this count, it is rejected and exploration continues. Guards against reasoning models that terminate too early.")
		includeLog      = flag.Bool("include-log", false, "Include full exploration log")
		testMode        = flag.Bool("test", false, "Test mode - limit analyses for faster testing")
		enableDebugLogs = flag.Bool("enable-debug-logs", true, "Enable detailed debug logging to MongoDB")
		estimateOnly    = flag.Bool("estimate", false, "Estimate cost only (no actual discovery)")
		testConnection  = flag.String("test-connection", "", "Test provider connection: 'warehouse', 'llm', 'embedding', or 'blurb-llm'")
		mode            = flag.String("mode", "", "Alternate run mode: 'index-schema' to build the project's schema retrieval index and exit; 'validate-doc' to run the LLM-native verifier+refuter against one insight or recommendation for the job named by --job-id and exit; 'validate-sql' to compile-check a batch of SQL statements against the project's warehouse for the job named by --job-id and exit; 'ask-serve' to run the always-up ad-hoc data Q&A service (a long-lived multi-project HTTP server; does not take --project-id). Default: run discovery.")
		jobID           = flag.String("job-id", "", "Job _id when --mode=validate-doc (ValidationJob) or --mode=validate-sql (SQLValidationJob). Ignored in other modes.")
	)

	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// validate-doc is keyed by --job-id (the agent reads project_id /
	// discovery_id / doc_id from the loaded ValidationJob row), so it
	// skips the --project-id requirement that gates discovery /
	// index-schema. Dispatched ahead of the project-id check.
	if *mode == "validate-doc" {
		applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
		err := runValidateDoc(cfg, *jobID)
		if err != nil {
			applog.WithError(err).Error("Validate-doc run failed")
		}
		applog.Sync()
		if err != nil {
			os.Exit(1)
		}
		return
	}

	// validate-sql is keyed by --job-id (the agent reads project_id from
	// the loaded SQLValidationJob row), so — like validate-doc — it skips
	// the --project-id requirement and is dispatched ahead of that check.
	if *mode == "validate-sql" {
		applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
		err := runValidateSQL(cfg, *jobID)
		if err != nil {
			applog.WithError(err).Error("Validate-sql run failed")
		}
		applog.Sync()
		if err != nil {
			os.Exit(1)
		}
		return
	}

	// ask-serve is a long-lived, multi-project HTTP service (it pools project
	// connections on demand), so — like validate-doc / validate-sql — it skips
	// the single --project-id requirement and is dispatched ahead of that check.
	if *mode == "ask-serve" {
		applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
		err := runAskServe(cfg)
		if err != nil {
			applog.WithError(err).Error("Ask-serve exited with error")
		}
		applog.Sync()
		if err != nil {
			os.Exit(1)
		}
		return
	}

	if *projectID == "" {
		fmt.Fprintf(os.Stderr, "Error: --project-id is required\n")
		flag.Usage()
		os.Exit(1)
	}

	// index-schema mode is the Phase B pipeline: drop Qdrant collection →
	// list tables → blurb + embed → upsert → exit. Runs before logger
	// init so the exit-code semantics stay clean for the API worker loop
	// that spawned this subprocess.
	if *mode == "index-schema" {
		applog.Init(cfg.Service.Name, cfg.Service.LogLevel)
		err := runIndexSchema(cfg, *projectID, *runID)
		if err != nil {
			applog.WithError(err).Error("Schema index failed")
		}
		applog.Sync()
		if err != nil {
			os.Exit(1)
		}
		return
	}
	if *mode != "" {
		fmt.Fprintf(os.Stderr, "Error: unknown --mode %q (expected: 'index-schema', 'validate-doc', 'validate-sql', 'ask-serve', or empty)\n", *mode)
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
	if *minSteps > *maxSteps {
		*minSteps = *maxSteps
	}
	if *minSteps < 0 {
		*minSteps = 0
	}

	applog.WithFields(applog.Fields{
		"project_id": *projectID,
		"max_steps":  *maxSteps,
		"min_steps":  *minSteps,
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

	if err := runDiscovery(cfg, *projectID, *runID, selectedAreas, *maxSteps, *minSteps, *includeLog, *testMode, *enableDebugLogs, *estimateOnly); err != nil {
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

// initWarehouseProvider builds a live warehouse provider for one of the
// project's warehouses. warehouseID selects which one; an empty id
// resolves to the project's primary warehouse (the behaviour for legacy
// single-warehouse projects). Credentials are read from the
// per-warehouse secret key (warehouse.CredentialsKey) — the primary /
// "default" warehouse keeps the legacy "warehouse-credentials" key, so
// existing projects need no secret migration.
// warehouseDomainOr returns the warehouse's own domain-pack binding
// (multi-warehouse: each datasource carries the slug of the pack generated for
// it), falling back to the project-level domain for legacy / single-warehouse
// projects that never had a per-warehouse binding.
func warehouseDomainOr(wh models.WarehouseConfig, projectDomain string) string {
	if wh.Domain != "" {
		return wh.Domain
	}
	return projectDomain
}

// warehouseIDOrDefault returns the warehouse's id, mapping the empty id to the
// reserved default (matching the model accessors + schema_retrieve). Callers use
// it wherever an id feeds a warehouse-scoped lookup, so an id-less default entry
// filters on a concrete "default" rather than being read as "all warehouses".
func warehouseIDOrDefault(wh models.WarehouseConfig) string {
	if wh.ID == "" {
		return models.DefaultWarehouseID
	}
	return wh.ID
}

func initWarehouseProvider(ctx context.Context, project *models.Project, warehouseID string, secretProvider gosecrets.Provider, projectID string) (gowarehouse.Provider, error) {
	wh, ok := project.WarehouseByID(warehouseID)
	if !ok || wh.Provider == "" {
		return nil, fmt.Errorf("no warehouse provider configured")
	}

	datasets := wh.Datasets
	if len(datasets) == 0 {
		return nil, fmt.Errorf("no datasets configured in project")
	}

	whCfg := gowarehouse.ProviderConfig{
		"project_id": wh.ProjectID,
		"dataset":    datasets[0],
		"location":   wh.Location,
	}
	for k, v := range wh.Config {
		whCfg[k] = v
	}

	whCreds, err := secretProvider.Get(ctx, projectID, gowarehouse.CredentialsKey(wh.ID))
	if err == nil && whCreds != "" {
		whCfg["credentials_json"] = whCreds
		applog.Info("Warehouse credentials loaded from secret provider")
	} else if err != nil && !errors.Is(err, gosecrets.ErrNotFound) {
		applog.WithError(err).Warn("Failed to read warehouse credentials from secret provider")
	}

	provider, err := gowarehouse.NewProvider(wh.Provider, whCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create warehouse provider (%s): %w", wh.Provider, err)
	}
	provider = gowarehouse.ApplyMiddleware(provider)

	applog.WithFields(applog.Fields{
		"provider":     wh.Provider,
		"warehouse_id": wh.ID,
		"datasets":     datasets,
	}).Info("Warehouse provider initialized")

	return provider, nil
}

func initQdrant(ctx context.Context, cfg *config.Config) (vectorstore.Provider, func(), error) {
	if cfg.Qdrant.URL == "" {
		applog.Info("Qdrant not configured — vector search disabled")
		return nil, func() {}, nil
	}

	// Parse host:port from URL
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
			applog.WithError(closeErr).Warn("Failed to close Qdrant client after health check failure")
		}
		return nil, func() {}, fmt.Errorf("qdrant health check failed: %w", err)
	}

	applog.WithField("url", cfg.Qdrant.URL).Info("Connected to Qdrant")
	return provider, func() {
		if err := provider.Close(); err != nil {
			applog.WithError(err).Warn("Failed to close Qdrant client")
		}
	}, nil
}

// resolveCredential applies the platform's credential-resolution order
// to a single slot: dashboard secret wins, env var is fallback, ambient
// (handled downstream by the provider factory for cloud auth methods) is
// last. The returned source string is one of "dashboard", "env", or
// "none" and is logged so operators can see where the credential came
// from.
//
// The order itself lives in the shared gosecrets.ResolveCredential helper
// so the agent, the API, and the enterprise plugins can't drift apart;
// this wrapper only adds the agent's warn-on-unexpected-error logging.
func resolveCredential(ctx context.Context, secretProvider gosecrets.Provider, projectID, secretKey, envVar string) (value, source string) {
	value, source, err := gosecrets.ResolveCredential(ctx, secretProvider, projectID, secretKey, envVar)
	if err != nil {
		// gcp/aws/azure backends wrap their errors with %w; ResolveCredential
		// already treats a (wrapped) ErrNotFound as "not set" and only
		// returns genuinely unexpected read failures here.
		applog.WithError(err).WithField("secret_key", secretKey).Warn("Failed to read credential from secret provider")
	}
	return value, source
}

func initEmbeddingProvider(ctx context.Context, project *models.Project, secretProvider gosecrets.Provider, projectID string) (goembedding.Provider, error) {
	if project.Embedding.Provider == "" {
		applog.Info("Embedding provider not configured — Phase 9 will skip embedding")
		return nil, nil
	}

	credential, source := resolveCredential(ctx, secretProvider, projectID, "embedding-credentials", "EMBEDDING_API_KEY")

	embCfg := goembedding.ProviderConfig{
		"credentials_json": credential,
		"model":            project.Embedding.Model,
	}
	// Copy provider-specific non-credential config (auth_method,
	// region, project_id, location, role_arn, external_id, …) from
	// the project document so Bedrock / Vertex factories see what the
	// dashboard saved. project.Embedding.Config is the map the
	// dashboard writes when the user picks an auth method or enters
	// method-specific fields.
	for k, v := range project.Embedding.Config {
		embCfg[k] = v
	}

	provider, err := goembedding.NewProvider(project.Embedding.Provider, embCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding provider (%s): %w", project.Embedding.Provider, err)
	}

	applog.WithFields(applog.Fields{
		"provider":       project.Embedding.Provider,
		"model":          project.Embedding.Model,
		"dims":           provider.Dimensions(),
		"credential_src": source,
	}).Info("Embedding provider initialized")

	return provider, nil
}

func initLLMProvider(ctx context.Context, cfg *config.Config, project *models.Project, secretProvider gosecrets.Provider, projectID string) (gollm.Provider, error) {
	if project.LLM.Provider == "" {
		return nil, fmt.Errorf("no LLM provider configured")
	}

	credential, source := resolveCredential(ctx, secretProvider, projectID, "llm-credentials", "LLM_API_KEY")

	llmCfg := gollm.ProviderConfig{
		"credentials_json": credential,
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
		"provider":       project.LLM.Provider,
		"model":          project.LLM.Model,
		"credential_src": source,
	}).Info("LLM provider initialized")

	return provider, nil
}

// --- Test connection ---

func runTestConnection(cfg *config.Config, projectID, target string) error {
	switch target {
	case "warehouse", "llm", "embedding", "blurb-llm":
	default:
		return fmt.Errorf("invalid test target %q: must be 'warehouse', 'llm', 'embedding', or 'blurb-llm'", target)
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
		// Resolve the primary through the accessor (not the raw id) so a stale /
		// removed primary_warehouse_id falls back to the first configured
		// warehouse instead of failing the test outright.
		provider, err := initWarehouseProvider(ctx, project, warehouseIDOrDefault(project.PrimaryWarehouse()), secretProvider, projectID)
		if err != nil {
			return err
		}
		defer provider.Close()

		if err := provider.HealthCheck(ctx); err != nil {
			return fmt.Errorf("warehouse health check failed: %w", err)
		}

		// Report the datasource we actually health-checked (the primary), not
		// the legacy singular Warehouse field — the two diverge on a real
		// multi-warehouse project where Warehouse is empty/stale.
		primaryWH := project.PrimaryWarehouse()
		out, err := json.Marshal(map[string]interface{}{
			"success":  true,
			"provider": primaryWH.Provider,
			"datasets": primaryWH.GetDatasets(),
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

	case "embedding":
		provider, err := initEmbeddingProvider(ctx, project, secretProvider, projectID)
		if err != nil {
			return err
		}
		if provider == nil {
			return fmt.Errorf("embedding provider not configured")
		}
		// Issue a minimal embed call so credentials + model availability
		// are both checked end-to-end.
		if _, err := provider.Embed(ctx, []string{"ping"}); err != nil {
			return fmt.Errorf("embedding test failed: %w", err)
		}
		out, err := json.Marshal(map[string]interface{}{
			"success":    true,
			"provider":   project.Embedding.Provider,
			"model":      project.Embedding.Model,
			"dimensions": provider.Dimensions(),
		})
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(out))

	case "blurb-llm":
		// Resolve the blurb LLM via the same path discovery uses; falls
		// back to the analysis LLM credential when blurb borrows that
		// provider's setup.
		providerName, model, credential, err := resolveBlurbLLM(ctx, cfg, project, secretProvider, projectID)
		if err != nil {
			return err
		}
		testCfg := *cfg
		testCfg.LLM.MaxRetries = 1
		testCfg.LLM.RequestDelayMs = 0
		// Mirror the strict guard in index_schema.go::runIndexSchema —
		// a non-nil BlurbLLM with an empty Provider field means the
		// document only carries override config (e.g. legacy/partial
		// documents) but resolveBlurbLLM has fallen back to the analysis
		// provider. Without checking Provider too, this branch would
		// feed the (likely empty) BlurbLLM.Config into the factory
		// while the resolved provider+model come from project.LLM,
		// diverging from what the agent does at indexing time.
		extra := map[string]string{}
		if project.BlurbLLM != nil && project.BlurbLLM.Provider != "" {
			for k, v := range project.BlurbLLM.Config {
				extra[k] = v
			}
		} else {
			for k, v := range project.LLM.Config {
				extra[k] = v
			}
		}
		llmCfg := buildLLMProviderConfig(&testCfg, extra, credential, model)
		provider, err := gollm.NewProvider(providerName, llmCfg)
		if err != nil {
			return fmt.Errorf("failed to create blurb LLM provider (%s): %w", providerName, err)
		}
		if err := provider.Validate(ctx); err != nil {
			return err
		}
		out, err := json.Marshal(map[string]interface{}{
			"success":  true,
			"provider": providerName,
			"model":    model,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(out))
	}

	return nil
}

// --- Discovery ---

func runDiscovery(cfg *config.Config, projectID string, runID string, selectedAreas []string, maxSteps, minSteps int, includeLog, testMode, enableDebugLogs, estimateOnly bool) error {
	ctx := context.Background()

	// Set project ID in context for warehouse middleware (e.g. governance)
	ctx = gowarehouse.WithProjectID(ctx, projectID)

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)

	// Initialize telemetry (reuses the same install ID as the API)
	installID := telemetry.GetOrCreateInstallID(ctx, mongoClient.Database())
	telemetry.Init(installID, goversion.Version, "agent")
	defer telemetry.Shutdown()

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

	// Validate project has seeded prompts
	if project.Prompts == nil {
		return fmt.Errorf("project %q has no seeded prompts — re-create the project or seed prompts via the API", projectID)
	}

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	// Resolve the primary through the accessor (not the raw id) so a stale /
	// removed primary_warehouse_id falls back to the first configured warehouse
	// instead of failing discovery outright.
	primaryWHID := warehouseIDOrDefault(project.PrimaryWarehouse())
	// Scope per-warehouse governance to the datasource discovery runs against, so
	// its exploration/sample queries are masked under that warehouse's policies
	// (project id was stamped above; ask-serve / index-schema / validation stamp
	// the same pair).
	ctx = gowarehouse.WithWarehouseID(ctx, primaryWHID)
	warehouseProvider, err := initWarehouseProvider(ctx, project, primaryWHID, secretProvider, projectID)
	if err != nil {
		return err
	}
	defer warehouseProvider.Close()

	llm, err := initLLMProvider(ctx, cfg, project, secretProvider, projectID)
	if err != nil {
		return err
	}

	// Initialize Qdrant (optional — nil if not configured)
	qdrantProvider, closeQdrant, err := initQdrant(ctx, cfg)
	if err != nil {
		applog.WithError(err).Warn("Qdrant initialization failed — vector search disabled")
		qdrantProvider = nil
	}
	defer closeQdrant()

	// Activate the knowledge sources provider if an enterprise plugin
	// registered a factory. No-op when only the community build is loaded.
	if err := gosources.Configure(ctx, gosources.Dependencies{
		Mongo:          mongoClient.Database(),
		Vectorstore:    qdrantProvider,
		SecretProvider: secretProvider,
	}); err != nil {
		applog.WithError(err).Warn("Knowledge sources provider configuration failed; sources disabled for this run")
	}

	// Initialize embedding provider (optional — nil if not configured)
	embeddingProvider, err := initEmbeddingProvider(ctx, project, secretProvider, projectID)
	if err != nil {
		applog.WithError(err).Warn("Embedding provider initialization failed — Phase 9 will skip embedding")
		embeddingProvider = nil
	}

	// Initialize AI client
	aiClient, err := ai.New(llm, project.LLM.Model)
	if err != nil {
		return fmt.Errorf("failed to create AI client: %w", err)
	}
	aiClient.SetProvenance(projectID, runID, project.LLM.Provider)
	if testMode {
		aiClient.SetTestMode(true)
	}

	// Initialize repositories
	contextRepo := database.NewContextRepository(db)
	discoveryRepo := database.NewDiscoveryRepository(db)
	debugLogRepo := database.NewDebugLogRepository(db, enableDebugLogs)
	// Per-step / per-area / per-result rows live in their own collections
	// (see database/discovery_log_repo.go). The previous embedded arrays
	// hit the 16MB BSON limit on long runs.
	discoveryLogRepo := database.NewDiscoveryLogRepository(db)

	if err := contextRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure context indexes")
	}
	if err := discoveryRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure discovery indexes")
	}
	if err := discoveryLogRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure discovery log split-collection indexes")
	}
	if enableDebugLogs {
		if err := debugLogRepo.EnsureIndexes(ctx); err != nil {
			applog.WithError(err).Warn("Failed to ensure debug log indexes")
		}
	}

	// Initialize run repositories for status updates. RunStepRepo
	// persists the per-step log rows that used to live in the embedded
	// `steps` array on the run document.
	runRepo := database.NewRunRepository(db)
	runStepRepo := database.NewRunStepRepository(db)
	if err := runStepRepo.EnsureIndexes(ctx); err != nil {
		applog.WithError(err).Warn("Failed to ensure run step indexes")
	}

	// Discovery runs against the primary warehouse — take its datasets and
	// filter from that warehouse config, NOT the legacy singular Warehouse
	// field (they diverge once a project has real per-warehouse entries and
	// the primary isn't the legacy one). For a legacy/single-warehouse
	// project PrimaryWarehouse() synthesises from Warehouse, so this is
	// identical to the old behaviour.
	primaryWH := project.PrimaryWarehouse()
	datasets := primaryWH.GetDatasets()

	// Schema-retrieval wiring (required — discovery is gated on
	// schema_index_status == "ready", so the cache + Qdrant collection
	// are guaranteed to exist by the time we get here).
	schemaCache := database.NewSchemaCacheRepository(db)
	warehouseHash := discovery.WarehouseConfigHash(primaryWH)

	schemaRetriever, err := newSchemaRetriever(cfg)
	if err != nil {
		return fmt.Errorf("failed to connect schema retriever (Qdrant): %w", err)
	}
	defer func() { _ = schemaRetriever.Close() }()

	// Run-scoped step index: separate Qdrant gRPC connection so the
	// shared schema-retrieval client isn't tied up by the analysis
	// path. Required for the run — analysis ranking depends on it,
	// so a missing embedding provider here is a hard error rather
	// than a silent regression to keyword-only selection.
	runStepClient, runStepCloser, err := newRunStepIndexClient(cfg)
	if err != nil {
		return fmt.Errorf("connect run step index (Qdrant): %w", err)
	}
	defer runStepCloser()
	if embeddingProvider == nil {
		return fmt.Errorf("analysis ranking requires an embedding provider — configure one in project settings")
	}
	if runID == "" {
		return fmt.Errorf("run id is required for run-scoped step index — caller must pass --run-id")
	}
	runStepIndex, err := discovery.NewRunStepIndex(runStepClient, embeddingProvider, runID)
	if err != nil {
		return fmt.Errorf("init run step index: %w", err)
	}
	applog.WithFields(applog.Fields{
		"run_id":         runID,
		"collection":     discovery.RunStepIndexCollectionName(runID),
		"embedder_model": embeddingProvider.ModelName(),
		"embedder_dims":  embeddingProvider.Dimensions(),
	}).Info("Run-step index ready")

	// Boot-time orphan sweep: drop per-run collections from previous
	// runs that crashed before reaching the deferred Drop. The
	// orchestrator's Drop covers clean exits; this covers crashes.
	sweepCtx, sweepCancel := context.WithTimeout(ctx, 30*time.Second)
	keep := loadActiveRunIDs(sweepCtx, db)
	keep[runID] = struct{}{}
	applog.WithFields(applog.Fields{
		"keep_count": len(keep),
		"run_id":     runID,
	}).Debug("Run-step-index orphan sweep starting")
	if dropped, err := discovery.SweepOrphanRunStepIndexes(sweepCtx, runStepClient, keep); err != nil {
		applog.WithError(err).Warn("Run-step-index orphan sweep failed; orphans will retry next boot")
	} else if dropped > 0 {
		applog.WithField("dropped", dropped).Info("Run-step-index orphan sweep dropped stale collections")
	}
	sweepCancel()

	// Create orchestrator
	orchestrator := discovery.NewOrchestrator(discovery.OrchestratorOptions{
		AIClient:          aiClient,
		Warehouse:         warehouseProvider,
		ContextRepo:       contextRepo,
		DiscoveryRepo:     discoveryRepo,
		DiscoveryLogRepo:  discoveryLogRepo,
		FeedbackRepo:      database.NewFeedbackRepository(db),
		DebugLogRepo:      debugLogRepo,
		RunRepo:           runRepo,
		RunStepRepo:       runStepRepo,
		RunID:             runID,
		ProjectID:         projectID,
		Domain:            warehouseDomainOr(primaryWH, project.Domain),
		Category:          project.Category,
		Language:          project.Language,
		Profile:           project.Profile,
		ProjectPrompts:    project.Prompts,
		Datasets:          datasets,
		FilterField:       primaryWH.FilterField,
		FilterValue:       primaryWH.FilterValue,
		LLMProvider:       project.LLM.Provider,
		LLMModel:          project.LLM.Model,
		WarehouseProvider: primaryWH.Provider,
		EnableDebugLogs:   enableDebugLogs,
		VectorStore:       qdrantProvider,
		EmbeddingProvider: embeddingProvider,
		EmbedIndexStore:   discovery.NewMongoEmbedIndexStore(db),
		SchemaRetriever:   schemaRetriever,
		SchemaCache:       schemaCache,
		WarehouseHash:     warehouseHash,
		WarehouseID:       warehouseIDOrDefault(primaryWH),
		RunStepIndex:      runStepIndex,
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

	// Run discovery. The outer cap is intentionally generous —
	// enterprise warehouses can return long-running SQL (multi-hour
	// scans, cold-start serverless warmup, etc.) and the per-step
	// budgets (warehouse provider QueryTimeout, LLM HTTP timeouts +
	// chatWithRetry, per-table schema timeout) are what keep stuck
	// operations responsive. The outer cap only exists as a
	// runaway-loop safety net; `DISCOVERY_MAX_DURATION=0` disables
	// it entirely for installs that prefer to rely on per-step
	// budgets alone.
	discoveryCtx, cancel := discoveryRunContext(ctx)
	defer cancel()

	result, err := orchestrator.RunDiscovery(discoveryCtx, discovery.DiscoveryOptions{
		MaxSteps:              maxSteps,
		MinSteps:              minSteps,
		IncludeExplorationLog: includeLog,
		TestMode:              testMode,
		SelectedAreas:         selectedAreas,
		ValidationEnabled:     project.EffectiveValidationEnabled(),
	})
	if err != nil {
		notify.NotifyAll(ctx, notify.Event{
			Type:        notify.EventDiscoveryFailed,
			ProjectID:   projectID,
			ProjectName: project.Name,
			RunID:       runID,
			Error:       err.Error(),
			Timestamp:   time.Now(),
		})
		telemetry.TrackDiscoveryFailed(
			project.Warehouse.Provider,
			project.LLM.Provider,
			project.Domain,
			classifyError(err),
		)
		return fmt.Errorf("discovery run failed: %w", err)
	}

	// Notify registered channels (async, non-fatal)
	notify.NotifyAll(ctx, notify.Event{
		Type:               notify.EventDiscoveryCompleted,
		ProjectID:          projectID,
		ProjectName:        project.Name,
		Domain:             project.Domain,
		Category:           project.Category,
		RunID:              runID,
		DiscoveryID:        result.ID,
		Duration:           result.Duration,
		InsightsTotal:      len(result.Insights),
		InsightsCritical:   countBySeverity(result.Insights, "critical"),
		InsightsHigh:       countBySeverity(result.Insights, "high"),
		InsightsMedium:     countBySeverity(result.Insights, "medium"),
		Recommendations:    len(result.Recommendations),
		QueriesExecuted:    result.TotalSteps,
		TopInsights:        topInsightBriefs(result.Insights, 5),
		TopRecommendations: topRecommendationBriefs(result.Recommendations, 3),
		Timestamp:          time.Now(),
	})

	telemetry.TrackDiscoveryCompleted(
		project.Warehouse.Provider,
		project.LLM.Provider,
		project.Domain,
		project.Category,
		result.Duration.Seconds(),
		len(result.Insights),
		len(result.Recommendations),
		result.TotalSteps,
	)

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

func countBySeverity(insights []models.Insight, severity string) int {
	count := 0
	for _, i := range insights {
		if i.Severity == severity {
			count++
		}
	}
	return count
}

func topInsightBriefs(insights []models.Insight, limit int) []notify.InsightBrief {
	// Sort by severity: critical > high > medium > low
	sevOrder := map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}
	sorted := make([]models.Insight, len(insights))
	copy(sorted, insights)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sevOrder[sorted[j].Severity] > sevOrder[sorted[i].Severity] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	briefs := make([]notify.InsightBrief, len(sorted))
	for i, ins := range sorted {
		briefs[i] = notify.InsightBrief{
			ID:            ins.ID,
			Name:          ins.Name,
			Severity:      ins.Severity,
			AnalysisArea:  ins.AnalysisArea,
			AffectedCount: ins.AffectedCount,
		}
	}
	return briefs
}

func topRecommendationBriefs(recs []models.Recommendation, limit int) []notify.RecommendationBrief {
	if len(recs) > limit {
		recs = recs[:limit]
	}
	briefs := make([]notify.RecommendationBrief, len(recs))
	for i, rec := range recs {
		briefs[i] = notify.RecommendationBrief{
			ID:                   rec.ID,
			Title:                rec.Title,
			Metric:               rec.ExpectedImpact.Metric,
			EstimatedImprovement: rec.ExpectedImpact.EstimatedImprovement,
		}
	}
	return briefs
}

// newRunStepIndexClient opens a gRPC connection to Qdrant for the
// run-scoped step index. The returned closer must be invoked at end
// of run — defer it.
func newRunStepIndexClient(cfg *config.Config) (*pb.Client, func(), error) {
	if cfg.Qdrant.URL == "" {
		return nil, func() {}, fmt.Errorf("qdrant is required (set QDRANT_URL)")
	}
	host := cfg.Qdrant.URL
	port := 6334
	if parts := strings.SplitN(host, ":", 2); len(parts) == 2 {
		host = parts[0]
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}
	client, err := pb.NewClient(&pb.Config{
		Host:   host,
		Port:   port,
		APIKey: cfg.Qdrant.APIKey,
	})
	if err != nil {
		return nil, func() {}, fmt.Errorf("qdrant client: %w", err)
	}
	return client, func() {
		if err := client.Close(); err != nil {
			applog.WithError(err).Warn("Failed to close run-step-index Qdrant client")
		}
	}, nil
}

// loadActiveRunIDs returns the set of discovery_runs ids that are
// considered "live" — recent and not in a terminal status. The
// boot-time orphan sweep keeps these and drops every other per-run
// Qdrant collection.
//
// Best-effort: a Mongo error returns an empty set, which is the safe
// default (the sweep will leave possibly-live collections in place
// rather than destroying ongoing work).
func loadActiveRunIDs(ctx context.Context, db *database.DB) map[string]struct{} {
	out := make(map[string]struct{})
	if db == nil {
		return out
	}
	repo := database.NewRunRepository(db)
	// Lookback tracks DISCOVERY_MAX_DURATION (with a small buffer)
	// so a long-running discovery is never misclassified as orphaned
	// and stripped of its run-step collection mid-flight.
	lookback := discoverySweepLookback()
	runs, err := repo.ListActiveRecent(ctx, lookback)
	if err != nil {
		applog.WithError(err).Warn("Could not list active runs for orphan sweep; sweep will be conservative")
		return out
	}
	for _, r := range runs {
		if r.ID != "" {
			out[r.ID] = struct{}{}
		}
	}
	return out
}

// discoveryMaxDurationEnv is the env var that controls the outer
// cap on a single discovery run. Value parses with
// time.ParseDuration ("24h", "168h"). Set to "0" to disable the
// cap entirely and rely on per-step budgets only. Empty / unset
// falls back to defaultDiscoveryMaxDuration.
const discoveryMaxDurationEnv = "DISCOVERY_MAX_DURATION"

// defaultDiscoveryMaxDuration is generous on purpose. Enterprise
// warehouses can return long-running SQL; the cap is a
// runaway-loop safety net, not a per-operation budget.
const defaultDiscoveryMaxDuration = 24 * time.Hour

// discoveryRunContext derives the discovery run ctx from parent
// ctx, applying DISCOVERY_MAX_DURATION if set. A value of "0"
// disables the cap (returns the parent ctx with a no-op cancel).
// An invalid value falls back to the default and logs a warning
// — better to keep the run going under a sane cap than to refuse
// to start because of a typo.
func discoveryRunContext(parent context.Context) (context.Context, context.CancelFunc) {
	raw := strings.TrimSpace(os.Getenv(discoveryMaxDurationEnv))
	d := defaultDiscoveryMaxDuration

	if raw != "" {
		parsed, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			applog.WithFields(applog.Fields{
				"env":      discoveryMaxDurationEnv,
				"value":    raw,
				"fallback": defaultDiscoveryMaxDuration.String(),
			}).Warn("invalid DISCOVERY_MAX_DURATION; falling back to default")
		case parsed == 0:
			applog.Info("DISCOVERY_MAX_DURATION=0 — outer cap disabled, per-step budgets only")
			return parent, func() {}
		case parsed < 0:
			applog.WithFields(applog.Fields{
				"env":      discoveryMaxDurationEnv,
				"value":    raw,
				"fallback": defaultDiscoveryMaxDuration.String(),
			}).Warn("negative DISCOVERY_MAX_DURATION; falling back to default")
		default:
			d = parsed
		}
	}

	applog.WithField("max_duration", d.String()).Info("Discovery outer cap configured")
	return context.WithTimeout(parent, d)
}

// sweepLookbackFloor is the minimum lookback used for the boot-time
// run-step orphan sweep. Keeps behaviour identical to the old
// hard-coded value when DISCOVERY_MAX_DURATION is unset / short.
const sweepLookbackFloor = 24 * time.Hour

// sweepLookbackBuffer is added to the parsed DISCOVERY_MAX_DURATION
// when computing the orphan-sweep lookback. The cap is when the
// agent gives up; the run might still be wrapping up its persistence
// tail for another ~10 minutes after that, and clock skew between
// the API and the agent pod can shift started_at by a few seconds.
// One hour absorbs both comfortably.
const sweepLookbackBuffer = time.Hour

// sweepLookbackDisabled is the lookback used when
// DISCOVERY_MAX_DURATION=0 (cap disabled). The operator has opted
// into runs of arbitrary length, so the sweep must err very
// conservatively or it will delete the run-step collection of a
// long-running discovery. 30 days is large enough that no realistic
// discovery is older — the status filter (`pending`/`running`) is
// the strong signal for "still live"; the lookback is just an
// extra safety net for orphaned `running` runs whose process
// crashed without updating Mongo.
const sweepLookbackDisabled = 30 * 24 * time.Hour

// discoverySweepLookback computes the lookback window for the
// boot-time orphan sweep so it always covers the configured outer
// cap. Without this, setting DISCOVERY_MAX_DURATION above 24h
// (e.g. 168h for week-long enterprise runs) shadows the
// hard-coded lookback — a still-running discovery older than 24h
// gets dropped from keepRunIDs and its Qdrant run-step collection
// is deleted mid-flight, breaking the in-progress run.
func discoverySweepLookback() time.Duration {
	raw := strings.TrimSpace(os.Getenv(discoveryMaxDurationEnv))
	d := defaultDiscoveryMaxDuration
	if raw != "" {
		parsed, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			// Fall through with defaultDiscoveryMaxDuration — matches
			// the behavior of discoveryRunContext, which also falls
			// back to the default on invalid values.
		case parsed == 0:
			return sweepLookbackDisabled
		case parsed < 0:
			// Same fall-through as the invalid case.
		default:
			d = parsed
		}
	}
	// Apply the buffer to the EFFECTIVE cap — including the default,
	// not just an explicit env-var setting. Without the buffer here,
	// an unset-env run started at minute 24:00:01 would be dropped
	// by the sweep at the next agent boot (clock skew + the agent's
	// own persistence-tail wraps it just past the floor).
	withBuffer := d + sweepLookbackBuffer
	if withBuffer < sweepLookbackFloor {
		return sweepLookbackFloor
	}
	return withBuffer
}

// classifyError returns a coarse error class for telemetry.
// Never sends the actual error message — only a category.
func classifyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "warehouse") || strings.Contains(msg, "query"):
		return "warehouse_error"
	case strings.Contains(msg, "LLM") || strings.Contains(msg, "llm") || strings.Contains(msg, "rate limit"):
		return "llm_error"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "MongoDB") || strings.Contains(msg, "mongo"):
		return "database_error"
	default:
		return "unknown"
	}
}
