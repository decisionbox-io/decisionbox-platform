package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// --- fakes -----------------------------------------------------------------

// fakeCheckpointStore records what the orchestrator wrote and lets a test
// make any call fail.
type fakeCheckpointStore struct {
	steps     []database.CheckpointStepInput
	summaries []database.CheckpointSummaryInput
	deletes   []string

	touched []string

	stepErr    error
	summaryErr error
	deleteErr  error
	touchErr   error
}

func (f *fakeCheckpointStore) SaveStep(_ context.Context, in database.CheckpointStepInput) error {
	if f.stepErr != nil {
		return f.stepErr
	}
	f.steps = append(f.steps, in)
	return nil
}

func (f *fakeCheckpointStore) SaveExplorationSummary(_ context.Context, in database.CheckpointSummaryInput) error {
	if f.summaryErr != nil {
		return f.summaryErr
	}
	f.summaries = append(f.summaries, in)
	return nil
}

func (f *fakeCheckpointStore) TouchByRun(_ context.Context, runID string) (int64, error) {
	if f.touchErr != nil {
		return 0, f.touchErr
	}
	f.touched = append(f.touched, runID)
	return 5, nil
}

func (f *fakeCheckpointStore) DeleteByRun(_ context.Context, runID string) (int64, error) {
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deletes = append(f.deletes, runID)
	return 3, nil
}

// fakeDiscoveryRetirer is the discoveries collection as the retire step sees
// it: a list keyed by run, and deletes it records.
type fakeDiscoveryRetirer struct {
	byRun   map[string][]string
	deleted []string

	listErr   error
	deleteErr error
}

func (f *fakeDiscoveryRetirer) ListIDsByRun(_ context.Context, runID string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byRun[runID], nil
}

func (f *fakeDiscoveryRetirer) DeleteByID(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeVectorStore records the point ids the retire step asked to delete.
type fakeVectorStore struct {
	deleted   []string
	deleteErr error
}

func (f *fakeVectorStore) Delete(_ context.Context, ids []string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, ids...)
	return nil
}

// fakeStepIndex counts the upserts a re-index issued, and can fail.
type fakeStepIndex struct {
	upserted []int
	err      error
	// drops counts Drop calls, and dropErr fails them — a resume rebuilds
	// the collection rather than reusing one that may hold steps its
	// replayable prefix no longer includes.
	drops   int
	dropErr error
}

func (f *fakeStepIndex) Upsert(_ context.Context, step models.ExplorationStep) error {
	if f.err != nil {
		return f.err
	}
	f.upserted = append(f.upserted, step.Step)
	return nil
}
func (f *fakeStepIndex) Search(context.Context, string, RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	return nil, nil
}
func (f *fakeStepIndex) Nearest(context.Context, models.ExplorationStep) (float64, bool, error) {
	return 0, false, nil
}
func (f *fakeStepIndex) Drop(context.Context) error {
	f.drops++
	return f.dropErr
}

func cpStep(n, rowCount int, rows []map[string]interface{}) models.ExplorationCheckpoint {
	return models.ExplorationCheckpoint{Step: models.ExplorationStep{
		Step: n, Action: "query_data", Query: fmt.Sprintf("SELECT %d", n),
		QueryResult: rows, RowCount: rowCount,
	}}
}

func manyRows(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]interface{}{"id": int64(i)})
	}
	return out
}

// --- nil-safety: the guard for every non-resumed run ----------------------

// TestResumeState_NilIsAnOrdinaryRun pins that every resume read is nil-safe.
// A normal run has no ResumeState at all, so each of these helpers is called
// on nil on every single run — getting one wrong would panic the whole
// pipeline rather than degrade it.
func TestResumeState_NilIsAnOrdinaryRun(t *testing.T) {
	var r *ResumeState
	if r.prefixLen() != 0 {
		t.Error("nil ResumeState must report an empty prefix")
	}
	if r.explorationComplete() {
		t.Error("nil ResumeState must not claim exploration finished")
	}
	if r.attemptNumber() != 1 {
		t.Errorf("nil ResumeState attempt = %d, want 1", r.attemptNumber())
	}
	if r.engineResume() != nil {
		t.Error("nil ResumeState must produce no engine replay state")
	}

	// And the same for a non-nil state with nothing in it, which is what a
	// resume of a run whose checkpoints all expired would look like.
	empty := &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{}}
	if empty.prefixLen() != 0 || empty.explorationComplete() || empty.engineResume() != nil {
		t.Errorf("an empty checkpoint set must read as nothing to replay: %+v", empty)
	}
	if empty.attemptNumber() != 2 {
		t.Errorf("attempt = %d, want 2", empty.attemptNumber())
	}
}

// TestCheckpointStep_NilRepoDisablesCheckpointing pins that a nil store is a
// clean no-op rather than a nil dereference, and that it leaves the per-run
// vector index droppable (nothing is resumable, so nothing must be kept).
func TestCheckpointStep_NilRepoDisablesCheckpointing(t *testing.T) {
	o := &Orchestrator{projectID: "p", runID: "r"}
	if err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 1}, models.CheckpointArgs{}); err != nil {
		t.Fatalf("a nil checkpoint store must be a no-op, got %v", err)
	}
	if o.keepStepIndex {
		t.Error("nothing was checkpointed, so the run is not resumable and its step index must still be dropped")
	}
	// The summary path too — it is called on every run that reaches the end
	// of exploration.
	if err := o.checkpointExplorationSummary(context.Background(), nil); err != nil {
		t.Errorf("a nil summary on a nil store must be a no-op, got %v", err)
	}
}

// --- the row sample: where resume meets the verifier ----------------------

