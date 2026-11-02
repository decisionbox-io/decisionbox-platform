//go:build integration

package discovery

import (
	"context"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// countRunStepPoints reports how many points a run's per-run collection
// holds. The duplicate-vs-no-op distinction these tests turn on is only
// visible as a count.
func countRunStepPoints(t *testing.T, client *pb.Client, runID string) int {
	t.Helper()
	n, err := client.Count(context.Background(), &pb.CountPoints{
		CollectionName: RunStepIndexCollectionName(runID),
		Exact:          pb.PtrOf(true),
	})
	if err != nil {
		t.Fatalf("count points for %s: %v", runID, err)
	}
	return int(n)
}

// Resume and the per-run step index.
//
// Replay re-indexes the prefix it replays, and that is only safe because the
// operation is exact and idempotent: the embedded text is the step's purpose
// plus its query — both checkpointed — and the point id is derived from
// (runID, step). These tests prove it against a real Qdrant, because the
// property they assert is a property of Qdrant's upsert semantics, not of
// our code reading its own writes.
//
// It matters for the one case nothing else covers: a run killed hard enough
// that no deferred cleanup ran, or whose collection the boot sweep already
// dropped. Resume must work either way.

// TestInteg_Resume_ReindexingAReplayedPrefixIsIdempotent is the guarantee
// that lets replay re-index unconditionally rather than having to know
// whether the collection survived the crash.
func TestInteg_Resume_ReindexingAReplayedPrefixIsIdempotent(t *testing.T) {
	client := startRunStepQdrant(t)
	embedder := &fixedDimEmbedder{dim: 8}
	ctx := context.Background()

	idx, err := NewRunStepIndex(client, embedder, "RUN_RESUME_IDEMPOTENT")
	if err != nil {
		t.Fatalf("NewRunStepIndex: %v", err)
	}

	prefix := []models.ExplorationStep{
		{Step: 1, QueryPurpose: "retention by cohort", Query: "SELECT cohort, COUNT(*) FROM ds.users GROUP BY 1", RowCount: 12},
		{Step: 2, QueryPurpose: "spend by tier", Query: "SELECT tier, SUM(spend) FROM ds.orders GROUP BY 1", RowCount: 7},
		{Step: 3, QueryPurpose: "churn by week", Query: "SELECT week, churned FROM ds.churn", RowCount: 30},
	}

	// Attempt 1 indexes them as exploration runs.
	for _, s := range prefix {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatalf("attempt 1 upsert step %d: %v", s.Step, err)
		}
	}
	before := countRunStepPoints(t, client, "RUN_RESUME_IDEMPOTENT")
	if before != 3 {
		t.Fatalf("points after attempt 1 = %d, want 3", before)
	}

	// The run is resumed and replays the same prefix into a collection that
	// survived. Re-indexing must be a no-op upsert, NOT three new points —
	// duplicates would make the analysis picker rank the same evidence
	// several times and spend its budget on it.
	for _, s := range prefix {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatalf("replay upsert step %d: %v", s.Step, err)
		}
	}
	if after := countRunStepPoints(t, client, "RUN_RESUME_IDEMPOTENT"); after != before {
		t.Errorf("points after replay = %d, want %d — the point id must be derived from (runID, step)", after, before)
	}

	// And the replayed steps are still retrievable, so the analysis picker
	// sees the pre-crash half of the run.
	// Retrievable, and still exactly three of them — the replay must not
	// have turned one step into two.
	hits, err := idx.Search(ctx, "retention by cohort", RunStepIndexSearchOpts{TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	seen := map[int]int{}
	for _, h := range hits {
		seen[h.Step]++
	}
	for _, want := range []int{1, 2, 3} {
		switch seen[want] {
		case 1: // as expected
		case 0:
			t.Errorf("step %d is not searchable after the replay; hits = %+v", want, hits)
		default:
			t.Errorf("step %d appears %d times — the replay duplicated it", want, seen[want])
		}
	}
}

// TestInteg_Resume_RebuildsAnIndexThatWasSwept is the other half, and the
// case resume cannot afford to depend on: the run was killed hard (no
// deferred Drop ran) or the boot sweep already reclaimed its collection.
// Replay has to rebuild it from the checkpoints rather than fail.
func TestInteg_Resume_RebuildsAnIndexThatWasSwept(t *testing.T) {
	client := startRunStepQdrant(t)
	embedder := &fixedDimEmbedder{dim: 8}
	ctx := context.Background()

	const runID = "RUN_RESUME_SWEPT"
	prefix := []models.ExplorationStep{
		{Step: 1, QueryPurpose: "retention by cohort", Query: "SELECT cohort FROM ds.users", RowCount: 5},
		{Step: 2, QueryPurpose: "spend by tier", Query: "SELECT tier FROM ds.orders", RowCount: 9},
	}

	first, err := NewRunStepIndex(client, embedder, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range prefix {
		if err := first.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// The sweep fires, or the run's own deferred Drop ran before it failed.
	if err := first.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	// A fresh process resumes the run. Same runID, so the same collection
	// name — which no longer exists.
	resumed, err := NewRunStepIndex(client, embedder, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range prefix {
		if err := resumed.Upsert(ctx, s); err != nil {
			t.Fatalf("re-indexing into a swept collection must recreate it, got: %v", err)
		}
	}
	if n := countRunStepPoints(t, client, runID); n != 2 {
		t.Errorf("points after rebuild = %d, want 2", n)
	}

	// Both steps must be retrievable again. Which one ranks first is a
	// property of the real embedder, not of the rebuild — the test embedder
	// is a hash, so asserting an order here would pin nothing but its
	// arithmetic.
	hits, err := resumed.Search(ctx, "spend by tier", RunStepIndexSearchOpts{TopK: 10})
	if err != nil {
		t.Fatalf("Search after rebuild: %v", err)
	}
	seen := map[int]bool{}
	for _, h := range hits {
		seen[h.Step] = true
	}
	for _, want := range []int{1, 2} {
		if !seen[want] {
			t.Errorf("step %d is not retrievable after the rebuild; hits = %+v", want, hits)
		}
	}
}

// TestInteg_Resume_ReplayedAndNewStepsShareOneIndex pins what the analysis
// phase actually needs: after a resume the picker ranks over the WHOLE run,
// not just the steps this process executed. A resumed run whose index held
// only its new steps would analyse a fraction of the evidence it paid for.
func TestInteg_Resume_ReplayedAndNewStepsShareOneIndex(t *testing.T) {
	client := startRunStepQdrant(t)
	embedder := &fixedDimEmbedder{dim: 8}
	ctx := context.Background()

	const runID = "RUN_RESUME_MIXED"
	idx, err := NewRunStepIndex(client, embedder, runID)
	if err != nil {
		t.Fatal(err)
	}

	// Steps 1-2 come from the crashed attempt's checkpoints.
	replayed := []models.ExplorationStep{
		{Step: 1, QueryPurpose: "retention by cohort", Query: "SELECT cohort FROM ds.users", RowCount: 5},
		{Step: 2, QueryPurpose: "spend by tier", Query: "SELECT tier FROM ds.orders", RowCount: 9},
	}
	for _, s := range replayed {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Steps 3-4 are executed fresh by the resumed run.
	fresh := []models.ExplorationStep{
		{Step: 3, QueryPurpose: "refund rate by region", Query: "SELECT region, refunds FROM ds.refunds", RowCount: 14},
		{Step: 4, QueryPurpose: "session length by platform", Query: "SELECT platform, len FROM ds.sessions", RowCount: 22},
	}
	for _, s := range fresh {
		if err := idx.Upsert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	if n := countRunStepPoints(t, client, runID); n != 4 {
		t.Fatalf("points = %d, want 4 (2 replayed + 2 new)", n)
	}

	// A search over a wide TopK must reach both halves.
	hits, err := idx.Search(ctx, "retention by cohort", RunStepIndexSearchOpts{TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	seen := map[int]bool{}
	for _, h := range hits {
		seen[h.Step] = true
	}
	for _, want := range []int{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("step %d is not retrievable after the resume; hits = %+v", want, hits)
		}
	}
}
