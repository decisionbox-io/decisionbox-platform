package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- fakes -----------------------------------------------------------------

// fakeAnalysisDraftStore records what the orchestrator wrote and lets a test
// make any call fail. The mutex is not decoration: SaveInsightVerdict is
// called from validationPhase's bounded workers.
type fakeAnalysisDraftStore struct {
	mu sync.Mutex

	saved    []database.AnalysisAreaDraftInput
	verdicts []database.InsightVerdictInput
	loads    []int
	deletes  []string
	areaDels [][2]string
	touched  []string

	drafts     map[string]models.AnalysisAreaDraft
	setAttempt int

	saveErr    error
	verdictErr error
	verdictNoRow bool
	loadErr    error
	deleteErr  error
	touchErr   error
	areaDelErr error
}

func (f *fakeAnalysisDraftStore) SaveDraft(_ context.Context, in database.AnalysisAreaDraftInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, in)
	return nil
}

func (f *fakeAnalysisDraftStore) SaveInsightVerdict(_ context.Context, in database.InsightVerdictInput) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verdictErr != nil {
		return false, f.verdictErr
	}
	// verdictNoRow reproduces the real store's MatchedCount-0 outcome: the
	// write succeeded and matched nothing, because the draft was swept, deleted
	// or fenced out by a newer attempt.
	if f.verdictNoRow {
		return false, nil
	}
	f.verdicts = append(f.verdicts, in)
	return true, nil
}

func (f *fakeAnalysisDraftStore) LoadDrafts(_ context.Context, _ string, explorationAttempt int) (*database.AnalysisDraftSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, explorationAttempt)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	drafts := f.drafts
	if drafts == nil {
		drafts = map[string]models.AnalysisAreaDraft{}
	}
	return &database.AnalysisDraftSet{Drafts: drafts, Attempt: f.setAttempt}, nil
}

func (f *fakeAnalysisDraftStore) DeleteByArea(_ context.Context, runID, areaID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.areaDelErr != nil {
		return f.areaDelErr
	}
	f.areaDels = append(f.areaDels, [2]string{runID, areaID})
	return nil
}

func (f *fakeAnalysisDraftStore) DeleteByRun(_ context.Context, runID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deletes = append(f.deletes, runID)
	return 3, nil
}

func (f *fakeAnalysisDraftStore) TouchByRun(_ context.Context, runID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return 0, f.touchErr
	}
	f.touched = append(f.touched, runID)
	return 3, nil
}

// draftArea is an area as the Phase 4 loop body leaves it just before
// validation: analysed, figures settled and repaired, and NOTHING validated.
// The absence of ValidationResults and of per-insight Quality is the whole
// shape of a draft.
func draftArea(id string, insights int, at time.Time) models.AnalysisStep {
	step := models.AnalysisStep{
		AreaID:    id,
		AreaName:  "Area " + id,
		RunAt:     at,
		Prompt:    "the area prompt",
		Response:  `{"insights":[]}`,
		TokensIn:  1000,
		TokensOut: 200,
	}
	for i := 0; i < insights; i++ {
		step.Insights = append(step.Insights, models.Insight{
			ID:            fmt.Sprintf("%s-insight-%d", id, i),
			AnalysisArea:  id,
			Name:          fmt.Sprintf("Finding %d in %s", i, id),
			Severity:      "high",
			AffectedCount: 100 - i,
			SourceSteps:   []int{3, 9},
			DiscoveredAt:  at.Add(time.Second),
		})
	}
	return step
}

// --- checkpointAreaDraft ---------------------------------------------------

// TestCheckpointAreaDraft_NoOpsThatProtectTheFence covers every input that
// would produce a row no resumed run could legitimately use, or one that
// protects nothing.
func TestCheckpointAreaDraft_NoOpsThatProtectTheFence(t *testing.T) {
	ctx := context.Background()
	step := draftArea("churn", 2, time.Now())

	tests := []struct {
		name string
		o    *Orchestrator
		step models.AnalysisStep
		val  bool
		why  string
	}{
		{
			name: "nil store disables per-insight resume",
			o:    &Orchestrator{projectID: "p", runID: "r", explorationSummaryAttempt: 1},
			step: step, val: true,
			why: "a deployment without the store must take exactly the path it always took",
		},
		{
			name: "no durable exploration summary",
			o: &Orchestrator{
				projectID: "p", runID: "r",
				analysisDraftRepo: &fakeAnalysisDraftStore{},
				statusReporter:    reporterFor(newFakeRunDoc(), 1),
			},
			step: step, val: true,
			why: "such a run re-explores on resume, so LoadDrafts is obliged to ignore the row",
		},
		{
			name: "validation disabled, so the draft would protect nothing",
			o: &Orchestrator{
				projectID: "p", runID: "r",
				analysisDraftRepo:         &fakeAnalysisDraftStore{},
				explorationSummaryAttempt: 1,
				statusReporter:            reporterFor(newFakeRunDoc(), 1),
			},
			step: step, val: false,
			why: "with no validation agent the area reaches its checkpoint through two in-memory passes",
		},
		{
			name: "no insights, so there is no validation to resume",
			o: &Orchestrator{
				projectID: "p", runID: "r",
				analysisDraftRepo:         &fakeAnalysisDraftStore{},
				explorationSummaryAttempt: 1,
				statusReporter:            reporterFor(newFakeRunDoc(), 1),
			},
			step: draftArea("churn", 0, time.Now()), val: true,
			why: "an empty area is moments from its completed checkpoint",
		},
		{
			name: "no area id, so the row would be unrestorable",
			o: &Orchestrator{
				projectID: "p", runID: "r",
				analysisDraftRepo:         &fakeAnalysisDraftStore{},
				explorationSummaryAttempt: 1,
				statusReporter:            reporterFor(newFakeRunDoc(), 1),
			},
			step: func() models.AnalysisStep { s := draftArea("", 2, time.Now()); return s }(), val: true,
			why: "a row without an area id is a key nothing can match",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.o.checkpointAreaDraft(ctx, tc.step, tc.val); err != nil {
				t.Fatalf("want a quiet no-op (%s), got %v", tc.why, err)
			}
			if store, ok := tc.o.analysisDraftRepo.(*fakeAnalysisDraftStore); ok && len(store.saved) != 0 {
				t.Errorf("wrote a draft anyway: %s", tc.why)
			}
		})
	}
}