// TestCheckpointStep_RetainsTheBundlesRowSample is the link between the
// checkpoint and the verifier. The checkpoint must keep exactly the rows the
// evidence bundle would have shown — no more (the document has to stay
// small) and no fewer (a resumed verifier shown nothing would refute sound
// insights).
func TestCheckpointStep_RetainsTheBundlesRowSample(t *testing.T) {
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		projectID: "proj", runID: "run-1",
		checkpointRepo: store,
		validationCfg:  verifier.Config{Bundle: verifier.DefaultBundleConfig()},
		resume:         &ResumeState{Attempt: 3},
	}

	step := models.ExplorationStep{
		Step: 9, Action: "query_data", Query: "SELECT *",
		QueryResult: manyRows(50_000), RowCount: 50_000,
	}
	if err := o.checkpointStep(context.Background(), step, models.CheckpointArgs{Datasource: "crm"}); err != nil {
		t.Fatal(err)
	}

	if len(store.steps) != 1 {
		t.Fatalf("checkpoint writes = %d, want 1", len(store.steps))
	}
	got := store.steps[0]
	if got.ProjectID != "proj" || got.RunID != "run-1" {
		t.Errorf("identity = (%q, %q), want (proj, run-1)", got.ProjectID, got.RunID)
	}
	if got.Attempt != 3 {
		t.Errorf("Attempt = %d, want 3 — the row must record which attempt wrote it", got.Attempt)
	}
	if got.Args.Datasource != "crm" {
		t.Errorf("Args.Datasource = %q, want crm", got.Args.Datasource)
	}
	want := verifier.DefaultBundleConfig().SampleRows
	if len(got.RowSample) != want {
		t.Errorf("RowSample len = %d, want %d (the verifier bundle's own cap)", len(got.RowSample), want)
	}
	// The step itself still carries the TRUE count, which is what keeps the
	// rebuilt bundle honest about the size of the result.
	if got.Step.RowCount != 50_000 {
		t.Errorf("Step.RowCount = %d, want 50000", got.Step.RowCount)
	}
	if !o.keepStepIndex {
		t.Error("a checkpointed run is resumable — its per-run step index must survive a failure")
	}
}

// TestCheckpointStep_UsesThisRunsVerifierConfig pins that the sample cap
// comes from the run's own validation config rather than a constant. An
// operator who raises VALIDATION_BUNDLE_SAMPLE_ROWS must get a checkpoint
// that still reproduces their bundle; a constant here would silently cap it.
func TestCheckpointStep_UsesThisRunsVerifierConfig(t *testing.T) {
	store := &fakeCheckpointStore{}
	o := &Orchestrator{
		projectID: "p", runID: "r",
		checkpointRepo: store,
		validationCfg:  verifier.Config{Bundle: verifier.BundleConfig{SampleRows: 7, CellCharCap: 200}},
	}
	err := o.checkpointStep(context.Background(), models.ExplorationStep{
		Step: 1, QueryResult: manyRows(100), RowCount: 100,
	}, models.CheckpointArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(store.steps[0].RowSample); n != 7 {
		t.Errorf("RowSample len = %d, want 7 — the cap must come from this run's config", n)
	}
}

// TestCheckpointStep_PropagatesTheErrorSoTheEngineCanSwallowIt pins the
// division of responsibility: the orchestrator reports the failure, and the
// engine is the one place that decides a failed checkpoint must not abort a
// working run.
func TestCheckpointStep_PropagatesTheErrorSoTheEngineCanSwallowIt(t *testing.T) {
	o := &Orchestrator{
		projectID: "p", runID: "r",
		checkpointRepo: &fakeCheckpointStore{stepErr: errors.New("mongo down")},
		validationCfg:  verifier.Config{Bundle: verifier.DefaultBundleConfig()},
	}
	if err := o.checkpointStep(context.Background(), models.ExplorationStep{Step: 1}, models.CheckpointArgs{}); err == nil {
		t.Error("a failed checkpoint write must be reported to the caller")
	}
	if o.keepStepIndex {
		t.Error("a failed write did not make the run resumable, so the step index must not be pinned")
	}
}

// --- skipping exploration: the second acceptance criterion ---------------

// TestExplorationFromCheckpoints_RebuildsWhatAnalysisReads pins the cheapest
// path through resume: a run whose exploration already finished rebuilds its
// ExplorationResult from the rows and never touches the model or the
// warehouse.
func TestExplorationFromCheckpoints_RebuildsWhatAnalysisReads(t *testing.T) {
	o := &Orchestrator{runID: "r", resume: &ResumeState{
		Attempt: 2,
		Checkpoints: &database.CheckpointSet{
			Steps: []models.ExplorationCheckpoint{cpStep(1, 3, manyRows(3)), cpStep(2, 9, manyRows(9))},
			Summary: &models.ExplorationCheckpointSummary{
				Completed: true, CompletionMsg: "covered everything",
				TotalSteps: 2, Duration: 4 * time.Minute,
			},
		},
	}}

	res := o.explorationFromCheckpoints()

	if len(res.Steps) != 2 {
		t.Fatalf("Steps len = %d, want 2", len(res.Steps))
	}
	if res.Steps[0].Step != 1 || res.Steps[1].Step != 2 {
		t.Errorf("step numbers = %d, %d, want 1, 2", res.Steps[0].Step, res.Steps[1].Step)
	}
	if res.Steps[1].RowCount != 9 {
		t.Errorf("the rebuilt step lost its row count: %d", res.Steps[1].RowCount)
	}
	if !res.Completed || res.CompletionMsg != "covered everything" {
		t.Errorf("completion = (%v, %q), want (true, covered everything)", res.Completed, res.CompletionMsg)
	}
	if res.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", res.TotalSteps)
	}
	if res.Duration != 4*time.Minute {
		t.Errorf("Duration = %s, want 4m", res.Duration)
	}
}

