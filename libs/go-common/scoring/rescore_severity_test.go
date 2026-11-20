package scoring

import (
	"reflect"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// Re-scoring a stored document's severity axis under a different
// configuration, which is what the enterprise settings page previews with.
//
// The claim the preview makes to a customer is not "here is roughly what will
// happen" -- it is "this is what the next run produces". A preview computed by
// a second code path could only be as true as its author's memory of the
// first, so the entry point under test is the engine's own, and what is
// asserted is that replaying a stored document gives the same sub-document a
// fresh score of the same document would.
//
// Everything below compares the WHOLE sub-document with DeepEqual rather than
// the fields each case is about. The two paths reach the same struct through
// different arithmetic -- one from a figure list, one from a stored driver --
// and a field either of them forgot to carry is exactly the defect this is
// here to catch.

// rescoreConfigs is the sweep: every way a deployment's configuration can
// differ from the one a stored document was scored under.
//
// Each is named for the customer action behind it, because that is what makes
// a failure legible: "cleared the thresholds" is a thing someone does on the
// settings page, and the case it exercises -- an amount-banded document
// falling back to the share it beat -- is the one the driver's Share slot
// exists for.
func rescoreConfigs(t *testing.T) []struct {
	name string
	cfg  Config
} {
	t.Helper()

	withBands := DefaultConfig()
	withBands.Severity.AmountBands = []MagnitudeBand{
		{At: 1_000_000, Band: gomodels.SeverityBandHigh},
		{At: 100_000, Band: gomodels.SeverityBandMedium},
	}

	bandsInLiras := withBands
	bandsInLiras.Severity.AmountCurrency = "₺"

	bandsInDollars := withBands
	bandsInDollars.Severity.AmountCurrency = "$"

	highBands := DefaultConfig()
	highBands.Severity.AmountBands = []MagnitudeBand{{At: 50_000_000_000, Band: gomodels.SeverityBandHigh}}

	weighted := DefaultConfig()
	weighted.Severity = ApplyOverride(weighted.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0, "revenue": 2.0, "engagement": 0.5},
	})

	weightedWithBands := withBands
	weightedWithBands.Severity = ApplyOverride(weightedWithBands.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3.0, "revenue": 2.0},
	})

	narrowShares := DefaultConfig()
	narrowShares.Severity.ShareBands = []MagnitudeBand{
		{At: 50, Band: gomodels.SeverityBandHigh},
		{At: 30, Band: gomodels.SeverityBandMedium},
	}

	return []struct {
		name string
		cfg  Config
	}{
		{"nothing configured", DefaultConfig()},
		{"weights set", weighted},
		{"thresholds set", withBands},
		{"thresholds set, in the document's own currency", bandsInDollars},
		{"thresholds set, in another currency", bandsInLiras},
		{"thresholds set above every amount", highBands},
		{"weights and thresholds set together", weightedWithBands},
		{"the share ladder recalibrated", narrowShares},
	}
}

// TestRescoreSeverityAgreesWithAFreshScore is the acceptance criterion
// "the preview agrees exactly with what the next run produces", mechanised.
//
// Every stored-document shape the engine can produce (the parity corpus) is
// scored under every configuration, then re-scored from each of those stored
// forms under every other configuration. A disagreement anywhere means the
// settings page would show a customer a reordering their next run would not
// perform.
//
// Recommendations are excluded, and not as an economy: a recommendation's
// severity is INHERITED from the insights it cites rather than derived from
// its own figures, so re-scoring one means re-inheriting, which is the
// citing insights' business and not this function's. The preview covers
// insights for the same reason.
func TestRescoreSeverityAgreesWithAFreshScore(t *testing.T) {
	configs := rescoreConfigs(t)

	for _, c := range parityCorpus() {
		if c.cited != nil {
			continue
		}
		for _, from := range configs {
			for _, to := range configs {
				t.Run(c.name+" / "+from.name+" → "+to.name, func(t *testing.T) {
					stored := c.in
					stored.Config = from.cfg
					prior := Score(stored)

					fresh := c.in
					fresh.Config = to.cfg
					want := Score(fresh)

					got := RescoreSeverity(prior, fresh)

					if !reflect.DeepEqual(got, want) {
						t.Errorf("the replay disagrees with a fresh score.\n got %+v\nwant %+v", got, want)
						if got.SeverityDriver != nil && want.SeverityDriver != nil {
							t.Errorf("driver: got %+v\n        want %+v", *got.SeverityDriver, *want.SeverityDriver)
						}
					}
				})
			}
		}
	}
}

