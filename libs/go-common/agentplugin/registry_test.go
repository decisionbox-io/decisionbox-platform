package agentplugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGetAllContextProviders_EmptyByDefault(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if got := GetAllContextProviders(); len(got) != 0 {
		t.Fatalf("expected empty registry, got %d providers", len(got))
	}
}

func TestRegisterContextProvider_NilPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterContextProvider(nil) should panic")
		}
	}()
	RegisterContextProvider(nil)
}

func TestRegisterContextProvider_EmptyNamePanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterContextProvider with empty name should panic")
		}
	}()
	RegisterContextProvider(ContextProviderFunc{ProviderName: "", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }})
}

func TestRegisterContextProvider_DoubleRegisterPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "knowledge", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("re-registering the same name should panic")
		}
	}()
	RegisterContextProvider(ContextProviderFunc{ProviderName: "knowledge", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }})
}

func TestReplaceContextProvider_NilPanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ReplaceContextProvider(nil) must panic")
		}
	}()
	ReplaceContextProvider(nil)
}

func TestReplaceContextProvider_EmptyNamePanics(t *testing.T) {
	resetForTest()
	defer resetForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ReplaceContextProvider with empty name must panic")
		}
	}()
	ReplaceContextProvider(ContextProviderFunc{ProviderName: "", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }})
}

func TestReplaceContextProvider_AddsWhenAbsent(t *testing.T) {
	resetForTest()
	defer resetForTest()

	ReplaceContextProvider(ContextProviderFunc{ProviderName: "fresh", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "x", nil }})

	provs := GetAllContextProviders()
	if len(provs) != 1 || provs[0].Name() != "fresh" {
		t.Fatalf("Replace on empty registry should append; got %v", providerNamesIn(provs))
	}
}

func TestReplaceContextProvider_SwapsKeepingOrder(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "first", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "A", nil }})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "knowledge", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "DEFAULT", nil }})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "third", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "C", nil }})

	ReplaceContextProvider(ContextProviderFunc{ProviderName: "knowledge", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "OVERRIDE", nil }})

	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, nil)
	want := "A\n\nOVERRIDE\n\nC\n"
	if got != want {
		t.Fatalf("Replace did not preserve slot order:\n got=%q\nwant=%q", got, want)
	}
	provs := GetAllContextProviders()
	if len(provs) != 3 || provs[1].Name() != "knowledge" {
		t.Fatalf("Replace should keep slot count and position; got %v", providerNamesIn(provs))
	}
}

func providerNamesIn(provs []ContextProvider) []string {
	out := make([]string, len(provs))
	for i, p := range provs {
		out[i] = p.Name()
	}
	return out
}

func TestRegisterContextProvider_OrderPreserved(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "first", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "A", nil }})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "second", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "B", nil }})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "third", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "C", nil }})

	got := GetAllContextProviders()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name() != "first" || got[1].Name() != "second" || got[2].Name() != "third" {
		t.Fatalf("registration order not preserved: got %s, %s, %s", got[0].Name(), got[1].Name(), got[2].Name())
	}
}

func TestGetAllContextProviders_ReturnsCopy(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "alpha", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }})

	first := GetAllContextProviders()
	first[0] = ContextProviderFunc{ProviderName: "tampered", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) { return "", nil }}

	second := GetAllContextProviders()
	if second[0].Name() != "alpha" {
		t.Fatalf("internal slice was mutated by caller; got %q want %q", second[0].Name(), "alpha")
	}
}

func TestRenderSections_Empty(t *testing.T) {
	resetForTest()
	defer resetForTest()

	if got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, nil); got != "" {
		t.Fatalf("RenderSections with no providers = %q, want empty", got)
	}
}

