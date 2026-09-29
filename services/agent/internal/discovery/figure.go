package discovery

import (
	"fmt"
	"math"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Checking a figure: run the arithmetic the model declared for it over the full rows of
// the step it named, and compare with the value it stated.
//
// The evaluator is unchanged from the version that measured well -- over 345
// declarations it produced three undecidables and no refutation that was not traceable
// to a gap in its own coverage. What changed around it is where the numbers come from:
// the value is a field, the precision comes from the format Go rendered with, and
// nothing is recovered from prose.
//
// Full rows, not the digest. The model wrote its figure from a compacted result that may
// have shown twenty of a hundred and fifty rows; the check runs over all of them. That
// asymmetry is the point, and it is the same reason attachQuantifierVerdicts evaluates
// over step.QueryResult.
//
// Nothing here rejects anything. Verdicts attach to the insight and the correction pass
// is the only thing that acts, for the reason stated on attachQuantifierVerdicts: until
// there is somewhere for a rejected insight to go, a gate turns one wrong number into a
// discarded document.

// EvaluateFigures settles every figure against the steps it cites, in declared order.
func EvaluateFigures(figures []models.Figure, steps map[int]StepRows) []models.FigureVerdict {
	out := make([]models.FigureVerdict, 0, len(figures))
	for _, f := range figures {
		out = append(out, evaluateFigure(f, steps))
	}
	return out
}

func evaluateFigure(f models.Figure, steps map[int]StepRows) models.FigureVerdict {
	v := models.FigureVerdict{
		ID: f.ID, Display: renderFigure(f), Step: f.Step, Kind: f.Kind, Claimed: f.Value,
	}
	undecidable := func(format string, args ...any) models.FigureVerdict {
		v.Status = models.FigureUndecidable
		v.Reason = fmt.Sprintf(format, args...)
		return v
	}

	// An id the prose cannot reference earns no verdict.
	//
	// The schemas accept any string, the resolving pattern accepts a letter followed by
	// letters, digits or underscores, and the evaluator never looked at the shape at all --
	// so `id: "revenue-total"` could be settled as holding while "{{revenue-total}}" shipped
	// in the published sentence. Refusing it in the render gate alone was no fix: the
	// renderer could not resolve that placeholder either way, and the verdict claiming the
	// figure was checked is the part a reader inherits.
	if !figureIDPattern.MatchString(f.ID) {
		return undecidable("%q is not an id the prose can reference; use a letter followed by letters, digits or underscores", f.ID)
	}

	if f.ValueMissing {
		// No value was declared, so there is no claim to settle -- and settling it as a
		// claim of zero is what printed "0" into a sentence, because the correction gate
		// then declines to replace it: zero is more than 1% from any real total.
		return undecidable("the figure declares no readable value")
	}

	ev, ok := steps[f.Step]
	if !ok {
		return undecidable("step %d is not among this insight's evidence", f.Step)
	}
	if len(ev.Rows) == 0 {
		return undecidable("step %d returned no rows", f.Step)
	}
	// A partial result cannot settle a figure that aggregates across rows, and this is the
	// one place in the layer where getting it wrong does active harm rather than merely
	// failing to catch something.
	//
	// The quantifier evaluator has refused incomplete evidence since the truncation work;
	// this path was reading the same StepRows and ignoring the same caveats. So a `sum`
	// over a capped result gave a partial total, the figure stating the true total was
	// refuted for disagreeing with it, and the correction gate -- which acts only inside
	// 1%, exactly where a small truncation lands -- replaced the correct number with the
	// partial one and re-settled it to `holds`. A right number becomes a wrong number and
	// is then certified, and a recommendation referencing it inherits both.
	//
	// Row-specific kinds are unaffected: `cell`, `diff` and a cell-over-cell `ratio` name
	// the rows they read, and a row is either in what came back or it is not, which oneCell
	// already reports. Only the kinds that fold the whole result need all of it.
	if aggregatesAcrossRows(f) && rowsIncomplete(ev.Quality) {
		return undecidable(
			"step %d came back incomplete, and %s folds every row, so the total it gives is a partial one",
			f.Step, f.Kind)
	}

	got, err := evalFigure(f, ev.Rows)
	if err != nil {
		return undecidable("%s", err.Error())
	}
	// A non-finite result is undecidable, not a refutation, and it must not be stored.
	// Division by a zero column total gives infinity and a zero-over-zero ratio gives NaN;
	// closeEnough already returns false for both, so the figure would be refuted for
	// arithmetic that produced no answer -- and the value would then be written into a
	// verdict that cannot be marshalled to JSON, which is how a single figure empties an
	// insights payload.
	if math.IsNaN(got) || math.IsInf(got, 0) {
		return undecidable("%s over step %d has no finite answer", f.Kind, f.Step)
	}
	// Evaluated is recorded in the figure's OWN terms, not the column's raw ones.
	//
	// A share is stored either as a fraction or as a percentage and the figure cannot see
	// which, so percentScalings offers both readings. Recording the raw value regardless
	// left the correction gate comparing a claim of 14.15% against an evaluated 0.1414,
	// judging them different quantities, and declining a correction to 14.14% that was
	// well inside its 1% limit -- and had it corrected, it would have written 0.1414 and
	// rendered "0.14%". So the scaling that was actually compared is the one kept: the
	// matching one when a reading holds, and otherwise the nearest, which is the only one
	// a correction could sensibly be measured against.
	v.Evaluated = got

	best, bestGap := got, math.Inf(1)
	for _, candidate := range percentScalings(f, got) {
		if closeEnough(f, candidate) {
			v.Evaluated = candidate
			v.Status = models.FigureHolds
			return v
		}
		if gap := relativeDistance(f.Value, candidate); gap < bestGap {
			best, bestGap = candidate, gap
		}
	}
	v.Evaluated = best
	got = best
	v.Status = models.FigureFails
	v.Reason = fmt.Sprintf("%s over step %d gives %s, and the figure states %s (%s)",
		f.Kind, f.Step, formatFigure(got), v.Display, relativeGap(f.Value, got))
	return v
}

// evalFigure runs one figure's arithmetic, or reports what it could not read. Every path
// that cannot compute returns an error, which the caller turns into undecidable -- never
// into a refutation.
func evalFigure(f models.Figure, rows []map[string]any) (float64, error) {
	switch f.Kind {
	case models.FigureCell:
		return oneCell(rows, f.Column, f.Row, "row")

	case models.FigureSum:
		scoped, err := scopeFigureRows(rows, f.Scope)
		if err != nil {
			return 0, err
		}
		return columnTotal(scoped, f.Column)

	case models.FigureCount:
		scoped, err := scopeFigureRows(rows, f.Scope)
		if err != nil {
			return 0, err
		}
		return float64(len(scoped)), nil

	case models.FigureRatio, models.FigureExcess:
		// Two kinds, one pair of operands. `ratio` is the quotient and `excess` is the
		// quotient minus one, and separating them is the whole reason `excess` exists:
		// the two answer different sentences -- "109.6% OF the lowest band" and "109.6%
		// MORE than it" -- and the only thing distinguishing them in the prose is a word
		// the evaluator cannot read. A run that offered only `ratio` shipped a vouched
		// figure behind "109.6% more" where the excess is 9.6%, and refuted a second
		// insight that wrote its excess correctly. See models.FigureExcess.
		num, den, err := ratioOperands(f, rows)
		if err != nil {
			return 0, err
		}
		if den == 0 {
			return 0, fmt.Errorf("the denominator of this %s is zero, so it is undefined", f.Kind)
		}
		q := num / den
		if f.Kind == models.FigureExcess {
			// Before the percent scaling, so an excess written as a percentage is
			// 100*(a/b - 1) and not 100*a/b - 1.
			q -= 1
		}
		if f.Unit == models.UnitPercent {
			q *= 100
		}
		return q, nil

	case models.FigureDiff:
		// Both operands required. Without this, a diff declared with no `other` resolved
		// that operand to "every row", which on a single-row step is the same cell as
		// the left one -- so the arithmetic silently gave zero and refuted the figure.
		if strings.TrimSpace(f.Other) == "" {
			return 0, fmt.Errorf("a diff needs an `other` row selector and none was given")
		}
		left, err := oneCell(rows, f.Column, f.Row, "row")
		if err != nil {
			return 0, err
		}
		right, err := oneCell(rows, f.Column, f.Other, "other")
		if err != nil {
			return 0, err
		}
		return left - right, nil

	case "":
		return 0, fmt.Errorf("the figure names no kind")
	default:
		return 0, fmt.Errorf("kind %q is not one of cell, sum, count, ratio, excess, diff", f.Kind)
	}
}

// ratioOperands resolves the numerator and denominator a `ratio` or an `excess` declares.
//
// The numerator is one cell. The denominator is another cell when `other` names one, and
// the column total over `scope` otherwise.
//
// Cell-over-cell was missing from the first version, and its absence did not merely cost
// coverage. Every spread, multiple and percentage change in a document is a ratio of two
// cells -- "4.8x more often", "within 5% of each other" -- and with no way to declare one
// the model declared the nearest kind it had, a share of the column total. That reads as a
// different quantity and is refuted, so the figure was reported false when only the
// declaration was. The lesson QuantifierAll was added for: a missing kind is not a gap in
// coverage, it is a false positive waiting for the model to approximate it.
func ratioOperands(f models.Figure, rows []map[string]any) (num, den float64, err error) {
	if num, err = oneCell(rows, f.Column, f.Row, "row"); err != nil {
		return 0, 0, err
	}
	if strings.TrimSpace(f.Other) != "" {
		if den, err = oneCell(rows, f.Column, f.Other, "other"); err != nil {
			return 0, 0, err
		}
		return num, den, nil
	}
	scoped, err := scopeFigureRows(rows, f.Scope)
	if err != nil {
		return 0, 0, err
	}
	if den, err = columnTotal(scoped, f.Column); err != nil {
		return 0, 0, err
	}
	return num, den, nil
}

// oneCell resolves a single-row selector to one numeric cell.
//
// A selector matching several rows is undecidable rather than a refutation, and so is
// one matching none. Both mean the declaration did not identify a cell, which is a
// statement about the declaration and not about the figure. Silently taking the first of
// several matches would turn an ambiguous declaration into a confident wrong answer.
func oneCell(rows []map[string]any, column, selector, field string) (float64, error) {
	if strings.TrimSpace(column) == "" {
		return 0, fmt.Errorf("the figure names no column")
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
	n, ok := asFloat(raw)
	if !ok {
		return 0, fmt.Errorf("column %q holds %v, which is not a number", column, raw)
	}
	return n, nil
}

// columnTotal sums a column over the rows given. A row missing the column is an error
// rather than a zero: a total silently short by one row reads as the model having
// written the wrong number.
func columnTotal(rows []map[string]any, column string) (float64, error) {
	if strings.TrimSpace(column) == "" {
		return 0, fmt.Errorf("the figure names no column")
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
		n, ok := asFloat(raw)
		if !ok {
			return 0, fmt.Errorf("column %q holds %v in row %d, which is not a number", column, raw, i+1)
		}
		total += n
	}
	return total, nil
}

func scopeFigureRows(rows []map[string]any, scope string) ([]map[string]any, error) {
	if strings.TrimSpace(scope) == "" {
		return rows, nil
	}
	return filterRows(rows, scope)
}

// aggregatesAcrossRows reports whether a figure's arithmetic folds the result set rather
// than reading named rows out of it.
//
// A ratio and an excess are in both camps depending on how they were declared: with
// `other` it is one row against another, and without it the denominator is the column
// total over scope, which is a fold.
func aggregatesAcrossRows(f models.Figure) bool {
	switch f.Kind {
	case models.FigureSum, models.FigureCount:
		return true
	case models.FigureRatio, models.FigureExcess:
		return strings.TrimSpace(f.Other) == ""
	}
	return false
}

// duplicateFigureIDs names the ids a document declared more than once.
//
// A repeated id makes every lookup in this layer last-wins while the evaluator still
// produces a verdict for each declaration, so the two get crossed: two figures both called
// f1, one refuted at 100 against 99 and one holding at 200, let the first verdict's numbers
// through the proximity gate and write 99 over the second figure. Rendering has the same
// ambiguity with no way to resolve it.
//
// So nothing acts on a duplicated id: it is not corrected, not referenceable, and not
// rendered. The reference ships visible, which is this layer's standing answer to a
// reference it cannot resolve -- a reader seeing "{{f1}}" knows something went wrong, and a
// reader seeing the wrong number does not.
func duplicateFigureIDs(figures []models.Figure) map[string]bool {
	seen := make(map[string]int, len(figures))
	for _, f := range figures {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			continue
		}
		seen[id]++
	}
	var dup map[string]bool
	for id, n := range seen {
		if n > 1 {
			if dup == nil {
				dup = map[string]bool{}
			}
			dup[id] = true
		}
	}
	return dup
}

// figuresByID and figureIndexByID are the only two ways this package should key figures by
// id, and they exist because the duplicate guard kept being added one site at a time.
//
// It went in at the correction pass, the reference index and the renderer, and was missed at
// the recommendation adoption pass -- where a review found exactly the crossing it was meant
// to stop: two figures named f1 both omitting a value, the first resolving to 100 and the
// second referencing nothing, let the first verdict's number into the second declaration.
// Three sites guarded and one not is what happens when the guard is a line you remember to
// write; here it is the constructor, so a lookup that skips it has to be written on purpose.
func figuresByID(figures []models.Figure) map[string]models.Figure {
	dup := duplicateFigureIDs(figures)
	out := make(map[string]models.Figure, len(figures))
	for _, f := range figures {
		if strings.TrimSpace(f.ID) == "" || dup[f.ID] {
			continue
		}
		out[f.ID] = f
	}
	return out
}

func figureIndexByID(figures []models.Figure) map[string]int {
	dup := duplicateFigureIDs(figures)
	out := make(map[string]int, len(figures))
	for i, f := range figures {
		if strings.TrimSpace(f.ID) == "" || dup[f.ID] {
			continue
		}
		out[f.ID] = i
	}
	return out
}
