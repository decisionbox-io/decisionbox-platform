package warehouse

import "testing"

// The capped cases are the real queries from the discovery corpora that
// produced a false population claim, so a regression here is the actual
// defect coming back rather than a synthetic stand-in.
func TestTrailingLimit(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"corpus p_type step", "SELECT p.p_type, COUNT(*) AS lineitems FROM lineitem l JOIN part p ON l.l_partkey = p.p_partkey GROUP BY p.p_type ORDER BY net_revenue DESC LIMIT 15", 15, true},
		{"corpus container step", "SELECT p.p_container, SUM(l.l_quantity) FROM lineitem l WHERE l.l_shipdate BETWEEN '1993-01-01' AND '1994-12-31' GROUP BY p.p_container ORDER BY net_revenue DESC LIMIT 12", 12, true},
		{"corpus products step", "SELECT product_name, SUM(profit) AS profit FROM orders GROUP BY product_name ORDER BY profit ASC LIMIT 12", 12, true},
		{"trailing semicolon", "SELECT a FROM t LIMIT 10;", 10, true},
		{"trailing whitespace and newline", "SELECT a FROM t\nLIMIT 10\n  ", 10, true},
		{"lowercase", "select a from t limit 7", 7, true},
		{"with offset", "SELECT a FROM t LIMIT 25 OFFSET 50", 25, true},
		{"uncapped", "SELECT a FROM t ORDER BY a DESC", 0, false},
		{"uncapped group by", "SELECT tier, SUM(x) FROM t GROUP BY tier ORDER BY 2 DESC", 0, false},
		// Anchoring: the cap must govern the output, not an intermediate set.
		{"limit in subquery only", "SELECT * FROM t WHERE id IN (SELECT id FROM u ORDER BY x LIMIT 10)", 0, false},
		{"subquery limit then outer group by", "SELECT k, COUNT(*) FROM (SELECT k FROM u LIMIT 100) s GROUP BY k", 0, false},
		{"outer limit wins over subquery limit", "SELECT k FROM (SELECT k FROM u LIMIT 100) s LIMIT 5", 5, true},
		{"zero cap is not a cap", "SELECT a FROM t LIMIT 0", 0, false},
		{"non-literal cap", "SELECT a FROM t LIMIT $1", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := TrailingLimit(tc.query)
			if n != tc.want || ok != tc.ok {
				t.Errorf("TrailingLimit(%q) = (%d, %v), want (%d, %v)", tc.query, n, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLeadingTop(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"plain", "SELECT TOP 15 p_type, SUM(x) FROM part GROUP BY p_type", 15, true},
		{"parenthesised", "SELECT TOP (15) p_type FROM part", 15, true},
		{"distinct", "SELECT DISTINCT TOP 10 a FROM t", 10, true},
		{"lowercase", "select top 5 a from t", 5, true},
		{"leading whitespace", "\n  SELECT TOP 3 a FROM t", 3, true},
		// PERCENT bounds a proportion, so row_count == n carries no signal.
		{"percent is not a cap", "SELECT TOP 10 PERCENT a FROM t", 0, false},
		{"percent lowercase", "select top 10 percent a from t", 0, false},
		{"uncapped", "SELECT a FROM t", 0, false},
		// TOP must lead the statement; the word elsewhere is not a cap.
		{"top as identifier", "SELECT top_sellers FROM t", 0, false},
		{"top in subquery only", "SELECT a FROM (SELECT TOP 5 a FROM t) s", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := LeadingTop(tc.query)
			if n != tc.want || ok != tc.ok {
				t.Errorf("LeadingTop(%q) = (%d, %v), want (%d, %v)", tc.query, n, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTrailingFetchFirst(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"oracle rows only", "SELECT p_type FROM part ORDER BY rev DESC FETCH FIRST 15 ROWS ONLY", 15, true},
		{"singular row", "SELECT a FROM t FETCH FIRST 1 ROW ONLY", 1, true},
		{"next", "SELECT a FROM t FETCH NEXT 20 ROWS ONLY", 20, true},
		{"semicolon", "SELECT a FROM t FETCH FIRST 5 ROWS ONLY;", 5, true},
		{"lowercase", "select a from t fetch first 8 rows only", 8, true},
		// WITH TIES can exceed n, so equality with row_count means nothing.
		{"with ties is not a cap", "SELECT a FROM t ORDER BY x FETCH FIRST 5 ROWS WITH TIES", 0, false},
		{"uncapped", "SELECT a FROM t", 0, false},
		{"fetch in subquery only", "SELECT a FROM (SELECT a FROM t FETCH FIRST 5 ROWS ONLY) s", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := TrailingFetchFirst(tc.query)
			if n != tc.want || ok != tc.ok {
				t.Errorf("TrailingFetchFirst(%q) = (%d, %v), want (%d, %v)", tc.query, n, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRownumCap(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"less than or equal", "SELECT a FROM t WHERE ROWNUM <= 15", 15, true},
		{"strictly less caps at n-1", "SELECT a FROM t WHERE ROWNUM < 15", 14, true},
		{"lowercase", "select a from t where rownum <= 6", 6, true},
		{"spaced operator", "SELECT a FROM t WHERE ROWNUM  <=  9", 9, true},
		{"strictly less than one is not a cap", "SELECT a FROM t WHERE ROWNUM < 1", 0, false},
		{"uncapped", "SELECT a FROM t", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := RownumCap(tc.query)
			if n != tc.want || ok != tc.ok {
				t.Errorf("RownumCap(%q) = (%d, %v), want (%d, %v)", tc.query, n, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAnyRowCap(t *testing.T) {
	q := "SELECT a FROM t FETCH FIRST 9 ROWS ONLY"
	if n, ok := AnyRowCap(q, TrailingLimit, TrailingFetchFirst); !ok || n != 9 {
		t.Errorf("AnyRowCap = (%d, %v), want (9, true)", n, ok)
	}
	if _, ok := AnyRowCap("SELECT a FROM t", TrailingLimit, TrailingFetchFirst); ok {
		t.Error("AnyRowCap on an uncapped query = true, want false")
	}
	if _, ok := AnyRowCap("SELECT a FROM t LIMIT 3"); ok {
		t.Error("AnyRowCap with no matchers = true, want false")
	}
}

// The caveat has to name the cap, because the model's job on reading it is to
// scope a claim to exactly those rows.
func TestRowCapCaveat(t *testing.T) {
	c := RowCapCaveat(15)
	if c.Kind != QualityTruncated {
		t.Errorf("Kind = %q, want %q", c.Kind, QualityTruncated)
	}
	for _, want := range []string{"15", "top-15", "not the whole population"} {
		if !contains(c.Detail, want) {
			t.Errorf("Detail = %q, want it to contain %q", c.Detail, want)
		}
	}
	if CaveatInstruction([]QualityCaveat{c}) == "" {
		t.Error("CaveatInstruction rendered empty for a row-cap caveat")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
