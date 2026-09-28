package discovery

import (
	"fmt"
	"math"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Figure checking. The model declares which step and which arithmetic produced
// each number it wrote; Go runs the arithmetic over that step's full rows and
// compares.
//
// Why this direction and not the obvious one is recorded on models.FigureClaim:
// three variants of "Go finds the number in the rows" were measured against
// three hand-adjudicated corpora and the best of them ran at 23% precision,
// because most numerals in a sound insight are two or three operations from the
// rows and that space contains almost anything. Declaring is mechanical and
// arithmetic is mechanical; only the search between them was not.
//
// Full rows, not the digest. The model wrote its figure from a compacted result
// that may have shown twenty of a hundred and fifty rows; the check runs over
// all of them. That asymmetry is the point -- it is the same reason
// attachQuantifierVerdicts evaluates over step.QueryResult.
//
// Nothing here rejects anything. The verdicts attach to the insight and
// repairRefutedInsights is the only thing that acts on them, exactly as for
// quantifier claims, and for the reason stated there: until there is somewhere
// for a rejected insight to go, a gate turns one wrong number into a discarded
// document.

// How close the arithmetic must come to the written figure is read from the
// figure's own stated precision, not from a fixed band.
//
// A relative band cannot work, and the red-proof is what showed it. Run 3 wrote
// "100,000" where the rows give 99,996 -- 0.004% apart, and false -- while the
// same documents wrote "$6.645B" for 6,645,321,130, which is 0.005% apart and
// true. Those overlap. Any single relative tolerance either passes the corruption
// or refutes the rounding, and the first version of this file passed both
// corruptions.
//
// The separator is how many places the prose actually wrote. "$6.645B" states
// four significant figures and so claims the interval [6.6445B, 6.6455B], which
// contains the cell. "100,000" states its last place in the units and so claims
// [99,999.5, 100,000.5], which does not contain 99,996. Stated precision settles
// every case measured across the three corpora, in both directions:
//
//	written     truth           stated interval          verdict
//	100,000     99,996          +/- 0.5                  fails
//	150,004     150,000         +/- 0.5                  fails
//	49.7%       49.343%         +/- 0.05                 fails
//	47.1%       49.343%         +/- 0.05                 fails
//	$6.645B     6,645,321,130   +/- 500,000              holds
//	~911K       911,395         +/- 500                  holds
//	20.1%       20.066%         +/- 0.05                 holds
//	49.4%       49.358%         +/- 0.05                 holds
//
// This is the rule the experiment's protocol pre-registered, restored after the
// first draft of this file replaced it with a constant.
//
// Deliberately no widening for an approximation marker. The scoring harness
// widens "~100,000" to 1% because over-counting invented figures there costs
// precision in a metric. Here it would hand the model a way to make any figure
// unrefutable by prefixing a tilde, and none of the observed corruptions carried
// a marker anyway.

// figureSlack is the half-width of the interval a written figure claims.
//
// Derived from the text, because Value alone cannot say it: 100000 and 1e5 are the
// same float and "100,000" and "0.1M" claim intervals a hundred thousand times
// apart. Falls back to a loose relative band when the string carries no numeral it
// can read, which errs toward holds -- the direction every unreadable case in this
// layer errs in.
func figureSlack(figure string, value float64) float64 {
	place, ok := statedPlace(figure)
	if !ok {
		return math.Max(math.Abs(value)*0.005, 0.005)
	}
	return place / 2
}

// statedPlace is the size of the last place the figure string writes: 1 for
// "100,000", 0.1 for "49.7%", 1e6 for "$6.645B", 1000 for "911K".
func statedPlace(figure string) (float64, bool) {
	m := reNumeral.FindStringSubmatchIndex(figure)
	if m == nil {
		return 0, false
	}
	digits := group(figure, m, 3)
	if digits == "" {
		return 0, false
	}
	suffix := strings.ToLower(strings.TrimSpace(strings.Trim(group(figure, m, 4), "^$")))
	suffix = strings.TrimRight(suffix, ".,;:)")

	place := 1.0
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		for range digits[i+1:] {
			place /= 10
		}
	}
	if scale, ok := numeralScale[suffix]; ok {
		place *= scale
	}
	return place, true
}

// EvaluateFigureClaims settles every declared figure against the steps it
// cites, in declaration order.
func EvaluateFigureClaims(claims []models.FigureClaim, steps map[int]StepRows) []models.FigureVerdict {
	out := make([]models.FigureVerdict, 0, len(claims))
	for _, c := range claims {
		out = append(out, evaluateFigureClaim(c, steps))
	}
	return out
}

func evaluateFigureClaim(c models.FigureClaim, steps map[int]StepRows) models.FigureVerdict {
	v := models.FigureVerdict{Figure: c.Figure, Step: c.Step, Kind: c.Kind, Claimed: c.Value}
	undecidable := func(format string, args ...any) models.FigureVerdict {
		v.Status = models.FigureUndecidable
		v.Reason = fmt.Sprintf(format, args...)
		return v
	}

	ev, ok := steps[c.Step]
	if !ok {
		return undecidable("step %d is not among this insight's evidence", c.Step)
	}
	if len(ev.Rows) == 0 {
		return undecidable("step %d returned no rows", c.Step)
	}

	got, err := evalFigure(c, ev.Rows)
	if err != nil {
		return undecidable("%s", err.Error())
	}
	v.Evaluated = got

	if closeEnough(c.Figure, c.Value, got) {
		v.Status = models.FigureHolds
		return v
	}
	v.Status = models.FigureFails
	v.Reason = fmt.Sprintf("%s over step %d gives %s, and the text writes %s (%s)",
		c.Kind, c.Step, formatFigure(got), c.Figure, relativeGap(c.Value, got))
	return v
}

