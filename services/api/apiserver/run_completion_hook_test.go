package apiserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/internal/runhooks"
)

func TestRegisterRunCompletionHook_DelegatesToRegistry(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	called := false
	RegisterRunCompletionHook("my-hook", func(context.Context, RunCompletion) error {
		called = true
		return nil
	})

	if !HasRegisteredRunCompletionHook() {
		t.Fatal("HasRegisteredRunCompletionHook = false after registration")
	}
	results := runhooks.Fire(context.Background(), RunCompletion{RunID: "r1"})
	if len(results) != 1 || results[0].Name != "my-hook" {
		t.Fatalf("Fire results = %+v, want one entry named my-hook", results)
	}
	if !called {
		t.Fatal("registered hook did not run when Fire was called")
	}
}

func TestRegisterRunCompletionHook_ErrorPropagatesThroughWrapper(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	want := errors.New("upstream broke")
	RegisterRunCompletionHook("err-hook", func(context.Context, RunCompletion) error {
		return want
	})

	results := runhooks.Fire(context.Background(), RunCompletion{RunID: "r1"})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !errors.Is(results[0].Err, want) {
		t.Fatalf("results[0].Err = %v, want %v", results[0].Err, want)
	}
}

func TestRegisterRunCompletionHook_NilPanics(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil hook")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "nil hook") {
			t.Fatalf("expected nil-hook panic message, got %v", r)
		}
	}()
	RegisterRunCompletionHook("nil-hook", nil)
}

func TestRegisterRunCompletionHook_DuplicatePanics(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	RegisterRunCompletionHook("dup", func(context.Context, RunCompletion) error { return nil })
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate name")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "already registered") {
			t.Fatalf("expected duplicate-name panic message, got %v", r)
		}
	}()
	RegisterRunCompletionHook("dup", func(context.Context, RunCompletion) error { return nil })
}

func TestRegisterRunCompletionHook_EmptyNamePanics(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for empty name")
		}
	}()
	RegisterRunCompletionHook("", func(context.Context, RunCompletion) error { return nil })
}

func TestHasRegisteredRunCompletionHook_DefaultFalse(t *testing.T) {
	t.Cleanup(ResetRunCompletionHooksForTest)
	ResetRunCompletionHooksForTest()

	if HasRegisteredRunCompletionHook() {
		t.Fatal("HasRegisteredRunCompletionHook = true with empty registry")
	}
}

func TestResetRunCompletionHooksForTest_ClearsRegistry(t *testing.T) {
	ResetRunCompletionHooksForTest()
	RegisterRunCompletionHook("temp", func(context.Context, RunCompletion) error { return nil })
	ResetRunCompletionHooksForTest()
	if HasRegisteredRunCompletionHook() {
		t.Fatal("registry not cleared by ResetRunCompletionHooksForTest")
	}
}
