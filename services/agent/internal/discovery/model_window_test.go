package discovery

import (
	"context"
	"testing"
)

// fakeWindowStore records SaveWindow calls for the self-calibration test.
type fakeWindowStore struct {
	saved   []savedWindow
	saveErr error
}

type savedWindow struct {
	provider string
	model    string
	window   int
}

func (f *fakeWindowStore) SaveWindow(_ context.Context, provider, model string, window int) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedWindow{provider, model, window})
	return nil
}

func TestResolveModelBudget_PrefersResolvedThenCalibrated(t *testing.T) {
	o := &Orchestrator{
		llmProvider:    "bedrock",
		llmModel:       "GLM-5",
		llmInputWindow: 131072, // resolved at run start (a guess)
		llmOutputCap:   64000,
	}
	// Before any calibration: uses the run-start resolved window.
	if w, out := o.resolveModelBudget(); w != 131072 || out != 64000 {
		t.Fatalf("pre-calibration got window=%d out=%d, want 131072/64000", w, out)
	}

	// After calibration: the learned true window wins.
	o.applyCalibratedWindow(context.Background(), "GLM-5", 202752)
	if w, _ := o.resolveModelBudget(); w != 202752 {
		t.Fatalf("post-calibration window=%d, want 202752", w)
	}
}

func TestApplyCalibratedWindow_PersistsOnceOnChange(t *testing.T) {
	store := &fakeWindowStore{}
	o := &Orchestrator{llmProvider: "bedrock", llmModel: "GLM-5", modelWindowRepo: store}

	o.applyCalibratedWindow(context.Background(), "GLM-5", 202752)
	o.applyCalibratedWindow(context.Background(), "GLM-5", 202752) // no-op repeat
	if len(store.saved) != 1 {
		t.Fatalf("expected exactly one persist on change, got %d", len(store.saved))
	}
	if store.saved[0] != (savedWindow{"bedrock", "GLM-5", 202752}) {
		t.Fatalf("persisted %+v, want bedrock/GLM-5/202752", store.saved[0])
	}

	// A new value persists again.
	o.applyCalibratedWindow(context.Background(), "GLM-5", 200000)
	if len(store.saved) != 2 || store.saved[1].window != 200000 {
		t.Fatalf("expected a second persist for the changed window, got %+v", store.saved)
	}
}

func TestApplyCalibratedWindow_IgnoresNonPositive(t *testing.T) {
	store := &fakeWindowStore{}
	o := &Orchestrator{llmProvider: "p", llmModel: "m", modelWindowRepo: store}
	o.applyCalibratedWindow(context.Background(), "m", 0)
	o.applyCalibratedWindow(context.Background(), "m", -5)
	if len(store.saved) != 0 {
		t.Fatalf("non-positive windows must not persist, got %+v", store.saved)
	}
}

func TestResolveModelBudget_FallsBackToCatalogWhenUnset(t *testing.T) {
	// No pre-resolved values and an unregistered provider → package defaults.
	o := &Orchestrator{llmProvider: "unregistered-x", llmModel: "m"}
	w, out := o.resolveModelBudget()
	if w <= 0 || out <= 0 {
		t.Fatalf("fallback must yield positive budget, got window=%d out=%d", w, out)
	}
}
