package discovery

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func testScoringCfg() scoring.Config { return scoring.DefaultConfig() }

func verdictOf(status valmodels.Status) *models.InsightValidation {
	return &models.InsightValidation{Combined: status}
}

// TestScoreInsightsClearsThenWrites is the mechanism, stated as an assertion.
//
// Correctness cannot come from editing the prompts: a project snapshots its
// area prompts at creation and a pack's seeds are never overwritten on
// upgrade, so an existing deployment keeps asking the model for the number
// forever. It comes from this — the scorer discards whatever arrived and
// writes what the evidence says, including a stale sub-document from a
// previous attempt.
func TestScoreInsightsClearsThenWrites(t *testing.T) {
	insights := []models.Insight{{
		ID:         "i1",
		Name:       "Day 1 drop",
		Confidence: 0.92, // the model's own guess
		Scoring: &models.Scoring{ // and a stale grade from a previous attempt
			ConfidenceBand:  models.ConfidenceBandLow,
			ConfidenceScore: 0.11,
			EngineVersion:   99,
		},
		Validation: verdictOf(valmodels.StatusSupported),
	}}
	scoreInsights(insights, scoringContext{cfg: testScoringCfg()})

	got := insights[0]
	if got.Scoring == nil {
		t.Fatal("no scoring sub-document was written")
	}
	if got.Scoring.ConfidenceBand != models.ConfidenceBandHigh {
		t.Errorf("band = %q, want high for a supported insight", got.Scoring.ConfidenceBand)
	}
	if got.Scoring.ConfidenceScore != 0.80 {
		t.Errorf("score = %v, want 0.8", got.Scoring.ConfidenceScore)
	}
	if got.Scoring.EngineVersion != scoring.EngineVersion {
		t.Errorf("engine version = %d, want %d — the stale 99 survived", got.Scoring.EngineVersion, scoring.EngineVersion)
	}
	if got.Confidence != got.Scoring.ConfidenceScore {
		t.Errorf("confidence = %v, want the derived score %v — the model's 0.92 must not survive",
			got.Confidence, got.Scoring.ConfidenceScore)
	}
	if got.Scoring.ConfidenceReason == "" {
		t.Error("no reason was stored")
	}
}

// TestScoreInsightsTheZeroCase — an insight validation never reached. It must
// come out as never-checked with a reason, not as the model's number and not
// as zero. Those are three different statements and only one of them is true.
func TestScoreInsightsTheZeroCase(t *testing.T) {
	insights := []models.Insight{{ID: "i1", Name: "n", Confidence: 0.88}}
	scoreInsights(insights, scoringContext{cfg: testScoringCfg()})

	got := insights[0]
	if got.Scoring == nil {
		t.Fatal("an unvalidated insight was left unscored, so it would render as legacy")
	}
	if got.Scoring.ConfidenceBand != models.ConfidenceBandNotConfirmed {
		t.Errorf("band = %q, want not_confirmed", got.Scoring.ConfidenceBand)
	}
	if got.Scoring.ConfidenceScore != 0.25 {
		t.Errorf("score = %v, want 0.25", got.Scoring.ConfidenceScore)
	}
	if got.Confidence != 0.25 {
		t.Errorf("confidence = %v, want 0.25 — not the model's 0.88 and not zero", got.Confidence)
	}
	if got.Scoring.ConfidenceReason == "" {
		t.Error("no reason was stored; the not-validated card still has to explain itself")
	}
}

// TestScoreRecommendationsUseTheirOwnVerdict — a recommendation is a separate
// claim from the insights it cites, and the verifier gave it its own verdict.
func TestScoreRecommendationsUseTheirOwnVerdict(t *testing.T) {
	recs := []models.Recommendation{
		{ID: "r1", Title: "confirmed", Confidence: 0.9, Validation: verdictOf(valmodels.StatusConfirmed)},
		{ID: "r2", Title: "rejected", Confidence: 0.9, Validation: verdictOf(valmodels.StatusRejected)},
		{ID: "r3", Title: "none", Confidence: 0.9},
	}
	scoreRecommendations(recs, nil, testScoringCfg())

	for i, want := range []struct {
		band  string
		score float64
	}{
		{models.ConfidenceBandHigh, 0.95},
		{models.ConfidenceBandLow, 0.05},
		{models.ConfidenceBandNotConfirmed, 0.25},
	} {
		got := recs[i]
		if got.Scoring == nil {
			t.Fatalf("recs[%d] was left unscored", i)
		}
		if got.Scoring.ConfidenceBand != want.band {
			t.Errorf("recs[%d] band = %q, want %q", i, got.Scoring.ConfidenceBand, want.band)
		}
		if got.Scoring.ConfidenceScore != want.score || got.Confidence != want.score {
			t.Errorf("recs[%d] score = %v / confidence = %v, want %v",
				i, got.Scoring.ConfidenceScore, got.Confidence, want.score)
		}
	}
}

