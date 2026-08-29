package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
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
	"github.com/decisionbox-io/decisionbox/services/agent/internal/mdtext"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
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

// persistTimeout bounds the tail-end durable writes — Save, split
// logs, embed/index — that run after compute has finished.
// Embed/index against a slow embedding API plus Qdrant upsert is
// the heaviest step here; 10 minutes covers it with comfortable
// margin while still failing loudly if Mongo / Qdrant are
// genuinely unreachable.
const persistTimeout = 10 * time.Minute

// completeTimeout bounds the single Mongo UpdateOne that marks the
// run as completed. Kept tiny because the only failure mode that
// matters here is Mongo being genuinely unreachable — and we don't
// want to share persistTimeout with the heavier embed/index step
// (Phase 8 can burn most of that budget on large results, which
// would otherwise leave Complete to run against an expired ctx).
const completeTimeout = 30 * time.Second

// runFinalizer is the subset of *StatusReporter that finalizeStatus
// touches. Defined as an interface so unit tests can inject a fake
// that records which terminal method was called without bringing up
// MongoDB.
type runFinalizer interface {
	Complete(ctx context.Context, discoveryID string, insightsFound int)
	Fail(ctx context.Context, discoveryID, errMsg string)
}

// finalizeStatus stamps the terminal status on the run document. On
// the happy path (computeErr == nil) it marks the run as completed;
// when the compute phase was cancelled mid-way (DISCOVERY_MAX_DURATION
// expired, SIGTERM, etc.) it marks the run as failed and returns the
// underlying error so the caller routes through the failed
// notification path rather than emitting a misleading
// EventDiscoveryCompleted. The partial result has already been saved
// to Mongo by the time this runs, so the dashboard can still surface
// what was discovered before cancellation.
//
// Uses its own ctx derived via WithoutCancel + a short timeout so a
// near-expiry persistCtx never prevents the final status write from
// landing — the run-completion UpdateOne (and the discovery_id
// back-reference Hook 5 in plugin-hooks.md depends on) always lands.
func finalizeStatus(parent context.Context, reporter runFinalizer, computeErr error, result *models.DiscoveryResult, insightCount int) error {
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), completeTimeout)
	defer cancel()

	if computeErr != nil {
		// Pass result.ID so plugin-hooks Hook 5 / the discovery-log
		// APIs can find the partial result the same way they would
		// for a completed run. Empty when Save itself failed and we
		// never got an ID — Fail then records the error only.
		reporter.Fail(completeCtx, result.ID, fmt.Sprintf("discovery cancelled: %v", computeErr))
		return fmt.Errorf("discovery cancelled mid-compute: %w", computeErr)
	}

	reporter.Complete(completeCtx, result.ID, insightCount)
	return nil
}

// persistContext derives the durable-write ctx from the run ctx.
// It deliberately uses WithoutCancel: the compute-phase ctx may be
// near (or past) its DISCOVERY_MAX_DURATION deadline, and the
// caller may have cancelled it (SIGTERM during shutdown). Losing
// the result of a completed discovery to either signal is a
// data-loss bug — the work is done; we owe durability. Values
// from the parent ctx (run identifiers, telemetry context) are
// preserved.
func persistContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), persistTimeout)
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
	aiClient  *ai.Client
	warehouse gowarehouse.Provider

	contextRepo   *database.ContextRepository
	discoveryRepo *database.DiscoveryRepository
	// discoveryLogRepo is held as an interface so unit tests can inject
	// a mock without having to spin up MongoDB. The concrete writer is
	// *database.DiscoveryLogRepository, wired in production by
	// agentserver.go.
	discoveryLogRepo discoveryLogPersister
	feedbackRepo     *database.FeedbackRepository
	debugLogRepo     *database.DebugLogRepository

	explorationEngine *ai.ExplorationEngine

	// validationAgent runs the LLM-native verifier + refuter pair on
	// every insight and recommendation. Constructed inside
	// RunDiscovery once the schema provider + query executor exist.
	// Nil when aiClient is nil; the orchestrator's validation phase
	// stamps Combined=validation_disabled on every doc in that case.
	validationAgent *verifier.Agent
	validationCfg   verifier.Config
	validationCaps  verifier.RunCaps

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

	// llmConfig is the project's LLM provider config (project.LLM.Config).
	// Read for the max_input_tokens / max_output_tokens operator overrides
	// when budgeting the analysis + recommendation output against the model
	// window. May be nil.
	llmConfig gollm.ProviderConfig

	// llmInputWindow / llmOutputCap are the effective context window and
	// output cap resolved once at run start (operator override → live
	// auto-detection → catalog → default). Zero means "resolve from the
	// catalog/override on demand" — the fallback path unit tests take.
	llmInputWindow int
	llmOutputCap   int

	// calibMu guards calibratedWindow, the model's true context window learned
	// from a context-overflow 400 during this run. When set it wins over the
	// resolved window so the remaining areas budget against ground truth.
	calibMu          sync.Mutex
	calibratedWindow int

	// modelWindowRepo persists a context window learned from an overflow 400
	// so a later run for the same project+model budgets correctly up front.
	// Optional — nil skips cross-run persistence (unit tests, and until the
	// repo is wired).
	modelWindowRepo modelWindowPersister

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
	schemaCache   SchemaCache
	warehouseHash string
	warehouseID   string

	// warehouseProviders holds one live warehouse provider per datasource
	// id for a multi-warehouse run (keyed by normalised id, primary
	// included). warehouses carries their configs (datasets, filter, card,
	// per-datasource pack). Both empty / single-entry on a single-warehouse
	// run, where RunDiscovery takes the unchanged primary-only path. See
	// orchestrator_multiwarehouse.go.
	warehouseProviders map[string]gowarehouse.Provider
	warehouses         []models.WarehouseConfig
}

