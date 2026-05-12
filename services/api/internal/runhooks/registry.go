// Package runhooks owns the process-global registry of discovery-run
// completion hooks. Plugins register a named hook via
// apiserver.RegisterRunCompletionHook (a thin wrapper around this
// package). The API's run-completion dispatcher reads the registry on
// every tick and fires each hook for any run that has terminated
// (status in {completed, failed, cancelled}) and has not had its hooks
// fired yet — see plugin-hooks.md, Hook 5.
//
// This lives in its own leaf package so both apiserver (which exports
// the registration function) and the dispatcher in services/api/internal/server
// (which fires the hooks) can depend on it without an import cycle.
package runhooks

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RunCompletion is the snapshot of a terminal discovery run that the
// dispatcher passes to each hook. Hooks receive a copy — mutating the
// struct does not affect the persisted run or other hooks.
type RunCompletion struct {
	// RunID is the discovery run identifier (DiscoveryRun._id, hex form).
	// Also serves as the discovery ID consumers reference (insights /
	// recommendations / executive summaries all key off the same value).
	RunID string

	// ProjectID is the project that owns the run.
	ProjectID string

	// Status is the terminal status: "completed", "failed", or "cancelled".
	Status string

	// CompletedAt is the time the run reached its terminal state, as
	// recorded by whichever party finalised the run (agent for normal
	// completion, API for cancel / fail / stale-sweeper paths).
	CompletedAt time.Time

	// Error is the failure message when Status == "failed" or when the
	// run was force-failed by the stale-run sweeper. Empty for
	// successful completion.
	Error string
}

// Hook is the function plugins implement to receive run-completion
// notifications. A non-nil return causes the dispatcher to leave the
// run unmarked so the hook runs again on the next tick. Hooks MUST be
// idempotent — partial-failure semantics mean a hook that previously
// succeeded may be invoked again if a peer hook failed on the same run.
type Hook func(ctx context.Context, run RunCompletion) error

// namedHook pairs a registration name with its function so the registry
// can detect duplicate registrations and surface hook identity in logs.
type namedHook struct {
	name string
	fn   Hook
}

var (
	mu    sync.RWMutex
	hooks []namedHook
)

// Register installs fn as the run-completion hook identified by name.
// Plugins call this from init(). Registering with an empty name, a
// nil function, or a duplicate name panics — these are programmer
// errors that would silently shadow earlier configuration.
//
// Hooks fire in registration order so output (logs, downstream side
// effects) is deterministic for a given binary build.
func Register(name string, fn Hook) {
	if name == "" {
		panic("runhooks: Register called with empty name")
	}
	if fn == nil {
		panic("runhooks: Register called with nil hook")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hooks {
		if h.name == name {
			panic(fmt.Sprintf("runhooks: hook %q already registered", name))
		}
	}
	hooks = append(hooks, namedHook{name: name, fn: fn})
}

// HookResult reports the outcome of a single hook invocation. The
// dispatcher uses this to decide whether to mark the run as
// hook-dispatched (all results err==nil) or to leave it for the next
// tick (any result err!=nil).
type HookResult struct {
	Name string
	Err  error
}

// Fire invokes every registered hook for the given run in registration
// order, returning one HookResult per hook. Hooks are invoked
// sequentially so the dispatcher can rely on ordering and on a hook
// having finished before the next observes its side effects (e.g. an
// audit hook recording the event before a downstream notifier fires).
//
// A hook that panics is recovered: its result records a synthesised
// error referencing the panic value, and subsequent hooks still run.
// This matches the resilience contract of the agent-side context
// provider hook (a misbehaving plugin must not prevent peer plugins
// from observing the same event).
func Fire(ctx context.Context, run RunCompletion) []HookResult {
	mu.RLock()
	snapshot := make([]namedHook, len(hooks))
	copy(snapshot, hooks)
	mu.RUnlock()

	results := make([]HookResult, 0, len(snapshot))
	for _, h := range snapshot {
		results = append(results, HookResult{
			Name: h.name,
			Err:  invokeHook(ctx, h, run),
		})
	}
	return results
}

// invokeHook calls fn with panic recovery so one misbehaving hook
// cannot abort the dispatcher's per-run loop.
func invokeHook(ctx context.Context, h namedHook, run RunCompletion) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hook %q panicked: %v", h.name, r)
		}
	}()
	return h.fn(ctx, run)
}

// HasRegistered reports whether any completion hook is currently
// registered. The server uses this at startup to decide whether to
// spin up the dispatcher goroutine — with zero hooks the dispatcher
// would issue Mongo scans for no benefit.
func HasRegistered() bool {
	mu.RLock()
	defer mu.RUnlock()
	return len(hooks) > 0
}

// Names returns the registered hook names in registration order.
// Exported for diagnostics and tests; production code MUST NOT depend
// on the exact slice for invocation routing — use Fire instead.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, len(hooks))
	for i, h := range hooks {
		out[i] = h.name
	}
	return out
}

// ResetForTest clears the registry. Test-only — production code MUST
// NOT call it. Exported so tests in sibling packages (server,
// apiserver) can isolate themselves from cross-test registration
// leakage.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	hooks = nil
}
