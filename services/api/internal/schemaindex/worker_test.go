package schemaindex

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// fakeRunner satisfies runner.Runner for the tests below. Only
// RunIndexSchema is exercised; the others are required by the
// interface but never called.
type fakeRunner struct {
	calls []runner.IndexSchemaOptions
	err   error
}

func (f *fakeRunner) Run(_ context.Context, _ runner.RunOptions) error { return nil }
func (f *fakeRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return nil, nil
}
func (f *fakeRunner) Cancel(_ context.Context, _ string) error { return nil }
func (f *fakeRunner) RunIndexSchema(_ context.Context, opts runner.IndexSchemaOptions) error {
	f.calls = append(f.calls, opts)
	return f.err
}

// New's nil-dependency checks are purely structural — they fire before
// any Mongo call, so unit tests can pass empty struct pointers.

func TestNew_RejectsMissingProjectsRepo(t *testing.T) {
	_, err := New(WorkerConfig{
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	if err == nil {
		t.Fatal("missing Projects should error")
	}
}

func TestNew_RejectsMissingProgressRepo(t *testing.T) {
	_, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Runner:   &fakeRunner{},
	})
	if err == nil {
		t.Fatal("missing Progress should error")
	}
}

func TestNew_RejectsMissingRunner(t *testing.T) {
	_, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
	})
	if err == nil {
		t.Fatal("missing Runner should error")
	}
}

func TestNew_DefaultsAppliedOnZeroPollInterval(t *testing.T) {
	w, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.cfg.PollInterval != DefaultPollInterval {
		t.Errorf("default not applied: %v", w.cfg.PollInterval)
	}
}

func TestNew_RespectsCustomPollInterval(t *testing.T) {
	w, err := New(WorkerConfig{
		Projects:     &database.ProjectRepository{},
		Progress:     &database.SchemaIndexProgressRepository{},
		Runner:       &fakeRunner{},
		PollInterval: 42, // nanoseconds — absurd but valid
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.cfg.PollInterval != 42 {
		t.Errorf("got %v, want 42ns", w.cfg.PollInterval)
	}
}

// --- cancel registry ---

func TestWorker_Cancel_UnknownProject_ReturnsFalse(t *testing.T) {
	w, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Cancel("nope") {
		t.Error("Cancel on an unknown project should return false")
	}
	if w.IsRunning("nope") {
		t.Error("IsRunning on an unknown project should return false")
	}
}

func TestWorker_Cancel_RegisteredProject_FiresFunc(t *testing.T) {
	w, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	w.register("p1", func() { fired = true })
	if !w.IsRunning("p1") {
		t.Error("IsRunning should be true after register")
	}
	if !w.Cancel("p1") {
		t.Error("Cancel should return true for a registered project")
	}
	if !fired {
		t.Error("registered cancel func was not invoked")
	}
}

func TestWorker_Cancel_DeregisterRemovesFromRegistry(t *testing.T) {
	w, err := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.register("p1", func() {})
	w.deregister("p1")
	if w.IsRunning("p1") {
		t.Error("IsRunning should be false after deregister")
	}
	if w.Cancel("p1") {
		t.Error("Cancel should miss after deregister")
	}
}

func TestWorker_Cancel_IsolationAcrossProjects(t *testing.T) {
	// Cancelling project A must not fire project B's cancel func.
	w, _ := New(WorkerConfig{
		Projects: &database.ProjectRepository{},
		Progress: &database.SchemaIndexProgressRepository{},
		Runner:   &fakeRunner{},
	})
	aFired, bFired := false, false
	w.register("A", func() { aFired = true })
	w.register("B", func() { bFired = true })

	w.Cancel("A")
	if !aFired {
		t.Error("A cancel did not fire")
	}
	if bFired {
		t.Error("B cancel fired while cancelling A")
	}
}
