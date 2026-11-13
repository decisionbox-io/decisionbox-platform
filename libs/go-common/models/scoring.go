package models

// Scoring is the derived scoring sub-document attached to an insight or a
// recommendation.
//
// It lives here, beside the validation and figure types, for the reason those
// do: three processes need it. The agent writes it, the API decodes a stored
// discovery into its OWN mirror of Insight, and both standalone collections
// carry their own copy -- and a field absent from any one of those four structs
// is a field BSON silently drops on the way to a client, however carefully its
// json name was chosen.
//
// Every field is derived. Nothing in here is ever read from model output: the
// scorer clears the sub-document and writes it from the validation verdict and
// the evidence trail, so a model that volunteers a scoring block cannot author
// its own grade. The agent's json tag for the field holding this struct is
// `evidence_scoring` rather than `scoring` for the same reason the quality and
// verdict blocks are not called `quality` and `quantifier_verdicts` -- a key the
// standard decoder would read straight into the field is a key the model can
// write.
//
// A document whose Scoring is nil was written before the scorer existed. That
// is the legacy marker, and it is why the field is a pointer: a value type
// makes "never scored" indistinguishable from "scored to all-zeroes", and the
// dashboard has to tell those apart to label an old insight's model-assigned
// number as legacy rather than render it as a band.
type Scoring struct {
	// ConfidenceBand is the five-value vocabulary a reader sees: high, medium,
	// low, not_confirmed or unverifiable. The band is what ranks and filters;
	// ConfidenceScore is the continuous number behind it, kept because sorting
	// on five buckets loses the order inside each one.
	ConfidenceBand string `bson:"confidence_band,omitempty"   json:"confidence_band,omitempty"`

	// ConfidenceBase is the score the verdict alone earned, before any dock.
	// Stored because the breakdown has to show where the number started: a
	// final of 0.70 is a different statement when it began at 0.80 than when
	// it began at 0.95.
	ConfidenceBase float64 `bson:"confidence_base,omitempty"   json:"confidence_base,omitempty"`

	// ConfidenceScore is the continuous score after every dock, floored. It is
	// also what the document's own `confidence` field carries, so every
	// existing consumer of that number keeps working and starts reading a
	// grounded value.
	ConfidenceScore float64 `bson:"confidence_score,omitempty"  json:"confidence_score,omitempty"`

	// ConfidenceReason is the engine's own words for why the score is what it
	// is. Stored rather than re-derived in each reader, so the dashboard, an
	// API client and the clarifying-questions digest quote one wording instead
	// of three that drift.
	ConfidenceReason string `bson:"confidence_reason,omitempty" json:"confidence_reason,omitempty"`

	// ConfidenceDocks is every deduction the base took, in the order it was
	// applied, each with the amount actually charged. The amounts always sum
	// to exactly ConfidenceBase - ConfidenceScore: a dock the cap or the floor
	// trimmed is stored trimmed, and one trimmed to nothing is not stored at
	// all. Anything else and the breakdown a reader opens does not add up.
	ConfidenceDocks []ConfidenceDock `bson:"confidence_docks,omitempty"  json:"confidence_docks,omitempty"`

	// --- The severity axis. ---------------------------------------------
	//
	// Severity is magnitude x weight, and both halves are derived: the
	// magnitude from figures Go re-evaluated against warehouse rows, the
	// weight from the analysis area. The analysis model's own `severity`
	// word and `risk_score` float are cleared and overwritten, for the
	// reason the confidence number is -- a model grading the importance of
	// the finding it has just written is not a measurement.
	//
	// MagnitudeScore is kept beside SeverityScore rather than recomputed
	// from it, because it is the WEIGHT-FREE half: it is what a comparison
	// across runs has to use, so that changing an area's weight cannot make
	// every finding in that area look as though it got worse.

	MagnitudeScore float64         `bson:"magnitude_score,omitempty" json:"magnitude_score,omitempty"`
	SeverityBand   string          `bson:"severity_band,omitempty"   json:"severity_band,omitempty"`
	SeverityScore  float64         `bson:"severity_score,omitempty"  json:"severity_score,omitempty"`
	SeverityDriver *SeverityDriver `bson:"severity_driver,omitempty" json:"severity_driver,omitempty"`

	// SeverityReason is the engine's own words for the band, stored for the
	// reason ConfidenceReason is: the dashboard, an API client, the
	// clarifying-questions digest and every summary reader quote one wording
	// instead of each composing its own from the driver. Three independent
	// phrasings of "why is this medium" is how a reader ends up with two
	// answers to it.
	SeverityReason string `bson:"severity_reason,omitempty" json:"severity_reason,omitempty"`

	AreaWeight   float64       `bson:"area_weight,omitempty"   json:"area_weight,omitempty"`
	WeightSource *WeightSource `bson:"weight_source,omitempty" json:"weight_source,omitempty"`

	// --- The priority axis. ----------------------------------------------
	//
	// A rollup of the two independent axes through a fixed matrix, so the
	// list can be ordered by one key. No derivation is stored: the matrix
	// cell is recomputable from SeverityBand and ConfidenceBand, which is
	// what a reader is shown.
	//
	// Both are absent on a document with no severity band -- a legacy row,
	// or one an older engine scored on the confidence axis alone. An absent
	// PriorityScore sorts last under a descending sort, which is the honest
	// place for a finding nothing graded.

	PriorityTier  string  `bson:"priority_tier,omitempty"  json:"priority_tier,omitempty"`
	PriorityScore float64 `bson:"priority_score,omitempty" json:"priority_score,omitempty"`

	// RiskFlags is what KIND of risk this finding is: the business-risk
	// category ids its analysis area maps to, in the area's own declaration
	// order.
	//
	// Derived from the area and from nothing the model wrote, like every
	// other field here. Absent on a document whose area maps to no category,
	// which is every document on a deployment that configures no taxonomy --
	// and absent reads as "not flagged" where an empty array would read as
	// "flagged, with nothing".
	RiskFlags []string `bson:"risk_flags,omitempty" json:"risk_flags,omitempty"`

	// EngineVersion is which calibration produced these numbers. Not
	// omitempty: a scored document always carries it, and the breakdown
	// explains itself with the cutoffs of the version that actually scored it
	// rather than whichever version happens to be deployed when it is read.
	EngineVersion int `bson:"engine_version" json:"engine_version"`
}

