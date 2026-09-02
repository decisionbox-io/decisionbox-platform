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
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
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
	// Scope warehouse middleware (per-warehouse governance) to this project; each
	// indexWarehouse call stamps the warehouse id below, so a governed datasource
	// masks its own sample data in the generated blurbs.
	ctx := gowarehouse.WithProjectID(context.Background(), projectID)

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
	//
	// Built only when some datasource in this project is actually described
	// by generated blurbs. A catalog source describes itself, so a project
	// made only of those never calls the generator — and resolving a blurb
	// LLM regardless would fail such a project's index run on a model it
	// would never have used.

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

	progressRepo := database.NewSchemaIndexProgressRepository(db)
	schemaCache := database.NewSchemaCacheRepository(db)

	var (
		gen        *blurb.Generator
		blurbLabel string
	)
	if projectNeedsBlurbs(project) {
		var err error
		gen, blurbLabel, err = newBlurbGenerator(ctx, cfg, project, secretProvider, projectID)
		if err != nil {
			return err
		}
	}

	// Per-dataset totals accumulate into the (project-level) progress doc so the
	// dashboard's bar is populated during schema_discovery (the longest leg on
	// ERP-scale warehouses). Callbacks are synchronous — per-table queries take
	// seconds each, Mongo UpdateOne ~1ms, so we're fine. Only the primary
	// warehouse reports progress: the doc is single-warehouse-shaped, so letting
	// each secondary reset the counters would make the bar bounce.
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

	// indexWarehouse discovers + indexes one datasource into the project's shared
	// schema cache + Qdrant collection (every row/point is tagged with the
	// warehouse id; BuildIndex clears only this warehouse's prior points, never
	// the shared collection). The sampling executor's filter MUST match the
	// discovery filter (queryexec verifies it before every sample query). Only
	// the primary reports progress (see above), so secondaries pass a nil
	// reporter + nil callbacks.
	indexWarehouse := func(ctx context.Context, wh models.WarehouseConfig, reportProgress bool) error {
		whID := warehouseIDOrDefault(wh)
		// Scope per-warehouse governance to this datasource so its sample queries
		// (and thus the blurbs) are masked under its own policies.
		ctx = gowarehouse.WithWarehouseID(ctx, whID)
		provider, err := initWarehouseProvider(ctx, project, whID, secretProvider, projectID)
		if err != nil {
			return err
		}
		defer provider.Close()

		executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
			Warehouse:    provider,
			ProviderSlug: wh.Provider,
			FilterField:  wh.FilterField,
			FilterValue: wh.FilterValue,
		})

		var progress discovery.ProgressReporter
		var onListed func(string, int)
		var onDiscovered func(string, string, bool)
		if reportProgress {
			progress = progressRepo
			onListed, onDiscovered = onTablesListed, onTableDiscovered
		}

		schemaDiscovery := discovery.NewSchemaDiscovery(discovery.SchemaDiscoveryOptions{
			Warehouse:         provider,
			Executor:          executor,
			ProjectID:         projectID,
			Datasets:          wh.GetDatasets(),
			Filter:            buildFilterClause(wh.FilterField, wh.FilterValue),
			OnTablesListed:    onListed,
			OnTableDiscovered: onDiscovered,
		})

		indexer := &discovery.SchemaIndexer{
			Discovery:     schemaDiscovery,
			Blurber:       gen,
			Embedder:      embeddingProvider,
			Retriever:     retriever,
			Progress:      progress,
			Cache:         schemaCache,
			WarehouseHash: discovery.WarehouseConfigHash(wh),
			WarehouseID:   whID,
		}
		// A source that describes itself with a catalog is indexed from it.
		// Without this the run begins by listing tables, which such a source
		// refuses, and the whole index fails rather than degrading — so the
		// datasource can never become ready.
		if catalog, ok := gowarehouse.AsCatalogSource(provider); ok {
			indexer.Catalog = catalog
			applog.WithField("warehouse_id", whID).Info("index_schema: source is catalog-shaped; indexing its catalog instead of tables")
		}

		start := time.Now()
		stats, err := indexer.BuildIndex(ctx, discovery.IndexOptions{
			ProjectID:       projectID,
			RunID:           runID,
			BlurbModelLabel: blurbLabel,
			DomainBlurb:     firstNonEmpty(project.Description, ""),
		})
		if err != nil {
			return fmt.Errorf("build index: %w", err)
		}
		applog.WithFields(applog.Fields{
			"warehouse_id":     whID,
			"tables":           stats.Tables,
			"dropped":          stats.Dropped,
			"blurb_tokens_in":  stats.BlurbTokensIn,
			"blurb_tokens_out": stats.BlurbTokensOut,
			"wall_clock_ms":    time.Since(start).Milliseconds(),
		}).Info("Warehouse schema index completed")
		return nil
	}

	// Index every configured datasource so ask-serve's search_tables /
	// lookup_schema and the router's evidence work across all of them, not just
	// the primary. A legacy / single-warehouse project yields exactly one
	// warehouse here, so its behaviour is unchanged. The primary indexes first
	// and its failure fails the run (the API worker's lifecycle depends on that);
	// secondaries are best-effort so one broken datasource can't block the rest.
	for i, wh := range warehousesToIndex(project) {
		if i == 0 {
			if err := indexWarehouse(ctx, wh, true); err != nil {
				return err
			}
			continue
		}
		if err := indexWarehouse(ctx, wh, false); err != nil {
			applog.WithError(err).WithField("warehouse_id", wh.ID).
				Warn("secondary warehouse schema index failed; ask-serve will lack its catalog until it re-indexes")
		}
	}

	return nil
}

