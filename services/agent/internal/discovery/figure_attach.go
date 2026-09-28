package discovery

import (
	"math"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// attachFigureVerdicts evaluates each insight's declared figures against the
// rows of the steps it cited, and records both what Go concluded and how much of
// the prose the declarations reached.
//
// Records rather than rejects, for the reason attachQuantifierVerdicts does: a
// gate with no repair path behind it converts one wrong number into a discarded
// document, and a document carrying one wrong figure usually carries a dozen
// right ones. repairRefutedInsights is the only thing that acts on a refutation.
//
// Coverage is recorded even when it is complete, and especially when there are no
// declarations at all. An insight declaring nothing is the case this counter
// exists for: the comparable layer's verdicts looked perfect on a quarter of
// documents because those documents had declared nothing to be wrong about, and
// that only came to light when someone counted numerals by hand. Counting it here
// means a null result can be told apart from an unreached one.
func attachFigureVerdicts(insights []models.Insight, stepByID map[int]*models.ExplorationStep) {
	for i := range insights {
		ins := &insights[i]

		// Cleared unconditionally and before any early return. Both fields are
		// derived, and `evidence_figures` / `evidence_figure_coverage` are
		// ordinary JSON tags, so a model that emits either key has its own
		// verdict decoded straight onto the insight. Having just been told to
		// emit `figure_claims`, volunteering the sibling is an obvious thing to
		// do -- and an insight that declared nothing is exactly where a
		// volunteered verdict would survive unnoticed.
		ins.FigureVerdicts = nil
		ins.FigureCoverage = nil

		coverage := figureCoverage(*ins)
		ins.FigureCoverage = &coverage

		if len(ins.FigureClaims) == 0 {
			if coverage.Written > 0 {
				applog.WithFields(applog.Fields{
					"insight":  ins.Name,
					"numerals": coverage.Written,
				}).Info("Insight declared no figures, so none of its numbers was checked")
			}
			continue
		}

		verdicts := EvaluateFigureClaims(ins.FigureClaims, quantifierEvidence(*ins, stepByID))
		ins.FigureVerdicts = verdicts
		for _, v := range verdicts {
			if v.Status != models.FigureFails {
				continue
			}
			applog.WithFields(applog.Fields{
				"insight":   ins.Name,
				"step":      v.Step,
				"kind":      v.Kind,
				"figure":    v.Figure,
				"claimed":   v.Claimed,
				"evaluated": v.Evaluated,
				"reason":    v.Reason,
			}).Warn("Figure is refuted by the arithmetic the insight declared for it")
		}
	}
}

// figureCoverage counts the numerals in an insight's prose and how many of them a
// declaration accounts for.
//
// Matching is loose and by value, never by string, because the prose necessarily
// rounds: "$33.12B" in the text against 33,116,752,392 in the declaration is the
// same figure and an exact comparison would call every rounded number undeclared.
//
// Deliberately looser than the check the verdicts use. This counter asks "did the
// model say where this number came from", not "is the number right" -- so a
// declaration that is refuted still counts as covering its numeral, which is the
// whole point: a refuted figure is a declared figure. Using the verdict's own
// stated-precision interval here would drop a refuted figure out of the covered
// count and make coverage rise as accuracy fell.
//
// A declaration whose value matches no numeral in the prose is not an error and is
// not counted against anything. The model may reasonably declare a figure it then
// phrases in words, and the extractor's own precision is too poor to call the
// difference.
func figureCoverage(ins models.Insight) models.FigureCoverage {
	texts := make([]string, 0, 2+len(ins.Indicators))
	texts = append(texts, ins.Name, ins.Description)
	texts = append(texts, ins.Indicators...)

	written := writtenNumerals(texts...)
	declared := 0
	for _, w := range written {
		for _, c := range ins.FigureClaims {
			if sameFigureLoosely(w, c.Value) {
				declared++
				break
			}
		}
	}
	return models.FigureCoverage{Written: len(written), Declared: declared}
}

// sameFigureLoosely is the coverage matcher: within 1% counts as the same figure.
//
// 1% rather than the verdict's interval because the two questions differ. A figure
// the prose writes as "$33.1B" and declares as 33,116,752,392 is one figure written
// twice, and so is one the prose writes as "5%" and declares as 4.97. Over-matching
// here understates coverage by at most the difference between two roundings of one
// number; under-matching would report a declared figure as undeclared, which is the
// error that makes the counter useless.
func sameFigureLoosely(written, declared float64) bool {
	scale := math.Max(math.Abs(written), math.Abs(declared))
	if scale < 1 {
		return math.Abs(written-declared) <= 0.01
	}
	return math.Abs(written-declared) <= scale*0.01
}
