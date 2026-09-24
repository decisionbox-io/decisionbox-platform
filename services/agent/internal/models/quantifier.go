package models

// QuantifierClaim is one declared quantifier statement: which step, column and
// predicate it rests on. The evaluator lives in internal/discovery; the type
// lives here so the API can serve a stored insight without importing it.
//
// Claim carries the sentence verbatim, because a failure has to name the text
// that must change rather than the predicate that failed.
type QuantifierClaim struct {
	Claim string `bson:"claim" json:"claim"`
	Kind  string `bson:"kind" json:"kind"`
	Step  int    `bson:"step" json:"step"`

	Column string `bson:"column,omitempty" json:"column,omitempty"`
	Filter string `bson:"filter,omitempty" json:"filter,omitempty"`

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