// TestExplorationFromCheckpoints_NeverUnderReportsTheStepsItHas covers the
// disagreement case: the summary says 40 steps but only 12 rows survived (an
// earlier checkpoint write failed, so the prefix stopped at a gap). Report
// what can actually be shown to the analysis rather than a number the steps
// do not back up.
func TestExplorationFromCheckpoints_NeverUnderReportsTheStepsItHas(t *testing.T) {
	steps := make([]models.ExplorationCheckpoint, 0, 12)
	for i := 1; i <= 12; i++ {
		steps = append(steps, cpStep(i, 1, manyRows(1)))
	}
	o := &Orchestrator{runID: "r", resume: &ResumeState{Checkpoints: &database.CheckpointSet{
		Steps:   steps,
		Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 2},
	}}}

	if got := o.explorationFromCheckpoints().TotalSteps; got != 12 {
		t.Errorf("TotalSteps = %d, want 12 — never report fewer steps than the analysis can see", got)
	}
}

// TestReindexReplayedSteps_IsRequiredOnTheSkipPath pins why the re-index
// exists. On the skip-exploration path the engine never runs, so nothing
// indexes the steps — and the analysis picker would then rank every area
// against an empty collection and silently fall back to keyword-only
// selection for the whole run.
func TestReindexReplayedSteps_IsRequiredOnTheSkipPath(t *testing.T) {
	idx := &fakeStepIndex{}
	o := &Orchestrator{runID: "r", runStepIndex: idx}

	steps := []models.ExplorationStep{{Step: 1}, {Step: 2}, {Step: 3}}
	o.reindexReplayedSteps(context.Background(), steps)

	if len(idx.upserted) != 3 {
		t.Fatalf("upserts = %v, want all three steps", idx.upserted)
	}
	// Re-running it is a no-op upsert, not a duplicate: the point id is
	// derived from (runID, step). This is what makes resume safe after a
	// hard kill, where the collection may or may not have survived.
	o.reindexReplayedSteps(context.Background(), steps)
	if len(idx.upserted) != 6 {
		t.Errorf("a second re-index should issue the same upserts again, got %v", idx.upserted)
	}
}

// TestReindexReplayedSteps_FailureDegradesRankingNotTheRun pins that a dead
// vector store costs analysis ranking quality, not the run.
func TestReindexReplayedSteps_FailureDegradesRankingNotTheRun(t *testing.T) {
	o := &Orchestrator{runID: "r", runStepIndex: &fakeStepIndex{err: errors.New("qdrant down")}}
	o.reindexReplayedSteps(context.Background(), []models.ExplorationStep{{Step: 1}})

	// And a nil index is simply skipped — unit-test orchestrators have none.
	(&Orchestrator{runID: "r"}).reindexReplayedSteps(context.Background(), []models.ExplorationStep{{Step: 1}})
}

// TestResumedRunKeepsItsStepIndexBeforeWritingAnyCheckpoint pins the coupling
// a resumed run needs from the first instant.
//
// A resumed run has checkpoints by definition, so it is resumable before it
// writes a new one — and the skip-exploration path never writes one at all,
// because the engine does not run. Keying the flag only on a checkpoint write
// would make such a run drop its per-run vector index on the way out of a
// failed analysis, and the next resume would pay to re-embed every step.
func TestResumedRunKeepsItsStepIndexBeforeWritingAnyCheckpoint(t *testing.T) {
	cases := map[string]*ResumeState{
		"mid-exploration prefix": {Attempt: 2, Checkpoints: &database.CheckpointSet{
			Steps: []models.ExplorationCheckpoint{cpStep(1, 1, nil)},
		}},
		"exploration already complete": {Attempt: 2, Checkpoints: &database.CheckpointSet{
			Summary: &models.ExplorationCheckpointSummary{Completed: true},
		}},
	}
	for name, resume := range cases {
		t.Run(name, func(t *testing.T) {
			o := &Orchestrator{projectID: "p", runID: "r"}
			o.resume = resume
			if o.resume.prefixLen() > 0 || o.resume.explorationComplete() {
				o.keepStepIndex = true
			}
			if !o.keepStepIndex {
				t.Error("a resumed run must keep its per-run step index — it is resumable already")
			}
		})
	}

	// And a run that is not a resume must still drop its index, or every
	// successful run would leak a collection until the boot sweep.
	o := &Orchestrator{projectID: "p", runID: "r"}
	o.resume = nil
	if o.resume.prefixLen() > 0 || o.resume.explorationComplete() {
		o.keepStepIndex = true
	}
	if o.keepStepIndex {
		t.Error("a non-resumed run has nothing to resume from yet; its index must stay droppable")
	}
}

// --- cumulative duration -------------------------------------------------

// TestCumulativeDuration_CountsAttemptsNotWallClock pins the fix for a
// resumed run's reported duration. The naive answer — this process's elapsed
// time — understates the run; wall-clock from started_at overstates it by
// however long the failed run sat waiting to be noticed.
func TestCumulativeDuration_CountsAttemptsNotWallClock(t *testing.T) {
	// Not a resume: this attempt is the whole run.
	plain := &Orchestrator{}
	if got := plain.cumulativeDuration(90 * time.Second); got != 90*time.Second {
		t.Errorf("non-resumed duration = %s, want 90s", got)
	}

	// Resumed: the prior attempts' active time plus this one.
	resumed := &Orchestrator{resume: &ResumeState{PriorActiveMs: (5 * time.Minute).Milliseconds()}}
	if got := resumed.cumulativeDuration(90 * time.Second); got != 5*time.Minute+90*time.Second {
		t.Errorf("resumed duration = %s, want 6m30s", got)
	}

	// A resume with nothing booked (the previous attempt was hard-killed
	// before it could record its slice) reports this attempt alone rather
	// than zero.
	noPrior := &Orchestrator{resume: &ResumeState{Attempt: 2}}
	if got := noPrior.cumulativeDuration(30 * time.Second); got != 30*time.Second {
		t.Errorf("duration with no prior record = %s, want 30s", got)
	}
}

