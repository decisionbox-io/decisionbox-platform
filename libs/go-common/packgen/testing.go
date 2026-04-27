package packgen

// ResetForTest clears the registered factory and active provider.
// Intended for use in tests in other packages that need a clean
// registry; production code MUST NOT call this.
func ResetForTest() {
	resetForTest()
}

// SetProviderForTest installs a Provider directly, bypassing the
// factory registration flow. Intended for use in tests that want to
// inject a stub Provider without spinning up real infrastructure.
// Production code MUST NOT call this.
func SetProviderForTest(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	factory = nil
	provider = p
}
