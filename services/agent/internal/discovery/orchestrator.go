package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	_ "github.com/decisionbox-io/decisionbox/libs/go-common/sources" // registers knowledge-sources context provider via init()
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai/schema_retrieve"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discipline"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation"
)

// discoveryLogPersister is the slice of *database.DiscoveryLogRepository
// the orchestrator actually calls during a run. Defined as an interface so
// the persistence wiring (Save{Exploration,Analysis,Validation,Recommendation}*)
// can be exercised by a unit-level mock instead of requiring MongoDB.
type discoveryLogPersister interface {
	SaveExplorationSteps(ctx context.Context, projectID, discoveryID, runID string, steps []models.ExplorationStep) error
	SaveAnalysisSteps(ctx context.Context, projectID, discoveryID, runID string, steps []models.AnalysisStep) error
	SaveValidationResults(ctx context.Context, projectID, discoveryID, runID string, results []models.ValidationResult) error
	SaveRecommendationLog(ctx context.Context, projectID, discoveryID, runID string, step *models.RecommendationStep) error
}

// AnalysisArea defines an analysis area resolved from project prompts.
type AnalysisArea struct {
	ID          string
	Name        string
	Description string
	Keywords    []string
	IsBase      bool
	Priority    int
}

// ResolvedPrompts holds all prompts resolved from project configuration.
type ResolvedPrompts struct {
	Exploration     string
	Recommendations string
	BaseContext     string
	AnalysisAreas   map[string]string
}

// Orchestrator coordinates the entire discovery process.
type Orchestrator struct {
	aiClient      *ai.Client
	warehouse     gowarehouse.Provider

	contextRepo      *database.ContextRepository
	discoveryRepo    *database.DiscoveryRepository
	// discoveryLogRepo is held as an interface so unit tests can inject
	// a mock without having to spin up MongoDB. The concrete writer is
	// *database.DiscoveryLogRepository, wired in production by
	// agentserver.go.
	discoveryLogRepo discoveryLogPersister
	feedbackRepo     *database.FeedbackRepository
	debugLogRepo     *database.DebugLogRepository

	explorationEngine    *ai.ExplorationEngine
	userCountValidator   *validation.UserCountValidator
	insightValidator     *validation.InsightValidator

	// runStepIndex is the per-run Qdrant collection of exploration
	// steps. Populated inline as exploration runs; queried by the
	// analysis phase to rank steps per area; dropped on run
	// completion. Must be non-nil for production runs — the
	// orchestrator surfaces a clear error when it isn't.
	runStepIndex RunStepIndex
	runID        string

	debugLogger    *debug.Logger
	statusReporter *StatusReporter

	projectID      string
	domain         string
	category       string
	language       string
	profile        map[string]interface{}
	projectPrompts *models.ProjectPrompts
	datasets       []string
	filterField    string
	filterValue    string
	llmProvider    string
	llmModel       string

	vectorStore       vectorstore.Provider
	embeddingProvider goembedding.Provider
	embedIndexStore   EmbedIndexStore

	// embedder + schemaRetriever are the Qdrant-backed schema retrieval
	// layer (top-K relevant tables in the prompt instead of dumping the
	// full catalog). Required — discovery is gated on schema_index_status
	// == "ready" upstream, so the indexer has populated both Mongo and
	// Qdrant by the time we run.
	embedder        Embedder
	schemaRetriever *schema_retrieve.Retriever

	// schemaCache + warehouseHash drive the bulk schemas-map lookup that
	// used to live as a per-table live re-discovery against the warehouse.
	// The cache is populated by the schema indexer (see
	// agentserver/index_schema.go) and indexed by WarehouseConfigHash so
	// any warehouse-config change self-invalidates the cache.
	schemaCache    SchemaCache
	warehouseHash  string
}

// OrchestratorOptions configures the orchestrator.
type OrchestratorOptions struct {
	AIClient      *ai.Client
	Warehouse     gowarehouse.Provider

	ContextRepo      *database.ContextRepository
	DiscoveryRepo    *database.DiscoveryRepository
	// DiscoveryLogRepo persists the per-step / per-area / per-result rows
	// (exploration_steps, analysis_steps, validation_results,
	// recommendation_log) that used to be embedded arrays inside the
	// discoveries document. Optional — when nil the orchestrator skips
	// the per-step persistence and only the parent DiscoveryResult lands
	// in Mongo. Production builds always wire this; the nil branch exists
	// for unit tests that don't bring up MongoDB.
	DiscoveryLogRepo *database.DiscoveryLogRepository
	FeedbackRepo     *database.FeedbackRepository
	DebugLogRepo     *database.DebugLogRepository

	RunRepo      *database.RunRepository
	// RunStepRepo persists the per-step rows that used to live as an
	// embedded `steps` array on the discovery_runs document. Required —
	// without it the status reporter has nowhere to write the live step
	// stream and the dashboard's progress feed goes dark.
	RunStepRepo  *database.RunStepRepository
	RunID        string

	ProjectID         string
	Domain            string
	Category          string
	// Language is the human-readable output language for narrative
	// fields (insight names/descriptions, recommendation titles, etc).
	// Substituted into prompts as {{LANGUAGE}}. Empty resolves to
	// "English" so legacy projects keep their pre-feature behavior.
	Language          string
	Profile           map[string]interface{}
	ProjectPrompts    *models.ProjectPrompts
	Datasets          []string
	FilterField       string
	FilterValue       string
	LLMProvider       string
	LLMModel          string
	WarehouseProvider string // provider id used to label warehouse-query debug rows
	EnableDebugLogs   bool

	// Optional — nil if Qdrant/embedding not configured
	VectorStore       vectorstore.Provider
	EmbeddingProvider goembedding.Provider

	// EmbedIndexStore is needed for Phase 9 to write to insights/recommendations collections
	EmbedIndexStore EmbedIndexStore

	// SchemaRetriever is the Qdrant-backed top-K schema retriever.
	// Required — discovery is gated on schema_index_status == "ready",
	// so the indexer has built the per-project Qdrant collection before
	// we get here. Passing nil produces a hard error at run time rather
	// than silently regressing to the legacy keyword-match heuristic.
	SchemaRetriever *schema_retrieve.Retriever

	// SchemaCache is the per-project schema cache populated by the
	// schema indexer (see agentserver/index_schema.go). Required for the
	// same reason as SchemaRetriever — without it the orchestrator would
	// re-issue ~one SELECT per warehouse table to rebuild the schemas
	// map, which is exactly the behavior the schema-retrieval feature
	// replaces.
	SchemaCache SchemaCache

	// WarehouseHash is the hash that keys the SchemaCache lookup. Caller
	// computes it once from the project's WarehouseConfig (matches what
	// the indexer wrote with) so a config change naturally misses the
	// cache and surfaces the "re-index required" error rather than
	// returning stale schemas.
	WarehouseHash string

	// RunStepIndex is the per-run vector index of exploration steps.
	// Required — the analysis phase uses it to rank steps per area.
	// A nil value here surfaces as a hard error when discovery
	// starts so a misconfigured agent doesn't silently regress to
	// the old keyword-only selection.
	RunStepIndex RunStepIndex
}

