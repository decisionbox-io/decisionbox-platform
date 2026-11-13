package scoring

// Business-risk categories, and what a configuration may say about them.
//
// A risk category answers a question the severity band does not: WHAT KIND of
// risk this finding is. The band says how much it matters; the flag says
// whether it is a compliance exposure, a security one, or a doubt about the
// data itself -- which is what a reader filters on and what a weight is
// configured against.
//
// Everything here is engine-owned on purpose. A pack declares its own
// taxonomy, but three categories cut across every domain and are therefore
// not a pack's to omit, and the weight range is the engine's contract rather
// than a pack's preference.

// Global risk category ids: the cross-cutting set every pack may map an area
// to, whether or not it declares a taxonomy of its own.
//
// Three, and no more. Each is a risk class that exists in every domain a pack
// could be written for and that a reader of any domain recognises without
// being taught it -- which is the test for being here rather than in a pack's
// own taxonomy. A domain-specific class ("revenue_leak", "churn") belongs to
// the pack that knows what it means.
const (
	RiskCategoryCompliance  = "compliance"
	RiskCategorySecurity    = "security"
	RiskCategoryDataQuality = "data_quality"
)

// MaxSeverityWeight is the largest weight a configuration may state, and the
// top of the (0, MaxSeverityWeight] range every stated weight must fall in.
//
// Bounded at both ends, and for different reasons. Zero and below are
// excluded because a weight of zero multiplies a magnitude to nothing and
// bands every finding in the category low -- deleting findings from the
// ranking by configuration, silently, in the direction that hides them. The
// ceiling is what keeps a weight a weight: the clamp in scoreSeverity already
// holds risk_score inside its [0, 1] contract, so an unbounded weight would
// not produce a wrong number, it would produce a column of identical 1.00s
// that sorts nothing.
//
// 3.0 because that is the span the ordering needs: it lifts the low magnitude
// (0.25) above the high band cutoff (0.66), which is the whole claim the
// feature makes -- a small finding in a category a business cares about
// outranks a middling one in a category it does not.
const MaxSeverityWeight = 3.0

// GlobalRiskCategories is the cross-cutting set, in a stable order.
//
// A function returning a fresh slice rather than a package var, so a caller
// that appends its pack's own categories to it cannot write into the engine's
// copy.
func GlobalRiskCategories() []string {
	return []string{RiskCategoryCompliance, RiskCategorySecurity, RiskCategoryDataQuality}
}

// ValidSeverityWeight reports whether a stated weight is inside
// (0, MaxSeverityWeight].
//
// The single implementation of the range rule: pack validation rejects a
// weight that fails it, and the engine ignores one that reached it anyway --
// from a hand-edited document, or from a pack saved before the rule existed.
// Two readings of one range is how the validator and the engine come to
// disagree about what a stored document means.
//
// NaN and both infinities fail it without being named: every comparison
// against NaN is false, and an infinity fails the ceiling.
func ValidSeverityWeight(w float64) bool {
	return w > 0 && w <= MaxSeverityWeight
}
