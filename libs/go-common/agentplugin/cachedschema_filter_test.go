package agentplugin

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestApplyCachedSchemaFilters_NoFilters_PassesThroughUnchanged(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	in := []string{"a.x", "a.y", "b.z"}
	got, err := ApplyCachedSchemaFilters(context.Background(), "p", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("got=%v, want=%v (input must be returned unchanged)", got, in)
	}
}

func TestRegisterCachedSchemaFilter_PanicsOnEmptyName(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name, got none")
		}
	}()
	RegisterCachedSchemaFilter("", func(context.Context, string, []string) ([]string, error) {
		return nil, nil
	})
}

func TestRegisterCachedSchemaFilter_PanicsOnNilFn(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil fn, got none")
		}
	}()
	RegisterCachedSchemaFilter("name", nil)
}

func TestRegisterCachedSchemaFilter_PanicsOnDuplicateName(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	noop := func(context.Context, string, []string) ([]string, error) { return nil, nil }
	RegisterCachedSchemaFilter("dup", noop)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate name, got none")
		}
	}()
	RegisterCachedSchemaFilter("dup", noop)
}

func TestApplyCachedSchemaFilters_ChainPreservesOrder(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	// First drops "a.x"; second drops "b.z". The chain runs in
	// registration order, so the second receives the first's output.
	RegisterCachedSchemaFilter("drop-x", func(_ context.Context, _ string, in []string) ([]string, error) {
		out := make([]string, 0, len(in))
		for _, t := range in {
			if t != "a.x" {
				out = append(out, t)
			}
		}
		return out, nil
	})
	RegisterCachedSchemaFilter("drop-z", func(_ context.Context, _ string, in []string) ([]string, error) {
		out := make([]string, 0, len(in))
		for _, t := range in {
			if t != "b.z" {
				out = append(out, t)
			}
		}
		return out, nil
	})
	got, err := ApplyCachedSchemaFilters(context.Background(), "p", []string{"a.x", "a.y", "b.z", "c.w"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a.y", "c.w"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v, want=%v", got, want)
	}
}

func TestApplyCachedSchemaFilters_FilterErrorAbortsChain(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	first := errors.New("boom")
	RegisterCachedSchemaFilter("err", func(context.Context, string, []string) ([]string, error) {
		return nil, first
	})
	called := false
	RegisterCachedSchemaFilter("never", func(context.Context, string, []string) ([]string, error) {
		called = true
		return nil, nil
	})
	got, err := ApplyCachedSchemaFilters(context.Background(), "p", []string{"a.x"})
	if err == nil {
		t.Fatal("expected error from filter, got none")
	}
	if !errors.Is(err, first) {
		t.Fatalf("error chain dropped wrapped err: %v", err)
	}
	if got != nil {
		t.Fatalf("got=%v on filter error, want nil", got)
	}
	if called {
		t.Fatal("subsequent filter ran after a prior filter errored — chain must abort")
	}
}

func TestApplyCachedSchemaFilters_PassesProjectID(t *testing.T) {
	defer ResetCachedSchemaFiltersForTest()
	ResetCachedSchemaFiltersForTest()
	var seenProject string
	RegisterCachedSchemaFilter("capture", func(_ context.Context, projectID string, in []string) ([]string, error) {
		seenProject = projectID
		return in, nil
	})
	if _, err := ApplyCachedSchemaFilters(context.Background(), "proj-7", []string{"x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenProject != "proj-7" {
		t.Fatalf("filter saw project=%q, want proj-7", seenProject)
	}
}