// TestCheckpointAreaDraft_StopsWhenTheRunHasBeenTakenOver covers both ways
// this attempt learns it has lost the run: the pre-write ownership read, and
// the row-level fence refusing the write.
//
// Both must surface as ai.ErrAttemptSuperseded rather than as a swallowed
// checkpoint failure, because the per-insight validation about to follow is
// the most expensive part of the area.
func TestCheckpointAreaDraft_StopsWhenTheRunHasBeenTakenOver(t *testing.T) {
	ctx := context.Background()
	step := draftArea("churn", 2, time.Now())

	t.Run("ownership lost before the write", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.owns = false
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(doc, 2),
		}
		if _, err := o.checkpointAreaDraft(ctx, step, true); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("want ai.ErrAttemptSuperseded, got %v", err)
		}
		if len(store.saved) != 0 {
			t.Error("wrote a draft the live attempt has not reached — a resume would restore it as this run's own work")
		}
	})

	t.Run("the row fence refuses the write", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{saveErr: database.ErrSupersededAttempt}
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		if _, err := o.checkpointAreaDraft(ctx, step, true); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("want ai.ErrAttemptSuperseded, got %v", err)
		}
	})
}

// TestCheckpointAreaDraft_SwallowsAnOrdinaryWriteFailure — a checkpoint must
// never be able to abort a working run. The cost of losing this row is that
// the area is analysed again on a later resume.
func TestCheckpointAreaDraft_SwallowsAnOrdinaryWriteFailure(t *testing.T) {
	store := &fakeAnalysisDraftStore{saveErr: errors.New("mongo down")}
	o := &Orchestrator{
		projectID: "p", runID: "r",
		analysisDraftRepo:         store,
		explorationSummaryAttempt: 1,
		statusReporter:            reporterFor(newFakeRunDoc(), 1),
	}
	if _, err := o.checkpointAreaDraft(context.Background(), draftArea("churn", 2, time.Now()), true); err != nil {
		t.Fatalf("a failed checkpoint write must not abort the run, got %v", err)
	}
}

// TestCheckpointAreaDraft_WritesTheWholeStepUnderBothFences pins what lands:
// the area's own attempt, the exploration attempt it was analysed over, and
// the step with its insights.
func TestCheckpointAreaDraft_WritesTheWholeStepUnderBothFences(t *testing.T) {
	store := &fakeAnalysisDraftStore{}
	o := &Orchestrator{
		projectID: "proj", runID: "run-1",
		analysisDraftRepo:         store,
		explorationSummaryAttempt: 2,
		resume:                    &ResumeState{Attempt: 3},
		statusReporter:            reporterFor(newFakeRunDoc(), 3),
	}
	step := draftArea("churn", 3, time.Now())
	if _, err := o.checkpointAreaDraft(context.Background(), step, true); err != nil {
		t.Fatalf("checkpointAreaDraft: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("want one draft written, got %d", len(store.saved))
	}
	got := store.saved[0]
	if got.RunID != "run-1" || got.ProjectID != "proj" {
		t.Errorf("wrong identity: %+v", got)
	}
	if got.Attempt != 3 {
		t.Errorf("attempt = %d, want this attempt's number 3 — the row fence depends on it", got.Attempt)
	}
	if got.ExplorationAttempt != 2 {
		t.Errorf("exploration_attempt = %d, want 2 — the staleness fence depends on it", got.ExplorationAttempt)
	}
	if len(got.Step.Insights) != 3 {
		t.Errorf("stored %d insights, want 3 — the insights are what make per-item resume possible", len(got.Step.Insights))
	}
}

// --- checkpointInsightVerdict ----------------------------------------------

// TestCheckpointInsightVerdict_NoOpsAndFailureIsSwallowed — a lost marker
// costs one re-validated insight, so nothing here may fail a run.
func TestCheckpointInsightVerdict_NoOpsAndFailureIsSwallowed(t *testing.T) {
	ctx := context.Background()
	vr := models.ValidationResult{InsightID: "i1", Combined: valmodels.StatusSupported}

	t.Run("nil store", func(t *testing.T) {
		o := &Orchestrator{runID: "r", explorationSummaryAttempt: 1}
		o.checkpointInsightVerdict(ctx, "churn", vr)
	})
	t.Run("no area id", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{runID: "r", analysisDraftRepo: store, explorationSummaryAttempt: 1}
		o.checkpointInsightVerdict(ctx, "", vr)
		if len(store.verdicts) != 0 {
			t.Error("wrote a marker with no area to attach it to")
		}
	})
	t.Run("no insight id", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{runID: "r", analysisDraftRepo: store, explorationSummaryAttempt: 1}
		o.checkpointInsightVerdict(ctx, "churn", models.ValidationResult{})
		if len(store.verdicts) != 0 {
			t.Error("wrote a marker keyed on an empty insight id — no restore could match it")
		}
	})
	t.Run("no durable exploration summary", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{runID: "r", analysisDraftRepo: store}
		o.checkpointInsightVerdict(ctx, "churn", vr)
		if len(store.verdicts) != 0 {
			t.Error("wrote a marker against a draft that could never be restored")
		}
	})
	t.Run("write failure is swallowed", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{verdictErr: errors.New("mongo down")}
		o := &Orchestrator{runID: "r", analysisDraftRepo: store, explorationSummaryAttempt: 1}
		o.checkpointInsightVerdict(ctx, "churn", vr)
	})
}

