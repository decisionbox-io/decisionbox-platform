package discovery

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The filter grammar is deliberately tiny: a conjunction of
// `column <op> literal` terms joined by AND, with op one of
// = == != <> < <= > >=.
//
// It stops there because the alternative is an expression evaluator, and an
// expression evaluator is a second query engine living beside the warehouse and
// disagreeing with it. Anything this cannot read is reported as undecidable
// rather than guessed at, so the evaluator's reach is visible in its verdicts
// instead of hidden in a wrong answer.
var filterTerm = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(<=|>=|!=|<>|==|=|<|>)\s*(.+?)\s*$`)

type quantifierTerm struct {
	column string
	op     string
	number float64
	text   string
	isNum  bool
}

// parseFilter reads a conjunction into clauses, or reports what it could not
// read.
func parseFilter(filter string) ([]quantifierTerm, error) {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" {
		return nil, fmt.Errorf("no filter given, so there is nothing to evaluate")
	}
	if strings.Contains(strings.ToUpper(trimmed), " OR ") {
		return nil, fmt.Errorf("filter %q contains OR, which this evaluator does not read", filter)
	}
	parts := regexp.MustCompile(`(?i)\s+AND\s+`).Split(trimmed, -1)
	clauses := make([]quantifierTerm, 0, len(parts))
	for _, p := range parts {
		m := filterTerm.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("filter term %q is not `column <op> literal`", strings.TrimSpace(p))
		}
		c := quantifierTerm{column: m[1], op: normaliseOp(m[2])}
		lit := m[3]
		if f, err := strconv.ParseFloat(strings.TrimSpace(lit), 64); err == nil {
			c.number, c.isNum = f, true
		} else {
			c.text = unquote(lit)
		}
		clauses = append(clauses, c)
	}
	return clauses, nil
}

func normaliseOp(op string) string {
	switch op {
	case "==":
		return "="
	case "<>":
		return "!="
	}
	return op
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// filterRows returns the rows satisfying every clause.
//
// A column the rows do not carry is an error rather than a non-match: a filter
// on a column that is not there would otherwise match nothing and read as a
// claim refuted, when in fact nothing was tested.
func filterRows(rows []map[string]any, filter string) ([]map[string]any, error) {
	clauses, err := parseFilter(filter)
	if err != nil {
		return nil, err
	}
	for _, c := range clauses {
		if len(rows) > 0 {
			if _, present := rows[0][c.column]; !present {
				return nil, fmt.Errorf("filter names column %q, which this step's rows do not carry", c.column)
			}
		}
	}
	var out []map[string]any
	for _, r := range rows {
		keep := true
		for _, c := range clauses {
			ok, err := c.matches(r)
			if err != nil {
				return nil, err
			}
			if !ok {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c quantifierTerm) matches(row map[string]any) (bool, error) {
	v := row[c.column]
	if c.isNum {
		f, ok := asFloat(v)
		if !ok {
			return false, fmt.Errorf("column %q holds a non-numeric value, so it cannot be compared to %g", c.column, c.number)
		}
		switch c.op {
		case "<":
			return f < c.number, nil
		case "<=":
			return f <= c.number, nil
		case ">":
			return f > c.number, nil
		case ">=":
			return f >= c.number, nil
		case "=":
			return f == c.number, nil
		case "!=":
			return f != c.number, nil
		}
		return false, fmt.Errorf("operator %q is not one this evaluator reads", c.op)
	}
	// String comparison: equality only. An ordering on text would be this
	// evaluator inventing a collation the warehouse never applied.
	s := strings.TrimSpace(fmt.Sprint(v))
	switch c.op {
	case "=":
		return strings.EqualFold(s, c.text), nil
	case "!=":
		return !strings.EqualFold(s, c.text), nil
	}
	return false, fmt.Errorf("operator %q cannot be applied to the text value %q", c.op, c.text)
}