// OrchestratorOptions configures the orchestrator.
type OrchestratorOptions struct {
	AIClient  *ai.Client
	Warehouse gowarehouse.Provider

	ContextRepo   *database.ContextRepository
	DiscoveryRepo *database.DiscoveryRepository
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

	RunRepo *database.RunRepository
	// RunStepRepo persists the per-step rows that used to live as an
	// embedded `steps` array on the discovery_runs document. Required —
	// without it the status reporter has nowhere to write the live step
	// stream and the dashboard's progress feed goes dark.
	RunStepRepo *database.RunStepRepository
	RunID       string

	ProjectID string
	Domain    string
	Category  string
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
	// LLMConfig is the project's LLM provider config (project.LLM.Config),
	// carrying the max_input_tokens / max_output_tokens operator overrides used
	// when budgeting output against the model window. Optional.
	LLMConfig gollm.ProviderConfig
	// LLMInputWindow / LLMOutputCap are the effective context window and output
	// cap resolved at run start (override → live auto-detection → catalog →
	// default). Zero means the orchestrator resolves from the catalog/override
	// on demand.
	LLMInputWindow int
	LLMOutputCap   int
	// ModelWindowRepo persists a context window learned from an overflow 400.
	// Optional — nil skips cross-run persistence.
	ModelWindowRepo   modelWindowPersister
	WarehouseProvider string // provider id used to label warehouse-query debug rows
	EnableDebugLogs   bool

	// Optional — nil if Qdrant/embedding not configured
	VectorStore       vectorstore.Provider
	EmbeddingProvider goembedding.Provider

	// EmbedIndexStore is needed for Phase 8 to write to insights/recommendations collections
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

	// WarehouseID scopes the SchemaCache lookup to one warehouse so a
	// discovery run reads only the catalog of the warehouse it targets.
	// Empty resolves to the project's default/primary warehouse. On a
	// multi-warehouse run this is the PRIMARY id — the default target for a
	// statement that names no datasource_id.
	WarehouseID string

	// WarehouseProviders holds one live warehouse provider per datasource
	// (keyed by warehouse id, primary included) for a multi-warehouse run.
	// Warehouses carries the matching configs. When both have >1 entry the
	// orchestrator runs multi-hop discovery (exploration hops between
	// datasources); otherwise it takes the unchanged single-warehouse path
	// against Warehouse. Optional — nil keeps legacy single-warehouse
	// behaviour.
	WarehouseProviders map[string]gowarehouse.Provider
	Warehouses         []models.WarehouseConfig

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

	// Verifier agent is constructed inside RunDiscovery once the
	// schema provider + query executor exist; the older single-pass
	// validators have been removed.

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
		llmConfig:          opts.LLMConfig,
		llmInputWindow:     opts.LLMInputWindow,
		llmOutputCap:       opts.LLMOutputCap,
		modelWindowRepo:    opts.ModelWindowRepo,
		vectorStore:        opts.VectorStore,
		embeddingProvider:  opts.EmbeddingProvider,
		embedIndexStore:    opts.EmbedIndexStore,
		embedder:           opts.EmbeddingProvider, // same interface, named differently to avoid ambiguity
		schemaRetriever:    opts.SchemaRetriever,
		schemaCache:        opts.SchemaCache,
		warehouseHash:      opts.WarehouseHash,
		warehouseID:        opts.WarehouseID,
		warehouseProviders: opts.WarehouseProviders,
		warehouses:         opts.Warehouses,
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

	// ValidationEnabled is the resolved per-project toggle for the
	// LLM-native verifier + refuter pipeline. The caller resolves
	// project.EffectiveValidationEnabled() into a bool. When false the
	// orchestrator skips constructing the verifier agent for this run —
	// validationPhase falls through to the nil-agent branch, stamping
	// every insight + recommendation with combined=validation_disabled
	// (and backfilling the legacy Status field for old consumers).
	ValidationEnabled bool
}

