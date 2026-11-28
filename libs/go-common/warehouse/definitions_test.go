package warehouse

import (
	"context"
	"testing"
)

// defsProvider carries business definitions and builds sample queries.
type defsProvider struct{ Provider }

func (defsProvider) BusinessDefinitions(context.Context) ([]BusinessDefinition, error) {
	return []BusinessDefinition{{Name: "Revenue", Kind: DefinitionMeasure, Expression: "Sum(Amount)"}}, nil
}

func (defsProvider) SampleQuery(_, table, _ string, limit int) string { return table }

// Definitions and the sample builder run nothing, so they are reached through
// middleware exactly like the row-cap inspector — and not through a wrapper
// that hides what it wraps.
func TestCapabilitiesAreFoundThroughMiddleware(t *testing.T) {
	inner := defsProvider{}
	for name, p := range map[string]Provider{
		"direct":    inner,
		"one layer": unwrappingWrapper{inner: inner},
		"two":       unwrappingWrapper{inner: unwrappingWrapper{inner: inner}},
	} {
		if _, ok := AsDefinitionSource(p); !ok {
			t.Errorf("%s: definitions not found", name)
		}
		if _, ok := SampleQueryBuilderOf(p); !ok {
			t.Errorf("%s: sample builder not found", name)
		}
	}
	if _, ok := AsDefinitionSource(opaqueWrapper{Provider: inner}); ok {
		t.Error("an opaque wrapper must not claim a capability it hides")
	}
	if _, ok := FindCapability[DefinitionSource](selfWrapper{}); ok {
		t.Error("a self-returning wrapper must end the search, not spin")
	}
	if _, ok := FindCapability[DefinitionSource](nil); ok {
		t.Error("nil has no capabilities")
	}
	if _, ok := AsDefinitionSource(capProvider{}); ok {
		t.Error("a provider without definitions reports none")
	}
}

// sliceWrapper is a value-typed wrapper whose value holds a slice: comparing
// two of them panics, so the search must never compare wrappers by value.
type sliceWrapper struct {
	Provider
	inner Provider
	tags  []string
}

func (w sliceWrapper) Unwrap() Provider { return w.inner }

// loopWrapper is a pointer wrapper; two of them can be wired into a cycle.
type loopWrapper struct {
	Provider
	next Provider
}

func (w *loopWrapper) Unwrap() Provider { return w.next }

func TestFindCapability_SafeOnAnyWrapper(t *testing.T) {
	chain := sliceWrapper{inner: sliceWrapper{inner: defsProvider{}, tags: []string{"b"}}, tags: []string{"a"}}
	if _, ok := AsDefinitionSource(chain); !ok {
		t.Error("definitions not found through value wrappers holding slices")
	}
	a, b := &loopWrapper{}, &loopWrapper{}
	a.next, b.next = b, a
	if _, ok := AsDefinitionSource(a); ok {
		t.Error("a cycle of wrappers must end the search with nothing found")
	}
}

func TestIsDefinitionKind(t *testing.T) {
	for kind, want := range map[string]bool{
		DefinitionMeasure: true, DefinitionDimension: true,
		ItemKindTable: false, ItemKindMetric: false, ItemKindDimension: false,
	} {
		if IsDefinitionKind(kind) != want {
			t.Errorf("IsDefinitionKind(%q) = %v", kind, !want)
		}
	}
}

const guidedSourceSlug = "test-guided-source"

// Registered once per process: the registry refuses a second registration, so
// a test that registered it would fail under -count=2.
func init() {
	RegisterWithMeta(guidedSourceSlug, func(ProviderConfig) (Provider, error) { return nil, nil }, ProviderMeta{
		Capability: Capability{QueryLanguage: "Request (JSON)", QueryGuide: "Write one JSON object."},
	})
}

func TestQueryGuideOf(t *testing.T) {
	if got := QueryGuideOf(guidedSourceSlug); got != "Write one JSON object." {
		t.Errorf("QueryGuideOf = %q", got)
	}
	if got := QueryGuideOf("not-registered"); got != "" {
		t.Errorf("an unregistered slug has no guide, got %q", got)
	}
}
