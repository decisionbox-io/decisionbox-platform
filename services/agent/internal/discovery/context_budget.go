package discovery

import (
	"context"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
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
// may spend, given how much genuinely new the last measured run added.
func (o *Orchestrator) priorContextBudget(marginalRatio float64, haveRatio bool) int {
	share := contextbudget.SaturatedShare(priorContextShare(), marginalRatio, haveRatio)
	return contextbudget.Share(o.baseContextAvailableInput(), share)
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
func (o *Orchestrator) contextProviderBudget(ctx context.Context, layer, prompt string) int {
	window, outputCap := o.budgetFor(layer)
	avail := contextbudget.AvailableInput(window, intendedOutputReserve(outputCap, window), analysisReservedSystemTokens)
	avail -= approxTokens(ctx, prompt)
	if avail <= 0 {
		return 0
	}
	return contextbudget.Share(avail, contextProviderShare())
}

// latestMarginalRatio is the most recent MEASURED share-of-new from the
// ledger's convergence history, and whether there is one at all.
//
// Points whose run recorded no findings are skipped. Such a point carries a
// ratio of 0 because the writer divides by a zero total, not because the run
// found only things already known — and reading it as saturation would hand the
// largest prior-context budget to a project whose last run failed or was
// cancelled before it found anything. Walking back to the last point that
// actually measured something is the difference between "nothing was new" and
// "there was nothing".
func latestMarginalRatio(history []commonmodels.ConvergencePoint) (float64, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].TotalFindings > 0 {
			return history[i].MarginalRatio, true
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
	reserve = boundOutputCap(reserve, window)

	skeleton := o.buildReflectionPrompt(result, nil, tasks, pol, catalogItems, true, true)
	return contextbudget.AvailableInput(window, reserve, analysisReservedSystemTokens) - approxTokens(ctx, skeleton)
}

// defaultDedupMinScore is the cosine floor above which two pieces of discovery
// output are the same thing said twice.
//
// ONE default for the two dedup paths this codebase has, because having two was
// the defect: the ledger's semantic dedup ran at 0.85 and demonstrably worked —
// on a production project it merged roughly eighty findings whose text had been
// reworded between runs — while the document-level check ran at a hard-coded
// 0.95 and marked zero of about five hundred insights. Two systems disagreeing
// about what "the same finding" means, and the effective one was not the one
// the user could see.
//
// 0.85 is the value with evidence behind it, measured on the same kind of text:
// a ledger finding is an area, a name and a description, and a document's
// embedding text is a name, a description, an area, a severity and a segment.
// Each path keeps its own override, so a deployment can still separate them.
const defaultDedupMinScore = 0.85

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
	if v <= 0 || v > 1 {
		return defaultDedupMinScore
	}
	return v
}
