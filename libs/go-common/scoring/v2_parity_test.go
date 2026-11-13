package scoring

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// The backward-compatibility guarantee, as a golden file.
//
// Configurable severity weights change what the engine CAN do without
// changing what it does for a deployment that configures nothing: a pack with
// no risk taxonomy and a project with no category weights must score exactly
// as they did before weights existed. Asserting that case by case invites the
// assertion to be updated alongside the behaviour it is supposed to pin, so
// the whole scoring sub-document is rendered and compared byte for byte.
//
// The golden was captured from the tree BEFORE the weight lookup landed and
// committed on its own, which is what makes it evidence. A golden captured
// after the change would agree with the change by construction and prove
// nothing at all.
//
// It covers every output field, not only the severity half, because the
// severity axis and the confidence axis meet in the priority rollup: a weight
// that moved a band would move a tier, and the tier is what the default list
// order reads.

// parityCase is one document, named for what about it is interesting.
type parityCase struct {
	name string
	in   Input
	// cited is the severity half a recommendation inherits. Non-nil marks the
	// case as a recommendation, which is a different entry point rather than
	// a flag on the input.
	cited []InheritedSeverity
}

// parityCorpus is the fixture corpus: the verdict ladder, the magnitude
// ladder, every dock, and the two recommendation outcomes.
//
// Hand-written rather than generated. A generated cross-product would be
// larger and would still miss the cases that matter here -- a duplicated
// figure id, an amount in a foreign currency, an unresolved reference -- while
// making the golden unreadable as a record of what the engine used to answer.
func parityCorpus() []parityCase {
	holdingShare := gomodels.Figure{ID: "f1", Value: 24.66, Unit: gomodels.UnitPercent}
	holdingShareV := gomodels.FigureVerdict{
		ID: "f1", Display: "24.66%", Status: gomodels.FigureHolds,
		Claimed: 24.66, Evaluated: 24.66,
	}
	midShare := gomodels.Figure{ID: "f2", Value: 7.5, Unit: gomodels.UnitPercent}
	midShareV := gomodels.FigureVerdict{
		ID: "f2", Display: "7.50%", Status: gomodels.FigureHolds,
		Claimed: 7.5, Evaluated: 7.5,
	}
	lowShare := gomodels.Figure{ID: "f3", Value: 1.2, Unit: gomodels.UnitPercent}
	lowShareV := gomodels.FigureVerdict{
		ID: "f3", Display: "1.20%", Status: gomodels.FigureHolds,
		Claimed: 1.2, Evaluated: 1.2,
	}
	amount := gomodels.Figure{ID: "a1", Value: 8_476_238_553, Unit: gomodels.UnitCurrency, Currency: "$"}
	amountV := gomodels.FigureVerdict{
		ID: "a1", Display: "$8.48B", Status: gomodels.FigureHolds,
		Claimed: 8_476_238_553, Evaluated: 8_476_238_553,
	}
	refutedShare := gomodels.Figure{ID: "f4", Value: 40, Unit: gomodels.UnitPercent}
	refutedShareV := gomodels.FigureVerdict{
		ID: "f4", Display: "40.00%", Status: gomodels.FigureFails,
		Claimed: 40, Evaluated: 3,
	}

	cfg := DefaultConfig()

	cases := []parityCase{
		// The verdict ladder, with one magnitude held constant, so the
		// confidence half of each row is comparable down the column.
		{name: "confirmed, high share", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "supported, high share", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "partial, high share", in: Input{
			Verdict: valmodels.StatusPartial, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "rejected, high share", in: Input{
			Verdict: valmodels.StatusRejected, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "unverifiable, high share", in: Input{
			Verdict: valmodels.StatusUnverifiable, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "no verdict at all, high share", in: Input{
			AreaID:  "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},

		// The magnitude ladder.
		{name: "confirmed, medium share", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "engagement",
			Figures: []gomodels.Figure{midShare}, FigureVerdicts: []gomodels.FigureVerdict{midShareV},
			Config: cfg,
		}},
		{name: "confirmed, low share", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "engagement",
			Figures: []gomodels.Figure{lowShare}, FigureVerdicts: []gomodels.FigureVerdict{lowShareV},
			Config: cfg,
		}},
		{name: "confirmed, nothing quantified", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "compliance",
			Config: cfg,
		}},
		{name: "confirmed, a grounded amount and no amount bands", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue", HomeCurrency: "$",
			Figures: []gomodels.Figure{amount}, FigureVerdicts: []gomodels.FigureVerdict{amountV},
			Config: cfg,
		}},
		{name: "confirmed, an amount and a share together", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue", HomeCurrency: "$",
			Figures:        []gomodels.Figure{amount, midShare},
			FigureVerdicts: []gomodels.FigureVerdict{amountV, midShareV},
			Config:         cfg,
		}},
		{name: "confirmed, an amount in a foreign currency", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue", HomeCurrency: "€",
			Figures: []gomodels.Figure{amount}, FigureVerdicts: []gomodels.FigureVerdict{amountV},
			Config: cfg,
		}},
		{name: "confirmed, the only figure refuted", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue",
			Figures: []gomodels.Figure{refutedShare}, FigureVerdicts: []gomodels.FigureVerdict{refutedShareV},
			Config: cfg,
		}},
		{name: "confirmed, a duplicated figure id", in: Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue",
			Figures: []gomodels.Figure{
				{ID: "f1", Value: 40, Unit: gomodels.UnitPercent},
				{ID: "f1", Value: 1, Unit: gomodels.UnitPercent},
			},
			FigureVerdicts: []gomodels.FigureVerdict{
				{ID: "f1", Display: "1.00%", Status: gomodels.FigureHolds, Claimed: 1, Evaluated: 1},
			},
			Config: cfg,
		}},

		// Every dock, alone and together, against one magnitude.
		{name: "supported, two quality caveats", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue",
			QualityCaveats: []gowarehouse.QualityCaveat{
				{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
				{Kind: gowarehouse.QualitySampled, Detail: "sampled at 10%"},
			},
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "supported, three quality caveats hit the cap", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue",
			QualityCaveats: []gowarehouse.QualityCaveat{
				{Kind: gowarehouse.QualityWithheld},
				{Kind: gowarehouse.QualitySampled},
				{Kind: gowarehouse.QualityTruncated},
			},
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "supported, refuted arithmetic and a refuted claim hit the cap", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare, refutedShare},
			FigureVerdicts: []gomodels.FigureVerdict{
				holdingShareV, refutedShareV,
			},
			QuantifierVerdicts: []gomodels.QuantifierVerdict{
				{Claim: "only", Kind: "only", Status: gomodels.QuantifierFails},
			},
			Config: cfg,
		}},
		{name: "supported, an unresolved reference", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue", UnresolvedFigures: 2,
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "supported, checking ran past the retained sample", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue", StepReadBeyondRetained: true,
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},
		{name: "supported, every dock at once", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue", HomeCurrency: "$",
			UnresolvedFigures: 1, StepReadBeyondRetained: true,
			QualityCaveats: []gowarehouse.QualityCaveat{
				{Kind: gowarehouse.QualityWithheld},
				{Kind: gowarehouse.QualityRestricted},
			},
			Figures: []gomodels.Figure{holdingShare, refutedShare},
			FigureVerdicts: []gomodels.FigureVerdict{
				holdingShareV, refutedShareV,
			},
			QuantifierVerdicts: []gomodels.QuantifierVerdict{
				{Claim: "top-3", Kind: "rank", Status: gomodels.QuantifierFails},
			},
			Config: cfg,
		}},
		{name: "refuter disabled is never docked", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue", RefuterDisabled: true,
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
			Config: cfg,
		}},

		// A zero-valued config falls back to this build's own calibration.
		{name: "a zero config falls back to the default", in: Input{
			Verdict: valmodels.StatusSupported, AreaID: "revenue",
			Figures: []gomodels.Figure{holdingShare}, FigureVerdicts: []gomodels.FigureVerdict{holdingShareV},
		}},
	}

	// The recommendation entry point: severity inherited, or nothing to
	// inherit.
	cases = append(cases,
		parityCase{
			name: "recommendation inheriting the highest-severity insight",
			in: Input{
				Verdict: valmodels.StatusSupported, Config: cfg,
			},
			cited: []InheritedSeverity{
				{
					MagnitudeScore: 0.60, SeverityBand: gomodels.SeverityBandMedium, SeverityScore: 0.60,
					SeverityReason: "Medium — banded on 7.50%, checked against warehouse rows.",
					AreaWeight:     1.0,
					WeightSource:   &gomodels.WeightSource{Kind: gomodels.WeightSourceDefault},
				},
				{
					MagnitudeScore: 1.00, SeverityBand: gomodels.SeverityBandHigh, SeverityScore: 1.00,
					SeverityReason: "High — banded on 24.66%, checked against warehouse rows.",
					AreaWeight:     1.0,
					WeightSource:   &gomodels.WeightSource{Kind: gomodels.WeightSourceDefault},
				},
			},
		},
		parityCase{
			name: "recommendation with nothing to inherit",
			in: Input{
				Verdict: valmodels.StatusPartial, Config: cfg,
			},
			cited: []InheritedSeverity{{}},
		},
	)
	return cases
}

