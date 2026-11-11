package scoring

import (
	"strings"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Input is everything the engine reads about one document.
//
// The one field carried without being read is RefuterDisabled, and it is
// carried deliberately: the invariant is that the refuter's absence costs
// nothing, and a field present with a test that it changes nothing pins that
// against a future change that starts charging for it.
//
// There is deliberately no affected count here, and there must not be. It is a
// model-authored integer with no denominator anywhere in the result to turn it
// into a share, so the only thing severity could do with it is invent a ratio.
// A field absent from this struct is a field no calibration can start reading
// by accident.
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

	// --- The severity axis. ---------------------------------------------

	// Figures is the numbers the document states, as data. Read alongside
	// FigureVerdicts above and joined on the figure id: the verdict says
	// whether Go's own evaluation reproduced the number, and the figure says
	// what the number IS and in what notation. Neither struct carries both.
	Figures []gomodels.Figure

	// HomeCurrency is the symbol of the currency this document's own
	// datasource reports in, or "" when that could not be determined.
	//
	// Resolved by the caller, not here, because the answer comes from the
	// project profile and the warehouse config a figure's cited step ran
	// against -- none of which a pure scorer should know about. "" is a real
	// answer and not a failure: a run spanning datasources that report in
	// different currencies has no single home currency, and the engine's
	// response is to band on share rather than to compare an amount against
	// another datasource's thresholds.
	HomeCurrency string

	// AreaID is the analysis area the document belongs to, which is what a
	// configured severity weight attaches to. Carried now and used to look up
	// a weight that is uniform today, so that configuring one is a change to
	// the config rather than to this signature.
	AreaID string

	// Config is the calibration to score against. The caller resolves it once
	// so that two areas of one run can never disagree.
	Config Config
}

// areaWeight is the multiplier for this input's analysis area, and what set
// it.
//
// Uniform today: no pack declares a risk taxonomy, so every area resolves to
// the engine default and the source says so. It is a method rather than an
// inline constant because the breakdown renders both halves, and because the
// version that makes weights configurable should add a lookup here rather than
// a field to Scoring.
func (in Input) areaWeight(cfg SeverityConfig) (float64, *gomodels.WeightSource) {
	weight := cfg.DefaultWeight
	if weight <= 0 {
		// A calibration that left it unset. 1.0 rather than 0, which would
		// multiply every magnitude to nothing and band every finding low --
		// silently, and in the direction that hides findings.
		weight = 1.0
	}
	return weight, &gomodels.WeightSource{Kind: gomodels.WeightSourceDefault}
}

// Score derives one document's whole scoring sub-document: the confidence
// axis, the severity axis, and the priority rollup over the two.
//
// Pure: no Mongo, no context, no clock, no model. The same input scored twice
// is the same output, which is what lets a calibration be replayed over stored
// runs without re-running anything.
//
// RiskFlags stays empty. A flag is derived from the analysis area's risk
// categories and no pack declares any, so there is nothing to derive it from
// yet -- and an empty list is the honest representation of that, where a
// guessed one would not be.
func Score(in Input) gomodels.Scoring {
	cfg := in.resolvedConfig()
	out := confidenceAxis(in, cfg)

	weight, weightSource := in.areaWeight(cfg.Severity)
	magnitude, sevScore, sevBand, driver, sevReason := scoreSeverity(severityInput{
		figures:      in.Figures,
		verdicts:     in.FigureVerdicts,
		homeCurrency: in.HomeCurrency,
		weight:       weight,
	}, cfg.Severity)

	out.MagnitudeScore = magnitude
	out.SeverityBand = sevBand
	out.SeverityScore = sevScore
	out.SeverityDriver = &driver
	out.SeverityReason = sevReason
	out.AreaWeight = weight
	out.WeightSource = weightSource

	return withPriority(out, cfg)
}