// TestCheckpointInsightVerdict_KeysOnTheInsightAndTheAttempt — the key is
// what a later attempt reads markers back by, and the attempt is the fence.
func TestCheckpointInsightVerdict_KeysOnTheInsightAndTheAttempt(t *testing.T) {
	store := &fakeAnalysisDraftStore{}
	o := &Orchestrator{
		runID: "run-1", analysisDraftRepo: store,
		explorationSummaryAttempt: 1,
		resume:                    &ResumeState{Attempt: 4},
	}
	vr := models.ValidationResult{InsightID: "churn-insight-1", Combined: valmodels.StatusConfirmed}
	o.checkpointInsightVerdict(context.Background(), "churn", vr)

	if len(store.verdicts) != 1 {
		t.Fatalf("want one marker, got %d", len(store.verdicts))
	}
	got := store.verdicts[0]
	if got.RunID != "run-1" || got.AreaID != "churn" || got.InsightID != "churn-insight-1" {
		t.Errorf("wrong key: %+v", got)
	}
	if got.Attempt != 4 {
		t.Errorf("attempt = %d, want 4", got.Attempt)
	}
	if got.Verdict.Combined != valmodels.StatusConfirmed {
		t.Errorf("stored verdict %q, want the one produced", got.Verdict.Combined)
	}
}

// --- loadAreaDrafts --------------------------------------------------------

// TestLoadAreaDrafts_GatesThatMakeTheSetEmpty — each gate makes the map empty
// for a reason, and two of them must not query at all.
func TestLoadAreaDrafts_GatesThatMakeTheSetEmpty(t *testing.T) {
	ctx := context.Background()
	drafts := map[string]models.AnalysisAreaDraft{
		"churn": {Step: draftArea("churn", 2, time.Now())},
	}

	t.Run("nil store", func(t *testing.T) {
		o := &Orchestrator{runID: "r", resume: &ResumeState{Attempt: 2}}
		if got := o.loadAreaDrafts(ctx, nil); got != nil {
			t.Errorf("want nil, got %d drafts", len(got))
		}
	})

	t.Run("not a resume: no query at all", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{drafts: drafts}
		o := &Orchestrator{runID: "r", analysisDraftRepo: store, explorationSummaryAttempt: 1}
		if got := o.loadAreaDrafts(ctx, nil); got != nil {
			t.Errorf("want nil on a first attempt, got %d", len(got))
		}
		if len(store.loads) != 0 {
			t.Error("queried for drafts on a first attempt — the answer is always empty")
		}
	})

	t.Run("this attempt is re-exploring: no query at all", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{drafts: drafts}
		o := &Orchestrator{
			runID: "r", analysisDraftRepo: store, explorationSummaryAttempt: 1,
			resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{}},
		}
		if got := o.loadAreaDrafts(ctx, nil); got != nil {
			t.Errorf("want nil when the step numbers are about to change meaning, got %d", len(got))
		}
		if len(store.loads) != 0 {
			t.Error("queried for drafts while about to re-explore")
		}
	})

	t.Run("a read failure costs money, not correctness", func(t *testing.T) {
		store := &fakeAnalysisDraftStore{loadErr: errors.New("mongo down")}
		o := resumedOrchestratorWithDrafts(store, 1)
		if got := o.loadAreaDrafts(ctx, nil); got != nil {
			t.Errorf("a failed read must degrade to re-analysing, got %d drafts", len(got))
		}
	})
}

// TestLoadAreaDrafts_ACompletedAreaSupersedesItsDraft is the rule the separate
// collection makes this function responsible for, and the one correctness
// claim of the whole seam.
//
// A completed checkpoint describes the same analysis with its validation done.
// Restoring the draft instead would re-validate work already paid for and —
// far worse — hand Phase 5 an area whose insights carry no verdict at all.
//
// The draft is deleted when its area completes, so this usually finds nothing
// to drop. It is still the guarantee rather than the delete: that delete is
// best-effort, and a draft also outlives its area whenever the process dies
// between the two writes.
func TestLoadAreaDrafts_ACompletedAreaSupersedesItsDraft(t *testing.T) {
	store := &fakeAnalysisDraftStore{drafts: map[string]models.AnalysisAreaDraft{
		"churn":   {Step: draftArea("churn", 2, time.Now())},
		"revenue": {Step: draftArea("revenue", 3, time.Now())},
	}}
	o := resumedOrchestratorWithDrafts(store, 1)

	completed := map[string]models.AnalysisAreaCheckpoint{
		"churn": {Step: doneArea("churn", 2, time.Now())},
	}
	got := o.loadAreaDrafts(context.Background(), completed)

	if _, ok := got["churn"]; ok {
		t.Error("restored a draft for an area that has a COMPLETED checkpoint — its insights would carry no validation verdict")
	}
	if _, ok := got["revenue"]; !ok {
		t.Error("dropped the draft of an area that has NOT completed; that work is lost for no reason")
	}
	if len(got) != 1 {
		t.Errorf("want exactly the unfinished area, got %d", len(got))
	}
}

