package discovery

import (
	"context"
	"math"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	"github.com/decisionbox-io/decisionbox/libs/go-common/contextbudget"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Input-side budgeting for composed prompt context: what the project already
// knows, and what the registered context providers retrieve.
//
// The failure this guards against is the mirror of the one analysis_budget.go
// guards against. There, a fixed OUTPUT cap plus a large prompt overflowed the
// window. Here, a fixed amount of prior knowledge was carried into every prompt
// regardless of the window: 25 ledger findings, whether the model could hold
// five hundred or barely five. On a large window that threw away the memory
// that makes discovery compound — a run on a project with 420 accumulated
// findings was shown 6% of them and re-reported the rest as new. On a small one
// the same constant is an overflow waiting for a slightly longer catalog.
//
// So the amounts are computed per run and per call from the effective window
// and the measured prompt, through the same llm.Budget arithmetic, resolved
// through the same operator-override / live-auto-detection / catalog / default
// chain that budgetFor already owns.

// Operator knobs (Rule 2). Both are shares of what the window leaves free
// rather than absolute token counts, because an absolute count is the thing
// being removed.
const (
	// priorContextShareEnv is the floor share of the available input window the
	// accumulated prior-knowledge block may occupy. Saturation can raise the
	// share above it (see contextbudget.SaturatedShare) but never below.
	priorContextShareEnv = "DISCOVERY_PRIOR_CONTEXT_SHARE"
	// contextProviderShareEnv is the share of a prompt's REMAINING headroom
	// offered to each registered context provider.
	contextProviderShareEnv = "DISCOVERY_CONTEXT_PROVIDER_SHARE"
	// contextRetrievalTimeoutEnv bounds one prompt's context-provider render.
	contextRetrievalTimeoutEnv = "DISCOVERY_CONTEXT_RETRIEVAL_TIMEOUT"
)

const (
	// defaultPriorContextShare — a fifth of the free window for everything the
	// project already knows, leaving the rest for the schema catalog, the
	// query evidence and the pack content that make up the bulk of a prompt.
	// At the 131K default window that is ~18K tokens, which is roughly 150
	// ranked findings: six times what the fixed constant carried, and still a
	// minority of the prompt.
	defaultPriorContextShare = 0.20
	// defaultContextProviderShare is deliberately a share of what is LEFT after
	// the prompt is assembled rather than of the whole window, because by the
	// time providers are asked, the prompt already holds the catalog and the
	// evidence. A quarter of the remainder keeps room for the providers that
	// ignore the budget entirely and bound themselves by Limit.
	defaultContextProviderShare = 0.25
	// defaultContextRetrievalTimeout bounds one prompt's whole provider render.
	//
	// It was three seconds, as a literal, which was sized for a provider
	// fetching a handful of chunks. A provider sized from the context budget
	// instead embeds a query, searches the vector store and hydrates hundreds
	// of documents, and the surrounding code degrades a timeout to "no
	// section" with a warning — so too tight a bound here does not slow the
	// prompt down, it silently empties it. Ten seconds, per prompt, with a knob
	// for a deployment whose store is further away than that.
	defaultContextRetrievalTimeout = 10 * time.Second
)

// baseContextLayers are the layers the base context reaches.
//
// buildBaseContext's output is prepended to the exploration prompt, to EVERY
// analysis-area prompt, and to the recommendations prompt, and since per-layer
// models those three can be three different models with three different
// windows. One string carried into all of them must therefore be sized for the
// smallest, or it is an overflow on whichever layer runs the smallest model —
// a failure that could not show while there was only ever one model.
var baseContextLayers = []string{
	llmlayer.LayerExploration,
	llmlayer.LayerAnalysis,
	llmlayer.LayerRecommendations,
}

func priorContextShare() float64 {
	return goconfig.GetEnvAsFloat(priorContextShareEnv, defaultPriorContextShare)
}

func contextProviderShare() float64 {
	return goconfig.GetEnvAsFloat(contextProviderShareEnv, defaultContextProviderShare)
}

func contextRetrievalTimeout() time.Duration {
	if d := goconfig.GetEnvAsDuration(contextRetrievalTimeoutEnv, defaultContextRetrievalTimeout); d > 0 {
		return d
	}
	return defaultContextRetrievalTimeout
}

// baseContextAvailableInput is how many input tokens the base context may
// occupy on the TIGHTEST of the layers that receive it.
//
// Each layer's availability is computed from its own window and its own output
// cap and the minimum is taken, rather than pairing the smallest window with
// some other layer's cap: those two numbers belong to different models, and a
// budget assembled from parts of two models describes neither.
func (o *Orchestrator) baseContextAvailableInput() int {
	avail := -1
	for _, layer := range baseContextLayers {
		window, outputCap := o.budgetFor(layer)
		a := contextbudget.AvailableInput(window, intendedOutputReserve(outputCap, window), analysisReservedSystemTokens)
		if avail < 0 || a < avail {
			avail = a
		}
	}
	if avail < 0 {
		return 0
	}
	return avail
}

// priorContextBudget is how many tokens the accumulated prior-knowledge block
// may spend out of available, given how much of what the last measured run
// reported the project did not already have.
//
// available is passed in rather than resolved here so the caller can log the
// same number it budgeted from: resolving it twice walks three layers' budgets
// twice and, if a calibration lands in between, reports a figure the budget was
// not actually computed from.
func priorContextBudget(available, fixedPromptTokens int, novelty float64, haveNovelty bool) int {
	share := contextbudget.SaturatedShare(priorContextShare(), novelty, haveNovelty)
	budget := contextbudget.Share(available, share)
	// What the rest of the prompt already costs comes off the top, with the
	// step picker's floor left behind it for the evidence. Taking a share of
	// the WHOLE allowance ignored both: on a 32K model with a large profile and
	// large area templates, the fixed prompt plus a saturated ledger left the
	// picker at its floor and the assembled request ran past the window — where
	// the fixed 25-finding block this replaces had fitted.
	if room := available - fixedPromptTokens - minPickerBudgetTokens; budget > room {
		budget = room
	}
	if budget < 0 {
		budget = 0
	}
	// The scaled share exceeds 1 when the operator configures a base above
	// 0.5, and a budget larger than the free space is not a budget — on a
	// 32K/8K model a 0.75 base would have allocated 26,356 tokens of 18,496
	// available, overrunning before any schema or evidence was added. Clamped
	// here rather than inside Share, which stays a plain fraction, because
	// this is where the headroom is known.
	if budget > available {
		budget = available
	}
	return budget
}

// contextProviderBudget is the per-provider token budget for one assembled
// prompt: what this layer's window still has free once the prompt that exists
// is measured, times the provider share.
//
// Measured rather than estimated up front, and recomputed per call, because the
// prompt it has to fit beside is different every time — an analysis area's
// prompt carries that area's query results, and a window calibrated mid-run
// from an overflow tightens the areas that follow.
//
// Zero when the prompt already fills the window. That is the honest answer and
// the safe one: providers that honour the budget go quiet instead of pushing a
// prompt that is already at the limit over it.
//
// headroom is everything that is free, of which the budget is a share. The
// caller needs both: the budget is what each provider that honours one may
// spend, and the headroom is what the prompt can actually afford in total —
// which is the only thing that bounds a provider that ignores the budget
// entirely.
func (o *Orchestrator) contextProviderBudget(ctx context.Context, layer, prompt string) (budget, headroom int) {
	window, outputCap := o.budgetFor(layer)
	avail := contextbudget.AvailableInput(window, intendedOutputReserve(outputCap, window), analysisReservedSystemTokens)
	avail -= approxTokens(ctx, prompt)
	if avail <= 0 {
		return 0, 0
	}
	return contextbudget.Share(avail, contextProviderShare()), avail
}

// latestRunNovelty is the most recent MEASURED share of a run's own findings
// that the project did not already have, and whether there is one at all.
//
// Deliberately NOT MarginalRatio, which divides a run's new findings by the
// whole active ledger and so answers a different question: a run that adds ten
// entirely new findings to a hundred-finding ledger scores 0.09 there while
// having repeated nothing. Reading that as repetition would steer the most
// productive mature projects away from the seams that are paying off, and would
// put a false sentence in their prompts.
//
// Points written before RunFindings existed are skipped rather than guessed at,
// so a project carrying only legacy points gets the base share until its next
// run records a measurement. A signal that is inert for one run is better than
// one driven by a number that does not mean what it is read as.
func latestRunNovelty(history []commonmodels.ConvergencePoint) (float64, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if n, ok := history[i].RunNovelty(); ok {
			return n, true
		}
	}
	return 0, false
}

