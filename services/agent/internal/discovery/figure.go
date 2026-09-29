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

	got, slack, err := evalFigure(f, ev.Rows)
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
	// And a bound that is not finite settles nothing. Two operands of 1e308 differ by zero,
	// but their magnitudes overflow when added, and an infinite tolerance certifies every
	// claim however wrong -- the mirror of a false refutation and worse, because it comes
	// back as `holds`. The arithmetic below is ordered to stay finite; this is the guard for
	// the case where the numbers are large enough that it cannot.
	//
	// Kept as an explicit invariant although it is no longer reachable on the outcome. The
	// precision rule below sends an infinite bound to undecidable anyway, since infinity
	// exceeds any interval, and a NaN bound would need a single term of eps*x to be infinite,
	// which no finite x produces. So there is no case that distinguishes this guard from the
	// rule below, and it is therefore NOT covered by the sabotage suite -- it states the
	// invariant rather than carrying it.
	if math.IsNaN(slack) || math.IsInf(slack, 0) {
		return undecidable(
			"the numbers behind %s over step %d are too large to bound its rounding error, so nothing here can say how far it could be out",
			f.Kind, f.Step)
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
	unresolvable := ""
	for _, factor := range percentScalings(f) {
		// The error bound scales with the reading, because it is an absolute bound on the
		// same quantity.
		candidate, candidateSlack := got*factor, slack*factor
		// A scaled reading that overflowed is not a reading, and it must not reach the
		// verdict. A percentage evaluating to 1e307 overflows on the `got*100` reading;
		// compareFigure calls that unresolved, but the assignment below had already stored
		// +Inf in Evaluated, and if the finite readings then disagreed the verdict shipped
		// carrying an infinity -- which cannot be marshalled, and takes the whole insights
		// payload with it. The same failure round 24 fixed for the answer itself.
		//
		// The unscaled reading is always finite here, because got is checked above, so
		// skipping the others never leaves the loop with nothing to compare.
		if math.IsNaN(candidate) || math.IsInf(candidate, 0) ||
			math.IsNaN(candidateSlack) || math.IsInf(candidateSlack, 0) {
			continue
		}
		switch compareFigure(f, candidate, candidateSlack) {
		case figureAgrees:
			v.Evaluated = candidate
			v.Status = models.FigureHolds
			return v

		case figureUnresolved:
			// The arithmetic's uncertainty straddles the edge of what the figure claims, so
			// some answers it could have produced agree and some do not. Nothing is
			// established, and certifying it would be a false `holds` -- worse than a false
			// refutation, because it ships as checked and every reference inherits that.
			//
			// Both halves of this were shipped. A total of exactly zero over a thousand
			// alternating 1e10 rows carries a worst-case bound of 1.11, which certified a
			// claim of 1; and 600 of those rows followed by 0.25 gives a true total of 0.25
			// with a bound of 0.40, which certified a claim of 1 because [-0.15, 0.65]
			// reaches into [0.5, 1.5]. The bound is not wrong in either -- it is a worst
			// case, and those columns happen to cancel -- but overlap is not agreement.
			if unresolvable == "" {
				v.Evaluated = candidate
				unresolvable = fmt.Sprintf(
					"%s over step %d comes to %s give or take %s, which reaches outside the %s this figure's precision claims, so nothing here can settle it either way",
					f.Kind, f.Step, formatFigure(candidate), formatFigure(candidateSlack),
					formatFigure(figureSlack(f)))
			}

		case figureDisagrees:
			if gap := relativeDistance(f.Value, candidate); gap < bestGap {
				best, bestGap = candidate, gap
			}
		}
	}
	// Reported only once every reading has been tried, so a percentage that settles cleanly
	// under one scaling is not lost to an unresolvable reading under another.
	//
	// Defensive rather than demonstrated: the bound scales with the reading, so a scaling that
	// agrees and a scaling that is precise enough pull in the same direction, and no fixture
	// has been found where returning at the first unresolvable reading differs from scanning
	// them all. Scanning cannot be worse, so it stays -- but this is not pinned by the
	// sabotage suite, because nothing reachable tells the two apart.
	if unresolvable != "" {
		return undecidable("%s", unresolvable)
	}
	v.Evaluated = best
	got = best
	v.Status = models.FigureFails
	v.Reason = fmt.Sprintf("%s over step %d gives %s, and the figure states %s (%s)",
		f.Kind, f.Step, formatFigure(got), v.Display, relativeGap(f.Value, got))
	return v
}