// NewOrchestrator creates a new discovery orchestrator.
func NewOrchestrator(opts OrchestratorOptions) *Orchestrator {
	var debugLogger *debug.Logger
	if opts.DebugLogRepo != nil {
		debugLogger = debug.NewLogger(debug.LoggerOptions{
			Repo:              opts.DebugLogRepo,
			AppID:             opts.ProjectID,
			Enabled:           opts.EnableDebugLogs,
			DiscoveryRunID:    opts.RunID,
			WarehouseProvider: opts.WarehouseProvider,
		})
	}

	if opts.AIClient != nil && debugLogger != nil {
		opts.AIClient.SetDebugLogger(debugLogger)
	}

	// Initialize user count validator
	filterClause := ""
	if opts.FilterField != "" && opts.FilterValue != "" {
		filterClause = fmt.Sprintf("WHERE %s = '%s'", opts.FilterField, opts.FilterValue)
	}

	var ucValidator *validation.UserCountValidator
	if opts.Warehouse != nil {
		ucValidator = validation.NewUserCountValidator(validation.UserCountValidatorOptions{
			Warehouse:   opts.Warehouse,
			DebugLogger: debugLogger,
			Dataset:     opts.Warehouse.GetDataset(),
			Filter:      filterClause,
		})
	}

	// InsightValidator created in RunDiscovery where QueryExecutor is available

	// Status reporter for live updates. Per-step rows go to the
	// discovery_run_steps collection via RunStepRepo (split out of the
	// run doc to avoid the 16MB-array problem); run-doc-level updates
	// (status / phase / counters) stay on RunRepo.
	statusReporter := NewStatusReporter(opts.RunRepo, opts.RunStepRepo, opts.ProjectID, opts.RunID, 0)

	// Normalize a typed-nil concrete pointer back to an untyped-nil
	// interface so the `o.discoveryLogRepo == nil` guard in
	// persistSplitLogs is not fooled by Go's interface-conversion
	// semantics: passing a nil *DiscoveryLogRepository through a
	// concrete-pointer field would otherwise produce a non-nil
	// interface value with a nil concrete pointer, and the persistence
	// branch would dereference it.
	var discoveryLogRepo discoveryLogPersister
	if opts.DiscoveryLogRepo != nil {
		discoveryLogRepo = opts.DiscoveryLogRepo
	}

	return &Orchestrator{
		aiClient:           opts.AIClient,
		warehouse:          opts.Warehouse,
		contextRepo:        opts.ContextRepo,
		discoveryRepo:      opts.DiscoveryRepo,
		discoveryLogRepo:   discoveryLogRepo,
		feedbackRepo:       opts.FeedbackRepo,
		debugLogRepo:       opts.DebugLogRepo,
		debugLogger:        debugLogger,
		statusReporter:     statusReporter,
		userCountValidator: ucValidator,
		projectID:          opts.ProjectID,
		domain:             opts.Domain,
		category:           opts.Category,
		language:           opts.Language,
		profile:            opts.Profile,
		projectPrompts:     opts.ProjectPrompts,
		datasets:           opts.Datasets,
		filterField:        opts.FilterField,
		filterValue:        opts.FilterValue,
		llmProvider:        opts.LLMProvider,
		llmModel:           opts.LLMModel,
		vectorStore:        opts.VectorStore,
		embeddingProvider:  opts.EmbeddingProvider,
		embedIndexStore:    opts.EmbedIndexStore,
		embedder:           opts.EmbeddingProvider, // same interface, named differently to avoid ambiguity
		schemaRetriever:    opts.SchemaRetriever,
		schemaCache:        opts.SchemaCache,
		warehouseHash:      opts.WarehouseHash,
		runStepIndex:       opts.RunStepIndex,
		runID:              opts.RunID,
	}
}

// DiscoveryOptions configures a discovery run.
type DiscoveryOptions struct {
	MaxSteps int
	// MinSteps is a floor on exploration steps — early "done" signals below
	// this value are rejected and exploration continues. Zero disables it.
	MinSteps              int
	IncludeExplorationLog bool
	TestMode              bool
	SelectedAreas         []string // if set, only run these analysis areas (partial run)
}

