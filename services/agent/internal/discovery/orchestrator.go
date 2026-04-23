package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/debug"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation"
)

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

	contextRepo   *database.ContextRepository
	discoveryRepo *database.DiscoveryRepository
	feedbackRepo  *database.FeedbackRepository
	debugLogRepo  *database.DebugLogRepository

	schemaDiscovery      *SchemaDiscovery
	explorationEngine    *ai.ExplorationEngine
	userCountValidator   *validation.UserCountValidator
	insightValidator     *validation.InsightValidator

	debugLogger    *debug.Logger
	statusReporter *StatusReporter

	projectID      string
	domain         string
	category       string
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
}

// OrchestratorOptions configures the orchestrator.
type OrchestratorOptions struct {
	AIClient      *ai.Client
	Warehouse     gowarehouse.Provider

	ContextRepo   *database.ContextRepository
	DiscoveryRepo *database.DiscoveryRepository
	FeedbackRepo  *database.FeedbackRepository
	DebugLogRepo  *database.DebugLogRepository

	RunRepo         *database.RunRepository
	RunID           string

	ProjectID       string
	Domain          string
	Category        string
	Profile         map[string]interface{}
	ProjectPrompts  *models.ProjectPrompts
	Datasets        []string
	FilterField     string
	FilterValue     string
	LLMProvider     string
	LLMModel        string
	EnableDebugLogs bool

	// Optional — nil if Qdrant/embedding not configured
	VectorStore       vectorstore.Provider
	EmbeddingProvider goembedding.Provider

	// EmbedIndexStore is needed for Phase 9 to write to insights/recommendations collections
	EmbedIndexStore EmbedIndexStore
}

