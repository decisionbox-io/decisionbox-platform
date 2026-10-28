package models

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// The agent and the API each resolve prompt caching with a local copy of the
// provider-registry key, so neither model package depends on the registry.
// That only holds while the copies agree with the real thing.
func TestPromptCacheConfigKeyMatchesRegistry(t *testing.T) {
	if promptCacheConfigKey != gollm.PromptCacheKey {
		t.Fatalf("promptCacheConfigKey = %q, but gollm.PromptCacheKey = %q — the legacy fallback would stop reading the stored value",
			promptCacheConfigKey, gollm.PromptCacheKey)
	}
}

// The agent must resolve exactly as the API does, or a project would cache
// during Ask and not during discovery (or the reverse).
func TestProject_EffectivePromptCacheEnabled(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		field *bool
		conf  map[string]string
		want  bool
	}{
		{"unset is on", nil, nil, true},
		{"legacy false", nil, map[string]string{"prompt_cache": "false"}, false},
		{"legacy FALSE", nil, map[string]string{"prompt_cache": "FALSE"}, false},
		{"field wins over legacy", &yes, map[string]string{"prompt_cache": "false"}, true},
		{"field off", &no, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Project{PromptCacheEnabled: c.field}
			p.LLM.Config = c.conf
			if got := p.EffectivePromptCacheEnabled(); got != c.want {
				t.Errorf("= %v, want %v", got, c.want)
			}
		})
	}
	if !(*Project)(nil).EffectivePromptCacheEnabled() {
		t.Error("nil project should resolve to caching on")
	}
}

// And the resolved value must be what gollm.PromptCacheEnabled then reads
// back out of the provider config — the two halves of the round trip.
func TestResolvedValueRoundTripsThroughProviderConfig(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name string
		proj *Project
		want bool
	}{
		{"off", &Project{PromptCacheEnabled: &no}, false},
		{"on", &Project{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gollm.ProviderConfig{}
			if tc.proj.EffectivePromptCacheEnabled() {
				cfg[gollm.PromptCacheKey] = "true"
			} else {
				cfg[gollm.PromptCacheKey] = "false"
			}
			if got := gollm.PromptCacheEnabled(cfg); got != tc.want {
				t.Errorf("provider reads %v, project resolved %v", got, tc.want)
			}
		})
	}
}
