package scoring

import (
	"strings"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Input is everything the confidence axis reads about one document.
//
// It carries what this version scores and nothing else. The per-claim
// verdicts, the figure list, the affected count and the analysis area belong to
// the severity and weight half, and adding them now would mean fields nothing
// sets correctly and no test exercises. What is locked is the SIGNATURE --
// Score(Input) models.Scoring -- so a later version adds fields here without
// touching a call site.
//
// The one field carried without being read is RefuterDisabled, and it is
// carried deliberately: the invariant is that the refuter's absence costs
// nothing, and a field present with a test that it changes nothing pins that
// against a future change that starts charging for it.
type Input struct {
	// Verdict is InsightValidation.Combined, or "" when the document carries
	// no validation block at all.
	Verdict valmodels.Status

	// RefuterDisabled is never docked. See candidateDocks.
	RefuterDisabled bool

	// QualityCaveats is what the sources said about the fidelity of the rows
	// this document was drawn from -- Insight.Quality. Empty for a
	// recommendation, which is shown insights rather than warehouse rows.
	QualityCaveats []gowarehouse.QualityCaveat

	// FigureVerdicts and QuantifierVerdicts are what Go concluded about the
	// document's own declared arithmetic and claims.
	FigureVerdicts     []gomodels.FigureVerdict
	QuantifierVerdicts []gomodels.QuantifierVerdict

	// UnresolvedFigures is how many reference placeholders the PUBLISHED prose
	// still carries.
	//
	// A count the caller supplies rather than something derived here, because
	// the honest detector is "the stored text still shows {{ref}}" and that
	// needs the reference pattern and the prose, neither of which belongs in a
	// pure scorer. Deriving it from the figure list instead is not an option:
	// the field that would say so is not persisted, so it cannot be recovered
	// when a document is re-scored from storage.
	UnresolvedFigures int

	// StepReadBeyondRetained is the verifier's own report that it asked for
	// rows past the end of the retained sample and got none.
	StepReadBeyondRetained bool

	// Config is the calibration to score against. The caller resolves it once
	// so that two areas of one run can never disagree.
	Config Config
}

// Score derives the confidence axis of one document's scoring sub-document.
//
// Pure: no Mongo, no context, no clock, no model. The same input scored twice
// is the same output, which is what lets a calibration be replayed over stored
// runs without re-running anything.
//
// It fills the confidence fields and the engine version, and nothing else. The
// severity, weight and priority fields stay zero, because severity and
// risk_score are still the analysis model's own words in this version and
// writing a derived band beside an authored one would leave two answers to the
// same question.
func Score(in Input) gomodels.Scoring {
	// A zero-valued Config reached Score — a caller that built its
	// Orchestrator as a struct literal, or one that simply left the field
	// unset. Scoring against it would be silently, uniformly wrong rather
	// than loud: an empty base table prices every verdict at 0, and cutoffs
	// of 0 then put every document in the top band. So an unset calibration
	// resolves to this build's own, which also makes the stamped
	// engine_version true rather than an unexplainable 0.
	//
	// Falling back rather than erroring, because this is a pure function on a
	// value type with no way to report and nothing a caller could do about it
	// at the seam; the panic-free rule applies and the default is the right
	// answer in every case a caller has not deliberately overridden.
	cfg := in.Config
	if len(cfg.Confidence.Bases) == 0 {
		cfg = DefaultConfig()
	}
	conf := cfg.Confidence

	base, reason := baseFor(in.Verdict, conf)
	docks, score := applyFloor(base, candidateDocks(in, conf), conf.Floor)

	version := cfg.Version
	if version == 0 {
		version = EngineVersion
	}

	return gomodels.Scoring{
		ConfidenceBand:   bandFor(in.Verdict, score, conf),
		ConfidenceBase:   base,
		ConfidenceScore:  score,
		ConfidenceReason: composeReason(reason, docks),
		ConfidenceDocks:  docks,
		EngineVersion:    version,
	}
}

// composeReason is the verdict's own sentence, plus one clause naming what
// reduced it.
//
// Both halves are needed. The verdict sentence alone reads wrong beside a band
// it did not produce -- "Warehouse rows are consistent with the claims that
// were checked" under a Medium label invites the reader to think the label is
// broken. And the clause is assembled from the dock labels already stored on
// the document, so this is still one wording rather than a second one invented
// here.
func composeReason(verdictReason string, docks []gomodels.ConfidenceDock) string {
	if len(docks) == 0 {
		return verdictReason
	}
	// Each code once: the quality dock can legitimately appear twice, and
	// repeating its sentence says nothing the first one did not.
	seen := make(map[string]struct{}, len(docks))
	parts := make([]string, 0, len(docks))
	for _, dock := range docks {
		if dock.Label == "" {
			continue
		}
		if _, dup := seen[dock.Code]; dup {
			continue
		}
		seen[dock.Code] = struct{}{}
		parts = append(parts, strings.ToLower(dock.Label[:1])+dock.Label[1:])
	}
	if len(parts) == 0 {
		return verdictReason
	}
	return verdictReason + " Lowered because " + joinClauses(parts) + "."
}

// joinClauses renders a list of clauses as prose: "a", "a and b",
// "a; b; and c". Semicolons past two because the clauses are themselves
// sentences with commas in them.
func joinClauses(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], "; ") + "; and " + parts[len(parts)-1]
	}
}