// RunDiscovery executes the complete discovery process.
func (o *Orchestrator) RunDiscovery(ctx context.Context, opts DiscoveryOptions) (*models.DiscoveryResult, error) {

	// Discovery runs execute as a staged pipeline:
	//   Phase 1  -> Load historical/project context
	//   Phase 2  -> Load cached schemas and prepare prompts
	//   Phase 3  -> Run autonomous exploration against the warehouse
	//   Phase 4  -> Analyze exploration results by business area
	//   Phase 4.5-> Validate generated insights
	//   Phase 5  -> Generate recommendations from validated insights
	//   Phase 5.5-> Validate recommendations
	//   Phase 6  -> Update reusable project context
	//   Phase 7  -> Persist discovery results
	//   Phase 8  -> Embed/index results for retrieval
	// Each phase updates the StatusReporter so the UI can track
	// live discovery progress and intermediate results.

	// Self-calibration: learn the model's true context window from any
	// context-overflow 400 (across every phase that calls the LLM) so the
	// remaining areas re-budget against it and a later run starts correct.
	o.installContextWindowObserver(ctx)

	// Reasoning opt-in: when the operator enabled reasoning (the "Enable
	// reasoning" checkbox, surfaced only for providers that wire native
	// thinking), request it on every LLM call. Default off = today, so big
	// models are byte-identical. Only Ollama acts on it, and only after
	// confirming the model supports thinking (/api/show capability).
	if o.aiClient != nil {
		o.aiClient.SetReasoning(gollm.ReasoningEnabled(o.llmConfig))
	}

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

	// Initialize the LLM-native validation agent.
	// Construction requires aiClient; an absent client → no agent →
	// every doc gets Combined=validation_disabled. We ALSO skip
	// construction when the project's ValidationEnabled toggle is off
	// — same end state, no LLM cost burned. The nil-agent branch in
	// validationPhase.validateInsights / validateRecommendations stamps
	// every doc with combined=validation_disabled + backfills the
	// legacy Status field.
	if o.aiClient != nil && opts.ValidationEnabled {
		o.validationCfg, o.validationCaps = verifier.LoadConfigFromEnv()
		va, vErr := verifier.NewAgent(o.aiClient, o.validationCfg)
		if vErr != nil {
			return nil, fmt.Errorf("init validation agent: %w", vErr)
		}
		o.validationAgent = va
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

	// Previous discovery insights, recommendations, and user feedback are
	// merged into a reusable context block that gets injected into later
	// exploration and analysis prompts. This helps the LLM avoid repeating
	// already-known findings and keeps future discoveries context-aware.
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
	keywords := o.collectAreaKeywords(analysisAreas)

	// Multi-warehouse (multi-hop) discovery: when the run wires more than
	// one datasource, build a per-datasource execution context — one
	// executor per datasource, the merged catalog + table→datasource index,
	// and the descriptors the grouped catalog + prompt render from. The
	// single-warehouse path is unchanged (dc stays nil). See
	// orchestrator_multiwarehouse.go.
	var dc *datasourceContext
	if o.isMultiWarehouse() {
		dc, err = o.buildDatasourceContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("build multi-warehouse context: %w", err)
		}
		// The schema provider + telemetry read the merged cross-warehouse
		// catalog from here on.
		schemas = dc.mergedSchemas
	}

	var rendered *Rendered
	if dc != nil {
		rendered = o.buildGroupedCatalog(dc, keywords)
	} else {
		rendered = (&SchemaContextBuilder{Schemas: schemas}).BuildCatalog(keywords)
	}
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

	// baseContext is reused across exploration, analysis, and
	// recommendation prompts. It combines project profile data,
	// previous discoveries, feedback summaries, language settings,
	// and domain-specific instructions into a single prompt context.
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

	// Multi-warehouse: append the datasource routing contract + each
	// datasource's routing card and its own domain-pack focus areas, so the
	// agent sets datasource_id per statement, hops between datasources, and
	// applies the right playbook per datasource.
	if dc != nil {
		explorationPrompt += buildDatasourcesPromptSection(dc)
	}

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
	//
	// Single-warehouse: scope search_tables to this run's warehouse so a
	// shared Qdrant collection doesn't surface a sibling's tables.
	// Multi-warehouse: search_tables spans ALL datasources (empty filter)
	// so the agent can discover tables in any datasource and hop to them;
	// each hit is attributed to its datasource via TableWarehouse and the
	// index payload, and query_data routes per datasource_id.
	schemaSearchWarehouseID := o.warehouseID
	var tableWarehouse map[string]string
	if dc != nil {
		schemaSearchWarehouseID = ""
		tableWarehouse = dc.tableWarehouse
	}
	schemaProvider, spErr := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID:      o.projectID,
		WarehouseID:    schemaSearchWarehouseID,
		Datasets:       o.datasets,
		Schemas:        schemas,
		TableWarehouse: tableWarehouse,
		Retriever:      o.schemaRetriever,
		Embedder:       o.embedder,
	})
	if spErr != nil {
		// This is a wiring bug — the schema cache lookup above already
		// guarantees Schemas is non-nil. Surface clearly rather than
		// continuing with a nil provider.
		return nil, fmt.Errorf("build schema provider: %w", spErr)
	}

	// schemaProvider is captured here for use by the validationAgent
	// executor constructed below.
	validationSchemaProvider := schemaProvider

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

	// Multi-warehouse: hand the engine one executor per datasource so each
	// query_data step routes to the datasource_id it targets. Single-
	// warehouse leaves dsExecutors nil and the engine falls back to the
	// single Executor keyed by the primary.
	var dsExecutors map[string]*queryexec.QueryExecutor
	if dc != nil {
		dsExecutors = dc.executors
	}

	// Resolve the model window + output cap once for the exploration engine's
	// reasoning-aware output budget (R3). At exploration time no overflow has
	// been observed yet, so this is the run-start resolved window (operator
	// override → live auto-detect → catalog → default). Only consulted on the
	// reasoning-effective path — a non-reasoning model keeps today's fixed
	// exploration ceiling regardless of these values.
	exploreWindow, exploreOutputCap := o.resolveModelBudget()

	o.explorationEngine = ai.NewExplorationEngine(ai.ExplorationEngineOptions{
		Client:            o.aiClient,
		Executor:          executor,
		Executors:         dsExecutors,
		PrimaryDatasource: normDatasourceID(o.warehouseID),
		TableDatasource:   tableWarehouse,
		MaxSteps:          opts.MaxSteps,
		MinSteps:          opts.MinSteps,
		Dataset:           datasetsStr,
		SchemaProvider:    schemaProvider,
		StepIndexer:       stepIndexer,

		// R3: reasoning-aware, window-budgeted per-step output ceiling.
		Window:             exploreWindow,
		OutputCap:          exploreOutputCap,
		ReasoningEffective: o.effectiveReasoning(),
		// Stream every exploration step to the StatusReporter so the live
		// dashboard can track exploration progress, executed queries,
		// token usage, query fixes, and failures in real time.
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string, inputTokens, outputTokens int, warehouseID string) {
			o.statusReporter.AddExplorationStep(ctx, stepNum, action, thinking, query, rowCount, queryTimeMs, queryFixed, errMsg, inputTokens, outputTokens, warehouseID)
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
	// non-English / abbreviated columns (customer ticket 2026-04-30).
	// The validation agent now reads explorationResult.Steps directly via
	// the executor; the previous local rebinding here was a refactor
	// leftover (assigned but never used after the scope closed).

	// Phase 4: Analysis by area (dynamic from domain pack)
	// Filter areas if selective run requested
	runAreas := analysisAreas
	runType := "full"
	// If the run requests specific analysis areas, limit execution to
	// only those areas instead of running the full domain pack.
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

	// Build the validation phase helper once per run. The step index
	// is built once now and mutated as exploration proceeds (the
	// orchestrator's explorationResult.Steps slice is the live source
	// the executor reads at validation time).
	stepByID := buildStepIndex(explorationResult.Steps)
	valPhase := &validationPhase{
		agent: o.validationAgent,
		cfg:   o.validationCfg,
		caps:  o.validationCaps,
		wh: verifier.WarehouseInfo{
			Dialect:     o.warehouse.SQLDialect(),
			Dataset:     o.warehouse.GetDataset(),
			FilterField: o.filterField,
			FilterValue: o.filterValue,
		},
		disc: verifier.DiscoveryContext{
			ProjectID: o.projectID,
			RunID:     o.runID,
			Domain:    o.domain,
			Language:  o.language,
		},
		executor: &verifier.DefaultExecutor{
			SchemaProvider:      validationSchemaProvider,
			QueryExec:           executor,
			StepByID:            stepByID,
			Cfg:                 o.validationCfg.Bundle,
			MaxReadStepRowsCall: o.validationCaps.MaxReadStepRowsCall,
		},
		primaryDS: normDatasourceID(o.warehouseID),
	}
	// Multi-warehouse: verify each insight / recommendation against the
	// datasource it is about, not always the primary (a secondary-derived
	// insight validated on the primary fails / mis-verifies).
	valPhase.whByDS, valPhase.executorByDS = o.buildValidationRouting(dc, validationSchemaProvider, stepByID, o.validationCfg, o.validationCaps)
	insightsValidatedThisRun := 0

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

		// Couple the picker's query-results budget to the model window so an
		// area's input can never grow past what the window can hold once the
		// output cap + safety margin are reserved. Recomputed per area so a
		// window calibrated mid-run (from an overflow 400) tightens the
		// remaining areas too.
		areaWindow, areaOutputCap := o.resolveModelBudget()
		picker.BudgetTokens = analysisPickerBudgetTokens(AnalysisQueryResultsBudgetTokens, areaWindow, areaOutputCap)

		pickResult, pickErr := picker.Pick(ctx, area, explorationResult.Steps)
		// Skip the area if step retrieval fails so the remaining areas
		// can continue processing.
		if pickErr != nil {
			applog.WithFields(applog.Fields{"area": area.ID, "error": pickErr.Error()}).Warn("Step picker failed; skipping area")
			continue
		}

		// Track analysis-phase metrics so the live status UI can show
		// step retrieval activity and dropped-step counts.
		o.statusReporter.IncrementAnalysisCounter(ctx, "step_index_search_calls", 1)
		if len(pickResult.Dropped) > 0 {
			o.statusReporter.IncrementAnalysisCounter(ctx, "steps_dropped", len(pickResult.Dropped))
		}

		// Skip the area when no relevant exploration steps were found.
		// This avoids sending empty or low-signal prompts to the LLM.
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
			"area":          area.ID,
			"area_name":     area.Name,
			"picked_count":  len(relevantSteps),
			"dropped_count": len(pickResult.Dropped),
			"picked":        pickedSummary,
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
			AreaID:            area.ID,
			AreaName:          area.Name,
			RunAt:             time.Now(),
			Prompt:            prompt,
			RelevantQueries:   len(relevantSteps),
			QueryResultsChars: len(queryResultsJSON),
			SelectedSteps:     pickedToTelemetry(pickResult.Picked),
			DroppedSteps:      droppedToTelemetry(pickResult.Dropped),
		}

		// Budget the requested output against the measured input so
		// input + output stays inside the model window (issue #347). The
		// adaptive context-overflow retry in the ai client is the net if the
		// estimate still overshoots on an uncatalogued model.
		inputTokens := approxTokens(ctx, prompt)
		maxTokens := budgetedMaxOutputTokens(areaWindow, inputTokens, areaOutputCap, analysisMinOutputTokens())
		applog.WithFields(applog.Fields{
			"area":         area.ID,
			"window":       areaWindow,
			"input_tokens": inputTokens,
			"output_cap":   areaOutputCap,
			"max_tokens":   maxTokens,
		}).Debug("Analysis area: budgeted output tokens")

		// Send the selected exploration evidence to the LLM and generate
		// structured insights for the current analysis area. The first call is
		// a plain Chat (byte-identical for big models); the self-heal retry
		// fires only when it yields zero parseable insights from a response that
		// wasn't a legitimately empty area (see analyzeAreaInsights).
		outcome := o.analyzeAreaInsights(ctx, area.ID, prompt, maxTokens)
		step.Response = outcome.response
		step.TokensIn = outcome.tokensIn
		step.TokensOut = outcome.tokensOut
		step.DurationMs = outcome.durationMs
		step.AnalysisParseRetries = outcome.parseRetries
		step.InsightsDroppedParse = outcome.droppedParse

		if outcome.chatErr != nil {
			step.Error = outcome.chatErr.Error()
			analysisLog = append(analysisLog, step)
			applog.WithFields(applog.Fields{"area": area.ID, "error": outcome.chatErr.Error()}).Warn("Analysis failed")
			continue
		}

		if len(outcome.insights) == 0 && outcome.parseErr != nil {
			step.Error = fmt.Sprintf("parse error: %s", outcome.parseErr.Error())
			analysisLog = append(analysisLog, step)
			applog.WithFields(applog.Fields{"area": area.ID, "error": outcome.parseErr.Error()}).Warn("Failed to parse insights")
			continue
		}

		insights := outcome.insights
		step.Insights = insights

		// Phase 4.5: Validate insights via the LLM-native verifier
		// agent. Walks insights in affected_count-desc order,
		// applies the per-run cap (insightsValidatedThisRun is threaded
		// across areas), and stamps Validation on each insight in
		// place. Returns one ValidationResult per insight.

		// Skip validation when the analysis step produced no insights.
		// The verifier only runs for successfully parsed insights.
		if len(insights) > 0 {
			var areaResults []models.ValidationResult
			areaResults, insightsValidatedThisRun = valPhase.validateInsights(ctx, insights, stepByID, area.ID, insightsValidatedThisRun)
			step.ValidationResults = areaResults
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

	// Phase 5: Generate recommendations — only insights with
	// Combined∈{supported,confirmed} flow to the recommender.
	applog.Info("Phase 5: Generating recommendations")
	o.statusReporter.SetPhase(ctx, models.PhaseRecommendations, "Generating actionable recommendations...", 85)
	recommenderInput := filterEligibleInsights(allInsights)
	applog.WithFields(applog.Fields{
		"total_insights":    len(allInsights),
		"eligible_insights": len(recommenderInput),
	}).Info("Filtered insights for recommendation generation")

	var recommendations []models.Recommendation
	var recStep *models.RecommendationStep

	// Skip recommendation generation when no insights passed validation.
	// Recommendations are only created from validated insights.
	if len(recommenderInput) == 0 {
		applog.Warn("No eligible insights for recommendation generation; skipping")
		recommendations = []models.Recommendation{}
		recStep = &models.RecommendationStep{Status: "skipped_no_eligible_insights", RunAt: time.Now(), InsightCount: 0}
	} else {
		recommendations, recStep = o.generateRecommendations(ctx, prompts.Recommendations, recommenderInput, baseContext, datasetsStr)
		var dropStats RecommendationDropStats
		recommendations, dropStats = validateRelatedInsightIDs(recommendations, recommenderInput)
		applyRecommendationDropStats(recStep, recommendations, dropStats)
	}

	// Emit a per-call RunStep so the live UI carries the recommendation
	// LLM call's tokens alongside exploration/analysis steps. recStep
	// is non-nil when the recommendation phase ran at all —
	// generateRecommendations always returns a step (even on
	// parse/LLM failure it stamps Error). recStep.RecommendationsDropped
	// surfaces server-side filtering (e.g. recs whose related_insight_ids
	// were hallucinated as slugs by the LLM) so the dashboard can render
	// a "N dropped due to invalid related_insight_ids" hint instead of
	// silently showing fewer recs than the model emitted.
	if recStep != nil {
		// The live RunStep message attributes drops to invalid
		// related_insight_ids, so pass only those two reasons here — parse
		// drops are surfaced separately via the recommendation_parse_error
		// status and the recommendations_dropped_parse counter.
		relatedIDDrops := recStep.RecommendationsDroppedMissingIDs + recStep.RecommendationsDroppedUnknownID
		o.statusReporter.AddRecommendationStep(ctx, len(recommendations), relatedIDDrops, recStep.Error, recStep.TokensIn, recStep.TokensOut)
	}

	// Phase 5.5: Validate recommendations via the LLM-native verifier
	// agent. Bundle's source-steps are the token-budgeted union of
	// related insights' source_steps; the agent runs verifier +
	// (optionally) refuter and stamps Validation on each rec in
	// place.
	var recValidationResults []models.ValidationResult

	// Skip recommendation validation if no recommendations were generated.
	// Validation is only meaningful when there are recommendations to verify.
	if len(recommendations) > 0 {
		recValidationResults = valPhase.validateRecommendations(ctx, recommendations, allInsights, stepByID)
	}

	// Phase 6: Update project context with discovered patterns
	// This phase persists learning from the current run so future discoveries
	// can use historical context (e.g., recurring patterns, trends, and signals).
	applog.Info("Phase 6: Updating project context")

	// Mark that a successful discovery run occurred for this project
	projectCtx.RecordDiscovery(true)

	// Merge newly discovered insights into long-term pattern memory
	projectCtx.UpdatePatterns(allInsights)

	// Persist updated context (best-effort; failures are non-fatal)
	if err := o.saveProjectContext(ctx, projectCtx); err != nil {
		applog.WithError(err).Warn("Failed to save project context")
	}

	// Phase 7: Save discovery result
	applog.Info("Phase 7: Saving discovery result")

	// Update UI to show that we are now saving final results
	o.statusReporter.SetPhase(ctx, models.PhaseSaving, "Saving discovery results...", 95)

	// Combine validation results from all analysis areas and recommendation phase
	allValidation := make([]models.ValidationResult, 0)
	for _, step := range analysisLog {
		allValidation = append(allValidation, step.ValidationResults...)
	}
	allValidation = append(allValidation, recValidationResults...)

	// Decide final run status based on whether any or all analysis areas failed
	if failedAreas > 0 && failedAreas == len(runAreas) {
		// All areas failed — mark as failed run
		runType = "failed"
	} else if failedAreas > 0 && runType != "partial" {
		// Some areas failed — mark as partial
		runType = "partial"
	}

	// Create final result object that will be saved and returned
	// Contains all insights, recommendations, and execution metadata
	result := &models.DiscoveryResult{
		ProjectID:       o.projectID,
		WarehouseID:     o.warehouseID,
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

	// Capture compute-phase ctx error BEFORE switching to the
	// dedicated persistence ctx. If DISCOVERY_MAX_DURATION (or any
	// other parent ctx cancellation) expired during analysis /
	// recommendations, the LLM calls returned context.DeadlineExceeded
	// and those errors are already recorded in the result. We still
	// owe durability for the partial result — the user should see
	// what was discovered before the cap was hit — but the run is
	// NOT a success: we Fail it (not Complete it) and propagate the
	// error so the caller fires the EventDiscoveryFailed notification
	// rather than EventDiscoveryCompleted.

	// Check if discovery was cancelled or timed out during execution
	computeErr := ctx.Err()

	// Persistence runs under a dedicated budget — see persistContext
	// for why it is independent of the compute-phase ctx. The
	// embed/index phase dominates this budget on large results
	// (embedding-API calls plus Qdrant upsert).

	// Create separate context for persistence so saving is not affected by compute timeout
	persistCtx, persistCancel := persistContext(ctx)
	defer persistCancel()

	// Save final discovery result to repository/database
	if err := o.discoveryRepo.Save(persistCtx, result); err != nil {
		return nil, err
	}

	// Store detailed execution logs (exploration, analysis, validation) for debugging
	o.persistSplitLogs(persistCtx, result.ID, explorationResult.Steps, analysisLog, allValidation, recStep)

	// Phase 8: Embed & Index (non-fatal — errors logged, discovery still completes).
	// Skipped when compute was cancelled — embedding & dedup against
	// partial / inconsistent data is wasteful and the indexes would
	// be stale for the next run anyway. Runs BEFORE Complete on the
	// happy path because Phase 8's first call is
	// statusReporter.SetPhase(PhaseEmbedIndex, …, 97) which writes
	// RunStatusRunning. If Complete ran first, SetPhase would silently
	// downgrade the just-completed run back to "running" with no
	// follow-up Complete, leaving every successful discovery stuck
	// non-terminal.
	if computeErr == nil && o.embedIndexStore != nil {

		// Run embedding + indexing step to store discovery results for future retrieval/search
		o.runPhaseEmbedIndex(persistCtx, result)
	}

	// Final step: update run status (success or failure) based on execution result
	if err := finalizeStatus(ctx, o.statusReporter, computeErr, result, len(allInsights)); err != nil {
		return result, err
	}

	applog.WithFields(applog.Fields{
		"project_id":      o.projectID,
		"insights":        len(allInsights),
		"recommendations": len(recommendations),
		"validations":     len(allValidation),
		"duration":        time.Since(startTime).String(),
	}).Info("Discovery run completed")

	// Return final discovery result to caller
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
	// Tag each exploration step with the datasource this discovery ran against
	// (multi-warehouse). A discovery runs against exactly one warehouse, so every
	// step shares o.warehouseID; downstream SQL-example validation / fine-tuning
	// uses the tag to route each statement to the right datasource. Only set it
	// when unset so a future per-step source isn't clobbered.
	for i := range explorationSteps {
		if explorationSteps[i].WarehouseID == "" {
			explorationSteps[i].WarehouseID = o.warehouseID
		}
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
// insightsForRecommenderPrompt returns a copy of insights with the Markdown
// rendition (DescriptionMd) cleared. The recommender reads the plain
// `description`; carrying description_md into INSIGHTS_DATA would put a second
// full copy of every insight's description in the prompt, roughly doubling the
// per-insight description tokens and risking the context/budget cap. The
// originals (which still need DescriptionMd for storage and rendering) are
// left untouched.
func insightsForRecommenderPrompt(insights []models.Insight) []models.Insight {
	out := make([]models.Insight, len(insights))
	copy(out, insights)
	for i := range out {
		out[i].DescriptionMd = ""
	}
	return out
}

// splitMarkdownDescription reduces an authored Markdown description to plain
// text and returns (plain, md). md is empty when the input carried no
// formatting (plain == reduction), so unformatted descriptions and legacy
// data keep a single field and the dashboard falls back to `description`.
func splitMarkdownDescription(authored string) (plain, md string) {
	plain = mdtext.ToPlainText(authored)
	if plain != authored {
		md = authored
	}
	return plain, md
}

// parseInsights decodes the analysis response tolerantly and per-item. It
// accepts either the {"insights": [...]} envelope or a bare top-level array
// (some models emit the array directly — both shapes appear in
// discovery_analysis_steps). Each element is decoded on its own through the
// tolerant models.Insight.UnmarshalJSON, so a single malformed insight is
// skipped and counted rather than discarding the whole area (the insight
// analogue of the recommendation loss fixed in issue #342). Mirrors
// parseRecommendations.
//
// Returns the kept insights, the number of per-item drops, and a non-nil error
// only when the response is not recognizable as insights at all (neither the
// envelope nor a bare array). A well-formed but empty result returns
// (empty, 0, nil) so callers can distinguish "no insights" from "could not
// parse".
func (o *Orchestrator) parseInsights(response string, areaID string) ([]models.Insight, int, error) {
	cleaned := cleanJSONResponse(response)

	var raws []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(cleaned), "[") {
		// Bare top-level array (some models emit the array directly).
		if err := json.Unmarshal([]byte(cleaned), &raws); err != nil {
			return nil, 0, err
		}
	} else {
		// Envelope object: the "insights" key must be present. An object with a
		// different key is a parse failure, not a legitimately empty result —
		// otherwise it would silently yield 0 insights with no retry.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(cleaned), &envelope); err != nil {
			return nil, 0, fmt.Errorf("failed to parse analysis response: %w", err)
		}
		// Match the key case-insensitively, as encoding/json does when decoding
		// into a struct tag — some models capitalize it (`{"Insights":[…]}`).
		var insRaw json.RawMessage
		found := false
		for k, v := range envelope {
			if strings.EqualFold(k, "insights") {
				insRaw, found = v, true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf(`response is missing the "insights" key`)
		}
		if strings.TrimSpace(string(insRaw)) == "null" {
			// A null array decodes into a nil slice without error; treat it as a
			// parse failure (→ retry) rather than a silent empty result.
			return nil, 0, fmt.Errorf(`"insights" is null`)
		}
		if err := json.Unmarshal(insRaw, &raws); err != nil {
			return nil, 0, fmt.Errorf(`"insights" is not an array: %w`, err)
		}
	}

	insights := make([]models.Insight, 0, len(raws))
	dropped := 0
	for i, raw := range raws {
		var insight models.Insight
		if err := json.Unmarshal(raw, &insight); err != nil {
			dropped++
			applog.WithFields(applog.Fields{
				"area":   areaID,
				"index":  i,
				"reason": err.Error(),
			}).Warn("Dropping unparseable insight; keeping the rest of the area")
			continue
		}

		insight.AnalysisArea = areaID
		if insight.DiscoveredAt.IsZero() {
			insight.DiscoveredAt = time.Now()
		}
		// Split the authored description: the LLM writes Markdown into
		// `description`; keep that rendition in DescriptionMd for the
		// dashboard, and reduce Description to the plain-text form that API
		// consumers, previews, and embeddings read.
		insight.Description, insight.DescriptionMd =
			splitMarkdownDescription(insight.Description)
		// Assign a UUID if the LLM didn't give one. The same UUID is later
		// reused as the standalone `insights._id` and the Qdrant point id, so
		// every link built from a search hit (Ask sources, related cards) can
		// use the existing /discoveries/{did}/insights/{id} route without any
		// client-side fallback. Qdrant only accepts UUID / uint64 point ids,
		// which is why the embedded id itself has to be a UUID.
		if insight.ID == "" {
			insight.ID = uuid.New().String()
		}

		insights = append(insights, insight)
	}

	return insights, dropped, nil
}

// analysisParseOutcome carries the result of one analysis area's chat +
// per-item parse + self-heal loop, so the loop can be unit-tested in isolation
// (mirrors how generateRecommendations is testable). parseErr is set only when
// the area produced zero insights because of a parse failure (not a
// legitimately empty area); chatErr is a transport/model error that skips the
// area outright.
type analysisParseOutcome struct {
	insights     []models.Insight
	response     string
	tokensIn     int
	tokensOut    int
	durationMs   int64
	parseRetries int
	droppedParse int
	chatErr      error
	parseErr     error
}

// analyzeAreaInsights runs one analysis area: a first plain Chat (no structured
// output, so big models that already work stay byte-identical), then — only if
// that first response yields zero parseable insights from a response that
// wasn't a legitimately empty area — up to ANALYSIS_PARSE_MAX_RETRIES
// corrective re-prompts. The retry appends analysisRepairSuffix and uses
// ChatWithFormat with a schema-constrained insight envelope, which self-gates
// on SupportsStructuredOutput (a safe no-op on providers without it, where the
// tolerant per-item parser remains the net). Mirrors generateRecommendations'
// self-heal loop.
func (o *Orchestrator) analyzeAreaInsights(ctx context.Context, areaID, prompt string, maxTokens int) analysisParseOutcome {
	maxRetries := goconfig.GetEnvAsInt(analysisParseMaxRetriesEnv, defaultAnalysisParseMaxRetries)
	if maxRetries < 0 {
		maxRetries = 0
	}
	insightFormat := insightResponseFormat()

	var out analysisParseOutcome
	var lastParseErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var (
			chatResult *ai.ChatResult
			err        error
		)
		if attempt == 0 {
			chatResult, err = o.aiClient.Chat(ctx, prompt, "", maxTokens)
		} else {
			out.parseRetries = attempt
			attemptPrompt := prompt + analysisRepairSuffix(lastParseErr)
			applog.WithFields(applog.Fields{
				"area":    areaID,
				"attempt": attempt,
				"reason":  errString(lastParseErr),
			}).Warn("Re-prompting for insights after unparseable response")
			chatResult, err = o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, insightFormat)
		}
		if err != nil {
			out.chatErr = err
			return out
		}

		out.response = chatResult.Content
		if attempt == 0 {
			out.tokensIn = chatResult.TokensIn
			out.tokensOut = chatResult.TokensOut
			out.durationMs = chatResult.DurationMs
		} else {
			out.tokensIn += chatResult.TokensIn
			out.tokensOut += chatResult.TokensOut
			out.durationMs += chatResult.DurationMs
		}

		parsed, dropped, perr := o.parseInsights(chatResult.Content, areaID)
		if perr == nil {
			out.droppedParse = dropped
			if len(parsed) > 0 || dropped == 0 {
				// >=1 kept, or a legitimately empty area — accept either.
				out.insights = parsed
				return out
			}
			lastParseErr = fmt.Errorf("all %d insight(s) failed to parse", dropped)
		} else {
			lastParseErr = perr
		}
	}

	out.parseErr = lastParseErr
	return out
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

	insightsJSON, _ := json.MarshalIndent(insightsForRecommenderPrompt(insights), "", "  ")

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

	// Budget the recommendation output against the measured input (same
	// root cause as the analysis phase, issue #347): a large insights-JSON
	// prompt plus a fixed output cap can overflow the model window. The
	// adaptive context-overflow retry is the net.
	recWindow, recOutputCap := o.resolveModelBudget()
	maxTokens := budgetedMaxOutputTokens(recWindow, approxTokens(ctx, prompt), recOutputCap, analysisMinOutputTokens())

	// Layer 1 (prevention): constrain the recommendation shape at generation
	// time where the provider supports it (issue #342). ChatWithFormat
	// self-gates on SupportsStructuredOutput, so this is a safe no-op on
	// providers without it (vertex-ai, azure-foundry) — the tolerant parser
	// below is the always-on net.
	format := recommendationResponseFormat()
	if o.aiClient.SupportsStructuredOutput() {
		applog.Info("Recommendation generation using schema-constrained output")
	}

	// Layer 3 (self-heal): if a response yields zero parseable recommendations,
	// re-ask with the specific reason before giving up. Mirrors the exploration
	// engine's runStepWithRetry. Fires only on a parse failure, never on a
	// legitimately empty result. Bounded and env-overridable (Rule 2).
	maxRetries := goconfig.GetEnvAsInt(recommendationParseMaxRetriesEnv, defaultRecommendationParseMaxRetries)
	if maxRetries < 0 {
		maxRetries = 0
	}

	var (
		recs         []models.Recommendation
		parseDropped int
		lastParseErr error
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptPrompt := prompt
		if attempt > 0 {
			step.RecommendationParseRetries = attempt
			attemptPrompt = prompt + recommendationRepairSuffix(lastParseErr)
			applog.WithFields(applog.Fields{
				"attempt": attempt,
				"reason":  errString(lastParseErr),
			}).Warn("Re-prompting for recommendations after unparseable response")
		}

		chatResult, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			step.Error = err.Error()
			applog.WithError(err).Warn("Failed to generate recommendations")
			return make([]models.Recommendation, 0), step
		}

		step.Response = chatResult.Content
		step.TokensIn += chatResult.TokensIn
		step.TokensOut += chatResult.TokensOut
		step.DurationMs += chatResult.DurationMs

		parsed, dropped, perr := parseRecommendations(chatResult.Content)
		if perr == nil {
			// The response was a recognizable envelope/array; record how many
			// items it dropped even when every one failed, so the telemetry
			// is stamped on the parse-error path below too.
			parseDropped = dropped
			if len(parsed) > 0 || dropped == 0 {
				// >=1 kept, or a legitimately empty result — accept either.
				recs, lastParseErr = parsed, nil
				break
			}
			lastParseErr = fmt.Errorf("all %d recommendation(s) failed to parse", dropped)
		} else {
			lastParseErr = perr
		}
	}

	// Layer 4 (surface): a zero-recommendations result caused by a parse
	// failure is stamped with a status + error the dashboard renders, instead
	// of silently showing an empty section (issue #342).
	if len(recs) == 0 && lastParseErr != nil {
		step.RecommendationsDroppedParse = parseDropped
		step.Error = fmt.Sprintf("parse error: %s", lastParseErr.Error())
		step.Status = statusRecommendationParseError
		applog.WithError(lastParseErr).Warn("Failed to parse recommendations")
		return make([]models.Recommendation, 0), step
	}

	for i := range recs {
		if recs[i].CreatedAt.IsZero() {
			recs[i].CreatedAt = time.Now()
		}
		// Same description split as insights: Markdown rendition into
		// DescriptionMd, plain reduction into Description.
		recs[i].Description, recs[i].DescriptionMd = splitMarkdownDescription(recs[i].Description)
		// Assign a UUID if the LLM didn't give one. Same rationale as for
		// insights: the UUID is reused as the standalone `recommendations._id`
		// and Qdrant point id, so URLs that hit the embedded array match
		// without a fallback. Prior to this, the embedded `id` was "", which
		// meant list → detail navigation was silently falling back to the
		// array index and Ask source links to recommendations never worked.
		if recs[i].ID == "" {
			recs[i].ID = uuid.New().String()
		}
	}

	step.RecommendationsDroppedParse = parseDropped
	step.Recommendations = recs
	return recs, step
}

