package discovery

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// A quantifier claim is any statement whose truth depends on rows other than
// the ones it names: "the only loss-making line", "the second largest pool",
// "margin improved every year", "p_type has 15 values". The model writes these
// from a table it can see, and gets them wrong in a specific way — it reasons
// over the ordering the table happens to carry rather than the one the claim
// needs.
//
// The observed case: an insight claimed Tables was "the only top-10 revenue
// line running a loss". Its cited step held all seventeen sub-categories with
// both sales and profit, inline, ordered by profit. Ranking on sales while
// filtering on profit needed a mental re-sort of seventeen rows, and Bookcases
// — rank 9 by sales, and loss-making — was missed. The document's own body
// named Bookcases as loss-making two sentences later.
//
// Nothing there was unknowable. The rows were present, correct and complete;
// only the check was not performed. So the check is what moves into Go: the
// model declares which step, column and predicate a quantifier claim rests on,
// and Go evaluates the predicate over that step's full rows.
//
// This only ever catches a claim the model declares. A superlative it does not
// recognise as one carries no declaration and is not checked. The alternative —
// scanning prose for "only", "largest", "every" and failing an undeclared match
// — is a regular expression over natural language deciding whether a claim is
// true, which on this corpus produced one true positive and five false ones.
// Declaring is mechanical and checking is not, so only the checking moves here.

// QuantifierKind names the predicate shape a claim rests on. Each kind fixes
// what Go must evaluate and which of QuantifierClaim's fields it reads.
type QuantifierKind = string

const (
	// QuantifierOnly — exactly one row in scope satisfies Filter.
	QuantifierOnly QuantifierKind = "only"
	// QuantifierRank — the row Subject identifies sits at Rank by Column.
	QuantifierRank QuantifierKind = "rank"
	// QuantifierMonotonic — Column moves in one direction across the rows.
	QuantifierMonotonic QuantifierKind = "monotonic"
	// QuantifierCardinality — Count rows in scope satisfy Filter.
	QuantifierCardinality QuantifierKind = "cardinality"
	// QuantifierAll — every row in scope satisfies Filter.
	//
	// Added because its absence produced wrong answers rather than no answers.
	// Without it the model reached for the nearest kind it had and declared
	// "Tables ran a loss in every year" as monotonic, so the evaluator
	// faithfully reported that profit was not monotonically decreasing -- true,
	// irrelevant, and read as the claim being refuted. A missing kind is not a
	// gap in coverage; it is a false positive waiting for the model to
	// approximate it.
	QuantifierAll QuantifierKind = "all"
)

// QuantifierStatus is the outcome of evaluating one claim.
type QuantifierStatus = string

const (
	// QuantifierHolds — the predicate is true over the step's rows.
	QuantifierHolds QuantifierStatus = "holds"
	// QuantifierFails — the predicate is false over the step's rows. This is
	// the only status that says the claim is wrong.
	QuantifierFails QuantifierStatus = "fails"
	// QuantifierUndecidable — the rows cannot settle it: the step is missing,
	// the column is absent, the filter is richer than the evaluator reads, or
	// the step is a capped view and no rank, count or uniqueness claim over a
	// top-N is decidable at all.
	//
	// Distinct from Fails on purpose. Undecidable is the evaluator declining;
	// treating it as a failure would reject sound claims for the evaluator's
	// own limits, which is how a checker starts costing more than it catches.
	QuantifierUndecidable QuantifierStatus = "undecidable"
)

// StepRows is the evidence an evaluation runs over: one step's full result rows
// plus whatever the source (or E1) said about their fidelity.
type StepRows struct {
	Rows    []map[string]any
	Quality []gowarehouse.QualityCaveat
}

// EvaluateQuantifierClaims settles every declared claim against the steps it
// cites, in declaration order.
func EvaluateQuantifierClaims(claims []models.QuantifierClaim, steps map[int]StepRows) []models.QuantifierVerdict {
	out := make([]models.QuantifierVerdict, 0, len(claims))
	for _, c := range claims {
		out = append(out, evaluateQuantifierClaim(c, steps))
	}
	return out
}

