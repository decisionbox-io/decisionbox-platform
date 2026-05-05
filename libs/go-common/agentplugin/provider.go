// Package agentplugin defines registries that let plugins extend the agent
// without forking community code. Each registry is a small, generic hook
// (context provider, list-tables filter, …) so multiple unrelated plugins
// can attach without naming collisions.
//
// Registries here are process-global. Plugins register from init() with a
// blank import; registration order does not matter for correctness, but
// invocation order is stable (registration order) so prompt output is
// deterministic across builds.
package agentplugin

import (
	"context"
)

// ContextProviderOpts is per-call tuning the agent passes to every provider.
// Providers that don't need a particular knob (e.g. a static-config provider
// that ignores Limit) may simply ignore the field. Zero values mean
// "implementation default".
type ContextProviderOpts struct {
	// Limit caps the number of items the provider should include
	// (interpretation is provider-specific: chunks, hints, rules, …).
	Limit int
	// MinScore is the minimum similarity threshold for retrieval-style
	// providers. Providers without a notion of similarity ignore it.
	MinScore float64
}

// ContextProvider returns a markdown section to inject into agent prompts
// for a given project and query. Implementations should:
//
//   - Be safe to call concurrently from multiple goroutines.
//   - Honor ctx cancellation; long-running retrieval must abort promptly.
//   - Return an empty string when there is nothing to inject — the caller
//     concatenates results, so empty strings are dropped without a special
//     case at the call site.
//   - Scope output to projectID — never mix data across projects.
//
// Errors should be returned as wrapped errors with project context. A
// non-nil error suppresses the section for that provider in the rendered
// output but does not abort other providers in the chain.
type ContextProvider interface {
	// Section returns the markdown to append. Empty string means "nothing
	// to add for this (project, query)" and is dropped silently by the
	// renderer.
	Section(ctx context.Context, projectID string, query string, opts ContextProviderOpts) (string, error)

	// Name is a stable identifier for telemetry and tests. It must be
	// unique across registered providers.
	Name() string
}

// ContextProviderFunc adapts a function to the ContextProvider interface.
// The returned provider's Name() is the supplied name; double-registering
// the same name is a programmer error and panics in RegisterContextProvider.
type ContextProviderFunc struct {
	ProviderName string
	Fn           func(ctx context.Context, projectID string, query string, opts ContextProviderOpts) (string, error)
}

// Section implements ContextProvider.
func (f ContextProviderFunc) Section(ctx context.Context, projectID string, query string, opts ContextProviderOpts) (string, error) {
	if f.Fn == nil {
		return "", nil
	}
	return f.Fn(ctx, projectID, query, opts)
}

// Name implements ContextProvider.
func (f ContextProviderFunc) Name() string {
	return f.ProviderName
}