// reflectionOutputPerVerdictTokens is what one prior-finding status verdict
// costs in the response: a finding id, a status and a one-line reason, as JSON.
// Measured from the shape the schema demands rather than guessed at.
const reflectionOutputPerVerdictTokens = 48

// reflectionPriorsBudget is how many tokens the reflection prompt's
// re-judgement list may spend: what this layer's window leaves free once the
// response is reserved, minus everything else the prompt will carry.
//
// The rest of the prompt is MEASURED, by rendering it with an empty
// re-judgement list and with both conditional bullets at their most verbose.
// Measuring beats estimating here because the other sections vary by orders of
// magnitude between projects — a wide warehouse's two catalog lists alone run
// to hundreds of names. Rendering the verbose form makes the estimate
// conservative in the only direction that is safe: the real prompt can only be
// shorter, so the list can only fit better than budgeted.
//
// The output reserve is the part worth explaining. A longer list demands a
// longer response, because the model may return a verdict per finding — and a
// reflection response truncated mid-JSON parses as nothing and silently
// discards the whole phase's judgement (#403). But the verdicts a run can
// honestly produce are bounded by the findings it actually SAW again, which
// consolidation has already counted, so the reserve is sized from reSeenCount
// rather than from the length of the list. Charging every listed finding for a
// verdict it will almost never receive would halve the list to buy headroom
// nothing uses.
func (o *Orchestrator) reflectionPriorsBudget(
	ctx context.Context,
	result *models.DiscoveryResult,
	tasks []commonmodels.LedgerTask,
	pol agentplugin.DiscoveryPolicy,
	catalogItems []string,
	window, outputCap, reSeenCount int,
) int {
	reserve := intendedOutputReserve(outputCap, window)
	if verdicts := reSeenCount * reflectionOutputPerVerdictTokens; verdicts > reserve {
		reserve = verdicts
	}
	// Bounded by what the model can actually emit as well as by the window.
	// Reserving room the model will never use is not caution, it is the
	// re-judgement list paying for nothing: a 4,096-cap model with 300 re-seen
	// findings would have reserved 14,400 tokens and could lose the whole list
	// while the output it was protecting could not have exceeded 4,096.
	reserve = boundOutputCap(reserve, window)
	if outputCap > 0 && reserve > outputCap {
		reserve = outputCap
	}

	skeleton := o.buildReflectionPrompt(result, nil, tasks, pol, catalogItems, true, true)
	return contextbudget.AvailableInput(window, reserve, analysisReservedSystemTokens) - approxTokens(ctx, skeleton)
}

