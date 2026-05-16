package azurefoundry

import (
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestAzureFoundry_FactoryWiresTimeout asserts ResolveHTTPTimeout is
// wired through the registered factory for every resolution branch.
func TestAzureFoundry_FactoryWiresTimeout(t *testing.T) {
	base := gollm.ProviderConfig{
		"endpoint": "https://example.services.ai.azure.com",
		"credentials_json":  "key",
		"model":    "claude-sonnet-4-6",
	}
	tests := []struct {
		name   string
		cfg    gollm.ProviderConfig
		envVal string
		want   time.Duration
	}{
		{name: "cfg_wins", cfg: merge(base, "timeout_seconds", "777"), envVal: "11s", want: 777 * time.Second},
		{name: "env_fills_in", cfg: base, envVal: "888s", want: 888 * time.Second},
		{name: "fallback_300s", cfg: base, want: azureFoundryDefaultTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(gollm.HTTPTimeoutEnvVar, tc.envVal)
			p, err := factory(tc.cfg)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			ap, ok := p.(*AzureFoundryProvider)
			if !ok {
				t.Fatalf("factory returned %T, want *AzureFoundryProvider", p)
			}
			if ap.httpClient.Timeout != tc.want {
				t.Fatalf("timeout = %v, want %v", ap.httpClient.Timeout, tc.want)
			}
		})
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
