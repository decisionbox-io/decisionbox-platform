package discovery

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Rendering a figure, and the interval that follows from having rendered it.
//
// This file is why no number in this layer is recovered by parsing text. The model
// supplies a value and how to write it; Go writes it. Because Go chose the last
// printed place, Go knows the interval the figure claims -- exactly, as arithmetic over
// the scale and the decimal count, rather than by reading digits back out of a string.
//
// The earlier design did read them back, and the failures were all of one shape: the
// first numeral in a phrase is not necessarily the quantity being checked. "5.0% of
// $34.86B" claimed an interval of ±0.05 around a value of 34.86 billion; "1997 Q4
// $8.61B" took its precision from a year. Both produced false refutations, and one went
// on to rewrite the wrong numeral and report the result as holding. None of that is
// reachable from here, because there is no string to misread.

// figureScales maps a scale name to the divisor and the suffix it prints.
var figureScales = map[string]struct {
	div    float64
	suffix string
}{
	models.ScaleNone:      {1, ""},
	models.ScaleThousands: {1e3, "K"},
	models.ScaleMillions:  {1e6, "M"},
	models.ScaleBillions:  {1e9, "B"},
}

// renderFigure writes a figure as the prose will carry it.
//
// An unknown unit or scale renders plainly rather than failing: a figure whose
// presentation Go does not recognise is still a figure whose arithmetic can be
// checked, and refusing to render it would lose the finding over a formatting word.
func renderFigure(f models.Figure) string {
	sc, ok := figureScales[f.Scale]
	if !ok {
		sc = figureScales[models.ScaleNone]
	}
	scaled := f.Value / sc.div
	digits := strconv.FormatFloat(scaled, 'f', f.Decimals, 64)

	var b strings.Builder
	if f.Approx {
		b.WriteString("~")
	}
	if f.Unit == models.UnitCurrency {
		if scaled < 0 {
			// The sign belongs outside the currency symbol: -$1.2M, not $-1.2M.
			b.WriteString("-")
			digits = strings.TrimPrefix(digits, "-")
		}
		b.WriteString("$")
	}
	// Thousands separators on anything unscaled, because an unscaled count or amount
	// is where they matter and a scaled one has at most four digits.
	if f.Scale == models.ScaleNone {
		digits = groupThousands(digits)
	}
	b.WriteString(digits)
	b.WriteString(sc.suffix)

	switch f.Unit {
	case models.UnitPercent:
		b.WriteString("%")
	case models.UnitMultiple:
		b.WriteString("x")
	case models.UnitDays:
		b.WriteString(" days")
	}
	return b.String()
}

// figureSlack is the half-width of the interval a rendered figure claims.
//
// Derived from the format Go printed with, not from the text it printed. "$6.645B" is
// three decimals at the billions scale, so it claims ±500,000; "100,000" is zero
// decimals unscaled, so it claims ±0.5. That difference -- 0.005% against 0.004% -- is
// what no relative tolerance can separate and what stated precision separates exactly,
// measured in both directions across three corpora.
func figureSlack(f models.Figure) float64 {
	sc, ok := figureScales[f.Scale]
	if !ok {
		sc = figureScales[models.ScaleNone]
	}
	place := sc.div
	for i := 0; i < f.Decimals; i++ {
		place /= 10
	}
	return place / 2
}

// groupThousands inserts comma separators into an already-rendered decimal string.
func groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	if neg {
		b.WriteString("-")
	}
	for i := 0; i < len(intPart); i++ {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(intPart[i])
	}
	return b.String() + frac
}

// closeEnough compares a figure's value against an evaluated one at the interval the
// rendering claims.
func closeEnough(f models.Figure, got float64) bool {
	if math.IsNaN(f.Value) || math.IsNaN(got) || math.IsInf(f.Value, 0) || math.IsInf(got, 0) {
		return false
	}
	return math.Abs(f.Value-got) <= figureSlack(f)
}

// percentScalings returns the evaluated values a percentage figure may legitimately be
// compared against.
//
// A warehouse stores a share either as a fraction or as a percentage, and which one is
// a property of the query the model wrote rather than of the figure's presentation. One
// replay produced three refutations from that alone: three cells of a share column
// evaluated as 0.1414 against a figure written 14.1%.
//
// The cost is stated: an error that is exactly a factor of one hundred cannot be
// caught. That is rarer than the storage convention the model cannot see, and the
// alternative -- scaling on the model's word -- refutes a correct figure whose column
// is already in percent, which is the same defect mirrored.
func percentScalings(f models.Figure, got float64) []float64 {
	if f.Unit != models.UnitPercent {
		return []float64{got}
	}
	return []float64{got, got * 100, got / 100}
}

func relativeGap(claimed, got float64) string {
	scale := math.Max(math.Abs(claimed), math.Abs(got))
	if scale == 0 {
		return "both zero"
	}
	return fmt.Sprintf("off by %.2f%%", 100*math.Abs(claimed-got)/scale)
}

func formatFigure(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.4g", v)
}