// renderParityCorpus scores every case and renders the results as the golden
// file holds them: the case name, then its whole scoring sub-document as JSON.
//
// JSON rather than %+v, because the question the golden answers is what a
// CLIENT reads: the json tags and the omitempty decisions are part of the
// contract, and a Go-syntax dump would hide a field that stopped being
// omitted.
func renderParityCorpus(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, c := range parityCorpus() {
		var got gomodels.Scoring
		if c.cited != nil {
			got = ScoreRecommendation(c.in, c.cited)
		} else {
			got = Score(c.in)
		}
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		b.WriteString("## " + c.name + "\n")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	return b.String()
}

// TestTheUnconfiguredEngineScoresExactlyAsItDidBeforeWeights -- the
// compatibility criterion, byte for byte.
//
// A failure here is not necessarily a bug: it is a change in what an
// unconfigured deployment's documents say, which is a decision rather than a
// detail. Re-capture the golden only once that decision has been made
// deliberately, and say so on the pull request.
func TestTheUnconfiguredEngineScoresExactlyAsItDidBeforeWeights(t *testing.T) {
	const golden = "testdata/v2_parity.golden"
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v", golden, err)
	}
	got := renderParityCorpus(t)
	if got == string(want) {
		return
	}
	// The whole rendering, written beside the golden, so the diff is one
	// command away rather than reconstructed from a test log.
	actual := golden + ".actual"
	if err := os.WriteFile(actual, []byte(got), 0o600); err != nil {
		t.Logf("could not write %s: %v", actual, err)
	}
	t.Errorf("the unconfigured engine no longer scores as it did before weights.\n"+
		"Compare %s with %s. If the change is deliberate, re-capture the golden and say so on the PR.", golden, actual)
}
