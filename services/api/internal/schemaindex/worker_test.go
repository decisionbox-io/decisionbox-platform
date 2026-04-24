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
