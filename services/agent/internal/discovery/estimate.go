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
	var phase phaseCosts
	if priced {
		phase = tokens.costs(pricing)
	}
	llmCostUSD := phase.total()

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
		// Each phase priced on its own, so caching (exploration only) and
		// a long-prompt tier land on the phase that incurs them.
		Breakdown: models.CostBreakdown{
			Exploration:     phase.Exploration,
			Analysis:        phase.Analysis,
			Validation:      phase.Validation,
			Recommendations: phase.Recommendations,
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
// prompt cache (zero without caching). The unexported fields keep the
// request-level shape pricing needs: each exploration step is one request
// whose prompt grows by a fixed step, and a long-prompt price tier applies
// per request, not per phase.
type runTokenEstimate struct {
	ExplorationInput, ExplorationCachedInput, ExplorationOutput int
	AnalysisInput, AnalysisOutput                               int
	ValidationInput, ValidationOutput                           int
	RecommendationsInput, RecommendationsOutput                 int

	steps, systemPrompt, growth int   // exploration request shape
	promptCache                 bool  // exploration reads its prefix from cache
	areas                       int   // analysis requests
	modeCaps                    []int // per-document token cap of each validation mode
	docs                        int   // validated documents
}

// phaseCosts is the priced forecast, per phase, in USD.
type phaseCosts struct {
	Exploration, Analysis, Validation, Recommendations float64
}

func (c phaseCosts) total() float64 {
	return c.Exploration + c.Analysis + c.Validation + c.Recommendations
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
	e.steps, e.systemPrompt, e.growth, e.promptCache = n, in.SystemPromptTokens, growth, in.PromptCache
	e.ExplorationInput = n*in.SystemPromptTokens + growth*n*(n-1)/2
	e.ExplorationOutput = n * avgExplorationOutputPerStep
	if in.PromptCache && n > 1 {
		e.ExplorationCachedInput = (n-1)*in.SystemPromptTokens + growth*(n-1)*(n-2)/2
	}
	e.areas = in.NumAreas

	e.AnalysisInput = in.NumAreas * (in.BaseContextTokens + in.AvgAreaPromptTokens + analysisQueryResultsPerArea)
	e.AnalysisOutput = in.NumAreas * analysisOutputPerArea

	docs := in.NumAreas * estimatedInsightsPerArea
	e.docs = docs
	perDocIn, perDocOut := modeTokens(in.Validation.Verifier)
	e.modeCaps = []int{in.Validation.Verifier.TokenCap}
	if in.Validation.RefuterEnabled {
		rIn, rOut := modeTokens(in.Validation.Refuter)
		perDocIn += rIn
		perDocOut += rOut
		e.modeCaps = append(e.modeCaps, in.Validation.Refuter.TokenCap)
	}
	e.ValidationInput = docs * perDocIn
	e.ValidationOutput = docs * perDocOut

	e.RecommendationsInput = in.BaseContextTokens + in.RecommendationsPromptTokens + docs*recommendationInputPerIns
	e.RecommendationsOutput = recommendationsOutput
	return e
}

// modeTokens splits a verifier/refuter mode's per-document token cap into
// input and output. The cap bounds input + output together across all of a
// document's rounds, so the total is never more than TokenCap whatever the
// round count; within it, each round re-sends the bundle and history and
// replies with a short action, so input dominates. One reply allowance
// (MaxOutputTokens) as the output share puts output at roughly 13–15% of
// the cap with the default settings, which is in line with measured runs.
// Pricing every round's maximum output instead would count the same capped
// budget twice.
func modeTokens(mc verifier.ModeConfig) (in, out int) {
	out = mc.MaxOutputTokens
	if out > mc.TokenCap {
		out = mc.TokenCap
	}
	return mc.TokenCap - out, out
}

// costs prices the forecast per phase.
//
// Exploration is priced step by step: with prompt caching a step reads the
// previous step's prompt at the cache-read rate and writes the new tail at
// the cache-write rate (the breakpoint sits at the end of each request, so
// every uncached token is written); without it the whole prompt is billed
// at the base input rate. The other phases are single-shot requests billed
// at the base rate. A long-prompt tier (TokenPricing.LongContextThreshold,
// e.g. Claude Haiku 5.5 above 100K) multiplies every rate of a request whose
// prompt exceeds the threshold — per request, as the provider bills it. A
// validation request is bounded by its mode's token cap, so the cap decides
// its tier.
func (e runTokenEstimate) costs(p gollm.TokenPricing) phaseCosts {
	const perMillion = 1_000_000.0
	tier := func(prompt int) float64 {
		if p.LongContextThreshold > 0 && p.LongContextMultiplier > 0 && prompt > p.LongContextThreshold {
			return p.LongContextMultiplier
		}
		return 1
	}
	usd := func(in, out int, inRate float64) float64 {
		return float64(in)/perMillion*inRate + float64(out)/perMillion*p.OutputPerMillion
	}
	var c phaseCosts
	for step := 1; step <= e.steps; step++ {
		prompt := e.systemPrompt + (step-1)*e.growth
		var in float64
		switch {
		case e.promptCache && step > 1:
			cached := prompt - e.growth
			in = float64(cached)/perMillion*p.CacheReadPerMillion + float64(prompt-cached)/perMillion*p.CacheWritePerMillion
		case e.promptCache:
			in = float64(prompt) / perMillion * p.CacheWritePerMillion
		default:
			in = float64(prompt) / perMillion * p.InputPerMillion
		}
		out := float64(avgExplorationOutputPerStep) / perMillion * p.OutputPerMillion
		c.Exploration += tier(prompt) * (in + out)
	}
	if e.areas > 0 {
		perAreaIn, perAreaOut := e.AnalysisInput/e.areas, e.AnalysisOutput/e.areas
		c.Analysis = float64(e.areas) * tier(perAreaIn) * usd(perAreaIn, perAreaOut, p.InputPerMillion)
	}
	if e.docs > 0 {
		m := 1.0
		for _, capTokens := range e.modeCaps {
			if t := tier(capTokens); t > m {
				m = t
			}
		}
		c.Validation = m * usd(e.ValidationInput, e.ValidationOutput, p.InputPerMillion)
	}
	c.Recommendations = tier(e.RecommendationsInput) * usd(e.RecommendationsInput, e.RecommendationsOutput, p.InputPerMillion)
	return c
}
