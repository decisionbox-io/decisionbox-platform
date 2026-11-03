package database

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestAnalysisAreaDraftDocTagsAreDisjoint is the guard the doc comment
// promises, and the same one the analysis checkpoint doc needs: the area id is
// the key half of the unique (run_id, area_id) index and it comes from the
// INLINED models.AnalysisStep.AreaID, so an obvious-looking AreaID field on
// the outer struct would write the key twice.
func TestAnalysisAreaDraftDocTagsAreDisjoint(t *testing.T) {
	docType := reflect.TypeOf(AnalysisAreaDraftDoc{})
	own := bsonTagNames(t, docType, false)
	embedded := bsonTagNames(t, docType, true)

	if len(embedded) == 0 {
		t.Fatal("models.AnalysisStep is no longer inlined; this test is testing nothing")
	}
	for name := range own {
		if embedded[name] {
			t.Errorf("bson tag %q is declared on both AnalysisAreaDraftDoc and the inlined models.AnalysisStep — one document cannot carry the same key twice", name)
		}
	}
	if !embedded["area_id"] {
		t.Error("the inlined models.AnalysisStep must supply area_id — it is the key half of uq_run_area")
	}
	if !own["run_id"] {
		t.Error("AnalysisAreaDraftDoc must supply run_id — the other half of uq_run_area")
	}
	// The marker map is the point of the row; a rename that silently dropped
	// it would make every resume re-validate everything while looking fine.
	if !own["insight_verdicts"] {
		t.Error("AnalysisAreaDraftDoc must supply insight_verdicts — the per-insight completion markers")
	}
}

// TestSaveDraft_RejectsRowsNothingCouldEverRestore covers the three inputs
// that would produce a row no resumed run can use. Each is refused before any
// Mongo call, which is what lets this run without a database.
func TestSaveDraft_RejectsRowsNothingCouldEverRestore(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	ctx := context.Background()
	step := models.AnalysisStep{AreaID: "churn", AreaName: "Churn Risks"}

	tests := []struct {
		name string
		in   AnalysisAreaDraftInput
		want string
	}{
		{
			name: "no run id",
			in:   AnalysisAreaDraftInput{Step: step, ExplorationAttempt: 1},
			want: "run_id is required",
		},
		{
			name: "no area id",
			in:   AnalysisAreaDraftInput{RunID: "run-1", ExplorationAttempt: 1},
			want: "area_id is required",
		},
		{
			name: "no durable exploration summary",
			in:   AnalysisAreaDraftInput{RunID: "run-1", Step: step},
			want: "exploration_attempt must be >= 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.SaveDraft(ctx, tc.in)
			if err == nil {
				t.Fatal("expected a refusal, got nil — a row nothing can restore would have been written")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestSaveInsightVerdict_RejectsUnaddressableMarkers covers the inputs that
// could not name a document to update. Refused before any Mongo call.
//
// The insight id matters most: it becomes a field path, so an empty one would
// write the verdict to the field "insight_verdicts." and a later restore would
// find a marker it cannot match to any insight.
func TestSaveInsightVerdict_RejectsUnaddressableMarkers(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	ctx := context.Background()

	tests := []struct {
		name string
		in   InsightVerdictInput
		want string
	}{
		{"no run id", InsightVerdictInput{AreaID: "churn", InsightID: "i1"}, "run_id is required"},
		{"no area id", InsightVerdictInput{RunID: "run-1", InsightID: "i1"}, "area_id is required"},
		{"no insight id", InsightVerdictInput{RunID: "run-1", AreaID: "churn"}, "insight_id is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.SaveInsightVerdict(ctx, tc.in)
			if err == nil {
				t.Fatal("expected a refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestDraftRepoRunScopedCallsRequireARunID covers the three whole-run
// operations. Each is a DeleteMany / UpdateMany on run_id, so an empty one
// would be a filter matching every row of every run.
func TestDraftRepoRunScopedCallsRequireARunID(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	ctx := context.Background()

	if _, err := repo.LoadDrafts(ctx, "", 1); err == nil {
		t.Error("LoadDrafts accepted an empty run id")
	}
	if _, err := repo.DeleteByRun(ctx, ""); err == nil {
		t.Error("DeleteByRun accepted an empty run id — that filter would match every run")
	}
	if _, err := repo.TouchByRun(ctx, ""); err == nil {
		t.Error("TouchByRun accepted an empty run id — that filter would match every run")
	}
	if err := repo.DeleteByArea(ctx, "", "churn"); err == nil {
		t.Error("DeleteByArea accepted an empty run id")
	}
	if err := repo.DeleteByArea(ctx, "run-1", ""); err == nil {
		t.Error("DeleteByArea accepted an empty area id — that would delete an arbitrary area's draft")
	}
}

// TestLoadDrafts_NoDurableSummaryReturnsEmptyWithoutQuerying pins the gate
// that makes a run with no exploration summary cost nothing: there is no
// exploration such a draft could be restored against, so no query is worth
// making. It runs against a repository with no collection at all, which is
// what proves the query never happened.
func TestLoadDrafts_NoDurableSummaryReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	set, err := repo.LoadDrafts(context.Background(), "run-1", 0)
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if set.Len() != 0 {
		t.Errorf("expected an empty set, got %d drafts", set.Len())
	}
}

// TestAnalysisDraftSetLenIsNilSafe — the orchestrator calls Len() on the
// result of a load it may have failed, so a nil set must answer zero rather
// than panic.
func TestAnalysisDraftSetLenIsNilSafe(t *testing.T) {
	var set *AnalysisDraftSet
	if set.Len() != 0 {
		t.Error("nil AnalysisDraftSet should report zero drafts")
	}
}
