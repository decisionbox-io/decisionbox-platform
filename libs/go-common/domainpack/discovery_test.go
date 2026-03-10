package domainpack

import (
	"testing"
)

// mockPack implements only Pack (not DiscoveryPack).
type mockPack struct{ name string }

func (p *mockPack) Name() string { return p.name }

// mockDiscoveryPack implements both Pack and DiscoveryPack.
type mockDiscoveryPack struct{ name string }

func (p *mockDiscoveryPack) Name() string                                      { return p.name }
func (p *mockDiscoveryPack) DomainCategories() []DomainCategory                { return nil }
func (p *mockDiscoveryPack) AnalysisAreas(cat string) []AnalysisArea           { return nil }
func (p *mockDiscoveryPack) Prompts(cat string) PromptTemplates                { return PromptTemplates{} }
func (p *mockDiscoveryPack) ProfileSchema(cat string) map[string]interface{}   { return nil }

func TestAsDiscoveryPack_WithDiscoveryPack(t *testing.T) {
	pack := &mockDiscoveryPack{name: "test"}
	dp, ok := AsDiscoveryPack(pack)
	if !ok {
		t.Error("should return true for DiscoveryPack")
	}
	if dp == nil {
		t.Error("should return non-nil DiscoveryPack")
	}
}

func TestAsDiscoveryPack_WithoutDiscoveryPack(t *testing.T) {
	pack := &mockPack{name: "test"}
	dp, ok := AsDiscoveryPack(pack)
	if ok {
		t.Error("should return false for non-DiscoveryPack")
	}
	if dp != nil {
		t.Error("should return nil")
	}
}

func TestRegisterAndGet(t *testing.T) {
	// Note: can't test Register with same name twice (panics).
	// The gaming pack already registers "gaming" via init().
	// Test Get for unknown pack.
	_, err := Get("nonexistent-domain-xyz")
	if err == nil {
		t.Error("should error for unknown domain")
	}
}

func TestRegisteredPacks(t *testing.T) {
	// Register a test pack for this test
	defer func() {
		// Clean up panic from double-register if test runs again
		recover()
	}()

	Register("test-rp", &mockPack{name: "test-rp"})
	names := RegisteredPacks()
	found := false
	for _, n := range names {
		if n == "test-rp" {
			found = true
		}
	}
	if !found {
		t.Error("should find registered test pack")
	}
}
