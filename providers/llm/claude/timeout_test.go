package claude

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestClaude_FactoryWiresTimeout covers the three resolution branches
// for the registered Claude factory: cfg wins, env var fills in when
// cfg is silent, and the historical 60s fallback applies when neither
// is set. Goes through the registry (gollm.NewProvider) so the test
// catches accidental disconnects between init() and the helper.
func TestClaude_FactoryWiresTimeout(t *testing.T) {
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: gollm.ProviderConfig{"credentials_json": "sk-x", "timeout_seconds": "777"}, envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: gollm.ProviderConfig{"credentials_json": "sk-x"}, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_60s", cfg: gollm.ProviderConfig{"credentials_json": "sk-x"}, want: claudeDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			p, err := gollm.NewProvider("claude", tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			cp, ok := p.(*ClaudeProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *ClaudeProvider", p)
			}
			if cp.httpClient.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", cp.httpClient.Timeout, tc.want)
			}
		})
	}
}