// TestRescoreSeverityAgreesWithAFreshScoreOnWeightedAreas extends the sweep to
// the inputs the corpus does not carry: an area mapped to risk categories, and
// an area with its own override.
//
// Separate from the corpus rather than added to it, because the corpus is a
// golden's fixture list and adding rows to it would re-capture a file whose
// whole job is to not move.
func TestRescoreSeverityAgreesWithAFreshScoreOnWeightedAreas(t *testing.T) {
	share := gomodels.Figure{ID: "f1", Value: 7.5, Unit: gomodels.UnitPercent}
	shareV := gomodels.FigureVerdict{ID: "f1", Display: "7.50%", Status: gomodels.FigureHolds, Claimed: 7.5, Evaluated: 7.5}
	amount := gomodels.Figure{ID: "a1", Value: 4_000_000, Unit: gomodels.UnitCurrency, Currency: "$"}
	amountV := gomodels.FigureVerdict{ID: "a1", Display: "$4.00M", Status: gomodels.FigureHolds, Claimed: 4_000_000, Evaluated: 4_000_000}

	override := 2.5
	illegal := 0.0

	areas := []struct {
		name string
		in   Input
	}{
		{"one category", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "pii", AreaCategories: []string{"compliance"},
			HomeCurrency: "$", Figures: []gomodels.Figure{share, amount},
			FigureVerdicts: []gomodels.FigureVerdict{shareV, amountV},
		}},
		{"two categories, the larger wins", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "leak", AreaCategories: []string{"engagement", "compliance"},
			Figures: []gomodels.Figure{share}, FigureVerdicts: []gomodels.FigureVerdict{shareV},
		}},
		{"a category nothing weights", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "x", AreaCategories: []string{"unknown_class"},
			Figures: []gomodels.Figure{share}, FigureVerdicts: []gomodels.FigureVerdict{shareV},
		}},
		{"an area override", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "pii", AreaCategories: []string{"compliance"},
			AreaOverride: &override,
			Figures:      []gomodels.Figure{share}, FigureVerdicts: []gomodels.FigureVerdict{shareV},
		}},
		{"an illegal area override, which is ignored", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "pii", AreaCategories: []string{"compliance"},
			AreaOverride: &illegal,
			Figures:      []gomodels.Figure{share}, FigureVerdicts: []gomodels.FigureVerdict{shareV},
		}},
		{"an amount alone, in a weighted area", Input{
			Verdict: valmodels.StatusConfirmed, AreaID: "revenue", AreaCategories: []string{"revenue"},
			HomeCurrency: "$", Figures: []gomodels.Figure{amount},
			FigureVerdicts: []gomodels.FigureVerdict{amountV},
		}},
	}

	configs := rescoreConfigs(t)
	for _, a := range areas {
		for _, from := range configs {
			for _, to := range configs {
				t.Run(a.name+" / "+from.name+" → "+to.name, func(t *testing.T) {
					stored := a.in
					stored.Config = from.cfg
					prior := Score(stored)

					fresh := a.in
					fresh.Config = to.cfg
					want := Score(fresh)

					if got := RescoreSeverity(prior, fresh); !reflect.DeepEqual(got, want) {
						t.Errorf("the replay disagrees with a fresh score.\n got %+v\nwant %+v", got, want)
					}
				})
			}
		}
	}
}

