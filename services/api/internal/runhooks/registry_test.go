package runhooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetRegistry(t *testing.T) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
}

func TestRegister_PopulatesRegistryInOrder(t *testing.T) {
	resetRegistry(t)

	Register("first", func(context.Context, RunCompletion) error { return nil })
	Register("second", func(context.Context, RunCompletion) error { return nil })
	Register("third", func(context.Context, RunCompletion) error { return nil })

	got := Names()
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("Names returned %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Names[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegister_EmptyNamePanics(t *testing.T) {
	resetRegistry(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for empty name, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "empty name") {
			t.Fatalf("expected empty-name panic message, got %v", r)
		}
	}()
	Register("", func(context.Context, RunCompletion) error { return nil })
}

func TestRegister_NilHookPanics(t *testing.T) {
	resetRegistry(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil hook, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "nil hook") {
			t.Fatalf("expected nil-hook panic message, got %v", r)
		}
	}()
	Register("nil-hook", nil)
}

func TestRegister_DuplicateNamePanics(t *testing.T) {
	resetRegistry(t)
	Register("dup", func(context.Context, RunCompletion) error { return nil })
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate name, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "already registered") {
			t.Fatalf("expected already-registered panic message, got %v", r)
		}
		// First registration is preserved.
		names := Names()
		if len(names) != 1 || names[0] != "dup" {
			t.Fatalf("registry mutated after panic: %v", names)
		}
	}()
	Register("dup", func(context.Context, RunCompletion) error { return nil })
}

func TestFire_ReturnsOneResultPerHook(t *testing.T) {
	resetRegistry(t)
	calls := []string{}
	var mu sync.Mutex
	makeHook := func(name string) Hook {
		return func(context.Context, RunCompletion) error {
			mu.Lock()
			calls = append(calls, name)
			mu.Unlock()
			return nil
		}
	}
	Register("a", makeHook("a"))
	Register("b", makeHook("b"))
	Register("c", makeHook("c"))

	results := Fire(context.Background(), RunCompletion{RunID: "r1"})

	if len(results) != 3 {
		t.Fatalf("Fire returned %d results, want 3", len(results))
	}
	wantNames := []string{"a", "b", "c"}
	for i, res := range results {
		if res.Name != wantNames[i] {
			t.Errorf("results[%d].Name = %q, want %q", i, res.Name, wantNames[i])
		}
		if res.Err != nil {
			t.Errorf("results[%d].Err = %v, want nil", i, res.Err)
		}
	}
	if len(calls) != 3 || calls[0] != "a" || calls[1] != "b" || calls[2] != "c" {
		t.Fatalf("hooks invoked out of registration order: %v", calls)
	}
}

func TestFire_PassesCompletionPayloadToHook(t *testing.T) {
	resetRegistry(t)
	want := RunCompletion{
		RunID:       "run-42",
		ProjectID:   "proj-7",
		Status:      "completed",
		CompletedAt: time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC),
		Error:       "",
	}
	var got RunCompletion
	Register("capture", func(_ context.Context, r RunCompletion) error {
		got = r
		return nil
	})
	Fire(context.Background(), want)
	if got != want {
		t.Fatalf("hook received %+v, want %+v", got, want)
	}
}

