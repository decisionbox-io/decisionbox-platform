package apiserver

import (
	"context"
	"errors"

	"github.com/decisionbox-io/decisionbox/services/api/internal/discoverytrigger"
)

// DiscoveryTriggerOptions are the inputs to a programmatic discovery-run
// trigger. Re-exported so plugins depend only on this package.
type DiscoveryTriggerOptions = discoverytrigger.Options

// DiscoveryTriggerResult is the outcome of a successful trigger.
type DiscoveryTriggerResult = discoverytrigger.Result

// Re-exported trigger errors. Callers use errors.Is / errors.As against
// these to branch on the rejection reason.
var (
	// ErrDiscoveryTriggerUnavailable is returned by TriggerDiscovery when
	// no trigger implementation has been registered — i.e. the API server
	// has not been constructed in this process.
	ErrDiscoveryTriggerUnavailable = errors.New("discovery trigger not available")

	// ErrProjectNotFound is returned when the project does not exist.
	ErrProjectNotFound = discoverytrigger.ErrProjectNotFound
)

// DiscoveryConflictError reports a state precondition that blocks the run
// (project not ready, schema index not ready, …).
type DiscoveryConflictError = discoverytrigger.ConflictError

// DiscoveryAlreadyRunningError reports that a discovery is already running
// for the project; RunID identifies the in-flight run.
type DiscoveryAlreadyRunningError = discoverytrigger.AlreadyRunningError

// DiscoveryInvalidParamsError reports invalid trigger parameters.
type DiscoveryInvalidParamsError = discoverytrigger.InvalidParamsError

// TriggerDiscovery starts a discovery run through the same code path as
// POST /api/v1/projects/{id}/discover — all lifecycle/schema-index
// gating, run-record reservation, plan-policy enforcement, and agent
// spawn are reused, so in-process callers never duplicate that logic.
//
// The implementation is registered by the API server during construction
// (Run). If TriggerDiscovery is called before the server is built (or in a
// build that never constructs it), it returns ErrDiscoveryTriggerUnavailable.
//
// On rejection it returns ErrProjectNotFound, *DiscoveryConflictError,
// *DiscoveryAlreadyRunningError, *DiscoveryInvalidParamsError, a
// *policy.PolicyError for plan denials, or a generic error for
// infrastructure failures.
func TriggerDiscovery(ctx context.Context, opts DiscoveryTriggerOptions) (DiscoveryTriggerResult, error) {
	fn := discoverytrigger.Get()
	if fn == nil {
		return DiscoveryTriggerResult{}, ErrDiscoveryTriggerUnavailable
	}
	return fn(ctx, opts)
}

// HasDiscoveryTrigger reports whether a trigger implementation has been
// registered. Exported for diagnostics and for callers that want to skip
// work when the trigger is unavailable.
func HasDiscoveryTrigger() bool {
	return discoverytrigger.Get() != nil
}