// defaultDedupMinScore is the cosine floor above which two pieces of discovery
// output are the same thing said twice.
//
// MEASURED, not chosen. 589 real documents (526 insights, 63 recommendations)
// from 30 runs across 8 projects were embedded and run through this exact check
// at candidate thresholds:
//
//	threshold  marked        of which from an earlier run
//	    0.95    71 (12%)      1
//	    0.92   152 (26%)      2
//	    0.90   183 (31%)     12
//	    0.88   206 (35%)     20
//	    0.85   239 (41%)     38
//
// The literal this replaces was 0.95, and the second column is why it never
// fired: ONE cross-run duplicate in 589 documents, on a corpus where the same
// finding is demonstrably reported run after run. The archetype in that data is
// a take-rate finding restated six times across runs in six wordings, scoring
// 0.872 to 0.904 — a 0.92 floor catches none of them and 0.95 catches none.
//
// Reading the pairs at each floor by hand: above 0.88 the marks are
// near-verbatim rewordings, including ones that only differ by a word and ones
// in different analysis areas. Below it they start including findings that are
// merely ABOUT the same subject with different figures — "top price decile is
// 40.7% of commission" against "top 2 deciles carry 57.4%" — where hiding one
// hides a different claim rather than a repetition. 0.88 is where that begins,
// so that is the default.
//
// It is NOT the ledger's 0.85, and the two should not be unified even though
// they judge a similar question. The ledger MERGES — it bumps a seen-count and
// keeps one document, on text that is a finding's name and description. This
// one HIDES, on an insight's full embedding text, which is longer and so scores
// higher for the same degree of relatedness. A harder action on longer text
// earns a stricter floor; forcing them equal would import this one's false
// positives into the other's corpus or the reverse.
//
// A wrong mark here is recoverable and visible — the row stays reachable
// through include_duplicates, carrying the score that hid it — which is what
// makes erring toward marking defensible at all.
const defaultDedupMinScore = 0.88