// Analysis-parse tunables (small/open-model insight recovery). Mirror the
// recommendation-parse knobs below; the first analysis call stays a plain Chat
// (byte-identical for big models), and these govern only the corrective retry.
const (
	// defaultAnalysisParseMaxRetries bounds how many corrective re-prompts an
	// analysis area issues when a non-empty response yields zero parseable
	// insights. One extra shot by default — mirrors the recommendation phase.
	// A legitimately empty area (the model found nothing) never retries.
	defaultAnalysisParseMaxRetries = 1

	// analysisParseMaxRetriesEnv overrides defaultAnalysisParseMaxRetries.
	analysisParseMaxRetriesEnv = "ANALYSIS_PARSE_MAX_RETRIES"
)

// Recommendation-parse tunables (issue #342).
const (
	// statusRecommendationParseError marks a RecommendationStep whose phase
	// produced zero recommendations because the LLM response could not be
	// parsed. The dashboard renders it as the reason for the empty section.
	statusRecommendationParseError = "recommendation_parse_error"

	// defaultRecommendationParseMaxRetries bounds how many corrective
	// re-prompts the recommendation phase issues when a response yields zero
	// parseable recommendations. One extra shot by default — the recommendation
	// call is a single large-output batch, so extra rounds are expensive.
	// Lower than exploration's maxParseRetries for that reason.
	defaultRecommendationParseMaxRetries = 1

	// recommendationParseMaxRetriesEnv overrides defaultRecommendationParseMaxRetries.
	recommendationParseMaxRetriesEnv = "RECOMMENDATION_PARSE_MAX_RETRIES"
)

