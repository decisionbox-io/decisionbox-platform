package models

// QuantifierClaim is one declared quantifier statement: which step, column and
// predicate it rests on.
//
// It lives in this shared module, not beside the evaluator in the agent's
// internal/models, for the reason InsightValidation does: the API decodes a
// stored discovery into its OWN mirror of Insight, and it cannot import an
// internal package. While these types were internal the API model simply had no
// field for them, so BSON decoding dropped the whole audit trail before any
// client saw it -- the JSON tags were unreachable. The agent aliases these, so
// the agent and the API see one struct on the wire.
//
// Claim carries the sentence verbatim, because a failure has to name the text
// that must change rather than the predicate that failed.
type QuantifierClaim struct {
	Claim string `bson:"claim" json:"claim"`
	Kind  string `bson:"kind" json:"kind"`
	Step  int    `bson:"step" json:"step"`

	Column string `bson:"column,omitempty" json:"column,omitempty"`
	Filter string `bson:"filter,omitempty" json:"filter,omitempty"`

	// Scope narrows the rows before the predicate applies, as a filter rather
	// than a ranking. It exists because a step commonly holds several series at
	// once and a claim is about one of them: "Tables ran a loss in every year"
	// rests on a step holding four sub-categories over four years each, and
	// without a scope the only way to declare it is `all` + `profit < 0` over
	// every row -- which asserts that Binders lost money too, and is false.
	//
	// Found by replaying the evaluator over a frozen corpus: two of five refuted
	// claims were true sentences that the grammar could not express, so the
	// evaluator was reporting its own missing vocabulary as the model being
	// wrong. Same class as the absent universal quantifier, and the same cost --
	// a gap here is a false refutation, not a silence.
	Scope string `bson:"scope,omitempty" json:"scope,omitempty"`

	// TopN / TopNColumn narrow the scope before the predicate applies. They
	// exist because the claims that go wrong rank on one column while
	// filtering on another: "the only top-10 revenue line running a loss" is
	// `profit < 0` over the top 10 by `sales`.
	TopN       int    `bson:"top_n,omitempty" json:"top_n,omitempty"`
	TopNColumn string `bson:"top_n_column,omitempty" json:"top_n_column,omitempty"`

	Subject string `bson:"subject,omitempty" json:"subject,omitempty"`
	Rank    int    `bson:"rank,omitempty" json:"rank,omitempty"`
	Count   int    `bson:"count,omitempty" json:"count,omitempty"`
	Order   string `bson:"order,omitempty" json:"order,omitempty"`
	Trend   string `bson:"trend,omitempty" json:"trend,omitempty"`

	// OrderBy names the column that puts a monotonic claim's rows in sequence --
	// the year, the decile, the band. Required for `monotonic`, and the evaluator
	// sorts by it ascending before it looks at Column.
	//
	// It exists because a trend is a statement about an ORDER, and the rows arrive
	// in whatever order the warehouse chose. Nothing asked the model to order its
	// query and nothing checked, so "margin improved each year" over a result
	// returned newest-first read as falling and was refuted -- a true sentence
	// rewritten or deleted because of a clause nobody wrote. Sorting here makes the
	// query's own ordering irrelevant: a badly ordered result now yields the right
	// verdict instead of the wrong one.
	OrderBy string `bson:"order_by,omitempty" json:"order_by,omitempty"`
}

// QuantifierVerdict is what Go concluded about one declared claim, in terms a
// repair prompt can quote back.
//
// Status is "holds", "fails" or "undecidable". Undecidable is the evaluator
// declining -- a missing step, a filter richer than it reads, a capped step
// over which no rank or count is settleable -- and is kept distinct from
// "fails" so the checker's own limits are never reported as the model being
// wrong.
type QuantifierVerdict struct {
	Claim  string `bson:"claim" json:"claim"`
	Kind   string `bson:"kind" json:"kind"`
	Step   int    `bson:"step" json:"step"`
	Status string `bson:"status" json:"status"`
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
}

// Quantifier verdict statuses, declared here beside the verdict they describe.
//
// They were strings in the evaluator's own package and a doc comment here, so
// anything outside the agent that had to read a stored verdict -- the API, the
// scoring engine -- had to spell the literal out again. Named once, aliased by
// the evaluator, and the two cannot drift.
const (
	// QuantifierHolds -- the predicate is true over the step's rows.
	QuantifierHolds = "holds"
	// QuantifierFails -- the predicate is false over the step's rows. The only
	// status that says the claim is wrong.
	QuantifierFails = "fails"
	// QuantifierUndecidable -- the rows cannot settle it. Kept distinct from
	// fails so the evaluator's own limits are never reported as the model
	// being wrong.
	QuantifierUndecidable = "undecidable"
)
