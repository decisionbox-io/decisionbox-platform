package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestApplyListTablesFilters_NoFiltersReturnsInputUnchanged(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	in := []string{"a", "b", "c"}
	out, err := ApplyListTablesFilters(context.Background(), "proj", "ds", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("output drifted: got %v, want %v", out, in)
	}
}

func TestRegisterListTablesFilter_NilFnPanics(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterListTablesFilter with nil fn must panic")
		}
	}()
	RegisterListTablesFilter("name", nil)
}

func TestRegisterListTablesFilter_EmptyNamePanics(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterListTablesFilter with empty name must panic")
		}
	}()
	RegisterListTablesFilter("", func(_ context.Context, _, _ string, t []string) ([]string, error) { return t, nil })
}

func TestRegisterListTablesFilter_DoubleRegisterPanics(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	RegisterListTablesFilter("scope", func(_ context.Context, _, _ string, t []string) ([]string, error) { return t, nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("re-registering the same filter name must panic")
		}
	}()
	RegisterListTablesFilter("scope", func(_ context.Context, _, _ string, t []string) ([]string, error) { return t, nil })
}

func TestApplyListTablesFilters_OrderPreservedAcrossFilters(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	// First filter strips "x"; second filter strips "y". Result should
	// contain neither.
	RegisterListTablesFilter("strip-x", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		out := in[:0:0]
		for _, t := range in {
			if t != "x" {
				out = append(out, t)
			}
		}
		return out, nil
	})
	RegisterListTablesFilter("strip-y", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		out := in[:0:0]
		for _, t := range in {
			if t != "y" {
				out = append(out, t)
			}
		}
		return out, nil
	})

	got, err := ApplyListTablesFilters(context.Background(), "proj", "ds", []string{"a", "x", "b", "y", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter chain output = %v, want %v", got, want)
	}
}

func TestApplyListTablesFilters_FilterErrorAborts(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	wantErr := errors.New("scope misconfigured")
	RegisterListTablesFilter("ok-first", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		return in, nil
	})
	RegisterListTablesFilter("boom", func(_ context.Context, _, _ string, _ []string) ([]string, error) {
		return nil, wantErr
	})
	called := 0
	RegisterListTablesFilter("never-called", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		called++
		return in, nil
	})

	out, err := ApplyListTablesFilters(context.Background(), "p", "d", []string{"a"})
	if out != nil {
		t.Fatalf("on error, output must be nil; got %v", out)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error not wrapped; got %v, want wrap of %v", err, wantErr)
	}
	if called != 0 {
		t.Fatalf("filter after a failing one must not be invoked; got called=%d", called)
	}
}

func TestApplyListTablesFilters_PassesProjectAndDataset(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	var gotProject, gotDataset string
	RegisterListTablesFilter("spy", func(_ context.Context, projectID, dataset string, in []string) ([]string, error) {
		gotProject = projectID
		gotDataset = dataset
		return in, nil
	})

	if _, err := ApplyListTablesFilters(context.Background(), "proj-42", "ds-7", []string{"x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotProject != "proj-42" || gotDataset != "ds-7" {
		t.Fatalf("filter got (%q, %q), want (%q, %q)", gotProject, gotDataset, "proj-42", "ds-7")
	}
}

func TestApplyListTablesFilters_FiltersChainedReceivePreviousOutput(t *testing.T) {
	resetListTablesFiltersForTest()
	defer resetListTablesFiltersForTest()

	var secondGot []string
	RegisterListTablesFilter("first", func(_ context.Context, _, _ string, _ []string) ([]string, error) {
		return []string{"only-survivor"}, nil
	})
	RegisterListTablesFilter("second", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		secondGot = in
		return in, nil
	})

	if _, err := ApplyListTablesFilters(context.Background(), "p", "d", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(secondGot, []string{"only-survivor"}) {
		t.Fatalf("second filter received %v, want pipeline output [only-survivor]", secondGot)
	}
}
