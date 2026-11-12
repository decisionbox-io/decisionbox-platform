package handler

import (
	"net/http"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
)

// The confidence engine's calibration, as data.
//
// It is here so that the dashboard can explain a score with the cutoffs that
// actually produced it, without holding a copy of them. A duplicated constant
// in TypeScript would be wrong in two ways at once: wrong for a document
// scored by an earlier engine version (whose cutoffs were different by
// definition — that is what a version bump IS), and wrong for one scored by a
// newer agent than the front-end was built against.
//
// Everything a reader needs about a specific document — its band, its reason,
// its base, each dock and the final score — is on the document itself. This
// endpoint supplies only the line the document cannot carry: which cutoff the
// score crossed. That split is deliberate, and it is why an API older than the
// agent degrades to omitting one line of the breakdown rather than breaking it.

// scoringEnginesResponse is the GET /api/v1/scoring/engines payload.
type scoringEnginesResponse struct {
	// CurrentVersion is what this deployment's agent would stamp on a
	// document it scored now.
	CurrentVersion int `json:"current_version"`

	// Engines is every calibration this build can explain a document with,
	// ascending by version. A document whose engine_version is absent from
	// this list was scored by a newer agent than this API, and the dashboard
	// renders its breakdown without the cutoff line.
	Engines []scoringEngine `json:"engines"`
}

type scoringEngine struct {
	Version    int                     `json:"version"`
	Confidence scoringConfidenceConfig `json:"confidence"`
	Severity   scoringSeverityConfig   `json:"severity"`
	Priority   scoringPriorityConfig   `json:"priority"`
}

// scoringSeverityConfig is the severity axis: which figures band a magnitude,
// what each band is worth, and where the weighted score's cutoffs fall.
type scoringSeverityConfig struct {
	// AmountBands bands a currency figure, and SHIPS EMPTY -- serialised as
	// `[]` rather than omitted, so a reader can see that the axis is
	// unconfigured rather than that the field is missing. An absolute money
	// cutoff is a property of a business, not of this engine, so there is no
	// default for it; a document with a checked amount and a checked share
	// therefore bands on the share while still showing the amount.
	AmountBands []scoringMagnitudeBand `json:"amount_bands"`
	// ShareBands bands a percentage figure. A share needs no per-business
	// calibration, which is why this one has defaults and AmountBands does
	// not.
	ShareBands []scoringMagnitudeBand `json:"share_bands"`

	// Magnitudes is what each band is worth before the weight.
	Magnitudes scoringMagnitudes `json:"magnitudes"`
	// Baseline is the magnitude of a document with no figure this engine
	// would use. Deliberately mid-range: an unquantified finding is
	// unmeasured, not small.
	Baseline float64 `json:"baseline"`

	// Cutoffs are the band boundaries on the weighted score, inclusive.
	Cutoffs scoringCutoffs `json:"cutoffs"`
	// DefaultWeight is the multiplier for an area with no configured weight.
	DefaultWeight float64 `json:"default_weight"`
}

type scoringMagnitudeBand struct {
	// At is the absolute value a figure must reach to land in Band.
	At   float64 `json:"at"`
	Band string  `json:"band"`
}

type scoringMagnitudes struct {
	High   float64 `json:"high"`
	Medium float64 `json:"medium"`
	Low    float64 `json:"low"`
}

// scoringPriorityConfig is the rollup: the matrix, what each tier is worth as
// a sort key, and how much the two continuous scores may refine it.
type scoringPriorityConfig struct {
	// Tiers is every tier, highest first, with its sort weight.
	Tiers []scoringTier `json:"tiers"`
	// TrustedConfidenceBands are the confidence bands the matrix treats as
	// actionable without further checking. Served rather than assumed,
	// because the matrix cell a reader is shown is chosen by this partition.
	TrustedConfidenceBands []string `json:"trusted_confidence_bands"`
	// Matrix is every cell: which tier a (severity band, trusted) pair
	// selects.
	Matrix []scoringMatrixCell `json:"matrix"`

	SeverityCoeff   float64 `json:"severity_coeff"`
	ConfidenceCoeff float64 `json:"confidence_coeff"`
}