// NewOrchestrator creates a new discovery orchestrator.
func NewOrchestrator(opts OrchestratorOptions) *Orchestrator {
	var debugLogger *debug.Logger
	if opts.DebugLogRepo != nil {
		debugLogger = debug.NewLogger(debug.LoggerOptions{
			Repo:           opts.DebugLogRepo,
			AppID:          opts.ProjectID,
			Enabled:        opts.EnableDebugLogs,
			DiscoveryRunID: opts.RunID,
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

	// Status reporter for live updates
	statusReporter := NewStatusReporter(opts.RunRepo, opts.RunID, 0)

	return &Orchestrator{
		aiClient:           opts.AIClient,
		warehouse:          opts.Warehouse,
		contextRepo:        opts.ContextRepo,
		discoveryRepo:      opts.DiscoveryRepo,
		feedbackRepo:       opts.FeedbackRepo,
		debugLogRepo:       opts.DebugLogRepo,
		debugLogger:        debugLogger,
		statusReporter:     statusReporter,
		userCountValidator: ucValidator,
		projectID:          opts.ProjectID,
		domain:             opts.Domain,
		category:           opts.Category,
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
	}
}

// DiscoveryOptions configures a discovery run.
type DiscoveryOptions struct {
	MaxSteps int
	// MinSteps is a floor on exploration steps — early "done" signals below
	// this value are rejected and exploration continues. Zero disables it.
	MinSteps              int
	SkipSchemaCache       bool
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

	// Initialize schema discovery for all datasets
	o.schemaDiscovery = NewSchemaDiscovery(SchemaDiscoveryOptions{
		Warehouse: o.warehouse,
		Executor:  executor,
		ProjectID: o.projectID,
		Datasets:  o.datasets,
		Filter:    filterClause,
	})

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

	// Phase 2: Schema discovery
	applog.Info("Phase 2: Discovering schemas")
	o.statusReporter.SetPhase(ctx, models.PhaseSchemaDiscovery, "Discovering warehouse table schemas...", 8)
	schemas, err := o.discoverSchemas(ctx, projectCtx, opts.SkipSchemaCache)
	if err != nil {
		return nil, fmt.Errorf("schema discovery failed: %w", err)
	}
	applog.WithField("tables", len(schemas)).Info("Schemas discovered")

	// Build context for prompts
	schemaJSON, _ := json.MarshalIndent(o.simplifySchemas(schemas), "", "  ")

	// Provide schema context to SQL fixer and insight validator
	sqlFixer.SetSchemaContext(string(schemaJSON))
	if o.insightValidator != nil {
		o.insightValidator.SetSchemaContext(string(schemaJSON))
	}

	profileStr := "No project profile configured. Analyze the data without game-specific context."
	if len(o.profile) > 0 {
		pj, _ := json.MarshalIndent(o.profile, "", "  ")
		profileStr = string(pj)
	}
	areasDesc := o.buildAnalysisAreasDescription(analysisAreas)

	// Prepare base context (shared across all prompts — substituted once)
	baseContext := prompts.BaseContext
	baseContext = strings.ReplaceAll(baseContext, "{{PROFILE}}", profileStr)
	baseContext = strings.ReplaceAll(baseContext, "{{PREVIOUS_CONTEXT}}", previousContextStr)

	// Prepare exploration prompt: base context + exploration-specific content
	explorationPrompt := baseContext + "\n\n" + prompts.Exploration
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{DATASET}}", datasetsStr)
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{SCHEMA_INFO}}", string(schemaJSON))
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER}}", filterClause)
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER_CONTEXT}}", o.buildFilterContext())
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{FILTER_RULE}}", o.buildFilterRule())
	explorationPrompt = strings.ReplaceAll(explorationPrompt, "{{ANALYSIS_AREAS}}", areasDesc)

	// Inject project knowledge sources (no-op if no enterprise plugin loaded
	// or no sources indexed). Query phrased to surface broad domain context
	// useful for any exploration step.
	knowledgeQuery := fmt.Sprintf("data exploration for %s; analysis areas: %s", datasetsStr, o.areaNamesCSV(analysisAreas))
	explorationPrompt = o.injectKnowledgeSources(ctx, explorationPrompt, knowledgeQuery, knowledgeTopKExploration)

	// Phase 3: Autonomous exploration
	applog.Info("Phase 3: Running autonomous exploration")
	o.statusReporter.SetPhase(ctx, models.PhaseExploration, "Starting autonomous data exploration...", 10)
	o.explorationEngine = ai.NewExplorationEngine(ai.ExplorationEngineOptions{
		Client:   o.aiClient,
		Executor: executor,
		MaxSteps: opts.MaxSteps,
		MinSteps: opts.MinSteps,
		Dataset:  datasetsStr,
		OnStep: func(stepNum int, action, thinking, query string, rowCount int, queryTimeMs int64, queryFixed bool, errMsg string) {
			o.statusReporter.AddExplorationStep(ctx, stepNum, action, thinking, query, rowCount, queryTimeMs, queryFixed, errMsg)
		},
	})

	explorationResult, err := o.explorationEngine.Explore(ctx, ai.ExplorationContext{
		ProjectID:     o.projectID,
		Dataset:       datasetsStr,
		InitialPrompt: explorationPrompt,
	})
	if err != nil {
		return nil, fmt.Errorf("exploration failed: %w", err)
	}
	applog.WithField("steps", explorationResult.TotalSteps).Info("Exploration completed")

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

	for _, area := range runAreas {
		areaPrompt, ok := prompts.AnalysisAreas[area.ID]
		if !ok {
			applog.WithField("area", area.ID).Warn("No prompt for analysis area, skipping")
			continue
		}

		// Filter exploration queries relevant to this area
		relevantQueries := o.filterQueriesByKeywords(explorationResult.Steps, area.Keywords)
		if len(relevantQueries) == 0 {
			applog.WithField("area", area.ID).Info("No relevant queries found, skipping")
			continue
		}

		applog.WithFields(applog.Fields{
			"area":    area.ID,
			"queries": len(relevantQueries),
		}).Info("Analyzing area")

		// Prepare analysis prompt: base context + area-specific prompt
		queryResultsJSON, _ := json.MarshalIndent(relevantQueries, "", "  ")
		prompt := baseContext + "\n\n" + areaPrompt
		prompt = strings.ReplaceAll(prompt, "{{DATASET}}", datasetsStr)
		prompt = strings.ReplaceAll(prompt, "{{TOTAL_QUERIES}}", fmt.Sprintf("%d", len(relevantQueries)))
		prompt = strings.ReplaceAll(prompt, "{{QUERY_RESULTS}}", string(queryResultsJSON))

		// Inject project knowledge sources relevant to this analysis area.
		areaQuery := fmt.Sprintf("%s: %s", area.Name, area.Description)
		prompt = o.injectKnowledgeSources(ctx, prompt, areaQuery, knowledgeTopKAnalysis)

		// Create analysis step to capture full dialog
		step := models.AnalysisStep{
			AreaID:          area.ID,
			AreaName:        area.Name,
			RunAt:           time.Now(),
			Prompt:          prompt,
			RelevantQueries: len(relevantQueries),
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
		o.statusReporter.AddAnalysisStep(ctx, area.ID, area.Name, len(insights), "")
		for _, insight := range insights {
			o.statusReporter.AddInsightStep(ctx, insight.Name, insight.Severity, area.ID)
		}

		// Report validation results to status
		for _, vr := range step.ValidationResults {
			o.statusReporter.AddValidationStep(ctx, vr.ClaimedMetric, vr.Status, vr.ClaimedCount, vr.VerifiedCount)
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
		ExplorationLog:    explorationResult.Steps,
		AnalysisLog:       analysisLog,
		RecommendationLog: recStep,
		ValidationLog:     allValidation,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	if err := o.discoveryRepo.Save(ctx, result); err != nil {
		return nil, fmt.Errorf("failed to save discovery result: %w", err)
	}

	// Phase 9: Embed & Index (non-fatal — errors logged, discovery still completes)
	if o.embedIndexStore != nil {
		o.runPhaseEmbedIndex(ctx, result)
	}

	// Mark run as completed
	o.statusReporter.Complete(ctx, len(allInsights))

	applog.WithFields(applog.Fields{
		"project_id":      o.projectID,
		"insights":        len(allInsights),
		"recommendations": len(recommendations),
		"validations":     len(allValidation),
		"duration":        time.Since(startTime).String(),
	}).Info("Discovery run completed")

	return result, nil
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

	prompt := baseContext + "\n\n" + promptTemplate
	prompt = strings.ReplaceAll(prompt, "{{DISCOVERY_DATE}}", time.Now().Format("2006-01-02"))
	prompt = strings.ReplaceAll(prompt, "{{INSIGHTS_SUMMARY}}", summary)
	prompt = strings.ReplaceAll(prompt, "{{INSIGHTS_DATA}}", string(insightsJSON))

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

	// Key learnings from notes
	if len(pctx.Notes) > 0 {
		sb.WriteString("### Key Learnings\n")
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

func (o *Orchestrator) simplifySchemas(schemas map[string]models.TableSchema) map[string]interface{} {
	simplified := make(map[string]interface{})
	for name, schema := range schemas {
		cols := make([]map[string]string, 0, len(schema.Columns))
		for _, col := range schema.Columns {
			cols = append(cols, map[string]string{
				"name": col.Name, "type": col.Type, "category": col.Category,
			})
		}
		simplified[name] = map[string]interface{}{
			"row_count":  schema.RowCount,
			"columns":    cols,
			"metrics":    schema.Metrics,
			"dimensions": schema.Dimensions,
		}
	}
	return simplified
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

func (o *Orchestrator) discoverSchemas(ctx context.Context, pctx *models.ProjectContext, skipCache bool) (map[string]models.TableSchema, error) {
	if !skipCache && pctx != nil && len(pctx.KnownSchemas) > 0 {
		schemas := make(map[string]models.TableSchema)
		for name, sk := range pctx.KnownSchemas {
			schemas[name] = sk.CurrentSchema
		}
		applog.WithField("cached_tables", len(schemas)).Info("Using cached schemas")
		return schemas, nil
	}

	return o.schemaDiscovery.DiscoverSchemas(ctx)
}

func (o *Orchestrator) filterQueriesByKeywords(steps []models.ExplorationStep, keywords []string) []models.ExplorationStep {
	var filtered []models.ExplorationStep
	for _, step := range steps {
		if step.Query == "" {
			continue
		}
		text := strings.ToLower(step.Query + " " + step.QueryPurpose + " " + step.Thinking)
		for _, kw := range keywords {
			if strings.Contains(text, strings.ToLower(kw)) {
				filtered = append(filtered, step)
				break
			}
		}
	}
	return filtered
}

// executorAdapter adapts queryexec.QueryExecutor to validation.SelfHealingExecutor.
type executorAdapter struct {
	executor *queryexec.QueryExecutor
}

func (a *executorAdapter) Execute(ctx context.Context, query string, purpose string) ([]map[string]interface{}, error) {
	result, err := a.executor.Execute(ctx, query, purpose)
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

// injectKnowledgeSources retrieves relevant chunks from project knowledge
// sources and prepends them as a "## Project Knowledge" section to the prompt.
//
// The hook is always called; without an enterprise plugin loaded the registered
// provider is a no-op and the prompt is returned unchanged. Errors are logged
// but never returned — failure to retrieve knowledge must not abort discovery.
func (o *Orchestrator) injectKnowledgeSources(ctx context.Context, prompt, query string, topK int) string {
	if query == "" || topK <= 0 {
		return prompt
	}

	// Apply a tight per-call timeout so a slow embedding call cannot stall a phase.
	retrieveCtx, cancel := context.WithTimeout(ctx, knowledgeMaxRetrievalPerPhase)
	defer cancel()

	chunks, err := gosources.GetProvider().RetrieveContext(retrieveCtx, o.projectID, query, gosources.RetrieveOpts{
		Limit:    topK,
		MinScore: knowledgeMinScore,
	})
	if err != nil {
		applog.WithFields(applog.Fields{
			"project_id": o.projectID,
			"error":      err.Error(),
		}).Warn("Knowledge source retrieval failed; continuing without project knowledge")
		return prompt
	}

	section := gosources.FormatPromptSection(chunks)
	if section == "" {
		return prompt
	}

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