// ScoreRecommendation derives a recommendation's scoring sub-document:
// confidence from its OWN verdict, severity INHERITED from the insights it
// cites, and the priority rollup over the two.
//
// A separate entry point rather than a flag on Input, because a
// recommendation's severity has a genuinely different derivation and the
// difference should be visible at the call site. Deriving it the insight way
// would be wrong in a way nothing downstream could detect: a recommendation's
// figures are `ref` and `sum` over figures its insights already declared, each
// of which Go checks and stamps `holds`, so the insight path would happily
// band a magnitude off a number it had already banded once -- counting the
// same evidence twice and reporting the result as independent.
//
// Confidence stays its own, for the reason it has since the confidence axis
// shipped: the verifier checked whether this ACTION follows from those
// findings, and that verdict is the one a reader is being told about.
// Inheriting confidence would report how well the evidence was checked rather
// than how well the recommendation was.
//
// `cited` is the severity half of each insight this recommendation cites, in
// citation order. Empty, or carrying no scored insight, leaves the severity
// half unset and therefore the priority rollup too: there is nothing to
// inherit, and a manufactured band would be the one thing this engine must not
// produce.
func ScoreRecommendation(in Input, cited []InheritedSeverity) gomodels.Scoring {
	cfg := in.resolvedConfig()
	out := confidenceAxis(in, cfg)

	inherited, ok := InheritSeverity(cited)
	if !ok {
		return out
	}

	out.MagnitudeScore = inherited.MagnitudeScore
	out.SeverityBand = inherited.SeverityBand
	out.SeverityScore = inherited.SeverityScore
	out.SeverityDriver = inherited.SeverityDriver
	out.SeverityReason = inherited.SeverityReason
	out.AreaWeight = inherited.AreaWeight
	out.WeightSource = inherited.WeightSource

	return withPriority(out, cfg)
}

// resolvedConfig is the calibration to score against, falling back to this
// build's own.
//
// A zero-valued Config reaches the engine from a caller that built its
// orchestrator as a struct literal, or simply left the field unset. Scoring
// against it would be silently, uniformly wrong rather than loud: an empty
// base table prices every verdict at 0, and cutoffs of 0 then put every
// document in the top band. Falling back rather than erroring, because this is
// a pure function on a value type with no way to report and nothing a caller
// could do about it at the seam -- and the default is the right answer in every
// case a caller has not deliberately overridden. It also makes the stamped
// engine_version true rather than an unexplainable 0.
func (in Input) resolvedConfig() Config {
	if len(in.Config.Confidence.Bases) == 0 || len(in.Config.Priority.TierWeights) == 0 {
		return DefaultConfig()
	}
	return in.Config
}

// confidenceAxis fills the confidence fields and the engine version, which is
// everything both entry points share.
func confidenceAxis(in Input, cfg Config) gomodels.Scoring {
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

// withPriority rolls the two bands up into a tier and a sort score.
//
// Called only once both axes are on the struct, so the matrix reads the
// document rather than a pair of arguments a caller could transpose.
func withPriority(s gomodels.Scoring, cfg Config) gomodels.Scoring {
	s.PriorityTier = priorityTier(s.SeverityBand, s.ConfidenceBand)
	s.PriorityScore = priorityScore(s.PriorityTier, s.SeverityScore, s.ConfidenceScore, cfg.Priority)
	return s
}

// RescoreConfidence re-derives the confidence axis of an ALREADY SCORED
// document and leaves its severity axis exactly as it was.
//
// This is what the on-demand "Run validation" path needs, and the asymmetry is
// deliberate rather than an economy. Severity is magnitude x weight: the
// magnitude is a conditional-on-true statement about how much the finding
// would matter, and a validation run changes neither it nor the weight. A
// stored severity is therefore an immutable fact about the run that produced
// it, and re-deriving it on this path would either produce the same number --
// pointless -- or, once weights are configurable, quietly rewrite a past run's
// result under today's configuration.
//
// Priority is the exception and MUST move, because one of its two inputs just
// did: a finding whose verdict goes from unverifiable to confirmed moves from
// investigate to critical, and leaving the old tier would leave the document
// disagreeing with its own bands.
//
// A prior with no severity band -- a document an older engine scored on the
// confidence axis alone -- yields no tier rather than a guessed one. There is
// no severity to roll up, and inventing a band to roll up would be inventing
// the one thing this engine exists not to invent.
func RescoreConfidence(prior gomodels.Scoring, in Input) gomodels.Scoring {
	// The confidence axis, through the same Score call the run uses, so there
	// is one derivation rather than two that drift. The severity half of its
	// answer is discarded below; computing it costs one pass over a figure
	// list already in hand, and the alternative -- a second entry point that
	// scores one axis -- is the duplication this comment exists to refuse.
	fresh := Score(in)

	out := prior
	out.ConfidenceBand = fresh.ConfidenceBand
	out.ConfidenceBase = fresh.ConfidenceBase
	out.ConfidenceScore = fresh.ConfidenceScore
	out.ConfidenceReason = fresh.ConfidenceReason
	out.ConfidenceDocks = fresh.ConfidenceDocks

	// The engine version stays the prior's. The severity half of this
	// document was produced by that version and is being kept, so stamping
	// today's would claim a calibration scored numbers it never saw.
	if out.EngineVersion == 0 {
		out.EngineVersion = fresh.EngineVersion
	}

	if out.SeverityBand == "" {
		out.PriorityTier = ""
		out.PriorityScore = 0
		return out
	}

	return withPriority(out, in.resolvedConfig())
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
