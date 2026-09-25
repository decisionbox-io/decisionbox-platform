package warehouse

import (
	"strings"
	"testing"
)

// capProvider is a concrete provider that can recognise its own dialect's cap.
type capProvider struct {
	Provider
	cap int
}

func (p capProvider) RowCap(string) (int, bool) { return p.cap, true }

// opaqueWrapper is what a middleware normally produces: a value satisfying
// Provider and nothing else, so every optional capability behind it is erased.
type opaqueWrapper struct{ Provider }

// unwrappingWrapper is the same thing but honest about what it wraps. It embeds
// Provider to satisfy the interface -- nothing here calls a Provider method --
// and keeps the wrapped one separately so Unwrap can hand it back.
type unwrappingWrapper struct {
	Provider
	inner Provider
}

func (w unwrappingWrapper) Unwrap() Provider { return w.inner }

type selfWrapper struct{ Provider }

func (w selfWrapper) Unwrap() Provider { return w }

func TestRowCapInspectorOf(t *testing.T) {
	inner := capProvider{cap: 25}

	if i, ok := rowCapInspectorOf(inner); !ok {
		t.Error("an unwrapped provider must be found directly")
	} else if n, _ := i.RowCap("x"); n != 25 {
		t.Errorf("RowCap = %d, want 25", n)
	}

	// The case this exists for: one layer of middleware that forwards Unwrap.
	if i, ok := rowCapInspectorOf(unwrappingWrapper{inner: inner}); !ok {
		t.Error("a wrapper exposing Unwrap must not hide the capability")
	} else if n, _ := i.RowCap("x"); n != 25 {
		t.Errorf("RowCap through a wrapper = %d, want 25", n)
	}

	// Two layers, because middlewares compose.
	nested := unwrappingWrapper{inner: unwrappingWrapper{inner: inner}}
	if _, ok := rowCapInspectorOf(nested); !ok {
		t.Error("nested wrappers must still resolve")
	}

	// A wrapper that neither implements the capability nor exposes Unwrap yields
	// nothing — unchanged behaviour, and the honest answer.
	if _, ok := rowCapInspectorOf(opaqueWrapper{Provider: inner}); ok {
		t.Error("an opaque wrapper cannot be seen through, and must not claim to be")
	}

	// A wrapper returning itself must terminate rather than spin.
	if _, ok := rowCapInspectorOf(selfWrapper{}); ok {
		t.Error("a self-returning wrapper must report nothing")
	}

	if _, ok := rowCapInspectorOf(nil); ok {
		t.Error("a nil provider must report nothing")
	}
}

// The caveat is what the agent actually consumes, so assert it end to end
// through the runner adapter a wrapped provider reaches it by.
func TestSQLRunner_RowCapSurvivesAWrapper(t *testing.T) {
	inner := capProvider{cap: 15}
	r := sqlRunner{p: unwrappingWrapper{inner: inner}}
	n, ok := r.RowCap("SELECT 1 LIMIT 15")
	if !ok || n != 15 {
		t.Errorf("RowCap = (%d, %v), want (15, true)", n, ok)
	}
}

func TestTrailingOffset(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"SELECT * FROM t LIMIT 100 OFFSET 100":        {100, true},
		"select * from t limit 10 offset 5;":          {5, true},
		"SELECT * FROM t LIMIT 100 OFFSET 100 ":       {100, true},
		"SELECT * FROM t LIMIT 100":                   {0, false}, // capped, not paginated
		"SELECT * FROM t":                             {0, false},
		"SELECT * FROM t LIMIT 100 OFFSET 0":          {0, false}, // skips nothing
		"SELECT * FROM (SELECT 1 LIMIT 5 OFFSET 5) x": {0, false}, // not the governing clause
	}
	for q, want := range cases {
		got, ok := TrailingOffset(q)
		if got != want.want || ok != want.ok {
			t.Errorf("TrailingOffset(%q) = (%d,%v), want (%d,%v)", q, got, ok, want.want, want.ok)
		}
	}
}

// The cap reader must be unaffected by capturing the offset alongside it.
func TestTrailingLimit_UnchangedByTheOffsetCapture(t *testing.T) {
	for q, want := range map[string]int{
		"SELECT * FROM t LIMIT 100":            100,
		"SELECT * FROM t LIMIT 100 OFFSET 100": 100,
		"select * from t limit 25;":            25,
	} {
		got, ok := TrailingLimit(q)
		if !ok || got != want {
			t.Errorf("TrailingLimit(%q) = (%d,%v), want (%d,true)", q, got, ok, want)
		}
	}
}

func TestRowOffsetCaveat(t *testing.T) {
	c := RowOffsetCaveat(100)
	if c.Kind != QualityWithheld {
		t.Errorf("kind = %q, want %q", c.Kind, QualityWithheld)
	}
	for _, want := range []string{"skipped the first 100", "a page", "never about the population"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail does not say %q: %s", want, c.Detail)
		}
	}
}

// T-SQL and Oracle put the offset BEFORE the cap, so the trailing pattern cannot
// see it: `ORDER BY x OFFSET 100 ROWS FETCH NEXT 100 ROWS ONLY`.
func TestOffsetRows(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"SELECT * FROM t ORDER BY x OFFSET 100 ROWS FETCH NEXT 100 ROWS ONLY": {100, true},
		"select * from t order by x offset 25 row fetch next 10 rows only":    {25, true},
		"SELECT * FROM t ORDER BY x OFFSET 0 ROWS FETCH NEXT 50 ROWS ONLY":    {0, false},
		"SELECT * FROM t FETCH FIRST 10 ROWS ONLY":                            {0, false},
		"SELECT TOP 10 * FROM t":                                              {0, false},
	}
	for q, want := range cases {
		got, ok := OffsetRows(q)
		if got != want.want || ok != want.ok {
			t.Errorf("OffsetRows(%q) = (%d,%v), want (%d,%v)", q, got, ok, want.want, want.ok)
		}
	}
}

func TestAnyRowOffset(t *testing.T) {
	// Either form, whichever the dialect renders.
	if n, ok := AnyRowOffset("SELECT * FROM t LIMIT 10 OFFSET 30", TrailingOffset, OffsetRows); !ok || n != 30 {
		t.Errorf("trailing form = (%d,%v), want (30,true)", n, ok)
	}
	if n, ok := AnyRowOffset("SELECT * FROM t ORDER BY x OFFSET 30 ROWS FETCH NEXT 10 ROWS ONLY", TrailingOffset, OffsetRows); !ok || n != 30 {
		t.Errorf("rows form = (%d,%v), want (30,true)", n, ok)
	}
	if _, ok := AnyRowOffset("SELECT * FROM t", TrailingOffset, OffsetRows); ok {
		t.Error("an unpaginated statement must report no offset")
	}
}