// ConfidenceDock is one deduction the confidence base took.
//
// Amount is the magnitude charged, always positive: the arithmetic the
// breakdown renders is base minus the sum of the amounts. Label is the
// engine's own plain-words name for the signal, stored on the document so a
// reader and a client see the same sentence without either inventing one.
type ConfidenceDock struct {
	Code   string  `bson:"code"   json:"code"`
	Label  string  `bson:"label"  json:"label"`
	Amount float64 `bson:"amount" json:"amount"`
}

// SeverityDriver is what a reader is shown beside a severity band instead of
// the band on its own: the number the magnitude came from, and the amount the
// finding is about.
//
// It carries TWO figure slots, and both are load-bearing. Amount bands are a
// per-business threshold rather than an engine constant -- "2M is severe" is
// true for one business and meaningless for another -- so they ship
// unconfigured, and an unconfigured engine cannot band an amount at all. What
// it can still do is extract the amount and show it, while the band comes from
// the share. One slot cannot say that: it would have to either hide a grounded
// amount, which is the number the reader most wants, or claim the band came
// from it, which would be false.
//
// Equal pointers are the normal case once amount bands ARE configured: Figure
// and Amount then name the same figure, and a renderer that shows both
// compares FigureID rather than printing it twice.
type SeverityDriver struct {
	// Kind is what banded the magnitude: "amount", "share" or "baseline".
	Kind string `bson:"kind" json:"kind"`

	// Quantified is false only for "baseline" -- no figure this engine would
	// use could be found, so the magnitude is the area baseline and the label
	// says "not quantified". It is NOT a low magnitude: "we could not measure
	// this" is not evidence that the thing is small, and treating it as one is
	// how a real finding gets buried by a model that declared no figure.
	Quantified bool `bson:"quantified" json:"quantified"`

	// Figure is the figure the magnitude was banded from, nil when Kind is
	// "baseline".
	Figure *SeverityFigure `bson:"figure,omitempty" json:"figure,omitempty"`

	// Amount is the largest checked amount this document states in its own
	// home currency, when it states one. Present whether or not it banded the
	// magnitude; nil when the document declares no such amount.
	Amount *SeverityFigure `bson:"amount,omitempty" json:"amount,omitempty"`
}

