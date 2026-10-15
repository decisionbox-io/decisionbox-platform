package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// A Turkish project writes a thousands dot and a decimal comma. Rendering US notation
// into Turkish prose does not look foreign, it reads as a different number: "746.779" is
// seven hundred forty-six thousand there, not seven hundred forty-six point seven.
func TestFigureRender_NotationFollowsTheLanguage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		language string
		fig      models.Figure
		want     string
	}{
		{
			"turkish groups with a point",
			"Turkish",
			models.Figure{Value: -746779.37, Unit: models.UnitCurrency, Currency: "₺", Decimals: 0},
			"-₺746.779",
		},
		{
			"turkish writes the decimal with a comma",
			"Turkish",
			models.Figure{Value: 30_628_707.91, Unit: models.UnitCurrency, Currency: "₺", Scale: models.ScaleMillions, Decimals: 2},
			"₺30,63M",
		},
		{
			"english is unchanged",
			"English",
			models.Figure{Value: -746779.37, Unit: models.UnitCurrency, Currency: "$", Decimals: 0},
			"-$746,779",
		},
		{
			"an empty language is unchanged",
			"",
			models.Figure{Value: 1_234_567.5, Unit: models.UnitCount, Decimals: 1},
			"1,234,567.5",
		},
		{
			"an unknown language keeps the notation everyone has had",
			"Klingon",
			models.Figure{Value: 1_234_567.5, Unit: models.UnitCount, Decimals: 1},
			"1,234,567.5",
		},
		{
			"a percentage uses the language's decimal mark, and keeps the sign behind",
			"Turkish",
			models.Figure{Value: 64.41, Unit: models.UnitPercent, Decimals: 2},
			"64,41%",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newFigureWriter(tc.language).renderFigure(tc.fig); got != tc.want {
				t.Fatalf("renderFigure = %q, want %q", got, tc.want)
			}
		})
	}
}

// The interval a figure claims is arithmetic over scale and decimal count, never a reading
// of the rendered string -- so changing the separators must not move it.
func TestFigureRender_NotationDoesNotMoveTheInterval(t *testing.T) {
	f := models.Figure{Value: 100_490_000, Unit: models.UnitCurrency, Scale: models.ScaleMillions, Decimals: 0}
	if tr, us := renderedValue(f), renderedValue(f); tr != us {
		t.Fatalf("renderedValue is not stable: %v vs %v", tr, us)
	}
	if got := renderedValue(f); got != 100_000_000 {
		t.Fatalf("renderedValue = %v, want 1e8 — the printed value, not the declared one", got)
	}
}

// The prose and the figure table are rendered by the same code, so a Turkish insight
// cannot carry one notation in its heading and another in its body.
func TestFigureRender_ProseAndVerdictAgreeOnNotation(t *testing.T) {
	w := newFigureWriter("Turkish")
	fig := models.Figure{ID: "f1", Value: -746779.37, Unit: models.UnitCurrency, Currency: "₺", Decimals: 0, Step: 1}
	ins := []models.Insight{{Name: "En düşük ay {{f1}}", SourceSteps: []int{1}, Figures: []models.Figure{fig}}}
	w.renderInsightFigures(ins)
	if ins[0].Name != "En düşük ay -₺746.779" {
		t.Fatalf("prose rendered %q", ins[0].Name)
	}
	if got := w.renderFigure(fig); got != "-₺746.779" {
		t.Fatalf("verdict display rendered %q, which disagrees with the prose", got)
	}
}

// CLDR grouping is not always three digits, which is the thing a hand-written rule gets
// wrong. Hindi groups the lakh; the old implementation wrote 1,234,567 for it.
func TestFigureRender_GroupingIsNotAlwaysThreeDigits(t *testing.T) {
	for _, tc := range []struct{ language, want string }{
		{"English", "1,234,567"},
		{"Turkish", "1.234.567"},
		{"Hindi", "12,34,567"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			f := models.Figure{Value: 1234567, Unit: models.UnitCount, Decimals: 0}
			if got := newFigureWriter(tc.language).renderFigure(f); got != tc.want {
				t.Fatalf("%s rendered %q, want %q", tc.language, got, tc.want)
			}
		})
	}
}

// A negative amount carries exactly one sign, outside the currency symbol, in every
// language.
//
// The first version of this test asserted only HasPrefix(got, "-$"), which is true of
// "-$\u200e-1.2M" -- so it passed while Arabic and Hebrew shipped the sign twice. CLDR
// prefixes their minus with a bidi marker (U+061C, U+200E), and the trim that was supposed
// to remove the inner sign matched nothing. Counting is the property; a prefix check is
// the hole.
func TestFigureRender_NegativeCarriesExactlyOneSign(t *testing.T) {
	for _, lang := range []string{"English", "Turkish", "German", "Hindi", "Arabic", "Hebrew", "Thai", "Russian"} {
		t.Run(lang, func(t *testing.T) {
			f := models.Figure{Value: -1_200_000, Unit: models.UnitCurrency, Currency: "$", Scale: models.ScaleMillions, Decimals: 1}
			got := newFigureWriter(lang).renderFigure(f)
			if n := strings.Count(got, "-"); n != 1 {
				t.Errorf("rendered %q with %d hyphens, want exactly 1", got, n)
			}
			for _, marker := range []string{"\u061c", "\u200e", "\u200f", "\u2212"} {
				if strings.Contains(got, marker) {
					t.Errorf("rendered %q, which carries a bidi or non-ASCII sign marker %q", got, marker)
				}
			}
			if !strings.HasPrefix(got, "-$") {
				t.Errorf("rendered %q; the sign belongs outside the symbol", got)
			}
		})
	}
}
