package policy

import "sync"

var (
	registryMu         sync.RWMutex
	registeredChecker  Checker
)

// RegisterChecker registers a Checker plugin. Typically called from
// init() inside the cloud tenant's policy-plugin module. Self-hosted
// deployments do not call this and use NoopChecker via GetChecker.
func RegisterChecker(c Checker) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredChecker = c
}

// GetChecker returns the registered Checker, or NoopChecker if none is
// registered. Never returns nil — callers can always safely dereference.
func GetChecker() Checker {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if registeredChecker != nil {
		return registeredChecker
	}
	return NoopChecker{}
}
