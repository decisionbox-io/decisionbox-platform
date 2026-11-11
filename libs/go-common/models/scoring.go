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

	// --- Declared, never written in this version. -----------------------
	//
	// Severity and risk_score are still the analysis model's own words, and
	// the scorer leaves them alone -- clearing them would blank the only
	// values that exist for them. The fields below are the severity, weight
	// and priority half of the same sub-document, declared here so that
	// grounding them later adds logic rather than a schema change, and so the
	// mirror guard covers them from the day the sub-document ships. They cost
	// nothing while unset: every one is omitempty.

	MagnitudeScore float64         `bson:"magnitude_score,omitempty" json:"magnitude_score,omitempty"`
	SeverityBand   string          `bson:"severity_band,omitempty"   json:"severity_band,omitempty"`
	SeverityScore  float64         `bson:"severity_score,omitempty"  json:"severity_score,omitempty"`
	SeverityDriver *SeverityDriver `bson:"severity_driver,omitempty" json:"severity_driver,omitempty"`
	AreaWeight     float64         `bson:"area_weight,omitempty"     json:"area_weight,omitempty"`
	WeightSource   *WeightSource   `bson:"weight_source,omitempty"   json:"weight_source,omitempty"`
	PriorityTier   string          `bson:"priority_tier,omitempty"   json:"priority_tier,omitempty"`
	PriorityScore  float64         `bson:"priority_score,omitempty"  json:"priority_score,omitempty"`
	RiskFlags      []string        `bson:"risk_flags,omitempty"      json:"risk_flags,omitempty"`

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

// SeverityDriver names the number that drove a grounded magnitude -- the
// amount or the share a reader should be shown beside the band, rather than
// the band on its own.
//
// Declared with the sub-document and written by nothing in this version, for
// the reason the severity fields above are.
type SeverityDriver struct {
	// FigureID is the figure the magnitude was computed from, and Display is
	// that figure as it reads in the prose, so the driver can be pointed at
	// without re-rendering it.
	FigureID string `bson:"figure_id,omitempty" json:"figure_id,omitempty"`
	Display  string `bson:"display,omitempty"   json:"display,omitempty"`

	// Value and Unit are the number itself and its notation, in the units of
	// the column it came from -- the same contract Figure uses.
	Value float64 `bson:"value,omitempty" json:"value,omitempty"`
	Unit  string  `bson:"unit,omitempty"  json:"unit,omitempty"`
}

// WeightSource records where an area's severity weight came from: the
// per-area override, the risk category it maps to, or the default. Declared
// with the sub-document and written by nothing in this version.
type WeightSource struct {
	Kind     string `bson:"kind,omitempty"     json:"kind,omitempty"`
	Category string `bson:"category,omitempty" json:"category,omitempty"`
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