// documentDedupMinScoreEnv overrides the floor for the insight and
// recommendation documents Phase 9 marks. Named for documents rather than for
// insights because it governs both, and they are the same kind of thing to this
// check.
const documentDedupMinScoreEnv = "DISCOVERY_DOCUMENT_DEDUP_MINSCORE"

// documentDedupMinScore resolves that floor.
//
// Bounded to (0, 1]: cosine similarity cannot exceed 1, so a larger value
// silently disables the check — which is what a typo like 95 (meaning 0.95)
// would do, and it would look exactly like a check that found nothing. A
// non-positive value would mark every document a duplicate of the first one.
// Either way the default is the safer answer than the number asked for.
func documentDedupMinScore() float64 {
	v := goconfig.GetEnvAsFloat(documentDedupMinScoreEnv, defaultDedupMinScore)
	// NaN first, and explicitly: ParseFloat accepts "NaN", and every
	// comparison with NaN is false — so the range check below would pass it
	// through, and then `score < threshold` would be false for every pair and
	// mark every document in the run a duplicate of the first one. A range
	// check written as a range cannot catch the one value that is outside
	// every range.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return defaultDedupMinScore
	}
	if v <= 0 || v > 1 {
		return defaultDedupMinScore
	}
	return v
}

// fixedPromptTokens is what every prompt of this run will carry besides the
// prior-knowledge block and the evidence: the rendered base context, and the
// largest body the block will be prepended to.
//
// The largest rather than the average, because the block is composed ONCE and
// prepended to all of them — exploration's own body, each analysis area's
// template, and the recommendations template, which receives the same base
// context and is easy to forget because it runs last. A budget that fits the
// average overflows on the widest.
//
// Every part is measured by RENDERING it, not by counting the template it came
// from. The templates are not what reaches the model: buildBaseContext appends
// the base discipline rules to every prompt, buildAnalysisAreaPrompt appends
// the digest legend, the quantifier and figure contracts and the analysis
// rules, and buildRecommendationsPrompt appends its own figure contract and
// rules. Those are thousands of tokens that a template count cannot see, and
// on a small window they are the difference between a prompt that fits and a
// provider 400. Counting a list of what the builders appended when this was
// written would drift the first time one of them appends something else; the
// builders themselves cannot.
//
// The base context is rendered with the prior-knowledge block's OTHER sections
// in place and the findings themselves absent, because the findings are
// precisely what is being budgeted and everything around them is not. The
// bodies are rendered with an empty base context, so the base is counted once
// rather than once per body.
//
// The schema catalog counts, and it is the largest of them on a warehouse of
// any size: the comment here used to say the catalog could not be seen from
// this point, and that was simply wrong — it is rendered before the block is
// chosen and substituted into the exploration body. A 32K-window model with a
// 1,525-table catalog and a saturated ledger assembled 32,045 input tokens
// before any evidence, where the fixed 25-finding block it replaces had fitted.
// Reducing the output request cannot recover an input-only overflow.
//
// What is genuinely still outside it is the curated correlation guidance on a
// multi-datasource run, which does not exist yet — the routing cards it is
// appended to are counted. That, and the query evidence, is why
// priorContextBudget also leaves the step picker its floor; past that the
// adaptive context-overflow retry remains the net, as it is for every other
// estimate on this path.
func (o *Orchestrator) fixedPromptTokens(ctx context.Context, p fixedPromptParts) int {
	base := approxTokens(ctx, o.buildBaseContext(p.Prompts.BaseContext, p.Profile, p.PriorHistory, p.Language, p.RefDataset))

	// Exploration is assembled inline by runDiscovery rather than by a builder,
	// so its pieces are counted here: the template, the areas description, the
	// rendered catalog, and the datasource routing cards.
	widest := approxTokens(ctx, p.Prompts.Exploration) + approxTokens(ctx, p.AreasDesc) +
		approxTokens(ctx, p.Catalog) + approxTokens(ctx, p.Datasources)
	if n := approxTokens(ctx, o.buildRecommendationsPrompt("", p.Prompts.Recommendations, "", "", p.RefDataset)); n > widest {
		widest = n
	}
	for _, a := range p.Areas {
		if body, ok := p.Prompts.AnalysisAreas[a.ID]; ok {
			if n := approxTokens(ctx, o.buildAnalysisAreaPrompt("", body, p.Datasets, 0, "", p.RefDataset, "")); n > widest {
				widest = n
			}
		}
	}
	return base + widest
}