// RunDiscovery executes the complete discovery process.
func (o *Orchestrator) RunDiscovery(ctx context.Context, opts DiscoveryOptions) (*models.DiscoveryResult, error) {
	// Set max steps for accurate progress reporting
	o.statusReporter.maxSteps = opts.MaxSteps
	if o.statusReporter.maxSteps <= 0 {
		o.statusReporter.maxSteps = 100
	}

	applog.WithFields(applog.Fields{
		"project_id": o.projectID,
		"domain":     o.domain,
		"category":   o.category,
	}).Info("Starting discovery run")

	startTime := time.Now()

	// Get prompts from project configuration (fully seeded at project creation)
	prompts, analysisAreas := o.resolvePrompts()

	// Build filter clause
	filterClause := o.buildFilterClause()

	// Datasets info for prompts — show all available datasets
	datasetsStr := strings.Join(o.datasets, ", ")

	// Initialize query executor (uses the warehouse provider which can query any dataset)
	sqlFixer := ai.NewSQLFixer(ai.SQLFixerOptions{
		Client:       o.aiClient,
		SQLFixPrompt: o.warehouse.SQLFixPrompt(),
		Dataset:      datasetsStr,
		Filter:       filterClause,
	})
	executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
		Warehouse:   o.warehouse,
		SQLFixer:    sqlFixer,
		DebugLogger: o.debugLogger,
		MaxRetries:  5,
		FilterField: o.filterField,
		FilterValue: o.filterValue,
	})

	// Initialize insight validator with self-healing executor
	if o.aiClient != nil {
		o.insightValidator = validation.NewInsightValidator(validation.InsightValidatorOptions{
			AIClient:  o.aiClient,
			Warehouse: o.warehouse,
			Executor:  &executorAdapter{executor: executor},
			Dataset:   datasetsStr,
			Filter:    filterClause,
		})
	}

	// Wire the self-healing executor into the user-count validator (Layer 4).
	// The validator's hardcoded user_id probes hallucinate on warehouses with
	// non-`user_id` user-id columns; routing through the executor with
	// FixOpts lets the SQL fixer substitute the real column name on retry
	// using the same source-step grounding evidence the insight verifier
	// uses. plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.4.
	if o.userCountValidator != nil {
		o.userCountValidator.SetExecutor(&executorAdapter{executor: executor})
	}

	// Note: live SchemaDiscovery is intentionally NOT constructed here.
	// Discovery runs require a ready schema index (API gates on
	// schema_index_status == "ready"), so o.schemaCache.Find returns
	// the full schemas map without touching the warehouse. The indexer
	// owns live discovery; the run loop never re-issues per-table
	// SELECTs during a discovery.

	// Phase 1: Load project context + previous discoveries + feedback
	applog.Info("Phase 1: Loading project context")
	o.statusReporter.SetPhase(ctx, models.PhaseInit, "Loading project context...", 5)
	projectCtx, err := o.loadProjectContext(ctx)
	if err != nil {
		applog.WithError(err).Warn("Failed to load project context, starting fresh")
		projectCtx = models.NewProjectContext(o.projectID)
	}

	// Load previous discoveries and feedback for context awareness
	prevInsights, prevRecs, feedbackSummaries := o.loadPreviousDiscoveryContext(ctx)
	applog.WithFields(applog.Fields{
		"prev_insights":  len(prevInsights),
		"prev_recs":      len(prevRecs),
		"feedback_items": len(feedbackSummaries),
	}).Info("Previous context loaded")

	previousContextStr := o.buildPreviousContext(projectCtx, prevInsights, prevRecs, feedbackSummaries)

	// Phase 2: Load schemas from the per-project schema cache.
	// (Discovery is gated on schema_index_status == "ready"; the indexer
	// has already populated the cache and the Qdrant collection.)
	applog.Info("Phase 2: Loading schemas from cache")
	o.statusReporter.SetPhase(ctx, models.PhaseSchemaDiscovery, "Loading cached warehouse schemas...", 8)
	schemas, err := o.discoverSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("schema cache lookup failed: %w", err)
	}
	applog.WithField("tables", len(schemas)).Info("Schemas loaded from cache")

	// Build the catalog the LLM sees in its system prompt: one line per
	// table. We DO NOT pre-populate per-table column / sample detail —
	// that arrives on demand via the lookup_schema action, served by
	// the SchemaProvider wired below. This is the architectural change
	// that bounds prompt growth (full discussion in
	// docs/architecture/agent-on-demand-schema.md).
	schemaBuilder := &SchemaContextBuilder{Schemas: schemas}
	keywords := o.collectAreaKeywords(analysisAreas)
	rendered := schemaBuilder.BuildCatalog(keywords)
	applog.WithFields(applog.Fields{
		"tables":          len(schemas),
		"catalog_tokens":  rendered.CatalogTokens,
		"catalog_dropped": rendered.CatalogDropped,
	}).Info("Schema catalog built")

	// Stamp telemetry on the run document. Per-action lookup / search
	// counters are bumped separately by the StatusReporter as the
	// engine services each on-demand schema action.
	o.statusReporter.RecordSchemaTelemetry(ctx, rendered.CatalogTokens, len(schemas))

	// SQL fixer + insight validator still consume a single "context"
	// string. Feed them the Level-0 catalog — they don't need the
	// full retrieved block (sample rows aren't useful when the goal is
	// to map an error back to a table name).
	sqlFixer.SetSchemaContext(rendered.Catalog)
	if o.insightValidator != nil {
		o.insightValidator.SetSchemaContext(rendered.Catalog)
	}

	profileStr := "No project profile configured. Analyze the data without game-specific context."
	if len(o.profile) > 0 {
		pj, _ := json.MarshalIndent(o.profile, "", "  ")
		profileStr = string(pj)
	}
	areasDesc := o.buildAnalysisAreasDescription(analysisAreas)

	// Prepare base context (shared across all prompts — substituted once).
	// {{LANGUAGE}} resolves to the project's configured output language;
	// empty falls back to "English" so legacy projects keep working.
	// Per the keep-technical-fields-English clause in base_context.md, SQL,
	// column names, JSON keys, severity values, and analysis_area values
	// stay in English regardless of the chosen output language.
	language := o.language
	if language == "" {
		language = "English"
	}
	// refDataset is the dataset name used to qualify {{REF:table}}
	// placeholders. We pick the first dataset because example SQL
	// snippets in domain-pack prompts target a single dataset (a
	// multi-dataset project still gets dialect-correct refs against
	// its primary dataset, which is what those examples need).
	refDataset := ""
	if len(o.datasets) > 0 {
		refDataset = o.datasets[0]
	}

	baseContext := o.buildBaseContext(prompts.BaseContext, profileStr, previousContextStr, language, refDataset)

	// Prepare exploration prompt: base context + exploration-specific content.
	// {{SCHEMA_INFO}} is the single canonical schema placeholder; it
	// resolves to the compact catalog (one line per table). Per-table
	// column / sample detail is no longer pre-rendered — the LLM
	// fetches it on demand via the lookup_schema action.
	explorationPrompt := baseContext + "\n\n" + prompts.Exploration
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{DATASET}}", datasetsStr)
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{SCHEMA_INFO}}", rendered.Catalog)
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER}}", filterClause)
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER_CONTEXT}}", o.buildFilterContext())
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER_RULE}}", o.buildFilterRule())
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{ANALYSIS_AREAS}}", areasDesc)
	explorationPrompt = substituteDialectTokens(explorationPrompt, o.warehouse, refDataset)

	// Inject project knowledge sources (no-op if no enterprise plugin loaded
	// or no sources indexed). Query phrased to surface broad domain context
	// useful for any exploration step.
	knowledgeQuery := fmt.Sprintf("data exploration for %s; analysis areas: %s", datasetsStr, o.areaNamesCSV(analysisAreas))
	explorationPrompt = o.injectKnowledgeSources(ctx, explorationPrompt, knowledgeQuery, knowledgeTopKExploration)

	// Phase 3: Autonomous exploration
	applog.Info("Phase 3: Running autonomous exploration")
	o.statusReporter.SetPhase(ctx, models.PhaseExploration, "Starting autonomous data exploration...", 10)

	// Build the SchemaProvider that backs the lookup_schema /
	// search_tables actions. The cache provider serves entirely from
	// the schemas map already loaded above + the per-project Qdrant
	// collection, so there is no live warehouse traffic in the
	// exploration loop.
	schemaProvider, spErr := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID: o.projectID,
		Datasets:  o.datasets,
		Schemas:   schemas,
		Retriever: o.schemaRetriever,
		Embedder:  o.embedder,
	})
	if spErr != nil {
		// This is a wiring bug — the schema cache lookup above already
		// guarantees Schemas is non-nil. Surface clearly rather than
		// continuing with a nil provider.
		return nil, fmt.Errorf("build schema provider: %w", spErr)
	}

	// Share the SchemaProvider with the verifier so its Layer 3 tool loop
	// can issue lookup_schema actions for tables source_steps did not cover.
	// Without this wiring the verifier falls through to single-shot
	// generation (Layer 1 + 2 only). Background:
	// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md §4.3.
	if o.insightValidator != nil {
		o.insightValidator.SetSchemaProvider(schemaProvider)
	}

	// Counting decorator: every successful upsert bumps the
	// run-level analysis_step_index_upserts counter so the dashboard
	// can show "indexed N of M steps" without re-deriving from the
	// step list.
	var stepIndexer ai.StepIndexer
	if o.runStepIndex != nil {
		stepIndexer = countingStepIndexer{
			inner:    o.runStepIndex,
			reporter: o.statusReporter,
			ctx:      ctx,
		}
	}

	o.explorationEngine = ai.NewExplorationEngine(ai.ExplorationEngineOptions{
		Client:         o.aiClient,
		Executor:       executor,
		MaxSteps:       opts.MaxSteps,
		MinSteps:       opts.MinSteps,
		Dataset:        datasetsStr,
		SchemaProvider: schemaProvider,
		StepIndexer:    stepIndexer,
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string, inputTokens, outputTokens int) {
			o.statusReporter.AddExplorationStep(ctx, stepNum, action, thinking, query, rowCount, queryTimeMs, queryFixed, errMsg, inputTokens, outputTokens)
		},
	})

	// Defer dropping the per-run Qdrant collection — runs that
	// crash mid-flight rely on the boot-time orphan sweep, but a
	// clean exit (success or failure) cleans up immediately.
	if o.runStepIndex != nil {
		applog.WithField("run_id", o.runID).Debug("orchestrator: per-run step index wired; deferring Drop")
		defer func() {
			applog.WithField("run_id", o.runID).Debug("orchestrator: dropping per-run step index on exit")
			if err := o.runStepIndex.Drop(ctx); err != nil {
				applog.WithError(err).Warn("Failed to drop per-run step index; orphan sweep will retry on next agent boot")
			}
		}()
	} else {
		applog.WithField("run_id", o.runID).Warn("orchestrator: runStepIndex is nil — analysis will use empty vector hits")
	}

	explorationResult, err := o.explorationEngine.Explore(ctx, ai.ExplorationContext{
		ProjectID:     o.projectID,
		Dataset:       datasetsStr,
		InitialPrompt: explorationPrompt,
	})
	if err != nil {
		return nil, fmt.Errorf("exploration failed: %w", err)
	}
	applog.WithField("steps", explorationResult.TotalSteps).Info("Exploration completed")

	// Wire the exploration log into the verifier before the analysis loop
	// runs. The verifier renders the SQL of cited source_steps into its
	// generation prompt as authoritative column-grounding evidence — without
	// this wiring it would hallucinate column names on warehouses with
	// non-English / abbreviated columns (customer ticket 2026-04-30, see
	// plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md). ValidateInsights
	// panics if this wiring is missing, by design.
	{
		steps := explorationResult.Steps
		if steps == nil {
			steps = []models.ExplorationStep{}
		}
		if o.insightValidator != nil {
			o.insightValidator.SetExplorationLog(steps)
		}
		if o.userCountValidator != nil {
			o.userCountValidator.SetExplorationLog(steps)
		}
	}

	// Phase 4: Analysis by area (dynamic from domain pack)
	// Filter areas if selective run requested
	runAreas := analysisAreas
	runType := "full"
	if len(opts.SelectedAreas) > 0 {
		runType = "partial"
		selected := make(map[string]bool)
		for _, a := range opts.SelectedAreas {
			selected[a] = true
		}
		var filtered []AnalysisArea
		for _, a := range analysisAreas {
			if selected[a.ID] {
				filtered = append(filtered, a)
			}
		}
		runAreas = filtered
		applog.WithFields(applog.Fields{
			"requested": opts.SelectedAreas,
			"matched":   len(runAreas),
		}).Info("Selective discovery — running subset of areas")
	}

	applog.Info("Phase 4: Running analysis by area")
	o.statusReporter.SetPhase(ctx, models.PhaseAnalysis, "Analyzing discoveries by category...", 65)
	allInsights := make([]models.Insight, 0)
	analysisLog := make([]models.AnalysisStep, 0)

	// Vector-ranked step picker for the analysis phase. The closure
	// over runStepIndex.Search keeps the picker decoupled from the
	// concrete index implementation — tests inject canned hits
	// directly.
	picker := NewAnalysisStepPicker(func(c context.Context, q string, sopts RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
		if o.runStepIndex == nil {
			return nil, nil
		}
		return o.runStepIndex.Search(c, q, sopts)
	})
	picker.EstimateRenderedSize = EstimateCompactedRenderedSize

	for _, area := range runAreas {
		areaPrompt, ok := prompts.AnalysisAreas[area.ID]
		if !ok {
			applog.WithField("area", area.ID).Warn("No prompt for analysis area, skipping")
			continue
		}

		pickResult, pickErr := picker.Pick(ctx, area, explorationResult.Steps)
		if pickErr != nil {
			applog.WithFields(applog.Fields{"area": area.ID, "error": pickErr.Error()}).Warn("Step picker failed; skipping area")
			continue
		}
		o.statusReporter.IncrementAnalysisCounter(ctx, "step_index_search_calls", 1)
		if len(pickResult.Dropped) > 0 {
			o.statusReporter.IncrementAnalysisCounter(ctx, "steps_dropped", len(pickResult.Dropped))
		}
		if len(pickResult.Picked) == 0 {
			applog.WithFields(applog.Fields{
				"area":    area.ID,
				"dropped": len(pickResult.Dropped),
			}).Info("No relevant queries found, skipping")
			continue
		}
		relevantSteps := stepsFromPickResult(pickResult)

		// Per-area picked-step debug log — useful for diagnosing why
		// the LLM produced (or didn't produce) insights for an area.
		var pickedSummary []map[string]any
		for _, p := range pickResult.Picked {
			pickedSummary = append(pickedSummary, map[string]any{
				"step":   p.Step.Step,
				"score":  p.Score,
				"source": string(p.Source),
			})
		}
		applog.WithFields(applog.Fields{
			"area":         area.ID,
			"area_name":    area.Name,
			"picked_count": len(relevantSteps),
			"dropped_count": len(pickResult.Dropped),
			"picked":       pickedSummary,
		}).Debug("Analysis area: picked steps")

		applog.WithFields(applog.Fields{
			"area":    area.ID,
			"queries": len(relevantSteps),
			"dropped": len(pickResult.Dropped),
		}).Info("Analyzing area")

		// Render the compacted view into the prompt. This replaces
		// the old json.MarshalIndent of the full ExplorationStep,
		// which on ERP-scale runs grew to >1M tokens.
		queryResultsJSON := RenderCompactedSteps(relevantSteps)
		applog.WithFields(applog.Fields{
			"area":          area.ID,
			"queries":       len(relevantSteps),
			"results_chars": len(queryResultsJSON),
			"prompt_chars":  len(baseContext) + len(areaPrompt) + len(queryResultsJSON),
		}).Debug("Analysis area: rendered prompt sizing")
		prompt := o.buildAnalysisAreaPrompt(baseContext, areaPrompt, datasetsStr, len(relevantSteps), queryResultsJSON, refDataset)

		// Inject project knowledge sources relevant to this analysis area.
		areaQuery := fmt.Sprintf("%s: %s", area.Name, area.Description)
		prompt = o.injectKnowledgeSources(ctx, prompt, areaQuery, knowledgeTopKAnalysis)

		// Create analysis step to capture full dialog
		step := models.AnalysisStep{
			AreaID:             area.ID,
			AreaName:           area.Name,
			RunAt:              time.Now(),
			Prompt:             prompt,
			RelevantQueries:    len(relevantSteps),
			QueryResultsChars:  len(queryResultsJSON),
			SelectedSteps:      pickedToTelemetry(pickResult.Picked),
			DroppedSteps:       droppedToTelemetry(pickResult.Dropped),
		}

		// Call LLM
		maxTokens := gollm.GetMaxOutputTokens(o.llmProvider, o.llmModel)
		chatResult, err := o.aiClient.Chat(ctx, prompt, "", maxTokens)
		if err != nil {
			step.Error = err.Error()
			analysisLog = append(analysisLog, step)
			applog.WithFields(applog.Fields{"area": area.ID, "error": err.Error()}).Warn("Analysis failed")
			continue
		}

		step.Response = chatResult.Content
		step.TokensIn = chatResult.TokensIn
		step.TokensOut = chatResult.TokensOut
		step.DurationMs = chatResult.DurationMs

		// Parse insights from response
		insights, parseErr := o.parseInsights(chatResult.Content, area.ID)
		if parseErr != nil {
			step.Error = fmt.Sprintf("parse error: %s", parseErr.Error())
			analysisLog = append(analysisLog, step)
			applog.WithFields(applog.Fields{"area": area.ID, "error": parseErr.Error()}).Warn("Failed to parse insights")
			continue
		}

		step.Insights = insights

		// Phase 4.5: Validate insights
		if len(insights) > 0 {
			var areaValidation []models.ValidationResult

			// First: validate user counts against total users
			if o.userCountValidator != nil {
				countResults := o.userCountValidator.ValidateInsights(ctx, insights)
				areaValidation = append(areaValidation, countResults...)
			}

			// Second: verify insights by querying the warehouse
			if o.insightValidator != nil {
				warehouseResults := o.insightValidator.ValidateInsights(ctx, insights)
				areaValidation = append(areaValidation, warehouseResults...)
			}

			step.ValidationResults = areaValidation
		}

		analysisLog = append(analysisLog, step)
		allInsights = append(allInsights, insights...)

		// Report analysis completion and insights to status
		o.statusReporter.AddAnalysisStep(ctx, area.ID, area.Name, len(insights), "", step.TokensIn, step.TokensOut)
		for _, insight := range insights {
			o.statusReporter.AddInsightStep(ctx, insight.Name, insight.Severity, area.ID)
		}

		// Report validation results to status. Tokens come from the
		// per-insight accumulator stamped onto the ValidationResult by
		// the validator.
		for _, vr := range step.ValidationResults {
			o.statusReporter.AddValidationStep(ctx, vr.ClaimedMetric, vr.Status, vr.ClaimedCount, vr.VerifiedCount, vr.InputTokens, vr.OutputTokens)
		}

		applog.WithFields(applog.Fields{
			"area":     area.ID,
			"insights": len(insights),
		}).Info("Analysis complete for area")
	}

	// Check for analysis failures
	var analysisErrors []string
	failedAreas := 0
	for _, step := range analysisLog {
		if step.Error != "" {
			failedAreas++
			analysisErrors = append(analysisErrors, fmt.Sprintf("%s: %s", step.AreaID, step.Error))
		}
	}
	if failedAreas > 0 {
		applog.WithFields(applog.Fields{
			"failed_areas": failedAreas,
			"total_areas":  len(runAreas),
		}).Warn("Some analysis areas failed")
	}

	// Phase 5: Generate recommendations
	applog.Info("Phase 5: Generating recommendations")
	o.statusReporter.SetPhase(ctx, models.PhaseRecommendations, "Generating actionable recommendations...", 85)
	recommendations, recStep := o.generateRecommendations(ctx, prompts.Recommendations, allInsights, baseContext, datasetsStr)
	// Emit a per-call RunStep so the live UI carries the recommendation
	// LLM call's tokens alongside exploration/analysis steps. recStep
	// is non-nil when the recommendation phase ran at all —
	// generateRecommendations always returns a step (even on
	// parse/LLM failure it stamps Error).
	if recStep != nil {
		o.statusReporter.AddRecommendationStep(ctx, len(recommendations), recStep.Error, recStep.TokensIn, recStep.TokensOut)
	}

	// Validate recommendation segment sizes
	var recValidationResults []models.ValidationResult
	if o.userCountValidator != nil && len(recommendations) > 0 {
		recValidationResults = o.userCountValidator.ValidateRecommendations(ctx, recommendations)
	}

	// Phase 6: Update project context with discovered patterns
	applog.Info("Phase 6: Updating project context")
	projectCtx.RecordDiscovery(true)
	projectCtx.UpdatePatterns(allInsights)
	if err := o.saveProjectContext(ctx, projectCtx); err != nil {
		applog.WithError(err).Warn("Failed to save project context")
	}

	// Phase 7: Save discovery result
	applog.Info("Phase 7: Saving discovery result")
	o.statusReporter.SetPhase(ctx, models.PhaseSaving, "Saving discovery results...", 95)

	// Merge all validation results
	allValidation := make([]models.ValidationResult, 0)
	for _, step := range analysisLog {
		allValidation = append(allValidation, step.ValidationResults...)
	}
	allValidation = append(allValidation, recValidationResults...)

	// Determine run type based on failures
	if failedAreas > 0 && failedAreas == len(runAreas) {
		// All areas failed — mark as failed run
		runType = "failed"
	} else if failedAreas > 0 && runType != "partial" {
		// Some areas failed — mark as partial
		runType = "partial"
	}

	result := &models.DiscoveryResult{
		ProjectID:       o.projectID,
		Domain:          o.domain,
		Category:        o.category,
		RunType:         runType,
		AreasRequested:  opts.SelectedAreas,
		DiscoveryDate:   time.Now(),
		TotalSteps:      explorationResult.TotalSteps,
		Duration:        time.Since(startTime),
		Schemas:         schemas,
		Insights:        allInsights,
		Recommendations: recommendations,
		Summary: models.Summary{
			Date:                 time.Now(),
			TotalInsights:        len(allInsights),
			TotalRecommendations: len(recommendations),
			QueriesExecuted:      explorationResult.TotalSteps,
			Errors:               analysisErrors,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := o.discoveryRepo.Save(ctx, result); err != nil {
		return nil, fmt.Errorf("failed to save discovery result: %w", err)
	}

	o.persistSplitLogs(ctx, result.ID, explorationResult.Steps, analysisLog, allValidation, recStep)

	// Phase 9: Embed & Index (non-fatal — errors logged, discovery still completes)
	if o.embedIndexStore != nil {
		o.runPhaseEmbedIndex(ctx, result)
	}

	// Mark run as completed. Pass the discovery's _id so the run
	// document carries the back-reference run-completion hook
	// consumers depend on (plugin-hooks.md Hook 5).
	o.statusReporter.Complete(ctx, result.ID, len(allInsights))

	applog.WithFields(applog.Fields{
		"project_id":      o.projectID,
		"insights":        len(allInsights),
		"recommendations": len(recommendations),
		"validations":     len(allValidation),
		"duration":        time.Since(startTime).String(),
	}).Info("Discovery run completed")

	return result, nil
}

// persistSplitLogs writes the per-step / per-area / per-result rows into
// their dedicated collections. Failures are logged and swallowed: the parent
// DiscoveryResult is already persisted, and rolling back over a log-write
// hiccup would lose the structured outputs (insights, recommendations,
// summary). Background: embedded arrays previously here blew past the 16MB
// BSON document limit on long runs. See database/discovery_log_repo.go.
//
// When discoveryLogRepo is nil (test or single-binary builds without
// MongoDB), this is a no-op.
func (o *Orchestrator) persistSplitLogs(
	ctx context.Context,
	discoveryID string,
	explorationSteps []models.ExplorationStep,
	analysisSteps []models.AnalysisStep,
	validations []models.ValidationResult,
	recStep *models.RecommendationStep,
) {
	if o.discoveryLogRepo == nil {
		return
	}
	if err := o.discoveryLogRepo.SaveExplorationSteps(ctx, o.projectID, discoveryID, o.runID, explorationSteps); err != nil {
		applog.WithError(err).Warn("Failed to persist exploration steps to split collection")
	}
	if err := o.discoveryLogRepo.SaveAnalysisSteps(ctx, o.projectID, discoveryID, o.runID, analysisSteps); err != nil {
		applog.WithError(err).Warn("Failed to persist analysis steps to split collection")
	}
	if err := o.discoveryLogRepo.SaveValidationResults(ctx, o.projectID, discoveryID, o.runID, validations); err != nil {
		applog.WithError(err).Warn("Failed to persist validation results to split collection")
	}
	if err := o.discoveryLogRepo.SaveRecommendationLog(ctx, o.projectID, discoveryID, o.runID, recStep); err != nil {
		applog.WithError(err).Warn("Failed to persist recommendation log to split collection")
	}
}

// parseInsights parses LLM response JSON into Insight structs.
func (o *Orchestrator) parseInsights(response string, areaID string) ([]models.Insight, error) {
	var result struct {
		Insights []models.Insight `json:"insights"`
	}

	cleaned := cleanJSONResponse(response)
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("failed to parse analysis response: %w", err)
	}

	for i := range result.Insights {
		result.Insights[i].AnalysisArea = areaID
		if result.Insights[i].DiscoveredAt.IsZero() {
			result.Insights[i].DiscoveredAt = time.Now()
		}
		// Assign a UUID if the LLM didn't give one. The same UUID is later
		// reused as the standalone `insights._id` and the Qdrant point id, so
		// every link built from a search hit (Ask sources, related cards) can
		// use the existing /discoveries/{did}/insights/{id} route without any
		// client-side fallback. Qdrant only accepts UUID / uint64 point ids,
		// which is why the embedded id itself has to be a UUID.
		if result.Insights[i].ID == "" {
			result.Insights[i].ID = uuid.New().String()
		}
	}

	return result.Insights, nil
}