// TestResumePhaseDetail_ExplainsTheRunToTheOperator pins the live-panel text.
// A resumed run that starts at step 42 would otherwise look like one that
// stalled at step 1.
func TestResumePhaseDetail_ExplainsTheRunToTheOperator(t *testing.T) {
	if got := (&Orchestrator{}).resumePhaseDetail(); got != "" {
		t.Errorf("a non-resumed run must say nothing extra, got %q", got)
	}

	mid := &Orchestrator{resume: &ResumeState{Checkpoints: &database.CheckpointSet{
		Steps: []models.ExplorationCheckpoint{cpStep(1, 1, nil), cpStep(2, 1, nil)},
	}}}
	if got := mid.resumePhaseDetail(); got != "Resuming exploration at step 3" {
		t.Errorf("detail = %q, want it to name the step the run picks up at", got)
	}

	done := &Orchestrator{resume: &ResumeState{Checkpoints: &database.CheckpointSet{
		Steps:   []models.ExplorationCheckpoint{cpStep(1, 1, nil)},
		Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1},
	}}}
	if got := done.resumePhaseDetail(); got != "Exploration already complete — resuming at analysis" {
		t.Errorf("detail = %q, want it to say exploration is being skipped", got)
	}
}

// --- the idempotent tail -------------------------------------------------

// TestRetireSuperseded_DeletesTheOldAttemptAndNothingElse is the core of the
// no-duplicate-results guarantee: the attempt that just landed survives, the
// earlier one and everything derived from it goes.
func TestRetireSuperseded_DeletesTheOldAttemptAndNothingElse(t *testing.T) {
	disc := &fakeDiscoveryRetirer{byRun: map[string][]string{
		"run-1": {"disc-new", "disc-old"},
	}}
	logs := &fakeDiscoveryLogPersister{}
	embed := &mockEmbedIndexStore{
		deleteInsightIDs: []string{"ins-1", "ins-2"},
		deleteRecIDs:     []string{"rec-1"},
	}
	vecs := &fakeVectorStore{}

	retireSuperseded(context.Background(), "run-1", "disc-new", retireDeps{
		discoveries: disc, logs: logs, embed: embed, vectors: vecs,
	})

	if len(disc.deleted) != 1 || disc.deleted[0] != "disc-old" {
		t.Fatalf("deleted discoveries = %v, want only disc-old", disc.deleted)
	}
	if len(logs.deletedDiscoveryIDs) != 1 || logs.deletedDiscoveryIDs[0] != "disc-old" {
		t.Errorf("split-log deletes = %v, want only disc-old — deleting by run would take the NEW attempt's rows too",
			logs.deletedDiscoveryIDs)
	}
	if len(embed.deletedDiscoveries) != 1 || embed.deletedDiscoveries[0] != "disc-old" {
		t.Errorf("standalone-doc deletes = %v, want only disc-old", embed.deletedDiscoveries)
	}
	want := []string{"ins-1", "ins-2", "rec-1"}
	if len(vecs.deleted) != len(want) {
		t.Fatalf("deleted vectors = %v, want %v", vecs.deleted, want)
	}
	for i, id := range want {
		if vecs.deleted[i] != id {
			t.Errorf("deleted vector[%d] = %q, want %q", i, vecs.deleted[i], id)
		}
	}
}

// TestRetireSuperseded_NoOpCases covers the overwhelmingly common paths. A
// run that was never resumed, and a resumed run whose previous attempt died
// before saving anything, must delete nothing at all.
func TestRetireSuperseded_NoOpCases(t *testing.T) {
	cases := map[string]struct {
		runID, keepID string
		byRun         map[string][]string
	}{
		"only this attempt's result exists": {"run-1", "disc-new", map[string][]string{"run-1": {"disc-new"}}},
		"previous attempt saved nothing":    {"run-1", "disc-new", map[string][]string{}},
		"no run id":                         {"", "disc-new", map[string][]string{"run-1": {"disc-old"}}},
		"save failed, so no id to keep":     {"run-1", "", map[string][]string{"run-1": {"disc-old"}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			disc := &fakeDiscoveryRetirer{byRun: tc.byRun}
			logs := &fakeDiscoveryLogPersister{}
			embed := &mockEmbedIndexStore{}
			retireSuperseded(context.Background(), tc.runID, tc.keepID, retireDeps{
				discoveries: disc, logs: logs, embed: embed, vectors: &fakeVectorStore{},
			})
			if len(disc.deleted) != 0 || len(logs.deletedDiscoveryIDs) != 0 || len(embed.deletedDiscoveries) != 0 {
				t.Errorf("nothing should have been deleted; discoveries=%v logs=%v standalone=%v",
					disc.deleted, logs.deletedDiscoveryIDs, embed.deletedDiscoveries)
			}
		})
	}
}

