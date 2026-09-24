package discovery

import (
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// attachQuantifierVerdicts evaluates each insight's declared quantifier claims
// against the rows of the steps it cited, and records what Go concluded.
//
// Records rather than rejects. A gate here with no repair path behind it would
// convert a false claim into a dropped document, and most documents carrying a
// false quantifier claim are otherwise sound -- the observed one had seven
// correct claims beside the wrong one. Dropping it would cost more than the
// claim does. The verdicts are attached so a repair loop can act on them and so
// the effect can be measured before anything is gated on it.
//
// That repair loop now exists (repairRefutedInsights, E5) and runs immediately
// after this function. It is still not a gate: it corrects the sentence, or
// removes the sentence, and the finding ships either way. The verdicts left here
// are what it reads, and the ones it cannot settle stay attached to the insight
// so a refuted claim that survives is visible rather than silent.
//
// A failing verdict is logged at Warn because it is the one signal that the
// model stated something its own evidence refutes.
//
// This is the second advisory layer, and it is advisory by the same reasoning
// as the first. E1 attaches a truncation caveat and does not reject on it,
// because its precision on the corpus was 2 of 7 and as a gate it would have
// killed five sound insights. E3 is far more precise -- it evaluates a
// predicate over rows rather than inferring intent from prose -- but precision
// is not the reason it does not gate. The reason is that there is nowhere for a
// rejected insight to go until E5 exists: a document with one refuted claim and
// seven sound ones would be dropped whole. Both layers add information the
// writer did not have and leave the decision downstream, and neither becomes a
// gate without a repair path behind it.
//
// Verdicts deliberately do not reach filterEligibleInsights, which decides
// which insights recommendations may cite. That function reads
// Validation.Combined and nothing else. A verdict consulted there would be a
// rejection in effect, whatever this comment called it.
func attachQuantifierVerdicts(insights []models.Insight, stepByID map[int]*models.ExplorationStep) {
	for i := range insights {
		ins := &insights[i]
		// Cleared unconditionally, before the early return. The field is
		// derived, but the decoder that reads a model response has no way to
		// know that -- and having just been told to emit `quantifier_claims`,
		// a model volunteering `quantifier_verdicts` alongside them is an
		// obvious thing to do. Clearing first means an insight that declared
		// nothing cannot carry verdicts it wrote for itself.
		ins.QuantifierVerdicts = nil
		if len(ins.QuantifierClaims) == 0 {
			continue
		}
		verdicts := EvaluateQuantifierClaims(ins.QuantifierClaims, quantifierEvidence(*ins, stepByID))
		ins.QuantifierVerdicts = verdicts
		for _, v := range verdicts {
			if v.Status != QuantifierFails {
				continue
			}
			applog.WithFields(applog.Fields{
				"insight": ins.Name,
				"step":    v.Step,
				"kind":    v.Kind,
				"claim":   v.Claim,
				"reason":  v.Reason,
			}).Warn("Quantifier claim is refuted by the rows of the step it cites")
		}
	}
}
