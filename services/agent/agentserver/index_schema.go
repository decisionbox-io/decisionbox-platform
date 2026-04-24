package agentserver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// runIndexSchema executes the schema-retrieval indexer for a single
// project and exits. Invoked when the agent is launched with
// `--mode index-schema`; the API's indexing worker owns the lifecycle
// status transitions around this call.
//
// Exit contract: 0 on success, non-zero on any error. The worker reads
// the exit code; stdout and stderr carry structured logs only.
func runIndexSchema(cfg *config.Config, projectID, runID string) error {
	ctx := context.Background()

	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()

	db := database.New(mongoClient)

	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}

	applog.WithFields(applog.Fields{
		"project":  project.Name,
		"domain":   project.Domain,
		"category": project.Category,
		"run_id":   runID,
	}).Info("Starting schema-index run")

	secretProvider, err := initSecretProvider(mongoClient)
	if err != nil {
		return err
	}

	// Warehouse + executor: reused from discovery so the sampling path is
	// identical (SampleQueryBuilder, SQL fixer), keeping blurb inputs the
	// same shape exploration would see.
	warehouseProvider, err := initWarehouseProvider(ctx, project, secretProvider, projectID)
	if err != nil {
		return err
	}
	defer warehouseProvider.Close()

	// Embedding provider is mandatory for schema indexing (plan §3.7).
	// If it's missing, fail fast with a message the API will relay to
	// the dashboard's error banner. The provider itself is pre-flight-
	// validated with a single "ping" embedding so credential / quota /
	// dimension-mismatch errors surface up-front instead of 20 minutes
	// into the indexing pipeline.
	embeddingProvider, err := initEmbeddingProvider(ctx, project, secretProvider, projectID)
	if err != nil {
		return fmt.Errorf("embedding provider: %w", err)
	}
	if embeddingProvider == nil {
		return fmt.Errorf("schema indexing requires an embedding provider — configure one in project settings")
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, 30*time.Second)
	if _, err := embeddingProvider.Embed(pingCtx, []string{"decisionbox schema-index pre-flight"}); err != nil {
		pingCancel()
		return fmt.Errorf("embedding provider pre-flight failed: %w", err)
	}
	pingCancel()

	// Blurb LLM — independent of the analysis LLM. Falls back to
	// project.LLM if blurb_llm is not set (e.g. a legacy project), on
	// the assumption the user already has credentials for that provider.
	blurbProvider, blurbModel, blurbAPIKey, err := resolveBlurbLLM(ctx, cfg, project, secretProvider, projectID)
	if err != nil {
		return fmt.Errorf("blurb llm: %w", err)
	}
	if blurb.IsReasoningClassModel(blurbModel) {
		return fmt.Errorf("blurb model %q is reasoning-class and cannot be used — pick gpt-4.1-nano, claude-haiku-4-5, or qwen.qwen3-32b-v1:0", blurbModel)
	}

	llmCfg := buildLLMProviderConfig(cfg, project.LLM.Config, blurbAPIKey, blurbModel)
	llm, err := gollm.NewProvider(blurbProvider, llmCfg)
	if err != nil {
		return fmt.Errorf("build blurb LLM (%s): %w", blurbProvider, err)
	}

	// Retriever: connect to Qdrant. Unlike discovery this is mandatory,
	// not optional — without Qdrant there is nothing to index into.
	if cfg.Qdrant.URL == "" {
		return fmt.Errorf("schema indexing requires Qdrant — set QDRANT_URL")
	}
	retriever, err := newSchemaRetriever(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = retriever.Close() }()
	if err := retriever.HealthCheck(ctx); err != nil {
		return fmt.Errorf("qdrant health check: %w", err)
	}

	// Discovery + executor. Executor runs sample-data queries during
	// schema discovery; we provide no SQLFixer so a broken sample-query
	// surfaces as a skipped table rather than triggering an LLM round-
	// trip (blurb indexing is already LLM-heavy; cascading fixer calls
	// would multiply cost).
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   warehouseProvider,
		FilterField: project.Warehouse.FilterField,
		FilterValue: project.Warehouse.FilterValue,
	})
	progressRepo := database.NewSchemaIndexProgressRepository(db)

	// Per-dataset totals accumulate into the progress doc so the
	// dashboard's progress bar is populated during schema_discovery
	// (the longest leg on ERP-scale warehouses, previously invisible
	// on the UI). Callbacks are synchronous — the per-table queries
	// take seconds each, Mongo UpdateOne takes ~1ms, so we're fine.
	onTablesListed := func(_ string, total int) {
		if err := progressRepo.SetCounters(ctx, projectID, total, 0); err != nil {
			applog.WithError(err).Warn("SetCounters on listed tables failed")
		}
	}
	onTableDiscovered := func(_, _ string, _ bool) {
		if err := progressRepo.IncrementDone(ctx, projectID, 1); err != nil {
			applog.WithError(err).Debug("IncrementDone during discovery failed")
		}
	}

	schemaDiscovery := discovery.NewSchemaDiscovery(discovery.SchemaDiscoveryOptions{
		Warehouse:         warehouseProvider,
		Executor:          executor,
		ProjectID:         projectID,
		Datasets:          project.Warehouse.GetDatasets(),
		Filter:            buildFilterClause(project.Warehouse.FilterField, project.Warehouse.FilterValue),
		OnTablesListed:    onTablesListed,
		OnTableDiscovered: onTableDiscovered,
	})

	workers := envIntDefault("BLURB_WORKERS", blurb.DefaultWorkers)
	gen, err := blurb.New(blurb.Config{
		LLM:          llm,
		Model:        blurbModel,
		ProviderName: blurbProvider,
		Workers:      workers,
	})
	if err != nil {
		return fmt.Errorf("blurb generator: %w", err)
	}

	indexer := &discovery.SchemaIndexer{
		Discovery: schemaDiscovery,
		Blurber:   gen,
		Embedder:  embeddingProvider,
		Retriever: retriever,
		Progress:  progressRepo,
	}

	start := time.Now()
	stats, err := indexer.BuildIndex(ctx, discovery.IndexOptions{
		ProjectID:       projectID,
		RunID:           runID,
		BlurbModelLabel: blurbProvider + "/" + blurbModel,
		DomainBlurb:     firstNonEmpty(project.Description, ""),
	})
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	applog.WithFields(applog.Fields{
		"tables":            stats.Tables,
		"dropped":           stats.Dropped,
		"blurb_tokens_in":   stats.BlurbTokensIn,
		"blurb_tokens_out":  stats.BlurbTokensOut,
		"wall_clock_ms":     time.Since(start).Milliseconds(),
	}).Info("Schema index completed")

	return nil
}

