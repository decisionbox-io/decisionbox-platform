package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- fakes -----------------------------------------------------------------

// fakeAnalysisCheckpointStore records what the orchestrator wrote and lets a
// test make any call fail.
type fakeAnalysisCheckpointStore struct {
	saved   []database.AnalysisAreaCheckpointInput
	loads   []int
	deletes []string
	touched []string

	areas      map[string]models.AnalysisAreaCheckpoint
	setAttempt int

	saveErr   error
	loadErr   error
	deleteErr error
	touchErr  error
}

func (f *fakeAnalysisCheckpointStore) SaveArea(_ context.Context, in database.AnalysisAreaCheckpointInput) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, in)
	return nil
}

func (f *fakeAnalysisCheckpointStore) LoadAreas(_ context.Context, _ string, explorationAttempt int) (*database.AnalysisCheckpointSet, error) {
	f.loads = append(f.loads, explorationAttempt)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	areas := f.areas
	if areas == nil {
		areas = map[string]models.AnalysisAreaCheckpoint{}
	}
	return &database.AnalysisCheckpointSet{Areas: areas, Attempt: f.setAttempt}, nil
}

func (f *fakeAnalysisCheckpointStore) DeleteByRun(_ context.Context, runID string) (int64, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deletes = append(f.deletes, runID)
	return 2, nil
}

func (f *fakeAnalysisCheckpointStore) TouchByRun(_ context.Context, runID string) (int64, error) {
	if f.touchErr != nil {
		return 0, f.touchErr
	}
	f.touched = append(f.touched, runID)
	return 2, nil
}

// rowRecordingStepWriter keeps the rows themselves, which is what the
// live-feed replay tests are about — the fake in status_attempt_test.go only
// counts them.
type rowRecordingStepWriter struct {
	rows     []models.RunStep
	attempts []int
}

func (f *rowRecordingStepWriter) AddStep(_ context.Context, _, _ string, attempt int, step models.RunStep) error {
	f.rows = append(f.rows, step)
	f.attempts = append(f.attempts, attempt)
	return nil
}

// doneArea is an area as the loop body leaves it when it succeeded.
func doneArea(id string, insights int, at time.Time) models.AnalysisStep {
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
		// A distinct id per insight, as parseInsights produces: it mints a
		// UUID for each. A fixture that reused one id would make the
		// remint's old→new mapping look ambiguous when it is not.
		insightID := fmt.Sprintf("%s-insight-%d", id, i)
		step.Insights = append(step.Insights, models.Insight{
			ID:           insightID,
			AnalysisArea: id,
			Name:         "Finding in " + id,
			Severity:     "high",
			DiscoveredAt: at.Add(time.Second),
			Validation: &valmodels.InsightValidation{
				Status:      string(valmodels.StatusConfirmed),
				Combined:    valmodels.StatusConfirmed,
				ValidatedAt: at.Add(2 * time.Second),
				// The verdicts name the document they judged, which is
				// what the remint has to restamp.
				Verifier: &valmodels.StructuredVerdict{DocID: insightID, Overall: valmodels.StatusConfirmed},
				Refuter:  &valmodels.StructuredVerdict{DocID: insightID, Overall: valmodels.StatusConfirmed},
			},
		})
		step.ValidationResults = append(step.ValidationResults, models.ValidationResult{
			InsightID:     insightID,
			Verifier:      &valmodels.StructuredVerdict{DocID: insightID, Overall: valmodels.StatusConfirmed},
			Refuter:       &valmodels.StructuredVerdict{DocID: insightID, Overall: valmodels.StatusConfirmed},
			AnalysisArea:  id,
			ClaimedMetric: "Finding in " + id,
			ClaimedCount:  10,
			VerifiedCount: 9,
			Status:        string(valmodels.StatusConfirmed),
			Combined:      valmodels.StatusConfirmed,
			ValidatedAt:   at.Add(2 * time.Second),
		})
	}
	return step
}

// --- the write path --------------------------------------------------------

// TestCheckpointAnalysisArea_WritesWhatARestoreNeeds pins the three values a
// restored area cannot be reassembled without: which attempt did the work,
// which exploration it was done over, and the area's slice of the run's
// validation budget.
func TestCheckpointAnalysisArea_WritesWhatARestoreNeeds(t *testing.T) {
	store := &fakeAnalysisCheckpointStore{}
	doc := newFakeRunDoc()
	o := &Orchestrator{
		projectID: "proj", runID: "run-1",
		analysisCheckpointRepo:    store,
		explorationSummaryAttempt: 2,
		statusReporter:            reporterFor(doc, 3),
		resume:                    &ResumeState{Attempt: 3},
	}

	step := doneArea("churn", 2, time.Now())
	if err := o.checkpointAnalysisArea(context.Background(), step, 2); err != nil {
		t.Fatalf("checkpointAnalysisArea: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d rows, want 1", len(store.saved))
	}
	in := store.saved[0]
	if in.RunID != "run-1" || in.ProjectID != "proj" {
		t.Errorf("row identity = %q/%q, want run-1/proj", in.RunID, in.ProjectID)
	}
	if in.Attempt != 3 {
		t.Errorf("attempt = %d, want 3 — the fence against an orphaned earlier attempt", in.Attempt)
	}
	if in.ExplorationAttempt != 2 {
		t.Errorf("exploration_attempt = %d, want 2 — the fence against restoring an area into a different exploration", in.ExplorationAttempt)
	}
	if in.InsightsValidated != 2 {
		t.Errorf("insights_validated = %d, want 2 — a resumed run restores the validation budget from it", in.InsightsValidated)
	}
	if in.Step.AreaID != "churn" || len(in.Step.Insights) != 2 {
		t.Errorf("the area's own output must be stored whole, got %q with %d insights", in.Step.AreaID, len(in.Step.Insights))
	}
	// keepStepIndex is not this function's to set, and the test pins the
	// boundary: an area can only be checkpointed once a durable exploration
	// summary exists, and both paths to one have already set the flag. A
	// setter here could never change the value, so it would read as
	// load-bearing while doing nothing.
	if o.keepStepIndex {
		t.Error("checkpointAnalysisArea must not set keepStepIndex; the exploration paths own it")
	}
}