// warehousesToIndex returns the datasources to index for a project, primary
// first and de-duplicated by (normalised) id. A legacy / single-warehouse
// project yields exactly its one warehouse, so indexing behaviour is unchanged.
func warehousesToIndex(project *models.Project) []models.WarehouseConfig {
	norm := func(id string) string {
		if id == "" {
			return models.DefaultWarehouseID
		}
		return id
	}
	primary := project.PrimaryWarehouse()
	out := []models.WarehouseConfig{primary}
	seen := map[string]bool{norm(primary.ID): true}
	for _, wh := range project.EffectiveWarehouses() {
		id := norm(wh.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, wh)
	}
	return out
}

// projectNeedsBlurbs reports whether any datasource in the project is
// described by generated blurbs. A catalog source is not — it supplies its own
// descriptions — so a project made only of those needs no blurb LLM, and
// demanding one would fail its index run on a model it would never call.
//
// An unregistered provider slug counts as needing blurbs, which is what every
// provider needed before catalog sources existed.
func projectNeedsBlurbs(project *models.Project) bool {
	for _, wh := range project.EffectiveWarehouses() {
		meta, ok := gowarehouse.GetProviderMeta(wh.Provider)
		if !ok || meta.EffectiveShape() != gowarehouse.ShapeCube {
			return true
		}
	}
	return false
}

// newBlurbGenerator resolves the blurb LLM and builds the generator, returning
// it with the "provider/model" label recorded on every indexed point.
func newBlurbGenerator(ctx context.Context, cfg *config.Config, project *models.Project, secretProvider gosecrets.Provider, projectID string) (*blurb.Generator, string, error) {
	blurbProvider, blurbModel, blurbAPIKey, err := resolveBlurbLLM(ctx, cfg, project, secretProvider, projectID)
	if err != nil {
		return nil, "", fmt.Errorf("blurb llm: %w", err)
	}
	if blurb.IsReasoningClassModel(blurbModel) {
		return nil, "", fmt.Errorf("blurb model %q is reasoning-class and cannot be used — pick gpt-4.1-nano, claude-haiku-4-5, or qwen.qwen3-32b-v1:0", blurbModel)
	}

	// Pick the right config source for the blurb provider — when blurb
	// is configured separately, its own Config holds the auth_method +
	// per-method fields. Falling through to project.LLM.Config here was
	// the legacy behaviour that worked only by accident when blurb and
	// analysis used the same provider (e.g. Gemini + Gemini); a mixed
	// setup like Vertex analysis + Bedrock blurb fed GCP auth_method
	// values into the AWS factory and tripped "unsupported auth method".
	blurbConfig := project.LLM.Config
	if project.BlurbLLM != nil && project.BlurbLLM.Provider != "" {
		blurbConfig = project.BlurbLLM.Config
	}
	llmCfg := buildLLMProviderConfig(cfg, blurbConfig, blurbAPIKey, blurbModel)
	llm, err := gollm.NewProvider(blurbProvider, llmCfg)
	if err != nil {
		return nil, "", fmt.Errorf("build blurb LLM (%s): %w", blurbProvider, err)
	}

	// BLURB_MAX_TOKENS lets operators bump the per-blurb response
	// budget without code changes. Defaults to blurb.DefaultMaxTokens
	// (8192) which fits a thinking-model preamble + a 2-4-sentence
	// answer across the full range of warehouse shapes we've seen
	// in practice.
	gen, err := blurb.New(blurb.Config{
		LLM:          llm,
		Model:        blurbModel,
		ProviderName: blurbProvider,
		Workers:      envIntDefault("BLURB_WORKERS", blurb.DefaultWorkers),
		MaxTokens:    envIntDefault("BLURB_MAX_TOKENS", blurb.DefaultMaxTokens),
		// A user-deployed endpoint serves its own model, so blurbModel is
		// empty — let the generator accept that instead of demanding an ID.
		AllowEmptyModel: strings.TrimSpace(blurbConfig["endpoint_id"]) != "",
	})
	if err != nil {
		return nil, "", fmt.Errorf("blurb generator: %w", err)
	}
	return gen, blurbProvider + "/" + blurbModel, nil
}

