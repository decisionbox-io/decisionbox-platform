package agentplugin

import (
	"context"
	"testing"
)

// stubProvider is a tiny ContextProvider used by tests in this file.
type stubProvider struct{ name string }

func (s stubProvider) Name() string { return s.name }
func (stubProvider) Section(context.Context, string, string, ContextProviderOpts) (string, error) {
	return "", nil
}

func TestResetForTest_ClearsAllProviders(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	RegisterContextProvider(stubProvider{name: "a"})
	RegisterContextProvider(stubProvider{name: "b"})
	if got := len(GetAllContextProviders()); got != 2 {
		t.Fatalf("setup: expected 2 providers, got %d", got)
	}
	ResetForTest()
	if got := len(GetAllContextProviders()); got != 0 {
		t.Fatalf("expected 0 providers after ResetForTest, got %d", got)
	}
}

func TestUnregisterContextProviderForTest_RemovesByName(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	RegisterContextProvider(stubProvider{name: "keep"})
	RegisterContextProvider(stubProvider{name: "drop"})
	RegisterContextProvider(stubProvider{name: "stay"})

	if ok := UnregisterContextProviderForTest("drop"); !ok {
		t.Fatal("expected UnregisterContextProviderForTest to return true on hit")
	}
	got := GetAllContextProviders()
	if len(got) != 2 || got[0].Name() != "keep" || got[1].Name() != "stay" {
		t.Fatalf("registry after unregister = %v, want [keep stay]", names(got))
	}
}

func TestUnregisterContextProviderForTest_NoMatchReturnsFalse(t *testing.T) {
	defer ResetForTest()
	ResetForTest()
	RegisterContextProvider(stubProvider{name: "only"})
	if ok := UnregisterContextProviderForTest("missing"); ok {
		t.Fatal("expected UnregisterContextProviderForTest to return false on miss")
	}
	if got := len(GetAllContextProviders()); got != 1 {
		t.Fatalf("registry size after miss = %d, want 1", got)
	}
}

func names(ps []ContextProvider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}