// TestLoadAreaDrafts_RefreshesTheRetentionClock — a resumed run reads these
// rows without rewriting them, so an operator resuming near the retention
// horizon would otherwise watch them expire mid-run.
func TestLoadAreaDrafts_RefreshesTheRetentionClock(t *testing.T) {
	store := &fakeAnalysisDraftStore{drafts: map[string]models.AnalysisAreaDraft{
		"churn": {Step: draftArea("churn", 2, time.Now())},
	}}
	o := resumedOrchestratorWithDrafts(store, 1)
	if got := o.loadAreaDrafts(context.Background(), nil); len(got) != 1 {
		t.Fatalf("want one draft, got %d", len(got))
	}
	if len(store.touched) != 1 {
		t.Error("did not re-anchor the retention clock on the rows it is about to depend on")
	}
}

// TestLoadAreaDrafts_NothingRestorableDoesNotTouch — no rows to depend on, so
// no write.
func TestLoadAreaDrafts_NothingRestorableDoesNotTouch(t *testing.T) {
	store := &fakeAnalysisDraftStore{drafts: map[string]models.AnalysisAreaDraft{
		"churn": {Step: draftArea("churn", 2, time.Now())},
	}}
	o := resumedOrchestratorWithDrafts(store, 1)
	completed := map[string]models.AnalysisAreaCheckpoint{"churn": {Step: doneArea("churn", 2, time.Now())}}
	if got := o.loadAreaDrafts(context.Background(), completed); got != nil {
		t.Fatalf("want nil, got %d", len(got))
	}
	if len(store.touched) != 0 {
		t.Error("touched rows this attempt is not going to use")
	}
}

// resumedOrchestratorWithDrafts is a resumed run that skipped exploration —
// the only shape in which drafts are loaded at all.
func resumedOrchestratorWithDrafts(store analysisDraftStore, explorationAttempt int) *Orchestrator {
	return &Orchestrator{
		projectID: "proj", runID: "run-1",
		analysisDraftRepo:         store,
		explorationSummaryAttempt: explorationAttempt,
		resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{
			Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1, Attempt: explorationAttempt},
			Steps:   []models.ExplorationCheckpoint{{Step: models.ExplorationStep{Step: 1}}},
		}},
		statusReporter: reporterFor(newFakeRunDoc(), 2),
	}
}

// --- restorableDrafts / restoreAreaDraft -----------------------------------

// TestRestorableDrafts_NarrowsToTheAreasThisRunIsRunning — a checkpoint can
// name an area this run is not running, because the domain pack was edited
// between attempts or the resume used a narrower selection. Such a row is
// never read by the loop, so it must not reach anything else either.
func TestRestorableDrafts_NarrowsToTheAreasThisRunIsRunning(t *testing.T) {
	drafts := map[string]models.AnalysisAreaDraft{
		"churn":   {Step: draftArea("churn", 2, time.Now())},
		"retired": {Step: draftArea("retired", 1, time.Now())},
	}
	got := restorableDrafts([]AnalysisArea{{ID: "churn"}, {ID: "revenue"}}, drafts)
	if len(got) != 1 {
		t.Fatalf("want one draft, got %d", len(got))
	}
	if _, ok := got["churn"]; !ok {
		t.Error("dropped a draft this run is going to use")
	}
	if _, ok := got["retired"]; ok {
		t.Error("kept a draft for an area this run is not running")
	}

	if restorableDrafts(nil, drafts) != nil {
		t.Error("a run with no areas restores nothing")
	}
	if restorableDrafts([]AnalysisArea{{ID: "churn"}}, nil) != nil {
		t.Error("no drafts means nil, not an empty map")
	}
}

// TestRestoreAreaDraft_HandsBackTheStoredIdsUnchanged is the half of the
// design most easily broken by "tidying up".
//
// The draft's insight ids must stay exactly as stored for the whole of the
// area's in-flight life, because this attempt's own verdict markers key on
// them — so a third attempt restoring the same immutable draft finds every
// marker any attempt wrote under one set of keys. Reminting here instead would
// silently orphan every marker on the next resume.
func TestRestoreAreaDraft_HandsBackTheStoredIdsUnchanged(t *testing.T) {
	stored := draftArea("churn", 3, time.Now())
	done := map[string]models.ValidationResult{
		"churn-insight-0": {InsightID: "churn-insight-0", Combined: valmodels.StatusSupported},
	}
	drafts := map[string]models.AnalysisAreaDraft{
		"churn": {Step: stored, InsightVerdicts: done},
	}

	step, insights, verdicts, ok := restoreAreaDraft(drafts, "churn")
	if !ok {
		t.Fatal("restoreAreaDraft reported no draft for an area that has one")
	}
	for i := range insights {
		want := fmt.Sprintf("churn-insight-%d", i)
		if insights[i].ID != want {
			t.Errorf("insight %d id = %q, want the STORED id %q — this attempt's markers key on it", i, insights[i].ID, want)
		}
	}
	if step.AreaID != "churn" || len(step.Insights) != 3 {
		t.Errorf("step came back wrong: %+v", step)
	}
	if len(verdicts) != 1 {
		t.Errorf("want the one marker the draft carries, got %d", len(verdicts))
	}

	// A draft never carries what Phase 4.5 and the quality pass produce; the
	// attempt that finishes the area does.
	if len(step.ValidationResults) != 0 {
		t.Error("a draft must carry no ValidationResults — they are the thing it has not done")
	}
	for _, ins := range step.Insights {
		if len(ins.Quality) != 0 {
			t.Error("a draft must carry no evidence-quality labels — they are attached after validation")
		}
	}
}

