package models

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// A figure is a number the model puts in an insight, emitted as data rather than
// typed into the prose.
//
// The prose carries a reference -- "{{f1}} of 1997 revenue" -- and this struct carries
// the value, the arithmetic over the evidence that produced it, and how it should be
// written. Go renders the reference into text at the end of the analysis phase, so the
// stored name and description read exactly as they would have if the model had typed
// the number.
//
// The point of the indirection is that **no number is ever recovered by parsing prose.**
// Three earlier designs did parse it, and each failed in a way that traces back to the
// parse rather than to the checking:
//
//   - Enumerating numerals with a regex to find which were checkable ran at 7-14%
//     precision, and its residual was honest arithmetic it could not reach.
//   - Reading a figure's precision back out of its own text produced false refutations
//     on "5.0% of $34.86B", "1997 Q4 $8.61B" and "1.234.567", because the first
//     numeral in a phrase need not be the quantity being checked. One of those went on
//     to rewrite the wrong numeral and report the insight as holding.
//   - Matching a prose numeral to its declaration by value needed a 1% tolerance, and
//     over 50 replayed insights 21 pairs of distinct declared figures sat inside that
//     tolerance of each other. The link was ambiguous by construction.
//
// Every one of those disappears when the figure is data and the prose points at it.
// Because Go renders, Go knows the precision exactly instead of inferring it; because
// the prose holds a reference, a correction updates one value and every mention
// follows; and because a figure cannot appear in the prose without a reference, there
// is nothing to enumerate.
type Figure struct {
	// ID is what the prose references, "f1". Unique within one insight.
	ID string `bson:"id" json:"id"`

	// Value is the number, in the units of the step's own column: 8476238553 for
	// $8.48B, 24.66 for a percentage written as 24.66%.
	Value float64 `bson:"value" json:"value"`

	// Unit, Scale and Decimals are how the figure is written. Go renders from them,
	// which is the whole reason the precision is known rather than parsed: the
	// interval a figure claims is half the last place Go printed, and Go chose it.
	Unit string `bson:"unit,omitempty" json:"unit,omitempty"`

	// Currency is the symbol a currency figure prints, "$" by default.
	//
	// Here because Go took over the rendering. While the model typed its own numbers it
	// typed its own symbol with them, and a euro-denominated warehouse got euros; once Go
	// rendered from `unit: "currency"` alone, every amount became dollars regardless of the
	// data. That is a regression this format introduced, not a limitation it inherited.
	//
	// Presentation belongs to the model and rendering to Go, the same division as unit,
	// scale and decimals -- so the model says which symbol and Go writes it. Defaulting to
	// "$" keeps the common case unchanged rather than silently dropping the symbol.
	Currency string `bson:"currency,omitempty" json:"currency,omitempty"`
	Scale    string `bson:"scale,omitempty" json:"scale,omitempty"`
	Decimals int    `bson:"decimals,omitempty" json:"decimals,omitempty"`

	// Approx marks a figure the prose rounds on purpose -- "~911K lines". It widens
	// nothing in the check; it only adds the tilde when rendering, so the reader is
	// told the number is approximate and the arithmetic is still held to the
	// precision actually printed.
	Approx bool `bson:"approx,omitempty" json:"approx,omitempty"`

	// The arithmetic. Same closed grammar the quantifier evaluator's filters use, and
	// the same kinds as before, because that part of the design measured well: over
	// 345 declarations the evaluator produced 3 undecidables and no refutation that
	// was not traceable to a defect in its own coverage.
	Step   int    `bson:"step" json:"step"`
	Kind   string `bson:"kind" json:"kind"`
	Column string `bson:"column,omitempty" json:"column,omitempty"`
	Row    string `bson:"row,omitempty" json:"row,omitempty"`
	Other  string `bson:"other,omitempty" json:"other,omitempty"`
	Scope  string `bson:"scope,omitempty" json:"scope,omitempty"`

	// Refs is the second grammar this type carries, and the only one a recommendation
	// can use.
	//
	// A recommendation is never shown warehouse rows -- it is given the insights and
	// nothing else -- so it cannot declare arithmetic over a step. What it can do is
	// point at a figure an insight already declared and Go already checked, which is
	// what nearly every number in a recommendation is: over four adjudicated runs, 602
	// of 638 numerals in recommendation prose were restatements of an insight's number.
	//
	// The two grammars share this struct because the expensive half is shared -- the
	// rendering, the interval that follows from having rendered, the verdict, the
	// correction record. Only resolution differs: Step/Column/Row resolve against rows,
	// Refs resolve against figures. Which one applies is decided by the document rather
	// than by the fields, so there is no mode to get wrong: an insight resolves steps,
	// a recommendation resolves references.
	Refs []FigureRef `bson:"refs,omitempty" json:"refs,omitempty"`

	// ValueMissing records that the model declared no readable value for this figure --
	// the key absent, or holding something like "1.2M" that no number can be recovered
	// from. Set by UnmarshalJSON, and neither stored nor served: a figure read back from
	// the database has a real value by construction, and the flag is only needed between
	// decoding a response and settling it in the same process.
	//
	// It exists because a missing value and a value of zero are not the same claim, and
	// treating them alike printed a fabricated number into the prose. An insight figure
	// with no value became a claim of 0, which the correction gate then refused to replace
	// because 0 is more than 1% from any real total, so "0" shipped in the sentence. The
	// recommendation side had the same defect from the other direction and was fixed
	// first; this is the same distinction, drawn once and used by both.
	ValueMissing bool `bson:"-" json:"-"`
}

