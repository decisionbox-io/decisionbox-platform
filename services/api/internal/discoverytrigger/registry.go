// Package discoverytrigger owns the process-global handle to the
// discovery-run trigger. The community API server registers the concrete
// implementation (the same code path POST /api/v1/projects/{id}/discover
// runs) during server construction; in-process callers reach it through
// apiserver.TriggerDiscovery — a thin re-export of Get().
//
// This lives in its own leaf package so both the handler/server (which
// register the implementation) and apiserver (which re-exports the public
// entry point for plugins) can depend on it without an import cycle —
// mirroring the runhooks package that backs RegisterRunCompletionHook.
package discoverytrigger

import (
	"context"
	"errors"
	"sync"
)

// Options are the inputs to a discovery-run trigger. They mirror the
// optional request body of POST /api/v1/projects/{id}/discover so the
// HTTP endpoint and in-process callers share one model.
type Options struct {
	// ProjectID is the project to run discovery for (required).
	ProjectID string
	// Areas restricts discovery to these analysis areas; empty = all.
	Areas []string
	// MaxSteps overrides the exploration-step budget; 0 = agent default.
	MaxSteps int
	// MinSteps is the floor on exploration steps. nil applies the
	// default (60% of the effective MaxSteps); a non-nil 0 disables the
	// floor; >0 sets an explicit floor.
	MinSteps *int
	// Source is a free-form label for where the trigger originated
	// ("manual", "schedule:<id>", …). Used for logging/observability
	// only; it is not persisted on the run.
	Source string
	// SkipIfRunning makes the trigger enforce the per-project "one
	// discovery at a time" overlap rule via a repo-level check, returning
	// *AlreadyRunningError when a run is already in progress — regardless
	// of which policy Checker is active. In-process callers (e.g. a
	// scheduler whose overlap policy is "skip") set this so the guarantee
	// holds even under a Checker that does not itself enforce per-project
	// concurrency (the enterprise LicenseChecker is advisory). The HTTP
	// endpoint leaves it false and relies on the existing Noop/cloud paths.
	SkipIfRunning bool
}

// Result is the outcome of a successful trigger.
type Result struct {
	// RunID is the discovery_runs._id reserved for the spawned run.
	RunID string
	// Status is the trigger status; "started" on success.
	Status string
}

// ErrProjectNotFound is returned when the project does not exist. HTTP
// callers map it to 404.
var ErrProjectNotFound = errors.New("project not found")

// ConflictError reports a state precondition that blocks the run
// (project not in the ready state, schema index not ready, …). HTTP
// callers map it to 409 with Message as the body.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// AlreadyRunningError reports that a discovery is already in progress for
// the project. RunID is the in-flight run. HTTP callers map it to 409.
type AlreadyRunningError struct{ RunID string }

func (e *AlreadyRunningError) Error() string {
	return "a discovery is already running for this project"
}

// InvalidParamsError reports invalid trigger parameters (e.g. an
// out-of-range min_steps). HTTP callers map it to 400.
type InvalidParamsError struct{ Message string }

func (e *InvalidParamsError) Error() string { return e.Message }

// Func is the trigger implementation signature.
type Func func(ctx context.Context, opts Options) (Result, error)

var (
	mu sync.RWMutex
	fn Func
)

// Register installs the trigger implementation. Called once during API
// server construction. A nil implementation panics (programmer error).
// Registration is last-wins so repeated server construction in tests does
// not panic; production constructs exactly one server.
func Register(f Func) {
	if f == nil {
		panic("discoverytrigger: Register called with nil func")
	}
	mu.Lock()
	defer mu.Unlock()
	fn = f
}

// Get returns the registered trigger, or nil if none has been registered
// (e.g. a build that never constructed the API server). Callers must
// handle nil.
func Get() Func {
	mu.RLock()
	defer mu.RUnlock()
	return fn
}

// ResetForTest clears the registry. Test-only.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	fn = nil
}
