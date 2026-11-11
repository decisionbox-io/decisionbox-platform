package scoring

import (
	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// Priority: one key to order a run's findings by, rolled up from the two
// independent axes.
//
// A run surfaces 20-40 insights at once and the two things a reader needs to
// know about each are orthogonal -- is it true, and does it matter if it is.
// Sorting by either alone gets one of them wrong: by confidence, a
// well-evidenced triviality leads the list; by severity, a severe claim the
// warehouse could not check leads it. The matrix keeps both facts and produces
// an order.
//
// Severity sets the ceiling and confidence refines inside it. That asymmetry
// is the whole design: a low-severity finding is low priority however
// impeccably evidenced, because nothing follows from acting on it, while a
// high-severity finding nothing could check is worth a look precisely because
// nobody knows yet.

// trustedConfidenceBands is the confidence half of the matrix, collapsed.
//
// Two groups rather than five columns, because the matrix answers one question
// -- can a reader act on this without checking it first -- and the five bands
// give three different reasons for "no" that all lead to the same cell. The
// three untrusted bands are deliberately NOT equivalent elsewhere: `low` means
// checked and contradicted, `unverifiable` means nothing could check it, and
// `not_confirmed` means nothing tried. They differ in what a reader should do
// next, which is what the confidence badge says; they agree on whether the
// finding can be taken at face value, which is all this matrix asks.
var trustedConfidenceBands = map[string]bool{
	gomodels.ConfidenceBandHigh:   true,
	gomodels.ConfidenceBandMedium: true,
}

// priorityTier is the matrix cell for one pair of bands.
//
//	severity \ confidence   trusted        untrusted
//	high                    critical       investigate
//	medium                  high           low
//	low                     low            low
func priorityTier(severityBand, confidenceBand string) string {
	trusted := trustedConfidenceBands[confidenceBand]
	switch severityBand {
	case gomodels.SeverityBandHigh:
		if trusted {
			return gomodels.PriorityTierCritical
		}
		// Investigate, and it outranks the `high` tier below. A high-severity
		// finding nobody has been able to check is the one a reader most
		// needs to see, and ranking it under a medium-severity confirmed one
		// buries it -- which is the behaviour sorting by confidence alone
		// produced.
		return gomodels.PriorityTierInvestigate
	case gomodels.SeverityBandMedium:
		if trusted {
			return gomodels.PriorityTierHigh
		}
		return gomodels.PriorityTierLow
	default:
		// Low severity, either way. A finding that would not matter if it
		// were true does not become actionable by being well evidenced, so
		// there is no cell here for confidence to improve. The continuous
		// score still orders these against each other.
		return gomodels.PriorityTierLow
	}
}

// priorityScore is the sort key: the tier, refined by the two continuous
// scores.
//
// The tier decides the order and the scores only break ties inside it. That is
// an arithmetic property rather than an intention: the coefficients total at
// most 0.20 against a tier step of 1.0, so no amount of severity or confidence
// can lift a finding over the tier above it. Pinned by
// TestThePriorityScoreNeverCrossesATier, because a later re-calibration that
// raised a coefficient past 0.5 would silently turn the tiers into decoration.
func priorityScore(tier string, severityScore, confidenceScore float64, cfg PriorityConfig) float64 {
	return roundSteps(cfg.TierWeights[tier]+
		cfg.SeverityCoeff*severityScore+
		cfg.ConfidenceCoeff*confidenceScore, priorityScoreSteps)
}

// priorityScoreSteps is the grid the sort key sits on, and it is FINER than
// the one the bands use.
//
// Deliberately. The two inputs are multiples of 0.01 and the two coefficients
// are too, so every reachable score is a multiple of 0.0005 -- exact at four
// decimals. Snapping to two instead would round the confidence refinement
// away: a tier-high finding at 50% confidence and one at 55% both become 1.12,
// which turns the tie-break into a coin toss decided by whatever order the
// database returned. This number is a sort key and is never rendered as
// arithmetic a reader checks, so the finer grid costs nothing and keeps the
// ordering the coefficients were chosen for.
const priorityScoreSteps = 10000

// RecommendationPriority projects a tier onto the 1-5 integer a
// recommendation's own `priority` field has always carried.
//
// The field is older than the tiers and is documented, served and rendered as
// 1 (highest) to 5 (lowest), so it is repopulated rather than repurposed: the
// tier order is preserved inside the existing scale and 5 is left unused,
// which is honest about there being four tiers and not five. A tier this build
// does not recognise yields 0, the field's own "unset" value, rather than a
// guess at where it belongs.
func RecommendationPriority(tier string) int {
	switch tier {
	case gomodels.PriorityTierCritical:
		return 1
	case gomodels.PriorityTierInvestigate:
		return 2
	case gomodels.PriorityTierHigh:
		return 3
	case gomodels.PriorityTierLow:
		return 4
	default:
		return 0
	}
}

// PriorityTierRank orders the four tiers, highest first, for a caller that has
// to sort or count by tier without re-deriving the score: the run-completed
// notification's tier counts and the top-insight briefs.
//
// Exported so those call sites read the engine's own ordering instead of
// keeping a second copy of it, which is how the dashboard's ledger rank tables
// came to disagree with the ledger's own.
func PriorityTierRank(tier string) int {
	switch tier {
	case gomodels.PriorityTierCritical:
		return 4
	case gomodels.PriorityTierInvestigate:
		return 3
	case gomodels.PriorityTierHigh:
		return 2
	case gomodels.PriorityTierLow:
		return 1
	default:
		return 0
	}
}