func evaluateQuantifierClaim(c models.QuantifierClaim, steps map[int]StepRows) models.QuantifierVerdict {
	v := models.QuantifierVerdict{Claim: c.Claim, Kind: c.Kind, Step: c.Step}
	undecidable := func(format string, args ...any) models.QuantifierVerdict {
		v.Status = QuantifierUndecidable
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

	// A capped step usually cannot settle a rank, a count or a uniqueness
	// claim: the rows it withheld are exactly the ones that would refute one.
	//
	// Unless the claim is about the returned rows themselves. "the only
	// loss-making line among the 10 largest by sales", declared with top_n 10
	// against a step that returned exactly those 10, ranges over a set the
	// step contains in full -- there the cap is the claim's scope, not a hole
	// in its evidence. Refusing that one was measured: it is the shape of the
	// claim this evaluator exists for, and a blanket refusal declined the only
	// declaration in a five-sample replay that named the original defect.
	if isTruncated(ev.Quality) && !scopedWithinResult(c, len(ev.Rows)) {
		return undecidable("step %d is a capped top-N view and this claim ranges beyond the rows it returned, so it is not decidable", c.Step)
	}

	scope, err := scopeRows(ev.Rows, c)
	if err != nil {
		return undecidable("%v", err)
	}

	switch c.Kind {
	case QuantifierOnly:
		return evalOnly(v, scope, c)
	case QuantifierCardinality:
		return evalCardinality(v, scope, c)
	case QuantifierAll:
		return evalAll(v, scope, c)
	case QuantifierRank:
		return evalRank(v, scope, c)
	case QuantifierMonotonic:
		return evalMonotonic(v, scope, c)
	default:
		return undecidable("unrecognised quantifier kind %q", c.Kind)
	}
}

// scopeRows narrows the step's rows to the set the claim ranges over: the rows
// matching Scope, and then the top N of those by a column, when the claim says
// so.
//
// Scope is applied first. A claim about the ten largest Tables months means the
// ten largest among the Tables rows, not the Tables rows among the ten largest
// overall -- those are different sets and only the first is what the sentence
// says.
func scopeRows(rows []map[string]any, c models.QuantifierClaim) ([]map[string]any, error) {
	if c.Scope != "" {
		scoped, err := filterRows(rows, c.Scope)
		if err != nil {
			return nil, fmt.Errorf("scope %q: %w", c.Scope, err)
		}
		if len(scoped) == 0 {
			return nil, fmt.Errorf("scope %q selects no rows of step %d", c.Scope, c.Step)
		}
		rows = scoped
	}
	if c.TopN <= 0 {
		return rows, nil
	}
	if c.TopNColumn == "" {
		return nil, fmt.Errorf("top_n %d given without top_n_column", c.TopN)
	}
	sorted, err := sortByColumn(rows, c.TopNColumn, "desc")
	if err != nil {
		return nil, err
	}
	if c.TopN < len(sorted) {
		sorted = sorted[:c.TopN]
	}
	return sorted, nil
}

func evalOnly(v models.QuantifierVerdict, scope []map[string]any, c models.QuantifierClaim) models.QuantifierVerdict {
	matched, err := filterRows(scope, c.Filter)
	if err != nil {
		v.Status, v.Reason = QuantifierUndecidable, err.Error()
		return v
	}
	if len(matched) == 1 {
		v.Status = QuantifierHolds
		v.Reason = fmt.Sprintf("exactly 1 of %d rows in scope satisfies %s", len(scope), c.Filter)
		return v
	}
	v.Status = QuantifierFails
	v.Reason = fmt.Sprintf("%d of %d rows in scope satisfy %s, not 1%s",
		len(matched), len(scope), c.Filter, namesOf(scope, matched))
	return v
}

func evalCardinality(v models.QuantifierVerdict, scope []map[string]any, c models.QuantifierClaim) models.QuantifierVerdict {
	matched := scope
	if c.Filter != "" {
		var err error
		if matched, err = filterRows(scope, c.Filter); err != nil {
			v.Status, v.Reason = QuantifierUndecidable, err.Error()
			return v
		}
	}
	if len(matched) == c.Count {
		v.Status = QuantifierHolds
		v.Reason = fmt.Sprintf("%d rows in scope, as claimed", c.Count)
		return v
	}
	v.Status = QuantifierFails
	v.Reason = fmt.Sprintf("%d rows in scope satisfy the claim, not the %d asserted", len(matched), c.Count)
	return v
}

// scopedWithinResult reports whether the claim ranges only over rows the step
// actually returned.
//
// TopN only, deliberately. A Scope filter narrows the rows in hand but says
// nothing about the rows the cap withheld -- one of those could match the scope
// and refute the claim, which is the situation this refusal exists for. A top-N
// scope is different in kind: it names a set the result contains in full.
func scopedWithinResult(c models.QuantifierClaim, returned int) bool {
	return c.TopN > 0 && c.TopN <= returned
}

func evalAll(v models.QuantifierVerdict, scope []map[string]any, c models.QuantifierClaim) models.QuantifierVerdict {
	matched, err := filterRows(scope, c.Filter)
	if err != nil {
		v.Status, v.Reason = QuantifierUndecidable, err.Error()
		return v
	}
	if len(matched) == len(scope) {
		v.Status = QuantifierHolds
		v.Reason = fmt.Sprintf("all %d rows in scope satisfy %s", len(scope), c.Filter)
		return v
	}
	var counter []map[string]any
	for _, r := range scope {
		found := false
		for _, m := range matched {
			if sameRow(r, m) {
				found = true
				break
			}
		}
		if !found {
			counter = append(counter, r)
		}
	}
	v.Status = QuantifierFails
	v.Reason = fmt.Sprintf("%d of %d rows in scope do not satisfy %s%s",
		len(counter), len(scope), c.Filter, namesOf(scope, counter))
	return v
}

func evalRank(v models.QuantifierVerdict, scope []map[string]any, c models.QuantifierClaim) models.QuantifierVerdict {
	if c.Rank <= 0 {
		v.Status, v.Reason = QuantifierUndecidable, "rank claim carries no rank"
		return v
	}
	if c.Column == "" {
		v.Status, v.Reason = QuantifierUndecidable, "rank claim names no column to rank by"
		return v
	}
	order := c.Order
	if order == "" {
		order = "desc"
	}
	sorted, err := sortByColumn(scope, c.Column, order)
	if err != nil {
		v.Status, v.Reason = QuantifierUndecidable, err.Error()
		return v
	}
	subject, err := filterRows(sorted, c.Subject)
	if err != nil {
		v.Status, v.Reason = QuantifierUndecidable, err.Error()
		return v
	}
	if len(subject) != 1 {
		v.Status = QuantifierUndecidable
		v.Reason = fmt.Sprintf("subject %q selects %d rows, not 1", c.Subject, len(subject))
		return v
	}
	actual := 0
	for i, r := range sorted {
		if sameRow(r, subject[0]) {
			actual = i + 1
			break
		}
	}
	if actual == c.Rank {
		v.Status = QuantifierHolds
		v.Reason = fmt.Sprintf("%s is rank %d by %s (%s)", c.Subject, actual, c.Column, order)
		return v
	}
	v.Status = QuantifierFails
	v.Reason = fmt.Sprintf("%s is rank %d by %s (%s), not %d", c.Subject, actual, c.Column, order, c.Rank)
	return v
}

func evalMonotonic(v models.QuantifierVerdict, scope []map[string]any, c models.QuantifierClaim) models.QuantifierVerdict {
	if c.Column == "" {
		v.Status, v.Reason = QuantifierUndecidable, "monotonic claim names no column"
		return v
	}
	vals := make([]float64, 0, len(scope))
	for _, r := range scope {
		f, ok := asFloat(r[c.Column])
		if !ok {
			v.Status = QuantifierUndecidable
			v.Reason = fmt.Sprintf("column %q is not numeric in every row of step %d", c.Column, c.Step)
			return v
		}
		vals = append(vals, f)
	}
	if len(vals) < 2 {
		v.Status, v.Reason = QuantifierUndecidable, "fewer than 2 rows, so no direction to check"
		return v
	}
	up := c.Trend != "decreasing"
	for i := 1; i < len(vals); i++ {
		broke := vals[i] <= vals[i-1]
		if !up {
			broke = vals[i] >= vals[i-1]
		}
		if broke {
			v.Status = QuantifierFails
			v.Reason = fmt.Sprintf("%s is not %s throughout: it goes %g -> %g between rows %d and %d",
				c.Column, trendWord(up), vals[i-1], vals[i], i, i+1)
			return v
		}
	}
	v.Status = QuantifierHolds
	v.Reason = fmt.Sprintf("%s is %s across all %d rows", c.Column, trendWord(up), len(vals))
	return v
}

func trendWord(up bool) string {
	if up {
		return "increasing"
	}
	return "decreasing"
}

// namesOf lists the rows that refuted an "only" claim, so a repair prompt can
// quote them rather than a count. A model told "2 rows, not 1" has to find
// those rows again, which is the step it got wrong in the first place.
//
// The label column is chosen from the data rather than guessed: the string
// column with the most distinct values across the scope, ties broken by column
// name. Picking the first string column found would depend on Go's map
// iteration order and could name "Furniture" twice where the rows are Tables
// and Bookcases.
func namesOf(scope, matched []map[string]any) string {
	col := labelColumn(scope)
	if col == "" {
		return ""
	}
	labels := make([]string, 0, len(matched))
	for _, r := range matched {
		if s, ok := r[col].(string); ok && s != "" {
			labels = append(labels, s)
		}
	}
	if len(labels) == 0 {
		return ""
	}
	sort.Strings(labels)
	return " (" + strings.Join(labels, ", ") + ")"
}

// labelColumn picks the string column that best identifies a row.
func labelColumn(rows []map[string]any) string {
	if len(rows) == 0 {
		return ""
	}
	names := make([]string, 0, len(rows[0]))
	for k, v := range rows[0] {
		if _, ok := v.(string); ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	best, bestN := "", 0
	for _, n := range names {
		seen := map[string]struct{}{}
		for _, r := range rows {
			if s, ok := r[n].(string); ok {
				seen[s] = struct{}{}
			}
		}
		if len(seen) > bestN {
			best, bestN = n, len(seen)
		}
	}
	return best
}

func isTruncated(q []gowarehouse.QualityCaveat) bool {
	for _, c := range q {
		if c.Kind == gowarehouse.QualityTruncated {
			return true
		}
	}
	return false
}

func sameRow(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || fmt.Sprint(av) != fmt.Sprint(bv) {
			return false
		}
	}
	return true
}

// sortByColumn returns the rows ordered by one numeric column. Stable, so rows
// tied on the column keep their result order and a rank over a tie is at least
// reproducible.
func sortByColumn(rows []map[string]any, col, order string) ([]map[string]any, error) {
	out := make([]map[string]any, len(rows))
	copy(out, rows)
	for _, r := range out {
		if _, ok := asFloat(r[col]); !ok {
			return nil, fmt.Errorf("column %q is missing or not numeric in every row, so it cannot be ranked", col)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := asFloat(out[i][col])
		b, _ := asFloat(out[j][col])
		if order == "asc" {
			return a < b
		}
		return a > b
	})
	return out, nil
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
