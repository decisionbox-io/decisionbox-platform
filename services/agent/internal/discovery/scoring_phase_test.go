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
	scoreInsights(insights, testScoringCfg())

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
	scoreInsights(insights, testScoringCfg())

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
	scoreRecommendations(recs, testScoringCfg())

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

// TestTheScorerLeavesSeverityAndRiskScoreAlone is the freeze.
//
// The scorer clears and overwrites confidence and the sub-document ONLY.
// Clearing severity or risk_score in this version would blank the only values
// that exist for them, and writing a derived severity beside an authored one
// would leave two answers to the same question.
func TestTheScorerLeavesSeverityAndRiskScoreAlone(t *testing.T) {
	insights := []models.Insight{{
		ID: "i1", Name: "n",
		Severity:      "critical",
		RiskScore:     0.67,
		AffectedCount: 16695,
		TargetSegment: "new players",
		Validation:    verdictOf(valmodels.StatusRejected),
	}}
	before := insights[0]
	scoreInsights(insights, testScoringCfg())
	after := insights[0]

	if after.Severity != before.Severity {
		t.Errorf("severity changed: %q -> %q", before.Severity, after.Severity)
	}
	if after.RiskScore != before.RiskScore {
		t.Errorf("risk_score changed: %v -> %v", before.RiskScore, after.RiskScore)
	}
	if after.AffectedCount != before.AffectedCount || after.TargetSegment != before.TargetSegment {
		t.Error("the scorer wrote a field outside the confidence axis")
	}
	// And nothing in the severity / weight / priority half of the
	// sub-document is written either.
	frozen := gomodels.Scoring{
		MagnitudeScore: after.Scoring.MagnitudeScore,
		SeverityBand:   after.Scoring.SeverityBand,
		SeverityScore:  after.Scoring.SeverityScore,
		SeverityDriver: after.Scoring.SeverityDriver,
		AreaWeight:     after.Scoring.AreaWeight,
		WeightSource:   after.Scoring.WeightSource,
		PriorityTier:   after.Scoring.PriorityTier,
		PriorityScore:  after.Scoring.PriorityScore,
		RiskFlags:      after.Scoring.RiskFlags,
	}
	if !reflect.DeepEqual(frozen, gomodels.Scoring{}) {
		t.Errorf("the scorer wrote a severity / weight / priority field: %+v", frozen)
	}

	recs := []models.Recommendation{{ID: "r1", Title: "t", Priority: 2, SegmentSize: 400}}
	scoreRecommendations(recs, testScoringCfg())
	if recs[0].Priority != 2 || recs[0].SegmentSize != 400 {
		t.Errorf("the scorer wrote a recommendation field outside the confidence axis: %+v", recs[0])
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
			scoreInsights(insights, testScoringCfg())

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
	in := insightScoringInput(ins, testScoringCfg())
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
	if !insightScoringInput(ins, testScoringCfg()).StepReadBeyondRetained {
		t.Error("the refuter's step-read flag did not reach the input")
	}

	// A nil validation block leaves the verdict empty rather than defaulting
	// to anything that reads as checked.
	ins.Validation = nil
	bare := insightScoringInput(ins, testScoringCfg())
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
	scoreInsights(once, testScoringCfg())
	twice := []models.Insight{ins}
	scoreInsights(twice, testScoringCfg())
	scoreInsights(twice, testScoringCfg())

	if !reflect.DeepEqual(once[0], twice[0]) {
		t.Errorf("scoring twice differs from scoring once:\n once  = %+v\n twice = %+v", once[0].Scoring, twice[0].Scoring)
	}
}