// parseRecommendations decodes the recommendation response tolerantly and
// per-item. It accepts either the {"recommendations": [...]} envelope or a bare
// top-level array (some models emit the array directly — both shapes are
// present in discovery_recommendation_log). Each element is decoded on its own,
// so a single malformed recommendation is skipped and counted rather than
// discarding the whole batch (issue #342 — combined with the tolerant
// Impact.UnmarshalJSON, a prose expected_impact is coerced, not dropped).
//
// Returns the kept recommendations, the number of per-item drops, and a non-nil
// error only when the response is not recognizable as recommendations at all
// (neither the envelope nor a bare array). A well-formed but empty result
// returns (empty, 0, nil) so callers can distinguish "no recommendations" from
// "could not parse".
func parseRecommendations(response string) ([]models.Recommendation, int, error) {
	cleaned := cleanJSONResponse(response)

	var raws []json.RawMessage
	if strings.HasPrefix(strings.TrimSpace(cleaned), "[") {
		// Bare top-level array (some models emit the array directly).
		if err := json.Unmarshal([]byte(cleaned), &raws); err != nil {
			return nil, 0, err
		}
	} else {
		// Envelope object: the "recommendations" key must be present. An object
		// with a different key (e.g. {"recommendation":[…]} or {"items":[…]})
		// is a parse failure, not a legitimately empty result — otherwise it
		// would silently yield 0 recommendations with no retry (issue #342).
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(cleaned), &envelope); err != nil {
			return nil, 0, err
		}
		// Match the key case-insensitively, as encoding/json does when
		// decoding into a struct tag — some models capitalize it
		// (`{"Recommendations":[…]}`).
		var recRaw json.RawMessage
		found := false
		for k, v := range envelope {
			if strings.EqualFold(k, "recommendations") {
				recRaw, found = v, true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf(`response is missing the "recommendations" key`)
		}
		if strings.TrimSpace(string(recRaw)) == "null" {
			// A null array decodes into a nil slice without error; treat it as a
			// parse failure (→ retry) rather than a silent empty result.
			return nil, 0, fmt.Errorf(`"recommendations" is null`)
		}
		if err := json.Unmarshal(recRaw, &raws); err != nil {
			return nil, 0, fmt.Errorf(`"recommendations" is not an array: %w`, err)
		}
	}

	recs := make([]models.Recommendation, 0, len(raws))
	dropped := 0
	for i, raw := range raws {
		var rec models.Recommendation
		if err := json.Unmarshal(raw, &rec); err != nil {
			dropped++
			applog.WithFields(applog.Fields{
				"index":  i,
				"reason": err.Error(),
			}).Warn("Dropping unparseable recommendation; keeping the rest of the batch")
			continue
		}
		recs = append(recs, rec)
	}
	return recs, dropped, nil
}