func TestRenderSections_ConcatenatesInOrder(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "first", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## A\nbody-a", nil
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "second", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## B\nbody-b", nil
	}})

	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, nil)
	want := "## A\nbody-a\n\n## B\nbody-b\n"
	if got != want {
		t.Fatalf("RenderSections =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderSections_DropsEmptySections(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "first", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "", nil
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "second", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## B\nbody-b", nil
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "third", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "   \n\n", nil
	}})

	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, nil)
	want := "## B\nbody-b\n"
	if got != want {
		t.Fatalf("RenderSections =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderSections_OneProviderErrorDoesNotSuppressOthers(t *testing.T) {
	resetForTest()
	defer resetForTest()

	wantErr := errors.New("boom")
	var capturedName string
	var capturedErr error

	RegisterContextProvider(ContextProviderFunc{ProviderName: "first", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## A\nbody-a", nil
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "second", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "should-be-suppressed", wantErr
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "third", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## C\nbody-c", nil
	}})

	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, func(name string, err error) {
		capturedName = name
		capturedErr = err
	})

	if !strings.Contains(got, "## A") || !strings.Contains(got, "## C") {
		t.Fatalf("expected both healthy sections, got %q", got)
	}
	if strings.Contains(got, "should-be-suppressed") {
		t.Fatalf("errored provider's body must not appear, got %q", got)
	}
	if capturedName != "second" || !errors.Is(capturedErr, wantErr) {
		t.Fatalf("onError callback got (%q, %v), want (%q, %v)", capturedName, capturedErr, "second", wantErr)
	}
}

func TestRenderSections_PanickingProviderIsContained(t *testing.T) {
	resetForTest()
	defer resetForTest()

	var capturedName string
	var capturedErr error

	RegisterContextProvider(ContextProviderFunc{ProviderName: "panicker", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		panic("provider exploded")
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "healthy", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## OK\nstill rendered", nil
	}})

	got := RenderSections(context.Background(), "p1", "q", ContextProviderOpts{}, func(name string, err error) {
		capturedName = name
		capturedErr = err
	})

	if !strings.Contains(got, "still rendered") {
		t.Fatalf("healthy provider must still render after panic; got %q", got)
	}
	if capturedName != "panicker" || capturedErr == nil {
		t.Fatalf("onError got (%q, %v), want panic surfaced for panicker", capturedName, capturedErr)
	}
	if !strings.Contains(capturedErr.Error(), "panicked") {
		t.Fatalf("recovered error should mention panic; got %v", capturedErr)
	}
}

func TestRenderSections_NilOnErrorIsSafe(t *testing.T) {
	resetForTest()
	defer resetForTest()

	RegisterContextProvider(ContextProviderFunc{ProviderName: "boom", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "", errors.New("nope")
	}})
	RegisterContextProvider(ContextProviderFunc{ProviderName: "ok", Fn: func(_ context.Context, _, _ string, _ ContextProviderOpts) (string, error) {
		return "## OK\nfine", nil
	}})

	if got := RenderSections(context.Background(), "p", "q", ContextProviderOpts{}, nil); !strings.Contains(got, "fine") {
		t.Fatalf("nil onError should still render successful sections; got %q", got)
	}
}

func TestContextProviderFunc_NilFnReturnsEmpty(t *testing.T) {
	p := ContextProviderFunc{ProviderName: "x"}
	got, err := p.Section(context.Background(), "proj", "q", ContextProviderOpts{})
	if err != nil || got != "" {
		t.Fatalf("nil Fn should be a no-op, got (%q, %v)", got, err)
	}
	if p.Name() != "x" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "x")
	}
}

func TestRenderSections_PassesProjectAndQuery(t *testing.T) {
	resetForTest()
	defer resetForTest()

	var gotProject, gotQuery string
	var gotOpts ContextProviderOpts
	RegisterContextProvider(ContextProviderFunc{ProviderName: "spy", Fn: func(_ context.Context, projectID, query string, opts ContextProviderOpts) (string, error) {
		gotProject = projectID
		gotQuery = query
		gotOpts = opts
		return "", nil
	}})

	_ = RenderSections(context.Background(), "proj-42", "the-query", ContextProviderOpts{Limit: 7, MinScore: 0.42}, nil)

	if gotProject != "proj-42" || gotQuery != "the-query" {
		t.Fatalf("provider got (%q, %q), want (%q, %q)", gotProject, gotQuery, "proj-42", "the-query")
	}
	if gotOpts.Limit != 7 || gotOpts.MinScore != 0.42 {
		t.Fatalf("provider got opts %+v, want Limit=7 MinScore=0.42", gotOpts)
	}
}

