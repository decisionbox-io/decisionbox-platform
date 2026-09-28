package discovery

import (
	"context"
	"sort"
	"strings"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Asking for the declarations the model left out.
//
// The check that evaluates declared arithmetic is only worth what its coverage is,
// and coverage is not a neutral sample. Measured over four replays and 659 numerals:
//
//	              grounded  derivable  ungrounded   ungrounded share
//	  declared         351         20          12          3%
//	  undeclared       163         59          54         20%
//
// The undeclared set is seven times as likely to hold a figure nothing can ground.
// The model declares what it can point at and passes over what it cannot, so a layer
// that checks only declarations runs on the population that was never in doubt and
// reports holds for all of it. That is adverse selection, and no amount of accuracy
// in the evaluator fixes it -- the figures that need checking are the ones absent
// from its input.
//
// So the choice of what gets checked is taken away from the model. Go extracts the
// numerals, subtracts the ones already declared, and names the remainder back with a
// demand: give the arithmetic, or say the numeral measures nothing.
//
// This is the job the rejected classifier is right for. Its precision at deciding
// whether a figure is *invented* was 7-14%, which disqualified it from driving a
// rewrite -- six sound sentences rewritten per real catch. Deciding which numerals to
// ASK about needs no precision at all: an over-extracted numeral costs one line in a
// prompt and one "that is a bucket edge" in the reply. The same tool, used for
// enumeration instead of judgement. E1 sets the precedent for the general point --
// it attaches a truncation caveat at two-of-seven precision because it is advisory --
// and the principle is that precision bounds what an action may be, not whether a
// signal is worth having.
//
// What this pass may not do is the whole of its safety.
//
//   - It cannot change a word of the prose. Only declarations are read back. A model
//     that could edit the text would satisfy the demand by deleting the numeral,
//     which is the "check removed rather than error fixed" failure undeclaredSurvivors
//     exists to reject one layer up.
//   - It cannot declare a figure that was not demanded. Only values on the list are
//     accepted, so the reply cannot reach past the question into figures already
//     settled.
//   - It cannot replace an existing declaration. The pass only adds.
//   - A numeral called a label is counted as a label and never as declared. Nothing
//     verifies a label, so the one defence is that dismissals stay visible beside
//     explanations.

// demandOutputCap bounds the reply. The demand asks for declarations over an area's
// insights, which is a list of small objects rather than prose, so it needs far less
// room than a rewrite.
const demandOutputCap = 16000

// maxDemandedFigures caps how many numerals one area may ask about.
//
// A cap rather than no cap because the extractor over-produces by design, and an area
// whose prose is dense with bucket edges could otherwise turn one call into a list of
// a hundred questions. Ordered by insight and by value before truncation, so which
// ones survive is deterministic rather than map order.
const maxDemandedFigures = 60

type demandTally struct {
	demanded   int
	explained  int
	labelled   int
	tokensIn   int
	tokensOut  int
	durationMs int64
}

// undeclaredNumerals lists what an insight's prose states and its declarations do not
// account for, in ascending value so the order is stable across runs.
//
// Each entry keeps the text the prose used, because the demand quotes the figure back
// as written rather than as a float.
func undeclaredNumerals(ins models.Insight) []numeralHit {
	texts := make([]string, 0, 2+len(ins.Indicators))
	texts = append(texts, ins.Name, ins.Description)
	texts = append(texts, ins.Indicators...)

	var out []numeralHit
	for _, h := range writtenNumeralHits(texts...) {
		accounted := false
		for _, c := range ins.FigureClaims {
			if sameFigureLoosely(h.value, c.Value) {
				accounted = true
				break
			}
		}
		if accounted {
			continue
		}
		// A numeral already dismissed as a label in an earlier pass is not asked
		// about again.
		if ins.FigureCoverage != nil && labelledAlready(*ins.FigureCoverage, h.value) {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].value < out[j].value })
	return out
}

func labelledAlready(cov models.FigureCoverage, v float64) bool {
	for _, l := range cov.Labels {
		if n, ok := parseDemandValue(l); ok && sameFigureLoosely(v, n) {
			return true
		}
	}
	return false
}