// TestRescoreSeverityFreezesTheConfidenceAxis — the invariant named, rather
// than left implied by the equality above.
//
// A stored confidence is a statement about a check that ran against warehouse
// rows. No weight and no money threshold has any bearing on it, and a re-score
// from storage does not hold the verdict, the caveats or the published prose
// it would take to re-derive one. So the axis is kept, and the input's own
// verdict is deliberately ignored.
func TestRescoreSeverityFreezesTheConfidenceAxis(t *testing.T) {
	prior := Score(Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "revenue",
		Figures:        []gomodels.Figure{{ID: "f1", Value: 24.66, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Display: "24.66%", Status: gomodels.FigureHolds, Claimed: 24.66, Evaluated: 24.66}},
		Config:         DefaultConfig(),
	})

	weighted := DefaultConfig()
	weighted.Severity = ApplyOverride(weighted.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3},
	})
	// A verdict that would score far lower, to prove it is not read.
	got := RescoreSeverity(prior, Input{
		Verdict: valmodels.StatusRejected, AreaID: "revenue",
		Config: weighted,
	})

	if got.ConfidenceBand != prior.ConfidenceBand ||
		got.ConfidenceBase != prior.ConfidenceBase ||
		got.ConfidenceScore != prior.ConfidenceScore ||
		got.ConfidenceReason != prior.ConfidenceReason {
		t.Errorf("the confidence axis moved:\n got %+v\nwant %+v", got, prior)
	}
	if !reflect.DeepEqual(got.ConfidenceDocks, prior.ConfidenceDocks) {
		t.Errorf("the docks moved: got %+v, want %+v", got.ConfidenceDocks, prior.ConfidenceDocks)
	}
	if got.EngineVersion != prior.EngineVersion {
		t.Errorf("engine version = %d, want the prior's %d", got.EngineVersion, prior.EngineVersion)
	}
}

// TestRescoreSeverityMovesThePriorityTier. Priority must move when severity
// does, because one of the matrix's two inputs just changed -- leaving the old
// tier would leave the document disagreeing with its own bands, which is the
// one thing a breakdown cannot survive.
func TestRescoreSeverityMovesThePriorityTier(t *testing.T) {
	in := Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "pii", AreaCategories: []string{"compliance"},
		Figures:        []gomodels.Figure{{ID: "f1", Value: 1.2, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Display: "1.20%", Status: gomodels.FigureHolds, Claimed: 1.2, Evaluated: 1.2}},
		Config:         DefaultConfig(),
	}
	prior := Score(in)
	if prior.PriorityTier != gomodels.PriorityTierLow {
		t.Fatalf("fixture tier = %q, want low", prior.PriorityTier)
	}

	weighted := DefaultConfig()
	weighted.Severity = ApplyOverride(weighted.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3},
	})
	next := in
	next.Config = weighted

	got := RescoreSeverity(prior, next)
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("severity = %q, want high (0.25 × 3 = 0.75)", got.SeverityBand)
	}
	if got.PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("tier = %q, want critical — the band moved and the tier must follow", got.PriorityTier)
	}
}

// TestRescoreSeverityLeavesALegacyDocumentAlone.
//
// A document with no severity band was scored by an engine that had no
// severity axis. Giving it one here would date a number to a run that never
// computed it, so the preview counts such documents instead of previewing
// them.
func TestRescoreSeverityLeavesALegacyDocumentAlone(t *testing.T) {
	prior := gomodels.Scoring{
		ConfidenceBand:  gomodels.ConfidenceBandHigh,
		ConfidenceScore: 0.95,
		EngineVersion:   1,
	}

	got := RescoreSeverity(prior, Input{AreaID: "revenue", Config: DefaultConfig()})

	if !reflect.DeepEqual(got, prior) {
		t.Errorf("a legacy document was rewritten:\n got %+v\nwant %+v", got, prior)
	}
}

// TestRescoreSeverityDropsFlagsACategoryNoLongerCarries. The flags are read
// off the same resolved weight map the weight is, so a category a
// configuration stops weighting is a category its documents stop being
// flagged with -- otherwise a chip would describe a document under a taxonomy
// that no longer exists.
func TestRescoreSeverityDropsFlagsACategoryNoLongerCarries(t *testing.T) {
	weighted := DefaultConfig()
	weighted.Severity = ApplyOverride(weighted.Severity, gomodels.ScoringOverride{
		CategoryWeights: map[string]float64{"revenue_leak": 2},
	})
	in := Input{
		Verdict: valmodels.StatusConfirmed, AreaID: "leak", AreaCategories: []string{"revenue_leak"},
		Figures:        []gomodels.Figure{{ID: "f1", Value: 7.5, Unit: gomodels.UnitPercent}},
		FigureVerdicts: []gomodels.FigureVerdict{{ID: "f1", Display: "7.50%", Status: gomodels.FigureHolds, Claimed: 7.5, Evaluated: 7.5}},
		Config:         weighted,
	}
	prior := Score(in)
	if len(prior.RiskFlags) != 1 {
		t.Fatalf("fixture flags = %v, want one", prior.RiskFlags)
	}

	// The weight removed, which is what clearing it on the settings page does.
	cleared := in
	cleared.Config = DefaultConfig()

	if got := RescoreSeverity(prior, cleared); got.RiskFlags != nil {
		t.Errorf("flags = %v, want none once nothing declares the category", got.RiskFlags)
	}
}

