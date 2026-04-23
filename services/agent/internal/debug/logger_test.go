package debug

import "testing"

func TestNewLogger_RespectsProvidedDiscoveryRunID(t *testing.T) {
	// When the agent is launched by the API via `--run-id <hex ObjectId>`,
	// the orchestrator threads that hex string into LoggerOptions so every
	// debug_logs entry can be joined back to the `discovery_runs` document.
	// Without this, the dashboard has no way to surface debug logs for a
	// specific run.
	wantID := "69ea0dc6ea0c124d6d183059"
	l := NewLogger(LoggerOptions{DiscoveryRunID: wantID})
	if got := l.GetDiscoveryRunID(); got != wantID {
		t.Errorf("GetDiscoveryRunID() = %q, want %q", got, wantID)
	}
}

func TestNewLogger_FallsBackToGeneratedUUID(t *testing.T) {
	// When no ID is supplied (standalone agent runs, tests) the logger
	// should still produce something stable for the lifetime of the run so
	// its own internal correlation works. A UUID suffices — we just don't
	// want two successive loggers to collide.
	a := NewLogger(LoggerOptions{})
	b := NewLogger(LoggerOptions{})

	if a.GetDiscoveryRunID() == "" {
		t.Error("fallback run ID should not be empty")
	}
	if a.GetDiscoveryRunID() == b.GetDiscoveryRunID() {
		t.Errorf("two loggers without IDs returned the same fallback: %q", a.GetDiscoveryRunID())
	}
}
