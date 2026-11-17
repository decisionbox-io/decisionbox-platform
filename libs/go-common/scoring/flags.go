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

// riskFlags is the business-risk flags an insight in this area carries: the
// area's own category ids, in the order the area declares them, deduped, with
// anything the configuration does not know dropped.
//
// Area-derived and nothing else. There is deliberately no input here that a
// model could write: a flag says what KIND of risk a finding is, which is a
// property of the area the finding was produced in, not a claim the analysis
// model is entitled to make about its own work -- the same reason the severity
// word and the confidence number stopped being its to write.
//
// `declared` is the resolved weight map, whose keys are the category ids a
// configuration declared. One map rather than a second set of known ids,
// because validation requires every declared category to state a weight, so
// the two would hold the same ids and could only differ by going wrong. The
// engine's global ids are known whether or not a configuration mentions them,
// which is what makes the cross-cutting set reachable for a pack that declares
// no taxonomy of its own.
//
// An id nothing declares is dropped SILENTLY, and that is the honest answer
// rather than a shortcut: pack validation rejects such an id at save time, so
// one reaching the engine came from a hand-edited document -- and this is a
// pure function with nothing to report through. Carrying it into the flags
// would render an unknown word as a chip beside the finding.
//
// Returns nil, never an empty slice, for an area with no categories: the field
// is omitempty, and an empty array on the wire would read as "flagged, with
// nothing" where absent reads as "not flagged".
func riskFlags(areaCategories []string, declared map[string]float64) []string {
	if len(areaCategories) == 0 {
		return nil
	}
	global := make(map[string]bool, 3)
	for _, id := range GlobalRiskCategories() {
		global[id] = true
	}

	var out []string
	seen := make(map[string]bool, len(areaCategories))
	for _, id := range areaCategories {
		if seen[id] {
			continue
		}
		_, isDeclared := declared[id]
		if !isDeclared && !global[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