// TestRestoreAreaDraft_NoDraftIsNotADraft — the zero case has to be
// distinguishable, because the caller branches on it to decide whether to pay
// for the analysis call.
func TestRestoreAreaDraft_NoDraftIsNotADraft(t *testing.T) {
	step, insights, verdicts, ok := restoreAreaDraft(nil, "churn")
	if ok {
		t.Fatal("reported a draft where there is none")
	}
	if step.AreaID != "" || insights != nil || verdicts != nil {
		t.Error("the miss case must hand back nothing")
	}
	if _, _, _, ok := restoreAreaDraft(map[string]models.AnalysisAreaDraft{"revenue": {}}, "churn"); ok {
		t.Error("matched a different area's draft")
	}
}

// --- discard ---------------------------------------------------------------

// TestDiscardAreaDraft_IsBestEffortAndNeverFailsTheRun — losing this delete
// is safe by construction, because loadAreaDrafts prefers the completed row.
func TestDiscardAreaDraft_IsBestEffortAndNeverFailsTheRun(t *testing.T) {
	store := &fakeAnalysisDraftStore{}
	o := &Orchestrator{runID: "run-1", analysisDraftRepo: store}
	o.discardAreaDraft(context.Background(), "churn")
	if len(store.areaDels) != 1 || store.areaDels[0] != [2]string{"run-1", "churn"} {
		t.Errorf("wrong delete: %v", store.areaDels)
	}

	failing := &fakeAnalysisDraftStore{areaDelErr: errors.New("mongo down")}
	o2 := &Orchestrator{runID: "run-1", analysisDraftRepo: failing}
	o2.discardAreaDraft(context.Background(), "churn")

	// And the no-ops, each of which would otherwise be a delete with a hole
	// in its filter.
	(&Orchestrator{runID: "run-1"}).discardAreaDraft(context.Background(), "churn")
	o3 := &Orchestrator{analysisDraftRepo: &fakeAnalysisDraftStore{}}
	o3.discardAreaDraft(context.Background(), "churn")
	if n := len(o3.analysisDraftRepo.(*fakeAnalysisDraftStore).areaDels); n != 0 {
		t.Error("deleted with no run id")
	}
}

// TestDiscardAreaDrafts_DeletesTheRunAndSwallowsFailure — called when the run
// reaches an outcome it cannot be resumed from.
func TestDiscardAreaDrafts_DeletesTheRunAndSwallowsFailure(t *testing.T) {
	store := &fakeAnalysisDraftStore{}
	o := &Orchestrator{runID: "run-1", analysisDraftRepo: store}
	o.discardAreaDrafts(context.Background(), "completed")
	if len(store.deletes) != 1 || store.deletes[0] != "run-1" {
		t.Errorf("wrong delete: %v", store.deletes)
	}

	failing := &fakeAnalysisDraftStore{deleteErr: errors.New("mongo down")}
	o2 := &Orchestrator{runID: "run-1", analysisDraftRepo: failing}
	o2.discardAreaDrafts(context.Background(), "completed")
}

// --- storedInsightIDs ------------------------------------------------------

// TestStoredInsightIDs_IsTheIdTheCheckpointsActuallyHold pins the invariant
// the whole recommendation restore gate rests on, in all three shapes an area
// can arrive in.
//
// An insight's stored id is the id carried by the checkpoint row that
// currently holds its area:
//
//   - analysed fresh here → the completed row this attempt writes holds this
//     attempt's ids, so stored == live;
//   - restored from a COMPLETED row → that row is never rewritten, so stored
//     is the pre-remint id;
//   - restored from a DRAFT and finished here → the completed row this attempt
//     writes holds the reminted ids, so stored == live again.
//
// Get the third case wrong — by recording the draft's id — and the
// recommendation checkpoint compares its insight set against ids no row
// carries, refusing a perfectly good restore on every later attempt.
func TestStoredInsightIDs_IsTheIdTheCheckpointsActuallyHold(t *testing.T) {
	insights := []models.Insight{
		{ID: "fresh-1"},         // analysed here
		{ID: "reminted-2"},      // restored from a completed checkpoint
		{ID: "draft-finished-3"} /* restored from a draft, finished here */,
	}
	// Only an area restored from a COMPLETED row appears in the map.
	storedByLive := map[string]string{"reminted-2": "attempt1-id-2"}

	ids, liveByStored := storedInsightIDs(insights, storedByLive)

	want := []string{"fresh-1", "attempt1-id-2", "draft-finished-3"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("stored id %d = %q, want %q", i, ids[i], want[i])
		}
	}
	// The gate's mapping admits only the ids a CHECKPOINT vouches for. The
	// other two are this attempt's own work, and a model-supplied id matching
	// a previous attempt's is not proof that the finding behind it is the
	// same — see TestStoredInsightIDs_OnlyACheckpointVouchesForAnIdentity.
	if liveByStored["attempt1-id-2"] != "reminted-2" {
		t.Errorf("liveByStored[attempt1-id-2] = %q, want reminted-2", liveByStored["attempt1-id-2"])
	}
	for _, own := range []string{"fresh-1", "draft-finished-3"} {
		if _, ok := liveByStored[own]; ok {
			t.Errorf("%q is in the gate's mapping, but only this attempt's own analysis vouches for it", own)
		}
	}
}

