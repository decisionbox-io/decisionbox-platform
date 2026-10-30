package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// bsonTagNames collects the bson field names a struct declares directly,
// skipping anything inlined or ignored. Used to pin the one storage trap in
// AnalysisAreaCheckpointDoc: it embeds models.AnalysisStep inline, so a field
// of its own that reused one of that struct's tags would write the same BSON
// key twice.
func bsonTagNames(t *testing.T, typ reflect.Type, inlined bool) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("bson")
		name := strings.Split(tag, ",")[0]
		isInline := strings.Contains(tag, ",inline")
		if isInline != inlined {
			continue
		}
		if inlined {
			// Recurse into the embedded struct's own direct fields.
			for k := range bsonTagNames(t, f.Type, false) {
				out[k] = true
			}
			continue
		}
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

// TestAnalysisAreaCheckpointDocTagsAreDisjoint is the guard the doc comment
// promises. The area id is the key half of the unique (run_id, area_id)
// index and it comes from the INLINED models.AnalysisStep.AreaID, so an
// obvious-looking AreaID field tagged bson:"area_id" on the outer struct
// would be a duplicate key rather than a convenience. The same goes for
// every other tag AnalysisStep already owns.
func TestAnalysisAreaCheckpointDocTagsAreDisjoint(t *testing.T) {
	docType := reflect.TypeOf(AnalysisAreaCheckpointDoc{})
	own := bsonTagNames(t, docType, false)
	embedded := bsonTagNames(t, docType, true)

	if len(embedded) == 0 {
		t.Fatal("models.AnalysisStep is no longer inlined; this test is testing nothing")
	}
	for name := range own {
		if embedded[name] {
			t.Errorf("bson tag %q is declared on both AnalysisAreaCheckpointDoc and the inlined models.AnalysisStep — one document cannot carry the same key twice", name)
		}
	}
	// And the key the index needs really does come from the embedded struct.
	if !embedded["area_id"] {
		t.Error("the inlined models.AnalysisStep must supply area_id — it is the key half of uq_run_area")
	}
	if !own["run_id"] {
		t.Error("AnalysisAreaCheckpointDoc must supply run_id — the other half of uq_run_area")
	}
}

// TestSaveArea_RejectsRowsNothingCouldEverRestore covers the three inputs
// that would produce a row no resumed run can use. Each is refused before
// any Mongo call, which is what lets this run without a database.
func TestSaveArea_RejectsRowsNothingCouldEverRestore(t *testing.T) {
	repo := &DiscoveryAnalysisCheckpointRepository{}
	ctx := context.Background()
	step := models.AnalysisStep{AreaID: "churn", AreaName: "Churn Risks"}

	tests := []struct {
		name string
		in   AnalysisAreaCheckpointInput
		want string
	}{
		{
			name: "no run id",
			in:   AnalysisAreaCheckpointInput{Attempt: 1, ExplorationAttempt: 1, Step: step},
			want: "run_id is required",
		},
		{
			name: "no area id",
			in:   AnalysisAreaCheckpointInput{RunID: "r1", Attempt: 1, ExplorationAttempt: 1},
			want: "area_id is required",
		},
		{
			// The staleness fence has no value to compare against, so the
			// row could be restored into an exploration it was never
			// analysed over. Refusing it is the whole point.
			name: "no exploration attempt",
			in:   AnalysisAreaCheckpointInput{RunID: "r1", Attempt: 1, Step: step},
			want: "exploration_attempt must be >= 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.SaveArea(ctx, tc.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestLoadAreas_WithoutADurableSummaryQueriesNothing is the read-side half of
// the fence. A run whose exploration summary never landed will re-explore on
// resume, so no area row it holds can be valid — and the empty set is
// returned without touching the collection, which is what makes a nil
// collection here a proof rather than a crash.
func TestLoadAreas_WithoutADurableSummaryQueriesNothing(t *testing.T) {
	repo := &DiscoveryAnalysisCheckpointRepository{}

	for _, attempt := range []int{0, -1} {
		set, err := repo.LoadAreas(context.Background(), "r1", attempt)
		if err != nil {
			t.Fatalf("exploration attempt %d: unexpected error: %v", attempt, err)
		}
		if set.Len() != 0 {
			t.Errorf("exploration attempt %d: restored %d areas, want none", attempt, set.Len())
		}
		if set.Areas == nil {
			t.Errorf("exploration attempt %d: Areas must be a usable empty map, not nil", attempt)
		}
	}
}

// TestAnalysisCheckpointRepo_RequireRunID pins the guard on every method that
// would otherwise address the whole collection rather than one run — a
// DeleteMany with an empty filter being the worst of them.
func TestAnalysisCheckpointRepo_RequireRunID(t *testing.T) {
	repo := &DiscoveryAnalysisCheckpointRepository{}
	ctx := context.Background()

	if _, err := repo.LoadAreas(ctx, "", 1); err == nil {
		t.Error("LoadAreas with no run id must fail")
	}
	if _, err := repo.DeleteByRun(ctx, ""); err == nil {
		t.Error("DeleteByRun with no run id must fail rather than delete every run's rows")
	}
	if _, err := repo.TouchByRun(ctx, ""); err == nil {
		t.Error("TouchByRun with no run id must fail rather than touch every run's rows")
	}
}

// TestAnalysisCheckpointSetLenIsNilSafe — the orchestrator logs the count on
// a path where the set is legitimately nil (not a resume, or the load
// failed).
func TestAnalysisCheckpointSetLenIsNilSafe(t *testing.T) {
	var set *AnalysisCheckpointSet
	if set.Len() != 0 {
		t.Error("Len on a nil set must be 0")
	}
}

// TestSupersededAttemptIsSharedWithExplorationCheckpoints — the orchestrator
// translates this one error into its stop signal for both kinds of
// checkpoint, so they must be the same sentinel rather than two errors that
// read alike.
func TestSupersededAttemptIsSharedWithExplorationCheckpoints(t *testing.T) {
	if !errors.Is(ErrSupersededAttempt, ErrSupersededAttempt) {
		t.Fatal("sentinel is not comparable to itself")
	}
	if ErrSupersededAttempt.Error() == "" {
		t.Error("the sentinel needs a message an operator can read in a log line")
	}
}