// TestCheckpointAnalysisArea_DoesNotMarkAFailedArea is the most important
// test in this file.
//
// When DISCOVERY_MAX_DURATION expires mid-analysis the loop does not stop: it
// error-marks every area it has left and the run fails with a thin partial
// result. If those counted as done, the resume would restore all of them,
// re-run nothing and reproduce the same near-empty discovery — so a failed
// area must stay unmarked and be retried.
func TestCheckpointAnalysisArea_DoesNotMarkAFailedArea(t *testing.T) {
	ctx := context.Background()
	for _, errText := range []string{
		"context deadline exceeded",
		"parse error: unexpected end of JSON input",
		"llm provider 503",
	} {
		store := &fakeAnalysisCheckpointStore{}
		o := &Orchestrator{
			projectID: "proj", runID: "run-1",
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		step := doneArea("churn", 0, time.Now())
		step.Error = errText

		if err := o.checkpointAnalysisArea(ctx, step, 0); err != nil {
			t.Fatalf("%q: a failed area must be a quiet no-op, got %v", errText, err)
		}
		if len(store.saved) != 0 {
			t.Errorf("%q: wrote a checkpoint for a FAILED area — a resume would then skip it and never produce its insights", errText)
		}
	}
}

// TestCheckpointAnalysisArea_NoOpsThatProtectTheFence covers the inputs that
// would produce a row no resumed run could legitimately use.
func TestCheckpointAnalysisArea_NoOpsThatProtectTheFence(t *testing.T) {
	ctx := context.Background()
	step := doneArea("churn", 1, time.Now())

	t.Run("nil store disables per-area resume", func(t *testing.T) {
		o := &Orchestrator{projectID: "p", runID: "r", explorationSummaryAttempt: 1}
		if err := o.checkpointAnalysisArea(ctx, step, 1); err != nil {
			t.Fatalf("want a no-op, got %v", err)
		}
	})

	t.Run("no durable exploration summary", func(t *testing.T) {
		// The summary write failed, so this run re-explores on resume and
		// nothing analysed over the current exploration could be restored
		// against the next one. LoadAreas would be obliged to ignore such a
		// row, so it must not be written.
		store := &fakeAnalysisCheckpointStore{}
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo: store,
			statusReporter:         reporterFor(newFakeRunDoc(), 1),
		}
		if err := o.checkpointAnalysisArea(ctx, step, 1); err != nil {
			t.Fatalf("want a no-op, got %v", err)
		}
		if len(store.saved) != 0 {
			t.Error("wrote a row with no exploration summary to stamp it against")
		}
	})

	t.Run("no area id", func(t *testing.T) {
		store := &fakeAnalysisCheckpointStore{}
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		anon := doneArea("", 1, time.Now())
		if err := o.checkpointAnalysisArea(ctx, anon, 1); err != nil {
			t.Fatalf("want a no-op, got %v", err)
		}
		if len(store.saved) != 0 {
			t.Error("wrote a row with no area id — nothing could ever match it")
		}
	})
}

// TestCheckpointAnalysisArea_StopsWhenTheRunHasBeenTakenOver — everything
// after analysis is expensive, so a superseded attempt must stop rather than
// spend it on a result that is then discarded.
func TestCheckpointAnalysisArea_StopsWhenTheRunHasBeenTakenOver(t *testing.T) {
	ctx := context.Background()
	step := doneArea("churn", 1, time.Now())

	t.Run("the ownership probe answers first", func(t *testing.T) {
		store := &fakeAnalysisCheckpointStore{}
		doc := newFakeRunDoc()
		doc.owns = false
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(doc, 2),
		}
		if err := o.checkpointAnalysisArea(ctx, step, 1); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("error = %v, want ai.ErrAttemptSuperseded", err)
		}
		if len(store.saved) != 0 {
			// The probe exists because the row-level fence only refuses a
			// write where a HIGHER-attempt row already exists, so a
			// superseded attempt running ahead of the live one would
			// otherwise plant an area the live attempt has not reached.
			t.Error("a superseded attempt must not write at all, not even a row the fence would later catch")
		}
	})

	t.Run("a cancelled run is not ours either", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.cancelled = true
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(doc, 1),
		}
		if err := o.checkpointAnalysisArea(ctx, step, 1); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("error = %v, want ai.ErrAttemptSuperseded — a cancel leaves the attempt untouched, so only a status-aware probe sees it", err)
		}
	})

	t.Run("the store reports the lost race", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{saveErr: database.ErrSupersededAttempt},
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		if err := o.checkpointAnalysisArea(ctx, step, 1); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("error = %v, want the store's ErrSupersededAttempt translated into the pipeline's stop signal", err)
		}
	})
}

// TestCheckpointAnalysisArea_AWriteFailureDoesNotAbortTheRun — a checkpoint
// is best-effort by design. Losing one costs the area on a later resume; it
// must never cost this run.
func TestCheckpointAnalysisArea_AWriteFailureDoesNotAbortTheRun(t *testing.T) {
	o := &Orchestrator{
		projectID: "p", runID: "r",
		analysisCheckpointRepo:    &fakeAnalysisCheckpointStore{saveErr: errors.New("mongo down")},
		explorationSummaryAttempt: 1,
		statusReporter:            reporterFor(newFakeRunDoc(), 1),
	}
	if err := o.checkpointAnalysisArea(context.Background(), doneArea("churn", 1, time.Now()), 1); err != nil {
		t.Fatalf("a failed checkpoint write must be swallowed, got %v", err)
	}
}