// recommendationRepairSuffix builds the reason-aware corrective instruction
// appended to the prompt on a re-prompt. It names the specific failure and
// pins the two shapes that trip the parser most (a prose expected_impact and a
// bare top-level array).
func recommendationRepairSuffix(err error) string {
	reason := "it could not be parsed as JSON"
	if err != nil {
		reason = err.Error()
	}
	return "\n\nYour previous response could not be used: " + reason + ".\n" +
		`Respond with ONLY a single JSON object of the form {"recommendations": [ ... ]} ` +
		"— no prose, no markdown fences, and not a bare top-level array. Each " +
		"recommendation's `expected_impact` MUST be an object with `metric`, " +
		"`estimated_improvement`, and `reasoning` string fields, never a bare string."
}

// analysisRepairSuffix builds the reason-aware corrective instruction appended
// to an analysis-area prompt on a re-prompt. It names the specific failure and
// pins the two shapes that trip the parser most (a bare top-level array and
// off-typed scalar fields the tolerant decoder still could not salvage).
func analysisRepairSuffix(err error) string {
	reason := "it could not be parsed as JSON"
	if err != nil {
		reason = err.Error()
	}
	return "\n\nYour previous response could not be used: " + reason + ".\n" +
		`Respond with ONLY a single JSON object of the form {"insights": [ ... ]} ` +
		"— no prose, no markdown fences, and not a bare top-level array. In each " +
		"insight, `affected_count` MUST be a plain integer, `risk_score` and " +
		"`confidence` plain numbers between 0 and 1, `source_steps` an array of " +
		"integers, and `indicators` an array of strings."
}

