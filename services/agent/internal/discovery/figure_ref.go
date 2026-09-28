package discovery

import (
	"fmt"
	"strings"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Checking a recommendation's figures.
//
// The insight evaluator runs the arithmetic a figure declares over the rows of the step
// it names. A recommendation has no steps -- it is given the insights and nothing else --
// so there is nothing to run arithmetic over, and asking for one would mean plumbing the
// exploration evidence into a second prompt that never needed it.
//
// What a recommendation has instead is better: the insights' figures, already settled.
// So a recommendation's figure names one of them, and Go resolves rather than evaluates.
// The operands arrive checked, which is why this file is short.
//
// Scope comes from measurement, not from symmetry with the insight layer. Across four
// adjudicated runs, 602 of 638 numerals in recommendation prose restated a number an
// insight had already stated; of the 36 that did not, most were projections ("a 3%
// conversion yields ~1,500") or policy parameters ("a 45-60 day track"), which are not
// measurements at all. And of eleven false recommendation claims, ten were inherited
// verbatim from a false insight -- fixed at the insight, not here -- leaving exactly one
// that this layer is for: a total over insight figures that matched no combination of
// them, in a headline its own body contradicted.

// figureRefIndex holds the settled value of every figure the recommender may reference,
// keyed by insight id and then figure id. Figure ids are only unique within an insight --
// every insight has an f1 -- so both halves are needed.
type figureRefIndex map[string]map[string]refValue

// refValue is one figure's settled value, or the reason it cannot be used.
type refValue struct {
	value float64
	// usable is false for a figure whose own check did not stand. Referencing it would
	// carry a number Go declined to vouch for into a second document, with the
	// reference making it look checked.
	usable bool
	why    string
}

// buildFigureRefIndex indexes the insights the recommender was given.
//
// Built from the recommender's own input rather than from every insight in the run, so a
// reference to an insight that never reached the prompt resolves to nothing and is
// reported, instead of silently picking up a figure from a document the model could not
// have read.
func buildFigureRefIndex(insights []models.Insight) figureRefIndex {
	ix := make(figureRefIndex, len(insights))
	for _, ins := range insights {
		if ins.ID == "" || len(ins.Figures) == 0 {
			continue
		}
		status := make(map[string]string, len(ins.FigureVerdicts))
		for _, v := range ins.FigureVerdicts {
			status[v.ID] = v.Status
		}
		byID := make(map[string]refValue, len(ins.Figures))
		for _, f := range ins.Figures {
			if strings.TrimSpace(f.ID) == "" {
				continue
			}
			// Holds is the only status a reference may stand on, and by the time this
			// runs it means checked and standing: the correction pass re-settles every
			// verdict it touched, so a figure Go fixed reads holds and a figure Go
			// declined to fix still reads fails.
			switch st := status[f.ID]; st {
			case models.FigureHolds:
				byID[f.ID] = refValue{value: f.Value, usable: true}
			case "":
				byID[f.ID] = refValue{why: "it was never checked"}
			default:
				byID[f.ID] = refValue{why: fmt.Sprintf("its own check came back %s", st)}
			}
		}
		ix[ins.ID] = byID
	}
	return ix
}

// resolve returns the settled value behind one reference.
func (ix figureRefIndex) resolve(r models.FigureRef) (float64, error) {
	figures, ok := ix[r.Insight]
	if !ok {
		return 0, fmt.Errorf("insight %s is not among the insights this recommendation was given", shortID(r.Insight))
	}
	rv, ok := figures[r.Figure]
	if !ok {
		return 0, fmt.Errorf("insight %s declares no figure %s", shortID(r.Insight), r.Figure)
	}
	if !rv.usable {
		return 0, fmt.Errorf("figure %s of insight %s cannot be referenced: %s", r.Figure, shortID(r.Insight), rv.why)
	}
	return rv.value, nil
}

// EvaluateRecommendationFigures settles every figure a recommendation declares against
// the insights it was given, in declared order.
func EvaluateRecommendationFigures(figures []models.Figure, ix figureRefIndex) []models.FigureVerdict {
	out := make([]models.FigureVerdict, 0, len(figures))
	for _, f := range figures {
		out = append(out, evaluateRecommendationFigure(f, ix))
	}
	return out
}

func evaluateRecommendationFigure(f models.Figure, ix figureRefIndex) models.FigureVerdict {
	v := models.FigureVerdict{
		ID: f.ID, Display: renderFigure(f), Kind: f.Kind, Claimed: f.Value,
	}
	undecidable := func(format string, args ...any) models.FigureVerdict {
		v.Status = models.FigureUndecidable
		v.Reason = fmt.Sprintf(format, args...)
		return v
	}

	if !models.RecommendationFigureKinds[f.Kind] {
		// Not a refutation. A kind this layer does not carry says nothing about whether
		// the number is right, and reporting the evaluator's own limits as the
		// document's errors is the failure this project spent five rounds learning to
		// avoid.
		return undecidable("kind %q is not one a recommendation can declare; only ref and sum resolve against insight figures", f.Kind)
	}
	if len(f.Refs) == 0 {
		return undecidable("kind %q declares no refs, so there is nothing to resolve", f.Kind)
	}
	if f.Kind == models.FigureRefKind && len(f.Refs) != 1 {
		return undecidable("kind ref restates one figure but names %d", len(f.Refs))
	}

	total := 0.0
	for _, r := range f.Refs {
		got, err := ix.resolve(r)
		if err != nil {
			return undecidable("%s", err.Error())
		}
		total += got
	}

	v.Evaluated = total
	if closeEnough(f, total) {
		v.Status = models.FigureHolds
		return v
	}
	v.Status = models.FigureFails
	v.Reason = fmt.Sprintf("the references total %s, %s", formatFigure(total), relativeGap(f.Value, total))
	return v
}

// adoptResolvedFigureValues replaces a recommendation figure's declared value with the
// one its own references produced, and records what changed.
//
// Unconditional, where the insight pass corrects only inside 1%. The gate exists there
// because the insight evaluator can itself be the wrong one: a ratio expressed as an
// excess, or across two columns of one row, is the model being right and Go's reading of
// it falling short, and rewriting the number to match would replace a figure the reader
// can check against the sentence with one that contradicts it. The insight pass has a
// safe fallback -- leave it refuted and visible, and the prose keeps the model's own
// coherent claim.
//
// Neither half of that holds here. The operands are figures Go has already checked and
// the operation is addition over them, so there is no column to misidentify and no
// grammar to fall short of. And there is no safe fallback: declining to adopt means
// rendering the declared value, which is the fabricated number this layer exists to stop
// -- the measured case was a headline claiming 96,447 buyers where the bands it named
// total 96,031, with the recommendation's own body saying 96,031 two paragraphs later.
//
// The residual is the mirror risk: the model lists the wrong references and Go faithfully
// totals them. That is the bet the whole layer rests on and the one the measurements keep
// confirming -- a model reliably reports what it used and unreliably computes over it.
// 96,447 matched no combination of the bands, so the arithmetic was the broken part.
func adoptResolvedFigureValues(rec *models.Recommendation, ix figureRefIndex) int {
	byID := make(map[string]int, len(rec.Figures))
	for j := range rec.Figures {
		byID[rec.Figures[j].ID] = j
	}

	var corrections []models.FigureCorrection
	for _, v := range rec.FigureVerdicts {
		if v.Status != models.FigureFails {
			continue
		}
		j, ok := byID[v.ID]
		if !ok {
			continue
		}
		f := &rec.Figures[j]
		before := renderFigure(*f)
		f.Value = v.Evaluated
		after := renderFigure(*f)
		if before == after {
			// The resolved value renders identically at the precision the figure is
			// written to, so the reader sees no change and nothing is claimed. A
			// rounding boundary, not an error.
			continue
		}
		corrections = append(corrections, models.FigureCorrection{
			ID: f.ID, From: v.Claimed, To: v.Evaluated, Text: before + " -> " + after,
		})
	}
	if len(corrections) == 0 {
		return 0
	}
	rec.FigureCorrections = corrections
	// Re-settle, so the verdicts describe the values that will be rendered rather than
	// the ones that were refuted.
	rec.FigureVerdicts = EvaluateRecommendationFigures(rec.Figures, ix)
	return len(corrections)
}

// attachRecommendationFigureVerdicts settles each recommendation's figures against the
// insights the recommender was given, adopts the values its references produced, and
// records what Go concluded.
//
// Records rather than rejects, for the reason the insight pass does: a gate with no
// correction path behind it turns one wrong number into a discarded document.
func attachRecommendationFigureVerdicts(recs []models.Recommendation, insights []models.Insight) (settled, adopted int) {
	ix := buildFigureRefIndex(insights)
	for i := range recs {
		rec := &recs[i]

		// Cleared before any early return, and unconditionally. All three fields are
		// derived and their JSON tags are ordinary, so a model that emits one has its
		// own verdict decoded straight onto the recommendation -- and having just been
		// asked for `figures`, volunteering the sibling is an obvious thing to do.
		rec.FigureVerdicts = nil
		rec.FigureCorrections = nil
		rec.FigureTemplate = nil

		if len(rec.Figures) == 0 {
			continue
		}
		rec.FigureVerdicts = EvaluateRecommendationFigures(rec.Figures, ix)
		settled += len(rec.FigureVerdicts)
		adopted += adoptResolvedFigureValues(rec, ix)

		for _, v := range rec.FigureVerdicts {
			if v.Status != models.FigureUndecidable {
				continue
			}
			applog.WithFields(applog.Fields{
				"recommendation": rec.Title,
				"figure":         v.ID,
				"display":        v.Display,
				"kind":           v.Kind,
				"reason":         v.Reason,
			}).Warn("Recommendation figure could not be resolved against an insight; its declared value ships unchecked")
		}
	}
	if adopted > 0 {
		applog.WithFields(applog.Fields{
			"figures": settled,
			"adopted": adopted,
		}).Info("Replaced recommendation figures with the values their references produced")
	}
	return settled, adopted
}

// shortID trims an insight uuid for a log line. Full ids are 36 characters and the log
// is read by a human looking for which reference failed, not resolving it again.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "(none)"
	}
	return id
}