// --- the read path ---------------------------------------------------------

func TestLoadCompletedAreas_RestoresAndReAnchorsTheRetentionClock(t *testing.T) {
	store := &fakeAnalysisCheckpointStore{
		areas: map[string]models.AnalysisAreaCheckpoint{
			"churn": {Step: doneArea("churn", 1, time.Now()), InsightsValidated: 1},
		},
		setAttempt: 1,
	}
	o := &Orchestrator{
		runID:                     "run-1",
		analysisCheckpointRepo:    store,
		explorationSummaryAttempt: 1,
		resume: &ResumeState{
			Attempt: 2,
			Checkpoints: &database.CheckpointSet{
				Steps:   []models.ExplorationCheckpoint{cpStep(1, 1, nil)},
				Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1, Attempt: 1},
			},
		},
	}

	got := o.loadCompletedAreas(context.Background())
	if len(got) != 1 {
		t.Fatalf("restored %d areas, want 1", len(got))
	}
	if len(store.loads) != 1 || store.loads[0] != 1 {
		t.Errorf("loaded with exploration attempt %v, want [1] — the fence value", store.loads)
	}
	if len(store.touched) != 1 {
		t.Error("restoring reads the rows without rewriting them, so their retention clock must be re-anchored or they expire mid-run")
	}
}