// generateRecommendations generates actionable recommendations and captures the full dialog.
func (o *Orchestrator) generateRecommendations(
	ctx context.Context,
	promptTemplate string,
	insights []models.Insight,
	baseContext string,
	datasetsStr string,
) ([]models.Recommendation, *models.RecommendationStep) {
	step := &models.RecommendationStep{
		RunAt:        time.Now(),
		InsightCount: len(insights),
	}

	if len(insights) == 0 {
		return make([]models.Recommendation, 0), step
	}

	insightsJSON, _ := json.MarshalIndent(insights, "", "  ")

	// Build insights summary
	areaCounts := make(map[string]int)
	for _, i := range insights {
		areaCounts[i.AnalysisArea]++
	}
	parts := make([]string, 0)
	for area, count := range areaCounts {
		parts = append(parts, fmt.Sprintf("%s: %d", area, count))
	}
	summary := fmt.Sprintf("Total: %d insights (%s)", len(insights), strings.Join(parts, ", "))

	refDataset := ""
	if len(o.datasets) > 0 {
		refDataset = o.datasets[0]
	}

	prompt := o.buildRecommendationsPrompt(baseContext, promptTemplate, summary, string(insightsJSON), refDataset)

	// Inject project knowledge sources relevant to the discovered insights.
	// Recommendations often need broader business context (constraints, prior
	// initiatives, tone) so we use a higher top-K than analysis prompts.
	recommendationQuery := o.recommendationsKnowledgeQuery(insights)
	prompt = o.injectKnowledgeSources(ctx, prompt, recommendationQuery, knowledgeTopKRecommendations)

	step.Prompt = prompt

	maxTokens := gollm.GetMaxOutputTokens(o.llmProvider, o.llmModel)
	chatResult, err := o.aiClient.Chat(ctx, prompt, "", maxTokens)
	if err != nil {
		step.Error = err.Error()
		applog.WithError(err).Warn("Failed to generate recommendations")
		return make([]models.Recommendation, 0), step
	}

	step.Response = chatResult.Content
	step.TokensIn = chatResult.TokensIn
	step.TokensOut = chatResult.TokensOut
	step.DurationMs = chatResult.DurationMs

	var result struct {
		Recommendations []models.Recommendation `json:"recommendations"`
	}

	cleaned := cleanJSONResponse(chatResult.Content)
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		step.Error = fmt.Sprintf("parse error: %s", err.Error())
		applog.WithError(err).Warn("Failed to parse recommendations")
		return make([]models.Recommendation, 0), step
	}

	for i := range result.Recommendations {
		if result.Recommendations[i].CreatedAt.IsZero() {
			result.Recommendations[i].CreatedAt = time.Now()
		}
		// Assign a UUID if the LLM didn't give one. Same rationale as for
		// insights: the UUID is reused as the standalone `recommendations._id`
		// and Qdrant point id, so URLs that hit the embedded array match
		// without a fallback. Prior to this, the embedded `id` was "", which
		// meant list → detail navigation was silently falling back to the
		// array index and Ask source links to recommendations never worked.
		if result.Recommendations[i].ID == "" {
			result.Recommendations[i].ID = uuid.New().String()
		}
	}

	step.Recommendations = result.Recommendations
	return result.Recommendations, step
}