// TestTheScorerClearsTheModelsSeverityAndRiskScore is the mechanism, one
// field over from the confidence one.
//
// It clears both and overwrites them from the evidence, unconditionally and
// including the case below -- no verdict this engine prices, no figure, so the
// band comes from the area baseline. That unconditional clear is the whole
// correctness argument: editing the prompts could not have been enough,
// because a project snapshots its area prompts at creation and a pack's seeds
// are never overwritten on upgrade, so an existing deployment keeps asking the
// model for `severity` and `risk_score` indefinitely.
func TestTheScorerClearsTheModelsSeverityAndRiskScore(t *testing.T) {
	insights := []models.Insight{{
		ID: "i1", Name: "n",
		Severity:      "critical",
		RiskScore:     0.67,
		AffectedCount: 16695,
		TargetSegment: "new players",
		Validation:    verdictOf(valmodels.StatusRejected),
		// A stale sub-document from a previous attempt, carrying a severity
		// half that must not survive either.
		Scoring: &models.Scoring{
			SeverityBand:   gomodels.SeverityBandHigh,
			SeverityScore:  0.95,
			PriorityTier:   gomodels.PriorityTierCritical,
			SeverityReason: "stale",
		},
	}}
	scoreInsights(insights, scoringContext{cfg: testScoringCfg()})
	got := insights[0]

	if got.Severity == "critical" {
		t.Error("the model's severity word survived the scorer")
	}
	if got.Severity != gomodels.SeverityBandMedium {
		t.Errorf("severity = %q, want the baseline band medium", got.Severity)
	}
	if got.RiskScore == 0.67 {
		t.Error("the model's risk_score survived the scorer")
	}
	if got.RiskScore != got.Scoring.SeverityScore {
		t.Errorf("risk_score %v does not mirror the derived severity score %v",
			got.RiskScore, got.Scoring.SeverityScore)
	}
	if got.Scoring.SeverityReason == "stale" {
		t.Error("the stale sub-document's reason survived")
	}
	// A rejected verdict is untrusted, so a baseline-medium finding is low
	// priority -- and it has a tier at all, which a stale one would not prove.
	if got.Scoring.PriorityTier != gomodels.PriorityTierLow {
		t.Errorf("tier = %q, want low (medium severity, rejected verdict)", got.Scoring.PriorityTier)
	}
	// Fields outside the grade are untouched.
	if got.AffectedCount != 16695 || got.TargetSegment != "new players" {
		t.Error("the scorer wrote a field outside the grade")
	}
	// Flags are the one part of the sub-document nothing derives yet.
	if len(got.Scoring.RiskFlags) != 0 {
		t.Errorf("RiskFlags = %v, want none", got.Scoring.RiskFlags)
	}
}

// TestTheScorerClearsAModelAuthoredRecommendationPriority -- the same, for the
// recommendation half. A recommendation citing nothing scored inherits no
// severity, so its priority is the field's own "unset" rather than a guess.
func TestTheScorerClearsAModelAuthoredRecommendationPriority(t *testing.T) {
	recs := []models.Recommendation{{
		ID: "r1", Title: "t", Priority: 2, Severity: "critical", SegmentSize: 400,
		Validation: verdictOf(valmodels.StatusSupported),
	}}
	scoreRecommendations(recs, nil, testScoringCfg())

	if recs[0].Priority != 0 {
		t.Errorf("priority = %d; nothing was cited, so there is no tier to project", recs[0].Priority)
	}
	if recs[0].Severity != "" {
		t.Errorf("severity = %q; the model's word must not survive", recs[0].Severity)
	}
	if recs[0].SegmentSize != 400 {
		t.Error("the scorer wrote a recommendation field outside the grade")
	}
	if recs[0].Scoring == nil || recs[0].Scoring.ConfidenceBand == "" {
		t.Error("the confidence axis is still derived from the recommendation's own verdict")
	}
}

