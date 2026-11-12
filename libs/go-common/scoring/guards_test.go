package scoring

import (
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// The guards in this file are the ones a caller is not expected to reach.
// They exist because the alternative was a silent wrong answer -- the
// fall-through to `low` that a default arm produces, and a severity sentence
// with no band word in it -- and an unreachable guard that nobody exercises
// is indistinguishable from one that does not work.
//
// RescoreConfidence short-circuits on an empty severity band before it ever
// calls priorityTier, so the matrix's own guard is NOT covered by the
// re-score path's test: that test asserts the same outcome by a different
// route. These call the guards directly.

// TestAnUnrecognisedSeverityBandGetsNoTier -- the matrix answers "" for a band
// it has no cell for, and never `low`.
//
// `low` is a judgement: "this would not matter if it were true". An absent or
// unknown band means nothing has judged it, and the two must not read alike --
// a finding hidden at the bottom of the list is the failure this returns ""
// to avoid.
func TestAnUnrecognisedSeverityBandGetsNoTier(t *testing.T) {
	bands := []string{"", "critical", "catastrophic", "HIGH", "info"}
	confidences := []string{
		gomodels.ConfidenceBandHigh,
		gomodels.ConfidenceBandMedium,
		gomodels.ConfidenceBandLow,
		gomodels.ConfidenceBandUnverifiable,
		gomodels.ConfidenceBandNotConfirmed,
		"",
	}
	for _, sb := range bands {
		for _, cb := range confidences {
			if got := priorityTier(sb, cb); got != "" {
				t.Errorf("priorityTier(%q, %q) = %q, want \"\"", sb, cb, got)
			}
		}
	}
}

// TestPriorityTierForMirrorsTheMatrix -- the exported wrapper the API's
// calibration endpoint enumerates the matrix through. It must agree with the
// engine's own answer for every cell, because the whole point of serving the
// matrix by asking the engine is that there is one matrix.
func TestPriorityTierForMirrorsTheMatrix(t *testing.T) {
	severities := []string{
		gomodels.SeverityBandHigh,
		gomodels.SeverityBandMedium,
		gomodels.SeverityBandLow,
		"nonsense",
	}
	confidences := []string{
		gomodels.ConfidenceBandHigh,
		gomodels.ConfidenceBandMedium,
		gomodels.ConfidenceBandLow,
		gomodels.ConfidenceBandUnverifiable,
		gomodels.ConfidenceBandNotConfirmed,
	}
	cells := 0
	for _, sb := range severities {
		for _, cb := range confidences {
			if got, want := PriorityTierFor(sb, cb), priorityTier(sb, cb); got != want {
				t.Errorf("PriorityTierFor(%q, %q) = %q, want %q", sb, cb, got, want)
			}
			cells++
		}
	}
	// A guard against the wrapper being accidentally narrowed to the three
	// real bands: the API asks it about whatever a document carries.
	if cells != 20 {
		t.Errorf("checked %d cells, want 20", cells)
	}
	if got := PriorityTierFor(gomodels.SeverityBandHigh, gomodels.ConfidenceBandUnverifiable); got != gomodels.PriorityTierInvestigate {
		t.Errorf("severe-but-unchecked = %q, want %q", got, gomodels.PriorityTierInvestigate)
	}
}

// TestWithPriorityWritesNeitherFieldWithoutATier -- when the matrix has no
// cell, both priority fields stay unset rather than one being written.
//
// A written tier with a zero score would sort as a real bottom-of-the-list
// grade; a written score with no tier would render as a dash next to a number.
// Neither is "ungraded", which is what this document is.
func TestWithPriorityWritesNeitherFieldWithoutATier(t *testing.T) {
	in := gomodels.Scoring{
		SeverityBand:    "not-a-band",
		SeverityScore:   1.0,
		ConfidenceBand:  gomodels.ConfidenceBandHigh,
		ConfidenceScore: 0.95,
	}
	got := withPriority(in, DefaultConfig())
	if got.PriorityTier != "" {
		t.Errorf("PriorityTier = %q, want \"\"", got.PriorityTier)
	}
	if got.PriorityScore != 0 {
		t.Errorf("PriorityScore = %v, want 0", got.PriorityScore)
	}
	// The rest of the document is untouched: this guard declines to grade,
	// it does not clear what another axis already decided.
	if got.SeverityScore != 1.0 || got.ConfidenceScore != 0.95 {
		t.Errorf("the guard altered another axis: %+v", got)
	}
}

// TestLargerRanksByAbsoluteValue -- the magnitude ladder ranks on absolute
// value, so a decline of -40% outranks a rise of 12%.
//
// The incumbent is kept on a tie deliberately: the winner is then the order
// the model declared the figures in, which is stable across re-scores of the
// same document. An unstable pick would change a stored driver's identity
// every time validation re-ran.
func TestLargerRanksByAbsoluteValue(t *testing.T) {
	fig := func(id string, v float64) *gomodels.SeverityFigure {
		return &gomodels.SeverityFigure{FigureID: id, Value: v}
	}
	cases := []struct {
		name      string
		cur, next *gomodels.SeverityFigure
		wantID    string
	}{
		{"nil incumbent takes the candidate", nil, fig("b", 3), "b"},
		{"nil candidate keeps the incumbent", fig("a", 3), nil, "a"},
		{"a bigger candidate wins", fig("a", 3), fig("b", 9), "b"},
		{"a smaller candidate loses", fig("a", 9), fig("b", 3), "a"},
		{"a bigger negative candidate wins", fig("a", 12), fig("b", -40), "b"},
		{"a smaller negative candidate loses", fig("a", -40), fig("b", 12), "a"},
		{"an equal candidate keeps the incumbent", fig("a", 5), fig("b", 5), "a"},
		{"a sign-flipped equal keeps the incumbent", fig("a", 5), fig("b", -5), "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := larger(c.cur, c.next)
			if got == nil {
				t.Fatalf("larger() = nil, want %s", c.wantID)
			}
			if got.FigureID != c.wantID {
				t.Errorf("larger() = %s, want %s", got.FigureID, c.wantID)
			}
		})
	}
	if larger(nil, nil) != nil {
		t.Error("larger(nil, nil) should be nil")
	}
}

