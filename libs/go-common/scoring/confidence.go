package scoring

import (
	"math"
	"sort"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// scoreQuantum is the grid every base, dock and cutoff sits on. Rounding to it
// after each step is what keeps the breakdown's arithmetic exact for a reader:
// 0.80 - 0.10 - 0.05 has to be 0.65 on screen, not 0.6500000000000001, and the
// invariant base - sum(docks) == final has to hold as an equality rather than
// within a tolerance.
const scoreQuantum = 0.01

// reasons is the engine's plain-words explanation per verdict.
//
// Written here and stored on the document, so the dashboard, an API client and
// the clarifying-questions digest all quote one wording. Three independent
// phrasings of the same fact is how a reader ends up with two answers to "why
// is this medium".
var reasons = map[valmodels.Status]string{
	valmodels.StatusConfirmed:          "Warehouse rows matched every claim exactly.",
	valmodels.StatusSupported:          "Warehouse rows are consistent with the claims that were checked.",
	valmodels.StatusPartial:            "Only some of the claims could be checked against warehouse rows.",
	valmodels.StatusRejected:           "Warehouse rows contradict a headline claim.",
	valmodels.StatusUnverifiable:       "Nothing in the cited evidence could check these claims.",
	valmodels.StatusValidationDisabled: "Validation was turned off for this run, so nothing checked these claims.",
	valmodels.StatusSkippedBudgetCap:   "The run reached its validation budget before this item was checked.",
}

// unscoredReason covers a document carrying no verdict at all and one carrying
// a status this build does not recognise -- a legacy `unverified` / `error`, or
// a value written by a newer agent. Both are the same statement to a reader.
const unscoredReason = "This has not been checked against warehouse rows."

// pricedButUnworded is the sentence for a calibration that prices a verdict
// this build has no wording for.
//
// It exists so the field is never stored empty. A band with no explanation
// beside it is the one shape this layer must not produce -- a reader would be
// shown a grade and no reason for it -- and "not checked" would be a worse
// answer than a vague one, because the document WAS checked. A test pins that
// every verdict the shipped calibration prices has a sentence of its own, so
// this is reachable only by a calibration that added one without wording it.
const pricedButUnworded = "Derived from this document's validation verdict."

// baseFor returns the score a verdict earns before any deduction, and the
// reason that goes with it.
func baseFor(v valmodels.Status, cfg ConfidenceConfig) (float64, string) {
	if base, ok := cfg.Bases[v]; ok {
		if r := reasons[v]; r != "" {
			return base, r
		}
		return base, pricedButUnworded
	}
	if r, ok := reasons[v]; ok {
		// A status the taxonomy defines but the base table deliberately does
		// not price: validation never ran. It gets the default base and its
		// own wording.
		return cfg.Default, r
	}
	return cfg.Default, unscoredReason
}

// bandFor decides which band a verdict and its final score land in.
//
// Two of the five bands come from the verdict and are terminal; the other
// three come from the cutoffs. That split is the whole reason unverifiable is
// not folded into low confidence even though 0.20 is below the low cutoff: the
// cutoffs rank documents whose claims were checked, and these two were not.
func bandFor(v valmodels.Status, score float64, cfg ConfidenceConfig) string {
	switch v {
	case valmodels.StatusUnverifiable:
		return gomodels.ConfidenceBandUnverifiable
	case valmodels.StatusConfirmed, valmodels.StatusSupported,
		valmodels.StatusPartial, valmodels.StatusRejected:
		// Checked. Ranked by the score.
	default:
		// validation_disabled, skipped_budget_cap, "", a legacy status, or
		// anything unrecognised: never checked.
		return gomodels.ConfidenceBandNotConfirmed
	}
	switch {
	case score >= cfg.HighAt:
		return gomodels.ConfidenceBandHigh
	case score >= cfg.MediumAt:
		return gomodels.ConfidenceBandMedium
	default:
		return gomodels.ConfidenceBandLow
	}
}

// candidateDocks is every deduction the input earns, at its full configured
// amount and already subject to the two caps, in the order it is charged.
//
// Order is part of the contract, not an accident: the floor and the caps trim
// from the end, so it decides which dock is the one a reader sees reduced. The
// refutations come before the two cheap structural docks because a refuted
// number is the more serious fact, and a cap that trimmed it instead would say
// the opposite.
func candidateDocks(in Input, cfg ConfidenceConfig) []gomodels.ConfidenceDock {
	// cfg is the resolved calibration Score settled, not in.Config: a caller
	// that left the Config unset must not reach the dock table through here
	// with a zero one.
	d := cfg.Docks
	out := make([]gomodels.ConfidenceDock, 0, 5)

	// One dock per distinct caveat KIND. Two "withheld" caveats from two steps
	// are one fact about one kind of degradation; withheld plus sampled are
	// two. Counted over sorted kinds so the same input always produces the
	// same docks, whatever order the steps were labelled in.
	quality := make([]gomodels.ConfidenceDock, 0, len(in.QualityCaveats))
	for range distinctCaveatKinds(in.QualityCaveats) {
		quality = append(quality, gomodels.ConfidenceDock{
			Code:   d.DegradedEvidence.Code,
			Label:  d.DegradedEvidence.Label,
			Amount: d.DegradedEvidence.Amount,
		})
	}
	out = append(out, capTotal(quality, d.DegradedEvidenceCap)...)

	refuted := make([]gomodels.ConfidenceDock, 0, 2)
	if anyFigureRefuted(in.FigureVerdicts) {
		refuted = append(refuted, gomodels.ConfidenceDock{
			Code:   d.RefutedArithmetic.Code,
			Label:  d.RefutedArithmetic.Label,
			Amount: d.RefutedArithmetic.Amount,
		})
	}
	if anyQuantifierRefuted(in.QuantifierVerdicts) {
		refuted = append(refuted, gomodels.ConfidenceDock{
			Code:   d.RefutedQuantifier.Code,
			Label:  d.RefutedQuantifier.Label,
			Amount: d.RefutedQuantifier.Amount,
		})
	}
	out = append(out, capTotal(refuted, d.RefutedCap)...)

	if in.UnresolvedFigures > 0 {
		out = append(out, gomodels.ConfidenceDock{
			Code:   d.UnresolvedFigure.Code,
			Label:  d.UnresolvedFigure.Label,
			Amount: d.UnresolvedFigure.Amount,
		})
	}
	if in.StepReadBeyondRetained {
		out = append(out, gomodels.ConfidenceDock{
			Code:   d.SampleEndedEarly.Code,
			Label:  d.SampleEndedEarly.Label,
			Amount: d.SampleEndedEarly.Amount,
		})
	}

	// RefuterDisabled is deliberately absent from every branch above. Combine
	// already accounts for the refuter not having run -- a verifier-supported
	// document with the refuter off stays supported rather than dropping to
	// partial -- so docking here would charge the same fact a second time.
	return out
}

// capTotal trims a group of docks, in order, so they total no more than limit,
// and drops any trimmed to nothing.
//
// Trimming rather than dropping whole entries is what keeps the stored
// deductions equal to the drop the score took. Trimming the LAST entry rather
// than the first is why candidateDocks fixes its order: the dock a cap reduces
// should be the less serious one.
func capTotal(docks []gomodels.ConfidenceDock, limit float64) []gomodels.ConfidenceDock {
	out := make([]gomodels.ConfidenceDock, 0, len(docks))
	spent := 0.0
	for _, dock := range docks {
		room := round(limit - spent)
		if room <= 0 {
			continue
		}
		if dock.Amount > room {
			dock.Amount = room
		}
		spent = round(spent + dock.Amount)
		out = append(out, dock)
	}
	return out
}

// applyFloor charges the docks in order against the base, stopping at the
// floor, and returns the docks ACTUALLY charged plus the score they leave.
//
// The docks it returns are what gets stored, which is the point: a breakdown
// that recorded untrimmed amounts and left the reader to infer "a floor
// applied here" is a breakdown whose numbers do not add up. Charging in order
// and storing what was charged makes base - sum(docks) == final true by
// construction, and a dock that changed nothing is dropped rather than shown
// as a deduction that was not taken.
func applyFloor(base float64, docks []gomodels.ConfidenceDock, floor float64) ([]gomodels.ConfidenceDock, float64) {
	score := base
	out := make([]gomodels.ConfidenceDock, 0, len(docks))
	for _, dock := range docks {
		room := round(score - floor)
		if room <= 0 {
			continue
		}
		if dock.Amount > room {
			dock.Amount = room
		}
		score = round(score - dock.Amount)
		out = append(out, dock)
	}
	if len(out) == 0 {
		// Nil rather than an empty slice: the field is omitempty and an
		// undocked document should carry no key at all, in BSON and on the
		// wire.
		return nil, score
	}
	return out, score
}

// distinctCaveatKinds returns the caveat kinds present, sorted. Detail is
// deliberately ignored: two "withheld" caveats naming different row counts are
// the same kind of degradation, and charging twice for them would price the
// number of steps an insight cited rather than how degraded its evidence was.
func distinctCaveatKinds(caveats []gowarehouse.QualityCaveat) []string {
	if len(caveats) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(caveats))
	out := make([]string, 0, len(caveats))
	for _, c := range caveats {
		kind := string(c.Kind)
		if kind == "" {
			continue
		}
		if _, dup := seen[kind]; dup {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// anyFigureRefuted reports whether a figure's declared arithmetic is still
// refuted on the text that shipped.
//
// It reads the persisted verdicts and needs no carve-out for figures Go
// corrected, because both correction paths re-settle the verdicts afterwards --
// "so the verdicts describe the values that will be rendered rather than the
// ones that were refuted". A stored `fails` is therefore a live defect in the
// published prose, not the record of a repair that worked.
func anyFigureRefuted(verdicts []gomodels.FigureVerdict) bool {
	for _, v := range verdicts {
		if v.Status == gomodels.FigureFails {
			return true
		}
	}
	return false
}

// anyQuantifierRefuted reports whether a declared rank / count / trend claim is
// false over the rows it cites. Undecidable is not a refutation -- that is the
// evaluator declining, and charging for it would price the checker's own limits.
func anyQuantifierRefuted(verdicts []gomodels.QuantifierVerdict) bool {
	for _, v := range verdicts {
		if v.Status == gomodels.QuantifierFails {
			return true
		}
	}
	return false
}

// round snaps a score to the grid every number in this engine sits on.
func round(v float64) float64 {
	return math.Round(v/scoreQuantum) * scoreQuantum
}
