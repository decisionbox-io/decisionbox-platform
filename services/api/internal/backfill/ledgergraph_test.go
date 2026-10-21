package backfill

import (
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// TestReflectionMarkerOutlivesThePhaseBudget pins the one relationship that
// makes the in-flight marker safe to ignore.
//
// The backfill skips a project whose ledger is marked as being written, and
// believes that marker for commonmodels.ReflectionMarkerTTL — a ceiling that
// exists because an agent killed mid-phase never clears its own marker. The
// ceiling therefore has to sit strictly above the longest a legitimate
// reflection can run, which is DISCOVERY_REFLECTION_TIMEOUT, on the default and
// on a deployment that raised it. Below it, the backfill would declare a running
// reflection stale and race the finding writes the marker exists to protect.
func TestReflectionMarkerOutlivesThePhaseBudget(t *testing.T) {
	t.Setenv(commonmodels.ReflectionTimeoutEnv, "")
	if got := commonmodels.ReflectionMarkerTTL(); got <= commonmodels.DefaultReflectionTimeout {
		t.Errorf("marker TTL = %v, must exceed the default reflection budget of %v", got, commonmodels.DefaultReflectionTimeout)
	}

	t.Setenv(commonmodels.ReflectionTimeoutEnv, "6h")
	if got := commonmodels.ReflectionMarkerTTL(); got <= 6*time.Hour {
		t.Errorf("marker TTL = %v with a 6h budget, must exceed it", got)
	}
}