// TestTheReplayNormalisesAStoredCurrency.
//
// A document written before CurrencySymbol upper-cased the codes it does not
// map carries whatever casing that run recorded. Reading it raw sent `cad`
// into a character-for-character comparison against a `CAD` ladder: the replay
// ranked the finding on its share while a fresh run ranked the same evidence
// on its amount, which is the preview disagreeing with the run.
func TestTheReplayNormalisesAStoredCurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity.AmountBands = []MagnitudeBand{{At: 1_000_000, Band: gomodels.SeverityBandHigh}}
	cfg.Severity.AmountCurrency = "CAD"

	// A stored driver carrying the old lower-case spelling.
	prior := gomodels.Scoring{
		SeverityBand:   gomodels.SeverityBandLow,
		MagnitudeScore: 0.25,
		SeverityDriver: &gomodels.SeverityDriver{
			Kind:       gomodels.SeverityDriverAmount,
			Quantified: true,
			Amount: &gomodels.SeverityFigure{
				Value: 2_400_000, Currency: "cad", Unit: gomodels.UnitCurrency, Display: "CAD 2.40M",
			},
		},
	}

	got := RescoreSeverity(prior, Input{AreaID: "revenue", Config: cfg})

	if got.SeverityDriver == nil || got.SeverityDriver.Kind != gomodels.SeverityDriverAmount {
		t.Fatalf("driver = %+v, want the amount to have banded it", got.SeverityDriver)
	}
	if got.SeverityBand != gomodels.SeverityBandHigh {
		t.Errorf("band = %q, want high — the ladder's currency and the stored one are the same currency",
			got.SeverityBand)
	}
}

// TestTheReplayRecoversAnAmountTheStoringRunDropped.
//
// Documents scored before the engine's currency comparison was normalised
// carry a driver with NO amount, where the profile and the figure stated the
// same currency in different casing: the old eligibility check excluded the
// figure, so there is nothing on the driver to replay. Replaying the driver
// alone then showed a new money ladder changing nothing, while the next run —
// which no longer drops the figure — ranked the same evidence far higher. A
// preview saying "nothing moves" about a configuration that moves plenty is
// the one thing it may not say.
// TestTheReplayNeverBandsOnEvidenceARunWouldNotRead.
//
// A run admits an amount only when its PUBLISHED prose states the figure and
// the figure's own arithmetic held, and it resolves home currency from the
// steps those figures cite. A stored document carries neither of those
// decisions, so an earlier version of the replay — which re-derived candidates
// from the full stored figure list against the primary datasource's currency
// whenever the driver had no amount — banded findings a fresh run leaves where
// they are. The replay reads the stored driver, and that is the whole contract.
func TestTheReplayNeverBandsOnEvidenceARunWouldNotRead(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Severity.AmountBands = []MagnitudeBand{{At: 1_000_000, Band: gomodels.SeverityBandHigh}}
	cfg.Severity.AmountCurrency = "CAD"

	// A document a run banded on its share, having read no amount it could use.
	prior := gomodels.Scoring{
		SeverityBand:   gomodels.SeverityBandLow,
		MagnitudeScore: 0.25,
		SeverityDriver: &gomodels.SeverityDriver{
			Kind:       gomodels.SeverityDriverShare,
			Quantified: true,
			Figure:     &gomodels.SeverityFigure{Value: 1.2, Unit: gomodels.UnitPercent, Display: "1.20%"},
		},
	}
	// Evidence stored beside it that clears this ladder twice over. The run
	// that stored the document did not band on it; neither may the preview.
	in := Input{
		AreaID: "revenue", Config: cfg,
		Figures: []gomodels.Figure{
			{ID: "a1", Value: 2_400_000, Unit: gomodels.UnitCurrency, Currency: "cad"},
			{ID: "f1", Value: 1.2, Unit: gomodels.UnitPercent},
		},
		FigureVerdicts: []gomodels.FigureVerdict{
			{ID: "a1", Display: "CAD 2.40M", Status: gomodels.FigureHolds, Claimed: 2_400_000, Evaluated: 2_400_000},
			{ID: "f1", Display: "1.20%", Status: gomodels.FigureHolds, Claimed: 1.2, Evaluated: 1.2},
		},
		HomeCurrency: "CAD",
	}
	got := RescoreSeverity(prior, in)

	if got.SeverityDriver == nil || got.SeverityDriver.Kind != gomodels.SeverityDriverShare {
		t.Fatalf("driver = %+v, want the stored share — the amount was never a run's to read", got.SeverityDriver)
	}
	if got.SeverityBand == gomodels.SeverityBandHigh {
		t.Errorf("band = high, want the ladder to leave a share-banded document alone")
	}

	// And it makes no difference whether the caller supplies the evidence at
	// all: the two answers are the same answer.
	bare := RescoreSeverity(prior, Input{AreaID: "revenue", Config: cfg})
	if bare.SeverityBand != got.SeverityBand || bare.MagnitudeScore != got.MagnitudeScore {
		t.Errorf("band/magnitude = %q/%v with evidence and %q/%v without; the replay must read only the driver",
			got.SeverityBand, got.MagnitudeScore, bare.SeverityBand, bare.MagnitudeScore)
	}
}

