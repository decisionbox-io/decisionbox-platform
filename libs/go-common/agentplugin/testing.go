package agentplugin

// ResetForTest clears every registered ContextProvider. Test-only;
// production code MUST NOT call this.
func ResetForTest() {
	resetForTest()
}

// UnregisterContextProviderForTest removes a single provider by name and
// returns true if it was present. Useful when a test wants to add a
// transient provider without disturbing init()-registered ones (the
// canonical knowledge-sources provider, in particular). Test-only;
// production code MUST NOT call this.
func UnregisterContextProviderForTest(name string) bool {
	providersMu.Lock()
	defer providersMu.Unlock()
	for i, p := range providers {
		if p.Name() == name {
			providers = append(providers[:i], providers[i+1:]...)
			return true
		}
	}
	return false
}

// ResetScoringConfigProviderForTest clears the registered scoring-configuration
// provider, so a test in another package can register a fake and leave the
// registry as it found it. Test-only; production code MUST NOT call this.
func ResetScoringConfigProviderForTest() {
	resetScoringForTest()
}

// ResetConsolidationForTest clears the registered ConsolidationProvider.
// Test-only; production code MUST NOT call this.
func ResetConsolidationForTest() {
	resetConsolidationForTest()
}
