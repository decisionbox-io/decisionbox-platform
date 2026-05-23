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

func TestDiscoverySweepLookback_FloorWhenUnset(t *testing.T) {
	// Unset env preserves the historical 24h lookback exactly — the
	// agent's boot-time orphan sweep behavior on an unconfigured
	// install must not change.
	t.Setenv(discoveryMaxDurationEnv, "")

	if got := discoverySweepLookback(); got != sweepLookbackFloor {
		t.Errorf("unset env should yield floor %s; got %s", sweepLookbackFloor, got)
	}
}

func TestDiscoverySweepLookback_TracksCapAboveFloor(t *testing.T) {
	// Codex r3 surfaced this: with DISCOVERY_MAX_DURATION=168h
	// (the documented week-long enterprise case) the old hard-coded
	// 24h lookback shadowed the cap, dropping still-running runs
	// from keepRunIDs and letting the sweep delete their Qdrant
	// run-step collections mid-flight. The lookback must track the
	// cap with a buffer.
	t.Setenv(discoveryMaxDurationEnv, "168h")

	want := 168*time.Hour + sweepLookbackBuffer
	if got := discoverySweepLookback(); got != want {
		t.Errorf("168h cap should yield %s lookback; got %s", want, got)
	}
}

func TestDiscoverySweepLookback_HonoursFloorOnSmallCap(t *testing.T) {
	// A 1h cap + buffer is below the historical floor — keeping
	// the floor means short-cap operators never accidentally shrink
	// the sweep window below 24h, which would put recently-finished
	// runs at risk during boot-time orphan sweeps.
	t.Setenv(discoveryMaxDurationEnv, "1h")

	if got := discoverySweepLookback(); got != sweepLookbackFloor {
		t.Errorf("1h cap should clamp to floor %s; got %s", sweepLookbackFloor, got)
	}
}

func TestDiscoverySweepLookback_ZeroUsesDisabledWindow(t *testing.T) {
	// DISCOVERY_MAX_DURATION=0 means the operator has opted into
	// runs of arbitrary length — the sweep must be very conservative
	// or it will delete the collection of a multi-day discovery.
	// The 30-day default is large enough that no realistic
	// discovery is older.
	t.Setenv(discoveryMaxDurationEnv, "0")

	if got := discoverySweepLookback(); got != sweepLookbackDisabled {
		t.Errorf("DISCOVERY_MAX_DURATION=0 should yield disabled window %s; got %s", sweepLookbackDisabled, got)
	}
}

func TestDiscoverySweepLookback_InvalidValueUsesFloor(t *testing.T) {
	// A typo in env config must not collapse the sweep window to 0
	// (which would treat every run as orphaned) — fall back to the
	// historical 24h floor.
	t.Setenv(discoveryMaxDurationEnv, "not-a-duration")

	if got := discoverySweepLookback(); got != sweepLookbackFloor {
		t.Errorf("invalid env should fall back to floor %s; got %s", sweepLookbackFloor, got)
	}
}

func TestDiscoverySweepLookback_NegativeValueUsesFloor(t *testing.T) {
	// Same reasoning as the invalid-value case — never zero-out the
	// lookback in response to a bad config.
	t.Setenv(discoveryMaxDurationEnv, "-1h")

	if got := discoverySweepLookback(); got != sweepLookbackFloor {
		t.Errorf("negative env should fall back to floor %s; got %s", sweepLookbackFloor, got)
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
