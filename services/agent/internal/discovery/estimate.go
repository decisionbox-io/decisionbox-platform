package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// EstimateOptions configures a cost estimation.
type EstimateOptions struct {
	MaxSteps      int
	SelectedAreas []string
}

// EstimateCost calculates estimated costs for a discovery run without executing it.
// Phases: load schemas → calculate prompt token sizes → dry-run queries → apply pricing.
func (o *Orchestrator) EstimateCost(ctx context.Context, opts EstimateOptions) (*models.CostEstimate, error) {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 100
	}

	applog.Info("Estimating discovery cost")

	// Initialize schema discovery if not already set (estimate bypasses RunDiscovery)
	if o.schemaDiscovery == nil {
		filterClause := ""
		if o.filterField != "" && o.filterValue != "" {
			filterClause = fmt.Sprintf("WHERE %s = '%s'", o.filterField, o.filterValue)
		}
		executor := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{
			Warehouse:   o.warehouse,
			MaxRetries:  2,
			FilterField: o.filterField,
			FilterValue: o.filterValue,
		})
		o.schemaDiscovery = NewSchemaDiscovery(SchemaDiscoveryOptions{
			Warehouse: o.warehouse,
			Executor:  executor,
			ProjectID: o.projectID,
			Datasets:  o.datasets,
			Filter:    filterClause,
		})
	}

	// Phase 1: Load project context (for schema cache)
	projectCtx, err := o.loadProjectContext(ctx)
	if err != nil {
		projectCtx = models.NewProjectContext(o.projectID)
	}

	// Phase 2: Discover schemas (use cache if available)
	applog.Info("Estimation: discovering schemas")
	schemas, err := o.discoverSchemas(ctx, projectCtx, false)
	if err != nil {
		return nil, fmt.Errorf("schema discovery failed: %w", err)
	}
	applog.WithField("tables", len(schemas)).Info("Estimation: schemas discovered")

	// Resolve prompts and areas from project configuration
	prompts, analysisAreas := o.resolvePrompts()

	// Filter areas if selective
	numAreas := len(analysisAreas)
	if len(opts.SelectedAreas) > 0 {
		selected := make(map[string]bool)
		for _, a := range opts.SelectedAreas {
			selected[a] = true
		}
		count := 0
		for _, a := range analysisAreas {
			if selected[a.ID] {
				count++
			}
		}
		numAreas = count
	}
	applog.WithFields(applog.Fields{
		"total_areas":    len(analysisAreas),
		"selected_areas": numAreas,
		"max_steps":      opts.MaxSteps,
	}).Info("Estimation: calculating token costs")

	// --- Calculate LLM token estimates ---

	// Build prompts to measure token sizes (rough: 1 token ≈ 4 chars)
	schemaJSON, _ := json.MarshalIndent(o.simplifySchemas(schemas), "", "  ")
	baseContextSize := len(prompts.BaseContext) / 4
	explorationPromptSize := (len(prompts.Exploration) + len(schemaJSON)) / 4

	// Exploration phase: system prompt + per-step conversation
	explorationInputTokens := baseContextSize + explorationPromptSize
	avgOutputPerStep := 500 // avg tokens per exploration step response
	explorationOutputTokens := opts.MaxSteps * avgOutputPerStep
	// Conversation grows: each step adds ~300 tokens of context
	explorationInputTokens += opts.MaxSteps * 300

	// Analysis phase: per area
	avgAreaPromptSize := 0
	for _, p := range prompts.AnalysisAreas {
		avgAreaPromptSize += len(p) / 4
	}
	if len(prompts.AnalysisAreas) > 0 {
		avgAreaPromptSize /= len(prompts.AnalysisAreas)
	}
	// Each area gets: base context + area prompt + query results (~2000 tokens avg)
	analysisInputPerArea := baseContextSize + avgAreaPromptSize + 2000
	analysisOutputPerArea := 2000
	analysisInputTokens := numAreas * analysisInputPerArea
	analysisOutputTokens := numAreas * analysisOutputPerArea

	// Validation phase: per insight (estimate 2 insights per area)
	estimatedInsights := numAreas * 2
	validationInputPerInsight := 500  // verification query prompt
	validationOutputPerInsight := 200
	validationInputTokens := estimatedInsights * validationInputPerInsight
	validationOutputTokens := estimatedInsights * validationOutputPerInsight

	// Recommendations phase
	recsInputTokens := baseContextSize + len(prompts.Recommendations)/4 + estimatedInsights*200
	recsOutputTokens := 3000

	totalInputTokens := explorationInputTokens + analysisInputTokens + validationInputTokens + recsInputTokens
	totalOutputTokens := explorationOutputTokens + analysisOutputTokens + validationOutputTokens + recsOutputTokens

	// --- Get LLM pricing ---
	llmProvider := o.llmProvider
	llmModel := o.llmModel
	llmMeta, _ := gollm.GetProviderMeta(llmProvider)
	var llmCostUSD float64

	pricing, ok := llmMeta.DefaultPricing[llmModel]
	if !ok {
		// Try _default or partial match
		for model, p := range llmMeta.DefaultPricing {
			if model == "_default" || strings.Contains(llmModel, model) {
				pricing = p
				ok = true
				break
			}
		}
	}
	if ok {
		llmCostUSD = float64(totalInputTokens)/1_000_000*pricing.InputPerMillion +
			float64(totalOutputTokens)/1_000_000*pricing.OutputPerMillion
	}

	// --- Warehouse cost estimation ---
	var warehouseCostUSD float64
	var estimatedBytes int64
	estimatedQueries := opts.MaxSteps + estimatedInsights // exploration + validation queries

	// Try dry-run on a representative query
	if ce, ok := o.warehouse.(gowarehouse.CostEstimator); ok {
		datasetsStr := strings.Join(o.datasets, ", ")
		// Run dry-run on a simple count query for each dataset
		for _, ds := range o.datasets {
			for tableName := range schemas {
				if !strings.Contains(tableName, ds) {
					continue
				}
				query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`", tableName)
				result, err := ce.DryRun(ctx, query)
				if err == nil && result.BytesProcessed > 0 {
					estimatedBytes += result.BytesProcessed
					break // one table per dataset is enough for estimation
				}
			}
		}
		_ = datasetsStr

		// Extrapolate: avg bytes per query * total queries
		if estimatedBytes > 0 {
			avgBytesPerQuery := estimatedBytes / int64(len(o.datasets))
			estimatedBytes = avgBytesPerQuery * int64(estimatedQueries)
		}

		// Get warehouse pricing
		whProvider := o.warehouse.GetDataset()
		_ = whProvider
		whMeta, found := gowarehouse.GetProviderMeta("bigquery")
		if found && whMeta.DefaultPricing != nil {
			bytesInTB := float64(estimatedBytes) / (1024 * 1024 * 1024 * 1024)
			warehouseCostUSD = bytesInTB * whMeta.DefaultPricing.CostPerTBScannedUSD
		}
	}

	// --- Build breakdown ---
	explorationShare := float64(explorationInputTokens+explorationOutputTokens) / float64(totalInputTokens+totalOutputTokens)
	analysisShare := float64(analysisInputTokens+analysisOutputTokens) / float64(totalInputTokens+totalOutputTokens)
	validationShare := float64(validationInputTokens+validationOutputTokens) / float64(totalInputTokens+totalOutputTokens)
	recsShare := float64(recsInputTokens+recsOutputTokens) / float64(totalInputTokens+totalOutputTokens)

	totalCost := llmCostUSD + warehouseCostUSD

	estimate := &models.CostEstimate{
		LLM: models.LLMCostEstimate{
			Provider:              llmProvider,
			Model:                 llmModel,
			EstimatedInputTokens:  totalInputTokens,
			EstimatedOutputTokens: totalOutputTokens,
			CostUSD:               llmCostUSD,
		},
		Warehouse: models.WarehouseCostEstimate{
			Provider:              "bigquery",
			EstimatedQueries:      estimatedQueries,
			EstimatedBytesScanned: estimatedBytes,
			CostUSD:               warehouseCostUSD,
		},
		TotalUSD: totalCost,
		Breakdown: models.CostBreakdown{
			Exploration:     explorationShare * llmCostUSD,
			Analysis:        analysisShare * llmCostUSD,
			Validation:      validationShare * llmCostUSD,
			Recommendations: recsShare * llmCostUSD,
		},
	}

	applog.WithFields(applog.Fields{
		"total_usd":    fmt.Sprintf("$%.4f", totalCost),
		"llm_usd":      fmt.Sprintf("$%.4f", llmCostUSD),
		"warehouse_usd": fmt.Sprintf("$%.4f", warehouseCostUSD),
		"input_tokens":  totalInputTokens,
		"output_tokens": totalOutputTokens,
	}).Info("Cost estimation complete")

	return estimate, nil
}
