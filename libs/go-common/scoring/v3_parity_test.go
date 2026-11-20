package scoring

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// What a WEIGHTED configuration scores, as a golden file.
//
// testdata/v2_parity.golden pins the unconfigured engine. This one pins the
// configured one: a pack taxonomy, project category weights, per-area
// overrides, and the resolution that folds them into the map the engine reads.
// It exists so a change to how weights are resolved or applied cannot move a
// configured deployment's numbers without the diff saying so.
//
// Captured from the tree before any change to the weighting code and
// committed on its own, for the reason the v2 golden gives: a golden captured
// after a change agrees with that change by construction.

// v3Taxonomy is a pack's declared risk taxonomy.
func v3Taxonomy() []gomodels.RiskCategory {
	return []gomodels.RiskCategory{
		{ID: "compliance", Label: "Compliance", Weight: 3.0},
		{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
		{ID: "engagement", Label: "Engagement", Weight: 1.0},
	}
}

// v3ProjectWeights overrides one declared category and weights one global id
// the pack says nothing about.
func v3ProjectWeights() map[string]float64 {
	return map[string]float64{"revenue_leak": 1.5, "data_quality": 2.5}
}

// v3Resolution is one ResolveSeverity call, named for what it exercises.
type v3Resolution struct {
	name     string
	taxonomy []gomodels.RiskCategory
	project  map[string]float64
}

func v3Resolutions() []v3Resolution {
	return []v3Resolution{
		{name: "taxonomy only", taxonomy: v3Taxonomy()},
		{name: "taxonomy and project weights", taxonomy: v3Taxonomy(), project: v3ProjectWeights()},
		{name: "project weights only", project: v3ProjectWeights()},
		{name: "an illegal taxonomy weight is skipped", taxonomy: []gomodels.RiskCategory{
			{ID: "compliance", Label: "Compliance", Weight: 0},
			{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
		}},
		{name: "an illegal project weight falls back to the pack", taxonomy: v3Taxonomy(),
			project: map[string]float64{"compliance": -1, "engagement": 3.5}},
		{name: "an empty id is skipped at both levels", taxonomy: []gomodels.RiskCategory{{ID: "", Weight: 2}},
			project: map[string]float64{"": 2}},
		{name: "nothing to resolve"},
	}
}

// v3ScoreCase is one weighted document.
type v3ScoreCase struct {
	name  string
	in    Input
	cited []InheritedSeverity
}

func v3ScoreCorpus() []v3ScoreCase {
	cfg := DefaultConfig()
	cfg.Severity = ResolveSeverity(cfg.Severity, v3Taxonomy(), v3ProjectWeights())

	share := func(id string, v float64) ([]gomodels.Figure, []gomodels.FigureVerdict) {
		return []gomodels.Figure{{ID: id, Value: v, Unit: gomodels.UnitPercent}},
			[]gomodels.FigureVerdict{{ID: id, Display: "share", Status: gomodels.FigureHolds, Claimed: v, Evaluated: v}}
	}
	low, lowV := share("lo", 1.2)
	mid, midV := share("mid", 7.5)
	high, highV := share("hi", 24.66)

	doc := func(verdict valmodels.Status, figs []gomodels.Figure, vs []gomodels.FigureVerdict, cats []string, override *float64) Input {
		return Input{
			Verdict: verdict, AreaID: "area", Figures: figs, FigureVerdicts: vs,
			AreaCategories: cats, AreaOverride: override, Config: cfg,
		}
	}
	w := func(v float64) *float64 { return &v }

	return []v3ScoreCase{
		{name: "a low share in a compliance area", in: doc(valmodels.StatusConfirmed, low, lowV, []string{"compliance"}, nil)},
		{name: "a medium share in an engagement area", in: doc(valmodels.StatusConfirmed, mid, midV, []string{"engagement"}, nil)},
		{name: "a high share in a compliance area clamps", in: doc(valmodels.StatusConfirmed, high, highV, []string{"compliance"}, nil)},
		{name: "two categories take the larger", in: doc(valmodels.StatusConfirmed, low, lowV, []string{"revenue_leak", "compliance"}, nil)},
		{name: "a project weight over the pack's", in: doc(valmodels.StatusConfirmed, low, lowV, []string{"engagement", "revenue_leak"}, nil)},
		{name: "a per-area override wins over its categories", in: doc(valmodels.StatusConfirmed, high, highV, []string{"compliance"}, w(0.5))},
		{name: "an illegal override falls through to the categories", in: doc(valmodels.StatusConfirmed, low, lowV, []string{"compliance"}, w(0))},
		{name: "nothing quantified in a global category the project weights", in: doc(valmodels.StatusSupported, nil, nil, []string{"data_quality"}, nil)},
		{name: "an unknown category weighs nothing and flags nothing", in: doc(valmodels.StatusConfirmed, mid, midV, []string{"fraud"}, nil)},
		{name: "an unverifiable finding in a weighted area", in: doc(valmodels.StatusUnverifiable, low, lowV, []string{"compliance"}, nil)},
		{name: "a rejected finding in a weighted area", in: doc(valmodels.StatusRejected, mid, midV, []string{"compliance"}, nil)},
		{name: "an area with no categories", in: doc(valmodels.StatusConfirmed, mid, midV, nil, nil)},
		{
			name: "a recommendation inheriting a weighted insight",
			in:   Input{Verdict: valmodels.StatusSupported, Config: cfg},
			cited: []InheritedSeverity{{
				MagnitudeScore: 0.25, SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 0.75,
				SeverityReason: "High — banded on 1.20%, checked against warehouse rows.",
				AreaWeight:     3.0,
				WeightSource:   &gomodels.WeightSource{Kind: gomodels.WeightSourceCategory, Category: "compliance"},
				RiskFlags:      []string{"compliance"},
			}},
		},
	}
}

// renderV3Parity renders every resolution and every weighted document as the
// golden holds them.
func renderV3Parity(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	write := func(title string, v any) {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("%s: marshal: %v", title, err)
		}
		b.WriteString("## " + title + "\n")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	for _, r := range v3Resolutions() {
		resolved := resolveV3(r)
		write("resolve: "+r.name, resolved.CategoryWeights)
	}
	for _, c := range v3ScoreCorpus() {
		if c.cited != nil {
			write("score: "+c.name, ScoreRecommendation(c.in, c.cited))
			continue
		}
		write("score: "+c.name, Score(c.in))
	}
	return b.String()
}

// TestAWeightedConfigurationScoresExactlyAsItDidInV3 -- the configured half
// of the compatibility criterion, byte for byte.
//
// Re-capture the golden only after deciding deliberately that configured
// deployments should score differently, and say so on the pull request.
func TestAWeightedConfigurationScoresExactlyAsItDidInV3(t *testing.T) {
	const golden = "testdata/v3_parity.golden"
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v", golden, err)
	}
	got := renderV3Parity(t)
	if got == string(want) {
		return
	}
	actual := golden + ".actual"
	if err := os.WriteFile(actual, []byte(got), 0o600); err != nil {
		t.Logf("could not write %s: %v", actual, err)
	}
	t.Errorf("a weighted configuration no longer scores as it did in v3.\n"+
		"Compare %s with %s. If the change is deliberate, re-capture the golden and say so on the PR.", golden, actual)
}

// resolveV3 is the resolution a v3 deployment ran: a pack taxonomy and a
// project's own weights, nothing else.
func resolveV3(r v3Resolution) SeverityConfig {
	return ResolveSeverity(DefaultConfig().Severity, r.taxonomy, r.project)
}
