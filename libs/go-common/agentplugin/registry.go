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

// ReplaceContextProvider atomically replaces the provider whose Name()
// equals the new provider's Name() — keeping its slot in registration
// order — or appends p when no provider with that name is registered.
//
// Use this when a plugin needs to override an init()-registered default
// (for example: an enterprise feature that wants to format the
// "knowledge-sources" section differently than the community default).
// The semantics are intentionally idempotent so a plugin that imports
// itself twice still ends up with one registration.
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
		section, err := safeSection(ctx, p, projectID, query, opts)
		if err != nil {
			if onError != nil {
				onError(p.Name(), err)
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

// safeSection invokes p.Section and converts a panic into an error.
func safeSection(ctx context.Context, p ContextProvider, projectID, query string, opts ContextProviderOpts) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("agentplugin: provider %q panicked: %v", p.Name(), r)
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
