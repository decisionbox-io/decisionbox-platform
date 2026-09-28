package discovery

import (
	"fmt"
	"math"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Correcting a refuted figure costs no model call and, now, no text edit.
//
// A refuted figure arrives with its arithmetic already evaluated, so the right number is
// known. Because the prose holds a reference rather than the number, correcting it is
// assigning a field: set the value, and every sentence mentioning that figure renders the
// corrected number when the templates are rendered.
//
// The previous version had to do this as string surgery -- find every standalone
// occurrence of a numeral across the name, the description, the indicators and the
// quantifier claim texts, agree they all measured the same thing, and rewrite each -- under
// a pile of refusals that existed because getting it wrong writes a new falsehood. It did
// get it wrong: on a compound figure it rewrote the leading numeral, which was a different
// quantity at a different scale, and then re-settled and reported the insight as holding.
// None of that machinery is reachable from here, because there is no text to edit.
//
// What it must still decline over is WHICH of the two is wrong.
//
// A refutation says the stated value and the declared arithmetic disagree. It does not say
// which one is at fault, and the first live run showed the assumption running the wrong
// way round. Of nine refutations, seven were a sound figure with a mis-declared
// arithmetic, and correcting the value wrote the arithmetic's answer into a sentence built
// for a different quantity:
//
//	$37,724 -> $7          a per-line dollar value declared as a ratio
//	$34.4B -> $206.6B      "annual revenue flat near $206.6B per year" -- the sum of all years
//	9.97% -> 111.08%       a discount rate declared as an unscoped ratio
//	2.51% -> 102.51%       a spread declared as the ratio it is the excess of
//
// Those are worse than the refutation they were meant to fix: a visible wrong number
// becomes an invisible one, in prose that now reads as nonsense, from the path with no
// model in the loop and therefore nothing reviewing it.
//
// So the gap decides. Two numbers within correctionGap of each other have to be the same
// quantity, differing by a rounding or a slipped digit, and the value is what is wrong.
// Two numbers 80% apart are different quantities, and it is the declaration that is wrong
// -- which Go cannot repair, because the prose is the only statement of what was meant.
// Those stay refuted and visible, which is the same choice undecidable makes and for the
// same reason.
//
// This restores, on a principle rather than by accident, what the previous design's
// refusals were doing: that pass declined most of these because a swap needed a matching
// unit and an unambiguous occurrence. Removing the refusals as "nothing left to decline
// over" was wrong, and one run was enough to show it.

// correctionGap is how close the stated and evaluated values must be for a correction to
// be a correction rather than a substitution of one quantity for another.
//
// 1% relative. Measured against the ten corrections observed across two runs it keeps
// every case where the two plainly agree -- 61.44 against 61.43, 54.66 against 55.04, and
// the digit transposition 34,363,832,414 against 34,373,633,413 that was the only genuine
// catch of the previous run -- and declines all seven where they do not.
const correctionGap = 0.01

func correctRefutedFigures(areaID string, insights []models.Insight, stepByID map[int]*models.ExplorationStep) int {
	total := 0
	for i := range insights {
		ins := &insights[i]
		if countRefutedFigures(ins.FigureVerdicts) == 0 {
			continue
		}

		byID := make(map[string]int, len(ins.Figures))
		for j := range ins.Figures {
			byID[ins.Figures[j].ID] = j
		}

		var corrections []models.FigureCorrection
		for _, v := range ins.FigureVerdicts {
			if v.Status != models.FigureFails {
				continue
			}
			j, ok := byID[v.ID]
			if !ok {
				continue
			}
			f := &ins.Figures[j]
			if !sameQuantity(v.Claimed, v.Evaluated) {
				// The declaration describes a different quantity than the figure states.
				// Rewriting the figure to match it would replace a number a reader can
				// check against the prose with one that contradicts the sentence around
				// it. Left refuted, and visible.
				applog.WithFields(applog.Fields{
					"area":      areaID,
					"insight":   ins.Name,
					"figure":    v.ID,
					"display":   v.Display,
					"claimed":   v.Claimed,
					"evaluated": v.Evaluated,
				}).Warn("Not correcting this figure: its declared arithmetic answers a different question, so the declaration is what is wrong")
				continue
			}
			before := renderFigure(*f)
			f.Value = v.Evaluated
			after := renderFigure(*f)
			if before == after {
				// The corrected value renders identically at the precision the figure is
				// written to, so the reader sees no change and nothing is claimed.
				// Reachable when the gap is smaller than the rendered place but larger
				// than the interval, which is a rounding boundary rather than an error.
				continue
			}
			corrections = append(corrections, models.FigureCorrection{
				ID: f.ID, From: v.Claimed, To: v.Evaluated, Text: before + " -> " + after,
			})
		}
		if len(corrections) == 0 {
			continue
		}
		ins.FigureCorrections = corrections
		// Re-settle against the same evidence, so the verdicts describe the values that
		// will be rendered rather than the ones that were refuted.
		ins.FigureVerdicts = EvaluateFigures(ins.Figures, quantifierEvidence(*ins, stepByID))
		total += len(corrections)

		texts := make([]string, 0, len(corrections))
		for _, c := range corrections {
			texts = append(texts, c.Text)
		}
		applog.WithFields(applog.Fields{
			"area":      areaID,
			"insight":   ins.Name,
			"figures":   texts,
			"remaining": countRefutedFigures(ins.FigureVerdicts),
		}).Info(fmt.Sprintf("Corrected %d refuted figure(s) in Go; no rewrite needed", len(corrections)))
	}
	return total
}

func countRefutedFigures(verdicts []models.FigureVerdict) int {
	n := 0
	for _, v := range verdicts {
		if v.Status == models.FigureFails {
			n++
		}
	}
	return n
}

// sameQuantity reports whether a stated value and an evaluated one are close enough to be
// two renderings of one number rather than two different numbers.
func sameQuantity(claimed, evaluated float64) bool {
	if math.IsNaN(claimed) || math.IsNaN(evaluated) || math.IsInf(claimed, 0) || math.IsInf(evaluated, 0) {
		return false
	}
	scale := math.Max(math.Abs(claimed), math.Abs(evaluated))
	if scale == 0 {
		return true
	}
	return math.Abs(claimed-evaluated)/scale <= correctionGap
}
