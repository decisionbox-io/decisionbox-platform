package sources

// ResetForTest clears the registered factory and active provider.
// Intended for use in tests in other packages that need to inject a stub
// Provider; production code MUST NOT call this.
func ResetForTest() {
	resetForTest()
}

// SetProviderForTest installs a Provider directly, bypassing the factory
// registration flow. Intended for use in tests; production code MUST NOT call this.
func SetProviderForTest(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	factory = nil
	provider = p
}
