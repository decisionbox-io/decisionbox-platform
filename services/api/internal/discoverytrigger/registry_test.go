package discoverytrigger

import (
	"context"
	"errors"
	"testing"
)

func TestRegisterAndGet(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()

	if Get() != nil {
		t.Fatal("Get() should be nil before registration")
	}

	want := Result{RunID: "r1", Status: "started"}
	Register(func(context.Context, Options) (Result, error) { return want, nil })

	fn := Get()
	if fn == nil {
		t.Fatal("Get() returned nil after Register")
	}
	got, err := fn(context.Background(), Options{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("trigger returned error: %v", err)
	}
	if got != want {
		t.Fatalf("trigger result = %+v, want %+v", got, want)
	}
}

func TestRegisterLastWins(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()

	Register(func(context.Context, Options) (Result, error) { return Result{RunID: "first"}, nil })
	Register(func(context.Context, Options) (Result, error) { return Result{RunID: "second"}, nil })

	got, _ := Get()(context.Background(), Options{})
	if got.RunID != "second" {
		t.Fatalf("RunID = %q, want second (last-wins)", got.RunID)
	}
}

func TestRegisterNilPanics(t *testing.T) {
	t.Cleanup(ResetForTest)
	ResetForTest()

	defer func() {
		if recover() == nil {
			t.Fatal("Register(nil) should panic")
		}
	}()
	Register(nil)
}

func TestTypedErrors(t *testing.T) {
	// ErrProjectNotFound is a sentinel usable with errors.Is.
	if !errors.Is(ErrProjectNotFound, ErrProjectNotFound) {
		t.Fatal("errors.Is should match ErrProjectNotFound")
	}

	// The struct errors are usable with errors.As and carry payloads.
	var conflict *ConflictError
	if !errors.As(error(&ConflictError{Message: "nope"}), &conflict) || conflict.Message != "nope" {
		t.Fatal("errors.As should extract *ConflictError with its message")
	}

	var running *AlreadyRunningError
	if !errors.As(error(&AlreadyRunningError{RunID: "r9"}), &running) || running.RunID != "r9" {
		t.Fatal("errors.As should extract *AlreadyRunningError with its run id")
	}

	var invalid *InvalidParamsError
	if !errors.As(error(&InvalidParamsError{Message: "bad"}), &invalid) || invalid.Message != "bad" {
		t.Fatal("errors.As should extract *InvalidParamsError with its message")
	}
}
