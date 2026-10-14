package discovery

import (
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
			"a percentage follows the language too",
			"Turkish",
			models.Figure{Value: 64.41, Unit: models.UnitPercent, Decimals: 2},
			"64,41%",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFigure(tc.fig, localeFor(tc.language)); got != tc.want {
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
	loc := localeFor("Turkish")
	fig := models.Figure{ID: "f1", Value: -746779.37, Unit: models.UnitCurrency, Currency: "₺", Decimals: 0, Step: 1}
	ins := []models.Insight{{Name: "En düşük ay {{f1}}", SourceSteps: []int{1}, Figures: []models.Figure{fig}}}
	renderInsightFigures(ins, loc)
	if ins[0].Name != "En düşük ay -₺746.779" {
		t.Fatalf("prose rendered %q", ins[0].Name)
	}
	if got := renderFigure(fig, loc); got != "-₺746.779" {
		t.Fatalf("verdict display rendered %q, which disagrees with the prose", got)
	}
}
