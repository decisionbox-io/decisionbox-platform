package agentplugin

import (
	"context"
	"errors"
	"testing"
)

func TestApplyListTablesFilters_NoFilters_PassesThroughUnchanged(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	in := []string{"a", "b", "c"}
	got, err := ApplyListTablesFilters(context.Background(), "p", "ds", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("len(got)=%d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("got[%d]=%q, want %q", i, got[i], in[i])
		}
	}
}

func TestRegisterListTablesFilter_PanicsOnEmptyName(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name, got none")
		}
	}()
	RegisterListTablesFilter("", func(context.Context, string, string, []string) ([]string, error) {
		return nil, nil
	})
}

func TestRegisterListTablesFilter_PanicsOnNilFn(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil fn, got none")
		}
	}()
	RegisterListTablesFilter("name", nil)
}

func TestRegisterListTablesFilter_PanicsOnDuplicateName(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	noop := func(context.Context, string, string, []string) ([]string, error) { return nil, nil }
	RegisterListTablesFilter("dup", noop)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate name, got none")
		}
	}()
	RegisterListTablesFilter("dup", noop)
}

func TestApplyListTablesFilters_ChainPreservesOrder(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	// First filter drops "x"; second filter drops "y". Either order
	// must produce the same result, so we assert the union of drops.
	RegisterListTablesFilter("drop-x", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		out := make([]string, 0, len(in))
		for _, t := range in {
			if t != "x" {
				out = append(out, t)
			}
		}
		return out, nil
	})
	RegisterListTablesFilter("drop-y", func(_ context.Context, _, _ string, in []string) ([]string, error) {
		out := make([]string, 0, len(in))
		for _, t := range in {
			if t != "y" {
				out = append(out, t)
			}
		}
		return out, nil
	})
	got, err := ApplyListTablesFilters(context.Background(), "p", "ds", []string{"a", "x", "b", "y", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got=%v, want=%v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("got[%d]=%q, want %q (got=%v)", i, got[i], v, got)
		}
	}
}

func TestApplyListTablesFilters_FilterErrorAbortsChain(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	first := errors.New("boom")
	RegisterListTablesFilter("err", func(context.Context, string, string, []string) ([]string, error) {
		return nil, first
	})
	called := false
	RegisterListTablesFilter("never", func(context.Context, string, string, []string) ([]string, error) {
		called = true
		return nil, nil
	})
	got, err := ApplyListTablesFilters(context.Background(), "p", "ds", []string{"a"})
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
		t.Fatal("subsequent filter was invoked after a prior filter errored — chain must abort")
	}
}

func TestApplyListTablesFilters_PassesProjectAndDataset(t *testing.T) {
	defer ResetListTablesFiltersForTest()
	ResetListTablesFiltersForTest()
	var seenProject, seenDataset string
	RegisterListTablesFilter("capture", func(_ context.Context, projectID, dataset string, in []string) ([]string, error) {
		seenProject = projectID
		seenDataset = dataset
		return in, nil
	})
	if _, err := ApplyListTablesFilters(context.Background(), "proj-7", "ds-3", []string{"x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenProject != "proj-7" {
		t.Fatalf("filter saw project=%q, want proj-7", seenProject)
	}
	if seenDataset != "ds-3" {
		t.Fatalf("filter saw dataset=%q, want ds-3", seenDataset)
	}
}