// TestARecommendationInheritsTheWorstSeverityItCites, and derives its priority
// from its OWN verdict.
//
// Both halves matter. Inheriting the max is what makes a recommendation as
// important as the worst thing it addresses; deriving priority from its own
// verdict is what makes the tier a statement about whether THIS action was
// checked, not about how well the evidence behind it was.
func TestARecommendationInheritsTheWorstSeverityItCites(t *testing.T) {
	insights := []models.Insight{
		{ID: "mild", Scoring: &models.Scoring{
			SeverityBand: gomodels.SeverityBandLow, SeverityScore: 0.25,
			MagnitudeScore: 0.25, AreaWeight: 1,
			SeverityReason: "Low — driven by 2%",
		}},
		{ID: "severe", Scoring: &models.Scoring{
			SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 1.0,
			MagnitudeScore: 1.0, AreaWeight: 1,
			SeverityReason: "High — driven by 34%",
		}},
		{ID: "legacy"}, // never scored
	}
	recs := []models.Recommendation{{
		ID: "r1", Title: "t",
		RelatedInsightIDs: []string{"mild", "legacy", "severe", "never-existed"},
		Validation:        verdictOf(valmodels.StatusUnverifiable),
	}}
	scoreRecommendations(recs, insights, testScoringCfg())
	got := recs[0]

	if got.Severity != gomodels.SeverityBandHigh {
		t.Errorf("severity = %q, want the worst cited insight's high", got.Severity)
	}
	if got.Scoring.SeverityReason != "High — driven by 34%" {
		t.Errorf("reason = %q, want the severe insight's own", got.Scoring.SeverityReason)
	}
	// Its own verdict is unverifiable, so high severity lands in investigate
	// rather than critical -- the cited insights' verdicts do not decide this.
	if got.Scoring.PriorityTier != gomodels.PriorityTierInvestigate {
		t.Errorf("tier = %q, want investigate from its own unverifiable verdict", got.Scoring.PriorityTier)
	}
	if got.Priority != scoring.RecommendationPriority(gomodels.PriorityTierInvestigate) {
		t.Errorf("priority int = %d, want the investigate projection", got.Priority)
	}
}

// TestTheUnresolvedFigureDockReadsThePublishedProse — an unresolved reference
// ships as written for insights, so the honest detector is the text a reader
// opens. The Markdown copy counts: the dashboard renders it in preference to
// the plain description.
func TestTheUnresolvedFigureDockReadsThePublishedProse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ins    models.Insight
		docked bool
	}{
		{"all rendered", models.Insight{Name: "Revenue reached $1.2B", Description: "All 400 buyers."}, false},
		{"a stray ref in the name", models.Insight{Name: "Revenue reached {{f1}}"}, true},
		{"a stray ref in the description", models.Insight{Description: "All {{f1}} buyers."}, true},
		{"a stray ref in an indicator", models.Insight{Indicators: []string{"Only {{f2}} returned"}}, true},
		{"a stray ref only in the markdown copy", models.Insight{
			Description: "All 400 buyers.", DescriptionMd: "All **{{f1}}** buyers.",
		}, true},
		// The id grammar refuses a hyphen, so this placeholder is one the
		// resolving pattern does not match and a reader still sees.
		{"an id outside the reference grammar", models.Insight{Name: "Revenue reached {{revenue-total}}"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ins := tc.ins
			ins.Validation = verdictOf(valmodels.StatusConfirmed)
			insights := []models.Insight{ins}
			scoreInsights(insights, scoringContext{cfg: testScoringCfg()})

			found := false
			for _, d := range insights[0].Scoring.ConfidenceDocks {
				if d.Code == scoring.DockUnresolvedFigure {
					found = true
				}
			}
			if found != tc.docked {
				t.Errorf("unresolved_figure dock = %v, want %v (docks: %+v)",
					found, tc.docked, insights[0].Scoring.ConfidenceDocks)
			}
		})
	}
}