// FigureRef points at a figure another document declared and Go already settled.
//
// Both halves are needed because a recommendation draws on several insights and figure
// ids are only unique within one of them: every insight has an f1.
type FigureRef struct {
	// Insight is the referenced insight's id, copied verbatim from the input -- the
	// same id related_insight_ids carries.
	Insight string `bson:"insight" json:"insight"`
	// Figure is the figure's id within that insight, "f2".
	Figure string `bson:"figure" json:"figure"`
}

// Figure kinds. Each fixes what Go evaluates and which fields it reads.
const (
	// FigureCell — one cell: Column, in the row Row selects.
	FigureCell = "cell"
	// FigureSum — the total of Column over Scope.
	FigureSum = "sum"
	// FigureCount — how many rows are in Scope.
	FigureCount = "count"
	// FigureRatio — Column in Row's row, over the same column in Other's row when
	// Other is given, and over the column total across Scope when it is not.
	FigureRatio = "ratio"
	// FigureDiff — Column in Row's row minus Column in Other's row.
	FigureDiff = "diff"

	// FigureRefKind — one figure another document already declared, restated. Refs
	// holds exactly one reference and no value is read from the figure itself: Go
	// takes the checked value from the reference and writes it with this figure's
	// notation. A recommendation kind.
	//
	// It is the reason a restatement cannot be mistyped and a headline cannot
	// contradict its own body -- both are the same reference, rendered twice.
	FigureRefKind = "ref"
)

// The kinds a recommendation may declare. Two, and both are there because the
// measurement asked for them: `ref` covers the 94% of recommendation numerals that
// restate an insight, and `sum` covers the one class of fresh arithmetic that has
// actually shipped a false number -- a total over several insight figures.
//
// Deliberately no others. The rule the quantifier work arrived at holds here too: a
// missing kind is not a gap in coverage, it is a false positive waiting for the model to
// approximate something into it. A projection ("a 3% conversion yields ~1,500 buyers")
// and a policy parameter ("a 45-60 day track") are not measurements, and giving them a
// kind would make an estimate look checked when only its arithmetic was.
var RecommendationFigureKinds = map[string]bool{
	FigureRefKind: true,
	FigureSum:     true,
}

// Units. A closed set, because Go renders from it and an unknown unit would have to
// be guessed at.
const (
	UnitCount    = "count"    // 8,668
	UnitCurrency = "currency" // $8.48B
	UnitPercent  = "percent"  // 24.66%
	UnitMultiple = "multiple" // 1.008x
	UnitPlain    = "plain"    // 25.52

	// There is deliberately no unit for a word like "days".
	//
	// A unit here is part of the number's NOTATION -- a currency symbol, a percent
	// sign, a thousands separator. A word is prose, and prose is the template's job:
	// the first live run rendered "{{f1}} days" with a days unit and shipped
	// "180.1 days days". The sentence already said it.
)

