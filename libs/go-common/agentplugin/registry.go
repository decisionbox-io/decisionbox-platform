package agentplugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

var (
	providersMu sync.RWMutex
	providers   []ContextProvider
)

// RegisterContextProvider registers p as a context provider. Plugins call
// this from init(). Registering with an empty Name() panics; registering
// the same Name() twice panics.
//
// Registration order determines invocation order so output is deterministic.
func RegisterContextProvider(p ContextProvider) {
	if p == nil {
		panic("agentplugin: RegisterContextProvider called with nil provider")
	}
	name := p.Name()
	if name == "" {
		panic("agentplugin: ContextProvider.Name() returned empty string")
	}
	providersMu.Lock()
	defer providersMu.Unlock()
	for _, existing := range providers {
		if existing.Name() == name {
			panic(fmt.Sprintf("agentplugin: ContextProvider %q already registered", name))
		}
	}
	providers = append(providers, p)
}

// RegisterDefaultContextProvider registers p only if no provider with
// p.Name() is already registered. Built-in defaults (e.g. the
// community "knowledge-sources" provider) call this from init() so an
// overriding plugin can win without a hard ordering guarantee on Go's
// init sequence — whichever side runs first wins, and the second
// side is a no-op. Use RegisterContextProvider when you genuinely
// want a duplicate-name panic to surface a wiring bug.
//
// Panics on nil p or empty p.Name().
func RegisterDefaultContextProvider(p ContextProvider) {
	if p == nil {
		panic("agentplugin: RegisterDefaultContextProvider called with nil provider")
	}
	name := p.Name()
	if name == "" {
		panic("agentplugin: ContextProvider.Name() returned empty string")
	}
	providersMu.Lock()
	defer providersMu.Unlock()
	for _, existing := range providers {
		if existing.Name() == name {
			return
		}
	}
	providers = append(providers, p)
}

// ReplaceContextProvider atomically replaces the provider whose Name()
// equals the new provider's Name() — keeping its slot in registration
// order — or appends p when no provider with that name is registered.
//
// Use this when a plugin needs to override an init()-registered default
// (for example: an enterprise feature that wants to format the
// "knowledge-sources" section differently than the community default).
// Pair with RegisterDefaultContextProvider on the default side so the
// override works regardless of init ordering: either side may run
// first and the final state is always p in the slot.
//
// Panics on nil p or empty p.Name().
func ReplaceContextProvider(p ContextProvider) {
	if p == nil {
		panic("agentplugin: ReplaceContextProvider called with nil provider")
	}
	name := p.Name()
	if name == "" {
		panic("agentplugin: ContextProvider.Name() returned empty string")
	}
	providersMu.Lock()
	defer providersMu.Unlock()
	for i, existing := range providers {
		if existing.Name() == name {
			providers[i] = p
			return
		}
	}
	providers = append(providers, p)
}

// GetAllContextProviders returns the registered providers in registration
// order. The returned slice is a copy — callers may mutate it freely.
func GetAllContextProviders() []ContextProvider {
	providersMu.RLock()
	defer providersMu.RUnlock()
	out := make([]ContextProvider, len(providers))
	copy(out, providers)
	return out
}

// RenderSections invokes every registered provider for (projectID, query, opts)
// and returns the concatenation of their non-empty sections, separated by a
// single blank line. Sections are emitted in registration order.
//
// Per-provider errors are reported via onError (if non-nil) and that
// provider's section is skipped — one failing provider must not suppress
// the rest. If onError is nil, errors are silently dropped; callers that
// want logging should pass a function that logs.
//
// A panic inside a provider is recovered and converted to an error so a
// misbehaving plugin can never abort prompt assembly.
func RenderSections(ctx context.Context, projectID string, query string, opts ContextProviderOpts, onError func(name string, err error)) string {
	provs := GetAllContextProviders()
	if len(provs) == 0 {
		return ""
	}
	sections := make([]string, 0, len(provs))
	for _, p := range provs {
		// Capture the name once via the panic-safe helper so neither
		// the error message in safeSection nor the onError callback
		// re-invokes a misbehaving Name() — that would leak a panic
		// out of RenderSections and contradict the "one bad provider
		// can never abort prompt assembly" guarantee.
		name := safeName(p)
		section, err := safeSection(ctx, p, name, projectID, query, opts)
		if err != nil {
			if onError != nil {
				onError(name, err)
			}
			continue
		}
		if strings.TrimSpace(section) == "" {
			continue
		}
		sections = append(sections, strings.TrimRight(section, "\n"))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n"
}

// safeName returns p.Name(), recovering from a panic in the
// Name() implementation and returning a placeholder. ContextProvider
// authors are expected to make Name() trivial and side-effect-free,
// but we don't trust that — a single misbehaving plugin must never
// abort prompt assembly through any path, including the one used to
// build error messages about it.
func safeName(p ContextProvider) (name string) {
	defer func() {
		if r := recover(); r != nil {
			name = "<unknown:Name() panicked>"
		}
	}()
	return p.Name()
}

// safeSection invokes p.Section and converts a panic into an error.
// The provider name is passed in (already captured via safeName) so
// neither this function nor its deferred handler can panic during
// error reporting.
func safeSection(ctx context.Context, p ContextProvider, name, projectID, query string, opts ContextProviderOpts) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("agentplugin: provider %q panicked: %v", name, r)
		}
	}()
	return p.Section(ctx, projectID, query, opts)
}

// resetForTest clears the registry. Test-only; never call from production.
func resetForTest() {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers = nil
}
