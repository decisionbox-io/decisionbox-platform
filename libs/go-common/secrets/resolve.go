package secrets

import (
	"context"
	"errors"
	"os"
)

// Credential sources returned by ResolveCredential, describing where a
// resolved value came from. Callers log this so operators can see which
// path supplied a credential.
const (
	// SourceDashboard — the per-project secret stored via the dashboard.
	SourceDashboard = "dashboard"
	// SourceEnv — the process environment fallback (managed-inference
	// gateway mode, or a self-hosted global default).
	SourceEnv = "env"
	// SourceNone — neither a secret nor an env value was present.
	SourceNone = "none"
)

// ResolveCredential applies the platform credential-resolution order to a
// single slot: the per-project dashboard secret wins, and the named
// environment variable is the fallback. It returns the resolved value and
// its source (SourceDashboard, SourceEnv, or SourceNone).
//
// Managed-inference gateway mode (AI_GATEWAY_URL set) stores no per-project
// AI secret by design, so every inference call site relies on the env
// fallback to reach the gateway; on-prem / BYO-key deployments set no such
// env var, so the per-project secret continues to win. This single helper
// is the one place that order lives — the agent, the API, and the
// enterprise plugins all call it so the resolution can't drift between
// surfaces.
//
// A missing secret is not an error: ErrNotFound — including the wrapped
// form the gcp/aws/azure backends return via %w — falls through to the env
// var silently. An *unexpected* secret-store error (a transport failure,
// say) is returned in err so the caller can log it, but resolution still
// falls through to the env var: an env-only or managed deployment must keep
// working through a transient secret-backend blip rather than hard-fail.
// A nil provider is tolerated (env-only resolution), which keeps call sites
// that may not have wired a secret provider honest.
func ResolveCredential(ctx context.Context, provider Provider, projectID, secretKey, envVar string) (value, source string, err error) {
	if provider != nil {
		v, gerr := provider.Get(ctx, projectID, secretKey)
		switch {
		case gerr == nil && v != "":
			return v, SourceDashboard, nil
		case gerr == nil:
			// Empty value stored — treat as unset and fall through to env.
		case errors.Is(gerr, ErrNotFound):
			// Not set — fall through to env silently. Use errors.Is because
			// the cloud backends wrap the sentinel with %w.
		default:
			// Unexpected read failure — remember it for the caller to log,
			// but still try the env fallback below.
			err = gerr
		}
	}
	if v := os.Getenv(envVar); v != "" {
		return v, SourceEnv, err
	}
	return "", SourceNone, err
}