// TestLoadCompletedAreas_RefusesToRestoreWhatItCannotTrust covers each gate,
// and the third is the correctness one: a run that is about to RE-explore
// must not restore areas whose insights cite the step numbers it is
// discarding.
func TestLoadCompletedAreas_RefusesToRestoreWhatItCannotTrust(t *testing.T) {
	ctx := context.Background()
	withArea := func() map[string]models.AnalysisAreaCheckpoint {
		return map[string]models.AnalysisAreaCheckpoint{
			"churn": {Step: doneArea("churn", 1, time.Now()), InsightsValidated: 1},
		}
	}

	t.Run("no store", func(t *testing.T) {
		o := &Orchestrator{runID: "r", explorationSummaryAttempt: 1, resume: &ResumeState{Attempt: 2}}
		if got := o.loadCompletedAreas(ctx); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("not a resume", func(t *testing.T) {
		store := &fakeAnalysisCheckpointStore{areas: withArea()}
		o := &Orchestrator{runID: "r", analysisCheckpointRepo: store, explorationSummaryAttempt: 1}
		if got := o.loadCompletedAreas(ctx); got != nil {
			t.Errorf("got %v, want nil — a first attempt has nothing to restore", got)
		}
		if len(store.loads) != 0 {
			t.Error("a first attempt must not even ask")
		}
	})

	t.Run("this attempt has to re-explore", func(t *testing.T) {
		store := &fakeAnalysisCheckpointStore{areas: withArea()}
		o := &Orchestrator{
			runID:                  "r",
			analysisCheckpointRepo: store,
			// No summary, so exploration runs again and the step numbers
			// those areas' insights cite are about to change.
			resume: &ResumeState{
				Attempt:     2,
				Checkpoints: &database.CheckpointSet{Steps: []models.ExplorationCheckpoint{cpStep(1, 1, nil)}},
			},
		}
		if got := o.loadCompletedAreas(ctx); got != nil {
			t.Errorf("got %v, want nil — restoring an area into a different exploration would attribute its insights to the wrong queries", got)
		}
		if len(store.loads) != 0 {
			t.Error("a re-exploring attempt must not even ask")
		}
	})

	t.Run("a read failure costs money, not correctness", func(t *testing.T) {
		store := &fakeAnalysisCheckpointStore{loadErr: errors.New("mongo down")}
		o := &Orchestrator{
			runID:                     "r",
			analysisCheckpointRepo:    store,
			explorationSummaryAttempt: 1,
			resume: &ResumeState{
				Attempt: 2,
				Checkpoints: &database.CheckpointSet{
					Steps:   []models.ExplorationCheckpoint{cpStep(1, 1, nil)},
					Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1, Attempt: 1},
				},
			},
		}
		if got := o.loadCompletedAreas(ctx); got != nil {
			t.Errorf("got %v, want nil — the areas are analysed again, which is the safe direction", got)
		}
	})
}

// --- the live feed ---------------------------------------------------------

// TestReplayAnalysisFeedForResume_ReEmitsEveryRowInTimeOrder. The feed is
// attempt-scoped, so a restored area's rows have to be re-emitted under this
// attempt or the operator sees recommendations drawn from insights that are
// not in the log.
func TestReplayAnalysisFeedForResume_ReEmitsEveryRowInTimeOrder(t *testing.T) {
	early := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	late := early.Add(10 * time.Minute)

	feed := &rowRecordingStepWriter{}
	reporter := newStatusReporter(newFakeRunDoc(), feed, "proj", "run-1", 100)
	reporter.attempt = 2
	o := &Orchestrator{runID: "run-1", statusReporter: reporter, resume: &ResumeState{Attempt: 2}}

	// Deliberately inserted newest-first: the map's iteration order is
	// arbitrary anyway, and the replay has to impose RunAt order itself.
	o.replayAnalysisFeedForResume(context.Background(), map[string]models.AnalysisAreaCheckpoint{
		"later":   {Step: doneArea("later", 1, late)},
		"earlier": {Step: doneArea("earlier", 2, early)},
	})

	// earlier: 1 area + 2 insights + 2 validations; later: 1 + 1 + 1.
	if len(feed.rows) != 8 {
		t.Fatalf("emitted %d rows, want 8 (one per area, insight and validation)", len(feed.rows))
	}
	for i, got := range feed.attempts {
		if got != 2 {
			t.Errorf("row %d carried attempt %d, want 2 — an unfenced row is served to the wrong attempt", i, got)
		}
	}
	// The earlier area's rows must all precede the later area's.
	for i, row := range feed.rows[:5] {
		if row.Timestamp.After(early.Add(time.Minute)) {
			t.Errorf("row %d at %v belongs to the later area; the replay must impose RunAt order", i, row.Timestamp)
		}
	}
	// Timestamps are the original ones, not now.
	if !feed.rows[0].Timestamp.Equal(early) {
		t.Errorf("first row at %v, want the area's own RunAt %v — the feed shows when the work happened", feed.rows[0].Timestamp, early)
	}
	if feed.rows[0].Type != "analysis" {
		t.Errorf("first row type = %q, want analysis", feed.rows[0].Type)
	}
	if feed.rows[1].Type != "insight" || feed.rows[1].InsightName == "" {
		t.Errorf("insight rows must carry the finding: %+v", feed.rows[1])
	}
	if !feed.rows[1].Timestamp.Equal(early.Add(time.Second)) {
		t.Errorf("insight row at %v, want the insight's own DiscoveredAt", feed.rows[1].Timestamp)
	}

	var validations int
	for _, row := range feed.rows {
		if row.Type == "validation" {
			validations++
			if row.Phase != models.PhaseValidation {
				t.Errorf("validation row in phase %q", row.Phase)
			}
		}
	}
	if validations != 3 {
		t.Errorf("emitted %d validation rows, want 3", validations)
	}
}

// TestReplayAnalysisFeedForResume_BumpsNoCounters — the per-action counters
// live on the run document and carry across attempts, so re-emitting a
// restored area must not touch them.
func TestReplayAnalysisFeedForResume_BumpsNoCounters(t *testing.T) {
	doc := newFakeRunDoc()
	feed := &rowRecordingStepWriter{}
	reporter := newStatusReporter(doc, feed, "proj", "run-1", 100)
	reporter.attempt = 2
	o := &Orchestrator{runID: "run-1", statusReporter: reporter, resume: &ResumeState{Attempt: 2}}

	o.replayAnalysisFeedForResume(context.Background(), map[string]models.AnalysisAreaCheckpoint{
		"churn": {Step: doneArea("churn", 2, time.Now())},
	})

	if len(doc.statuses) != 0 {
		t.Errorf("the replay wrote run statuses %v; it must write rows only", doc.statuses)
	}
	if len(doc.attempts) != 0 {
		t.Errorf("the replay made %d run-document writes; every counter it would bump was already banked by the attempt that earned it", len(doc.attempts))
	}
}

// TestReplayAnalysisFeedForResume_OnlyEmitsAreasThisRunRestores — the
// narrowing in restorableAreas is what keeps the feed honest. An area the run
// is not running is never merged into the result, so emitting its rows would
// show the operator analysis of an area the finished discovery does not
// contain.
func TestReplayAnalysisFeedForResume_OnlyEmitsAreasThisRunRestores(t *testing.T) {
	feed := &rowRecordingStepWriter{}
	reporter := newStatusReporter(newFakeRunDoc(), feed, "proj", "run-1", 100)
	reporter.attempt = 2
	o := &Orchestrator{runID: "run-1", statusReporter: reporter, resume: &ResumeState{Attempt: 2}}

	loaded := map[string]models.AnalysisAreaCheckpoint{
		"churn":   {Step: doneArea("churn", 1, time.Now())},
		"retired": {Step: doneArea("retired", 2, time.Now())},
	}
	o.replayAnalysisFeedForResume(context.Background(),
		restorableAreas([]AnalysisArea{{ID: "churn"}}, loaded))

	// churn alone: 1 area row + 1 insight + 1 validation.
	if len(feed.rows) != 3 {
		t.Fatalf("emitted %d rows, want 3 — only churn is restored", len(feed.rows))
	}
	for _, row := range feed.rows {
		if strings.Contains(row.Message, "retired") {
			t.Errorf("emitted a row for an area this run is not running: %q", row.Message)
		}
	}
}

func TestReplayAnalysisFeedForResume_NoOpWithoutRestoredAreas(t *testing.T) {
	feed := &rowRecordingStepWriter{}
	reporter := newStatusReporter(newFakeRunDoc(), feed, "proj", "run-1", 100)
	o := &Orchestrator{runID: "run-1", statusReporter: reporter}

	o.replayAnalysisFeedForResume(context.Background(), nil)
	o.replayAnalysisFeedForResume(context.Background(), map[string]models.AnalysisAreaCheckpoint{})
	if len(feed.rows) != 0 {
		t.Errorf("emitted %d rows for a run with nothing restored", len(feed.rows))
	}

	// And with status reporting disabled entirely (agent run without API).
	bare := &Orchestrator{runID: "run-1"}
	bare.replayAnalysisFeedForResume(context.Background(), map[string]models.AnalysisAreaCheckpoint{
		"churn": {Step: doneArea("churn", 1, time.Now())},
	})
}

// --- the restorable count the live panel shows -----------------------------

// TestRestorableAreas_DropsAreasThisRunIsNotRunning. The checkpoint set can
// legitimately name an area the run is not running — the domain pack was
// edited between attempts, or the operator resumed with a narrower selection.
// Narrowing once keeps the loop, the live panel and the feed replay looking
// at the same set: an un-narrowed row would be counted as progress on work
// the run was never going to do, and re-emitted into the feed as analysis of
// an area the finished discovery does not contain.
func TestRestorableAreas_DropsAreasThisRunIsNotRunning(t *testing.T) {
	runAreas := []AnalysisArea{{ID: "churn"}, {ID: "engagement"}, {ID: "levels"}}
	completed := map[string]models.AnalysisAreaCheckpoint{
		"churn":   {Step: doneArea("churn", 1, time.Now()), InsightsValidated: 1},
		"retired": {Step: doneArea("retired", 1, time.Now()), InsightsValidated: 4},
	}

	got := restorableAreas(runAreas, completed)
	if len(got) != 1 {
		t.Fatalf("narrowed to %d areas, want 1 — only churn is both checkpointed and in this run", len(got))
	}
	if _, ok := got["churn"]; !ok {
		t.Error("churn is in this run and checkpointed; it must survive")
	}
	if _, ok := got["retired"]; ok {
		t.Error("retired is not in this run; it must be dropped before anything reads it")
	}
	// And the budget is charged over the narrowed set, so an area the run is
	// not running cannot shrink the budget nothing is spending.
	if b := restoredValidationBudget(got); b != 1 {
		t.Errorf("restored budget = %d, want 1 — the dropped area's 4 must not be charged", b)
	}

	if got := restorableAreas(nil, completed); got != nil {
		t.Errorf("got %v, want nil when the run has no areas", got)
	}
	if got := restorableAreas(runAreas, nil); got != nil {
		t.Errorf("got %v, want nil when nothing is checkpointed", got)
	}
	if got := restorableAreas([]AnalysisArea{{ID: "levels"}}, completed); got != nil {
		t.Errorf("got %v, want nil when no checkpoint matches this run's areas", got)
	}
}

// TestRestoredValidationBudget_IsSeededBeforeAnyAreaRuns is the regression
// test for the ordering bug.
//
// The area list comes from ranging over a map, so an area this attempt still
// has to run can be processed BEFORE a restored one. If the restored budget
// were added per area instead of seeded up front, that first area would
// validate against a budget of zero and push the run past the project's cap —
// and the restore would then top the counter up to a number already exceeded.
func TestRestoredValidationBudget_IsSeededBeforeAnyAreaRuns(t *testing.T) {
	completed := map[string]models.AnalysisAreaCheckpoint{
		"churn":      {Step: doneArea("churn", 1, time.Now()), InsightsValidated: 2},
		"engagement": {Step: doneArea("engagement", 1, time.Now()), InsightsValidated: 1},
	}
	if got := restoredValidationBudget(completed); got != 3 {
		t.Fatalf("budget = %d, want 3", got)
	}

	// Simulate the hostile order: the unfinished area first, the restored
	// ones after. With the budget seeded, a cap of 3 is already spent before
	// the unfinished area asks for any of it.
	const cap = 3
	spent := restoredValidationBudget(completed)
	runAreas := []AnalysisArea{{ID: "levels"}, {ID: "churn"}, {ID: "engagement"}}
	var log []models.AnalysisStep
	var insights []models.Insight
	validatedHere := 0
	for _, area := range runAreas {
		if cp, ok := completed[area.ID]; ok {
			log, insights = restoreArea(cp, log, insights)
			continue
		}
		if spent < cap {
			spent++
			validatedHere++
		}
	}
	if validatedHere != 0 {
		t.Errorf("validated %d insights in the unfinished area; the restored areas had already spent the whole cap", validatedHere)
	}
	if spent != cap {
		t.Errorf("spent = %d, want %d — the budget must never exceed the cap", spent, cap)
	}
	if len(log) != 2 || len(insights) != 2 {
		t.Errorf("restored %d areas / %d insights, want 2 / 2", len(log), len(insights))
	}

	if got := restoredValidationBudget(nil); got != 0 {
		t.Errorf("budget = %d, want 0 for a run with nothing restored", got)
	}
}

// --- cleanup ---------------------------------------------------------------

// TestDiscardCheckpoints_TakesTheAnalysisRowsWithIt — the two collections are
// one feature, and this is the single place a terminal-and-not-resumable run
// drops what made it resumable. Leaving the area rows behind would leak rows
// for a run that can never read them again.
func TestDiscardCheckpoints_TakesTheAnalysisRowsWithIt(t *testing.T) {
	explore := &fakeCheckpointStore{}
	analysis := &fakeAnalysisCheckpointStore{}
	o := &Orchestrator{
		runID:                  "run-1",
		checkpointRepo:         explore,
		analysisCheckpointRepo: analysis,
		keepStepIndex:          true,
	}

	o.discardCheckpoints(context.Background(), "completed")

	if len(explore.deletes) != 1 {
		t.Errorf("exploration deletes = %v, want [run-1]", explore.deletes)
	}
	if len(analysis.deletes) != 1 {
		t.Errorf("analysis deletes = %v, want [run-1] — half a purge leaks the rows nothing will ever read again", analysis.deletes)
	}
	if o.keepStepIndex {
		t.Error("a run that cannot be resumed must let its per-run vector collection go")
	}
}

// TestDiscardAnalysisCheckpoints_FailureIsSwallowed — the TTL is the backstop,
// and a tidy-up failure must not surface as a run failure.
func TestDiscardAnalysisCheckpoints_FailureIsSwallowed(t *testing.T) {
	o := &Orchestrator{
		runID:                  "run-1",
		analysisCheckpointRepo: &fakeAnalysisCheckpointStore{deleteErr: errors.New("mongo down")},
	}
	o.discardAnalysisCheckpoints(context.Background(), "cancelled")

	// And with no store at all, which is a deployment with per-area resume
	// off rather than an error.
	bare := &Orchestrator{runID: "run-1"}
	bare.discardAnalysisCheckpoints(context.Background(), "cancelled")
}

func TestRefreshAnalysisCheckpointTTL_FailureDegradesALaterResumeOnly(t *testing.T) {
	store := &fakeAnalysisCheckpointStore{}
	o := &Orchestrator{runID: "run-1", analysisCheckpointRepo: store}
	o.refreshAnalysisCheckpointTTL(context.Background())
	if len(store.touched) != 1 {
		t.Errorf("touched %v, want [run-1]", store.touched)
	}

	failing := &Orchestrator{
		runID:                  "run-1",
		analysisCheckpointRepo: &fakeAnalysisCheckpointStore{touchErr: errors.New("mongo down")},
	}
	failing.refreshAnalysisCheckpointTTL(context.Background())

	// No run id is the agent-without-API case; it must not query at all.
	noRun := &Orchestrator{analysisCheckpointRepo: &fakeAnalysisCheckpointStore{}}
	noRun.refreshAnalysisCheckpointTTL(context.Background())
}

// --- wiring ----------------------------------------------------------------

// TestNewOrchestrator_TypedNilAnalysisCheckpointRepoNormalizes — a caller
// passing a typed-nil repository must not leave a non-nil interface on the
// orchestrator, or the `== nil` guards in this file return false and the
// first completed area dereferences it.
func TestNewOrchestrator_TypedNilAnalysisCheckpointRepoNormalizes(t *testing.T) {
	var typedNil *database.DiscoveryAnalysisCheckpointRepository
	o := NewOrchestrator(OrchestratorOptions{
		DiscoveryAnalysisCheckpointRepo: typedNil,
		ProjectID:                       "p",
		RunID:                           "r",
	})
	if o.analysisCheckpointRepo != nil {
		t.Fatalf("typed-nil DiscoveryAnalysisCheckpointRepo must normalize to a nil interface; got %#v", o.analysisCheckpointRepo)
	}
	// Sanity: every guarded path is a clean no-op rather than a panic.
	ctx := context.Background()
	if err := o.checkpointAnalysisArea(ctx, doneArea("churn", 1, time.Now()), 1); err != nil {
		t.Errorf("checkpointAnalysisArea: %v", err)
	}
	if got := o.loadCompletedAreas(ctx); got != nil {
		t.Errorf("loadCompletedAreas = %v, want nil", got)
	}
	o.discardAnalysisCheckpoints(ctx, "test")
	o.refreshAnalysisCheckpointTTL(ctx)
}

// --- the merge -------------------------------------------------------------

// TestRestoreArea_MergesTheUnionThePipelineReadsDownstream. Everything after
// Phase 4 — the validation totals, the failed-area count and so the run's
// classification, the Phase 7 result, the split logs, the recommendation
// eligibility filter, Phase 8's embedding — reads these three accumulators,
// so a restored area is indistinguishable from one this attempt ran only if
// all three land correctly.
func TestRestoreArea_MergesTheUnionThePipelineReadsDownstream(t *testing.T) {
	now := time.Now()
	ranHere := doneArea("engagement", 1, now)
	restored := models.AnalysisAreaCheckpoint{Step: doneArea("churn", 2, now.Add(-time.Hour)), InsightsValidated: 2}

	// The run has already analysed one area itself and validated its one
	// insight; now it restores a checkpointed one.
	log := []models.AnalysisStep{ranHere}
	insights := append([]models.Insight{}, ranHere.Insights...)

	log, insights = restoreArea(restored, log, insights)

	if len(log) != 2 {
		t.Fatalf("analysis log has %d entries, want 2 — the restored area must appear in the split logs and the failed-area denominator", len(log))
	}
	if log[1].AreaID != "churn" || log[1].Prompt == "" || log[1].Response == "" {
		t.Errorf("the restored step must land whole, got %+v", log[1])
	}
	if log[1].Error != "" {
		t.Error("a restored area always carries an empty Error — only completed areas are checkpointed — so it must never be counted as a failed area")
	}
	if len(insights) != 3 {
		t.Fatalf("got %d insights, want 3 (1 run here + 2 restored)", len(insights))
	}
	// The id is REMINTED, not carried over — see reidentifyRestoredArea. What
	// has to hold is that the insight is internally consistent under its new
	// id, which is what Phase 5's related_insight_ids resolve against.
	if insights[1].ID == "" || insights[1].ID == "churn-insight" {
		t.Errorf("restored insight id = %q, want a fresh one", insights[1].ID)
	}
	if log[1].Insights[0].ID != insights[1].ID {
		t.Errorf("the analysis log's insight (%q) and the run's insight list (%q) must agree on the id",
			log[1].Insights[0].ID, insights[1].ID)
	}
	if log[1].ValidationResults[0].InsightID != insights[1].ID {
		t.Errorf("validation result points at %q, want the reminted %q — a dangling InsightID would orphan the evidence",
			log[1].ValidationResults[0].InsightID, insights[1].ID)
	}
	// The budget itself is seeded before the loop rather than accumulated
	// here — see TestRestoredValidationBudget_IsSeededBeforeAnyAreaRuns.
	if got := restoredValidationBudget(map[string]models.AnalysisAreaCheckpoint{"churn": restored}); got != 2 {
		t.Errorf("restored budget = %d, want 2", got)
	}
}

// TestReidentifyRestoredArea_GivesTheInsightsFreshIdentitiesAndKeepsThemConsistent
// is the regression test for three separate failures that all came from one
// cause: an insight id surviving into a second attempt.
//
// The id keys the embedded insight, the standalone row and the Qdrant point,
// all written by the tail as this attempt's own. A carried-over id may
// already be in those collections from the attempt that produced the area,
// which made the standalone insert a duplicate-key error (and that phase is
// non-fatal, so it logged, returned, and skipped the recommendation rows and
// the embedding too), made the pre-upsert duplicate check match the surviving
// point and mark the insight a duplicate of itself, and let a superseded
// attempt still persisting write the shared ids back under its own discovery.
func TestReidentifyRestoredArea_GivesTheInsightsFreshIdentitiesAndKeepsThemConsistent(t *testing.T) {
	stored := doneArea("churn", 2, time.Now())
	for i, id := range []string{"old-a", "old-b"} {
		stored.Insights[i].ID = id
		stored.Insights[i].Validation.Verifier.DocID = id
		stored.Insights[i].Validation.Refuter.DocID = id
		stored.ValidationResults[i].InsightID = id
		stored.ValidationResults[i].Verifier.DocID = id
		stored.ValidationResults[i].Refuter.DocID = id
	}

	got := reidentifyRestoredArea(stored)

	if len(got.Insights) != 2 || len(got.ValidationResults) != 2 {
		t.Fatalf("got %d insights / %d results, want 2 / 2", len(got.Insights), len(got.ValidationResults))
	}
	seen := map[string]bool{}
	for i, ins := range got.Insights {
		if ins.ID == "" {
			t.Fatalf("insight %d has no id", i)
		}
		if ins.ID == "old-a" || ins.ID == "old-b" {
			t.Errorf("insight %d kept its old id %q; the tail writes it as this attempt's own", i, ins.ID)
		}
		if seen[ins.ID] {
			t.Errorf("insight %d reuses id %q — two insights sharing an id would collide in the standalone write", i, ins.ID)
		}
		seen[ins.ID] = true
		// And the remint must not disturb anything else about the finding.
		if ins.Name == "" || ins.Severity == "" || ins.AnalysisArea != "churn" {
			t.Errorf("insight %d lost content in the remint: %+v", i, ins)
		}
	}
	// Each result must follow its own insight, in order.
	for i, vr := range got.ValidationResults {
		if vr.InsightID != got.Insights[i].ID {
			t.Errorf("validation result %d points at %q, want %q", i, vr.InsightID, got.Insights[i].ID)
		}
	}

	// The nested verdicts name the document they judged, separately from the
	// outer id and behind a POINTER the slice copy alone would share. Left
	// alone, this attempt persists evidence identifying an insight that is
	// not in its result — and naming one the retire step deletes.
	for i, ins := range got.Insights {
		if ins.Validation == nil {
			t.Fatalf("insight %d lost its validation in the remint", i)
		}
		if ins.Validation.Verifier == nil || ins.Validation.Refuter == nil {
			t.Fatalf("insight %d lost a verdict in the remint", i)
		}
		if ins.Validation.Verifier.DocID != ins.ID {
			t.Errorf("insight %d verifier judges %q, want the reminted %q", i, ins.Validation.Verifier.DocID, ins.ID)
		}
		if ins.Validation.Refuter.DocID != ins.ID {
			t.Errorf("insight %d refuter judges %q, want the reminted %q", i, ins.Validation.Refuter.DocID, ins.ID)
		}
		if ins.Validation.Combined != valmodels.StatusConfirmed {
			t.Errorf("insight %d lost its verdict status: %q", i, ins.Validation.Combined)
		}
	}
	for i, vr := range got.ValidationResults {
		if vr.Verifier == nil || vr.Refuter == nil {
			t.Fatalf("validation result %d lost a verdict", i)
		}
		if vr.Verifier.DocID != vr.InsightID || vr.Refuter.DocID != vr.InsightID {
			t.Errorf("validation result %d verdicts judge %q/%q, want %q",
				i, vr.Verifier.DocID, vr.Refuter.DocID, vr.InsightID)
		}
	}

	// The CHECKPOINT keeps its own ids, all the way down: a later attempt
	// remints from the same baseline, and the live-feed replay has already
	// read these slices. The Validation pointer is the one that makes this
	// a real risk rather than a formality.
	if stored.Insights[0].ID != "old-a" || stored.ValidationResults[0].InsightID != "old-a" {
		t.Error("the stored step was mutated in place; the checkpoint's own ids must survive the remint")
	}
	if stored.Insights[0].Validation.Verifier.DocID != "old-a" {
		t.Errorf("the stored verdict was mutated through its shared pointer: now judges %q",
			stored.Insights[0].Validation.Verifier.DocID)
	}
	if got.Insights[0].Validation == stored.Insights[0].Validation {
		t.Error("the restored insight shares the checkpoint's Validation pointer; a later write through it would corrupt the stored row")
	}
}

// TestReidentifyRestoredArea_EmptyAndUnmatchedCases — an area that produced no
// insights needs no remint, and a validation result whose insight is gone must
// be left alone rather than pointed at an unrelated finding.
func TestReidentifyRestoredArea_EmptyAndUnmatchedCases(t *testing.T) {
	empty := doneArea("levels", 0, time.Now())
	if got := reidentifyRestoredArea(empty); len(got.Insights) != 0 {
		t.Errorf("got %d insights for an empty area", len(got.Insights))
	}

	noVerdicts := doneArea("churn", 1, time.Now())
	noVerdicts.Insights[0].Validation = nil
	noVerdicts.ValidationResults[0].Verifier = nil
	noVerdicts.ValidationResults[0].Refuter = nil
	got2 := reidentifyRestoredArea(noVerdicts)
	if got2.Insights[0].Validation != nil {
		t.Error("an insight with no validation must come back with none rather than an empty one")
	}
	if got2.ValidationResults[0].Verifier != nil || got2.ValidationResults[0].Refuter != nil {
		t.Error("absent verdicts must stay absent — a refuter is legitimately nil when it is disabled")
	}
	if got2.ValidationResults[0].InsightID != got2.Insights[0].ID {
		t.Error("the outer reference must still be remapped when the verdicts are absent")
	}

	orphaned := doneArea("churn", 1, time.Now())
	orphaned.ValidationResults = append(orphaned.ValidationResults, models.ValidationResult{
		InsightID: "an-insight-this-area-no-longer-has",
	})
	got := reidentifyRestoredArea(orphaned)
	if got.ValidationResults[1].InsightID != "an-insight-this-area-no-longer-has" {
		t.Errorf("an unmatched InsightID was rewritten to %q; it must be left as it was rather than re-pointed at some other finding",
			got.ValidationResults[1].InsightID)
	}
	if got.ValidationResults[0].InsightID != got.Insights[0].ID {
		t.Error("the matched result must still follow its insight")
	}
}

// TestRestoreArea_AnEmptyAreaStillCounts — an area that legitimately produced
// no insights is complete, and is checkpointed. Its step still has to reach
// the analysis log, because the run's classification is computed over the
// number of areas that produced a step.
func TestRestoreArea_AnEmptyAreaStillCounts(t *testing.T) {
	empty := models.AnalysisAreaCheckpoint{Step: doneArea("levels", 0, time.Now())}

	log, insights := restoreArea(empty, nil, nil)
	if len(log) != 1 {
		t.Errorf("analysis log has %d entries, want 1", len(log))
	}
	if len(insights) != 0 {
		t.Errorf("got %d insights, want none", len(insights))
	}
}

// TestRestoreArea_DoesNotAliasTheCaller — the checkpoint's insight slice is
// read-only to the orchestrator, which goes on to append its own areas'
// insights to the same accumulator.
func TestRestoreArea_DoesNotAliasTheCaller(t *testing.T) {
	cp := models.AnalysisAreaCheckpoint{Step: doneArea("churn", 2, time.Now()), InsightsValidated: 2}
	before := len(cp.Step.Insights)

	_, insights := restoreArea(cp, nil, nil)
	insights[0].Name = "mutated by a later phase"

	if cp.Step.Insights[0].Name == "mutated by a later phase" {
		t.Error("the restored insights must not be the checkpoint's own backing array; a later phase writing through them would corrupt the step the split logs persist")
	}
	if len(cp.Step.Insights) != before {
		t.Errorf("the checkpoint's insights were resliced: %d, want %d", len(cp.Step.Insights), before)
	}
}

// --- the live and replayed renderings must not drift -----------------------

// TestReplayAnalysisArea_RendersExactlyWhatTheLivePathRenders is why the row
// builders were extracted rather than duplicated.
//
// A restored area's rows sit in the same feed as the areas this attempt ran,
// so if the two renderings diverge the operator sees one half of their log
// described differently from the other — and the divergence would only show
// up on a resumed run, which is the hardest case to notice it in. The rows
// must match on everything except the timestamp, which is the one field the
// replay deliberately overrides.
func TestReplayAnalysisArea_RendersExactlyWhatTheLivePathRenders(t *testing.T) {
	ctx := context.Background()
	step := doneArea("churn", 2, time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC))

	// The live path, as the Phase 4 loop drives it for this same area.
	liveFeed := &rowRecordingStepWriter{}
	live := newStatusReporter(newFakeRunDoc(), liveFeed, "proj", "run-1", 100)
	live.attempt = 1
	live.AddAnalysisStep(ctx, step.AreaID, step.AreaName, len(step.Insights), step.Error, step.TokensIn, step.TokensOut)
	for _, insight := range step.Insights {
		live.AddInsightStep(ctx, insight.Name, insight.Severity, step.AreaID)
	}
	for _, vr := range step.ValidationResults {
		live.AddValidationStep(ctx, vr.ClaimedMetric, vr.Status, vr.ClaimedCount, vr.VerifiedCount, vr.InputTokens, vr.OutputTokens)
	}

	// The replay path, for the same area.
	replayFeed := &rowRecordingStepWriter{}
	replay := newStatusReporter(newFakeRunDoc(), replayFeed, "proj", "run-1", 100)
	replay.attempt = 2
	replay.ReplayAnalysisArea(ctx, step)

	if len(replayFeed.rows) != len(liveFeed.rows) {
		t.Fatalf("replay emitted %d rows, live emitted %d — the replay must re-emit everything the area originally did",
			len(replayFeed.rows), len(liveFeed.rows))
	}
	for i := range liveFeed.rows {
		want, got := liveFeed.rows[i], replayFeed.rows[i]
		// The timestamp is the deliberate difference: the live row is
		// stamped by the repository at write time, the replayed one carries
		// the moment the work actually happened.
		want.Timestamp, got.Timestamp = time.Time{}, time.Time{}
		if want != got {
			t.Errorf("row %d drifted\n live:   %+v\n replay: %+v", i, want, got)
		}
	}
}

// TestReplayAnalysisArea_IsANoOpWithoutStatusReporting — an agent run without
// the API gets an empty run id, which is how status reporting is switched
// off. (A nil *StatusReporter is not a case: no method on this type accepts
// one, and the orchestrator guards the field instead — see
// replayAnalysisFeedForResume.)
func TestReplayAnalysisArea_IsANoOpWithoutStatusReporting(t *testing.T) {
	feed := &rowRecordingStepWriter{}
	noRun := newStatusReporter(newFakeRunDoc(), feed, "proj", "", 100)
	noRun.ReplayAnalysisArea(context.Background(), doneArea("churn", 1, time.Now()))
	if len(feed.rows) != 0 {
		t.Errorf("emitted %d rows with status reporting disabled", len(feed.rows))
	}
}
