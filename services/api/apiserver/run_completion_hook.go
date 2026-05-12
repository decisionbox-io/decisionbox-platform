package apiserver

import (
	"github.com/decisionbox-io/decisionbox/services/api/internal/runhooks"
)

// RunCompletion mirrors runhooks.RunCompletion as the public payload
// type plugins receive. It is re-exported here so plugins only depend
// on this package — services/api/internal/runhooks is an
// implementation-detail leaf that the dispatcher reads.
type RunCompletion = runhooks.RunCompletion

// RunCompletionHook is the function signature plugins implement.
type RunCompletionHook = runhooks.Hook

// RegisterRunCompletionHook installs fn as a discovery-run completion
// hook identified by name. Plugins call this from init(); the API's
// dispatcher invokes every registered hook once per terminal run.
//
// Calling RegisterRunCompletionHook with an empty name, a nil function,
// or a duplicate name panics — silent shadowing would be a footgun for
// a process-global registry.
//
// Hooks fire in registration order. A non-nil return leaves the run
// unmarked so the dispatcher retries it on the next tick. Hooks MUST
// be idempotent: a peer hook failing on the same run causes successful
// peers to be invoked again.
//
// See plugin-hooks.md Hook 5 for the full contract and an example.
func RegisterRunCompletionHook(name string, fn RunCompletionHook) {
	runhooks.Register(name, fn)
}

// HasRegisteredRunCompletionHook reports whether at least one
// completion hook is registered. Exported for diagnostics; the
// dispatcher reads the same registry directly.
func HasRegisteredRunCompletionHook() bool {
	return runhooks.HasRegistered()
}

// ResetRunCompletionHooksForTest clears the registry. Test-only.
// Production code MUST NOT call it.
func ResetRunCompletionHooksForTest() {
	runhooks.ResetForTest()
}