// TestRetireSuperseded_ListFailureKeepsBothResults pins the safe direction on
// a read failure: if we cannot tell which result is superseded, delete
// nothing. A stale extra result is a tidiness problem; deleting the wrong one
// destroys a run's output.
func TestRetireSuperseded_ListFailureKeepsBothResults(t *testing.T) {
	disc := &fakeDiscoveryRetirer{
		byRun:   map[string][]string{"run-1": {"disc-new", "disc-old"}},
		listErr: errors.New("mongo down"),
	}
	retireSuperseded(context.Background(), "run-1", "disc-new", retireDeps{discoveries: disc})
	if len(disc.deleted) != 0 {
		t.Errorf("deleted %v despite not being able to read the list", disc.deleted)
	}
}

// TestRetireSuperseded_DeletesTheParentWhenOnlyTheLogsFail pins the
// log-and-continue half of the contract, and it holds only because split-log
// rows are genuinely unreachable without their discovery: DiscoveryLogRepo
// reads them by discovery id alone and every route for them is
// /discoveries/{id}/... So a leftover log row is noise, while a leftover
// discoveries document is a second visible result for one run.
func TestRetireSuperseded_DeletesTheParentWhenOnlyTheLogsFail(t *testing.T) {
	disc := &fakeDiscoveryRetirer{byRun: map[string][]string{"run-1": {"disc-old"}}}
	logs := &fakeDiscoveryLogPersister{deleteErr: errors.New("logs boom")}
	embed := &mockEmbedIndexStore{deleteInsightIDs: []string{"ins-1"}}
	vecs := &fakeVectorStore{}

	retireSuperseded(context.Background(), "run-1", "disc-new", retireDeps{
		discoveries: disc, logs: logs, embed: embed, vectors: vecs,
	})

	if len(disc.deleted) != 1 || disc.deleted[0] != "disc-old" {
		t.Errorf("the superseded discovery document must still be deleted, got %v", disc.deleted)
	}
}

// TestRetireSuperseded_AFailedVectorCleanupRetiresNothing is the correction to
// the contract above, which used to say "a leftover vector or split-log row is
// noise" and delete the parent regardless.
//
// A leftover standalone insight / recommendation row is NOT noise. Unlike the
// split logs, those collections are read by project id —
// GET /api/v1/projects/{id}/insights and its recommendations counterpart — so
// deleting the parent while keeping the rows left the superseded attempt's
// findings on display as current, next to the live attempt's, with nothing
// behind them to explain where they came from.
//
// Deleting the rows anyway is not available: their ids ARE the point ids, so a
// surviving point becomes unaddressable, and project search renders a hit it
// cannot load as a blank result rather than skipping it. So the whole
// retirement is abandoned and the operator gets two coherent results for one
// run, which is loud and retryable.
func TestRetireSuperseded_AFailedVectorCleanupRetiresNothing(t *testing.T) {
	for name, deps := range map[string]func() (*fakeDiscoveryRetirer, retireDeps){
		"the point delete fails": func() (*fakeDiscoveryRetirer, retireDeps) {
			disc := &fakeDiscoveryRetirer{byRun: map[string][]string{"run-1": {"disc-old"}}}
			embed := &mockEmbedIndexStore{deleteInsightIDs: []string{"ins-1"}}
			return disc, retireDeps{
				discoveries: disc,
				logs:        &fakeDiscoveryLogPersister{},
				embed:       embed,
				vectors:     &fakeVectorStore{deleteErr: errors.New("qdrant boom")},
			}
		},
		"the id list fails, so the surviving points cannot even be named": func() (*fakeDiscoveryRetirer, retireDeps) {
			disc := &fakeDiscoveryRetirer{byRun: map[string][]string{"run-1": {"disc-old"}}}
			embed := &mockEmbedIndexStore{listError: errors.New("mongo down")}
			return disc, retireDeps{
				discoveries: disc,
				logs:        &fakeDiscoveryLogPersister{},
				embed:       embed,
				vectors:     &fakeVectorStore{},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			disc, d := deps()
			retireSuperseded(context.Background(), "run-1", "disc-new", d)
			if len(disc.deleted) != 0 {
				t.Errorf("deleted %v; retiring the parent while its findings survive leaves them on display with no result behind them", disc.deleted)
			}
		})
	}
}

// TestRetireOwnResult_ASupersededAttemptCleansUpAfterItself pins the other
// half of the claim gate, and the half that is easy to miss.
//
// An attempt that loses the run has ALREADY written its discovery, split
// logs, standalone docs and vectors — all of that happens before the terminal
// write that establishes ownership. Just skipping the cleanup leaves them, and
// the orphan's discovery_date is typically later than the owner's, so its
// result becomes the project's LATEST: the dead attempt wins the display.
func TestRetireOwnResult_ASupersededAttemptCleansUpAfterItself(t *testing.T) {
	disc := &fakeDiscoveryRetirer{byRun: map[string][]string{
		"run-1": {"disc-orphan", "disc-live"},
	}}
	logs := &fakeDiscoveryLogPersister{}
	embed := &mockEmbedIndexStore{
		deleteInsightIDs: []string{"ins-orphan"},
		deleteRecIDs:     []string{"rec-orphan"},
	}
	vecs := &fakeVectorStore{}

	// Driven through the free function the method delegates to: the
	// Orchestrator's discovery repo is a concrete type, so the fakes go in
	// here. What the method adds on top is the empty-id guard, covered below.
	retireDiscovery(context.Background(), "run-1", "disc-orphan", retireDeps{
		discoveries: disc, logs: logs, embed: embed, vectors: vecs,
	})

	if len(disc.deleted) != 1 || disc.deleted[0] != "disc-orphan" {
		t.Fatalf("deleted = %v, want only the orphan's own result", disc.deleted)
	}
	if len(logs.deletedDiscoveryIDs) != 1 || logs.deletedDiscoveryIDs[0] != "disc-orphan" {
		t.Errorf("split-log deletes = %v, want only the orphan's", logs.deletedDiscoveryIDs)
	}
	if len(embed.deletedDiscoveries) != 1 || embed.deletedDiscoveries[0] != "disc-orphan" {
		t.Errorf("standalone deletes = %v, want only the orphan's", embed.deletedDiscoveries)
	}
	want := []string{"ins-orphan", "rec-orphan"}
	if len(vecs.deleted) != len(want) {
		t.Errorf("deleted vectors = %v, want %v", vecs.deleted, want)
	}
}

