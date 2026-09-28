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

// refValue is one figure's value and what Go is willing to say about it.
//
// The three states are distinct because they need different answers, which the first live
// replay proved by conflating two of them. A figure whose own check did not stand still
// HAS a value, and that value is already in the insight's prose -- the correction gate
// leaves a refuted figure visible on purpose. So a recommendation restating it is faithful
// to the document it cites, and the refutation stays recorded where it happened. Only a
// reference that resolves to nothing has no number behind it at all.
type refValue struct {
	value float64
	// slack is the half-interval the SOURCE figure was verified at, which is the
	// precision its own insight printed it to. A `holds` verdict says the arithmetic
	// landed inside that interval and nothing narrower: "~911K" at the thousands scale
	// vouches for the value to within 500, so a recommendation restating it unscaled as
	// "911,000" would assert ±0.5 on a number checked to ±500.
	slack float64
	// found is false when the id resolves to nothing: no such insight, no such figure.
	// There is no number to carry.
	found bool
	// vouched is false for a figure whose own check came back refuted or never ran. The
	// value is still used -- the alternative is fabricating one -- but the recommendation
	// figure is undecidable rather than holding, so nothing claims it was checked here.
	vouched bool
	why     string
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
		// An insight whose prose was repaired can no longer vouch for its figures.
		//
		// Repair runs after the figures are rendered, and it edits text: it substitutes a
		// refuted count or drops a sentence outright, and it touches no figure. So a
		// figure stating 12 can pass its own check, have its sentence repaired to 302,
		// and keep the value 12 -- at which point a reference with no declared value
		// adopts 12 and puts the corrected error back into a second document.
		//
		// The insight itself is fine: its prose says 302 and that is what a reader sees.
		// What is broken is the figure's link to the sentence, which a text edit severed,
		// and there is no way from here to tell which figures the edit reached. So the
		// whole insight's figures become unreferenceable rather than guessed at. Repair is
		// rare -- it runs only where a declared claim its own evidence contradicts -- and
		// the cost is a recommendation typing a number instead of referencing one, which
		// is the status quo. The cost of the alternative is a false number.
		if ins.Repair != nil {
			why := "the insight's prose was repaired after its figures were rendered, so the figure no longer tracks the sentence"
			byID := make(map[string]refValue, len(ins.Figures))
			for _, f := range ins.Figures {
				if strings.TrimSpace(f.ID) == "" {
					continue
				}
				byID[f.ID] = refValue{value: f.Value, slack: figureSlack(f), found: true, why: why}
			}
			ix[ins.ID] = byID
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
				byID[f.ID] = refValue{value: f.Value, slack: figureSlack(f), found: true, vouched: true}
			case "":
				byID[f.ID] = refValue{value: f.Value, slack: figureSlack(f), found: true,
					why: "it was never checked"}
			default:
				byID[f.ID] = refValue{value: f.Value, slack: figureSlack(f), found: true,
					why: fmt.Sprintf("its own check came back %s, so the insight carries it unvouched too", st)}
			}
		}
		ix[ins.ID] = byID
	}
	return ix
}

// resolve returns the value behind one reference, whether Go vouches for it, and -- when
// it does not -- why.
//
// A missing reference is an error, because there is no number. An unvouched one is not:
// the value comes back with vouched false, and the caller carries it while refusing to
// call the figure checked.
func (ix figureRefIndex) resolve(r models.FigureRef) (value float64, slack float64, vouched bool, why string, err error) {
	figures, ok := ix[r.Insight]
	if !ok {
		return 0, 0, false, "", fmt.Errorf("insight %s is not among the insights this recommendation was given", shortID(r.Insight))
	}
	rv, ok := figures[r.Figure]
	if !ok || !rv.found {
		return 0, 0, false, "", fmt.Errorf("insight %s declares no figure %s", shortID(r.Insight), r.Figure)
	}
	if !rv.vouched {
		return rv.value, rv.slack, false, fmt.Sprintf("figure %s of insight %s is not vouched for: %s", r.Figure, shortID(r.Insight), rv.why), nil
	}
	return rv.value, rv.slack, true, "", nil
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
	unvouched := ""
	// A sum is verified no better than the coarsest of its operands: adding a value known
	// to within 500 to one known exactly leaves the total known to within 500.
	sourceSlack := 0.0
	for _, r := range f.Refs {
		got, slack, vouched, why, err := ix.resolve(r)
		if err != nil {
			return undecidable("%s", err.Error())
		}
		if !vouched && unvouched == "" {
			unvouched = why
		}
		if slack > sourceSlack {
			sourceSlack = slack
		}
		total += got
	}

	v.Evaluated = total
	v.Resolved = true
	if unvouched != "" {
		// The value is carried so the prose gets the number the cited insight itself
		// shows, but nothing here claims it was checked.
		return undecidable("%s", unvouched)
	}
	// No declared value is the contract being followed, not a disagreement: the
	// recommendation contract has no `value` field and says the platform supplies the
	// number. The first live replay recorded eighteen of nineteen figures as corrected
	// from zero, which made compliance look identical to being wrong in exactly the
	// telemetry used to measure it.
	// A restatement may not assert more accuracy than the source was checked to. The
	// insight that printed "~911K" vouched for its value to within 500; a recommendation
	// rewriting it unscaled as "911,000" asserts a half-unit, and its `holds` would be
	// claiming a precision nothing established. The value still renders -- it is the
	// number the insight itself shows -- and the verdict says so.
	if own := figureSlack(f); own < sourceSlack {
		if f.Value == 0 {
			v.Claimed = total
		}
		return undecidable(
			"the figures referenced were checked to within %s, and this one is written to within %s, "+
				"which is finer than anything established", formatFigure(sourceSlack), formatFigure(own))
	}
	if f.Value == 0 {
		v.Claimed = total
		v.Status = models.FigureHolds
		return v
	}
	if closeEnough(f, total) {
		v.Status = models.FigureHolds
		return v
	}
	v.Status = models.FigureFails
	v.Reason = fmt.Sprintf("the references give %s, %s", formatFigure(total), relativeGap(f.Value, total))
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
	filled := false
	for _, v := range rec.FigureVerdicts {
		if !v.Resolved {
			continue
		}
		j, ok := byID[v.ID]
		if !ok {
			continue
		}
		f := &rec.Figures[j]

		// An absent number is filled in from the best source there is, vouched or not.
		// The contract tells the model not to write a value, so absence is the normal
		// case; and where the operand is one Go declined to vouch for, the cited insight
		// is already showing that number to the same reader.
		if f.Value == 0 {
			f.Value = v.Evaluated
			filled = true
			continue
		}

		// A number the model did state is overwritten only when Go can vouch for the
		// replacement, which is what a refutation means here. An unvouched operand
		// leaves the model's own figure standing: swapping it for a number Go explicitly
		// would not stand behind is not a correction, and recording it as one would put
		// an unverified substitution in the log that exists to show verified ones.
		if v.Status != models.FigureFails {
			continue
		}
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
	if len(corrections) > 0 {
		rec.FigureCorrections = corrections
	}
	// Re-settle whenever a value moved, which includes the fill-in path and not only the
	// corrected one. Skipping it there left the audit trail saying `display: "0"` on a
	// figure whose claimed and evaluated values were both 52134 -- every contract-
	// compliant figure, since the contract is what tells the model to omit the value.
	if filled || len(corrections) > 0 {
		rec.FigureVerdicts = EvaluateRecommendationFigures(rec.Figures, ix)
	}
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
