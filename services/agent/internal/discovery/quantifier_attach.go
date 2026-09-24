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
// A failing verdict is logged at Warn because it is the one signal that the
// model stated something its own evidence refutes.
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
		evidence := make(map[int]StepRows, len(ins.SourceSteps))
		for _, id := range ins.SourceSteps {
			step, ok := stepByID[id]
			if !ok || step == nil {
				continue
			}
			evidence[id] = StepRows{Rows: step.QueryResult, Quality: step.Quality}
		}
		verdicts := EvaluateQuantifierClaims(ins.QuantifierClaims, evidence)
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