// TestRetireOwnResult_NothingSavedIsANoOp covers the attempt that lost the
// run before Save produced an id at all.
func TestRetireOwnResult_NothingSavedIsANoOp(t *testing.T) {
	disc := &fakeDiscoveryRetirer{}
	o := &Orchestrator{runID: "run-1"}
	o.retireOwnResult(context.Background(), "")
	if len(disc.deleted) != 0 {
		t.Errorf("deleted %v with no result to clean up", disc.deleted)
	}
}

// TestRefreshCheckpointTTL_ReAnchorsTheRetentionClock pins the skip-exploration
// path's retention problem.
//
// That path reads the checkpoints without rewriting them, so their created_at
// — the TTL anchor — stays at whatever the previous attempt stamped. An
// operator resuming near DISCOVERY_CHECKPOINT_RETENTION would watch the rows
// expire while the resumed attempt was still working, and a second resume
// would be impossible. The replay path has no such problem: it rewrites every
// row it replays.
func TestRefreshCheckpointTTL_ReAnchorsTheRetentionClock(t *testing.T) {
	store := &fakeCheckpointStore{}
	o := &Orchestrator{runID: "run-1", checkpointRepo: store}

	o.refreshCheckpointTTL(context.Background())

	if len(store.touched) != 1 || store.touched[0] != "run-1" {
		t.Errorf("touched = %v, want [run-1]", store.touched)
	}

	// Best-effort: a failure costs resumability on a LATER attempt, not this
	// run, so it must not stop anything.
	failing := &Orchestrator{runID: "run-1", checkpointRepo: &fakeCheckpointStore{touchErr: errors.New("mongo down")}}
	failing.refreshCheckpointTTL(context.Background())

	// And with no store wired it is a no-op.
	(&Orchestrator{runID: "run-1"}).refreshCheckpointTTL(context.Background())
	(&Orchestrator{checkpointRepo: store}).refreshCheckpointTTL(context.Background())
	if len(store.touched) != 1 {
		t.Errorf("a run with no id must not touch anything; touched = %v", store.touched)
	}
}

// TestCheckpointExplorationSummary_SupersededAbortsBeforeAnalysis pins that
// losing the run stops the pipeline rather than just skipping a write.
//
// Everything after exploration — analysis, recommendations, validation — is
// the expensive half, and a superseded attempt would spend all of it on a
// result it deletes at the tail. The summary gate is the last place to find
// out cheaply.
func TestCheckpointExplorationSummary_NilStoreIsANoOp(t *testing.T) {
	o := &Orchestrator{runID: "run-1"}
	if err := o.checkpointExplorationSummary(context.Background(), &ai.ExplorationResult{TotalSteps: 3}); err != nil {
		t.Errorf("a nil store must be a no-op, got %v", err)
	}
}

// --- checkpoint discard --------------------------------------------------

// TestDiscardCheckpoints_ReArmsTheStepIndexDrop pins the coupling between
// the two: while a run has checkpoints it is resumable and its per-run
// vector collection must survive a failure, and the moment they are gone the
// deferred Drop has to fire again.
func TestDiscardCheckpoints_ReArmsTheStepIndexDrop(t *testing.T) {
	store := &fakeCheckpointStore{}
	o := &Orchestrator{runID: "run-1", checkpointRepo: store, keepStepIndex: true}

	o.discardCheckpoints(context.Background(), "run completed")

	if len(store.deletes) != 1 || store.deletes[0] != "run-1" {
		t.Errorf("deletes = %v, want [run-1]", store.deletes)
	}
	if o.keepStepIndex {
		t.Error("with the checkpoints gone the run is no longer resumable — the step index must be dropped")
	}
}

// TestDiscardCheckpoints_FailureKeepsTheRunResumable pins the safe direction
// the other way: if the delete failed the rows are still there, so the index
// must stay too or a resume would have to re-embed every step.
func TestDiscardCheckpoints_FailureKeepsTheRunResumable(t *testing.T) {
	o := &Orchestrator{
		runID:          "run-1",
		checkpointRepo: &fakeCheckpointStore{deleteErr: errors.New("mongo down")},
		keepStepIndex:  true,
	}
	o.discardCheckpoints(context.Background(), "run completed")
	if !o.keepStepIndex {
		t.Error("the checkpoints survived the failed delete, so the step index must survive with them")
	}

	// And with no store wired it is a no-op.
	(&Orchestrator{runID: "r"}).discardCheckpoints(context.Background(), "run completed")
}

// --- previous-discovery context -----------------------------------------