// baseContextRebuild is what is needed to compose the base context again with a
// different number of prior findings.
//
// It exists for the recommendations phase, which is the one phase whose
// variable payload is NOT bounded by a picker: analysis has an evidence budget
// that shrinks to fit, and recommendations simply carry every insight the run
// produced. On a mature ledger that payload plus a prior block sized for the
// exploration shape can exceed the window, and an input-only overflow cannot be
// recovered by asking for less output — the phase returns nothing, where the
// fixed 25-finding block it replaces had fitted.
//
// Held on the orchestrator rather than threaded through six parameters because
// it is run-scoped state in the same sense promptFindingIDs and
// baseContextFindingIDs already are.
type baseContextRebuild struct {
	template     string
	profile      string
	language     string
	refDataset   string
	frame        previousContextFrame
	lrc          *ledgerReadContext
	prevInsights []models.InsightSummary
}

// render composes the base context with the given findings block in the slot.
func (o *Orchestrator) renderBaseContext(rb *baseContextRebuild, findings string) string {
	return o.buildBaseContext(rb.template, rb.profile, rb.frame.with(findings), rb.language, rb.refDataset)
}

// refitAgainst returns the base context a phase should use, given a function
// that assembles that phase's whole prompt around a candidate base context.
//
// The block is sized once, early, against what the prompt was expected to cost.
// Two phases then carry something that estimate could not include, because it
// is this run's own output and does not exist yet: the recommendations phase
// carries every insight the run produced, and an analysis area carries the
// evidence its picker retained. Neither is bounded by anything the early
// estimate can see — recommendations have no picker at all, and the picker does
// not drop or re-compact its final step, so a single large exploration result
// stays whatever size it is however small the budget.
//
// So each of them checks the prompt it is about to send, and resizes the block
// if it does not fit. Measured, not predicted. An input-only overflow cannot be
// answered by asking for less output: the phase simply returns nothing, where
// the fixed 25-finding block this replaces had fitted.
//
// Returns the caller's base context untouched when it already fits, which is
// the ordinary case and costs two string builds.
func (o *Orchestrator) refitAgainst(ctx context.Context, phase, baseContext string,
	layer string, assemble func(base string) string) (string, []string) {
	rb := o.baseRebuild
	if rb == nil || rb.lrc == nil || len(rb.lrc.findings) == 0 {
		return baseContext, o.baseContextFindingIDs
	}
	window, outputCap := o.budgetFor(layer)
	available := contextbudget.AvailableInput(window, intendedOutputReserve(outputCap, window), analysisReservedSystemTokens)

	if approxTokens(ctx, assemble(baseContext)) <= available {
		return baseContext, o.baseContextFindingIDs
	}

	// What this phase costs with no prior findings at all. Everything else in
	// the prompt is fixed by now, this run's own output included, so this is
	// the real floor.
	//
	// An EMPTY slot, not renderPriorFindingsSlot(nil, …): passing a nil read
	// context selects the no-ledger fallback, which re-inserts the legacy
	// insight dump — unbudgeted, into the one prompt that has no room for the
	// budgeted block it is replacing.
	bare := o.renderBaseContext(rb, "")
	fixed := approxTokens(ctx, assemble(bare))
	room := available - fixed - ledgerBlockOverhead()
	findings, descChars := contextbudget.FitFindings(rb.lrc.candidates, room,
		contextbudget.MinFindingDescChars, contextbudget.MaxFindingDescChars)

	applog.WithFields(applog.Fields{
		"project_id": o.projectID,
		"phase":      phase,
		"available":  available,
		"fixed":      fixed,
		"was_shown":  len(rb.lrc.findings),
		"now_shown":  len(findings),
	}).Info("Prior context refitted against the prompt this phase is really sending")

	// The ids THIS prompt carries, not the ids the run-start block carried.
	// Retrieval is told to skip what the prompt already holds, so a stale list
	// suppresses prior knowledge this prompt no longer contains and the model
	// never sees it — the same mistake as excluding the base context's findings
	// from a prompt built without the base context.
	carried := make([]string, 0, len(findings))
	for i := range findings {
		carried = append(carried, findings[i].ID)
	}

	if len(findings) == 0 {
		return bare, nil
	}
	return o.renderBaseContext(rb, renderLedgerFindings(findings, descChars)), carried
}