// resolveBlurbLLM picks the provider + model + credential for blurb
// generation. Order of resolution for the credential:
//  1. blurb-llm-credentials secret (project-scoped, blurb-specific).
//  2. BLURB_LLM_API_KEY env (operator fallback).
//  3. llm-credentials secret (when blurb provider matches analysis LLM).
//  4. LLM_API_KEY env.
//
// The blurb-specific secret + env always take precedence; the analysis-
// LLM fallback only fires when the blurb provider is empty or matches
// the analysis provider — same intent as before, just with the env
// layer interleaved so operators can configure blurb credentials at the
// pod level.
func resolveBlurbLLM(ctx context.Context, _ *config.Config, project *models.Project, secretProvider gosecrets.Provider, projectID string) (providerName, model, credential string, err error) {
	providerName = project.LLM.Provider
	model = project.LLM.Model
	blurbConfig := project.LLM.Config
	if project.BlurbLLM != nil && project.BlurbLLM.Provider != "" {
		providerName = project.BlurbLLM.Provider
		blurbConfig = project.BlurbLLM.Config
		// Use the blurb override's own model — empty or explicit. Don't
		// inherit the analysis model into an endpoint override: that would
		// send an unrelated model the endpoint doesn't serve. The legacy
		// convenience (a non-endpoint override reusing the analysis model
		// when its own model is blank) is preserved.
		model = project.BlurbLLM.Model
		if model == "" && strings.TrimSpace(blurbConfig["endpoint_id"]) == "" {
			model = project.LLM.Model
		}
	}
	if providerName == "" {
		return "", "", "", fmt.Errorf("no LLM provider configured (project.blurb_llm or project.llm)")
	}
	// An endpoint-backed config may legitimately run with no model (the
	// endpoint serves its own) or with an explicit one (strict serving
	// containers that validate the OpenAI model field) — either is passed
	// through verbatim. Only a non-endpoint config must supply a model.
	if model == "" && strings.TrimSpace(blurbConfig["endpoint_id"]) == "" {
		return "", "", "", fmt.Errorf("no model configured for blurb LLM")
	}

	// Try the blurb-specific credential first; fall back to the analysis
	// LLM credential when blurb borrows the analysis provider's setup.
	credential, _ = resolveCredential(ctx, secretProvider, projectID, "blurb-llm-credentials", "BLURB_LLM_API_KEY")
	if credential == "" {
		credential, _ = resolveCredential(ctx, secretProvider, projectID, "llm-credentials", "LLM_API_KEY")
	}
	return providerName, model, credential, nil
}

// buildLLMProviderConfig mirrors what initLLMProvider does but with a
// caller-supplied credential + model so the shared wiring doesn't have
// to know about the blurb LLM override.
func buildLLMProviderConfig(cfg *config.Config, extraConfig map[string]string, credential, model string) gollm.ProviderConfig {
	out := gollm.ProviderConfig{
		"credentials_json": credential,
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