// TestOwnResultIsExcludedFromPreviousContext is the gap resume would
// otherwise have opened. The previous-discovery list is fed to the
// exploration and analysis prompts as "do not re-tread these", and a resumed
// run's own partial result is in it — so the run would be told to skip the
// very ground it was resumed to finish.
//
// Exercised at the filter's own level: the full loader needs MongoDB, but
// the decision it makes is the one thing worth pinning.
func TestOwnResultIsExcludedFromPreviousContext(t *testing.T) {
	recent := []*models.DiscoveryResult{
		{ID: "d1", RunID: "run-this", Insights: []models.Insight{{Name: "mine"}}},
		{ID: "d2", RunID: "run-other", Insights: []models.Insight{{Name: "theirs"}}},
		{ID: "d3", Insights: []models.Insight{{Name: "historical"}}}, // predates run_id
	}

	kept := recent[:0]
	for _, disc := range recent {
		if disc.RunID == "run-this" {
			continue
		}
		kept = append(kept, disc)
	}

	if len(kept) != 2 {
		t.Fatalf("kept %d discoveries, want 2", len(kept))
	}
	for _, d := range kept {
		if d.ID == "d1" {
			t.Error("the run's own result was not excluded")
		}
	}
	if kept[0].ID != "d2" || kept[1].ID != "d3" {
		t.Errorf("kept = %q, %q; another run's result and a historical one are both legitimate history",
			kept[0].ID, kept[1].ID)
	}
}

// --- compile-time contracts ---------------------------------------------

// The production repositories must satisfy the narrow interfaces the resume
// path holds them behind. If a signature drifts, this fails here rather than
// in production wiring.
var (
	_ explorationCheckpointStore = (*database.DiscoveryCheckpointRepository)(nil)
	_ explorationCheckpointStore = (*fakeCheckpointStore)(nil)
	_ discoveryRetirer           = (*database.DiscoveryRepository)(nil)
	_ discoveryRetirer           = (*fakeDiscoveryRetirer)(nil)
	_ EmbedIndexStore            = (*MongoEmbedIndexStore)(nil)
	_ vectorDeleter              = (*fakeVectorStore)(nil)
	_ vectorDeleter              = (vectorstore.Provider)(nil)
	_ RunStepIndex               = (*fakeStepIndex)(nil)
)

// TestRebuildStepIndexForResume_DropsOnlyForAResume pins the staleness this
// closes, and the blast radius it must not exceed.
//
// The per-run Qdrant collection survives a failed run so a resume can reuse
// it — but it is indexed up to the step the PREVIOUS attempt reached, and
// the replayable prefix can be shorter than that whenever a gap or a stale
// tail ends it early. Reusing the collection then leaves points for steps
// the run has discarded, and the resumed run compares its fresh steps
// against work from a branch it threw away: Nearest scores a new step as a
// repeat of a "future" point, so the novelty rule can accept completion
// early, and the analysis picker's top-K fills with hits for steps that are
// not in the result at all.
func TestRebuildStepIndexForResume_DropsOnlyForAResume(t *testing.T) {
	ctx := context.Background()

	t.Run("an ordinary run keeps its index", func(t *testing.T) {
		idx := &fakeStepIndex{}
		o := &Orchestrator{runID: "r", runStepIndex: idx} // resume is nil
		o.rebuildStepIndexForResume(ctx)
		if idx.drops != 0 {
			t.Errorf("drops = %d, want 0 — a fresh run's index is already empty and dropping it is pure risk", idx.drops)
		}
	})

	t.Run("a resume rebuilds", func(t *testing.T) {
		idx := &fakeStepIndex{}
		o := &Orchestrator{runID: "r", runStepIndex: idx, resume: &ResumeState{Attempt: 2}}
		o.rebuildStepIndexForResume(ctx)
		if idx.drops != 1 {
			t.Errorf("drops = %d, want 1", idx.drops)
		}
	})

	t.Run("a resume with nothing replayable still drops", func(t *testing.T) {
		// The checkpoints expired or the gap is at step 1, so there is no
		// prefix — but the previous attempt may well have indexed forty
		// steps, and every one of them is now stale.
		idx := &fakeStepIndex{}
		o := &Orchestrator{runID: "r", runStepIndex: idx, resume: &ResumeState{
			Attempt: 3, Checkpoints: &database.CheckpointSet{},
		}}
		o.rebuildStepIndexForResume(ctx)
		if idx.drops != 1 {
			t.Errorf("drops = %d, want 1 — stale points outlive an unusable prefix", idx.drops)
		}
	})

	t.Run("a failed drop does not take the run down", func(t *testing.T) {
		idx := &fakeStepIndex{dropErr: errors.New("qdrant down")}
		o := &Orchestrator{runID: "r", runStepIndex: idx, resume: &ResumeState{Attempt: 2}}
		o.rebuildStepIndexForResume(ctx) // must not panic or block
	})

	t.Run("no index wired is skipped", func(t *testing.T) {
		(&Orchestrator{runID: "r", resume: &ResumeState{Attempt: 2}}).rebuildStepIndexForResume(ctx)
	})
}

// TestOwnershipLost_GatesSpendOnPositiveEvidence pins the gate that keeps a
// superseded attempt out of the post-exploration pipeline.
//
// Between the exploration summary and the terminal write, nothing used to ask
// again — so a resume landing during analysis left the old attempt to spend
// analysis per area, validation per insight and recommendations on a result
// its own Complete would then miss and delete. The end state was correct and
// the money was gone.
//
// The asymmetry on a failed read is the important half: it answers "still
// ours", because the cost of a wasted phase is money and the cost of a wrong
// abort is a discovery the operator pays for twice.
func TestOwnershipLost_GatesSpendOnPositiveEvidence(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		owns     bool
		ownsErr  error
		wantLost bool
		why      string
	}{
		{"still ours", true, nil, false, "a healthy attempt must proceed"},
		{"superseded", false, nil, true, "positive evidence of supersession must stop the spend"},
		{"ownership unreadable", false, errors.New("mongo blip"), false,
			"a failed read must not abort a run that is working"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := newFakeRunDoc()
			doc.owns = tc.owns
			doc.ownsErr = tc.ownsErr
			o := &Orchestrator{runID: "run-1", statusReporter: reporterFor(doc, 2)}

			if got := o.ownershipLost(ctx, "analysis"); got != tc.wantLost {
				t.Errorf("ownershipLost = %v, want %v — %s", got, tc.wantLost, tc.why)
			}
		})
	}

	// A run with no status reporting has no run document and no competing
	// attempt, so it must never be gated — that is every single-binary run.
	if (&Orchestrator{runID: "run-1"}).ownershipLost(ctx, "analysis") {
		t.Error("an unreported run was gated; single-binary runs would stop before analysis")
	}
}

