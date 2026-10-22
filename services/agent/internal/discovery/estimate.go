package discovery

import (
	"context"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
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
	// The catalog is one line per table (column / sample detail is fetched
	// on demand via lookup_schema), so the system prompt is base context +
	// exploration template + catalog. Sizes use the 1-token-per-4-chars
	// heuristic; the estimator is an upper-bound planning aid, not a
	// billing record.
	schemaBuilder := &SchemaContextBuilder{Schemas: schemas}
	catalogEntries := schemaBuilder.buildCatalog(nil)
	catalogLen := 0
	for _, e := range catalogEntries {
		// 48 chars/line is typical for the renderer's format; exact
		// width doesn't matter for a 1-token-per-4-char heuristic.
		catalogLen += 48 + len(e.Table)
	}
	baseContextSize := len(prompts.BaseContext) / 4
	avgAreaPromptSize := 0
	for _, p := range prompts.AnalysisAreas {
		avgAreaPromptSize += len(p) / 4
	}
	if len(prompts.AnalysisAreas) > 0 {
		avgAreaPromptSize /= len(prompts.AnalysisAreas)
	}
	llmProvider := o.llmProvider
	llmModel := o.llmModel
	llmMeta, _ := gollm.GetProviderMeta(llmProvider)
	pricing, priced := llmMeta.PricingFor(llmModel)
	validationCfg, _ := verifier.LoadConfigFromEnv()

	tokens := estimateRunTokens(runTokenInputs{
		MaxSteps:                    opts.MaxSteps,
		NumAreas:                    numAreas,
		SystemPromptTokens:          baseContextSize + (len(prompts.Exploration)+catalogLen)/4,
		BaseContextTokens:           baseContextSize,
		AvgAreaPromptTokens:         avgAreaPromptSize,
		RecommendationsPromptTokens: len(prompts.Recommendations) / 4,
		PromptCache:                 priced && pricing.CacheReadPerMillion > 0 && gollm.PromptCacheEnabled(o.llmConfig),
		Validation:                  validationCfg,
	})
	explorationInputTokens, explorationOutputTokens := tokens.ExplorationInput, tokens.ExplorationOutput
	analysisInputTokens, analysisOutputTokens := tokens.AnalysisInput, tokens.AnalysisOutput
	validationInputTokens, validationOutputTokens := tokens.ValidationInput, tokens.ValidationOutput
	recsInputTokens, recsOutputTokens := tokens.RecommendationsInput, tokens.RecommendationsOutput
	estimatedInsights := numAreas * estimatedInsightsPerArea

	totalInputTokens := explorationInputTokens + analysisInputTokens + validationInputTokens + recsInputTokens
	totalOutputTokens := explorationOutputTokens + analysisOutputTokens + validationOutputTokens + recsOutputTokens

	// --- Apply LLM pricing ---
	// PricingFor resolves the model against the catalog (canonical
	// ID + aliases) so cross-region inference profiles, date-stamped
	// snapshots, and family-only short forms all hit the same row.
	// When the model is not in the catalog we emit no cost estimate
	// rather than show a $0 line.
	var llmCostUSD float64
	if priced {
		llmCostUSD = tokens.cost(pricing)
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
			Provider:                   llmProvider,
			Model:                      llmModel,
			EstimatedInputTokens:       totalInputTokens,
			EstimatedCachedInputTokens: tokens.ExplorationCachedInput,
			EstimatedOutputTokens:      totalOutputTokens,
			CostUSD:                    llmCostUSD,
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
		"total_usd":     fmt.Sprintf("$%.4f", totalCost),
		"llm_usd":       fmt.Sprintf("$%.4f", llmCostUSD),
		"warehouse_usd": fmt.Sprintf("$%.4f", warehouseCostUSD),
		"input_tokens":  totalInputTokens,
		"output_tokens": totalOutputTokens,
	}).Info("Cost estimation complete")

	return estimate, nil
}

// Per-step and per-area averages the estimator assumes. Upper-bound
// planning figures, not measurements of a particular run.
const (
	// avgExplorationOutputPerStep is one action reply (a JSON action and
	// its rationale).
	avgExplorationOutputPerStep = 600
	// avgExplorationUserMessagePerStep is the action result the engine
	// appends each step — query results (~1.5K tokens), lookup_schema
	// (~0.7K) and search_tables (~0.4K) in a ~70/20/10 mix.
	avgExplorationUserMessagePerStep = 1200
	// analysisQueryResultsPerArea is the condensed step results an
	// analysis area reads.
	analysisQueryResultsPerArea = 2000
	analysisOutputPerArea       = 2000
	// estimatedInsightsPerArea drives validation and recommendation sizing.
	estimatedInsightsPerArea  = 2
	recommendationInputPerIns = 200
	recommendationsOutput     = 3000
)