// TestStoredInsightIDs_AFirstAttemptNamesItsIdsButVouchesForNone — the
// ordinary first attempt. The LIST is its own live ids, because that is what
// its checkpoints will hold and therefore what the next attempt recognises;
// the gate's mapping is empty, because nothing has vouched for any of them
// yet. So a first attempt's recommendation checkpoint becomes restorable only
// once a later attempt restores its areas from those checkpoints — which is
// the case the feature exists for.
func TestStoredInsightIDs_AFirstAttemptNamesItsIdsButVouchesForNone(t *testing.T) {
	insights := []models.Insight{{ID: "a"}, {ID: "b"}}
	ids, liveByStored := storedInsightIDs(insights, nil)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("stored ids = %v, want [a b]", ids)
	}
	if len(liveByStored) != 0 {
		t.Errorf("the gate's mapping should be empty on a first attempt, got %v", liveByStored)
	}
}

// TestStoredInsightIDs_SkipsAnInsightWithNoId — an unidentified insight
// cannot be named in a recommendation's citations, and admitting an empty
// string would let two of them collide in the map.
func TestStoredInsightIDs_SkipsAnInsightWithNoId(t *testing.T) {
	ids, liveByStored := storedInsightIDs([]models.Insight{{ID: ""}, {ID: "a"}}, nil)
	if len(ids) != 1 || ids[0] != "a" {
		t.Errorf("got %v, want [a]", ids)
	}
	if _, ok := liveByStored[""]; ok {
		t.Error("mapped the empty id")
	}
}

// --- regressions ------------------------------------------------------------

// TestRestoredDraftBudget_IsSummedForEveryDraftBeforeTheLoop is a cap
// regression, and it is the same defect per-area resume had one rung up.
//
// The orchestrator builds its area list by ranging over a map of prompts, so
// the order is nondeterministic: an area this attempt has to analyse from
// scratch can run FIRST, against a budget that has not yet absorbed a restored
// draft's verdicts. Charging a draft's markers only when its own area is
// reached let the run validate past the cap and then top the counter up past
// it — with a cap of four and two restored markers, the fresh area got four
// checks and the run finished on six.
func TestRestoredDraftBudget_IsSummedForEveryDraftBeforeTheLoop(t *testing.T) {
	drafts := map[string]models.AnalysisAreaDraft{
		"churn": {Step: draftArea("churn", 3, time.Now()), InsightVerdicts: map[string]models.ValidationResult{
			"churn-insight-0": {InsightID: "churn-insight-0"},
			"churn-insight-1": {InsightID: "churn-insight-1"},
		}},
		"revenue": {Step: draftArea("revenue", 2, time.Now()), InsightVerdicts: map[string]models.ValidationResult{
			"revenue-insight-0": {InsightID: "revenue-insight-0"},
		}},
		// A draft with nothing validated yet contributes nothing.
		"retention": {Step: draftArea("retention", 2, time.Now())},
	}
	if got := restoredDraftBudget(drafts); got != 3 {
		t.Errorf("restored draft budget = %d, want 3 — every marker of every draft, counted once", got)
	}
	if got := restoredDraftBudget(nil); got != 0 {
		t.Errorf("no drafts should spend nothing, got %d", got)
	}
}

// TestCheckpointAnalysisArea_ReportsWhetherTheRowLanded is the signal the
// draft delete depends on.
//
// A checkpoint write failure is swallowed by design, so a caller that read
// only the error would delete the area's draft anyway — throwing away the one
// recoverable copy of an area it had already paid to validate, and forcing a
// later resume to re-analyse and re-validate all of it.
func TestCheckpointAnalysisArea_ReportsWhetherTheRowLanded(t *testing.T) {
	ctx := context.Background()
	step := doneArea("churn", 2, time.Now())

	t.Run("a landed write reports true", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointAnalysisArea(ctx, step, 2)
		if err != nil {
			t.Fatalf("checkpointAnalysisArea: %v", err)
		}
		if !got {
			t.Error("reported no row for a write that succeeded, so the area's draft would be kept forever")
		}
	})

	t.Run("a swallowed failure reports false", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{saveErr: errors.New("mongo down")},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointAnalysisArea(ctx, step, 2)
		if err != nil {
			t.Fatalf("a failed checkpoint must not abort the run, got %v", err)
		}
		if got {
			t.Error("reported a row that is not on disk; the caller would delete the draft and leave nothing recoverable")
		}
	})

	t.Run("every other no-op reports false", func(t *testing.T) {
		failed := doneArea("churn", 2, time.Now())
		failed.Error = "context deadline exceeded"
		for name, o := range map[string]*Orchestrator{
			"nil store": {projectID: "p", runID: "r", explorationSummaryAttempt: 1},
			"no durable exploration summary": {
				projectID: "p", runID: "r",
				analysisCheckpointRepo: &fakeAnalysisCheckpointStore{},
				statusReporter:         reporterFor(newFakeRunDoc(), 1),
			},
		} {
			got, err := o.checkpointAnalysisArea(ctx, step, 2)
			if err != nil || got {
				t.Errorf("%s: got (%v, %v), want (false, nil)", name, got, err)
			}
		}
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		if got, err := o.checkpointAnalysisArea(ctx, failed, 0); err != nil || got {
			t.Errorf("a failed area: got (%v, %v), want (false, nil)", got, err)
		}
	})
}