// refitForRecommendations sizes the block against the insights payload this run
// produced. That phase has no evidence picker at all: it carries every insight.
func (o *Orchestrator) refitForRecommendations(ctx context.Context, baseContext, template, summary, insightsJSON, refDataset string) (string, []string) {
	return o.refitAgainst(ctx, "recommendations", baseContext, llmlayer.LayerRecommendations,
		func(base string) string {
			return o.buildRecommendationsPrompt(base, template, summary, insightsJSON, refDataset)
		})
}

// refitForAnalysisArea sizes the block against the evidence the picker actually
// retained for this area.
//
// The picker's budget is not a bound on that: Pick neither drops nor
// re-compacts its final step, so an area whose exploration produced one large
// result keeps it whatever BudgetTokens says. Lowering the evidence budget
// therefore could not rescue a prompt the block had already filled, and this is
// what does.
func (o *Orchestrator) refitForAnalysisArea(ctx context.Context, baseContext, areaPrompt, datasetsStr string,
	totalQueries int, queryResultsJSON, refDataset string, soFar runFindingsSoFar) (string, []string) {
	return o.refitAgainst(ctx, "analysis", baseContext, llmlayer.LayerAnalysis,
		func(base string) string {
			return o.buildAnalysisAreaPrompt(base, areaPrompt, datasetsStr, totalQueries, queryResultsJSON, refDataset, soFar)
		})
}

// fixedPromptParts is everything a run's prompts will carry besides the
// prior-knowledge block and the query evidence, as it exists at the moment the
// block has to be sized.
//
// A struct rather than nine positional strings, so that what is counted is
// legible at the call site — and so that the next thing the prompt grows is
// added as a named field by someone who can see the list, rather than
// forgotten because the signature was already long. Three of the review
// findings on this change were a piece of the prompt that nothing was counting.
type fixedPromptParts struct {
	Prompts    ResolvedPrompts
	Profile    string
	Language   string
	Datasets   string
	AreasDesc  string
	RefDataset string
	// Catalog is the rendered schema catalog substituted into {{SCHEMA_INFO}}.
	Catalog string
	// Datasources is the routing-contract section a multi-datasource run
	// appends. Empty on a single-warehouse run.
	Datasources string
	// PriorHistory is the prior-knowledge block MINUS the findings being
	// budgeted: the coverage map, the saturation reading, user feedback,
	// earlier recommendations, the open-thread queue, trends and observations.
	// It lands in the same prompt as the findings and had been invisible to
	// their budget — forty open threads of 1,500 characters are 15,000 tokens
	// rendered immediately under a block sized as though the window were free.
	PriorHistory string
	Areas        []AnalysisArea
}