// TestAProvidersLadderAppliesWhateverFormItsCurrencyTook.
//
// The ladder's currency and the document's home currency arrive from different
// places — a provider or a hand-edited document on one side, the agent's own
// resolution on the other — so they are routinely the same currency in
// different forms. A raw comparison meant a provider returning `USD` had its
// thresholds accepted, stored, pinned and then silently never applied to a run
// whose home currency had been normalised to `$`; and in the other direction a
// fresh score banded on the share while the replay banded on the amount, which
// is the preview disagreeing with the run.
func TestAProvidersLadderAppliesWhateverFormItsCurrencyTook(t *testing.T) {
	for _, tc := range []struct{ name, ladder, home string }{
		{"code against symbol", "USD", "$"},
		{"symbol against code", "$", "USD"},
		{"both codes, different casing", "cad", "CAD"},
		{"both the same", "$", "$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Severity.AmountBands = []MagnitudeBand{{At: 1_000_000, Band: gomodels.SeverityBandHigh}}
			cfg.Severity.AmountCurrency = tc.ladder

			prior := gomodels.Scoring{
				SeverityBand:   gomodels.SeverityBandLow,
				MagnitudeScore: 0.25,
				SeverityDriver: &gomodels.SeverityDriver{
					Kind: gomodels.SeverityDriverAmount, Quantified: true,
					Amount: &gomodels.SeverityFigure{
						Value: 2_400_000, Currency: tc.home, Unit: gomodels.UnitCurrency,
					},
				},
			}

			got := RescoreSeverity(prior, Input{AreaID: "revenue", Config: cfg})
			if got.SeverityBand != gomodels.SeverityBandHigh {
				t.Errorf("band = %q for a %s ladder on a %s amount, want high — same currency, different form",
					got.SeverityBand, tc.ladder, tc.home)
			}
		})
	}

	// And a ladder in a genuinely different currency still ranks nothing.
	cfg := DefaultConfig()
	cfg.Severity.AmountBands = []MagnitudeBand{{At: 1_000_000, Band: gomodels.SeverityBandHigh}}
	cfg.Severity.AmountCurrency = "TRY"
	prior := gomodels.Scoring{
		SeverityBand:   gomodels.SeverityBandLow,
		MagnitudeScore: 0.25,
		SeverityDriver: &gomodels.SeverityDriver{
			Kind: gomodels.SeverityDriverAmount, Quantified: true,
			Amount: &gomodels.SeverityFigure{Value: 2_400_000, Currency: "$", Unit: gomodels.UnitCurrency},
		},
	}
	if got := RescoreSeverity(prior, Input{AreaID: "revenue", Config: cfg}); got.SeverityBand == gomodels.SeverityBandHigh {
		t.Error("a lira ladder banded a dollar amount high")
	}
}
