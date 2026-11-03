package discovery

// One analysis area's work up to the point validation begins, lifted out of
// RunDiscovery's Phase 4 loop so that loop can choose between doing it and
// restoring a draft of it that a previous attempt already paid for.
//
// Nothing here is new behaviour: it is the loop body as it was, with the three
// `continue`s and the one superseded `return` turned into a disposition the
// caller acts on, so the control flow stays where it was readable.

import (
	"context"
	"fmt"
	"time"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// areaAnalysisInput is what analysing one area needs. Everything but `area`
// is fixed for the whole run and is built once before the loop.
type areaAnalysisInput struct {
	area        AnalysisArea
	prompts     ResolvedPrompts
	baseContext string
	datasetsStr string
	refDataset  string
	picker      *AnalysisStepPicker
	// explorationSteps is the live slice the engine appends to, which is
	// what the picker ranks over.
	explorationSteps []models.ExplorationStep
	stepByID         map[int]*models.ExplorationStep
	currencies       currencyPlan
}

// areaDisposition is what the loop should do with an outcome.
type areaDisposition int

const (
	// areaAnalysed: the area produced insights and is ready to validate.
	areaAnalysed areaDisposition = iota
	// areaSkipped: nothing to analyse — no prompt, no pickable steps, or the
	// picker failed. The area contributes no step at all, exactly as before,
	// so it is absent from the analysis log rather than counted as a failure.
	areaSkipped
	// areaFailed: the analysis call or its parse failed. The step carries the
	// error and the caller appends it, which is what makes the area count
	// toward the run's failed-area tally.
	areaFailed
	// areaSuperseded: another attempt took the run over mid-call. The caller
	// must abandon the run rather than spend the validation phase on a result
	// that will be discarded.
	areaSuperseded
)

// areaAnalysisOutcome is one area's analysis, and what to do with it.
type areaAnalysisOutcome struct {
	disposition areaDisposition
	step        models.AnalysisStep
	insights    []models.Insight
}

// analyseArea runs one analysis area up to, but not including, Phase 4.5.
func (o *Orchestrator) analyseArea(ctx context.Context, in areaAnalysisInput) areaAnalysisOutcome {
	area := in.area
	prompts := in.prompts
	baseContext := in.baseContext
	datasetsStr := in.datasetsStr
	refDataset := in.refDataset
	picker := in.picker
	stepByID := in.stepByID
	currencies := in.currencies

	areaPrompt, ok := prompts.AnalysisAreas[area.ID]
	if !ok {
		applog.WithField("area", area.ID).Warn("No prompt for analysis area, skipping")
		return areaAnalysisOutcome{disposition: areaSkipped}
	}

	// Couple the picker's query-results budget to the model window so an
	// area's input can never grow past what the window can hold once the
	// output cap + safety margin are reserved. Recomputed per area so a
	// window calibrated mid-run (from an overflow 400) tightens the
	// remaining areas too.
	areaWindow, areaOutputCap := o.budgetFor(llmlayer.LayerAnalysis)
	picker.BudgetTokens = analysisPickerBudgetTokens(AnalysisQueryResultsBudgetTokens, areaWindow, areaOutputCap)

	pickResult, pickErr := picker.Pick(ctx, area, in.explorationSteps)
	// Skip the area if step retrieval fails so the remaining areas
	// can continue processing.
	if pickErr != nil {
		applog.WithFields(applog.Fields{"area": area.ID, "error": pickErr.Error()}).Warn("Step picker failed; skipping area")
		return areaAnalysisOutcome{disposition: areaSkipped}
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
		return areaAnalysisOutcome{disposition: areaSkipped}
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

	// How much of each cited step's result this area's prompt will show.
	// A no-op unless DISCOVERY_TRACE is set.
	traceExposure(area.ID, relevantSteps)

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

	// Smart-overflow breadcrumb (R5): when the picker trimmed steps for
	// budget, tell the model what evidence exists but wasn't shown, so it
	// doesn't conclude the un-shown angles were never examined. Empty (and
	// thus a no-op) on any run that fit without trimming — i.e. every
	// big-window run.
	if bc := formatAlsoExamined(pickResult.AlsoExamined); bc != "" {
		prompt += bc
	}

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

	// Again, now that the call has returned. The guard at the top of the
	// loop was true when the area started, and an analysis call is long
	// enough for a resume to land inside it — after which this attempt
	// would validate the insights it just got (more LLM calls) and
	// append analysis, insight and validation run-step rows. Those rows
	// are keyed on run_id ALONE, not on attempt, so they would show up
	// in the resumed run's live log as its own work.
	if o.ownershipLost(ctx, "validating area "+area.ID) {
		return areaAnalysisOutcome{disposition: areaSuperseded}
	}
	step.Response = outcome.response
	step.TokensIn = outcome.tokensIn
	step.TokensOut = outcome.tokensOut
	step.DurationMs = outcome.durationMs
	step.AnalysisParseRetries = outcome.parseRetries
	step.InsightsDroppedParse = outcome.droppedParse

	if outcome.chatErr != nil {
		step.Error = outcome.chatErr.Error()
		applog.WithFields(applog.Fields{"area": area.ID, "error": outcome.chatErr.Error()}).Warn("Analysis failed")
		return areaAnalysisOutcome{disposition: areaFailed, step: step}
	}

	if len(outcome.insights) == 0 && outcome.parseErr != nil {
		step.Error = fmt.Sprintf("parse error: %s", outcome.parseErr.Error())
		applog.WithFields(applog.Fields{"area": area.ID, "error": outcome.parseErr.Error()}).Warn("Failed to parse insights")
		return areaAnalysisOutcome{disposition: areaFailed, step: step}
	}

	insights := outcome.insights
	step.Insights = insights

	// Which currency each amount is in, before anything reads a figure. Figure.Symbol
	// answers "$" for a figure that states none, so an unmarked amount in any other
	// currency printed a dollar sign -- in one shipped sentence, next to the currency
	// the model had written as a word. Settled here rather than at each print site so
	// a stored figure says what it is in, and per datasource rather than per run,
	// because a figure read from a secondary warehouse is in that warehouse's
	// currency: see figure_currency.go.
	settleInsightFigureCurrencies(insights, currencies, stepByID)

	// Skip validation when the analysis step produced no insights.
	// The verifier only runs for successfully parsed insights.
	// Settle the figures first, then render them into the prose.
	//
	// Before the quantifier pass and before repair, because until the references are
	// rendered the prose is a template: a smoke run showed the repair prompt being
	// handed sentences reading "{{f1}} of customers placed 11+ orders" and asked to
	// rewrite them. Repair edits prose and has no notion of this format, so it has to
	// be given ordinary text -- which also means its behaviour is unchanged from
	// before figures existed.
	//
	// Settling first costs nothing, because a figure is checked against the step's
	// rows rather than against the prose. Nothing here needs the text.
	o.figureWriter().attachFigureVerdicts(insights, stepByID)

	// A refuted figure needs no model to fix: the arithmetic that refuted it already
	// produced the right number, and the prose holds a reference rather than the
	// number, so correcting it is assigning a field.
	if corrected := o.figureWriter().correctRefutedFigures(area.ID, insights, stepByID); corrected > 0 {
		step.FiguresCorrected = corrected
	}

	// Render the references. From here on the insight carries ordinary sentences, and
	// every later phase -- the quantifier pass, repair, validation, the API, the
	// dashboard, the exec summary -- reads them without knowing this format exists.
	// Unresolved counts too. Gating on resolved-or-inlined discarded the tally in the
	// one case it mattered most -- an area where every reference failed to resolve
	// reported FigureRefsUnresolved as zero, which reads as nothing went wrong.
	if rendered := o.figureWriter().renderInsightFigures(insights); rendered.resolved > 0 || rendered.inlined > 0 || rendered.unresolved > 0 {
		step.FiguresInlined = rendered.inlined
		step.FigureRefsUnresolved = rendered.unresolved
		step.FiguresValueUnreadable = rendered.valueUnreadable
	}

	// Settle every quantifier claim the model declared against the rows of
	// the step it cited, before anything downstream reads the insight. The
	// rows are already here and already correct; what was missing was the
	// check.
	attachQuantifierVerdicts(insights, stepByID)

	// Bounded repair before discard (E5). Every insight whose own cited rows
	// contradict a claim it declared gets the evaluator's reason handed back
	// and up to ANALYSIS_REPAIR_MAX_ROUNDS attempts to say something true;
	// whatever is still refuted loses the sentence rather than the finding.
	// Runs before validation so the verifier judges the text that ships.
	// A no-op on every insight that agreed with its evidence, which is
	// almost all of them.
	repair := o.repairRefutedInsights(ctx, area.ID, insights, stepByID, maxTokens)
	step.InsightsRepaired = repair.repaired
	step.InsightsClaimsDropped = repair.claimsDropped
	step.InsightsUnrepaired = repair.unrepaired
	step.AnalysisRepairRounds = repair.rounds
	// Repair calls are LLM calls, so their cost belongs in this area's
	// totals rather than disappearing into an untracked side channel.
	step.TokensIn += repair.tokensIn
	step.TokensOut += repair.tokensOut
	step.DurationMs += repair.durationMs
	if repair.rounds > 0 || repair.substituted > 0 {
		applog.WithFields(applog.Fields{
			"area":          area.ID,
			"rounds":        repair.rounds,
			"substituted":   repair.substituted,
			"repaired":      repair.repaired,
			"claim_dropped": repair.claimsDropped,
			"unrepaired":    repair.unrepaired,
		}).Info("Repaired insights whose declared claims their own evidence contradicted")
	}
	return areaAnalysisOutcome{disposition: areaAnalysed, step: step, insights: insights}
}
