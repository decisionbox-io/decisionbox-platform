package scoring

import (
	"fmt"
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// Re-weighing a stored document, the primitive behind any preview of what a
// change of weights would do to a run that already happened.
//
// Two properties, and between them they are the whole claim:
//
//   - identity: a document re-weighed under the configuration it was scored
//     with comes back exactly as it was stored;
//   - composition: a document scored under one configuration and re-weighed
//     under another is exactly what scoring it under the other would have
//     produced.
//
// The second is what makes a preview honest. Both are asserted over every
// insight in the two parity corpora, whose cases were chosen to cover the
// magnitude ladder, every confidence band, the per-area override and the clamp.

// reweighCase is one insight document and the area facts a run would pin for
// it.
type reweighCase struct {
	name string
	in   Input
}

func (c reweighCase) area() gomodels.RunAreaRisk {
	return gomodels.RunAreaRisk{RiskCategories: c.in.AreaCategories, SeverityWeight: c.in.AreaOverride}
}

// reweighCorpus is every insight case in the v2 and v3 parity corpora.
// Recommendations are left out: their severity is inherited, not weighed.
func reweighCorpus() []reweighCase {
	var out []reweighCase
	for _, c := range parityCorpus() {
		if c.cited == nil {
			out = append(out, reweighCase{name: "v2/" + c.name, in: c.in})
		}
	}
	for _, c := range v3ScoreCorpus() {
		if c.cited == nil {
			out = append(out, reweighCase{name: "v3/" + c.name, in: c.in})
		}
	}
	return out
}

// alternativeConfigs are the configurations a document is re-weighed under:
// every way the layers can disagree with what it was scored with.
func alternativeConfigs() map[string]Config {
	build := func(deployment, project map[string]float64) Config {
		cfg := DefaultConfig()
		cfg.Severity = ResolveSeverity(cfg.Severity, v3Taxonomy(), deployment, project)
		return cfg
	}
	return map[string]Config{
		"the shipped calibration, unweighted": DefaultConfig(),
		"the pack alone":                      build(nil, nil),
		"a deployment raising compliance":     build(map[string]float64{"compliance": 3, "data_quality": 0.5}, nil),
		"a deployment lowering everything":    build(map[string]float64{"compliance": 0.2, "revenue_leak": 0.2, "engagement": 0.2, "data_quality": 0.2}, nil),
		"a project overruling the deployment": build(map[string]float64{"compliance": 3}, map[string]float64{"compliance": 0.5, "fraud": 2}),
	}
}

func TestReweighUnderTheSameConfigurationChangesNothing(t *testing.T) {
	for _, c := range reweighCorpus() {
		t.Run(c.name, func(t *testing.T) {
			stored := Score(c.in)
			got, ok := Reweigh(stored, c.area(), c.in.Config)
			if !ok {
				t.Fatal("a document the current engine scored was not re-weighable")
			}
			if !reflect.DeepEqual(got, stored) {
				t.Errorf("re-weighed under its own configuration, the document moved:\n got  %+v\n want %+v", got, stored)
			}
		})
	}
}

func TestReweighUnderAnotherConfigurationIsWhatScoringUnderItProduces(t *testing.T) {
	for _, c := range reweighCorpus() {
		stored := Score(c.in)
		for name, cfg := range alternativeConfigs() {
			t.Run(fmt.Sprintf("%s under %s", c.name, name), func(t *testing.T) {
				fresh := c.in
				fresh.Config = cfg
				want := Score(fresh)

				got, ok := Reweigh(stored, c.area(), cfg)
				if !ok {
					t.Fatal("not re-weighable")
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("re-weighed and scored disagree:\n reweighed %+v\n scored    %+v", got, want)
				}
			})
		}
	}
}

// TestReweighOnlyMovesTheWeightedHalf -- the magnitude, the driver and the
// confidence axis are facts about the run, and a weight cannot move them.
func TestReweighOnlyMovesTheWeightedHalf(t *testing.T) {
	cfg := DefaultConfig()
	stored := scoreArea(cfg, 1.2, []string{"compliance"}, nil)

	heavy := DefaultConfig()
	heavy.Severity = ResolveSeverity(heavy.Severity, nil, map[string]float64{"compliance": 3}, nil)
	got, ok := Reweigh(stored, gomodels.RunAreaRisk{RiskCategories: []string{"compliance"}}, heavy)
	if !ok {
		t.Fatal("not re-weighable")
	}

	if got.SeverityBand != gomodels.SeverityBandHigh || got.AreaWeight != 3 || got.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("weighted half = %s / ×%v / %s, want high / ×3 / critical", got.SeverityBand, got.AreaWeight, got.PriorityTier)
	}
	if got.MagnitudeScore != stored.MagnitudeScore || !reflect.DeepEqual(got.SeverityDriver, stored.SeverityDriver) {
		t.Error("the magnitude or its driver moved")
	}
	if got.ConfidenceBand != stored.ConfidenceBand || got.ConfidenceScore != stored.ConfidenceScore ||
		got.ConfidenceReason != stored.ConfidenceReason || !reflect.DeepEqual(got.ConfidenceDocks, stored.ConfidenceDocks) {
		t.Error("the confidence axis moved")
	}
	if got.EngineVersion != stored.EngineVersion {
		t.Errorf("engine version %d, want the stored %d", got.EngineVersion, stored.EngineVersion)
	}
}

// TestReweighRefusesWhatItCannotReweigh -- no severity half to re-weigh, or
// one a different calibration produced, comes back untouched and says so.
func TestReweighRefusesWhatItCannotReweigh(t *testing.T) {
	stored := scoreArea(DefaultConfig(), 24.66, []string{"compliance"}, nil)

	confidenceOnly := stored
	confidenceOnly.SeverityBand = ""

	otherVersion := stored
	otherVersion.EngineVersion = EngineVersion + 1

	for name, prior := range map[string]gomodels.Scoring{
		"no severity half":         confidenceOnly,
		"another engine version":   otherVersion,
		"never scored by anything": {},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := Reweigh(prior, gomodels.RunAreaRisk{RiskCategories: []string{"compliance"}}, DefaultConfig())
			if ok {
				t.Error("reported re-weighable")
			}
			if !reflect.DeepEqual(got, prior) {
				t.Errorf("the prior was changed: %+v", got)
			}
		})
	}
}

// TestReweighDoesNotWriteIntoThePrior -- a preview re-weighs documents it
// then compares against, so writing through a shared slice or pointer would
// make every comparison report "nothing moved".
func TestReweighDoesNotWriteIntoThePrior(t *testing.T) {
	stored := Score(Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "a", AreaCategories: []string{"compliance", "security"},
		Config: DefaultConfig(),
	})
	before := fmt.Sprintf("%+v %+v %+v %v", stored, *stored.SeverityDriver, *stored.WeightSource, stored.RiskFlags)

	heavy := DefaultConfig()
	heavy.Severity = ResolveSeverity(heavy.Severity, nil, map[string]float64{"security": 2}, nil)
	if _, ok := Reweigh(stored, gomodels.RunAreaRisk{RiskCategories: []string{"security"}}, heavy); !ok {
		t.Fatal("not re-weighable")
	}

	after := fmt.Sprintf("%+v %+v %+v %v", stored, *stored.SeverityDriver, *stored.WeightSource, stored.RiskFlags)
	if before != after {
		t.Errorf("the prior changed:\n before %s\n after  %s", before, after)
	}
}