// optsRecorder captures the options one provider was handed.
type optsRecorder struct {
	name string
	got  ContextProviderOpts
}

func (r *optsRecorder) Name() string { return r.name }

func (r *optsRecorder) Section(_ context.Context, _ string, _ string, opts ContextProviderOpts) (string, error) {
	r.got = opts
	return "section from " + r.name, nil
}

// TestRenderSectionsPassesOptsThroughVerbatim pins the per-provider budget
// contract: every registered provider is handed exactly the options the caller
// set, with nothing redistributed or divided between them.
//
// The whole fix for providers competing over one shared Limit is that each gets
// its own budget, so a renderer that quietly split or rewrote TokenBudget would
// restore the defect while every other test still passed.
func TestRenderSectionsPassesOptsThroughVerbatim(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)

	a := &optsRecorder{name: "provider-a"}
	b := &optsRecorder{name: "provider-b"}
	RegisterContextProvider(a)
	RegisterContextProvider(b)

	want := ContextProviderOpts{
		Limit:       5,
		MinScore:    0.4,
		TokenBudget: 9000,
		ExcludeIDs:  []string{"f1", "f2"},
	}
	out := RenderSections(context.Background(), "proj-1", "a query", want, nil)
	if out == "" {
		t.Fatal("expected both sections")
	}

	for _, r := range []*optsRecorder{a, b} {
		if r.got.TokenBudget != want.TokenBudget {
			t.Errorf("%s got TokenBudget %d, want %d — the budget is per provider, not a pool to divide",
				r.name, r.got.TokenBudget, want.TokenBudget)
		}
		if r.got.Limit != want.Limit || r.got.MinScore != want.MinScore {
			t.Errorf("%s got Limit=%d MinScore=%v, want %d/%v", r.name, r.got.Limit, r.got.MinScore, want.Limit, want.MinScore)
		}
		if len(r.got.ExcludeIDs) != len(want.ExcludeIDs) {
			t.Fatalf("%s got %d ExcludeIDs, want %d", r.name, len(r.got.ExcludeIDs), len(want.ExcludeIDs))
		}
		for i := range want.ExcludeIDs {
			if r.got.ExcludeIDs[i] != want.ExcludeIDs[i] {
				t.Errorf("%s ExcludeIDs[%d] = %q, want %q", r.name, i, r.got.ExcludeIDs[i], want.ExcludeIDs[i])
			}
		}
	}
}

// TestRenderSectionsZeroBudgetIsNotSilence pins the back-compatible reading of
// an unset TokenBudget: a caller that supplied no budget must get the
// behaviour it got before the field existed, which is bounded by Limit.
//
// Reading zero as "carry nothing" would silently empty every prompt assembled
// by a caller that has not been taught the new field — including the /ask path
// and any plugin calling RenderSections directly.
func TestRenderSectionsZeroBudgetIsNotSilence(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)

	rec := &optsRecorder{name: "legacy-caller-provider"}
	RegisterContextProvider(rec)

	out := RenderSections(context.Background(), "proj-1", "a query", ContextProviderOpts{Limit: 3, MinScore: 0.4}, nil)
	if out == "" {
		t.Fatal("a caller that set only Limit must still get its section")
	}
	if rec.got.TokenBudget != 0 {
		t.Errorf("TokenBudget = %d, want 0 — an unsupplied budget must not be invented", rec.got.TokenBudget)
	}
	if rec.got.Limit != 3 {
		t.Errorf("Limit = %d, want 3", rec.got.Limit)
	}
}