// errString renders an error for structured logging, empty when nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// --- Helper methods ---

// resolvePrompts extracts prompts and analysis areas from project configuration.
// All prompts are fully seeded at project creation from the domain pack.
func (o *Orchestrator) resolvePrompts() (ResolvedPrompts, []AnalysisArea) {
	if o.projectPrompts == nil {
		return ResolvedPrompts{}, nil
	}

	// Build a unified prompt structure used by LLM across all phases
	resolved := ResolvedPrompts{
		Exploration:     o.projectPrompts.Exploration,
		Recommendations: o.projectPrompts.Recommendations,
		BaseContext:     o.projectPrompts.BaseContext,
		AnalysisAreas:   make(map[string]string),
	}

	var areas []AnalysisArea
	// Convert enabled analysis areas into runtime execution format used by analysis engine
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

// buildFilterContext returns a natural-language instruction injected into LLM prompts
// to ensure all generated queries respect the required dataset filter constraint.
func (o *Orchestrator) buildFilterContext() string {
	if o.filterField == "" {
		return ""
	}
	return fmt.Sprintf("**Filter**: All queries must include `%s = '%s'`", o.filterField, o.filterValue)
}

// buildFilterRule returns a strict LLM instruction enforcing query-level filtering rules.
// This ensures generated SQL always respects project data isolation constraints.
func (o *Orchestrator) buildFilterRule() string {
	if o.filterField == "" {
		return "**No filter required**: This dataset contains only this project's data."
	}
	return fmt.Sprintf("**ALWAYS filter by %s**: `WHERE %s = '%s'`", o.filterField, o.filterField, o.filterValue)
}

// buildAnalysisAreasDescription converts analysis areas into a numbered markdown list
// used in LLM prompts to describe available analysis categories.
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
	schemas, err := o.schemaCache.Find(ctx, o.projectID, o.warehouseID, o.warehouseHash)
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
	knowledgeTopKExploration      = 3
	knowledgeTopKAnalysis         = 5
	knowledgeTopKRecommendations  = 8
	knowledgeMinScore             = 0.4
	knowledgeMaxRetrievalPerPhase = 3 * time.Second
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
			"project_id":       o.projectID,
			"context_provider": name,
			"error":            err.Error(),
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