// SeverityFigure is one figure as a severity driver refers to it.
//
// Display is the figure as the prose renders it ("$8.48B", "24.66%"), taken
// from the verdict rather than re-rendered here, so the number beside the band
// is character-for-character the number in the sentence.
type SeverityFigure struct {
	// FigureID is the reference the insight's own prose carries, so the
	// driver can be pointed at in the text it came from.
	FigureID string `bson:"figure_id,omitempty" json:"figure_id,omitempty"`
	Display  string `bson:"display,omitempty"   json:"display,omitempty"`

	// Value and Unit are the number itself and its notation, in the units of
	// the column it came from -- the same contract Figure uses. Currency is
	// the symbol a currency figure prints.
	Value    float64 `bson:"value,omitempty"    json:"value,omitempty"`
	Unit     string  `bson:"unit,omitempty"     json:"unit,omitempty"`
	Currency string  `bson:"currency,omitempty" json:"currency,omitempty"`

	// Verdict is what Go concluded about this figure's own arithmetic. Always
	// "holds" on a figure the engine would use, and stored anyway: it is the
	// direct answer to the objection that a derived severity could still rest
	// on a number the model made up, and a reader quoting the stored word is
	// not asserting something about the engine that the engine stopped
	// guaranteeing.
	Verdict string `bson:"verdict,omitempty" json:"verdict,omitempty"`
}

// WeightSource records where an area's severity weight came from: the
// per-area override, the risk category it maps to, or the engine default.
//
// Written even when the default applied, because the breakdown has to say
// which weight multiplied the magnitude and what set it. An absent source
// beside a present AreaWeight would leave a reader unable to tell a
// deliberate 1.0 from an unwritten field.
type WeightSource struct {
	Kind     string `bson:"kind,omitempty"     json:"kind,omitempty"`
	Category string `bson:"category,omitempty" json:"category,omitempty"`
}

// Weight sources: what set an area's severity weight.
const (
	WeightSourceDefault      = "default"
	WeightSourceCategory     = "category"
	WeightSourceAreaOverride = "area_override"
)

// RiskCategory is one business-risk category: what it is called, and how much
// a finding in it is worth relative to one in an unweighted area.
//
// It lives here, beside WeightSource, because the taxonomy has to exist in
// four places that cannot all import each other -- the pack model, the project
// model, the agent's own mirror of the project, and the scoring engine, which
// must not import any of the three. Declared once rather than hand-copied,
// for the reason Scoring itself is: a field absent from one copy is a field
// BSON silently drops.
//
// Weight is omitempty so a category that takes the engine default is stored
// as a category rather than as a category weighted zero -- which is not a
// legal weight and would be rejected if it were stated.
type RiskCategory struct {
	ID          string  `bson:"id"                    json:"id"`
	Label       string  `bson:"label"                 json:"label"`
	Description string  `bson:"description,omitempty" json:"description,omitempty"`
	Weight      float64 `bson:"weight,omitempty"      json:"weight,omitempty"`
}

// The confidence band vocabulary.
//
// Three of the five are decided by where the score falls; two are decided by
// the verdict and are terminal, because they are different statements from a
// low score rather than weaker versions of it. "Nothing could check this"
// (unverifiable) and "this was never checked" (not_confirmed) both say the
// evidence is absent; low confidence says the evidence is present and weak.
const (
	ConfidenceBandHigh         = "high"
	ConfidenceBandMedium       = "medium"
	ConfidenceBandLow          = "low"
	ConfidenceBandNotConfirmed = "not_confirmed"
	ConfidenceBandUnverifiable = "unverifiable"
)

// The severity band vocabulary: three values, and no `critical`.
//
// `critical` used to be the top severity word and is now the top PRIORITY
// tier, which is what the matrix calls it. The word moved rather than being
// duplicated, because two vocabularies for one idea leave every filter and
// every counter having to decide which of them it meant. A document written
// before this carries `critical` in its own `severity` field and is rendered
// as a legacy value, never re-interpreted as a tier.
const (
	SeverityBandHigh   = "high"
	SeverityBandMedium = "medium"
	SeverityBandLow    = "low"
)

// Magnitude driver kinds -- what produced the band, in precedence order.
const (
	// SeverityDriverAmount -- a checked currency figure in the document's own
	// home currency, banded by the configured amount bands.
	SeverityDriverAmount = "amount"
	// SeverityDriverShare -- a checked percentage figure.
	SeverityDriverShare = "share"
	// SeverityDriverBaseline -- nothing this engine would use, so the area
	// baseline stands in and the label reads "not quantified".
	SeverityDriverBaseline = "baseline"
)

// The priority tier vocabulary.
//
// Ordered critical > investigate > high > low. `investigate` outranks `high`
// deliberately: a high-severity finding that nothing could check has to
// surface rather than be buried, because severity sets the ceiling and
// confidence only refines inside it. A reader who is shown a severe finding
// labelled "investigate" knows both facts about it; one who is shown it at the
// bottom of the list is shown neither.
const (
	PriorityTierCritical    = "critical"
	PriorityTierInvestigate = "investigate"
	PriorityTierHigh        = "high"
	PriorityTierLow         = "low"
)
