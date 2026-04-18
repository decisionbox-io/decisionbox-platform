package sources

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

// RegisterFactory registers a provider constructor. Enterprise plugins call
// this from init() with a blank import. Calling RegisterFactory more than
// once is a programmer error and panics.
//
// Registering a factory does NOT activate the provider — the API/Agent must
// call Configure once their dependencies are ready.
func RegisterFactory(f ProviderFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if factory != nil {
		panic("sources: RegisterFactory called twice")
	}
	if f == nil {
		panic("sources: RegisterFactory called with nil factory")
	}
	factory = f
}

// Configure constructs and activates the registered provider using the
// supplied dependencies. Must be called by the API/Agent after MongoDB,
// vectorstore, and secret provider are initialized.
//
// If no factory is registered (enterprise plugin not loaded), Configure is a
// no-op and GetProvider continues to return the NoOp implementation.
//
// Calling Configure more than once replaces the active provider.
func Configure(ctx context.Context, deps Dependencies) error {
	registryMu.Lock()
	f := factory
	registryMu.Unlock()
	if f == nil {
		return nil
	}
	p, err := f(deps)
	if err != nil {
		return fmt.Errorf("sources: factory returned error: %w", err)
	}
	registryMu.Lock()
	provider = p
	registryMu.Unlock()
	return nil
}

// GetProvider returns the active Provider, or a NoOp if none has been
// configured. Safe to call from any goroutine; never returns nil.
func GetProvider() Provider {
	registryMu.RLock()
	p := provider
	registryMu.RUnlock()
	if p != nil {
		return p
	}
	return noopProvider{}
}

// resetForTest clears registry state. Test-only; do not call from production code.
func resetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	factory = nil
	provider = nil
}