// evalFigure runs one figure's arithmetic, and reports how far the answer can be out.
//
// The second return is an absolute bound on this computation's own floating-point error,
// in the units of the value beside it. It exists because four review rounds tried to guess
// it from the RESULT and were each just short: the error a figure carries is set by the
// numbers that went in, and an excess of 100.0000015 over 100 divides a difference of
// 1.5e-6 by 100, so it inherits error from operands eight orders of magnitude larger than
// the answer. No expression in the result's own magnitude can see that, which is why each
// guess needed a bigger constant than the last. Derived here, where the operands are, it
// is a bound rather than a guess.
//
// eps is the relative error of a single float64, so eps*|x| bounds how far x sits from the
// decimal it was written as, and the per-kind expressions below propagate that through the
// arithmetic in the ordinary way.
//
// Every path that cannot compute returns an error, which the caller turns into undecidable
// -- never into a refutation.
func evalFigure(f models.Figure, rows []map[string]any) (value, slack float64, err error) {
	switch f.Kind {
	case models.FigureCell:
		// A cell is read, not computed. It is exactly the double the row holds.
		v, err := oneCell(rows, f.Column, f.Row, "row")
		return v, 0, err

	case models.FigureSum:
		scoped, err := scopeFigureRows(rows, f.Scope)
		if err != nil {
			return 0, 0, err
		}
		total, slack, err := columnTotal(scoped, f.Column)
		if err != nil {
			return 0, 0, err
		}
		return total, slack, nil

	case models.FigureCount:
		scoped, err := scopeFigureRows(rows, f.Scope)
		if err != nil {
			return 0, 0, err
		}
		// A count of rows is an exact integer.
		return float64(len(scoped)), 0, nil

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
			return 0, 0, err
		}
		// Zero is not the only undefined denominator. One whose own uncertainty reaches zero
		// is just as undefined, because the quotient is then unbounded -- and the bound below
		// cannot say so, since it scales with the quotient and a zero numerator collapses it
		// to nothing. A denominator column of [1e9, 0.01, -1e9, -0.01] totals exactly zero
		// but accumulates to -9.5e-9 with an uncertainty of 8.9e-7, and an excess with a zero
		// numerator over that was certified as exactly -100%.
		if math.Abs(den.value) <= den.slack {
			return 0, 0, fmt.Errorf(
				"the denominator of this %s cannot be told apart from zero (%s give or take %s), so the quotient is unbounded",
				f.Kind, formatFigure(den.value), formatFigure(den.slack))
		}
		q := num.value / den.value
		if f.Kind == models.FigureExcess {
			// (a-b)/b rather than a/b-1, which is the same number in exact arithmetic and
			// not in floating point.
			//
			// a/b-1 rounds the quotient first, near 1, and the subtraction then keeps that
			// absolute error while the result shrinks. (a-b)/b has no such step: when a and
			// b are close -- which is when it matters -- a-b is EXACT in floating point, so
			// the only rounding left is the division. Measured over the three cases review
			// found, it lands 1.1 to 2.4 times closer to the truth.
			//
			// What that buys is the accuracy of the number RECORDED, not the verdict. The
			// bound returned below is wide enough to absorb either form's error, so the
			// verdict came out the same both ways once it was derived from the operands --
			// but Evaluated is stored, served, and read by the correction gate, so it should
			// be the better number.
			//
			// Before the percent scaling, so an excess written as a percentage is
			// 100*(a-b)/b and not 100*a/b - 1.
			q = (num.value - den.value) / den.value
			if math.IsInf(q, 0) {
				// The subtraction overflowed although the answer is representable: 1e308
				// against -1e308 is an excess of exactly -2, but the difference is 2e308 and
				// there is no such float64. a/b-1 has no intermediate that large, so it
				// answers where the stable form cannot -- less accurately, which is the
				// trade this branch exists to make and only at magnitudes where the stable
				// form gives nothing at all.
				q = num.value/den.value - 1
			}
		}
		// Both readings differentiate the same way -- d/da = 1/b and d/db = -a/b^2, since
		// (a-b)/b is a/b minus a constant -- so each operand's own error passes through the
		// same partials: |dq| <= (ea + |a/b|*eb) / |b|.
		//
		// Written from the operands' bounds rather than from |a/b| alone, because a
		// denominator that is a column total carries the rounding of every addition in it,
		// not just the error of one double. With two cells this reduces to 2*eps*|a/b|,
		// which is what it was before review round 33.
		quotient := math.Abs(num.value / den.value)
		slack := (num.slack + quotient*den.slack) / math.Abs(den.value)
		if f.Unit == models.UnitPercent {
			q *= 100
			slack *= 100
		}
		return q, slack, nil

	case models.FigureDiff:
		// Both operands required. Without this, a diff declared with no `other` resolved
		// that operand to "every row", which on a single-row step is the same cell as
		// the left one -- so the arithmetic silently gave zero and refuted the figure.
		if strings.TrimSpace(f.Other) == "" {
			return 0, 0, fmt.Errorf("a diff needs an `other` row selector and none was given")
		}
		left, err := oneCell(rows, f.Column, f.Row, "row")
		if err != nil {
			return 0, 0, err
		}
		right, err := oneCell(rows, f.Column, f.Other, "other")
		if err != nil {
			return 0, 0, err
		}
		// Two operands, each out by at most eps of its own magnitude. Proportional to the
		// answer only when they are far apart, which is exactly when it does not matter.
		//
		// Each term scaled before they are added. Adding the magnitudes first overflows to
		// infinity for two operands of 1e308 -- whose difference is zero -- and an infinite
		// bound certifies every claim, however wrong. The order below stays finite; a bound
		// that goes non-finite anyway is caught in evaluateFigure.
		return left - right, floatEps*math.Abs(left) + floatEps*math.Abs(right), nil

	case "":
		return 0, 0, fmt.Errorf("the figure names no kind")
	default:
		return 0, 0, fmt.Errorf("kind %q is not one of cell, sum, count, ratio, excess, diff", f.Kind)
	}
}

