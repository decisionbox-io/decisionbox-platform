package scoring

import (
	"strconv"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// allSeverityBands and allConfidenceBands are the two axes in full, so the
// matrix tests cover every cell rather than the cells someone remembered.
var (
	allSeverityBands = []string{
		gomodels.SeverityBandHigh, gomodels.SeverityBandMedium, gomodels.SeverityBandLow,
	}
	allConfidenceBands = []string{
		gomodels.ConfidenceBandHigh, gomodels.ConfidenceBandMedium,
		gomodels.ConfidenceBandLow, gomodels.ConfidenceBandUnverifiable,
		gomodels.ConfidenceBandNotConfirmed,
	}
)

// TestThePriorityMatrix asserts every cell by name.
//
// Written out rather than generated from the same switch it is testing, which
// would only assert that the code equals itself.
func TestThePriorityMatrix(t *testing.T) {
	for _, tc := range []struct {
		severity, confidence, want string
	}{
		// High severity: the only row where confidence changes the answer, and
		// both answers are near the top of the list.
		{gomodels.SeverityBandHigh, gomodels.ConfidenceBandHigh, gomodels.PriorityTierCritical},
		{gomodels.SeverityBandHigh, gomodels.ConfidenceBandMedium, gomodels.PriorityTierCritical},
		{gomodels.SeverityBandHigh, gomodels.ConfidenceBandLow, gomodels.PriorityTierInvestigate},
		{gomodels.SeverityBandHigh, gomodels.ConfidenceBandUnverifiable, gomodels.PriorityTierInvestigate},
		{gomodels.SeverityBandHigh, gomodels.ConfidenceBandNotConfirmed, gomodels.PriorityTierInvestigate},

		// Medium severity: trusted is worth acting on, untrusted is not worth
		// surfacing over a severe finding.
		{gomodels.SeverityBandMedium, gomodels.ConfidenceBandHigh, gomodels.PriorityTierHigh},
		{gomodels.SeverityBandMedium, gomodels.ConfidenceBandMedium, gomodels.PriorityTierHigh},
		{gomodels.SeverityBandMedium, gomodels.ConfidenceBandLow, gomodels.PriorityTierLow},
		{gomodels.SeverityBandMedium, gomodels.ConfidenceBandUnverifiable, gomodels.PriorityTierLow},
		{gomodels.SeverityBandMedium, gomodels.ConfidenceBandNotConfirmed, gomodels.PriorityTierLow},

		// Low severity: nothing confidence can do. A finding that would not
		// matter if true does not become actionable by being well evidenced.
		{gomodels.SeverityBandLow, gomodels.ConfidenceBandHigh, gomodels.PriorityTierLow},
		{gomodels.SeverityBandLow, gomodels.ConfidenceBandMedium, gomodels.PriorityTierLow},
		{gomodels.SeverityBandLow, gomodels.ConfidenceBandLow, gomodels.PriorityTierLow},
		{gomodels.SeverityBandLow, gomodels.ConfidenceBandUnverifiable, gomodels.PriorityTierLow},
		{gomodels.SeverityBandLow, gomodels.ConfidenceBandNotConfirmed, gomodels.PriorityTierLow},
	} {
		if got := priorityTier(tc.severity, tc.confidence); got != tc.want {
			t.Errorf("(%s severity, %s confidence) = %q, want %q",
				tc.severity, tc.confidence, got, tc.want)
		}
	}

	// And the partition is exhaustive: every pair of real bands produces one
	// of the four tiers, so no cell falls through to "".
	for _, sev := range allSeverityBands {
		for _, conf := range allConfidenceBands {
			if tier := priorityTier(sev, conf); PriorityTierRank(tier) == 0 {
				t.Errorf("(%s, %s) produced %q, which is not a tier", sev, conf, tier)
			}
		}
	}
}

// TestAHighSeverityUnverifiableInsightLandsInInvestigate is the acceptance
// criterion, as its own test.
//
// A severe finding nothing could check is the one a reader most needs to see.
// Sorting by confidence put it at the bottom of the list; this is the assertion
// that it is now near the top, and specifically that it outranks every
// medium-severity confirmed finding -- which is the comparison that makes
// `investigate` sit above `high` rather than below it.
func TestAHighSeverityUnverifiableInsightLandsInInvestigate(t *testing.T) {
	cfg := DefaultConfig()

	tier := priorityTier(gomodels.SeverityBandHigh, gomodels.ConfidenceBandUnverifiable)
	if tier != gomodels.PriorityTierInvestigate {
		t.Fatalf("tier = %q, want investigate", tier)
	}

	// The worst possible investigate finding against the best possible high
	// one: severity at its band floor and confidence at the engine floor,
	// versus a perfect medium-severity confirmed finding.
	worstInvestigate := priorityScore(gomodels.PriorityTierInvestigate, cfg.Severity.MediumAt, cfg.Confidence.Floor, cfg.Priority)
	bestHigh := priorityScore(gomodels.PriorityTierHigh, 1.0, 1.0, cfg.Priority)
	if worstInvestigate <= bestHigh {
		t.Errorf("an unverifiable severe finding (%v) sorted below a confirmed medium one (%v); "+
			"the whole point of the investigate tier is that it does not",
			worstInvestigate, bestHigh)
	}
}

// TestTheConfidenceCollapseIsTwoGroups -- the five bands reduce to "can a
// reader act on this without checking it first".
//
// The three untrusted bands are deliberately NOT equivalent elsewhere: `low`
// means checked and contradicted, `unverifiable` means nothing could check it,
// and `not_confirmed` means nothing tried. They differ in what a reader should
// do next, which the confidence badge says; they agree on this one question.
func TestTheConfidenceCollapseIsTwoGroups(t *testing.T) {
	trusted := []string{gomodels.ConfidenceBandHigh, gomodels.ConfidenceBandMedium}
	untrusted := []string{
		gomodels.ConfidenceBandLow, gomodels.ConfidenceBandUnverifiable,
		gomodels.ConfidenceBandNotConfirmed,
	}
	for _, band := range trusted {
		if !trustedConfidenceBands[band] {
			t.Errorf("%q is not trusted, but a reader can act on it", band)
		}
	}
	for _, band := range untrusted {
		if trustedConfidenceBands[band] {
			t.Errorf("%q is trusted, but nothing confirmed it", band)
		}
	}
	// A band this build does not know -- a document scored by a newer engine --
	// is untrusted. Treating an unrecognised band as trusted would promote a
	// finding on the strength of a word this build cannot read.
	if trustedConfidenceBands["something_newer"] {
		t.Error("an unrecognised confidence band was treated as trusted")
	}
}

// TestThePriorityScoreNeverCrossesATier is the guard the coefficients exist
// under.
//
// The tier decides the order; the two scores only break ties inside it. That
// is arithmetic, not intention: the coefficients total at most 0.20 against a
// tier step of 1.0. A later re-calibration that raised one past 0.5 would
// silently turn the tiers into decoration, and every existing test would still
// pass.
func TestThePriorityScoreNeverCrossesATier(t *testing.T) {
	cfg := DefaultConfig()
	// The extremes of both continuous scores. A score cannot leave [0, 1]:
	// severity is clamped there and confidence is bounded by its floor and
	// its highest base.
	extremes := []float64{0, 0.05, 0.5, 0.95, 1.0}

	worst := map[string]float64{}
	best := map[string]float64{}
	for _, tier := range []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	} {
		first := true
		for _, sev := range extremes {
			for _, conf := range extremes {
				s := priorityScore(tier, sev, conf, cfg.Priority)
				if first || s < worst[tier] {
					worst[tier] = s
				}
				if first || s > best[tier] {
					best[tier] = s
				}
				first = false
			}
		}
	}

	ordered := []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	}
	for i := 0; i+1 < len(ordered); i++ {
		higher, lower := ordered[i], ordered[i+1]
		if worst[higher] <= best[lower] {
			t.Errorf("the worst %s (%v) does not outrank the best %s (%v); "+
				"the refinement coefficients have grown past one tier step",
				higher, worst[higher], lower, best[lower])
		}
	}
}

