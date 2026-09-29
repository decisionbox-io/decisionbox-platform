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
	digits := strconv.FormatFloat(scaled, 'f', f.Places(), 64)

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
		b.WriteString(f.Symbol())
	}
	// Thousands separators on anything unscaled, because an unscaled count or amount
	// is where they matter and a scaled one has at most four digits.
	if f.Scale == models.ScaleNone {
		digits = groupThousands(digits)
	}
	b.WriteString(digits)
	b.WriteString(sc.suffix)

	b.WriteString(unitSuffix(f.Unit))
	return b.String()
}

// unitSuffix is what a unit appends after the digits.
func unitSuffix(unit string) string {
	switch unit {
	case models.UnitPercent:
		return "%"
	case models.UnitMultiple:
		return "x"
	}
	return ""
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
	for i := 0; i < f.Places(); i++ {
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

// renderedValue is the number the prose actually carries, as a number.
//
// Not the same as Value whenever Value holds more precision than the format prints, and
// that gap is what the check has to be about. A figure of 100,490,000 at the millions scale
// with no decimals PRINTS "$100M". Evidence of 100,510,000 sits 20,000 from the value and
// 510,000 from what was printed, so measuring from the value called it holding while the
// sentence said $100M and the evidence said $101M.
//
// Derived by rendering and reading back, deliberately: it is the one place where parsing a
// number is not a guess, because Go wrote the string from a float a line earlier and the
// alternative is a second rounding implementation that can disagree with the first.
func renderedValue(f models.Figure) float64 {
	sc, ok := figureScales[f.Scale]
	if !ok {
		sc = figureScales[models.ScaleNone]
	}
	printed, err := strconv.ParseFloat(strconv.FormatFloat(f.Value/sc.div, 'f', f.Places(), 64), 64)
	if err != nil {
		return f.Value
	}
	return printed * sc.div
}

// figureAgreement is what the comparison is able to establish, which is three answers and
// not two.
//
// Two was the mistake that took six review rounds to surface. `holds` or `fails` forces the
// arithmetic's own uncertainty to be folded into one of them: fold it into the interval and
// a figure is certified whenever the two merely OVERLAP -- a total of 0.25 known to within
// 0.4 certified a claim of 1, because [-0.15, 0.65] reaches into [0.5, 1.5]. Leave it out
// and a figure equal to its evidence is refuted for a gap of 1e-14. Neither is a tolerance
// problem; both are what happens when an uncertain measurement is forced to a verdict.
type figureAgreement int

const (
	// figureAgrees -- every value the arithmetic could have produced is inside the interval
	// the figure printed. Agreement is established.
	figureAgrees figureAgreement = iota
	// figureDisagrees -- none of them is. Disagreement is established, wherever inside its
	// uncertainty the true answer sits.
	figureDisagrees
	// figureUnresolved -- some are and some are not, so the arithmetic cannot settle this
	// figure at the precision it claims. Never a verdict.
	figureUnresolved
)

// compareFigure asks what the evidence establishes about one figure.
//
// evalSlack is the bound evalFigure derived on its own arithmetic, so [got-evalSlack,
// got+evalSlack] is where the true answer lies. The figure claims [rendered-claim,
// rendered+claim], where claim is half the last place it printed, widened by the cost of
// printing and reading it back.
//
// Containment, not overlap. That is the whole of it, and it replaces every tuned constant
// the previous rounds added: there is nothing left to size, because both intervals are
// derived -- one from the precision the figure chose, one from the arithmetic it declared.
func compareFigure(f models.Figure, got, evalSlack float64) figureAgreement {
	rendered := renderedValue(f)
	if math.IsNaN(rendered) || math.IsNaN(got) || math.IsNaN(evalSlack) ||
		math.IsInf(rendered, 0) || math.IsInf(got, 0) || math.IsInf(evalSlack, 0) {
		// Nothing is established by arithmetic that produced no number. Unresolved rather
		// than disagreeing, because reporting the evaluator's own limit as the document's
		// error is the failure this layer exists to avoid.
		return figureUnresolved
	}
	claim := figureSlack(f) + printingSlack(rendered, got)
	gap := math.Abs(rendered - got)
	evalSlack = math.Abs(evalSlack)
	switch {
	case gap+evalSlack <= claim:
		return figureAgrees
	case gap-evalSlack > claim:
		return figureDisagrees
	default:
		return figureUnresolved
	}
}

// printingSlack is what the figure's own interval must be widened by to account for having
// been printed and read back.
//
// renderedValue formats the value and parses the result, which costs about half the gap
// between representable float64 values at that magnitude. An unscaled cell of
// 100000000.0000045 written to six decimals prints "100000000.000005" and reads back
// 5.0664e-7 from the number it came from, against an interval of 5e-7 -- so a figure exactly
// equal to its evidence missed by the cost of printing it. Six decimals at 1e8 is sixteen
// significant digits, past what a float64 holds; the figure is asking for precision the
// machine does not have, and allowing it is the honest answer.
//
// Part of the figure's interval rather than the arithmetic's uncertainty, because that is
// where it comes from: it is doubt about what the figure claims, not about what the evidence
// is.
func printingSlack(rendered, got float64) float64 {
	return 4 * floatSpacing(math.Max(math.Abs(rendered), math.Abs(got)))
}

// closeEnough reports whether agreement is established, which is the common question and
// the only one the render path asks.
func closeEnough(f models.Figure, got, evalSlack float64) bool {
	return compareFigure(f, got, evalSlack) == figureAgrees
}

// floatSpacing is the gap between representable float64 values next to m -- one unit in the
// last place. Two numbers closer together than this are one number as far as the hardware is
// concerned, so no check in this layer can distinguish them.
func floatSpacing(m float64) float64 {
	m = math.Abs(m)
	if m == 0 || math.IsInf(m, 0) || math.IsNaN(m) {
		return 0
	}
	// Measured upwards, except at the very top of the range, where the next value up IS
	// infinity -- which made the spacing infinite and every comparison hold, so a figure
	// claiming zero was certified against evidence of MaxFloat64. Downwards there is always
	// a finite neighbour, and one place below is the same width as one place above except
	// exactly at a power of two, where it is half. Half a place is the right answer at the
	// one magnitude that has no place above it.
	if up := math.Nextafter(m, math.Inf(1)); !math.IsInf(up, 0) {
		return up - m
	}
	return m - math.Nextafter(m, 0)
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
func percentScalings(f models.Figure) []float64 {
	if f.Unit != models.UnitPercent {
		return []float64{1}
	}
	// An excess has no such ambiguity, and offering it one puts a number in the verdict
	// that no arithmetic over the rows produces.
	//
	// The ambiguity is about a COLUMN: a share column is stored either as a fraction or as
	// a percentage, and the figure cannot see which. An excess is a quotient of two cells
	// of one column, so the convention cancels, and evalFigure has already applied the only
	// factor of a hundred there is. Left in, the readings of a true 9.59 included 958.78,
	// which is the one nearest a figure stating 109.59 -- so the verdict on the falsehood
	// this kind was added for reported a value from nowhere.
	//
	// `ratio` is dimensionless in the same way and is deliberately NOT changed here. Its
	// readings are what three corpora of verdicts were measured against, and narrowing them
	// moves settled behaviour rather than defining a new kind. Worth doing on its own
	// evidence; not worth folding into this.
	if f.Kind == models.FigureExcess {
		return []float64{1}
	}
	return []float64{1, 100, 0.01}
}

// relativeDistance is the same measure relativeGap reports, as a number.
//
// Used to pick which of a percentage's candidate readings a refutation should be measured
// against, so the correction gate compares two readings of one quantity rather than a
// percentage against a fraction.
func relativeDistance(claimed, got float64) float64 {
	scale := math.Max(math.Abs(claimed), math.Abs(got))
	if scale == 0 {
		return 0
	}
	return math.Abs(claimed-got) / scale
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