// demandFigureDeclarations asks one area's model for the declarations its insights
// left out, and merges back only what it was asked for.
//
// One call for the whole area rather than one per insight: the question is a list of
// numerals, the answer is a list of objects, and an insight's neighbours cost almost
// nothing to include. Returns a no-op tally when nothing is undeclared, which is the
// happy path and makes the pass free on a document that declared everything.
func (o *Orchestrator) demandFigureDeclarations(
	ctx context.Context,
	areaID string,
	insights []models.Insight,
	stepByID map[int]*models.ExplorationStep,
	maxTokens int,
) demandTally {
	var tally demandTally
	if o.aiClient == nil {
		return tally
	}

	wanted := make(map[int][]numeralHit, len(insights))
	total := 0
	for i := range insights {
		missing := undeclaredNumerals(insights[i])
		if len(missing) == 0 {
			continue
		}
		if total+len(missing) > maxDemandedFigures {
			if room := maxDemandedFigures - total; room > 0 {
				missing = missing[:room]
			} else {
				break
			}
		}
		wanted[i+1] = missing
		total += len(missing)
	}
	if total == 0 {
		return tally
	}
	tally.demanded = total

	prompt := buildFigureDemandPrompt(insights, wanted, demandSteps(insights, wanted, stepByID))
	budget := maxTokens
	if budget <= 0 || budget > demandOutputCap {
		budget = demandOutputCap
	}
	res, err := o.aiClient.ChatWithFormat(ctx, prompt, "", budget, figureDemandResponseFormat())
	if err != nil {
		applog.WithFields(applog.Fields{
			"area":     areaID,
			"demanded": total,
			"error":    err.Error(),
		}).Warn("Could not ask for the missing figure declarations; they stay unexplained")
		return tally
	}
	tally.tokensIn, tally.tokensOut, tally.durationMs = res.TokensIn, res.TokensOut, res.DurationMs

	reply, perr := parseFigureDemandReply(res.Content)
	if perr != nil {
		applog.WithFields(applog.Fields{
			"area":  areaID,
			"error": perr.Error(),
		}).Warn("Could not read the figure-declaration reply; the figures stay unexplained")
		return tally
	}

	tally.explained, tally.labelled = mergeDemandReply(insights, wanted, reply)

	applog.WithFields(applog.Fields{
		"area":        areaID,
		"demanded":    tally.demanded,
		"explained":   tally.explained,
		"labelled":    tally.labelled,
		"unexplained": tally.demanded - tally.explained - tally.labelled,
	}).Info("Asked for the figure declarations the prose left out")
	return tally
}

// mergeDemandReply takes only what was asked for. Returns how many numerals gained a
// declaration and how many were called labels.
func mergeDemandReply(insights []models.Insight, wanted map[int][]numeralHit, reply figureDemandReply) (explained, labelled int) {
	for _, d := range reply.Declarations {
		idx := d.Insight
		if idx < 1 || idx > len(insights) {
			continue
		}
		ins := &insights[idx-1]
		if !onDemandList(wanted[idx], d.Claim.Value) {
			// Not asked about. A reply reaching past the question into figures
			// already settled is how a pass meant to add coverage would start
			// rewriting the record.
			continue
		}
		if alreadyDeclared(*ins, d.Claim.Value) {
			continue
		}
		ins.FigureClaims = append(ins.FigureClaims, d.Claim)
		explained++
	}
	for _, l := range reply.NotMeasurements {
		idx := l.Insight
		if idx < 1 || idx > len(insights) {
			continue
		}
		v, ok := parseDemandValue(l.Figure)
		if !ok || !onDemandList(wanted[idx], v) {
			continue
		}
		ins := &insights[idx-1]
		if alreadyDeclared(*ins, v) {
			// Declared and dismissed in one reply. The declaration is the stronger
			// statement and the one that can be checked, so it wins.
			continue
		}
		if ins.FigureCoverage == nil {
			ins.FigureCoverage = &models.FigureCoverage{}
		}
		if labelledAlready(*ins.FigureCoverage, v) {
			continue
		}
		ins.FigureCoverage.Labels = append(ins.FigureCoverage.Labels, strings.TrimSpace(l.Figure))
		labelled++
	}
	return explained, labelled
}

func onDemandList(list []numeralHit, v float64) bool {
	for _, w := range list {
		if sameFigureLoosely(v, w.value) {
			return true
		}
	}
	return false
}

func alreadyDeclared(ins models.Insight, v float64) bool {
	for _, c := range ins.FigureClaims {
		if sameFigureLoosely(v, c.Value) {
			return true
		}
	}
	return false
}

// demandSteps is the evidence the reply needs: the steps the insights being asked
// about cited, deduplicated and in step order.
func demandSteps(insights []models.Insight, wanted map[int][]numeralHit, stepByID map[int]*models.ExplorationStep) []models.ExplorationStep {
	seen := map[int]struct{}{}
	var ids []int
	for idx := range wanted {
		if idx < 1 || idx > len(insights) {
			continue
		}
		for _, id := range insights[idx-1].SourceSteps {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	out := make([]models.ExplorationStep, 0, len(ids))
	for _, id := range ids {
		if s, ok := stepByID[id]; ok && s != nil {
			out = append(out, *s)
		}
	}
	return out
}
