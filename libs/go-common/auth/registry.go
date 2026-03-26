package auth

import "sync"

var (
	registryMu         sync.Mutex
	registeredProvider Provider
)

// RegisterProvider registers an auth provider plugin (typically called via init()).
// If no provider is registered, GetProvider returns NoAuthProvider.
func RegisterProvider(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredProvider = p
}

// GetProvider returns the registered auth provider, or NoAuthProvider if none registered.
func GetProvider() Provider {
	registryMu.Lock()
	defer registryMu.Unlock()
	if registeredProvider != nil {
		return registeredProvider
	}
	return NewNoAuthProvider()
}