// TestThePriorityScoreKeepsTheConfidenceRefinement -- the sort key sits on a
// finer grid than the bands precisely so this does not round away.
//
// Snapped to two decimals, a tier-high finding at 50% confidence and one at
// 55% both become 1.12, and the tie-break becomes whatever order the database
// returned.
func TestThePriorityScoreKeepsTheConfidenceRefinement(t *testing.T) {
	cfg := DefaultConfig()
	lower := priorityScore(gomodels.PriorityTierHigh, 0.6, 0.50, cfg.Priority)
	higher := priorityScore(gomodels.PriorityTierHigh, 0.6, 0.55, cfg.Priority)
	if higher <= lower {
		t.Errorf("confidence 0.55 scored %v and 0.50 scored %v; the refinement was rounded away",
			higher, lower)
	}

	// Severity still leads confidence: a better-evidenced finding does not
	// overtake a more severe one inside the same tier.
	severeButDoubted := priorityScore(gomodels.PriorityTierHigh, 1.0, 0.40, cfg.Priority)
	mildButCertain := priorityScore(gomodels.PriorityTierHigh, 0.6, 0.95, cfg.Priority)
	if severeButDoubted <= mildButCertain {
		t.Errorf("inside one tier, confidence (%v) overtook severity (%v)",
			mildButCertain, severeButDoubted)
	}
}

