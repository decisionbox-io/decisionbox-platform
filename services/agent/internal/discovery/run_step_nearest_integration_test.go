//go:build integration

package discovery

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Nearest against a real Qdrant.
//
// What is proved here is the query: that it reaches the store, reads the top
// score back, excludes the step being judged, and reports "nothing to compare
// against" for the two cases that legitimately have nothing — an empty
// collection and a step with no text. Those are exactly the answers the
// stopping rule reads as unjudgeable, and getting any of them wrong ends
// runs early or never.
//
// What is NOT proved here is where the similarity threshold sits. The shared
// harness embeds by hashing, so identical text scores 1 and anything else
// scores arbitrarily — enough to exercise the plumbing, and useless for
// calibrating a cutoff. That calibration needs a real embedding model and
// belongs to the live evaluation, not to this test.

func newNearestIndex(t *testing.T, runID string) RunStepIndex {
	t.Helper()
	idx, err := NewRunStepIndex(startRunStepQdrant(t), &fixedDimEmbedder{dim: 8}, runID)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	return idx
}

func TestIntegration_Nearest_EmptyIndexHasNothingToCompare(t *testing.T) {
	idx := newNearestIndex(t, "nearest-empty")
	_, found, err := idx.Nearest(context.Background(), models.ExplorationStep{
		Step: 1, QueryPurpose: "sessions by channel", Query: "SELECT 1",
	})
	if err != nil {
		t.Fatalf("Nearest on an empty index errored: %v", err)
	}
	if found {
		t.Error("an empty index reported something to compare against")
	}
}

func TestIntegration_Nearest_FindsAnIdenticalEarlierStep(t *testing.T) {
	ctx := context.Background()
	idx := newNearestIndex(t, "nearest-identical")

	first := models.ExplorationStep{Step: 1, QueryPurpose: "sessions by channel", Query: "SELECT channel, sessions FROM t"}
	if err := idx.Upsert(ctx, first); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// The same question asked again, at a later step number.
	again := first
	again.Step = 2
	score, found, err := idx.Nearest(ctx, again)
	if err != nil {
		t.Fatalf("Nearest: %v", err)
	}
	if !found {
		t.Fatal("a step identical to an indexed one found nothing to compare against")
	}
	if score < 0.99 {
		t.Errorf("an identical step scored %v against its twin; want ~1", score)
	}
}

func TestIntegration_Nearest_ExcludesTheStepBeingJudged(t *testing.T) {
	ctx := context.Background()
	idx := newNearestIndex(t, "nearest-self")

	step := models.ExplorationStep{Step: 7, QueryPurpose: "revenue by country", Query: "SELECT country, revenue FROM t"}
	// Index it FIRST, which is the order the engine avoids — the guard
	// exists so a caller that gets it wrong does not measure a step against
	// itself and conclude every run is repetitive.
	if err := idx.Upsert(ctx, step); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_, found, err := idx.Nearest(ctx, step)
	if err != nil {
		t.Fatalf("Nearest: %v", err)
	}
	if found {
		t.Error("a step was judged against itself")
	}
}

func TestIntegration_Nearest_AStepWithNoTextIsUnjudgeable(t *testing.T) {
	ctx := context.Background()
	idx := newNearestIndex(t, "nearest-empty-text")
	if err := idx.Upsert(ctx, models.ExplorationStep{Step: 1, QueryPurpose: "p", Query: "SELECT 1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A lookup_schema step carries neither a purpose nor a query. It says
	// nothing, so it is not evidence that the run found new ground — and it
	// must not error, since it happens on ordinary runs.
	_, found, err := idx.Nearest(ctx, models.ExplorationStep{Step: 2, Action: "lookup_schema"})
	if err != nil {
		t.Fatalf("Nearest on a text-less step errored: %v", err)
	}
	if found {
		t.Error("a step with nothing to embed was judged anyway")
	}
}