// TestCheckpointAreaDraft_ReportsWhetherTheDraftLanded — the bool gates the
// per-insight marker writes rather than merely informing them.
//
// A marker write is filtered on `attempt <= this attempt`, so with no draft of
// its own it can land on an EARLIER attempt's row for the same area. An
// insight id can be model-supplied rather than minted, so a reused id would
// then overwrite a marker describing a different finding, and a later resume
// would restore that finding as validated without ever verifying it. No draft,
// no markers.
func TestCheckpointAreaDraft_ReportsWhetherTheDraftLanded(t *testing.T) {
	ctx := context.Background()
	step := draftArea("churn", 2, time.Now())

	t.Run("a landed write reports true", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisDraftRepo:         &fakeAnalysisDraftStore{},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointAreaDraft(ctx, step, true)
		if err != nil {
			t.Fatalf("checkpointAreaDraft: %v", err)
		}
		if !got {
			t.Error("reported no draft for a write that succeeded, so this area's markers would never be written")
		}
	})

	t.Run("a swallowed failure reports false", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisDraftRepo:         &fakeAnalysisDraftStore{saveErr: errors.New("mongo down")},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointAreaDraft(ctx, step, true)
		if err != nil {
			t.Fatalf("a failed draft write must not abort the run, got %v", err)
		}
		if got {
			t.Error("reported a draft that is not on disk; its markers could land on an earlier attempt's row")
		}
	})

	t.Run("every no-op reports false", func(t *testing.T) {
		cases := map[string]struct {
			o    *Orchestrator
			step models.AnalysisStep
			val  bool
		}{
			"nil store":     {&Orchestrator{projectID: "p", runID: "r", explorationSummaryAttempt: 1}, step, true},
			"no summary":    {&Orchestrator{projectID: "p", runID: "r", analysisDraftRepo: &fakeAnalysisDraftStore{}, statusReporter: reporterFor(newFakeRunDoc(), 1)}, step, true},
			"no validation": {&Orchestrator{projectID: "p", runID: "r", analysisDraftRepo: &fakeAnalysisDraftStore{}, explorationSummaryAttempt: 1, statusReporter: reporterFor(newFakeRunDoc(), 1)}, step, false},
			"no insights":   {&Orchestrator{projectID: "p", runID: "r", analysisDraftRepo: &fakeAnalysisDraftStore{}, explorationSummaryAttempt: 1, statusReporter: reporterFor(newFakeRunDoc(), 1)}, draftArea("churn", 0, time.Now()), true},
		}
		for name, tc := range cases {
			got, err := tc.o.checkpointAreaDraft(ctx, tc.step, tc.val)
			if err != nil || got {
				t.Errorf("%s: got (%v, %v), want (false, nil)", name, got, err)
			}
		}
	})
}

// TestStoredInsightIDs_OnlyACheckpointVouchesForAnIdentity is the regression
// for a gate that trusted a model's word.
//
// An insight id is not always server-minted: the analysis parser keeps a
// model-supplied one. So an area this attempt analysed ITSELF can carry an id
// a previous attempt used for a DIFFERENT finding — reachable whenever an area
// is re-analysed because its checkpoint expired or could not be read. Treating
// the matching id as proof of matching content let a stored recommendation set
// be restored over findings it was never reasoned from, carrying validation
// verdicts another finding earned.
//
// The list still names every eligible insight, because that is what the next
// attempt will recognise once this attempt's own checkpoints hold them; the
// gate's mapping admits only the ones a checkpoint already holds.
func TestStoredInsightIDs_OnlyACheckpointVouchesForAnIdentity(t *testing.T) {
	insights := []models.Insight{
		{ID: "restored-live"}, // from a completed checkpoint
		{ID: "i1"},            // analysed here, and the model chose this id
	}
	storedByLive := map[string]string{"restored-live": "attempt1-id"}

	ids, liveByStored := storedInsightIDs(insights, storedByLive)

	if len(ids) != 2 || ids[0] != "attempt1-id" || ids[1] != "i1" {
		t.Fatalf("stored ids = %v, want [attempt1-id i1] — both, so the next attempt recognises them", ids)
	}
	if liveByStored["attempt1-id"] != "restored-live" {
		t.Error("a checkpoint-backed insight must be in the gate's mapping")
	}
	if _, ok := liveByStored["i1"]; ok {
		t.Error("a freshly analysed insight is in the gate's mapping; a model reusing an id would then look like proof of unchanged content")
	}
}

// TestRecommendationGate_RefusesASetCoveringAReAnalysedArea closes the loop:
// an attempt that re-analysed an area cannot restore recommendations reasoned
// over the previous attempt's findings, even when the model handed back the
// same ids.
func TestRecommendationGate_RefusesASetCoveringAReAnalysedArea(t *testing.T) {
	// Attempt 1 generated over two insights with model ids i1 and i2.
	cp := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("r1", "i1")),
		EligibleInsightIDs: []string{"i1", "i2"},
	}
	// Attempt 2 restored one area (i1) and RE-ANALYSED the other, whose model
	// emitted i2 again for a changed finding.
	eligible := []models.Insight{{ID: "live-1"}, {ID: "i2"}}
	_, liveByStored := storedInsightIDs(eligible, map[string]string{"live-1": "i1"})

	if _, ok := restorableRecommendations(cp, eligible, liveByStored, nil); ok {
		t.Error("restored recommendations reasoned over a finding this attempt re-analysed; only a checkpoint can vouch for an id")
	}
}