func TestFire_CapturesErrors(t *testing.T) {
	resetRegistry(t)
	wantErr := errors.New("downstream service down")
	Register("ok", func(context.Context, RunCompletion) error { return nil })
	Register("fail", func(context.Context, RunCompletion) error { return wantErr })
	Register("ok2", func(context.Context, RunCompletion) error { return nil })

	results := Fire(context.Background(), RunCompletion{RunID: "r2"})

	if len(results) != 3 {
		t.Fatalf("Fire returned %d results, want 3", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("results[0].Err = %v, want nil", results[0].Err)
	}
	if !errors.Is(results[1].Err, wantErr) {
		t.Errorf("results[1].Err = %v, want %v", results[1].Err, wantErr)
	}
	if results[2].Err != nil {
		t.Errorf("results[2].Err = %v, want nil — peer failure must not stop sibling hooks", results[2].Err)
	}
}

func TestFire_RecoversPanic(t *testing.T) {
	resetRegistry(t)
	Register("panicker", func(context.Context, RunCompletion) error {
		panic("kaboom")
	})
	var ran bool
	Register("after-panic", func(context.Context, RunCompletion) error {
		ran = true
		return nil
	})

	results := Fire(context.Background(), RunCompletion{RunID: "r3"})

	if len(results) != 2 {
		t.Fatalf("Fire returned %d results, want 2", len(results))
	}
	if results[0].Err == nil {
		t.Fatal("expected panic to surface as an error result")
	}
	if !strings.Contains(results[0].Err.Error(), "panicked") || !strings.Contains(results[0].Err.Error(), "kaboom") {
		t.Errorf("panic error = %v, want a panic-tagged message containing the panic value", results[0].Err)
	}
	if !strings.Contains(results[0].Err.Error(), "panicker") {
		t.Errorf("panic error = %v, want the hook name embedded for diagnosis", results[0].Err)
	}
	if !ran {
		t.Error("post-panic hook did not run — panic must not abort sibling hooks")
	}
}

func TestFire_NoHooksReturnsEmptySlice(t *testing.T) {
	resetRegistry(t)
	got := Fire(context.Background(), RunCompletion{RunID: "r4"})
	if got == nil {
		t.Fatal("Fire returned nil — expected an empty slice for explicit no-op semantics")
	}
	if len(got) != 0 {
		t.Fatalf("Fire returned %d results, want 0", len(got))
	}
}

func TestFire_RespectsContextValueDelivery(t *testing.T) {
	resetRegistry(t)
	type ctxKey struct{}
	var seen any
	Register("ctx-reader", func(ctx context.Context, _ RunCompletion) error {
		seen = ctx.Value(ctxKey{})
		return nil
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "expected-value")
	Fire(ctx, RunCompletion{RunID: "r5"})
	if seen != "expected-value" {
		t.Fatalf("hook received context value %v, want %q", seen, "expected-value")
	}
}

func TestHasRegistered(t *testing.T) {
	resetRegistry(t)
	if HasRegistered() {
		t.Fatal("HasRegistered = true with empty registry")
	}
	Register("only", func(context.Context, RunCompletion) error { return nil })
	if !HasRegistered() {
		t.Fatal("HasRegistered = false after a registration")
	}
	ResetForTest()
	if HasRegistered() {
		t.Fatal("HasRegistered = true after ResetForTest")
	}
}

func TestNames_EmptyWhenNoRegistrations(t *testing.T) {
	resetRegistry(t)
	names := Names()
	if len(names) != 0 {
		t.Fatalf("Names returned %v on empty registry, want empty slice", names)
	}
}

func TestNames_CopyIsIndependent(t *testing.T) {
	resetRegistry(t)
	Register("a", func(context.Context, RunCompletion) error { return nil })
	first := Names()
	first[0] = "tampered"
	again := Names()
	if again[0] != "a" {
		t.Fatalf("Names returned a slice aliased to registry storage: got %q after caller mutation", again[0])
	}
}

func TestResetForTest_ClearsBetweenTestRuns(t *testing.T) {
	ResetForTest()
	Register("leaks", func(context.Context, RunCompletion) error { return nil })
	ResetForTest()
	if HasRegistered() {
		t.Fatal("ResetForTest did not clear the registry")
	}
}

func TestRegister_ConcurrentRegistrationsAllSurvive(t *testing.T) {
	resetRegistry(t)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Register(fmt.Sprintf("hook-%02d", i), func(context.Context, RunCompletion) error { return nil })
		}(i)
	}
	wg.Wait()
	if got := len(Names()); got != n {
		t.Fatalf("Names returned %d entries after concurrent registration, want %d", got, n)
	}
}

func TestFire_ConcurrentCallsAreSafe(t *testing.T) {
	resetRegistry(t)
	var calls int64
	Register("counter", func(context.Context, RunCompletion) error {
		atomic.AddInt64(&calls, 1)
		return nil
	})
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Fire(context.Background(), RunCompletion{RunID: "r"})
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&calls); got != n {
		t.Fatalf("hook invoked %d times, want %d", got, n)
	}
}
