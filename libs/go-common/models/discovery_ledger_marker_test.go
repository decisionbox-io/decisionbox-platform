package models

import (
	"testing"
	"time"
)

func TestReflectionMarker_Fresh(t *testing.T) {
	t.Setenv(ReflectionTimeoutEnv, "")
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	if !(ReflectionMarker{RunID: "r", Since: now.Add(-time.Minute)}).Fresh(now) {
		t.Error("an entry a minute old is an update in progress")
	}
	// Past the budget plus its hour of slack, the agent that wrote it is gone.
	if (ReflectionMarker{RunID: "r", Since: now.Add(-(DefaultReflectionTimeout + time.Hour + time.Second))}).Fresh(now) {
		t.Error("an entry older than the TTL must not be believed")
	}
	// An entry with no time cannot be placed, so it is not evidence of anything.
	if (ReflectionMarker{RunID: "r"}).Fresh(now) {
		t.Error("an entry with no `since` must not be believed")
	}

	// A deployment that raised the budget keeps believing its own long runs.
	t.Setenv(ReflectionTimeoutEnv, "6h")
	if !(ReflectionMarker{RunID: "r", Since: now.Add(-5 * time.Hour)}).Fresh(now) {
		t.Error("a 5h-old entry under a 6h budget is still an update in progress")
	}
	if got := ReflectionTimeout(); got != 6*time.Hour {
		t.Errorf("ReflectionTimeout() = %v, want the deployment's 6h", got)
	}
}
