package agentserver

import (
	"context"
	"testing"
	"time"
)

func TestDiscoveryRunContext_DefaultsWhenUnset(t *testing.T) {
	t.Setenv(discoveryMaxDurationEnv, "")

	ctx, cancel := discoveryRunContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline when env unset (default applies)")
	}
	remaining := time.Until(deadline)
	if remaining > defaultDiscoveryMaxDuration || remaining < defaultDiscoveryMaxDuration-time.Second {
		t.Fatalf("deadline outside expected window: %s (want ~%s)", remaining, defaultDiscoveryMaxDuration)
	}
}

func TestDiscoveryRunContext_HonoursValidDuration(t *testing.T) {
	t.Setenv(discoveryMaxDurationEnv, "72h")

	ctx, cancel := discoveryRunContext(context.Background())
	defer cancel()

	want := 72 * time.Hour
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	remaining := time.Until(deadline)
	if remaining > want || remaining < want-time.Second {
		t.Fatalf("deadline outside expected window: %s (want ~%s)", remaining, want)
	}
}

func TestDiscoveryRunContext_ZeroDisablesCap(t *testing.T) {
	// DISCOVERY_MAX_DURATION=0 is the documented escape hatch for
	// installs that prefer to rely solely on per-step budgets.
	// Returning the parent ctx is what makes the orchestrator
	// behave as if no outer cap were configured at all — and
	// regressing this back into a tiny zero-duration timeout would
	// kill discoveries on startup.
	t.Setenv(discoveryMaxDurationEnv, "0")

	parent := context.Background()
	ctx, cancel := discoveryRunContext(parent)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("DISCOVERY_MAX_DURATION=0 must not impose a deadline")
	}
	if ctx != parent {
		t.Fatal("DISCOVERY_MAX_DURATION=0 must return the parent ctx unchanged")
	}
}

func TestDiscoveryRunContext_InvalidValueFallsBackToDefault(t *testing.T) {
	// A typo in env config should not break the agent — we log a
	// warning and apply the default. The test pins that contract
	// so the fallback never silently turns into a zero-deadline
	// (which would kill every discovery immediately).
	t.Setenv(discoveryMaxDurationEnv, "not-a-duration")

	ctx, cancel := discoveryRunContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected fallback to default deadline")
	}
	remaining := time.Until(deadline)
	if remaining > defaultDiscoveryMaxDuration || remaining < defaultDiscoveryMaxDuration-time.Second {
		t.Fatalf("fallback deadline outside expected window: %s (want ~%s)", remaining, defaultDiscoveryMaxDuration)
	}
}

func TestDiscoveryRunContext_NegativeValueFallsBackToDefault(t *testing.T) {
	// time.ParseDuration accepts "-1h" — without the explicit
	// guard this would create an immediately-expired ctx and kill
	// every discovery. Falling back to the default is the safer
	// behavior.
	t.Setenv(discoveryMaxDurationEnv, "-1h")

	ctx, cancel := discoveryRunContext(context.Background())
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("negative env must fall back to default; got pre-expired ctx: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected fallback deadline")
	}
	remaining := time.Until(deadline)
	if remaining > defaultDiscoveryMaxDuration || remaining < defaultDiscoveryMaxDuration-time.Second {
		t.Fatalf("fallback deadline outside expected window: %s (want ~%s)", remaining, defaultDiscoveryMaxDuration)
	}
}

func TestDiscoveryRunContext_TrimsWhitespace(t *testing.T) {
	// Helm values files routinely leave trailing whitespace on env
	// vars. The trim keeps " 6h " from being treated as invalid.
	t.Setenv(discoveryMaxDurationEnv, "  6h  ")

	ctx, cancel := discoveryRunContext(context.Background())
	defer cancel()

	want := 6 * time.Hour
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	remaining := time.Until(deadline)
	if remaining > want || remaining < want-time.Second {
		t.Fatalf("deadline outside expected window: %s (want ~%s)", remaining, want)
	}
}
