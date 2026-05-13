package llm

import (
	"testing"
	"time"
)

func TestResolveHTTPTimeout(t *testing.T) {
	const fallback = 300 * time.Second

	tests := []struct {
		name   string
		cfg    ProviderConfig
		envVal string // empty means unset
		want   time.Duration
	}{
		// cfg branch — integer seconds, set by the agent or per-project settings
		{name: "cfg_positive_integer_wins", cfg: ProviderConfig{"timeout_seconds": "120"}, want: 120 * time.Second},
		{name: "cfg_wins_over_env", cfg: ProviderConfig{"timeout_seconds": "120"}, envVal: "9000s", want: 120 * time.Second},
		{name: "cfg_zero_falls_through_to_env", cfg: ProviderConfig{"timeout_seconds": "0"}, envVal: "10m", want: 10 * time.Minute},
		{name: "cfg_negative_falls_through_to_env", cfg: ProviderConfig{"timeout_seconds": "-1"}, envVal: "10m", want: 10 * time.Minute},
		{name: "cfg_non_numeric_falls_through_to_env", cfg: ProviderConfig{"timeout_seconds": "1m"}, envVal: "10m", want: 10 * time.Minute},
		{name: "cfg_empty_string_falls_through_to_env", cfg: ProviderConfig{"timeout_seconds": ""}, envVal: "10m", want: 10 * time.Minute},
		{name: "cfg_missing_key_falls_through_to_env", cfg: ProviderConfig{"api_key": "sk-x"}, envVal: "10m", want: 10 * time.Minute},
		{name: "nil_cfg_falls_through_to_env", cfg: nil, envVal: "10m", want: 10 * time.Minute},

		// env branch — Go duration format
		{name: "env_seconds_suffix", envVal: "900s", want: 900 * time.Second},
		{name: "env_minutes_suffix", envVal: "15m", want: 15 * time.Minute},
		{name: "env_hour_suffix", envVal: "1h", want: time.Hour},
		{name: "env_mixed_units", envVal: "1m30s", want: 90 * time.Second},
		{name: "env_zero_falls_through_to_fallback", envVal: "0s", want: fallback},
		{name: "env_negative_falls_through_to_fallback", envVal: "-5s", want: fallback},
		{name: "env_unitless_int_falls_through_to_fallback", envVal: "300", want: fallback}, // ParseDuration rejects bare integers
		{name: "env_garbage_falls_through_to_fallback", envVal: "fifteen-min", want: fallback},
		{name: "env_whitespace_falls_through_to_fallback", envVal: "  ", want: fallback},

		// fallback branch
		{name: "no_cfg_no_env_returns_fallback", want: fallback},
		{name: "empty_cfg_no_env_returns_fallback", cfg: ProviderConfig{}, want: fallback},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(HTTPTimeoutEnvVar, tc.envVal)
			got := ResolveHTTPTimeout(tc.cfg, fallback)
			if got != tc.want {
				t.Fatalf("ResolveHTTPTimeout(%v, env=%q) = %v, want %v", tc.cfg, tc.envVal, got, tc.want)
			}
		})
	}
}

// TestResolveHTTPTimeout_RespectsFallbackZero documents that callers
// passing a zero fallback get a zero return — http.Client.Timeout=0
// means "no timeout" in net/http, which is occasionally what a test
// or special-case caller wants. The helper does not second-guess it.
func TestResolveHTTPTimeout_RespectsFallbackZero(t *testing.T) {
	t.Setenv(HTTPTimeoutEnvVar, "")
	if got := ResolveHTTPTimeout(nil, 0); got != 0 {
		t.Fatalf("ResolveHTTPTimeout(nil, 0) = %v, want 0", got)
	}
}
