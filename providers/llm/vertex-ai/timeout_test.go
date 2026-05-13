package vertexai

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestVertex_FactoryWiresTimeout asserts ResolveHTTPTimeout is wired
// through the registered factory for every resolution branch. Skips
// when ADC is unavailable so a developer without GCP creds can still
// run the rest of the package's tests.
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
			p, err := factory(tc.cfg)
			if err != nil {
				// newGCPAuth fails without Application Default
				// Credentials. The timeout-wiring assertion still
				// belongs in this package; skip the env without ADC
				// rather than papering over a real factory bug.
				t.Skipf("factory requires GCP ADC: %v", err)
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