// TestSeverityReasonNamesTheAmountTheBaselineCouldNotRank -- the shipped
// configuration has no amount bands, so a document whose only checked figure
// is a currency amount lands on the area baseline. The amount is real and is
// named; the sentence says plainly that it did not set the band.
//
// This is the sentence behind the dashboard's "not quantified" marker, and the
// case a reader would otherwise read as a bug: a grounded sum on screen beside
// a band it did not produce.
func TestSeverityReasonNamesTheAmountTheBaselineCouldNotRank(t *testing.T) {
	got := severityReason(gomodels.SeverityBandMedium, gomodels.SeverityDriver{
		Kind:       gomodels.SeverityDriverBaseline,
		Quantified: false,
		Amount: &gomodels.SeverityFigure{
			FigureID: "revenue_at_risk", Display: "$2.40M", Value: 2400000,
			Unit: gomodels.UnitCurrency, Currency: "$", Verdict: "holds",
		},
	})
	for _, want := range []string{"Medium", "not quantified", "$2.40M", "no amount bands are configured"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason = %q, missing %q", got, want)
		}
	}

	// With no amount either, the sentence says the finding states no figure
	// at all -- a different fact, and not one to dress up with a number.
	bare := severityReason(gomodels.SeverityBandMedium, gomodels.SeverityDriver{
		Kind: gomodels.SeverityDriverBaseline,
	})
	if strings.Contains(bare, "at stake") {
		t.Errorf("reason = %q, should not claim an amount", bare)
	}
	if !strings.Contains(bare, "states no checked figure") {
		t.Errorf("reason = %q, want it to say no figure was stated", bare)
	}
}