// resolveBlurbLLM picks the provider + model + API key for blurb
// generation. Order of resolution:
//  1. project.BlurbLLM (new field, Phase A1) if set.
//  2. project.LLM (fallback — reuses the analysis provider's key).
//
// The API key is pulled from secrets: "blurb-llm-api-key" first, then
// falls back to "llm-api-key" if the blurb provider matches the
// analysis provider (most common case).
func resolveBlurbLLM(ctx context.Context, _ *config.Config, project *models.Project, secretProvider gosecrets.Provider, projectID string) (providerName, model, apiKey string, err error) {
	providerName = project.LLM.Provider
	model = project.LLM.Model
	if project.BlurbLLM != nil && project.BlurbLLM.Provider != "" {
		providerName = project.BlurbLLM.Provider
		if project.BlurbLLM.Model != "" {
			model = project.BlurbLLM.Model
		}
	}
	if providerName == "" {
		return "", "", "", fmt.Errorf("no LLM provider configured (project.blurb_llm or project.llm)")
	}
	if model == "" {
		return "", "", "", fmt.Errorf("no model configured for blurb LLM")
	}

	// Try the blurb-specific secret first; fall back to the shared key.
	if key, lookupErr := secretProvider.Get(ctx, projectID, "blurb-llm-api-key"); lookupErr == nil && key != "" {
		apiKey = key
	} else if key, lookupErr := secretProvider.Get(ctx, projectID, "llm-api-key"); lookupErr == nil && key != "" {
		apiKey = key
	}
	return providerName, model, apiKey, nil
}

// buildLLMProviderConfig mirrors what initLLMProvider does but with a
// caller-supplied api_key + model so the shared wiring doesn't have to
// know about the blurb LLM override.
func buildLLMProviderConfig(cfg *config.Config, extraConfig map[string]string, apiKey, model string) gollm.ProviderConfig {
	out := gollm.ProviderConfig{
		"api_key":          apiKey,
		"model":            model,
		"max_retries":      strconv.Itoa(cfg.LLM.MaxRetries),
		"timeout_seconds":  strconv.Itoa(int(cfg.LLM.Timeout.Seconds())),
		"request_delay_ms": strconv.Itoa(cfg.LLM.RequestDelayMs),
	}
	for k, v := range extraConfig {
		out[k] = v
	}
	return out
}

// newSchemaRetriever opens a Qdrant connection for schema-retrieval.
// Keeps wiring local — the generic vectorstore provider used by
// insights/recommendations is shaped for a different collection model.
func newSchemaRetriever(cfg *config.Config) (*schema_retrieve.Retriever, error) {
	host := cfg.Qdrant.URL
	port := 6334
	if parts := strings.SplitN(host, ":", 2); len(parts) == 2 {
		host = parts[0]
		if p, err := strconv.Atoi(parts[1]); err == nil {
			port = p
		}
	}
	r, err := schema_retrieve.New(schema_retrieve.Config{
		Host:   host,
		Port:   port,
		APIKey: cfg.Qdrant.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("connect schema qdrant: %w", err)
	}
	return r, nil
}

func buildFilterClause(field, value string) string {
	field = strings.TrimSpace(field)
	value = strings.TrimSpace(value)
	if field == "" || value == "" {
		return ""
	}
	// Escape single quotes so filter values containing them don't break
	// the sample query builder's string interpolation.
	value = strings.ReplaceAll(value, "'", "''")
	return fmt.Sprintf("WHERE %s = '%s'", field, value)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func envIntDefault(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
