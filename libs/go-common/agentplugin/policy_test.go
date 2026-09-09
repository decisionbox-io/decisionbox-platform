package agentplugin

import (
	"context"
	"errors"
	"testing"
)

type fakePolicyProvider struct {
	policy DiscoveryPolicy
	err    error
	panics bool
}

func (f fakePolicyProvider) Policy(context.Context, string) (DiscoveryPolicy, error) {
	if f.panics {
		panic("boom")
	}
	return f.policy, f.err
}
func (fakePolicyProvider) Name() string { return "fake" }

func TestResolveDiscoveryPolicy_DefaultWhenUnregistered(t *testing.T) {
	resetPolicyForTest()
	got, err := ResolveDiscoveryPolicy(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultDiscoveryPolicy() {
		t.Errorf("want default policy, got %+v", got)
	}
}

func TestResolveDiscoveryPolicy_RegisteredWins(t *testing.T) {
	resetPolicyForTest()
	t.Cleanup(resetPolicyForTest)
	want := DiscoveryPolicy{EvolutionMode: EvolutionModeAuto, FrontierPolicy: FrontierDepthFirst}
	RegisterDiscoveryPolicyProvider(fakePolicyProvider{policy: want})
	got, err := ResolveDiscoveryPolicy(context.Background(), "p1")
	if err != nil || got != want {
		t.Fatalf("got %+v, err %v; want %+v", got, err, want)
	}
}

func TestResolveDiscoveryPolicy_ErrorFallsBackToDefault(t *testing.T) {
	resetPolicyForTest()
	t.Cleanup(resetPolicyForTest)
	RegisterDiscoveryPolicyProvider(fakePolicyProvider{err: errors.New("db down")})
	got, err := ResolveDiscoveryPolicy(context.Background(), "p1")
	if err == nil {
		t.Error("expected the provider error to be surfaced")
	}
	if got != DefaultDiscoveryPolicy() {
		t.Errorf("want default policy on error, got %+v", got)
	}
}

func TestResolveDiscoveryPolicy_PanicRecovered(t *testing.T) {
	resetPolicyForTest()
	t.Cleanup(resetPolicyForTest)
	RegisterDiscoveryPolicyProvider(fakePolicyProvider{panics: true})
	got, err := ResolveDiscoveryPolicy(context.Background(), "p1")
	if err == nil {
		t.Error("a panicking provider should surface an error, not crash")
	}
	if got != DefaultDiscoveryPolicy() {
		t.Errorf("want default policy after panic, got %+v", got)
	}
}

func TestResolveDiscoveryPolicy_NormalizesUnknownValues(t *testing.T) {
	resetPolicyForTest()
	t.Cleanup(resetPolicyForTest)
	RegisterDiscoveryPolicyProvider(fakePolicyProvider{policy: DiscoveryPolicy{EvolutionMode: "bogus", FrontierPolicy: "bogus"}})
	got, _ := ResolveDiscoveryPolicy(context.Background(), "p1")
	if got.EvolutionMode != EvolutionModeOff || got.FrontierPolicy != FrontierBalanced {
		t.Errorf("unknown values should normalize to default, got %+v", got)
	}
}

func TestRegisterDiscoveryPolicyProvider_NilPanics(t *testing.T) {
	resetPolicyForTest()
	t.Cleanup(resetPolicyForTest)
	defer func() {
		if recover() == nil {
			t.Error("registering nil should panic")
		}
	}()
	RegisterDiscoveryPolicyProvider(nil)
}
