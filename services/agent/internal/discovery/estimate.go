package discovery

import (
	"context"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// EstimateOptions configures a cost estimation.
type EstimateOptions struct {
	MaxSteps      int
	SelectedAreas []string
}

// EstimateCost calculates estimated costs for a discovery run without executing it.
// Phases: load schemas → calculate prompt token sizes → dry-run queries → apply pricing.
//
// Like RunDiscovery, EstimateCost requires the schema cache to be populated
// (estimate is meaningless without knowing the warehouse footprint, and the
// API gates estimate behind the same schema_index_status == "ready" check).
// No live-warehouse fallback by design.
func (o *Orchestrator) EstimateCost(ctx context.Context, opts EstimateOptions) (*models.CostEstimate, error) {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 100
	}

	applog.Info("Estimating discovery cost")

	// Phase 1: Load schemas from the per-project cache.
	applog.Info("Estimation: loading schemas from cache")
	schemas, err := o.discoverSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("schema cache lookup failed: %w", err)
	}
	applog.WithField("tables", len(schemas)).Info("Estimation: schemas loaded from cache")

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
	//
	// Token math (post on-demand-schema architecture):
	//
	//   - System prompt: base context + exploration template + the
	//     catalog block. The catalog is one line per table; size is
	//     bounded by the per-line approximation below. There is NO
	//     longer an upfront L1 dump (~16K tokens previously) — the
	//     model fetches column / sample detail on demand via
	//     lookup_schema. This is the dominant change vs. the previous
	//     estimator.
	//   - Per step: avg LLM output ~600 tokens (action JSON is small;
	//     thinking blocks vary). Plus per step the previous turn's
	//     user message lands in conversation history; we assume
	//     ~1.2K tokens / step on average, weighted across action types
	//     (query results dominate, lookup_schema and search_tables
	//     are lighter).
	//
	// The numbers are upper-bound estimates — the estimator is a
	// planning aid, not a billing record, so we round up.
	schemaBuilder := &SchemaContextBuilder{Schemas: schemas}
	catalogEntries := schemaBuilder.buildCatalog(nil)
	catalogLen := 0
	for _, e := range catalogEntries {
		// 48 chars/line is typical for the renderer's format; exact
		// width doesn't matter for a 1-token-per-4-char heuristic.
		catalogLen += 48 + len(e.Table)
	}
	baseContextSize := len(prompts.BaseContext) / 4
	explorationPromptSize := (len(prompts.Exploration) + catalogLen) / 4

	// Exploration phase: system prompt + growing per-step conversation.
	// Per-step user-message budget mixes query_data (~1.5K tokens),
	// lookup_schema (~0.7K), and search_tables (~0.4K). Empirical mix
	// runs ~70/20/10 → weighted avg 1.16K tokens, rounded to 1.2K.
	explorationInputTokens := baseContextSize + explorationPromptSize
	avgOutputPerStep := 600
	explorationOutputTokens := opts.MaxSteps * avgOutputPerStep
	const avgUserMessagePerStepTokens = 1200
	explorationInputTokens += opts.MaxSteps * avgUserMessagePerStepTokens

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
