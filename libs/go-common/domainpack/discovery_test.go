package domainpack

import (
	"strings"
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

func TestRegister_PanicOnNil(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("Register(nil) should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Errorf("panic value should be a string, got %T", r)
			return
		}
		if msg != "domainpack: Register pack is nil for nil-test" {
			t.Errorf("unexpected panic message: %s", msg)
		}
	}()
	Register("nil-test", nil)
}

func TestRegister_PanicOnDuplicate(t *testing.T) {
	// First register a unique pack name for this test
	const name = "dup-test"
	defer func() {
		r := recover()
		if r == nil {
			t.Error("Register duplicate should panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Errorf("panic value should be a string, got %T", r)
			return
		}
		if msg != "domainpack: Register called twice for "+name {
			t.Errorf("unexpected panic message: %s", msg)
		}
	}()
	Register(name, &mockPack{name: name})
	Register(name, &mockPack{name: name}) // should panic
}

func TestGet_NonExistent(t *testing.T) {
	pack, err := Get("absolutely-nonexistent-domain-pack-xyz")
	if err == nil {
		t.Error("Get for nonexistent pack should return error")
	}
	if pack != nil {
		t.Error("Get for nonexistent pack should return nil")
	}
	if err != nil && !strings.Contains(err.Error(), "unknown domain") {
		t.Errorf("error should mention 'unknown domain', got: %s", err.Error())
	}
}

func TestGet_RegisteredPack(t *testing.T) {
	const name = "get-test-pack"
	defer func() { recover() }() // in case already registered from prior run
	Register(name, &mockDiscoveryPack{name: name})

	pack, err := Get(name)
	if err != nil {
		t.Fatalf("Get(%q) error: %v", name, err)
	}
	if pack == nil {
		t.Fatal("Get should return non-nil pack")
	}
	if pack.Name() != name {
		t.Errorf("pack.Name() = %q, want %q", pack.Name(), name)
	}
}