type scoringTier struct {
	Tier   string  `json:"tier"`
	Weight float64 `json:"weight"`
	// Priority is the 1-5 integer a recommendation in this tier carries, so a
	// client reading that field knows which tier produced it.
	Priority int `json:"priority"`
}

type scoringMatrixCell struct {
	Severity string `json:"severity"`
	// Confidence is "trusted" or "untrusted" -- the two groups the five
	// confidence bands collapse to.
	Confidence string `json:"confidence"`
	Tier       string `json:"tier"`
}

type scoringConfidenceConfig struct {
	// Bases is what each validation verdict is worth before any deduction,
	// keyed by the verdict name the document carries.
	Bases map[string]float64 `json:"bases"`

	// Default is the base for a document carrying no verdict this engine
	// prices — validation off, the run's budget reached, or nothing stamped.
	Default float64 `json:"default"`

	// Cutoffs are the band boundaries, inclusive: a score at exactly `high`
	// is high.
	Cutoffs scoringCutoffs `json:"cutoffs"`

	// Floor is the lowest score any document can reach.
	Floor float64 `json:"floor"`

	// Docks is every deduction, with the words the engine stores on a
	// document and the amount it charges. The dashboard renders the label
	// off the document rather than from here; these entries are what let it
	// show the full calibration rather than only the docks one document
	// happened to take.
	Docks []scoringDock `json:"docks"`
}

type scoringCutoffs struct {
	High   float64 `json:"high"`
	Medium float64 `json:"medium"`
}

type scoringDock struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Max   float64 `json:"max"`
	// Cap is the total this dock may charge across every entry of its own
	// code, or across the group it shares a cap with. Zero means uncapped.
	Cap float64 `json:"cap,omitempty"`
	// SharedCapWith names the other dock this one shares its cap with, so a
	// reader of the calibration can see why two 0.10 deductions can total
	// 0.15.
	SharedCapWith string `json:"shared_cap_with,omitempty"`
}

// ScoringEngines returns the confidence engine's calibration, per version.
//
// GET /api/v1/scoring/engines
func ScoringEngines(w http.ResponseWriter, r *http.Request) {
	// Never cache. It is cheap to build, and a stale copy would explain a
	// document with the cutoffs of whatever version was deployed when the
	// response was cached — which is exactly the failure this endpoint exists
	// to prevent.
	w.Header().Set("Cache-Control", "no-store")

	versions := scoring.KnownVersions()
	engines := make([]scoringEngine, 0, len(versions))
	for _, v := range versions {
		cfg, ok := scoring.ConfigForVersion(v)
		if !ok {
			// KnownVersions and ConfigForVersion read the same table, so this
			// cannot happen; skipping rather than erroring keeps a future
			// divergence from taking the whole endpoint down.
			continue
		}
		engines = append(engines, scoringEngine{
			Version:    v,
			Confidence: confidenceConfigPayload(cfg.Confidence),
			Severity:   severityConfigPayload(cfg.Severity),
			Priority:   priorityConfigPayload(cfg.Priority),
		})
	}

	writeJSON(w, http.StatusOK, scoringEnginesResponse{
		CurrentVersion: scoring.EngineVersion,
		Engines:        engines,
	})
}