// TestStoredInsightIDs_VouchesForNeitherOfTwoInsightsSharingAStoredId is a
// mistranslation regression.
//
// Separate areas can return the same model-supplied insight id, and restoring
// them mints two distinct live ids — so one mapping entry under the shared
// stored id would translate a citation, or a figure ref, to whichever of the
// two happened to be written last, while the recommendation kept the
// validation verdict it earned against the other.
func TestStoredInsightIDs_VouchesForNeitherOfTwoInsightsSharingAStoredId(t *testing.T) {
	insights := []models.Insight{
		{ID: "live-a", AnalysisArea: "churn"},
		{ID: "live-b", AnalysisArea: "revenue"},
		{ID: "live-c", AnalysisArea: "retention"},
	}
	// Two areas handed back the same model id, so both restore from it.
	storedByLive := map[string]string{
		"live-a": "shared",
		"live-b": "shared",
		"live-c": "unique",
	}
	ids, liveByStored := storedInsightIDs(insights, storedByLive)

	if len(ids) != 3 {
		t.Fatalf("stored ids = %v, want one per eligible insight", ids)
	}
	if _, ok := liveByStored["shared"]; ok {
		t.Error("vouched for an ambiguous stored id; a citation would be translated to whichever insight was written last")
	}
	if liveByStored["unique"] != "live-c" {
		t.Error("dropped an unambiguous mapping alongside the ambiguous one")
	}
}

// TestAnalysisGenerationFingerprint_ChangesWhenAnAreaIsReAnalysed is the root
// fix for a family of findings that all reduced to one thing: an insight id
// cannot say which analysis it came from, because a model-supplied id survives
// re-analysis.
//
// The fingerprint names each contributing area and when its analysis ran — a
// value carried verbatim through a restore and freshly set by a re-analysis —
// so it distinguishes generations that the ids cannot.
func TestAnalysisGenerationFingerprint_ChangesWhenAnAreaIsReAnalysed(t *testing.T) {
	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	eligible := []models.Insight{
		{ID: "i1", AnalysisArea: "churn"},
		{ID: "i2", AnalysisArea: "churn"}, // the same area counts once
		{ID: "i3", AnalysisArea: "revenue"},
	}
	first := analysisGenerationFingerprint(eligible, []models.AnalysisStep{
		{AreaID: "churn", RunAt: t1},
		{AreaID: "revenue", RunAt: t1},
		// An area that contributed no eligible insight is not part of it:
		// the recommendations never saw it, and a failed area re-stamped on
		// every attempt would otherwise refuse every restore.
		{AreaID: "ignored", RunAt: t2, Error: "llm provider 503"},
	})
	if len(first) != 2 {
		t.Fatalf("fingerprint = %v, want one entry per contributing area", first)
	}

	// Restoring both areas carries their RunAt verbatim, so the generation is
	// the same and the recommendations stay restorable.
	again := analysisGenerationFingerprint(eligible, []models.AnalysisStep{
		{AreaID: "revenue", RunAt: t1},
		{AreaID: "churn", RunAt: t1},
	})
	if !sameAnalysisGeneration(first, again) {
		t.Errorf("a restored analysis changed its fingerprint (%v vs %v); the motivating case would never restore", first, again)
	}

	// Re-analysing one of them does change it, even though every id is the same.
	reanalysed := analysisGenerationFingerprint(eligible, []models.AnalysisStep{
		{AreaID: "churn", RunAt: t1},
		{AreaID: "revenue", RunAt: t2},
	})
	if sameAnalysisGeneration(first, reanalysed) {
		t.Error("a re-analysed area kept its fingerprint; recommendations would be restored over content that replaced what they were reasoned from")
	}
}

// TestAnalysisGenerationFingerprint_SurvivesTheMillisecondBSONKeeps — the
// stored RunAt comes back truncated to milliseconds, so the fingerprint has to
// be computed at that precision on both sides or an attempt's own value would
// never match the one the next attempt reads back.
func TestAnalysisGenerationFingerprint_SurvivesTheMillisecondBSONKeeps(t *testing.T) {
	inMemory := time.Date(2026, 3, 1, 10, 0, 0, 123_456_789, time.UTC)
	readBack := inMemory.Truncate(time.Millisecond)

	eligible := []models.Insight{{ID: "i1", AnalysisArea: "churn"}}
	a := analysisGenerationFingerprint(eligible, []models.AnalysisStep{{AreaID: "churn", RunAt: inMemory}})
	b := analysisGenerationFingerprint(eligible, []models.AnalysisStep{{AreaID: "churn", RunAt: readBack}})
	if !sameAnalysisGeneration(a, b) {
		t.Errorf("nanosecond and millisecond forms disagree (%v vs %v); no checkpoint would ever be restorable", a, b)
	}
}

// TestSameAnalysisGeneration_EdgeCases
func TestSameAnalysisGeneration_EdgeCases(t *testing.T) {
	if !sameAnalysisGeneration(nil, nil) {
		t.Error("two empty fingerprints should match — a run with no eligible insights has no generation")
	}
	if sameAnalysisGeneration([]string{"a@1"}, nil) {
		t.Error("a fingerprint should not match an absent one")
	}
	if sameAnalysisGeneration([]string{"a@1"}, []string{"a@1", "b@1"}) {
		t.Error("a shorter fingerprint matched a longer one")
	}
	if sameAnalysisGeneration([]string{"a@1"}, []string{"a@2"}) {
		t.Error("different generations of one area matched")
	}
}
