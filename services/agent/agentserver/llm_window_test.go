package agentserver

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// fakeResolverProvider implements gollm.Provider + gollm.ModelInfoResolver so
// the resolver's live-detection branch can be exercised deterministically.
type fakeResolverProvider struct {
	caps gollm.ModelCapabilities
	err  error
}

func (f *fakeResolverProvider) Chat(context.Context, gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return nil, errors.New("not used")
}
func (f *fakeResolverProvider) Validate(context.Context) error { return nil }
func (f *fakeResolverProvider) ResolveModelInfo(context.Context, string) (gollm.ModelCapabilities, error) {
	if f.err != nil {
		return gollm.ModelCapabilities{}, f.err
	}
	return f.caps, nil
}

// plainProvider implements only gollm.Provider (no live detection).
type plainProvider struct{}

func (plainProvider) Chat(context.Context, gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return nil, errors.New("not used")
}
func (plainProvider) Validate(context.Context) error { return nil }

// The provider name is intentionally not a registered catalog provider, so the
// catalog/default rung resolves to the package defaults (128K input, 64K
// output) and the test is independent of which providers are imported.
const unregistered = "unregistered-test-provider"

func TestResolveModelBudget_OperatorOverrideWins(t *testing.T) {
	p := &fakeResolverProvider{caps: gollm.ModelCapabilities{MaxInputTokens: 262144, MaxOutputTokens: 16384}}
	cfg := gollm.ProviderConfig{gollm.MaxInputTokensKey: "202752"}
	window, _ := resolveModelBudget(context.Background(), p, unregistered, "m", cfg, 300000)
	if window != 202752 {
		t.Fatalf("operator override must win: window=%d, want 202752", window)
	}
}

func TestResolveModelBudget_PersistedBeatsLive(t *testing.T) {
	p := &fakeResolverProvider{caps: gollm.ModelCapabilities{MaxInputTokens: 262144}}
	window, _ := resolveModelBudget(context.Background(), p, unregistered, "m", nil, 200000)
	if window != 200000 {
		t.Fatalf("persisted must beat live: window=%d, want 200000", window)
	}
}

func TestResolveModelBudget_LiveBeatsCatalogDefault(t *testing.T) {
	p := &fakeResolverProvider{caps: gollm.ModelCapabilities{MaxInputTokens: 262144, MaxOutputTokens: 16384}}
	window, outCap := resolveModelBudget(context.Background(), p, unregistered, "m", nil, 0)
	if window != 262144 {
		t.Fatalf("live must beat catalog/default: window=%d, want 262144", window)
	}
	if outCap != 16384 {
		t.Fatalf("live output cap should be used: outCap=%d, want 16384", outCap)
	}
}

func TestResolveModelBudget_FallsBackToDefault(t *testing.T) {
	// Plain provider (no live), no override, no persisted → catalog/default.
	window, outCap := resolveModelBudget(context.Background(), plainProvider{}, unregistered, "m", nil, 0)
	if window != gollm.DefaultMaxInputTokens {
		t.Fatalf("expected default window %d, got %d", gollm.DefaultMaxInputTokens, window)
	}
	if outCap != gollm.DefaultMaxOutputTokens {
		t.Fatalf("expected default output %d, got %d", gollm.DefaultMaxOutputTokens, outCap)
	}
}

func TestResolveModelBudget_LiveErrorFallsThrough(t *testing.T) {
	p := &fakeResolverProvider{err: errors.New("gateway down")}
	window, _ := resolveModelBudget(context.Background(), p, unregistered, "m", nil, 0)
	if window != gollm.DefaultMaxInputTokens {
		t.Fatalf("live error must fall through to default: window=%d, want %d", window, gollm.DefaultMaxInputTokens)
	}
}

func TestResolveModelBudget_OutputOverrideCapsLive(t *testing.T) {
	// Live says 100000 output, operator caps at 32768 → 32768.
	p := &fakeResolverProvider{caps: gollm.ModelCapabilities{MaxOutputTokens: 100000}}
	cfg := gollm.ProviderConfig{gollm.MaxOutputTokensKey: "32768"}
	_, outCap := resolveModelBudget(context.Background(), p, unregistered, "m", cfg, 0)
	if outCap != 32768 {
		t.Fatalf("operator output override must cap live: outCap=%d, want 32768", outCap)
	}
}
