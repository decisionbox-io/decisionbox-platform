package warehouse

import "testing"

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
