package ollama

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestOllama_FactoryWiresTimeout asserts ResolveHTTPTimeout is wired
// through the registered factory for every resolution branch. The
// effective timeout is observable via OllamaProvider.httpTimeout
// because the ollama SDK wraps the *http.Client behind unexported
// fields.
func TestOllama_FactoryWiresTimeout(t *testing.T) {
	base := gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:7b",
	}
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: merge(base, "timeout_seconds", "777"), envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: base, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_5m", cfg: base, want: ollamaDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			p, err := gollm.NewProvider("ollama", tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			op, ok := p.(*OllamaProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *OllamaProvider", p)
			}
			if op.httpTimeout != tc.want {
				t.Fatalf("timeout = %v, want %v", op.httpTimeout, tc.want)
			}
		})
	}
}

// TestNewOllamaProvider_TimeoutFallback documents the contract that a
// non-positive timeout falls back to the default.
func TestNewOllamaProvider_TimeoutFallback(t *testing.T) {
	for _, in := range []time.Duration{0, -1 * time.Second} {
		p, err := NewOllamaProvider("http://localhost:11434", "m", in)
		if err != nil {
			t.Fatalf("NewOllamaProvider: %v", err)
		}
		if p.httpTimeout != ollamaDefaultTimeout {
			t.Fatalf("timeout(%v) = %v, want %v", in, p.httpTimeout, ollamaDefaultTimeout)
		}
	}
	p, err := NewOllamaProvider("http://localhost:11434", "m", 42*time.Second)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	if p.httpTimeout != 42*time.Second {
		t.Fatalf("timeout(42s) = %v, want 42s", p.httpTimeout)
	}
}

func merge(base gollm.ProviderConfig, k, v string) gollm.ProviderConfig {
	out := make(gollm.ProviderConfig, len(base)+1)
	for kk, vv := range base {
		out[kk] = vv
	}
	out[k] = v
	return out
}