// runTokenInputs is what estimateRunTokens needs to forecast a run.
type runTokenInputs struct {
	MaxSteps                    int
	NumAreas                    int
	SystemPromptTokens          int // base context + exploration template + catalog
	BaseContextTokens           int
	AvgAreaPromptTokens         int
	RecommendationsPromptTokens int
	// PromptCache is true when the model's provider caches the repeated
	// prefix of multi-turn calls (and the project leaves it on).
	PromptCache bool
	Validation  verifier.Config
}

// runTokenEstimate is the per-phase token forecast for one discovery run.
// ExplorationCachedInput is the part of ExplorationInput read from the
// prompt cache (zero without caching).
type runTokenEstimate struct {
	ExplorationInput, ExplorationCachedInput, ExplorationOutput int
	AnalysisInput, AnalysisOutput                               int
	ValidationInput, ValidationOutput                           int
	RecommendationsInput, RecommendationsOutput                 int
}

// estimateRunTokens forecasts a run's tokens per phase.
//
// Exploration re-sends the whole conversation every step: step n's input
// is the system prompt plus every earlier action and result, so over N
// steps the input is N·S + g·N(N−1)/2 (S = system prompt, g = growth per
// step). With prompt caching each step after the first reads the previous
// step's prompt from the cache, leaving only the new tail uncached.
//
// Validation is sized from the verifier / refuter token caps, which bound
// each document's tokens per mode.
func estimateRunTokens(in runTokenInputs) runTokenEstimate {
	var e runTokenEstimate
	n := in.MaxSteps
	growth := avgExplorationUserMessagePerStep + avgExplorationOutputPerStep
	e.ExplorationInput = n*in.SystemPromptTokens + growth*n*(n-1)/2
	e.ExplorationOutput = n * avgExplorationOutputPerStep
	if in.PromptCache && n > 1 {
		e.ExplorationCachedInput = (n-1)*in.SystemPromptTokens + growth*(n-1)*(n-2)/2
	}

	e.AnalysisInput = in.NumAreas * (in.BaseContextTokens + in.AvgAreaPromptTokens + analysisQueryResultsPerArea)
	e.AnalysisOutput = in.NumAreas * analysisOutputPerArea

	docs := in.NumAreas * estimatedInsightsPerArea
	perDocIn, perDocOut := modeTokens(in.Validation.Verifier)
	if in.Validation.RefuterEnabled {
		rIn, rOut := modeTokens(in.Validation.Refuter)
		perDocIn += rIn
		perDocOut += rOut
	}
	e.ValidationInput = docs * perDocIn
	e.ValidationOutput = docs * perDocOut

	e.RecommendationsInput = in.BaseContextTokens + in.RecommendationsPromptTokens + docs*recommendationInputPerIns
	e.RecommendationsOutput = recommendationsOutput
	return e
}

// modeTokens splits a verifier/refuter mode's per-document token cap into
// input and output: the cap bounds input + output together, of which the
// verdict reply is at most MaxOutputTokens.
func modeTokens(mc verifier.ModeConfig) (in, out int) {
	out = mc.MaxOutputTokens
	if out > mc.TokenCap {
		out = mc.TokenCap
	}
	return mc.TokenCap - out, out
}

// cost prices the forecast. Cached exploration input is billed at the
// cache-read rate and the rest of exploration input at the cache-write
// rate (the cache breakpoint sits at the end of each request, so every
// uncached token is written); other phases are single-shot and billed at
// the base input rate.
func (e runTokenEstimate) cost(p gollm.TokenPricing) float64 {
	const perMillion = 1_000_000.0
	in := float64(e.AnalysisInput + e.ValidationInput + e.RecommendationsInput)
	exploreUncached := float64(e.ExplorationInput - e.ExplorationCachedInput)
	usd := in / perMillion * p.InputPerMillion
	if e.ExplorationCachedInput > 0 {
		usd += float64(e.ExplorationCachedInput)/perMillion*p.CacheReadPerMillion +
			exploreUncached/perMillion*p.CacheWritePerMillion
	} else {
		usd += exploreUncached / perMillion * p.InputPerMillion
	}
	out := float64(e.ExplorationOutput + e.AnalysisOutput + e.ValidationOutput + e.RecommendationsOutput)
	return usd + out/perMillion*p.OutputPerMillion
}