// --- Helper methods ---

// resolvePrompts extracts prompts and analysis areas from project configuration.
// All prompts are fully seeded at project creation from the domain pack.
func (o *Orchestrator) resolvePrompts() (ResolvedPrompts, []AnalysisArea) {
	if o.projectPrompts == nil {
		return ResolvedPrompts{}, nil
	}

	resolved := ResolvedPrompts{
		Exploration:     o.projectPrompts.Exploration,
		Recommendations: o.projectPrompts.Recommendations,
		BaseContext:     o.projectPrompts.BaseContext,
		AnalysisAreas:   make(map[string]string),
	}

	var areas []AnalysisArea
	for id, cfg := range o.projectPrompts.AnalysisAreas {
		if !cfg.Enabled {
			continue
		}
		resolved.AnalysisAreas[id] = cfg.Prompt
		areas = append(areas, AnalysisArea{
			ID:          id,
			Name:        cfg.Name,
			Description: cfg.Description,
			Keywords:    cfg.Keywords,
			IsBase:      cfg.IsBase,
			Priority:    cfg.Priority,
		})
	}

	return resolved, areas
}

// buildBaseContext renders the per-run base context: applies template
// substitutions, dialect tokens, and the platform-enforced claim-discipline
// rules (rules 1, 2, 7, 8). The returned string is prepended to every
// downstream prompt, so the rule cascade reaches exploration, analysis
// areas, and recommendations through this single function.
func (o *Orchestrator) buildBaseContext(template, profileStr, previousContext, language, refDataset string) string {
	base := template
	base = strings.ReplaceAll(base, "{{PROFILE}}", profileStr)
	base = strings.ReplaceAll(base, "{{PREVIOUS_CONTEXT}}", previousContext)
	base = strings.ReplaceAll(base, "{{LANGUAGE}}", language)
	base = substituteDialectTokens(base, o.warehouse, refDataset)
	return discipline.AppendBaseContextRules(base)
}

