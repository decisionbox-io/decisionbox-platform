package llm

import (
	"os"
	"strconv"
	"time"
)

// HTTPTimeoutEnvVar is the operator-facing env var that sets a global
// HTTP-client timeout for every registered LLM provider. The value is
// a Go duration string ("300s", "5m", "1h") — same format the agent's
// config layer has accepted for LLM_TIMEOUT for years, so a single
// variable applies to both the agent and the API process. Values that
// fail to parse, or parse to <= 0, are ignored so a misconfigured
// deployment keeps the provider fallback rather than crashing.
const HTTPTimeoutEnvVar = "LLM_TIMEOUT"

// ResolveHTTPTimeout returns the HTTP-client timeout an LLM provider
// should apply to outbound model calls. Resolution order:
//
//  1. cfg["timeout_seconds"] when a positive integer is present —
//     per-project override stored alongside the LLM credentials in
//     MongoDB, or pushed in by the agent's config layer from
//     LLM_TIMEOUT. Wins over everything so an operator can lengthen
//     the timeout for one tenant without touching the deployment.
//  2. LLM_TIMEOUT env var when a parseable Go duration is present —
//     deployment-wide default for every provider. Used by the API
//     process where there's no per-call cfg["timeout_seconds"]; the
//     agent already reads LLM_TIMEOUT through its own config layer.
//  3. fallback — the provider's historical hard-coded default. Callers
//     pass this explicitly so the env var stays opt-in: providers that
//     consciously want 60 s (e.g. Claude direct API) keep that until
//     the operator overrides.
//
// The helper is intentionally lenient about parse errors. A typo like
// LLM_TIMEOUT=15min should fall through to the next layer rather than
// panic at process start — provider construction happens per-request
// and an exception would cascade into discovery failures.
func ResolveHTTPTimeout(cfg ProviderConfig, fallback time.Duration) time.Duration {
	if cfg != nil {
		if v, err := strconv.Atoi(cfg["timeout_seconds"]); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	if raw := os.Getenv(HTTPTimeoutEnvVar); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
