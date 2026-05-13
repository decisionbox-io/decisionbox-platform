package bedrock

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestBedrock_FactoryWiresTimeout exercises ResolveHTTPTimeout via the
// registered factory so the env-var path is verified end-to-end. The
// factory itself does not call AWS (the AWS SDK loads credentials
// lazily), so this runs on any host.
func TestBedrock_FactoryWiresTimeout(t *testing.T) {
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: gollm.ProviderConfig{"model": "anthropic.claude-sonnet-4-20250514-v1:0", "timeout_seconds": "777"}, envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: gollm.ProviderConfig{"model": "anthropic.claude-sonnet-4-20250514-v1:0"}, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_300s", cfg: gollm.ProviderConfig{"model": "anthropic.claude-sonnet-4-20250514-v1:0"}, want: bedrockDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			p, err := factory(tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			bp, ok := p.(*BedrockProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *BedrockProvider", p)
			}
			if bp.httpClient.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", bp.httpClient.Timeout, tc.want)
			}
		})
	}
}
