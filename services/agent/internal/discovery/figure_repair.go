package discovery

import (
	"fmt"

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
// Every refuted figure is corrected. There is nothing left to decline over: no ambiguity
// about which numeral to swap, no field that might mean something else, no minimum length.
// The one case still left alone is a figure the evaluator could not settle, which is not a
// refutation and has no known correct value.
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