// TestSeverityReasonStillOpensWithAWordForAnUnknownBand -- a calibration that
// adds a band without naming it still produces a sentence.
//
// A band with no explanation beside it is the one shape this layer must not
// emit: the dashboard renders the stored reason verbatim, so an empty string
// here is an empty row on screen.
func TestSeverityReasonStillOpensWithAWordForAnUnknownBand(t *testing.T) {
	got := severityReason("severe-ish", gomodels.SeverityDriver{
		Kind:       gomodels.SeverityDriverShare,
		Quantified: true,
		Figure:     &gomodels.SeverityFigure{Display: "41.2%", Value: 41.2, Unit: gomodels.UnitPercent, Verdict: "holds"},
	})
	if got == "" {
		t.Fatal("reason is empty for an unrecognised band")
	}
	if !strings.HasPrefix(got, "Severity") {
		t.Errorf("reason = %q, want it to open with the generic label", got)
	}
	if !strings.Contains(got, "41.2%") {
		t.Errorf("reason = %q, should still name the figure", got)
	}
}

// TestPriorityTierRankOrdersEveryTierAndSinksTheUnknown -- the ordering the
// notification's tier counts and the top-insight briefs sort by.
//
// Exported so those call sites read the engine's ordering rather than keeping
// a second copy, which is how the dashboard's rank tables once came to
// disagree with the ledger's own. An unrecognised tier ranks 0, below every
// real one: a tier this build has no word for must not sort above a tier it
// does.
func TestPriorityTierRankOrdersEveryTierAndSinksTheUnknown(t *testing.T) {
	ranks := map[string]int{
		gomodels.PriorityTierCritical:    PriorityTierRank(gomodels.PriorityTierCritical),
		gomodels.PriorityTierInvestigate: PriorityTierRank(gomodels.PriorityTierInvestigate),
		gomodels.PriorityTierHigh:        PriorityTierRank(gomodels.PriorityTierHigh),
		gomodels.PriorityTierLow:         PriorityTierRank(gomodels.PriorityTierLow),
	}
	descending := []string{
		gomodels.PriorityTierCritical,
		gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh,
		gomodels.PriorityTierLow,
	}
	for i := 1; i < len(descending); i++ {
		if ranks[descending[i-1]] <= ranks[descending[i]] {
			t.Errorf("%s (%d) must outrank %s (%d)",
				descending[i-1], ranks[descending[i-1]], descending[i], ranks[descending[i]])
		}
	}
	// Investigate above High is the deliberate inversion the matrix exists to
	// express; a test that only checked "descending" would not catch it being
	// quietly swapped back.
	if ranks[gomodels.PriorityTierInvestigate] <= ranks[gomodels.PriorityTierHigh] {
		t.Error("investigate must outrank high")
	}
	for _, unknown := range []string{"", "urgent", "CRITICAL"} {
		if got := PriorityTierRank(unknown); got != 0 {
			t.Errorf("PriorityTierRank(%q) = %d, want 0", unknown, got)
		}
		if got := PriorityTierRank(unknown); got >= ranks[gomodels.PriorityTierLow] {
			t.Errorf("PriorityTierRank(%q) = %d, should sort below low (%d)", unknown, got, ranks[gomodels.PriorityTierLow])
		}
	}
}

// TestFigureWordsAlwaysRendersSomething -- the driver figure's prose.
//
// Every arm matters because this string is concatenated into the stored
// severity reason: an empty return would leave a sentence with a hole in it
// ("High — driven by , checked against warehouse rows"), which is worse than
// naming the figure by its reference.
func TestFigureWordsAlwaysRendersSomething(t *testing.T) {
	cases := []struct {
		name string
		in   *gomodels.SeverityFigure
		want string
	}{
		{"nil figure", nil, "an unnamed figure"},
		{"a rendered number wins", &gomodels.SeverityFigure{Display: "41.2%", FigureID: "pct"}, "41.2%"},
		{"the reference when nothing was rendered", &gomodels.SeverityFigure{FigureID: "pct"}, "figure pct"},
		{"neither", &gomodels.SeverityFigure{}, "an unnamed figure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := figureWords(c.in); got != c.want {
				t.Errorf("figureWords() = %q, want %q", got, c.want)
			}
		})
	}
	// The sentence it feeds never ends up with a hole in it.
	reason := severityReason(gomodels.SeverityBandHigh, gomodels.SeverityDriver{
		Kind:       gomodels.SeverityDriverShare,
		Quantified: true,
		Figure:     &gomodels.SeverityFigure{FigureID: "pct"},
	})
	if strings.Contains(reason, "by ,") || strings.Contains(reason, "  ") {
		t.Errorf("reason has a hole in it: %q", reason)
	}
}
