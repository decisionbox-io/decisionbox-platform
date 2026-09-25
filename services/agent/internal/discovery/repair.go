package discovery

import (
	"context"
	"fmt"
	"sort"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Bounded repair before discard (E5).
//
// E1 and E3 both stop at attaching what they found, and both stop there for the
// same stated reason: there is nowhere for a refuted claim to go. A gate would
// convert one false sentence into a dropped document, and the document that
// motivated all of this carried seven sound claims beside the wrong one. This is
// the path that reason was waiting for.
//
// The unit of repair is one insight, and the unit of failure is one sentence. A
// claim comes back refuted; the insight is handed back with the evaluator's own
// reason; every check is re-run on whatever comes back; and if the claim is still
// false when the round cap is spent, the sentence is removed and the rest of the
// insight ships. The document is never the thing that is dropped.
//
// Three properties make the result measurable rather than merely different:
//
//   - The evidence is pinned. A rewrite may not change source_steps, so the
//     cheapest repair -- citing a different step until the claim is true -- is not
//     available.
//   - A round that removes the check instead of the error is rejected. Keeping a
//     refuted sentence while deleting its quantifier_claims entry would pass every
//     check trivially, and is the one thing a model asked to make a claim check
//     out would find first.
//   - A round that makes things worse is discarded, not adopted. The next round
//     re-prompts from the better text.
//
// What is not here: nothing consults the repair record to decide whether an
// insight ships. That is still true after E5 -- the difference is that the
// sentence a reader would have been misled by is gone from the text, so there is
// less left for a gate to do. filterEligibleInsights still reads
// Validation.Combined and nothing else.

const (
	// defaultAnalysisRepairMaxRounds bounds the corrective LLM calls spent on
	// one insight whose own evidence refutes it. Two, because the first round
	// has the evaluator's reason and the counter-example rows and should be
	// enough, and a second covers the case where the first rewrite trades one
	// refuted claim for another. Beyond that the model is not converging and
	// removing the sentence is both cheaper and more honest.
	//
	// Set ANALYSIS_REPAIR_MAX_ROUNDS=0 to disable the rewrite entirely. That
	// leaves the mechanical repairs -- a count Go computed, a sentence removed --
	// which cost nothing and need no model. To leave a refuted claim completely
	// untouched, the two advisory layers behind this one are what to look at:
	// see the caveat-not-gate note in attachQuantifierVerdicts.
	defaultAnalysisRepairMaxRounds = 2

	// analysisRepairMaxRoundsEnv overrides defaultAnalysisRepairMaxRounds.
	analysisRepairMaxRoundsEnv = "ANALYSIS_REPAIR_MAX_ROUNDS"

	// repairOutputCap ceilings the output budget for a repair call. One insight
	// comes back, not an area's worth, so the area's budget is far more than it
	// needs and a smaller ask is a cheaper and faster one.
	repairOutputCap = 4096
)

// repairTally is what one area's repair pass did, for the AnalysisStep counters.
type repairTally struct {
	repaired      int
	claimsDropped int
	unrepaired    int
	substituted   int
	rounds        int
	tokensIn      int
	tokensOut     int
	durationMs    int64
}

// repairRefutedInsights walks the area's insights and repairs each one whose
// declared claims its own cited rows contradict. Insights with no refuted claim
// are not touched and cost nothing, which on the measured corpus is nearly all
// of them.
//
// Runs after attachQuantifierVerdicts and before validation, so the verifier
// sees the text that will ship rather than the text that was refuted.
func (o *Orchestrator) repairRefutedInsights(
	ctx context.Context,
	areaID string,
	insights []models.Insight,
	stepByID map[int]*models.ExplorationStep,
	maxTokens int,
) repairTally {
	var tally repairTally
	maxRounds := goconfig.GetEnvAsInt(analysisRepairMaxRoundsEnv, defaultAnalysisRepairMaxRounds)
	if maxRounds < 0 {
		maxRounds = 0
	}
	for i := range insights {
		if countRefuted(insights[i].QuantifierVerdicts) == 0 {
			continue
		}
		o.repairInsight(ctx, areaID, &insights[i], stepByID, maxRounds, maxTokens, &tally)
	}
	return tally
}

// repairInsight repairs one insight in place and records what it took.
func (o *Orchestrator) repairInsight(
	ctx context.Context,
	areaID string,
	ins *models.Insight,
	stepByID map[int]*models.ExplorationStep,
	maxRounds, maxTokens int,
	tally *repairTally,
) {
	evidence := quantifierEvidence(*ins, stepByID)
	rep := &models.InsightRepair{}
	refutedAtEntry := refutedClaims(ins.QuantifierVerdicts)
	// The insight as it arrived. Kept only to ask later whether a claim was ever a
	// sentence in it: repair edits *ins in place, so that cannot be read off it
	// afterwards.
	//
	// The slices are copied, not shared. `*ins` copies a slice HEADER, so the
	// mechanical count substitution -- which writes through &ins.Indicators[i] --
	// would edit this snapshot too. An indicator-only claim corrected in place
	// would then be absent from the snapshot, insightMentions would say the prose
	// never carried it, and a genuine fix would be recorded as a withdrawal: the
	// exact corruption of the fixed/withdrawn distinction that distinction was
	// added to prevent.
	entryText := *ins
	entryText.Indicators = append([]string(nil), ins.Indicators...)
	entryText.QuantifierClaims = append([]models.QuantifierClaim(nil), ins.QuantifierClaims...)

	// Round zero: whatever Go can settle without asking. A count the evaluator
	// has already computed is not something a model needs to be asked for, and a
	// call not made is the cheapest correct call.
	if fixed := substituteRefutedCounts(ins, evidence); len(fixed) > 0 {
		ins.QuantifierVerdicts = EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
		tally.substituted++
		applog.WithFields(applog.Fields{
			"area":    areaID,
			"insight": ins.Name,
			"claims":  len(fixed),
		}).Info("Corrected a refuted count in Go; no rewrite needed")
	}

	// The rewrite rounds.
	for round := 1; round <= maxRounds && countRefuted(ins.QuantifierVerdicts) > 0; round++ {
		rep.Rounds = round
		tally.rounds++

		candidate, err := o.rewriteInsight(ctx, *ins, stepByID, maxTokens, tally)
		if err != nil {
			applog.WithFields(applog.Fields{
				"area":    areaID,
				"insight": ins.Name,
				"round":   round,
				"error":   err.Error(),
			}).Warn("Insight repair round failed; falling through to sentence removal")
			break
		}

		merged := mergeRepairedInsight(*ins, candidate)
		merged.QuantifierVerdicts = EvaluateQuantifierClaims(merged.QuantifierClaims, evidence)

		if stripped := undeclaredSurvivors(*ins, merged); len(stripped) > 0 {
			// The check was removed, not the error. Discarding the round rather
			// than adopting it is the whole reason this is checked: a claim that
			// is no longer declared is not thereby true, and adopting it would
			// let every subsequent measurement read as a clean pass.
			applog.WithFields(applog.Fields{
				"area":    areaID,
				"insight": ins.Name,
				"round":   round,
				"claims":  stripped,
			}).Warn("Rejecting repair round: a contradicted sentence survived with its declaration removed")
			continue
		}
		if unproven := unprovenRepairs(*ins, merged); len(unproven) > 0 {
			// A refuted claim that is still declared must now HOLD. A round that
			// leaves it undecidable has corrected nothing -- and undecidable is
			// reachable by editing the claim instead of the sentence, since `step`
			// and `filter` are both authored: point the claim at a step this
			// insight does not cite, or name a column the rows do not carry, and
			// the evaluator declines rather than refuses. countRefuted alone reads
			// that as progress because it counts only failures, and rep.Fixed
			// would then report the original false claim as fixed with no
			// predicate ever proven over the rows.
			applog.WithFields(applog.Fields{
				"area":    areaID,
				"insight": ins.Name,
				"round":   round,
				"claims":  unproven,
			}).Warn("Rejecting repair round: a contradicted claim survives without being proven to hold")
			continue
		}
		if countRefuted(merged.QuantifierVerdicts) > countRefuted(ins.QuantifierVerdicts) {
			applog.WithFields(applog.Fields{
				"area":    areaID,
				"insight": ins.Name,
				"round":   round,
				"before":  countRefuted(ins.QuantifierVerdicts),
				"after":   countRefuted(merged.QuantifierVerdicts),
			}).Warn("Rejecting repair round: the rewrite contradicts more of its evidence than the original")
			continue
		}
		*ins = merged
	}

	// Whatever is still refuted loses its sentence. This is the "before discard"
	// half: the claim goes, the finding stays.
	//
	// The unit is the sentence, not the clause. A sentence carrying a refuted
	// claim beside a sound one loses both, which throws away something true --
	// but excising a clause leaves prose a reader can see is broken, and that is
	// worse than one fewer correct sentence. The rounds above exist so the model
	// does the surgery in the normal case and this coarse cut is the fallback.
	// A declaration can outlive its sentence this way; it is re-settled below and
	// still holds over the rows, so it stays as the record of what was checked.
	for _, v := range ins.QuantifierVerdicts {
		if v.Status != QuantifierFails {
			continue
		}
		if dropClaimSentence(ins, v.Claim) {
			rep.Dropped = append(rep.Dropped, v.Claim)
			continue
		}
		// Two refuted claims can share one sentence, and the first removal took
		// it. dropClaimSentence then finds nothing to change and reports false,
		// which would file a claim the reader can no longer see as unrepaired --
		// escalating the outcome to its worst bucket and leaving a live failure
		// recorded about text that is gone.
		if !insightMentions(*ins, v.Claim) {
			rep.Dropped = append(rep.Dropped, v.Claim)
			continue
		}
		rep.Unrepaired = append(rep.Unrepaired, v.Claim)
		applog.WithFields(applog.Fields{
			"area":    areaID,
			"insight": ins.Name,
			"claim":   v.Claim,
			"reason":  v.Reason,
		}).Warn("Contradicted claim could not be corrected or removed; it ships with its verdict attached")
	}

	// Drop the declarations whose sentences are gone, then settle the claims
	// that remain. A stored verdict about a sentence the document no longer
	// contains would be read as a live failure by everything downstream.
	if len(rep.Dropped) > 0 {
		ins.QuantifierClaims = withoutClaims(ins.QuantifierClaims, rep.Dropped)
		ins.QuantifierVerdicts = EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
	}

	// A claim that left undeclared and was never a sentence here was withdrawn,
	// not repaired -- the document never said it, so nothing about the document
	// changed.
	rep.Withdrawn = withdrawnClaims(refutedAtEntry, entryText, *ins)
	rep.Fixed = remaining(refutedAtEntry, rep.Dropped, rep.Unrepaired, rep.Withdrawn)
	rep.Outcome = repairOutcome(rep)
	ins.Repair = rep

	switch rep.Outcome {
	case models.RepairRepaired:
		tally.repaired++
	case models.RepairClaimDropped:
		tally.claimsDropped++
	case models.RepairUnrepaired:
		tally.unrepaired++
	}
}

// rewriteInsight makes one repair call and returns the insight it parsed out.
func (o *Orchestrator) rewriteInsight(
	ctx context.Context,
	ins models.Insight,
	stepByID map[int]*models.ExplorationStep,
	maxTokens int,
	tally *repairTally,
) (models.Insight, error) {
	if o.aiClient == nil {
		// Checked here rather than at the top of the pass: a rewrite needs a
		// model, and the mechanical repairs -- a count Go computed, a sentence
		// removed -- do not. Refusing the whole pass would make a missing client
		// leave a refuted sentence in the document.
		return models.Insight{}, fmt.Errorf("no LLM client, so no rewrite is possible")
	}
	failed := refutedVerdicts(ins.QuantifierVerdicts)
	prompt := buildInsightRepairPrompt(ins, failed, refutedSteps(failed, stepByID))

	budget := maxTokens
	if budget <= 0 || budget > repairOutputCap {
		budget = repairOutputCap
	}
	res, err := o.aiClient.ChatWithFormat(ctx, prompt, "", budget, insightResponseFormat())
	if err != nil {
		return models.Insight{}, err
	}
	tally.tokensIn += res.TokensIn
	tally.tokensOut += res.TokensOut
	tally.durationMs += res.DurationMs

	parsed, _, perr := o.parseInsights(res.Content, ins.AnalysisArea)
	if perr != nil {
		return models.Insight{}, perr
	}
	if len(parsed) == 0 {
		return models.Insight{}, fmt.Errorf("repair response carried no insight")
	}
	// One insight went out, so one comes back. A response carrying several is
	// the model expanding the finding rather than correcting it; the first is
	// the rewrite and the rest are not asked for.
	return parsed[0], nil
}

// mergeRepairedInsight takes the authored fields from the rewrite and keeps
// everything derived or identifying from the original.
//
// source_steps is the one that matters. A model told its claim is false over the
// rows of step 29 can make the claim true by citing step 30 instead, which is not
// a repair -- it is the same sentence with different evidence, unchecked. Pinning
// the citation means the only thing a round can change is what the finding says.
//
// Known gap: a claim's own `step` field is authored and stays mutable, so within
// the pinned citation set a claim can still hop to a step where it happens to
// hold. Measured: the first repair arm moved two already-holding declarations
// from step 30 to step 4 (both benign -- the two steps carry the same rows), and
// the arm with the confinement instruction moved none. Not pinned here on
// purpose, because pinning it would be wrong: E1's finding is that a population
// claim needs the step that saw the population, so a correct repair sometimes
// has to re-point. Watching it with more units is the next measurement, not a
// speculative guard now.
//
// The id is kept because parseInsights mints a fresh UUID for an insight that
// arrives without one, and that id is the standalone document key and the vector
// point id. A repair that changed it would orphan every link to the finding.
func mergeRepairedInsight(orig, rewritten models.Insight) models.Insight {
	out := orig
	if rewritten.Name != "" {
		out.Name = rewritten.Name
	}
	if rewritten.Description != "" {
		out.Description = rewritten.Description
		// Taken together: DescriptionMd is derived from Description at parse
		// time, so a rewrite that carried formatting sets both and one that did
		// not clears the stale Markdown rather than leaving the old sentence
		// rendered beside the new plain text.
		out.DescriptionMd = rewritten.DescriptionMd
	}
	if rewritten.Severity != "" {
		out.Severity = rewritten.Severity
	}
	if rewritten.AffectedCount != 0 {
		out.AffectedCount = rewritten.AffectedCount
	}
	if rewritten.RiskScore != 0 {
		out.RiskScore = rewritten.RiskScore
	}
	if rewritten.Confidence != 0 {
		out.Confidence = rewritten.Confidence
	}
	if rewritten.TargetSegment != "" {
		out.TargetSegment = rewritten.TargetSegment
	}
	if len(rewritten.Metrics) > 0 {
		out.Metrics = rewritten.Metrics
	}
	if len(rewritten.Indicators) > 0 {
		out.Indicators = rewritten.Indicators
	}
	// Always taken, empty included: a rewrite that deleted the offending
	// sentence should also have deleted its declaration, and keeping the old
	// array would leave a claim about text that is gone.
	out.QuantifierClaims = rewritten.QuantifierClaims
	return out
}

// undeclaredSurvivors names the claims that are still in the rewritten text but
// no longer declared -- the model removing the check rather than the error.
//
// A genuine repair reworded or deleted the sentence, so the old claim text is not
// found and nothing is reported. A model that left the sentence alone and simply
// dropped the entry is caught here, which is the only reason this function
// exists: without it, the trivial pass is also the easiest one to write.
// unprovenRepairs lists the claims that were refuted on entry, are still
// declared after the rewrite, and do not now hold.
//
// "No longer refuted" is not "true". Undecidable means the evaluator declined,
// and a rewrite can reach that by editing the declaration rather than the
// sentence -- which is a claim quietly exempted from checking, not a claim made
// correct.
func unprovenRepairs(before, after models.Insight) []string {
	status := make(map[string]QuantifierStatus, len(after.QuantifierVerdicts))
	for _, v := range after.QuantifierVerdicts {
		status[v.Claim] = v.Status
	}
	var out []string
	for _, v := range before.QuantifierVerdicts {
		if v.Status != QuantifierFails {
			continue
		}
		st, stillDeclared := status[v.Claim]
		if !stillDeclared {
			// Gone from the declarations: undeclaredSurvivors judges that case --
			// a rejected round when the sentence is still there, a withdrawal
			// when the prose never carried the claim.
			continue
		}
		if st != QuantifierHolds {
			out = append(out, v.Claim)
		}
	}
	return out
}

// withdrawnClaims lists the claims that entered refuted, are no longer declared,
// and were never a sentence in the insight as it arrived.
func withdrawnClaims(refutedAtEntry []string, entry, after models.Insight) []string {
	declared := make(map[string]struct{}, len(after.QuantifierClaims))
	for _, c := range after.QuantifierClaims {
		declared[c.Claim] = struct{}{}
	}
	var out []string
	for _, claim := range refutedAtEntry {
		if _, ok := declared[claim]; ok {
			continue
		}
		if !insightMentions(entry, claim) {
			out = append(out, claim)
		}
	}
	return out
}

func undeclaredSurvivors(before, after models.Insight) []string {
	declared := make(map[string]struct{}, len(after.QuantifierClaims))
	for _, c := range after.QuantifierClaims {
		declared[c.Claim] = struct{}{}
	}
	var out []string
	for _, c := range before.QuantifierClaims {
		if _, ok := declared[c.Claim]; ok {
			continue
		}
		if insightMentions(after, c.Claim) {
			out = append(out, c.Claim)
		}
	}
	return out
}

// quantifierEvidence collects the rows of the steps an insight cited, which is
// the set every claim on it is settled against. Shared with
// attachQuantifierVerdicts so a repair is judged over exactly the evidence the
// original verdict was reached over.
func quantifierEvidence(ins models.Insight, stepByID map[int]*models.ExplorationStep) map[int]StepRows {
	evidence := make(map[int]StepRows, len(ins.SourceSteps))
	for _, id := range ins.SourceSteps {
		step, ok := stepByID[id]
		if !ok || step == nil {
			continue
		}
		evidence[id] = StepRows{Rows: step.QueryResult, Quality: step.Quality}
	}
	return evidence
}

// refutedSteps returns the steps the failing claims cite, in step order.
// Only those: the repair prompt shows the evidence the rewrite has to satisfy,
// and every other step in the citation is prompt weight that changes nothing.
func refutedSteps(failed []models.QuantifierVerdict, stepByID map[int]*models.ExplorationStep) []models.ExplorationStep {
	ids := make([]int, 0, len(failed))
	seen := make(map[int]struct{}, len(failed))
	for _, v := range failed {
		if _, ok := seen[v.Step]; ok {
			continue
		}
		seen[v.Step] = struct{}{}
		ids = append(ids, v.Step)
	}
	sort.Ints(ids)
	out := make([]models.ExplorationStep, 0, len(ids))
	for _, id := range ids {
		if step, ok := stepByID[id]; ok && step != nil {
			out = append(out, *step)
		}
	}
	return out
}

func countRefuted(verdicts []models.QuantifierVerdict) int {
	n := 0
	for _, v := range verdicts {
		if v.Status == QuantifierFails {
			n++
		}
	}
	return n
}

func refutedVerdicts(verdicts []models.QuantifierVerdict) []models.QuantifierVerdict {
	out := make([]models.QuantifierVerdict, 0, len(verdicts))
	for _, v := range verdicts {
		if v.Status == QuantifierFails {
			out = append(out, v)
		}
	}
	return out
}

func refutedClaims(verdicts []models.QuantifierVerdict) []string {
	out := make([]string, 0, len(verdicts))
	for _, v := range verdicts {
		if v.Status == QuantifierFails {
			out = append(out, v.Claim)
		}
	}
	return out
}

// withoutClaims drops the named declarations.
func withoutClaims(claims []models.QuantifierClaim, drop []string) []models.QuantifierClaim {
	if len(drop) == 0 {
		return claims
	}
	dropped := make(map[string]struct{}, len(drop))
	for _, c := range drop {
		dropped[c] = struct{}{}
	}
	out := make([]models.QuantifierClaim, 0, len(claims))
	for _, c := range claims {
		if _, ok := dropped[c.Claim]; ok {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// remaining returns the entries of all that appear in none of the exclusions.
func remaining(all []string, exclusions ...[]string) []string {
	if len(all) == 0 {
		return nil
	}
	excluded := make(map[string]struct{})
	for _, set := range exclusions {
		for _, s := range set {
			excluded[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(all))
	for _, s := range all {
		if _, ok := excluded[s]; ok {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// repairOutcome reports the worst thing that happened to any one claim, because
// that is the part a reviewer needs to see first.
func repairOutcome(rep *models.InsightRepair) string {
	switch {
	case len(rep.Unrepaired) > 0:
		return models.RepairUnrepaired
	case len(rep.Dropped) > 0:
		return models.RepairClaimDropped
	case len(rep.Fixed) > 0:
		return models.RepairRepaired
	// Nothing corrected and nothing removed: the only change was a declaration
	// about nothing in the prose going away.
	case len(rep.Withdrawn) > 0:
		return models.RepairWithdrawn
	default:
		return models.RepairRepaired
	}
}