// buildAnalysisAreaPrompt renders one analysis-area prompt: combines the
// already-built base context with the area's content, substitutes per-area
// template variables, and appends the platform-enforced insight-writing
// discipline rules (3, 4, 5, 6). Called once per analysis area inside the
// run loop — user-added custom areas flow through the same path, so they
// inherit the rules regardless of pack content.
func (o *Orchestrator) buildAnalysisAreaPrompt(baseContext, areaPrompt, datasetsStr string, totalQueries int, queryResultsJSON, refDataset string) string {
	prompt := baseContext + "\n\n" + areaPrompt
	prompt = strings.ReplaceAll(prompt, "{{DATASET}}", datasetsStr)
	prompt = strings.ReplaceAll(prompt, "{{TOTAL_QUERIES}}", fmt.Sprintf("%d", totalQueries))
	prompt = strings.ReplaceAll(prompt, "{{QUERY_RESULTS}}", queryResultsJSON)
	prompt = substituteDialectTokens(prompt, o.warehouse, refDataset)
	return discipline.AppendAnalysisRules(prompt)
}

// buildRecommendationsPrompt renders the recommendations prompt: combines
// the already-built base context with the recommendations template,
// substitutes recommendation-specific template variables, and appends the
// platform-enforced recommendation discipline rules (3, 4, 5, 6 framed for
// the recommendation schema + non-dramatic-language reiteration).
func (o *Orchestrator) buildRecommendationsPrompt(baseContext, template, insightsSummary, insightsJSON, refDataset string) string {
	prompt := baseContext + "\n\n" + template
	prompt = strings.ReplaceAll(prompt, "{{DISCOVERY_DATE}}", time.Now().Format("2006-01-02"))
	prompt = strings.ReplaceAll(prompt, "{{INSIGHTS_SUMMARY}}", insightsSummary)
	prompt = strings.ReplaceAll(prompt, "{{INSIGHTS_DATA}}", insightsJSON)
	prompt = substituteDialectTokens(prompt, o.warehouse, refDataset)
	return discipline.AppendRecommendationsRules(prompt)
}

func (o *Orchestrator) buildFilterClause() string {
	if o.filterField == "" || o.filterValue == "" {
		return ""
	}
	return fmt.Sprintf("WHERE %s = '%s'", o.filterField, o.filterValue)
}

func (o *Orchestrator) buildFilterContext() string {
	if o.filterField == "" {
		return ""
	}
	return fmt.Sprintf("**Filter**: All queries must include `%s = '%s'`", o.filterField, o.filterValue)
}

func (o *Orchestrator) buildFilterRule() string {
	if o.filterField == "" {
		return "**No filter required**: This dataset contains only this project's data."
	}
	return fmt.Sprintf("**ALWAYS filter by %s**: `WHERE %s = '%s'`", o.filterField, o.filterField, o.filterValue)
}

func (o *Orchestrator) buildAnalysisAreasDescription(areas []AnalysisArea) string {
	var sb strings.Builder
	for i, area := range areas {
		fmt.Fprintf(&sb, "%d. **%s** - %s\n", i+1, area.Name, area.Description)
	}
	return sb.String()
}

// buildPreviousContext builds a rich context from previous discoveries and user feedback.
// This prevents duplicate insights, respects user feedback, and helps the LLM focus on new findings.
func (o *Orchestrator) buildPreviousContext(
	pctx *models.ProjectContext,
	prevInsights []models.InsightSummary,
	prevRecs []models.RecommendationSummary,
	feedback []models.FeedbackSummary,
) string {
	if pctx == nil || pctx.TotalDiscoveries == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Previous Discovery Context\n\n")
	fmt.Fprintf(&sb, "This is discovery run #%d. ", pctx.TotalDiscoveries+1)
	fmt.Fprintf(&sb, "Last discovery: %s.\n\n", pctx.LastDiscoveryDate.Format("2006-01-02"))

	// Previous insights
	if len(prevInsights) > 0 {
		sb.WriteString("### Previously Found Insights\n")
		sb.WriteString("These insights were already discovered. Do NOT repeat them unless the data has significantly changed. Focus on new patterns.\n\n")
		for _, ins := range prevInsights {
			fmt.Fprintf(&sb, "- **%s** [%s, %s] — %d affected (%s)\n",
				ins.Name, ins.AnalysisArea, ins.Severity, ins.AffectedCount, ins.Date)
		}
		sb.WriteString("\n")
	}

	// User feedback
	disliked := make([]models.FeedbackSummary, 0)
	liked := make([]models.FeedbackSummary, 0)
	for _, f := range feedback {
		if f.Rating == "dislike" {
			disliked = append(disliked, f)
		} else {
			liked = append(liked, f)
		}
	}

	if len(disliked) > 0 {
		sb.WriteString("### User Feedback — Disliked Insights (AVOID)\n")
		sb.WriteString("The user marked these insights as NOT useful. Avoid similar conclusions or address the feedback.\n\n")
		for _, f := range disliked {
			if f.Comment != "" {
				fmt.Fprintf(&sb, "- **%s** — user comment: \"%s\"\n", f.InsightName, f.Comment)
			} else {
				fmt.Fprintf(&sb, "- **%s** — marked not useful\n", f.InsightName)
			}
		}
		sb.WriteString("\n")
	}

	if len(liked) > 0 {
		sb.WriteString("### User Feedback — Liked Insights (MONITOR)\n")
		sb.WriteString("The user found these valuable. Check if they have changed or evolved.\n\n")
		for _, f := range liked {
			fmt.Fprintf(&sb, "- **%s**\n", f.InsightName)
		}
		sb.WriteString("\n")
	}

	// Previous recommendations
	if len(prevRecs) > 0 {
		sb.WriteString("### Previously Given Recommendations\n")
		sb.WriteString("Don't repeat these unless the situation has changed.\n\n")
		for _, rec := range prevRecs {
			fmt.Fprintf(&sb, "- P%d: %s (%s)\n", rec.Priority, rec.Title, rec.Category)
		}
		sb.WriteString("\n")
	}

	// Agent observations are auto-learnings the orchestrator records during
	// discovery (separate from user-authored knowledge sources, which render
	// under "## Project Knowledge").
	if len(pctx.Notes) > 0 {
		sb.WriteString("### Agent observations\n")
		shown := 0
		for i := len(pctx.Notes) - 1; i >= 0 && shown < 10; i-- {
			note := pctx.Notes[i]
			if note.Relevance >= 0.5 {
				fmt.Fprintf(&sb, "- %s\n", note.Note)
				shown++
			}
		}
	}

	return sb.String()
}