// closeEnough compares a written figure against an evaluated one at the interval
// the figure's own text claims.
func closeEnough(figure string, claimed, got float64) bool {
	if math.IsNaN(claimed) || math.IsNaN(got) || math.IsInf(claimed, 0) || math.IsInf(got, 0) {
		return false
	}
	return math.Abs(claimed-got) <= figureSlack(figure, claimed)
}

func relativeGap(claimed, got float64) string {
	scale := math.Max(math.Abs(claimed), math.Abs(got))
	if scale == 0 {
		return "both zero"
	}
	return fmt.Sprintf("off by %.2f%%", 100*math.Abs(claimed-got)/scale)
}

func formatFigure(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%.4g", f)
}

// evalFigure runs one declaration's arithmetic, or reports what it could not
// read. Every path that cannot compute returns an error, which the caller turns
// into undecidable -- never into a refutation.
func evalFigure(c models.FigureClaim, rows []map[string]any) (float64, error) {
	switch c.Kind {
	case models.FigureCell:
		return oneCell(rows, c.Column, c.Row, "row")
	case models.FigureSum:
		scoped, err := scopeFigureRows(rows, c.Scope)
		if err != nil {
			return 0, err
		}
		return columnTotal(scoped, c.Column)
	case models.FigureCount:
		scoped, err := scopeFigureRows(rows, c.Scope)
		if err != nil {
			return 0, err
		}
		return float64(len(scoped)), nil
	case models.FigureRatio:
		num, err := oneCell(rows, c.Column, c.Row, "row")
		if err != nil {
			return 0, err
		}
		scoped, err := scopeFigureRows(rows, c.Scope)
		if err != nil {
			return 0, err
		}
		den, err := columnTotal(scoped, c.Column)
		if err != nil {
			return 0, err
		}
		if den == 0 {
			return 0, fmt.Errorf("the total of %q over the scoped rows is zero, so a ratio is undefined", c.Column)
		}
		q := num / den
		if c.Pct {
			q *= 100
		}
		return q, nil
	case models.FigureDiff:
		left, err := oneCell(rows, c.Column, c.Row, "row")
		if err != nil {
			return 0, err
		}
		right, err := oneCell(rows, c.Column, c.Other, "other")
		if err != nil {
			return 0, err
		}
		return left - right, nil
	case "":
		return 0, fmt.Errorf("the declaration names no kind")
	default:
		return 0, fmt.Errorf("kind %q is not one of cell, sum, count, ratio, diff", c.Kind)
	}
}

// oneCell resolves a single-row selector to one numeric cell.
//
// A selector matching several rows is undecidable rather than a refutation, and
// so is one matching none. Both mean the declaration did not identify a cell,
// which is a statement about the declaration and not about the figure -- the
// distinction quantifier.go draws between Undecidable and Fails, for the same
// reason. Silently taking the first of several matches would turn an ambiguous
// declaration into a confident wrong answer.
func oneCell(rows []map[string]any, column, selector, field string) (float64, error) {
	if strings.TrimSpace(column) == "" {
		return 0, fmt.Errorf("the declaration names no column")
	}
	matched := rows
	if strings.TrimSpace(selector) != "" {
		var err error
		if matched, err = filterRows(rows, selector); err != nil {
			return 0, err
		}
	}
	switch len(matched) {
	case 1:
		// fall through
	case 0:
		return 0, fmt.Errorf("%s %q selects none of the step's %d rows", field, selector, len(rows))
	default:
		if strings.TrimSpace(selector) == "" {
			return 0, fmt.Errorf("the step returned %d rows and no %s selector says which one", len(rows), field)
		}
		return 0, fmt.Errorf("%s %q selects %d rows, so it does not name one cell", field, selector, len(matched))
	}
	raw, present := matched[0][column]
	if !present {
		return 0, fmt.Errorf("the step's rows do not carry a column %q", column)
	}
	f, ok := asFloat(raw)
	if !ok {
		return 0, fmt.Errorf("column %q holds %v, which is not a number", column, raw)
	}
	return f, nil
}

// columnTotal sums a column over the rows given. A row missing the column is an
// error rather than a zero: a total silently short by one row reads as the model
// having written the wrong number.
func columnTotal(rows []map[string]any, column string) (float64, error) {
	if strings.TrimSpace(column) == "" {
		return 0, fmt.Errorf("the declaration names no column")
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("the scope selects none of the step's rows")
	}
	total := 0.0
	for i, r := range rows {
		raw, present := r[column]
		if !present {
			return 0, fmt.Errorf("the step's rows do not carry a column %q", column)
		}
		f, ok := asFloat(raw)
		if !ok {
			return 0, fmt.Errorf("column %q holds %v in row %d, which is not a number", column, raw, i+1)
		}
		total += f
	}
	return total, nil
}

func scopeFigureRows(rows []map[string]any, scope string) ([]map[string]any, error) {
	if strings.TrimSpace(scope) == "" {
		return rows, nil
	}
	return filterRows(rows, scope)
}