// floatEps is the relative error of one float64: half the gap between 1 and the next
// representable value. eps*|x| bounds how far x sits from the decimal it was written as.
const floatEps = 1.1102230246251565e-16

// figureOperand is one number the arithmetic read, with a bound on how far it can be out.
//
// The bound travels with the value because the two cannot be recombined later. A cell is out
// by at most the error of writing it as a double; a column total is out by the rounding every
// addition accumulated, which on a hundred rows is two orders of magnitude more. A ratio over
// a column total inherits the second, and review round 33 found the bound short by ninefold
// for exactly that: a numerator of 1.23455 over a hundred cells of 0.1 evaluated to
// 12.345500000000024, which refuted a figure printing "12.345%" and then rewrote it to
// "12.346%" over accumulation error alone.
type figureOperand struct {
	value float64
	slack float64
}

func cellOperand(v float64) figureOperand {
	return figureOperand{value: v, slack: roundingSlack(v)}
}

// roundingSlack bounds how far one double sits from the decimal it was written as.
//
// eps times the magnitude, which is the relative error of a float64 -- floored at the
// smallest positive double, because at the bottom of the range that product UNDERFLOWS to
// zero while the true error gets larger rather than smaller. A subnormal has fewer
// significand bits than a normal value, so its spacing is an absolute 4.9e-324 rather than a
// relative eps: cells of "1.1e-320" and "1e-320" gave an excess a bound of exactly zero,
// which refuted a correct 10.00% and let the correction gate rewrite it to 9.98% and certify
// that.
//
// The floor applies to zero as well, and that is the part worth explaining. A first attempt
// exempted it -- an exact zero being the one value with no representation error -- and review
// walked straight through the exception: "1e-324" is below the smallest subnormal, so it
// PARSES to zero, and a numerator that underflowed was then treated as exact. The same
// certified correction, one value over. Nothing here can tell an exact zero from a decimal
// that vanished into it, because both arrive as the same float64, so the bound has to cover
// both. It costs the smallest positive double on a genuine zero, which cannot change any
// comparison at any magnitude.
//
// So the floor is unconditional, and the class is closed rather than one instance of it:
// every value gets a bound, and no value gets none.
func roundingSlack(v float64) float64 {
	if s := floatEps * math.Abs(v); s > math.SmallestNonzeroFloat64 {
		return s
	}
	return math.SmallestNonzeroFloat64
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
func ratioOperands(f models.Figure, rows []map[string]any) (num, den figureOperand, err error) {
	n, err := oneCell(rows, f.Column, f.Row, "row")
	if err != nil {
		return num, den, err
	}
	num = cellOperand(n)
	if strings.TrimSpace(f.Other) != "" {
		o, err := oneCell(rows, f.Column, f.Other, "other")
		if err != nil {
			return num, den, err
		}
		return num, cellOperand(o), nil
	}
	scoped, err := scopeFigureRows(rows, f.Scope)
	if err != nil {
		return num, den, err
	}
	total, slack, err := columnTotal(scoped, f.Column)
	if err != nil {
		return num, den, err
	}
	return num, figureOperand{value: total, slack: slack}, nil
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

// columnTotal sums a column over the rows given, and bounds the rounding the sum
// accumulated. A row missing the column is an error rather than a zero: a total silently
// short by one row reads as the model having written the wrong number.
//
// The bound is n times the sum of each term's own rounding, which is the standard bound for
// adding n numbers one after another. Each term is scaled as it is added rather than
// afterwards, so a column of values near the top of the float64 range does not overflow to
// an infinite -- and therefore unlimited -- tolerance, and roundingSlack keeps a term near
// the bottom of the range from contributing nothing.
func columnTotal(rows []map[string]any, column string) (total, slack float64, err error) {
	if strings.TrimSpace(column) == "" {
		return 0, 0, fmt.Errorf("the figure names no column")
	}
	if len(rows) == 0 {
		return 0, 0, fmt.Errorf("the scope selects none of the step's rows")
	}
	for i, r := range rows {
		raw, present := r[column]
		if !present {
			return 0, 0, fmt.Errorf("the step's rows do not carry a column %q", column)
		}
		n, ok := asFloat(raw)
		if !ok {
			return 0, 0, fmt.Errorf("column %q holds %v in row %d, which is not a number", column, raw, i+1)
		}
		total += n
		slack += roundingSlack(n)
	}
	return total, float64(len(rows)) * slack, nil
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
