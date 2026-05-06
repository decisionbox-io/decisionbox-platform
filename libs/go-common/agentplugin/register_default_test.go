package agentplugin

import (
	"context"
	"testing"
)

func TestRegisterDefaultContextProvider_AppendsWhenMissing(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	RegisterDefaultContextProvider(stubProvider{name: "default"})
	got := GetAllContextProviders()
	if len(got) != 1 || got[0].Name() != "default" {
		t.Fatalf("registry after default register = %v, want [default]", names(got))
	}
}

func TestRegisterDefaultContextProvider_NoOpWhenAlreadyRegistered(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	// Plugin "wins" by registering an override before the default's
	// init runs. The default register call must be a no-op — not panic
	// and not overwrite the plugin's choice — so init() ordering is
	// not load-bearing.
	override := stubProvider{name: "shared"}
	def := stubProvider{name: "shared"}
	ReplaceContextProvider(override)
	RegisterDefaultContextProvider(def)
	got := GetAllContextProviders()
	if len(got) != 1 {
		t.Fatalf("expected exactly one provider after default no-op, got %d", len(got))
	}
	// Pointer / value equality is fine — both are zero-sized structs
	// with the same name; check that the slot didn't grow.
	if got[0].Name() != "shared" {
		t.Fatalf("provider name=%q, want shared", got[0].Name())
	}
}

func TestRegisterDefaultContextProvider_ReplaceAfterDefaultStillWorks(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	// Reverse order: default registers first, plugin replaces.
	def := stubProvider{name: "shared"}
	override := overridingProvider{name: "shared"}
	RegisterDefaultContextProvider(def)
	ReplaceContextProvider(override)
	got := GetAllContextProviders()
	if len(got) != 1 {
		t.Fatalf("expected exactly one provider after replace, got %d", len(got))
	}
	if _, ok := got[0].(overridingProvider); !ok {
		t.Fatalf("provider type=%T, want overridingProvider — Replace did not swap", got[0])
	}
}

func TestRegisterDefaultContextProvider_PanicsOnNilProvider(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil provider, got none")
		}
	}()
	RegisterDefaultContextProvider(nil)
}

func TestRegisterDefaultContextProvider_PanicsOnEmptyName(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name, got none")
		}
	}()
	RegisterDefaultContextProvider(stubProvider{name: ""})
}

// overridingProvider is a distinct type from stubProvider so a test
// that wants to assert the slot was replaced can do so via a type
// assertion rather than by-name comparison.
type overridingProvider struct{ name string }

func (o overridingProvider) Name() string { return o.name }
func (overridingProvider) Section(context.Context, string, string, ContextProviderOpts) (string, error) {
	return "", nil
}
