package vertexai

import (
	"context"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/gcpcreds"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestVertex_FactoryWiresTimeout asserts ResolveHTTPTimeout is wired
// through the registered factory for every resolution branch. Stubs
// the GCP auth constructor via the package-level `newAuth` seam so the
// factory's downstream code path executes on hosts without ADC.
func TestVertex_FactoryWiresTimeout(t *testing.T) {
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: gollm.ProviderConfig{"project_id": "test-project", "model": "gemini-2.5-pro", "timeout_seconds": "777"}, envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: gollm.ProviderConfig{"project_id": "test-project", "model": "gemini-2.5-pro"}, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_300s", cfg: gollm.ProviderConfig{"project_id": "test-project", "model": "gemini-2.5-pro"}, want: vertexDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			restore := stubAuth(&gcpAuth{tokenSource: &mockTokenSource{token: "test"}}, nil)
			defer restore()

			p, err := factory(tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			vp, ok := p.(*VertexAIProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *VertexAIProvider", p)
			}
			if vp.httpClient.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", vp.httpClient.Timeout, tc.want)
			}
		})
	}
}

// stubAuth replaces newAuth with a fake that returns the supplied
// values, restoring the original on cleanup. Returning a closure keeps
// the test's defer site honest about scope.
func stubAuth(a *gcpAuth, err error) func() {
	prev := newAuth
	newAuth = func(_ context.Context, _ gcpcreds.Config) (*gcpAuth, error) {
		return a, err
	}
	return func() { newAuth = prev }
}
