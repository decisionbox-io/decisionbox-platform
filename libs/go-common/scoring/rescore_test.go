package scoring

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// scoredInsight is a fully scored document to re-score FROM: high severity off
// a checked share, and a verdict nothing could check.
func scoredInsight(t *testing.T) gomodels.Scoring {
	t.Helper()
	got := withFigures(t, "$", DefaultConfig(),
		pair(fig("f1", 34, gomodels.UnitPercent, "", gomodels.FigureHolds)))
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Fatalf("fixture is not high severity: %q", got.SeverityBand)
	}
	return got
}

// TestRescoreConfidenceFreezesTheSeverityAxis is the invariant the on-demand
// "Run validation" path rests on, asserted field by field rather than by spot
// check.
//
// Severity is magnitude x weight. The magnitude is a conditional-on-true
// statement about how much the finding would matter, and a validation run
// changes neither it nor the weight -- so a stored severity is an immutable
// fact about the run that produced it. Re-deriving it here would either
// produce the same number, which is pointless, or, once weights are
// configurable, quietly rewrite a past run's result under today's
// configuration.
func TestRescoreConfidenceFreezesTheSeverityAxis(t *testing.T) {
	prior := scoredInsight(t)

	// Re-validated, and this time the warehouse contradicted it. The input
	// carries NO figures at all, which is the strongest form of the test: if
	// the severity half were re-derived it would fall to the baseline and the
	// frozen comparison would fail.
	got := RescoreConfidence(prior, Input{
		Verdict: valmodels.StatusRejected,
		Config:  DefaultConfig(),
	})

	if got.MagnitudeScore != prior.MagnitudeScore ||
		got.SeverityBand != prior.SeverityBand ||
		got.SeverityScore != prior.SeverityScore ||
		got.SeverityReason != prior.SeverityReason ||
		got.AreaWeight != prior.AreaWeight {
		t.Errorf("the severity axis moved:\n got %+v\nwant %+v", got, prior)
	}
	if !reflect.DeepEqual(got.SeverityDriver, prior.SeverityDriver) {
		t.Errorf("the driver moved: got %+v, want %+v", got.SeverityDriver, prior.SeverityDriver)
	}
	if !reflect.DeepEqual(got.WeightSource, prior.WeightSource) {
		t.Errorf("the weight source moved: got %+v, want %+v", got.WeightSource, prior.WeightSource)
	}
	if !reflect.DeepEqual(got.RiskFlags, prior.RiskFlags) {
		t.Errorf("the flags moved: got %+v, want %+v", got.RiskFlags, prior.RiskFlags)
	}

	// And the confidence axis DID move.
	if got.ConfidenceBand != gomodels.ConfidenceBandLow {
		t.Errorf("confidence band = %q, want low from the rejected verdict", got.ConfidenceBand)
	}
	if got.ConfidenceScore == prior.ConfidenceScore {
		t.Error("the confidence score did not move, but the verdict did")
	}
	if got.ConfidenceReason == prior.ConfidenceReason {
		t.Error("the confidence reason did not move, but the verdict did")
	}
}

// TestRescoreConfidenceMovesThePriorityTier -- priority is the exception and
// MUST move, because one of its two inputs just did. Leaving the old tier
// would leave the document disagreeing with its own bands.
func TestRescoreConfidenceMovesThePriorityTier(t *testing.T) {
	// A high-severity finding nothing could check: investigate.
	prior := RescoreConfidence(scoredInsight(t), Input{
		Verdict: valmodels.StatusUnverifiable,
		Config:  DefaultConfig(),
	})
	if prior.PriorityTier != gomodels.PriorityTierInvestigate {
		t.Fatalf("tier = %q, want investigate", prior.PriorityTier)
	}

	// An operator re-runs validation and it confirms: the same severity, a
	// trusted verdict, so critical.
	got := RescoreConfidence(prior, Input{
		Verdict: valmodels.StatusConfirmed,
		Config:  DefaultConfig(),
	})
	if got.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("tier = %q, want critical after the verdict confirmed", got.PriorityTier)
	}
	if got.PriorityScore <= prior.PriorityScore {
		t.Errorf("priority score did not rise (%v -> %v) when the tier did",
			prior.PriorityScore, got.PriorityScore)
	}
	if got.SeverityBand != prior.SeverityBand {
		t.Errorf("severity band moved to %q on a re-validation", got.SeverityBand)
	}
}

// TestRescoreConfidenceKeepsThePriorEngineVersion -- the severity half of this
// document was produced by that version and is being kept, so stamping
// today's would claim a calibration scored numbers it never saw.
func TestRescoreConfidenceKeepsThePriorEngineVersion(t *testing.T) {
	prior := scoredInsight(t)
	prior.EngineVersion = 99

	got := RescoreConfidence(prior, Input{Verdict: valmodels.StatusConfirmed, Config: DefaultConfig()})
	if got.EngineVersion != 99 {
		t.Errorf("engine version = %d, want the prior's 99", got.EngineVersion)
	}

	// A prior carrying none at all -- hand-edited, or written by something
	// that did not stamp it -- gets this build's, because an unexplainable 0
	// is worse than a version a reader can look up.
	prior.EngineVersion = 0
	if got := RescoreConfidence(prior, Input{Config: DefaultConfig()}); got.EngineVersion != EngineVersion {
		t.Errorf("engine version = %d, want %d", got.EngineVersion, EngineVersion)
	}
}

// TestRescoreConfidenceIsIdempotent -- the same verdict re-applied changes
// nothing, which is what makes a repeated "Run validation" click safe.
func TestRescoreConfidenceIsIdempotent(t *testing.T) {
	in := Input{Verdict: valmodels.StatusSupported, Config: DefaultConfig()}
	once := RescoreConfidence(scoredInsight(t), in)
	twice := RescoreConfidence(once, in)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("re-scoring twice differed:\n once %+v\ntwice %+v", once, twice)
	}
}

// TestRescoreConfidenceWithAnUnsetConfig -- the same fallback Score has. A
// caller that left the calibration unset must not reach an empty priority
// table, which would price every tier at 0 and leave the sort key saying
// nothing.
func TestRescoreConfidenceWithAnUnsetConfig(t *testing.T) {
	got := RescoreConfidence(scoredInsight(t), Input{Verdict: valmodels.StatusConfirmed})
	if got.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("tier = %q, want critical", got.PriorityTier)
	}
	if got.PriorityScore == 0 {
		t.Error("priority score = 0 with an unset config; the tier table was empty")
	}
}