func (o *Orchestrator) loadProjectContext(ctx context.Context) (*models.ProjectContext, error) {
	return o.contextRepo.GetByProjectID(ctx, o.projectID)
}

func (o *Orchestrator) saveProjectContext(ctx context.Context, pctx *models.ProjectContext) error {
	return o.contextRepo.Save(ctx, pctx)
}

// loadPreviousDiscoveryContext fetches recent discoveries + feedback and builds compact summaries.
func (o *Orchestrator) loadPreviousDiscoveryContext(ctx context.Context) (
	[]models.InsightSummary, []models.RecommendationSummary, []models.FeedbackSummary,
) {
	// Load last 5 discoveries
	recentDiscoveries, err := o.discoveryRepo.ListRecent(ctx, o.projectID, 5)
	if err != nil {
		applog.WithError(err).Warn("Failed to load recent discoveries for context")
		return nil, nil, nil
	}

	if len(recentDiscoveries) == 0 {
		return nil, nil, nil
	}

	// Build insight summaries (deduped by name)
	seenInsights := make(map[string]bool)
	insightSummaries := make([]models.InsightSummary, 0)
	recSummaries := make([]models.RecommendationSummary, 0)
	seenRecs := make(map[string]bool)

	for _, disc := range recentDiscoveries {
		dateStr := disc.DiscoveryDate.Format("2006-01-02")
		for _, ins := range disc.Insights {
			key := ins.AnalysisArea + ":" + ins.Name
			if seenInsights[key] {
				continue
			}
			seenInsights[key] = true
			insightSummaries = append(insightSummaries, models.InsightSummary{
				Name:          ins.Name,
				AnalysisArea:  ins.AnalysisArea,
				Severity:      ins.Severity,
				AffectedCount: ins.AffectedCount,
				Date:          dateStr,
			})
		}
		for _, rec := range disc.Recommendations {
			if seenRecs[rec.Title] {
				continue
			}
			seenRecs[rec.Title] = true
			recSummaries = append(recSummaries, models.RecommendationSummary{
				Title:    rec.Title,
				Category: rec.Category,
				Priority: rec.Priority,
			})
		}
	}

	// Load feedback for these discoveries
	feedbackSummaries := make([]models.FeedbackSummary, 0)
	if o.feedbackRepo != nil {
		discoveryIDs := make([]string, 0, len(recentDiscoveries))
		for _, d := range recentDiscoveries {
			if d.ID != "" {
				discoveryIDs = append(discoveryIDs, d.ID)
			}
		}

		fbEntries, err := o.feedbackRepo.ListByDiscoveryIDs(ctx, discoveryIDs)
		if err != nil {
			applog.WithError(err).Warn("Failed to load feedback for context")
		} else {
			// Build insight name lookup from discoveries
			insightNameByKey := make(map[string]string)
			for _, disc := range recentDiscoveries {
				for i, ins := range disc.Insights {
					insightNameByKey[disc.ID+":insight:"+fmt.Sprintf("%d", i)] = ins.Name
					if ins.ID != "" {
						insightNameByKey[disc.ID+":insight:"+ins.ID] = ins.Name
					}
				}
				for i, rec := range disc.Recommendations {
					insightNameByKey[disc.ID+":recommendation:"+fmt.Sprintf("%d", i)] = rec.Title
				}
			}

			for _, fb := range fbEntries {
				name := insightNameByKey[fb.DiscoveryID+":"+fb.TargetType+":"+fb.TargetID]
				if name == "" {
					name = fb.TargetType + " #" + fb.TargetID
				}
				feedbackSummaries = append(feedbackSummaries, models.FeedbackSummary{
					InsightName: name,
					Rating:      fb.Rating,
					Comment:     fb.Comment,
				})
			}
		}
	}

	// Cap summaries to avoid prompt bloat
	if len(insightSummaries) > 30 {
		insightSummaries = insightSummaries[:30]
	}
	if len(recSummaries) > 15 {
		recSummaries = recSummaries[:15]
	}

	return insightSummaries, recSummaries, feedbackSummaries
}

// discoverSchemas loads the schemas map from the project's schema cache
// and returns it as-is. The cache is the single source of truth — there
// is no live-warehouse fallback, by design:
//   - The discovery API gates on schema_index_status == "ready", so the
//     cache is guaranteed to be populated by the time we run.
//   - Falling back to live re-discovery would issue ~one SELECT per
//     table (the legacy SchemaDiscovery path), which on a 1,400-table
//     warehouse takes ~50 minutes and is exactly what the
//     schema-retrieval feature replaces.
//
// A cache miss here means an invariant has been violated upstream
// (warehouse config changed without a re-index, the indexer wrote
// nothing, the cache was cleared) — surface it as a hard error so the
// user reaches for /reindex rather than silently waiting an hour.
func (o *Orchestrator) discoverSchemas(ctx context.Context) (map[string]models.TableSchema, error) {
	if o.schemaCache == nil {
		return nil, fmt.Errorf("schema cache not wired into orchestrator (programmer error)")
	}
	if o.warehouseHash == "" {
		return nil, fmt.Errorf("warehouse hash not set on orchestrator (programmer error)")
	}
	schemas, err := o.schemaCache.Find(ctx, o.projectID, o.warehouseHash)
	if err != nil {
		return nil, fmt.Errorf("read schema cache: %w", err)
	}
	if len(schemas) == 0 {
		return nil, fmt.Errorf("schema cache is empty for this project — re-index required (POST /api/v1/projects/%s/reindex)", o.projectID)
	}
	applog.WithField("cached_tables", len(schemas)).Info("Loaded schemas from cache")

	// Run any registered cached-schema filters (e.g. discovery-scope)
	// so per-project allow / deny lists shrink the catalog the LLM
	// sees on this run. Filters are silent on no-op (zero filters
	// registered, or scope mode == none); when they shrink the set we
	// log before/after so an operator who set a scope can confirm it
	// took effect for this run without grepping.
	//
	// Sort the keys before invoking filters so input order is
	// deterministic across runs — Go map iteration is randomised, and
	// downstream filters (or their logs / metrics) shouldn't see a
	// different order each time the same set of tables flows through.
	keys := make([]string, 0, len(schemas))
	for k := range schemas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kept, ferr := agentplugin.ApplyCachedSchemaFilters(ctx, o.projectID, keys)
	if ferr != nil {
		return nil, fmt.Errorf("cached-schema filter: %w", ferr)
	}
	// Subset validation: a filter is allowed to shrink the input but
	// MUST NOT add tables we never had in the cache. Catching this
	// here prevents a misbehaving plugin from inventing a key that
	// looks the right shape but has no schema attached, which would
	// surface to the LLM as a "schema for X" prompt with no X in the
	// catalog.
	inputSet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		inputSet[k] = struct{}{}
	}
	for _, k := range kept {
		if _, ok := inputSet[k]; !ok {
			return nil, fmt.Errorf("cached-schema filter returned %q which was not in the input set; filters may only shrink the catalog", k)
		}
	}
	if len(kept) != len(keys) {
		keptSet := make(map[string]struct{}, len(kept))
		for _, k := range kept {
			keptSet[k] = struct{}{}
		}
		filtered := make(map[string]models.TableSchema, len(kept))
		for k, v := range schemas {
			if _, ok := keptSet[k]; ok {
				filtered[k] = v
			}
		}
		applog.WithFields(applog.Fields{
			"before": len(keys),
			"after":  len(filtered),
		}).Info("Cached-schema filter applied")
		if len(filtered) == 0 {
			return nil, fmt.Errorf("cached-schema filter dropped every table for this project — review the discovery scope (POST /api/v1/projects/%s/discovery-scope) or set mode=none", o.projectID)
		}
		return filtered, nil
	}
	return schemas, nil
}

