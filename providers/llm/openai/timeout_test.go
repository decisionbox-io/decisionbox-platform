package openai

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestOpenAI_FactoryWiresTimeout asserts ResolveHTTPTimeout is wired
// through the registered factory for every resolution branch.
func TestOpenAI_FactoryWiresTimeout(t *testing.T) {
	base := gollm.ProviderConfig{
		"credentials_json": "sk-test",
		"model":   "gpt-4o",
	}
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: gollm.ProviderConfig{"credentials_json": "sk-test", "model": "gpt-4o", "timeout_seconds": "777"}, envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: base, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_5m", cfg: base, want: openaiDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			p, err := gollm.NewProvider("openai", tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			op, ok := p.(*OpenAIProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *OpenAIProvider", p)
			}
			if op.client.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", op.client.Timeout, tc.want)
			}
		})
	}
}

// TestNewOpenAIProvider_TimeoutFallback documents the contract that a
// non-positive timeout falls back to the default — keeps test
// constructors terse without forcing every caller to repeat the const.
func TestNewOpenAIProvider_TimeoutFallback(t *testing.T) {
	for _, in := range []time.Duration{0, -1 * time.Second} {
		p := NewOpenAIProvider("k", "m", "", in)
		if p.client.Timeout != openaiDefaultTimeout {
			t.Fatalf("timeout(%v) = %v, want %v", in, p.client.Timeout, openaiDefaultTimeout)
		}
	}
	p := NewOpenAIProvider("k", "m", "", 42*time.Second)
	if p.client.Timeout != 42*time.Second {
		t.Fatalf("timeout(42s) = %v, want 42s", p.client.Timeout)
	}
}
