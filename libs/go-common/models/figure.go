package models

// A figure claim is one number the model wrote, together with the arithmetic
// over its evidence that produced it.
//
// It exists because three successive attempts to check figures the other way
// round -- Go extracts the numerals and hunts for them in the rows -- were
// measured on three hand-adjudicated corpora and none was usable. Recall was
// perfect and precision was 7-14%; narrowing by shape reached 23% and cost a
// red-proof; a near-miss test tripled precision and halved recall. The cause is
// structural and no threshold removes it: a numeral in a sound insight is
// typically two or three arithmetic steps from the rows, and the space of
// two-or-three-step derivations over a step's cells contains almost any number.
// One corpus had an exact sum of two cells, an exact sum of three percentages
// and a ratio of two derived ratios all reported as invented, while the
// operation that would have grounded them -- sum of two arbitrary cells -- also
// grounds a known-false figure in another corpus.
//
// So the question moves, the way it did for quantifier claims: the model says
// which step and which arithmetic a figure came from, and Go does the
// arithmetic. There is no unreachability problem left, because the model
// supplies the reach. A figure it will not declare is counted as undeclared
// rather than guessed at.
//
// Lives in this shared module for the reason QuantifierClaim does: the API
// decodes a stored discovery into its own mirror of Insight and cannot import an
// internal package, so a type kept internal loses the whole audit trail to BSON
// before any client sees it.
type FigureClaim struct {
	// Figure is the number as written, "$33.12B" or "17.92%", because a
	// refutation has to name the text that must change rather than the value
	// that failed.
	Figure string `bson:"figure" json:"figure"`

	// Value is what Figure means in the units of the step's own column: 33.12B
	// written as 33116752392, a percentage written as 17.92 rather than 0.1792.
	// Asked for separately because parsing "$33.12B" back to a number is the
	// one part of this the model should not be trusted with, and because the
	// gap between the two is itself worth seeing.
	Value float64 `bson:"value" json:"value"`

	Step int    `bson:"step" json:"step"`
	Kind string `bson:"kind" json:"kind"`

	// Column is the column the arithmetic runs over. Required by every kind
	// except count.
	Column string `bson:"column,omitempty" json:"column,omitempty"`

	// Row selects the single row a cell, a ratio's numerator or a diff's left
	// operand comes from, in the filter grammar parseFilter reads.
	Row string `bson:"row,omitempty" json:"row,omitempty"`

	// Other selects a diff's right operand, in the same grammar as Row.
	Other string `bson:"other,omitempty" json:"other,omitempty"`

	// Scope narrows which rows a sum, a count or a ratio's denominator covers.
	// Empty means every row the step returned.
	Scope string `bson:"scope,omitempty" json:"scope,omitempty"`

	// Pct multiplies a ratio by 100, so a share written "20.1%" is compared
	// against 20.1 rather than 0.201. Without it the same declaration would be
	// refuted by a factor of a hundred, which is an evaluator reporting its own
	// convention as the model being wrong.
	Pct bool `bson:"pct,omitempty" json:"pct,omitempty"`
}

// Figure kinds. Each fixes what Go evaluates and which fields it reads.
//
// Five rather than four because a missing kind does not produce silence, it
// produces the model reaching for the nearest kind it has -- the lesson
// QuantifierAll was added for. "X is N more than Y" is a common enough sentence
// that without FigureDiff it would be declared as a cell.
const (
	// FigureCell — one cell: Column, in the row Row selects.
	FigureCell = "cell"
	// FigureSum — the total of Column over Scope.
	FigureSum = "sum"
	// FigureCount — how many rows are in Scope.
	FigureCount = "count"
	// FigureRatio — Column in the row Row selects, over the total of Column
	// across Scope. Pct scales it to a percentage.
	FigureRatio = "ratio"
	// FigureDiff — Column in Row's row minus Column in Other's row.
	FigureDiff = "diff"
)

// FigureVerdict is what Go concluded about one declared figure.
type FigureVerdict struct {
	Figure string `bson:"figure" json:"figure"`
	Step   int    `bson:"step" json:"step"`
	Kind   string `bson:"kind" json:"kind"`
	Status string `bson:"status" json:"status"`

	// Claimed and Evaluated are both recorded even when they agree, so a
	// measurement over these verdicts can see how far a refutation missed by
	// without re-running the arithmetic.
	Claimed   float64 `bson:"claimed" json:"claimed"`
	Evaluated float64 `bson:"evaluated,omitempty" json:"evaluated,omitempty"`

	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
}

// Figure verdict statuses, mirroring the quantifier ones and for the same
// reason: an evaluator that reports its own limits as the document's errors
// costs more than it catches.
const (
	// FigureHolds — the arithmetic reproduces the written figure.
	FigureHolds = "holds"
	// FigureFails — the arithmetic gives a different number. The only status
	// that says the figure is wrong.
	FigureFails = "fails"
	// FigureUndecidable — the rows cannot settle it: the step is not cited, the
	// column is absent, the filter is richer than the grammar reads, or the row
	// selector matched none or several rows.
	FigureUndecidable = "undecidable"
)

// FigureCoverage counts how much of an insight's prose the declarations reach.
//
// Recorded because the comparable layer's weakness was invisible until it was
// measured by hand: a quarter of insights declared no quantifier claim at all,
// and every verdict those documents carried was therefore about nothing. Here
// the count is mechanical -- the numerals in the prose are extracted and
// compared against the declared set.
//
// The extractor used for Declared/Written is the same one whose 7-14% precision
// disqualified it from driving a rewrite. That is the right job for it: an
// over-extracted numeral costs one spurious Undeclared in a counter, never a
// rewritten sentence, and nothing branches on its opinion about truth.
type FigureCoverage struct {
	// Written is how many distinct numerals the extractor found in name,
	// description and indicators.
	Written int `bson:"written" json:"written"`
	// Declared is how many of those a declaration accounts for, by value.
	Declared int `bson:"declared" json:"declared"`
}