// countingStepIndexer wraps a RunStepIndex and bumps the run's
// analysis_step_index_upserts counter on every successful upsert.
// Lives next to the orchestrator because that is the only place the
// StatusReporter and RunStepIndex come together — the engine itself
// stays decoupled from telemetry plumbing.
type countingStepIndexer struct {
	inner    RunStepIndex
	reporter *StatusReporter
	ctx      context.Context
}

// Upsert delegates to the wrapped index; on success bumps the
// per-run upsert counter. Errors from the inner Upsert propagate
// untouched so the engine logs them.
func (c countingStepIndexer) Upsert(ctx context.Context, step models.ExplorationStep) error {
	if err := c.inner.Upsert(ctx, step); err != nil {
		return err
	}
	if c.reporter != nil {
		c.reporter.IncrementAnalysisCounter(c.ctx, "step_index_upserts", 1)
	}
	return nil
}

// stepsFromPickResult flattens PickResult.Picked back to plain
// ExplorationSteps so the renderer + the prompt counters can consume
// them without knowing about pick scores.
func stepsFromPickResult(pr *PickResult) []models.ExplorationStep {
	out := make([]models.ExplorationStep, 0, len(pr.Picked))
	for _, p := range pr.Picked {
		out = append(out, p.Step)
	}
	return out
}

// pickedToTelemetry serialises PickedStep for the analysis-log
// telemetry record. The dashboard's debug view surfaces it as a
// per-area "what fed the LLM" breakdown.
func pickedToTelemetry(picked []PickedStep) []models.SelectedStep {
	out := make([]models.SelectedStep, 0, len(picked))
	for _, p := range picked {
		out = append(out, models.SelectedStep{
			Step:   p.Step.Step,
			Score:  p.Score,
			Source: string(p.Source),
		})
	}
	return out
}

// droppedToTelemetry mirrors pickedToTelemetry for the dropped list.
func droppedToTelemetry(dropped []DroppedStep) []models.DroppedAnalysisStep {
	out := make([]models.DroppedAnalysisStep, 0, len(dropped))
	for _, d := range dropped {
		out = append(out, models.DroppedAnalysisStep{
			Step:   d.StepNumber,
			Score:  d.Score,
			Reason: string(d.Reason),
		})
	}
	return out
}

// executorAdapter adapts queryexec.QueryExecutor to validation.SelfHealingExecutor.
// It forwards per-call FixOpts via ExecuteWithFixOpts so the validator's
// rendered VerificationContext reaches the SQL fixer on every retry attempt.
type executorAdapter struct {
	executor *queryexec.QueryExecutor
}

func (a *executorAdapter) Execute(ctx context.Context, query string, purpose string, opts queryexec.FixOpts) ([]map[string]interface{}, error) {
	result, err := a.executor.ExecuteWithFixOpts(ctx, query, purpose, opts)
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

func cleanJSONResponse(response string) string {
	response = strings.TrimSpace(response)

	if idx := strings.Index(response, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(response[start:], "```"); end >= 0 {
			return strings.TrimSpace(response[start : start+end])
		}
	}

	if idx := strings.Index(response, "```"); idx >= 0 {
		start := idx + len("```")
		if nl := strings.Index(response[start:], "\n"); nl >= 0 {
			start += nl + 1
		}
		if end := strings.Index(response[start:], "```"); end >= 0 {
			return strings.TrimSpace(response[start : start+end])
		}
	}

	for i, c := range response {
		if c == '{' || c == '[' {
			return response[i:]
		}
	}

	return response
}

// --- Knowledge sources injection ---

// Top-K values per phase. Tuned based on prompt size:
//   - Exploration prompts already include schema + analysis areas → keep small.
//   - Analysis prompts add query results → moderate.
//   - Recommendation prompts often need broader business context → larger.
const (
	knowledgeTopKExploration       = 3
	knowledgeTopKAnalysis          = 5
	knowledgeTopKRecommendations   = 8
	knowledgeMinScore              = 0.4
	knowledgeMaxRetrievalPerPhase  = 3 * time.Second
)

// injectKnowledgeSources walks every registered agentplugin context provider
// (knowledge sources today; column hints / area priority later) and prepends
// their concatenated markdown sections to the prompt.
//
// The hook is always called; with no providers loaded — or when every
// provider returns an empty section — the prompt is returned unchanged.
// Per-provider errors are logged but never returned: failure inside one
// provider must not abort discovery, and must not suppress sections from
// other providers.
func (o *Orchestrator) injectKnowledgeSources(ctx context.Context, prompt, query string, topK int) string {
	if query == "" || topK <= 0 {
		return prompt
	}

	// Apply a tight per-call timeout so a slow embedding call cannot stall a phase.
	retrieveCtx, cancel := context.WithTimeout(ctx, knowledgeMaxRetrievalPerPhase)
	defer cancel()

	section := agentplugin.RenderSections(retrieveCtx, o.projectID, query, agentplugin.ContextProviderOpts{
		Limit:    topK,
		MinScore: knowledgeMinScore,
	}, func(name string, err error) {
		applog.WithFields(applog.Fields{
			"project_id":        o.projectID,
			"context_provider":  name,
			"error":             err.Error(),
		}).Warn("Context provider failed; continuing without its section")
	})

	if section == "" {
		return prompt
	}

	// agentplugin.RenderSections appends a single trailing newline. Preserve
	// the historical "section + blank line + prompt" layout the orchestrator
	// produced before the registry refactor.
	return section + "\n" + prompt
}

// areaNamesCSV returns a comma-separated list of analysis area names for use
// in knowledge retrieval queries.
func (o *Orchestrator) areaNamesCSV(areas []AnalysisArea) string {
	names := make([]string, 0, len(areas))
	for _, a := range areas {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// collectAreaKeywords flattens the keyword lists from every analysis area
// into one de-duplicated slice. Used by the schema-context builder for
// Level 0 hint tagging and Level 1 sparse-keyword re-rank.
func (o *Orchestrator) collectAreaKeywords(areas []AnalysisArea) []string {
	seen := make(map[string]struct{}, 4*len(areas))
	out := make([]string, 0, 4*len(areas))
	for _, a := range areas {
		for _, k := range a.Keywords {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// recommendationsKnowledgeQuery builds the retrieval query string for the
// recommendations prompt by joining insight names. Capped at 200 chars to
// stay within typical embedding model input limits.
func (o *Orchestrator) recommendationsKnowledgeQuery(insights []models.Insight) string {
	if len(insights) == 0 {
		return ""
	}
	names := make([]string, 0, len(insights))
	for _, i := range insights {
		if i.Name != "" {
			names = append(names, i.Name)
		}
	}
	q := "recommendations for: " + strings.Join(names, ", ")
	if len(q) > 200 {
		q = q[:200]
	}
	return q
}