// TestEveryReachablePriorityScoreIsExactAtFourDecimals -- the score is
// persisted and sorted on in the database, so a 17-digit float would be a trap
// for a client comparing it and noise in the index.
func TestEveryReachablePriorityScoreIsExactAtFourDecimals(t *testing.T) {
	cfg := DefaultConfig()
	exact := func(v float64) bool {
		short := strconv.FormatFloat(v, 'f', -1, 64)
		full := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
		return short == full
	}
	checked := 0
	for _, tier := range []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	} {
		// Every multiple of 0.01 in [0, 1] on both axes: the two scores are
		// snapped to that grid, so this is the reachable set in full.
		for sev := 0; sev <= 100; sev++ {
			for conf := 0; conf <= 100; conf++ {
				s := priorityScore(tier, float64(sev)/100, float64(conf)/100, cfg.Priority)
				if !exact(s) {
					t.Fatalf("tier=%s sev=%v conf=%v: score serialises as %s",
						tier, sev, conf, strconv.FormatFloat(s, 'f', -1, 64))
				}
				checked++
			}
		}
	}
	if checked != 4*101*101 {
		t.Fatalf("the generator covered %d cases, not the grid it declares", checked)
	}
}

// TestRecommendationPriorityPreservesTheTierOrder -- the integer is older than
// the tiers and is documented, served and rendered as 1 (highest) to 5
// (lowest), so it is repopulated rather than repurposed.
func TestRecommendationPriorityPreservesTheTierOrder(t *testing.T) {
	ordered := []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	}
	prev := 0
	for _, tier := range ordered {
		got := RecommendationPriority(tier)
		if got <= prev {
			t.Errorf("%s mapped to %d, which does not follow %d; the tier order was lost",
				tier, got, prev)
		}
		if got < 1 || got > 5 {
			t.Errorf("%s mapped to %d, outside the documented 1-5 scale", tier, got)
		}
		prev = got
	}
	// An unrecognised tier is the field's own "unset", not a guess at where
	// it belongs.
	if got := RecommendationPriority("something_newer"); got != 0 {
		t.Errorf("an unknown tier mapped to %d, want 0 (unset)", got)
	}
}

// TestADocumentWithNoSeverityBandGetsNoTier -- a legacy row, or one an older
// engine scored on the confidence axis alone. There is nothing to roll up, and
// inventing a band to roll up would be inventing the one thing this engine
// exists not to invent. An absent score sorts last, which is the honest place
// for a finding nothing graded.
func TestADocumentWithNoSeverityBandGetsNoTier(t *testing.T) {
	got := RescoreConfidence(gomodels.Scoring{EngineVersion: 1}, Input{Config: DefaultConfig()})
	if got.PriorityTier != "" || got.PriorityScore != 0 {
		t.Errorf("tier=%q score=%v, want neither", got.PriorityTier, got.PriorityScore)
	}
}
