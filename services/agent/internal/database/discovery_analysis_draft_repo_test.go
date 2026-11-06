package database

import (
	"context"
	"errors"
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
			_, err := repo.SaveInsightVerdict(ctx, tc.in)
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

// TestSafeMarkerKey_RejectsIdsThatCannotBeBSONFieldNames — the ids are
// normally minted UUIDs, but a model that supplies its own id for an insight
// keeps it: the analysis parser only mints one when the field is absent.
func TestSafeMarkerKey_RejectsIdsThatCannotBeBSONFieldNames(t *testing.T) {
	for _, ok := range []string{
		"6f1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d",
		"insight-1",
		"a$b", // a "$" is only a problem at the start
	} {
		if !safeMarkerKey(ok) {
			t.Errorf("%q should be usable as a marker key", ok)
		}
	}
	for _, bad := range []string{
		"",
		"finding.1",   // becomes a NESTED field, not a marker
		"$set",        // reads as an operator
		"a\x00b",      // invalid outright
		"cohort.7.id", // several
	} {
		if safeMarkerKey(bad) {
			t.Errorf("%q must be refused as a marker key", bad)
		}
	}
}

// TestSaveInsightVerdict_RefusesAnIdThatWouldNestInsteadOfKey is the
// regression. A dotted id silently became a nested field, so the row came
// back carrying a zero-valued marker under the prefix: the budget counted it
// while the restore could not match it, and at the cap an insight the previous
// attempt had verified ended up skipped for budget instead of restored.
//
// Refusing it degrades the way a failed marker write already does — the
// insight is validated again — which is a cost rather than a corruption.
func TestSaveInsightVerdict_RefusesAnIdThatWouldNestInsteadOfKey(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	_, err := repo.SaveInsightVerdict(context.Background(), InsightVerdictInput{
		RunID: "run-1", AreaID: "churn", InsightID: "finding.1",
	})
	if !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Fatalf("want ErrUnsafeMarkerKey, got %v", err)
	}
}

// TestMarkersForInsights_DropsAMarkerNamingNothingInTheDraft keeps the budget
// arithmetic sound regardless of how a key reached the document: a marker no
// insight can claim would charge the cap for work nobody restores.
func TestMarkersForInsights_DropsAMarkerNamingNothingInTheDraft(t *testing.T) {
	insights := []models.Insight{{ID: "i1"}, {ID: "i2"}}
	markers := map[string]models.ValidationResult{
		"i1":    {InsightID: "i1"},
		"ghost": {InsightID: "ghost"},
	}
	got := markersForInsights(insights, markers, "run-1", "churn")
	if len(got) != 1 {
		t.Fatalf("kept %d markers, want 1", len(got))
	}
	if _, ok := got["i1"]; !ok {
		t.Error("dropped a marker for an insight the draft carries")
	}
	if _, ok := got["ghost"]; ok {
		t.Error("kept a marker no insight can claim; the cap would be charged for it")
	}

	if markersForInsights(insights, nil, "r", "a") != nil {
		t.Error("no markers should yield nil")
	}
	if markersForInsights(nil, markers, "r", "a") != nil {
		t.Error("a draft carrying no insights can claim no marker")
	}
}

// TestMarkerKeysUsable_RefusesUnsafeAndAmbiguousIds — a checkpoint can carry
// per-item markers only if every document is nameable by exactly one of them.
func TestMarkerKeysUsable_RefusesUnsafeAndAmbiguousIds(t *testing.T) {
	if err := markerKeysUsable([]string{"i1", "i2", "i3"}); err != nil {
		t.Errorf("distinct safe ids must be usable, got %v", err)
	}
	if err := markerKeysUsable(nil); err != nil {
		t.Errorf("no documents means nothing to key, got %v", err)
	}
	if err := markerKeysUsable([]string{"i1", "finding.2"}); !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Errorf("want ErrUnsafeMarkerKey, got %v", err)
	}
	if err := markerKeysUsable([]string{"i1", "i2", "i1"}); !errors.Is(err, ErrAmbiguousMarkerKey) {
		t.Errorf("want ErrAmbiguousMarkerKey, got %v", err)
	}
}

// TestSaveDraft_RefusesAnAreaWhoseInsightsShareAnId is the regression, and the
// most serious of the id-shape family because the consequence is a validation
// BYPASS rather than a lost marker.
//
// A model that supplies its own ids can repeat one. With two insights sharing
// an id, the single marker written for the first would be restored onto both —
// so the second skipped the verifier and could reach the recommender carrying
// a `confirmed` verdict an unrelated finding earned, and the late remint would
// then hand them distinct ids without correcting either verdict.
func TestSaveDraft_RefusesAnAreaWhoseInsightsShareAnId(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	step := models.AnalysisStep{
		AreaID: "churn",
		Insights: []models.Insight{
			{ID: "i1", Name: "first finding"},
			{ID: "i1", Name: "a different finding the model gave the same id"},
		},
	}
	err := repo.SaveDraft(context.Background(), AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	})
	if !errors.Is(err, ErrAmbiguousMarkerKey) {
		t.Fatalf("want ErrAmbiguousMarkerKey, got %v", err)
	}
}

// TestSaveDraft_RefusesAnAreaWithAnUnsafeInsightId — refused at the draft
// write too, not only at the marker write, so no row exists whose markers
// could be partial in a way the budget would miscount.
func TestSaveDraft_RefusesAnAreaWithAnUnsafeInsightId(t *testing.T) {
	repo := &DiscoveryAnalysisDraftRepository{}
	step := models.AnalysisStep{
		AreaID:   "churn",
		Insights: []models.Insight{{ID: "finding.1"}},
	}
	err := repo.SaveDraft(context.Background(), AnalysisAreaDraftInput{
		ProjectID: "p1", RunID: "run-1", Attempt: 1, ExplorationAttempt: 1, Step: step,
	})
	if !errors.Is(err, ErrUnsafeMarkerKey) {
		t.Fatalf("want ErrUnsafeMarkerKey, got %v", err)
	}
}