// confidenceConfigPayload projects one engine version's confidence
// calibration onto the wire shape.
//
// A projection rather than the engine's own struct, because the engine keys
// its base table by the typed validation status and a Go map key type is not
// something a JSON client should have to know about.
func confidenceConfigPayload(cfg scoring.ConfidenceConfig) scoringConfidenceConfig {
	bases := make(map[string]float64, len(cfg.Bases))
	for status, base := range cfg.Bases {
		bases[string(status)] = base
	}
	// Spelled out so an unpriced status is visibly absent rather than
	// silently defaulted by a reader.
	for _, status := range []valmodels.Status{valmodels.StatusValidationDisabled, valmodels.StatusSkippedBudgetCap} {
		delete(bases, string(status))
	}

	d := cfg.Docks
	return scoringConfidenceConfig{
		Bases:   bases,
		Default: cfg.Default,
		Cutoffs: scoringCutoffs{High: cfg.HighAt, Medium: cfg.MediumAt},
		Floor:   cfg.Floor,
		Docks: []scoringDock{
			{Code: d.DegradedEvidence.Code, Label: d.DegradedEvidence.Label,
				Max: d.DegradedEvidence.Amount, Cap: d.DegradedEvidenceCap},
			{Code: d.RefutedArithmetic.Code, Label: d.RefutedArithmetic.Label,
				Max: d.RefutedArithmetic.Amount, Cap: d.RefutedCap,
				SharedCapWith: d.RefutedQuantifier.Code},
			{Code: d.RefutedQuantifier.Code, Label: d.RefutedQuantifier.Label,
				Max: d.RefutedQuantifier.Amount, Cap: d.RefutedCap,
				SharedCapWith: d.RefutedArithmetic.Code},
			{Code: d.UnresolvedFigure.Code, Label: d.UnresolvedFigure.Label,
				Max: d.UnresolvedFigure.Amount},
			{Code: d.SampleEndedEarly.Code, Label: d.SampleEndedEarly.Label,
				Max: d.SampleEndedEarly.Amount},
		},
	}
}

// severityConfigPayload projects one engine version's severity calibration
// onto the wire shape.
func severityConfigPayload(cfg scoring.SeverityConfig) scoringSeverityConfig {
	return scoringSeverityConfig{
		AmountBands: magnitudeBandsPayload(cfg.AmountBands),
		ShareBands:  magnitudeBandsPayload(cfg.ShareBands),
		Magnitudes: scoringMagnitudes{
			High:   cfg.HighMagnitude,
			Medium: cfg.MediumMagnitude,
			Low:    cfg.LowMagnitude,
		},
		Baseline:      cfg.BaselineMagnitude,
		Cutoffs:       scoringCutoffs{High: cfg.HighAt, Medium: cfg.MediumAt},
		DefaultWeight: cfg.DefaultWeight,
	}
}

// magnitudeBandsPayload always returns a non-nil slice, so an unconfigured
// band set serialises as `[]` rather than `null`. A reader has to be able to
// tell "this axis is deliberately unconfigured" from "this field is missing",
// and `null` says neither.
func magnitudeBandsPayload(bands []scoring.MagnitudeBand) []scoringMagnitudeBand {
	out := make([]scoringMagnitudeBand, 0, len(bands))
	for _, b := range bands {
		out = append(out, scoringMagnitudeBand{At: b.At, Band: b.Band})
	}
	return out
}

// priorityConfigPayload projects the matrix and the sort weights.
//
// The matrix is enumerated rather than described, because a client rendering
// "high severity x unverified evidence -> investigate" beside a tier badge
// needs the cell, and deriving it from a rule stated in prose is how a second
// implementation of the matrix appears.
func priorityConfigPayload(cfg scoring.PriorityConfig) scoringPriorityConfig {
	tiers := []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	}
	out := scoringPriorityConfig{
		Tiers: make([]scoringTier, 0, len(tiers)),
		TrustedConfidenceBands: []string{
			gomodels.ConfidenceBandHigh, gomodels.ConfidenceBandMedium,
		},
		SeverityCoeff:   cfg.SeverityCoeff,
		ConfidenceCoeff: cfg.ConfidenceCoeff,
	}
	for _, tier := range tiers {
		out.Tiers = append(out.Tiers, scoringTier{
			Tier:     tier,
			Weight:   cfg.TierWeights[tier],
			Priority: scoring.RecommendationPriority(tier),
		})
	}
	// Built by asking the engine for each cell rather than restating the
	// matrix here, so this payload cannot disagree with the scorer.
	for _, severity := range []string{
		gomodels.SeverityBandHigh, gomodels.SeverityBandMedium, gomodels.SeverityBandLow,
	} {
		for _, confidence := range []struct {
			label string
			band  string
		}{
			{"trusted", gomodels.ConfidenceBandHigh},
			{"untrusted", gomodels.ConfidenceBandUnverifiable},
		} {
			out.Matrix = append(out.Matrix, scoringMatrixCell{
				Severity:   severity,
				Confidence: confidence.label,
				Tier:       scoring.PriorityTierFor(severity, confidence.band),
			})
		}
	}
	return out
}