// TestTheEvidenceTrailReachesTheInput wires each signal on a real insight
// through to its dock, so a field renamed on the model cannot silently stop
// being read.
func TestTheEvidenceTrailReachesTheInput(t *testing.T) {
	ins := models.Insight{
		ID: "i1", Name: "n",
		Quality:            []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412"}},
		FigureVerdicts:     []gomodels.FigureVerdict{{ID: "f1", Status: gomodels.FigureFails}},
		QuantifierVerdicts: []gomodels.QuantifierVerdict{{Claim: "the only one", Status: gomodels.QuantifierFails}},
		Validation: &models.InsightValidation{
			Combined: valmodels.StatusConfirmed,
			Verifier: &valmodels.StructuredVerdict{StepReadBeyondRetained: true},
		},
	}
	in := insightScoringInput(ins, scoringContext{cfg: testScoringCfg()})
	if in.Verdict != valmodels.StatusConfirmed {
		t.Errorf("verdict = %q", in.Verdict)
	}
	if len(in.QualityCaveats) != 1 || len(in.FigureVerdicts) != 1 || len(in.QuantifierVerdicts) != 1 {
		t.Errorf("an evidence layer did not reach the input: %+v", in)
	}
	if !in.StepReadBeyondRetained {
		t.Error("the verifier's step-read flag did not reach the input")
	}

	// The refuter's flag counts too: the agents page independently, and either
	// one hitting the end of the sample narrowed the evidence.
	ins.Validation = &models.InsightValidation{
		Combined: valmodels.StatusConfirmed,
		Verifier: &valmodels.StructuredVerdict{},
		Refuter:  &valmodels.StructuredVerdict{StepReadBeyondRetained: true},
	}
	if !insightScoringInput(ins, scoringContext{cfg: testScoringCfg()}).StepReadBeyondRetained {
		t.Error("the refuter's step-read flag did not reach the input")
	}

	// A nil validation block leaves the verdict empty rather than defaulting
	// to anything that reads as checked.
	ins.Validation = nil
	bare := insightScoringInput(ins, scoringContext{cfg: testScoringCfg()})
	if bare.Verdict != "" || bare.RefuterDisabled || bare.StepReadBeyondRetained {
		t.Errorf("a nil validation block produced %+v", bare)
	}
}

// TestRecommendationsCarryNoQualityOrQuantifierSignal — structurally absent,
// and correctly so: a recommendation is shown insights rather than warehouse
// rows. Asserted rather than assumed, because a future field of those names on
// Recommendation would silently start docking it.
func TestRecommendationsCarryNoQualityOrQuantifierSignal(t *testing.T) {
	in := recommendationScoringInput(models.Recommendation{
		ID: "r1", Title: "t",
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Status: gomodels.FigureFails}},
		Validation:     verdictOf(valmodels.StatusSupported),
	}, testScoringCfg())

	if len(in.QualityCaveats) != 0 {
		t.Errorf("a recommendation carried quality caveats: %+v", in.QualityCaveats)
	}
	if len(in.QuantifierVerdicts) != 0 {
		t.Errorf("a recommendation carried quantifier verdicts: %+v", in.QuantifierVerdicts)
	}
	if len(in.FigureVerdicts) != 1 {
		t.Error("the figure verdicts did not reach the input; a recommendation's references ARE checked")
	}
}

// TestScoringIsIdempotent — the on-demand path re-scores a document that was
// already scored, and a resumed run can hand the same insights to the scorer a
// second time. Either must not double-dock.
func TestScoringIsIdempotent(t *testing.T) {
	ins := models.Insight{
		ID: "i1", Name: "All {{f1}} buyers",
		Quality:        []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412"}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Status: gomodels.FigureFails}},
		Validation:     verdictOf(valmodels.StatusSupported),
	}
	once := []models.Insight{ins}
	scoreInsights(once, scoringContext{cfg: testScoringCfg()})
	twice := []models.Insight{ins}
	scoreInsights(twice, scoringContext{cfg: testScoringCfg()})
	scoreInsights(twice, scoringContext{cfg: testScoringCfg()})

	if !reflect.DeepEqual(once[0], twice[0]) {
		t.Errorf("scoring twice differs from scoring once:\n once  = %+v\n twice = %+v", once[0].Scoring, twice[0].Scoring)
	}
}
