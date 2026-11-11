package handler

import (
	"net/http"

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
		engines = append(engines, scoringEngine{Version: v, Confidence: confidenceConfigPayload(cfg.Confidence)})
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
