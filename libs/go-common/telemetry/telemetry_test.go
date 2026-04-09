package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func resetGlobal() {
	globalClient = nil
	once = sync.Once{}
}

func TestIsEnabled_Default(t *testing.T) {
	os.Unsetenv("TELEMETRY_ENABLED")
	os.Unsetenv("DO_NOT_TRACK")
	if !isEnabled() {
		t.Error("telemetry should be enabled by default")
	}
}

func TestIsEnabled_DisabledByTelemetryEnabled(t *testing.T) {
	t.Setenv("TELEMETRY_ENABLED", "false")
	os.Unsetenv("DO_NOT_TRACK")
	if isEnabled() {
		t.Error("telemetry should be disabled when TELEMETRY_ENABLED=false")
	}
}

func TestIsEnabled_DisabledByDoNotTrack(t *testing.T) {
	os.Unsetenv("TELEMETRY_ENABLED")
	t.Setenv("DO_NOT_TRACK", "1")
	if isEnabled() {
		t.Error("telemetry should be disabled when DO_NOT_TRACK=1")
	}
}

func TestIsEnabled_DoNotTrackTrue(t *testing.T) {
	os.Unsetenv("TELEMETRY_ENABLED")
	t.Setenv("DO_NOT_TRACK", "true")
	if isEnabled() {
		t.Error("telemetry should be disabled when DO_NOT_TRACK=true")
	}
}

func TestIsEnabled_DoNotTrackYes(t *testing.T) {
	os.Unsetenv("TELEMETRY_ENABLED")
	t.Setenv("DO_NOT_TRACK", "yes")
	if isEnabled() {
		t.Error("telemetry should be disabled when DO_NOT_TRACK=yes")
	}
}

func TestTrack_DisabledIsNoop(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	t.Setenv("TELEMETRY_ENABLED", "false")

	Init("test-id", "0.1.0", "test")
	Track("test_event", map[string]any{"key": "value"})

	// Should not panic and client should have no events
	if globalClient.enabled {
		t.Error("client should be disabled")
	}
}

func TestTrack_NilClientIsNoop(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	// No Init() called — globalClient is nil
	Track("test_event", nil) // Should not panic
}

func TestTrack_AddsEvent(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	t.Setenv("TELEMETRY_ENABLED", "true")
	os.Unsetenv("DO_NOT_TRACK")

	// Use a test server so flush doesn't hit a real endpoint
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("TELEMETRY_ENDPOINT", ts.URL)

	Init("test-id", "0.1.0", "test")

	Track("test_event", map[string]any{"count": 42})

	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()

	if len(globalClient.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(globalClient.events))
	}
	if globalClient.events[0].Name != "test_event" {
		t.Errorf("expected event name 'test_event', got %q", globalClient.events[0].Name)
	}
	if globalClient.events[0].Properties["count"] != 42 {
		t.Errorf("expected count=42, got %v", globalClient.events[0].Properties["count"])
	}
}

func TestFlush_SendsBatch(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	t.Setenv("TELEMETRY_ENABLED", "true")
	os.Unsetenv("DO_NOT_TRACK")

	var received Batch
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("TELEMETRY_ENDPOINT", ts.URL)

	Init("test-install-id", "0.2.0", "api")

	Track("server_started", map[string]any{"port": "8080"})

	globalClient.flush()

	// Give the async send a moment
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if received.InstallID != "test-install-id" {
		t.Errorf("expected install_id 'test-install-id', got %q", received.InstallID)
	}
	if received.Version != "0.2.0" {
		t.Errorf("expected version '0.2.0', got %q", received.Version)
	}
	if received.Service != "api" {
		t.Errorf("expected service 'api', got %q", received.Service)
	}
	if len(received.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received.Events))
	}
	if received.Events[0].Name != "server_started" {
		t.Errorf("expected event 'server_started', got %q", received.Events[0].Name)
	}
}

func TestFlush_EmptyIsNoop(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	t.Setenv("TELEMETRY_ENABLED", "true")
	os.Unsetenv("DO_NOT_TRACK")

	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	t.Setenv("TELEMETRY_ENDPOINT", ts.URL)

	Init("test-id", "0.1.0", "test")

	globalClient.flush()
	time.Sleep(50 * time.Millisecond)

	if called {
		t.Error("flush should not send when no events are buffered")
	}
}

func TestDurationBucket(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{30, "<1m"},
		{59.9, "<1m"},
		{60, "1-5m"},
		{180, "1-5m"},
		{300, "5-15m"},
		{600, "5-15m"},
		{900, ">15m"},
		{3600, ">15m"},
	}
	for _, tt := range tests {
		got := DurationBucket(tt.seconds)
		if got != tt.want {
			t.Errorf("DurationBucket(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestCountBucket(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{0, "0"},
		{1, "1-5"},
		{5, "1-5"},
		{6, "6-20"},
		{20, "6-20"},
		{21, "21-50"},
		{50, "21-50"},
		{51, "50+"},
		{200, "50+"},
	}
	for _, tt := range tests {
		got := CountBucket(tt.count)
		if got != tt.want {
			t.Errorf("CountBucket(%d) = %q, want %q", tt.count, got, tt.want)
		}
	}
}

func TestGenerateUUID(t *testing.T) {
	id1 := generateUUID()
	id2 := generateUUID()

	if len(id1) != 36 {
		t.Errorf("UUID should be 36 chars, got %d: %q", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("two generated UUIDs should be different")
	}

	// Verify format: 8-4-4-4-12
	if id1[8] != '-' || id1[13] != '-' || id1[18] != '-' || id1[23] != '-' {
		t.Errorf("UUID format invalid: %q", id1)
	}
}

func TestIsEnabled_Returns_False_Before_Init(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	if IsEnabled() {
		t.Error("IsEnabled should return false before Init")
	}
}

func TestShutdown_NilClientIsNoop(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	Shutdown() // Should not panic
}

func TestTrackHelpers(t *testing.T) {
	resetGlobal()
	defer resetGlobal()

	t.Setenv("TELEMETRY_ENABLED", "true")
	os.Unsetenv("DO_NOT_TRACK")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	t.Setenv("TELEMETRY_ENDPOINT", ts.URL)

	Init("test-id", "0.1.0", "test")

	// None of these should panic
	TrackServerStarted(map[string]any{"port": "8080"})
	TrackServerStopped()
	TrackProjectCreated("bigquery", "claude", "gaming")
	TrackDiscoveryCompleted("bigquery", "claude", "gaming", "match-3", 120, 5, 3, 45)
	TrackDiscoveryFailed("snowflake", "openai", "social", "warehouse_connection_failed")

	globalClient.mu.Lock()
	count := len(globalClient.events)
	globalClient.mu.Unlock()

	if count != 5 {
		t.Errorf("expected 5 events from helpers, got %d", count)
	}
}
