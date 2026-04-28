package packgen

import (
	"context"
	"fmt"
	"sync"
)

var (
	registryMu sync.RWMutex
	factory    ProviderFactory
	provider   Provider
)

// RegisterFactory registers a provider constructor. Plugins call this
// from init() with a blank import. Calling RegisterFactory more than
// once is a programmer error and panics. A nil factory also panics.
//
// Registering a factory does NOT activate the provider — the API or
// Agent must call Configure once their dependencies are ready.
func RegisterFactory(f ProviderFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if f == nil {
		panic("packgen: RegisterFactory called with nil factory")
	}
	if factory != nil {
		panic("packgen: RegisterFactory called twice")
	}
	factory = f
}

// Configure constructs and activates the registered provider using the
// supplied dependencies. Must be called by the API or Agent after
// MongoDB, vectorstore, secret provider, and sources are initialized.
//
// If no factory is registered, Configure is a no-op and GetProvider
// continues to return the no-op implementation.
//
// If the factory returns an error the active provider is left unchanged
// (the no-op stays in place) and the error is returned wrapped.
//
// Calling Configure more than once with a successful factory replaces
// the active provider. This is intended for tests and re-initialization
// flows; production code calls Configure once at startup.
func Configure(ctx context.Context, deps Dependencies) error {
	registryMu.Lock()
	f := factory
	registryMu.Unlock()
	if f == nil {
		return nil
	}
	p, err := f(deps)
	if err != nil {
		return fmt.Errorf("packgen: factory returned error: %w", err)
	}
	registryMu.Lock()
	provider = p
	registryMu.Unlock()
	return nil
}

// GetProvider returns the active Provider, or the no-op implementation
// if none has been configured. Safe to call from any goroutine; never
// returns nil.
func GetProvider() Provider {
	registryMu.RLock()
	p := provider
	registryMu.RUnlock()
	if p != nil {
		return p
	}
	return noopProvider{}
}

// IsAvailable reports whether a non-no-op Provider is configured. Useful
// for handlers that want to short-circuit with a 404 before validating
// request bodies, and for dashboards that hide the generator UI when no
// provider is configured.
//
// IsAvailable returns false in two states: (1) no factory was
// registered, and (2) a factory was registered but Configure has not
// been called yet (or Configure failed and the no-op is still in
// place).
func IsAvailable() bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return provider != nil
}

// resetForTest clears registry state. Test-only; do not call from
// production code.
func resetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	factory = nil
	provider = nil
}
