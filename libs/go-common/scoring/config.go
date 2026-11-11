// Package scoring derives an insight's or a recommendation's grade from the
// evidence the run already produced, rather than from the analysis model's own
// opinion of its work.
//
// It lives under libs/go-common because three processes need it -- the agent
// writes the scores, the API serves the calibration that produced them, and
// neither can reach the other's internal tree. The same reason
// models/validation lives there.
//
// Everything here is pure: no Mongo, no context, no clock, no LLM. Score reads
// a struct and returns a struct, which is what lets the calibration be replayed
// over stored runs without re-running anything.
package scoring

import (
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// EngineVersion is the calibration this build produces. It is stamped on every
// document the scorer writes.
//
// A re-calibration is a version bump with a new entry in the table below, not
// an edit of this one -- a document has to be explainable with the cutoffs that
// actually scored it, and rewriting version 1's numbers would make every
// already-stored breakdown a lie.
const EngineVersion = 1

// Dock codes. The code is the stable machine name; the label beside it in the
// config is the words a reader sees, and is stored on each document so nothing
// downstream has to re-word it.
const (
	// DockDegradedEvidence -- a source said the rows this insight was computed
	// from are not a faithful answer to the query that produced them. Charged
	// once per distinct caveat kind, so withheld-and-sampled costs more than
	// withheld twice.
	DockDegradedEvidence = "degraded_evidence"

	// DockRefutedArithmetic -- a figure's declared arithmetic did not
	// reproduce the number, and the repair pass could not fix it. The verdicts
	// are re-settled after any correction Go made, so a stored `fails` is a
	// figure still wrong in the prose that shipped.
	DockRefutedArithmetic = "refuted_arithmetic"

	// DockRefutedQuantifier -- a declared "only", rank, count or trend is
	// false over the rows it cites.
	DockRefutedQuantifier = "refuted_quantifier"

	// DockUnresolvedFigure -- the published prose still carries a {{ref}}
	// placeholder, so a reader sees a hole where a number was promised.
	DockUnresolvedFigure = "unresolved_figure"

	// DockSampleEndedEarly -- a validation agent asked for rows past the end
	// of the retained sample and got none, so the evidence it could reach was
	// narrower than the evidence that exists.
	DockSampleEndedEarly = "sample_ended_early"
)

// Config is one engine version's calibration, in full. Every number the engine
// uses is declared here and nowhere else, so a call site never carries a
// literal and a re-calibration is a diff in one table.
type Config struct {
	// Version is the engine version this calibration IS, so a Config in hand
	// always knows what to stamp.
	Version int

	Confidence ConfidenceConfig
}

// ConfidenceConfig is the confidence axis: where a verdict starts, what the
// evidence trail takes off it, and where the bands fall.
type ConfidenceConfig struct {
	// Bases is what each validation verdict is worth before any deduction.
	Bases map[valmodels.Status]float64

	// Default is the base for a document that carries no verdict at all --
	// validation turned off, the run's budget reached, a legacy status, or
	// nothing stamped. Deliberately low and deliberately NOT a band of its
	// own by score: such a document lands in the terminal not_confirmed band,
	// because "never checked" is a different statement from "checked and
	// weak".
	Default float64

	// HighAt and MediumAt are the band cutoffs, inclusive: a score at exactly
	// HighAt is high.
	HighAt   float64
	MediumAt float64

	// Floor is the lowest score any document can reach. Zero would read as
	// "no confidence at all", which is a stronger claim than this engine is
	// entitled to make about a document a warehouse merely contradicted.
	Floor float64

	Docks DockConfig
}

// DockSpec is one deduction: its code, the words a reader sees, and what it
// costs.
type DockSpec struct {
	Code   string
	Label  string
	Amount float64
}

// DockConfig is every deduction and the two caps over them.
//
// The caps exist because the signals are not independent. Three quality
// caveats on one insight are mostly one fact about one source, and a refuted
// figure usually travels with a refuted claim about it -- charging each in full
// would push an otherwise-supported finding below a rejected one.
type DockConfig struct {
	DegradedEvidence DockSpec
	// DegradedEvidenceCap bounds the quality docks in total, however many
	// distinct caveat kinds the sources reported.
	DegradedEvidenceCap float64

	RefutedArithmetic DockSpec
	RefutedQuantifier DockSpec
	// RefutedCap bounds the two refutation docks TOGETHER. Applied in dock
	// order, so the arithmetic dock is charged in full and the quantifier one
	// is what gets trimmed.
	RefutedCap float64

	UnresolvedFigure DockSpec
	SampleEndedEarly DockSpec
}

// DefaultConfig is the current engine version's calibration.
//
// The bases are spread so that the verdict alone separates the five outcomes a
// run actually produces, and so that one dock is enough to move a supported
// finding out of high without being enough to move a confirmed one. Calibrated
// against stored runs rather than chosen: the model's own number occupied
// 0.75-0.99 with a mean of 0.93, which is a quarter of the range correlating
// with nothing.
func DefaultConfig() Config {
	return Config{
		Version: EngineVersion,
		Confidence: ConfidenceConfig{
			Bases: map[valmodels.Status]float64{
				// Row evidence directly equal to the claim. Rare, and the only
				// base a single dock cannot move out of high.
				valmodels.StatusConfirmed: 0.95,
				// The default positive verdict: evidence consistent with the
				// claim. High, and exactly one dock from medium.
				valmodels.StatusSupported: 0.80,
				// Some claims checked, or one side of the pair incomplete.
				valmodels.StatusPartial: 0.55,
				// The warehouse contradicted a headline claim. At the floor:
				// there is no evidence trail that rehabilitates this.
				valmodels.StatusRejected: 0.05,
				// Nothing could be checked. Low as a number, and rendered as
				// its own band rather than as low confidence.
				valmodels.StatusUnverifiable: 0.20,
			},
			Default:  0.25,
			HighAt:   0.75,
			MediumAt: 0.40,
			Floor:    0.05,
			Docks: DockConfig{
				DegradedEvidence: DockSpec{
					Code:   DockDegradedEvidence,
					Label:  "A source reported the rows behind this are not a faithful answer to the query",
					Amount: 0.10,
				},
				DegradedEvidenceCap: 0.20,
				RefutedArithmetic: DockSpec{
					Code:   DockRefutedArithmetic,
					Label:  "A number's own arithmetic does not reproduce it",
					Amount: 0.10,
				},
				RefutedQuantifier: DockSpec{
					Code:   DockRefutedQuantifier,
					Label:  "A claim about rank, count or trend is false over the rows it cites",
					Amount: 0.10,
				},
				RefutedCap: 0.15,
				UnresolvedFigure: DockSpec{
					Code:   DockUnresolvedFigure,
					Label:  "The text still carries a reference to a number that was never supplied",
					Amount: 0.05,
				},
				SampleEndedEarly: DockSpec{
					Code:   DockSampleEndedEarly,
					Label:  "Checking ran past the end of the retained row sample",
					Amount: 0.05,
				},
			},
		},
	}
}

// configs is every calibration this build can explain a document with, keyed
// by engine version. One entry today; a re-calibration adds the next.
//
// It is a function rather than a package var because Config carries a map, and
// a shared one would be writable by every caller that got a copy of the
// struct.
func configs() map[int]func() Config {
	return map[int]func() Config{
		EngineVersion: DefaultConfig,
	}
}

// ConfigForVersion returns the calibration that engine version used, and
// whether this build knows it.
//
// This is what the breakdown rests on: an insight scored by an earlier version
// explains itself with the cutoffs that actually scored it. An unknown version
// is a document written by a newer agent than this reader, and the honest
// answer is "not known here" rather than the current numbers under a different
// version's name.
func ConfigForVersion(v int) (Config, bool) {
	build, ok := configs()[v]
	if !ok {
		return Config{}, false
	}
	return build(), true
}

// KnownVersions lists the engine versions this build can explain, ascending.
func KnownVersions() []int {
	all := configs()
	out := make([]int, 0, len(all))
	for v := range all {
		out = append(out, v)
	}
	// Ascending without a sort import: the keys are a small dense range of
	// versions, so an insertion pass is clearer than pulling in sort for it.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