// Scales. The suffix a figure is written with, and the divisor that goes with it.
const (
	ScaleNone      = ""
	ScaleThousands = "thousands" // K
	ScaleMillions  = "millions"  // M
	ScaleBillions  = "billions"  // B
)

// FigureVerdict is what Go concluded about one figure's declared arithmetic.
type FigureVerdict struct {
	// ID and Display name the figure both ways: the reference a template carries and
	// the text a reader sees.
	ID      string `bson:"id" json:"id"`
	Display string `bson:"display" json:"display"`

	Step   int    `bson:"step" json:"step"`
	Kind   string `bson:"kind" json:"kind"`
	Status string `bson:"status" json:"status"`

	// Claimed and Evaluated are both recorded even when they agree, so a measurement
	// over these verdicts can see how far a refutation missed by without re-running
	// the arithmetic.
	Claimed   float64 `bson:"claimed" json:"claimed"`
	Evaluated float64 `bson:"evaluated,omitempty" json:"evaluated,omitempty"`

	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`

	// Resolved says a value was actually produced for this figure, which is not the
	// same as the figure holding. A recommendation figure whose reference points at an
	// insight figure Go declined to vouch for is undecidable AND resolved: the number
	// exists and is carried into the prose, because the alternative -- discovered by
	// shipping it -- is a sentence reading "$11.48B — 0.00% of gross $229.58B" where a
	// model told not to write a value wrote none and the absence rendered as zero.
	Resolved bool `bson:"resolved,omitempty" json:"resolved,omitempty"`
}

// Figure verdict statuses. An evaluator that reports its own limits as the document's
// errors costs more than it catches, so anything it cannot settle is undecidable and
// never a refutation.
const (
	// FigureHolds — the arithmetic reproduces the figure at the precision it is
	// written to.
	FigureHolds = "holds"
	// FigureFails — the arithmetic gives a different number. The only status that says
	// the figure is wrong.
	FigureFails = "fails"
	// FigureUndecidable — the rows cannot settle it: the step is not cited, the column
	// is absent, the filter is richer than the grammar reads, or the row selector
	// matched none or several rows.
	FigureUndecidable = "undecidable"
)

// FigureCorrection records a figure whose value Go replaced with the one its own
// declared arithmetic produced.
//
// Correcting is a re-render rather than a text edit, which is the second thing the
// format buys. Under the old design a correction had to find every standalone
// occurrence of a numeral across the name, the description, the indicators and the
// claim texts, agree that they all measured the same thing, and rewrite each -- and a
// measured failure of exactly that rewrote the wrong numeral in a compound phrase and
// then reported the insight as holding. Here the prose holds a reference, so one value
// changes and every mention of it follows, identically and by construction.
type FigureCorrection struct {
	ID   string  `bson:"id" json:"id"`
	From float64 `bson:"from" json:"from"`
	To   float64 `bson:"to" json:"to"`
	// Text is the substitution as a reader sees it, "$34.36B -> $34.37B".
	Text string `bson:"text" json:"text"`
}

// FigureTemplate keeps the prose as the model authored it, references intact, after
// the rendered text has been written into the insight's own fields.
//
// Kept because the rendered fields are what every reader downstream consumes -- the
// API, the dashboard, the exec summary -- and none of them should have to know this
// format exists. The templates are the audit trail: they are what makes it checkable
// after the fact that a figure in the prose came from a declaration rather than from
// the model typing a number.
type FigureTemplate struct {
	Name        string   `bson:"name,omitempty" json:"name,omitempty"`
	Description string   `bson:"description,omitempty" json:"description,omitempty"`
	Indicators  []string `bson:"indicators,omitempty" json:"indicators,omitempty"`

	// Claims is the quantifier claim text as authored, references intact.
	//
	// Here because the claim is rendered as well, and it has to be: the quantifier
	// contract asks for the claim verbatim as written in the prose, the figure contract
	// has the prose carrying references, and every consumer matches claim text against
	// the rendered fields above. Leaving it unrendered meant a refuted sentence could
	// survive while its declaration was recorded as withdrawn.
	Claims []string `bson:"claims,omitempty" json:"claims,omitempty"`
}

// RecommendationFigureTemplate keeps a recommendation's prose as the model authored it,
// references intact, after the rendered text has been written into its own fields.
//
// A separate type from FigureTemplate rather than a reuse of it, because the audit trail
// is only worth keeping if it says which field a reference was in, and a recommendation's
// fields are not an insight's. Flattening title/actions/impact into name/indicators would
// make the record ambiguous in exactly the place it is consulted.
type RecommendationFigureTemplate struct {
	Title       string   `bson:"title,omitempty" json:"title,omitempty"`
	Description string   `bson:"description,omitempty" json:"description,omitempty"`
	Actions     []string `bson:"actions,omitempty" json:"actions,omitempty"`

	ImpactMetric      string `bson:"impact_metric,omitempty" json:"impact_metric,omitempty"`
	ImpactImprovement string `bson:"impact_improvement,omitempty" json:"impact_improvement,omitempty"`
	ImpactReasoning   string `bson:"impact_reasoning,omitempty" json:"impact_reasoning,omitempty"`
}

// UnmarshalJSON decodes a figure tolerantly.
//
// Small and open models emit numbers as strings -- `"value": "100"`, `"step": "48"` -- and
// the insight and recommendation decoders have coerced that since issue #342, where one
// off-typed field failed a whole batch and silently zeroed an area's findings. Figures are
// nested inside those structs and so were decoded strictly regardless, which put the same
// hole back: one mistyped figure discards the entire insight it belongs to, and the
// claim-dropping salvage path cannot rescue it because the failure is not in the claims.
//
// Only JSON decoding is customised; BSON is untouched, so stored figures read back
// unchanged, and well-typed input from a large model decodes to identical values.
func (f *Figure) UnmarshalJSON(data []byte) error {
	type alias Figure
	aux := &struct {
		Value    json.RawMessage `json:"value"`
		Step     json.RawMessage `json:"step"`
		Decimals json.RawMessage `json:"decimals"`
		*alias
	}{alias: (*alias)(f)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	var ok bool
	f.Value, ok = flexFloatOK(aux.Value)
	f.ValueMissing = !ok
	f.Step = int(flexFloat(aux.Step))
	f.Decimals = int(flexFloat(aux.Decimals))
	return nil
}

// flexFloat reads a number that may have been written as a string, with thousands
// separators, or omitted. An unreadable value yields 0, which the evaluator then reports as
// undecidable rather than refuting -- the same choice made everywhere else in this layer.
func flexFloat(raw json.RawMessage) float64 {
	v, _ := flexFloatOK(raw)
	return v
}

// flexFloatOK reads a number that may have been written as a string, and says whether one
// was actually there. The bool is the whole point: a caller that cannot tell "absent" from
// "zero" turns missing data into a reported measurement.
func flexFloatOK(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	if raw[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, false
		}
		str = strings.TrimSpace(strings.ReplaceAll(str, ",", ""))
		str = strings.TrimPrefix(str, "$")
		str = strings.TrimSuffix(str, "%")
		v, err := strconv.ParseFloat(str, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// MaxFigureDecimals bounds the decimal places a figure may be written to.
//
// Not a style limit -- a resource one. Decimals arrives from model output, and
// strconv.FormatFloat allocates in proportion to the precision asked for, so a response
// carrying `"decimals": 1000000000` builds a gigabyte-scale string and the slack calculation
// loops a billion times, all before any evidence is looked at. Six places is past anything a
// currency, a share or a multiple needs, and the clamp is applied at rendering rather than
// only at decode so a figure read back from storage is bounded too.
const MaxFigureDecimals = 6

// Places is the decimal precision to render this figure at, bounded.
//
// Negative is clamped to zero rather than passed through: FormatFloat reads a negative
// precision as "the smallest number of digits necessary to represent the value uniquely",
// which is a different contract from the one this layer rests on -- the interval a figure
// claims is half the last place it printed, and that is only knowable if the count of places
// is the count that was asked for.
// Symbol is the currency symbol this figure prints.
func (f Figure) Symbol() string {
	if c := strings.TrimSpace(f.Currency); c != "" {
		return c
	}
	return "$"
}

func (f Figure) Places() int {
	switch {
	case f.Decimals < 0:
		return 0
	case f.Decimals > MaxFigureDecimals:
		return MaxFigureDecimals
	}
	return f.Decimals
}