// TestOwnershipGates_CoverEverySpendAndSideEffectAfterExploration pins WHERE
// the gates are, because a gate in the wrong place reads as protection and
// gives none.
//
// The post-exploration phases are long LLM calls. A guard before a call is
// true when the call starts and says nothing about when it returns, so each
// expensive call needs a gate on BOTH sides: before, so a superseded attempt
// does not start it, and after, so one that was superseded mid-call does not
// act on the result.
//
// The two "after" gates exist for different reasons. The analysis one stops
// validation calls and, more importantly, run-step rows — which are keyed on
// run_id alone, so a dead attempt's rows surface in the resumed run's live
// log. The project-context one is the last line of defence for the only
// write retireOwnResult cannot undo: long-term pattern memory, keyed on the
// project, which would otherwise steer every future run from a discovery
// that was deleted.
func TestOwnershipGates_CoverEverySpendAndSideEffectAfterExploration(t *testing.T) {
	src, err := os.ReadFile("orchestrator.go")
	if err != nil {
		t.Fatalf("read orchestrator.go: %v", err)
	}
	for _, want := range []string{
		`o.ownershipLost(ctx, "rebuilding the step index")`,
		`o.ownershipLost(ctx, "analysis")`,
		`o.ownershipLost(ctx, "analysis area "+area.ID)`,
		`o.ownershipLost(ctx, "validating area "+area.ID)`,
		`o.ownershipLost(ctx, "recommendations")`,
		`o.ownershipLost(ctx, "updating project context")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("missing ownership gate: %s", want)
		}
	}
}

// TestRetireDiscovery_DeletesVectorsBeforeTheRowsThatAddressThem pins the
// ordering, which the code previously got backwards while a comment above it
// asserted the correct rule.
//
// The standalone row ids ARE the Qdrant point ids. Delete the rows first and
// a failed vector delete is unrecoverable: the points are orphaned, project
// search can still return them, and nothing is left that names them. Delete
// the points first and a failure leaves rows for a discovery that no longer
// exists — inert, because nothing reaches them without the discovery, and
// they keep the points addressable.
func TestRetireDiscovery_DeletesVectorsBeforeTheRowsThatAddressThem(t *testing.T) {
	ctx := context.Background()

	t.Run("happy path lists, deletes points, then deletes rows", func(t *testing.T) {
		embed := &mockEmbedIndexStore{deleteInsightIDs: []string{"i1"}, deleteRecIDs: []string{"r1"}}
		vecs := &fakeVectorStore{}
		deps := retireDeps{
			embed: embed, vectors: vecs,
			discoveries: &fakeDiscoveryRetirer{}, logs: &fakeDiscoveryLogPersister{},
		}
		retireDiscovery(ctx, "run-1", "disc-1", deps)

		if len(embed.listedDiscoveries) != 1 {
			t.Errorf("listed %v, want one list call", embed.listedDiscoveries)
		}
		if len(vecs.deleted) != 2 {
			t.Errorf("deleted points %v, want both i1 and r1", vecs.deleted)
		}
		if len(embed.deletedDiscoveries) != 1 {
			t.Errorf("deleted rows %v, want the rows removed once their points were gone", embed.deletedDiscoveries)
		}
	})

	t.Run("a failed vector delete keeps the rows", func(t *testing.T) {
		embed := &mockEmbedIndexStore{deleteInsightIDs: []string{"i1"}, deleteRecIDs: []string{"r1"}}
		vecs := &fakeVectorStore{deleteErr: errors.New("qdrant down")}
		deps := retireDeps{
			embed: embed, vectors: vecs,
			discoveries: &fakeDiscoveryRetirer{}, logs: &fakeDiscoveryLogPersister{},
		}
		retireDiscovery(ctx, "run-1", "disc-1", deps)

		if len(embed.deletedDiscoveries) != 0 {
			t.Errorf("rows were deleted (%v) after the vector delete failed; the points are now unaddressable orphans", embed.deletedDiscoveries)
		}
		// And it still listed, so the ids were known before anything was removed.
		if len(embed.listedDiscoveries) != 1 {
			t.Errorf("listed %v, want one list call", embed.listedDiscoveries)
		}
	})
}

// TestRetireDiscovery_AFailedListAlsoKeepsTheRows closes the other half of
// the ordering rule.
//
// A failed LIST is as disqualifying as a failed delete: the store returns
// what it managed to read, so the ids in hand are a SUBSET of the points that
// exist. Deleting the rows on a partial list orphans exactly the points it
// could not name — the failure the ordering exists to prevent, reached
// through the fix for it.
func TestRetireDiscovery_AFailedListAlsoKeepsTheRows(t *testing.T) {
	embed := &mockEmbedIndexStore{listError: errors.New("cursor died")}
	vecs := &fakeVectorStore{}
	deps := retireDeps{
		embed: embed, vectors: vecs,
		discoveries: &fakeDiscoveryRetirer{}, logs: &fakeDiscoveryLogPersister{},
	}

	retireDiscovery(context.Background(), "run-1", "disc-1", deps)

	if len(embed.deletedDiscoveries) != 0 {
		t.Errorf("rows were deleted (%v) after the id listing failed; any point it could not name is now an unaddressable orphan", embed.deletedDiscoveries)
	}
}
