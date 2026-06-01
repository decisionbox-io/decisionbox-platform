package systeminfo

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestRegisterAndCollect(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "API", Kind: KindService, Version: "1.2.3"})
	Register(Descriptor{Name: "Schema indexing", Kind: KindWorker, RunsIn: "API", Version: "1.2.3", Note: "shares the API image"})

	got := Collect(context.Background())
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	// Services sort before workers.
	if got[0].Name != "API" || got[1].Name != "Schema indexing" {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got[1].RunsIn != "API" || got[1].Note == "" {
		t.Fatalf("worker descriptor not preserved: %+v", got[1])
	}
}

func TestRegisterReplacesByName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "API", Kind: KindService, Version: "1.0.0"})
	Register(Descriptor{Name: "API", Kind: KindService, Version: "2.0.0"})

	got := Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (re-registration must replace, not append)", len(got))
	}
	if got[0].Version != "2.0.0" {
		t.Fatalf("version = %q, want 2.0.0 (last registration wins)", got[0].Version)
	}
}

func TestCollectSortsServicesBeforeWorkersThenByName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "Validation jobs", Kind: KindWorker})
	Register(Descriptor{Name: "Schema indexing", Kind: KindWorker})
	Register(Descriptor{Name: "Dashboard", Kind: KindService})
	Register(Descriptor{Name: "API", Kind: KindService})

	got := Collect(context.Background())
	want := []string{"API", "Dashboard", "Schema indexing", "Validation jobs"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("order[%d] = %q, want %q (full: %+v)", i, got[i].Name, name, got)
		}
	}
}

func TestUnknownKindSortsLast(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "Mystery", Kind: Kind("plugin")})
	Register(Descriptor{Name: "API", Kind: KindService})
	Register(Descriptor{Name: "Worker", Kind: KindWorker})

	got := Collect(context.Background())
	if got[len(got)-1].Name != "Mystery" {
		t.Fatalf("unknown kind should sort last, got order: %+v", got)
	}
}

func TestRegisterSourceContributesAndMerges(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "API", Kind: KindService, Version: "1.0.0"})
	RegisterSource(func(_ context.Context) ([]Descriptor, error) {
		return []Descriptor{
			{Name: "Edge worker", Kind: KindWorker, Version: "9.9.9"},
		}, nil
	})

	got := Collect(context.Background())
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (static + source): %+v", len(got), got)
	}
	var found bool
	for _, d := range got {
		if d.Name == "Edge worker" && d.Version == "9.9.9" {
			found = true
		}
	}
	if !found {
		t.Fatalf("source descriptor missing: %+v", got)
	}
}

func TestSourceOverridesStaticByName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "API", Kind: KindService, Version: "stale"})
	RegisterSource(func(_ context.Context) ([]Descriptor, error) {
		return []Descriptor{{Name: "API", Kind: KindService, Version: "live"}}, nil
	})

	got := Collect(context.Background())
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (source must override static by name)", len(got))
	}
	if got[0].Version != "live" {
		t.Fatalf("version = %q, want live (dynamic source wins)", got[0].Version)
	}
}

func TestErroringSourceIsSkippedButPartialKept(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Descriptor{Name: "API", Kind: KindService})
	// A source that returns a partial slice alongside an error: the
	// descriptors it managed to read must still be included.
	RegisterSource(func(_ context.Context) ([]Descriptor, error) {
		return []Descriptor{{Name: "Partial", Kind: KindWorker}}, errors.New("store unreachable")
	})
	// A source that blows up entirely (nil, error) must not take down
	// the inventory.
	RegisterSource(func(_ context.Context) ([]Descriptor, error) {
		return nil, errors.New("boom")
	})

	got := Collect(context.Background())
	names := map[string]bool{}
	for _, d := range got {
		names[d.Name] = true
	}
	if !names["API"] || !names["Partial"] {
		t.Fatalf("expected API + Partial despite source errors, got: %+v", got)
	}
}

func TestSourceWithEmptyNameDescriptorIgnored(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	RegisterSource(func(_ context.Context) ([]Descriptor, error) {
		return []Descriptor{{Name: "", Kind: KindService}, {Name: "Real", Kind: KindService}}, nil
	})
	got := Collect(context.Background())
	if len(got) != 1 || got[0].Name != "Real" {
		t.Fatalf("empty-name descriptor should be dropped, got: %+v", got)
	}
}

func TestCollectAlwaysNonNil(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	got := Collect(context.Background())
	if got == nil {
		t.Fatal("Collect returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	defer func() {
		if recover() == nil {
			t.Fatal("Register with empty Name should panic")
		}
	}()
	Register(Descriptor{Kind: KindService})
}

func TestRegisterNilSourcePanics(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterSource with nil Source should panic")
		}
	}()
	RegisterSource(nil)
}

// TestConcurrentRegisterAndCollect exercises the mutex under -race.
func TestConcurrentRegisterAndCollect(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			Register(Descriptor{Name: string(rune('A' + n%26)), Kind: KindService})
		}(i)
		go func() {
			defer wg.Done()
			_ = Collect(context.Background())
		}()
	}
	wg.Wait()
}
