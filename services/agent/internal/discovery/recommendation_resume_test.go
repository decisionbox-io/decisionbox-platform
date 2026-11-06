package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeRecommendationCheckpointStore records what the orchestrator wrote. The
// mutex is not decoration: SaveRecVerdict is called from validationPhase's
// bounded workers.
type fakeRecommendationCheckpointStore struct {
	mu sync.Mutex

	saved    []database.RecommendationCheckpointInput
	verdicts []database.RecommendationVerdictInput
	loads    []int
	deletes  []string
	touched  []string

	cp *models.RecommendationCheckpoint

	saveErr    error
	verdictErr error
	verdictNoRow bool
	loadErr    error
	deleteErr  error
	touchErr   error
}

func (f *fakeRecommendationCheckpointStore) SaveDraft(_ context.Context, in database.RecommendationCheckpointInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, in)
	return nil
}

func (f *fakeRecommendationCheckpointStore) SaveRecVerdict(_ context.Context, in database.RecommendationVerdictInput) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verdictErr != nil {
		return false, f.verdictErr
	}
	if f.verdictNoRow {
		return false, nil
	}
	f.verdicts = append(f.verdicts, in)
	return true, nil
}

func (f *fakeRecommendationCheckpointStore) Load(_ context.Context, _ string, explorationAttempt int) (*models.RecommendationCheckpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads = append(f.loads, explorationAttempt)
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.cp, nil
}

func (f *fakeRecommendationCheckpointStore) DeleteByRun(_ context.Context, runID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return 0, f.deleteErr
	}
	f.deletes = append(f.deletes, runID)
	return 1, nil
}

func (f *fakeRecommendationCheckpointStore) TouchByRun(_ context.Context, runID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touchErr != nil {
		return 0, f.touchErr
	}
	f.touched = append(f.touched, runID)
	return 1, nil
}

func recStepWith(recs ...models.Recommendation) models.RecommendationStep {
	return models.RecommendationStep{
		RunAt:           time.Now(),
		Prompt:          "the recommendation prompt",
		Response:        `{"recommendations":[]}`,
		TokensIn:        5000,
		TokensOut:       900,
		InsightCount:    len(recs),
		Recommendations: recs,
	}
}

func rec(id string, cites ...string) models.Recommendation {
	return models.Recommendation{
		ID:                id,
		Title:             "Do the thing: " + id,
		RelatedInsightIDs: cites,
	}
}

// --- checkpointRecommendations ---------------------------------------------

// TestCheckpointRecommendations_DoesNotMarkAPhaseThatBankedNothing is the
// counterpart of the failed-area rule, and it exists for the same reason: a
// restored failure reproduces the failure instead of recovering from it.
func TestCheckpointRecommendations_DoesNotMarkAPhaseThatBankedNothing(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		step *models.RecommendationStep
		why  string
	}{
		{
			name: "the generation call failed",
			step: &models.RecommendationStep{Error: "llm provider 503"},
			why:  "a resume must retry the call, not restore the failure",
		},
		{
			name: "the response could not be parsed",
			step: &models.RecommendationStep{Status: statusRecommendationParseError, Error: "parse error: unexpected end of JSON input"},
			why:  "same — the model produced nothing usable",
		},
		{
			name: "the phase never ran for want of eligible insights",
			step: &models.RecommendationStep{Status: statusRecommendationSkippedNoEligible},
			why:  "no LLM call was made, so there is nothing to protect",
		},
		{
			name: "no step at all",
			step: nil,
			why:  "nothing happened",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeRecommendationCheckpointStore{}
			o := &Orchestrator{
				projectID: "p", runID: "r",
				recommendationCheckpointRepo: store,
				explorationSummaryAttempt:    1,
				statusReporter:               reporterFor(newFakeRunDoc(), 1),
			}
			if _, err := o.checkpointRecommendations(ctx, tc.step, []string{"i1"}, nil); err != nil {
				t.Fatalf("want a quiet no-op, got %v", err)
			}
			if len(store.saved) != 0 {
				t.Errorf("checkpointed a phase that banked nothing: %s", tc.why)
			}
		})
	}
}

// TestCheckpointRecommendations_RecordsACleanEmptyResult — zero
// recommendations is a real answer that cost a real LLM call, and the status
// the dashboard renders is on the step. Re-running it would buy nothing.
func TestCheckpointRecommendations_RecordsACleanEmptyResult(t *testing.T) {
	store := &fakeRecommendationCheckpointStore{}
	o := &Orchestrator{
		projectID: "p", runID: "r",
		recommendationCheckpointRepo: store,
		explorationSummaryAttempt:    1,
		statusReporter:               reporterFor(newFakeRunDoc(), 1),
	}
	step := recStepWith()
	step.Status = statusRecommendationFiguresUnresolved
	step.RecommendationsDroppedUnresolvedFigures = 3

	if _, err := o.checkpointRecommendations(context.Background(), &step, []string{"i1"}, nil); err != nil {
		t.Fatalf("checkpointRecommendations: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("a clean empty result must be checkpointed; got %d rows", len(store.saved))
	}
	if store.saved[0].Step.Status != statusRecommendationFiguresUnresolved {
		t.Error("lost the status the dashboard renders")
	}
}

// TestCheckpointRecommendations_NoOpsAndOwnership covers the fences and both
// ways this attempt learns it has lost the run.
func TestCheckpointRecommendations_NoOpsAndOwnership(t *testing.T) {
	ctx := context.Background()
	step := recStepWith(rec("r1", "i1"))

	t.Run("nil store", func(t *testing.T) {
		o := &Orchestrator{runID: "r", explorationSummaryAttempt: 1}
		if _, err := o.checkpointRecommendations(ctx, &step, nil, nil); err != nil {
			t.Fatalf("want a no-op, got %v", err)
		}
	})

	t.Run("no durable exploration summary", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{}
		o := &Orchestrator{
			runID: "r", recommendationCheckpointRepo: store,
			statusReporter: reporterFor(newFakeRunDoc(), 1),
		}
		if _, err := o.checkpointRecommendations(ctx, &step, nil, nil); err != nil {
			t.Fatalf("want a no-op, got %v", err)
		}
		if len(store.saved) != 0 {
			t.Error("wrote a row Load is obliged to ignore")
		}
	})

	t.Run("ownership lost before the write", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.owns = false
		store := &fakeRecommendationCheckpointStore{}
		o := &Orchestrator{
			runID: "r", recommendationCheckpointRepo: store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(doc, 2),
		}
		if _, err := o.checkpointRecommendations(ctx, &step, nil, nil); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("want ai.ErrAttemptSuperseded, got %v", err)
		}
		if len(store.saved) != 0 {
			t.Error("wrote recommendations the live attempt has not generated")
		}
	})

	t.Run("the row fence refuses the write", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{saveErr: database.ErrSupersededAttempt}
		o := &Orchestrator{
			runID: "r", recommendationCheckpointRepo: store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		if _, err := o.checkpointRecommendations(ctx, &step, nil, nil); !errors.Is(err, ai.ErrAttemptSuperseded) {
			t.Fatalf("want ai.ErrAttemptSuperseded, got %v", err)
		}
	})

	t.Run("an ordinary write failure is swallowed", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{saveErr: errors.New("mongo down")}
		o := &Orchestrator{
			runID: "r", recommendationCheckpointRepo: store,
			explorationSummaryAttempt: 1,
			statusReporter:            reporterFor(newFakeRunDoc(), 1),
		}
		if _, err := o.checkpointRecommendations(ctx, &step, nil, nil); err != nil {
			t.Fatalf("a failed checkpoint must not abort the run, got %v", err)
		}
	})
}

// TestCheckpointRecommendations_StoresTheEligibleSetAndBothFences — the
// eligible set IS the restore gate, so losing it would make every later
// attempt refuse the row.
func TestCheckpointRecommendations_StoresTheEligibleSetAndBothFences(t *testing.T) {
	store := &fakeRecommendationCheckpointStore{}
	o := &Orchestrator{
		projectID: "proj", runID: "run-1",
		recommendationCheckpointRepo: store,
		explorationSummaryAttempt:    2,
		resume:                       &ResumeState{Attempt: 3},
		statusReporter:               reporterFor(newFakeRunDoc(), 3),
	}
	step := recStepWith(rec("r1", "i1"), rec("r2", "i2"))
	if _, err := o.checkpointRecommendations(context.Background(), &step, []string{"i1", "i2"}, nil); err != nil {
		t.Fatalf("checkpointRecommendations: %v", err)
	}
	got := store.saved[0]
	if got.Attempt != 3 || got.ExplorationAttempt != 2 {
		t.Errorf("fences wrong: attempt=%d exploration=%d", got.Attempt, got.ExplorationAttempt)
	}
	if len(got.EligibleInsightIDs) != 2 {
		t.Errorf("stored %d eligible ids, want 2 — they are the restore gate", len(got.EligibleInsightIDs))
	}
	if len(got.Step.Recommendations) != 2 {
		t.Errorf("stored %d recommendations, want 2", len(got.Step.Recommendations))
	}
}

// --- checkpointRecommendationVerdict ---------------------------------------

// TestCheckpointRecommendationVerdict_NoOpsAndFailureIsSwallowed — a lost
// marker costs one re-validated recommendation.
func TestCheckpointRecommendationVerdict_NoOpsAndFailureIsSwallowed(t *testing.T) {
	ctx := context.Background()
	vr := models.ValidationResult{InsightID: "r1", Combined: valmodels.StatusSupported}

	t.Run("nil store", func(t *testing.T) {
		(&Orchestrator{runID: "r", explorationSummaryAttempt: 1}).checkpointRecommendationVerdict(ctx, vr)
	})
	t.Run("no recommendation id", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{}
		o := &Orchestrator{runID: "r", recommendationCheckpointRepo: store, explorationSummaryAttempt: 1}
		o.checkpointRecommendationVerdict(ctx, models.ValidationResult{})
		if len(store.verdicts) != 0 {
			t.Error("wrote a marker keyed on an empty id")
		}
	})
	t.Run("no durable exploration summary", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{}
		o := &Orchestrator{runID: "r", recommendationCheckpointRepo: store}
		o.checkpointRecommendationVerdict(ctx, vr)
		if len(store.verdicts) != 0 {
			t.Error("wrote a marker against a row that could never be restored")
		}
	})
	t.Run("write failure is swallowed", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{verdictErr: errors.New("mongo down")}
		o := &Orchestrator{runID: "r", recommendationCheckpointRepo: store, explorationSummaryAttempt: 1}
		o.checkpointRecommendationVerdict(ctx, vr)
	})
}

// --- the restore gate ------------------------------------------------------

// TestRestorableRecommendations_AcceptsAndTranslates is the happy path, and
// the translation is the point: the stored citations name the ids the
// checkpoints hold, and this attempt's insights carry reminted ones.
func TestRestorableRecommendations_AcceptsAndTranslates(t *testing.T) {
	cp := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("r1", "stored-1"), rec("r2", "stored-1", "stored-2")),
		EligibleInsightIDs: []string{"stored-1", "stored-2"},
		RecVerdicts: map[string]models.ValidationResult{
			"r1": {InsightID: "r1", Combined: valmodels.StatusSupported},
		},
	}
	eligible := []models.Insight{{ID: "live-1"}, {ID: "live-2"}}
	liveByStored := map[string]string{"stored-1": "live-1", "stored-2": "live-2"}

	got, ok := restorableRecommendations(cp, eligible, liveByStored, nil)
	if !ok {
		t.Fatal("refused a checkpoint whose every insight is still present")
	}
	if len(got.Step.Recommendations) != 2 {
		t.Fatalf("want 2 recommendations, got %d", len(got.Step.Recommendations))
	}
	want := [][]string{{"live-1"}, {"live-1", "live-2"}}
	for i, w := range want {
		gotIDs := got.Step.Recommendations[i].RelatedInsightIDs
		if len(gotIDs) != len(w) {
			t.Fatalf("rec %d cites %v, want %v", i, gotIDs, w)
		}
		for j := range w {
			if gotIDs[j] != w[j] {
				t.Errorf("rec %d citation %d = %q, want %q", i, j, gotIDs[j], w[j])
			}
		}
	}
	// The loaded document must keep its own ids: a later attempt translates
	// from the same baseline.
	if cp.Step.Recommendations[0].RelatedInsightIDs[0] != "stored-1" {
		t.Error("mutated the loaded checkpoint's citations — a later attempt would translate from a moved baseline")
	}
}

// TestRestorableRecommendations_RefusesWhenTheInsightSetChanged covers the
// three clauses of the gate. Each is a run whose recommendations would
// describe findings it no longer holds, so each must regenerate.
func TestRestorableRecommendations_RefusesWhenTheInsightSetChanged(t *testing.T) {
	base := func() *models.RecommendationCheckpoint {
		return &models.RecommendationCheckpoint{
			Step:               recStepWith(rec("r1", "stored-1")),
			EligibleInsightIDs: []string{"stored-1", "stored-2"},
		}
	}

	t.Run("a different number of eligible insights", func(t *testing.T) {
		// One area was re-analysed, so it contributed different findings.
		eligible := []models.Insight{{ID: "live-1"}}
		if _, ok := restorableRecommendations(base(), eligible, map[string]string{"stored-1": "live-1"}, nil); ok {
			t.Error("restored recommendations reasoned over a different number of insights")
		}
	})

	t.Run("an eligible insight no row accounts for", func(t *testing.T) {
		// Same size, but one of this attempt's insights was analysed fresh —
		// so no stored id maps onto it.
		eligible := []models.Insight{{ID: "live-1"}, {ID: "fresh-2"}}
		if _, ok := restorableRecommendations(base(), eligible, map[string]string{"stored-1": "live-1"}, nil); ok {
			t.Error("restored recommendations whose insight set is not the one this attempt has")
		}
	})

	t.Run("a mapped insight that is no longer eligible", func(t *testing.T) {
		// The project's recommendation_verdicts changed between attempts, so
		// an insight the recommendations were reasoned from is now filtered
		// out of the recommender's input.
		eligible := []models.Insight{{ID: "live-1"}, {ID: "live-3"}}
		liveByStored := map[string]string{"stored-1": "live-1", "stored-2": "live-2"}
		if _, ok := restorableRecommendations(base(), eligible, liveByStored, nil); ok {
			t.Error("restored recommendations citing an insight the operator has excluded")
		}
	})

	t.Run("a citation that does not translate", func(t *testing.T) {
		cp := &models.RecommendationCheckpoint{
			// The eligible set lines up, but one recommendation cites
			// something outside it — which is what a broken remap looks like.
			Step:               recStepWith(rec("r1", "stored-1"), rec("r2", "ghost")),
			EligibleInsightIDs: []string{"stored-1"},
		}
		eligible := []models.Insight{{ID: "live-1"}}
		if _, ok := restorableRecommendations(cp, eligible, map[string]string{"stored-1": "live-1"}, nil); ok {
			t.Error("restored a recommendation citing an insight this attempt does not have — it would be fail-open backfilled or dropped downstream")
		}
	})

	t.Run("nil checkpoint", func(t *testing.T) {
		if _, ok := restorableRecommendations(nil, nil, nil, nil); ok {
			t.Error("restored something from nothing")
		}
	})
}

// TestRestorableRecommendations_RefusesTheWholeSetOrNone — partial acceptance
// would ship recommendations grounded in a translation that failed, which is
// exactly what recoverRelatedInsightIDs' fail-open backfill would then hide.
func TestRestorableRecommendations_RefusesTheWholeSetOrNone(t *testing.T) {
	cp := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("good", "stored-1"), rec("bad", "ghost")),
		EligibleInsightIDs: []string{"stored-1"},
	}
	got, ok := restorableRecommendations(cp, []models.Insight{{ID: "live-1"}}, map[string]string{"stored-1": "live-1"}, nil)
	if ok || got != nil {
		t.Fatal("accepted a set containing one untranslatable recommendation; it must be all or nothing")
	}
}

// TestRestorableRecommendations_AcceptsARecommendationThatCitesNothing — an
// uncited recommendation is not the gate's business: Phase 5's own
// validateRelatedInsightIDs is what drops it, and it already ran on the
// attempt that stored this row.
func TestRestorableRecommendations_AcceptsARecommendationThatCitesNothing(t *testing.T) {
	cp := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("r1")),
		EligibleInsightIDs: []string{"stored-1"},
	}
	if _, ok := restorableRecommendations(cp, []models.Insight{{ID: "live-1"}}, map[string]string{"stored-1": "live-1"}, nil); !ok {
		t.Error("refused a checkpoint over a recommendation with no citations, which the generating attempt already adjudicated")
	}
}

// --- the rec remint --------------------------------------------------------

// TestReidentifyRestoredRecommendations_RepointsEverythingThatNamesThem — a
// recommendation id keys the embedded recommendation, the standalone row and
// the Qdrant point, all inserted by the tail. An id carried over from the
// attempt that generated it may already be in those collections.
func TestReidentifyRestoredRecommendations_RepointsEverythingThatNamesThem(t *testing.T) {
	recs := []models.Recommendation{
		{
			ID: "stored-r1",
			Validation: &valmodels.InsightValidation{
				Combined: valmodels.StatusSupported,
				Verifier: &valmodels.StructuredVerdict{DocID: "stored-r1", Mode: valmodels.ModeVerifier},
				Refuter:  &valmodels.StructuredVerdict{DocID: "stored-r1", Mode: valmodels.ModeRefuter},
			},
		},
		{ID: "stored-r2"},
	}
	results := []models.ValidationResult{
		{
			InsightID: "stored-r1",
			DocKind:   valmodels.DocRecommendation,
			Verifier:  &valmodels.StructuredVerdict{DocID: "stored-r1"},
			Refuter:   &valmodels.StructuredVerdict{DocID: "stored-r1"},
		},
		{InsightID: "unrelated", DocKind: valmodels.DocRecommendation},
	}

	reidentifyRestoredRecommendations(recs, results)

	for i, r := range recs {
		if r.ID == "" || r.ID == "stored-r1" || r.ID == "stored-r2" {
			t.Fatalf("recommendation %d kept a stored id (%q) — the tail would insert a duplicate _id", i, r.ID)
		}
	}
	if recs[0].Validation.Verifier.DocID != recs[0].ID {
		t.Error("the verifier verdict still names the stored id")
	}
	if recs[0].Validation.Refuter.DocID != recs[0].ID {
		t.Error("the refuter verdict still names the stored id")
	}
	if results[0].InsightID != recs[0].ID {
		t.Error("the validation result still names the stored id")
	}
	if results[0].Verifier.DocID != recs[0].ID || results[0].Refuter.DocID != recs[0].ID {
		t.Error("the result's nested verdicts still name the stored id")
	}
	// A result for a document this set no longer carries is left exactly as it
	// was rather than re-pointed at some other recommendation.
	if results[1].InsightID != "unrelated" {
		t.Error("re-pointed a result that belongs to nothing here")
	}
}

// TestReidentifyRestoredRecommendations_DoesNotShareValidationWithTheStored
// struct — Validation is a POINTER, so a shallow copy shares it, and
// restamping in place would rewrite the loaded checkpoint's own evidence.
func TestReidentifyRestoredRecommendations_DoesNotShareValidationWithTheStored(t *testing.T) {
	shared := &valmodels.InsightValidation{
		Verifier: &valmodels.StructuredVerdict{DocID: "stored-r1"},
	}
	recs := []models.Recommendation{{ID: "stored-r1", Validation: shared}}
	reidentifyRestoredRecommendations(recs, nil)

	if shared.Verifier.DocID != "stored-r1" {
		t.Error("rewrote the stored verdict in place; a later attempt would remint from a moved baseline")
	}
	if recs[0].Validation == shared {
		t.Error("the restored recommendation still shares its Validation with the stored one")
	}
}

// --- budget ----------------------------------------------------------------

// TestRestoredRecommendationBudget_CountsOnlyMarkersThisAttemptStillCarries —
// a marker for a recommendation the translation dropped would shrink a budget
// nothing is spending.
func TestRestoredRecommendationBudget_CountsOnlyMarkersThisAttemptStillCarries(t *testing.T) {
	cp := &models.RecommendationCheckpoint{
		Step: recStepWith(rec("r1"), rec("r2")),
		RecVerdicts: map[string]models.ValidationResult{
			"r1":    {InsightID: "r1"},
			"ghost": {InsightID: "ghost"},
		},
	}
	got := restoredRecommendationBudget(cp)
	if len(got) != 1 {
		t.Fatalf("want one marker, got %d", len(got))
	}
	if _, ok := got["r1"]; !ok {
		t.Error("dropped a marker for a recommendation this attempt carries")
	}
	if _, ok := got["ghost"]; ok {
		t.Error("kept a marker for a recommendation this attempt does not carry")
	}

	if restoredRecommendationBudget(nil) != nil {
		t.Error("nil checkpoint should yield nil")
	}
	if restoredRecommendationBudget(&models.RecommendationCheckpoint{Step: recStepWith(rec("r1"))}) != nil {
		t.Error("no markers should yield nil, not an empty map")
	}
}

// TestRecommendationsPendingValidation — what the live panel reports and the
// only thing Phase 5.5 will pay for.
func TestRecommendationsPendingValidation(t *testing.T) {
	recs := []models.Recommendation{rec("r1"), rec("r2"), rec("r3")}
	done := map[string]models.ValidationResult{"r1": {}, "r3": {}}
	if got := recommendationsPendingValidation(recs, done); got != 1 {
		t.Errorf("pending = %d, want 1", got)
	}
	if got := recommendationsPendingValidation(recs, nil); got != 3 {
		t.Errorf("pending with no markers = %d, want 3", got)
	}
}

// --- load / discard --------------------------------------------------------

// TestLoadRecommendationCheckpoint_GatesAndTTLRefresh — two gates must not
// query at all, a read failure degrades to regenerating, and a hit
// re-anchors the retention clock on the row it is about to depend on.
func TestLoadRecommendationCheckpoint_GatesAndTTLRefresh(t *testing.T) {
	ctx := context.Background()
	cp := &models.RecommendationCheckpoint{Step: recStepWith(rec("r1"))}

	t.Run("not a resume: no query", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{cp: cp}
		o := &Orchestrator{runID: "r", recommendationCheckpointRepo: store, explorationSummaryAttempt: 1}
		if got := o.loadRecommendationCheckpoint(ctx); got != nil {
			t.Error("loaded a checkpoint on a first attempt")
		}
		if len(store.loads) != 0 {
			t.Error("queried on a first attempt — the answer is always nil")
		}
	})

	t.Run("re-exploring: no query", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{cp: cp}
		o := &Orchestrator{
			runID: "r", recommendationCheckpointRepo: store, explorationSummaryAttempt: 1,
			resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{}},
		}
		if got := o.loadRecommendationCheckpoint(ctx); got != nil {
			t.Error("loaded a checkpoint while about to re-explore")
		}
		if len(store.loads) != 0 {
			t.Error("queried while about to re-explore")
		}
	})

	t.Run("read failure degrades to regenerating", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{loadErr: errors.New("mongo down")}
		o := resumedOrchestratorWithRecCheckpoint(store, 1)
		if got := o.loadRecommendationCheckpoint(ctx); got != nil {
			t.Error("a failed read must degrade to regenerating")
		}
	})

	t.Run("a hit re-anchors the retention clock", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{cp: cp}
		o := resumedOrchestratorWithRecCheckpoint(store, 1)
		if got := o.loadRecommendationCheckpoint(ctx); got == nil {
			t.Fatal("want the checkpoint")
		}
		if len(store.touched) != 1 {
			t.Error("did not re-anchor the retention clock on the row it is about to depend on")
		}
	})

	t.Run("a miss touches nothing", func(t *testing.T) {
		store := &fakeRecommendationCheckpointStore{}
		o := resumedOrchestratorWithRecCheckpoint(store, 1)
		if got := o.loadRecommendationCheckpoint(ctx); got != nil {
			t.Error("want nil")
		}
		if len(store.touched) != 0 {
			t.Error("touched a row that does not exist")
		}
	})
}

// TestDiscardRecommendationCheckpoint_DeletesAndSwallowsFailure
func TestDiscardRecommendationCheckpoint_DeletesAndSwallowsFailure(t *testing.T) {
	store := &fakeRecommendationCheckpointStore{}
	o := &Orchestrator{runID: "run-1", recommendationCheckpointRepo: store}
	o.discardRecommendationCheckpoint(context.Background(), "completed")
	if len(store.deletes) != 1 || store.deletes[0] != "run-1" {
		t.Errorf("wrong delete: %v", store.deletes)
	}

	failing := &fakeRecommendationCheckpointStore{deleteErr: errors.New("mongo down")}
	(&Orchestrator{runID: "run-1", recommendationCheckpointRepo: failing}).
		discardRecommendationCheckpoint(context.Background(), "completed")

	// And the no-ops that would otherwise be a delete with a hole in it.
	(&Orchestrator{runID: "run-1"}).discardRecommendationCheckpoint(context.Background(), "completed")
	bare := &fakeRecommendationCheckpointStore{}
	(&Orchestrator{recommendationCheckpointRepo: bare}).discardRecommendationCheckpoint(context.Background(), "completed")
	if len(bare.deletes) != 0 {
		t.Error("deleted with no run id")
	}
}

func resumedOrchestratorWithRecCheckpoint(store recommendationCheckpointStore, explorationAttempt int) *Orchestrator {
	return &Orchestrator{
		projectID: "proj", runID: "run-1",
		recommendationCheckpointRepo: store,
		explorationSummaryAttempt:    explorationAttempt,
		resume: &ResumeState{Attempt: 2, Checkpoints: &database.CheckpointSet{
			Summary: &models.ExplorationCheckpointSummary{Completed: true, TotalSteps: 1, Attempt: explorationAttempt},
			Steps:   []models.ExplorationCheckpoint{{Step: models.ExplorationStep{Step: 1}}},
		}},
		statusReporter: reporterFor(newFakeRunDoc(), 2),
	}
}

// --- regressions ------------------------------------------------------------

// TestRecommendationStepInStoredIDs_KeepsTheCheckpointInOneIdSpace is the
// regression for a checkpoint that defeated its own gate.
//
// An attempt that restored a completed area reasons over REMINTED insight ids,
// so the recommender cites those — while the eligible set is recorded in the
// ids the checkpoints hold, because that is the only id space stable across
// attempts. Storing the citations unchanged mixed the two: the next resume
// passed the eligible-set check and then could not translate a single
// citation, refusing a row that described exactly the insights it held and
// re-paying for the generation call and every recommendation's validation.
func TestRecommendationStepInStoredIDs_KeepsTheCheckpointInOneIdSpace(t *testing.T) {
	// live-1 came from a restored completed area (stored as attempt1-id-1);
	// live-2 this attempt analysed itself, so its live id IS its stored id.
	storedByLive := map[string]string{"live-1": "attempt1-id-1"}
	step := recStepWith(
		rec("r1", "live-1"),
		rec("r2", "live-1", "live-2"),
		rec("r3"),
	)

	got := recommendationStepInStoredIDs(step, storedByLive)

	want := [][]string{{"attempt1-id-1"}, {"attempt1-id-1", "live-2"}, nil}
	for i, w := range want {
		gotIDs := got.Recommendations[i].RelatedInsightIDs
		if len(gotIDs) != len(w) {
			t.Fatalf("rec %d stored as %v, want %v", i, gotIDs, w)
		}
		for j := range w {
			if gotIDs[j] != w[j] {
				t.Errorf("rec %d citation %d = %q, want %q", i, j, gotIDs[j], w[j])
			}
		}
	}

	// The live recommendations must keep their live ids: Phase 5.5 validates
	// them and the result persists them.
	if step.Recommendations[0].RelatedInsightIDs[0] != "live-1" {
		t.Error("rewrote the live recommendations; the result would cite ids it does not contain")
	}

	// And a run that restored nothing is untouched, which is what keeps a
	// first attempt on exactly the path it always took.
	plain := recStepWith(rec("r1", "a"))
	if out := recommendationStepInStoredIDs(plain, nil); out.Recommendations[0].RelatedInsightIDs[0] != "a" {
		t.Error("an attempt that restored nothing must store its citations unchanged")
	}
}

// TestRecommendationCheckpoint_SurvivesTwoSuccessiveResumes closes the loop on
// the id-space regression end to end: a row written by an attempt that
// restored an area must still be restorable by the attempt after it.
func TestRecommendationCheckpoint_SurvivesTwoSuccessiveResumes(t *testing.T) {
	// Attempt 2 restored a completed area: stored "a1", live "a2".
	stored := recommendationStepInStoredIDs(
		recStepWith(rec("r1", "a2")),
		map[string]string{"a2": "a1"},
	)
	cp := &models.RecommendationCheckpoint{
		Step:               stored,
		EligibleInsightIDs: []string{"a1"},
	}

	// Attempt 3 restores the same area from the same untouched row: stored
	// "a1" again, live "a3" this time.
	got, ok := restorableRecommendations(cp, []models.Insight{{ID: "a3"}}, map[string]string{"a1": "a3"}, nil)
	if !ok {
		t.Fatal("attempt 3 refused a checkpoint whose insight it is holding — the id spaces are still mixed")
	}
	if got.Step.Recommendations[0].RelatedInsightIDs[0] != "a3" {
		t.Errorf("citation = %q, want this attempt's live id a3", got.Step.Recommendations[0].RelatedInsightIDs[0])
	}
}

// TestTranslateRecommendationInsightIDs_CoversFigureRefsNotJustCitations is
// the regression for the second place a recommendation names an insight.
//
// A recommendation's figures are its one grammar for numbers, and each ref
// names the insight whose figure it restates — the same id space
// related_insight_ids uses. Translating only the citations left the figure
// evidence pointing at the previous attempt's insights, so the result shipped
// provenance naming documents its own discovery did not contain, with the
// original verdicts still attached.
func TestTranslateRecommendationInsightIDs_CoversFigureRefsNotJustCitations(t *testing.T) {
	in := []models.Recommendation{
		{
			ID:                "r1",
			RelatedInsightIDs: []string{"stored-1"},
			Figures: []commonmodels.Figure{
				{ID: "f1", Refs: []commonmodels.FigureRef{
					{Insight: "stored-1", Figure: "f3"},
					{Insight: "stored-2", Figure: "f1"},
				}},
				{ID: "f2"}, // no refs
			},
		},
		{ID: "r2"}, // no citations, no figures
	}
	to := map[string]string{"stored-1": "live-1", "stored-2": "live-2"}

	out, missing := translateRecommendationInsightIDs(in, to)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if out[0].RelatedInsightIDs[0] != "live-1" {
		t.Errorf("citation = %q, want live-1", out[0].RelatedInsightIDs[0])
	}
	refs := out[0].Figures[0].Refs
	if refs[0].Insight != "live-1" || refs[1].Insight != "live-2" {
		t.Errorf("figure refs = %q/%q, want live-1/live-2", refs[0].Insight, refs[1].Insight)
	}
	if refs[0].Figure != "f3" {
		t.Error("rewrote the figure id within the insight; only the insight id is translated")
	}

	// Deep copy: nothing the caller handed over may be touched. A shallow
	// copy of the recommendation shares the Figures array, and a copy of a
	// Figure shares its Refs array.
	if in[0].RelatedInsightIDs[0] != "stored-1" {
		t.Error("rewrote the input's citations in place")
	}
	if in[0].Figures[0].Refs[0].Insight != "stored-1" {
		t.Error("rewrote the input's figure refs in place — a later attempt would translate from a moved baseline")
	}
}

// TestTranslateRecommendationInsightIDs_ReportsWhatItCouldNotTranslate — the
// signal the restore gate refuses on.
func TestTranslateRecommendationInsightIDs_ReportsWhatItCouldNotTranslate(t *testing.T) {
	in := []models.Recommendation{{
		ID:                "r1",
		RelatedInsightIDs: []string{"known", "ghost-cite"},
		Figures: []commonmodels.Figure{{ID: "f1", Refs: []commonmodels.FigureRef{
			{Insight: "ghost-ref"},
			{Insight: ""}, // an empty ref names nothing and is left alone
		}}},
	}}
	_, missing := translateRecommendationInsightIDs(in, map[string]string{"known": "live"})

	got := map[string]bool{}
	for _, id := range missing {
		got[id] = true
	}
	if !got["ghost-cite"] || !got["ghost-ref"] {
		t.Errorf("missing = %v, want both the untranslatable citation and the untranslatable figure ref", missing)
	}
	if got[""] {
		t.Error("reported an empty ref as missing")
	}
}

// TestTranslateRecommendationInsightIDs_IdentityOnAnEmptyMapping — a run that
// restored nothing names the ids its own checkpoints hold, so an empty
// mapping must pass every id through and report nothing missing.
func TestTranslateRecommendationInsightIDs_IdentityOnAnEmptyMapping(t *testing.T) {
	in := []models.Recommendation{{
		ID:                "r1",
		RelatedInsightIDs: []string{"a"},
		Figures:           []commonmodels.Figure{{ID: "f1", Refs: []commonmodels.FigureRef{{Insight: "b"}}}},
	}}
	out, missing := translateRecommendationInsightIDs(in, nil)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none on the identity mapping", missing)
	}
	if out[0].RelatedInsightIDs[0] != "a" || out[0].Figures[0].Refs[0].Insight != "b" {
		t.Error("the identity mapping changed an id")
	}
}

// TestRestorableRecommendations_RefusesAnUntranslatableFigureRef — a figure
// ref that cannot be translated is exactly as broken as a citation that
// cannot: its `holds` verdict would vouch for evidence the result does not
// contain, so the whole checkpoint is refused.
func TestRestorableRecommendations_RefusesAnUntranslatableFigureRef(t *testing.T) {
	step := recStepWith(rec("r1", "stored-1"))
	step.Recommendations[0].Figures = []commonmodels.Figure{
		{ID: "f1", Refs: []commonmodels.FigureRef{{Insight: "a-dropped-insight", Figure: "f2"}}},
	}
	cp := &models.RecommendationCheckpoint{Step: step, EligibleInsightIDs: []string{"stored-1"}}

	if _, ok := restorableRecommendations(cp, []models.Insight{{ID: "live-1"}}, map[string]string{"stored-1": "live-1"}, nil); ok {
		t.Error("restored a recommendation whose figure restates an insight this attempt does not have")
	}
}

// TestRestorableRecommendations_TranslatesFigureRefsOnTheAcceptPath
func TestRestorableRecommendations_TranslatesFigureRefsOnTheAcceptPath(t *testing.T) {
	step := recStepWith(rec("r1", "stored-1"))
	step.Recommendations[0].Figures = []commonmodels.Figure{
		{ID: "f1", Refs: []commonmodels.FigureRef{{Insight: "stored-1", Figure: "f2"}}},
	}
	cp := &models.RecommendationCheckpoint{Step: step, EligibleInsightIDs: []string{"stored-1"}}

	got, ok := restorableRecommendations(cp, []models.Insight{{ID: "live-1"}}, map[string]string{"stored-1": "live-1"}, nil)
	if !ok {
		t.Fatal("refused a checkpoint whose every reference resolves")
	}
	if ref := got.Step.Recommendations[0].Figures[0].Refs[0].Insight; ref != "live-1" {
		t.Errorf("figure ref = %q, want this attempt's live-1", ref)
	}
	// And the loaded row keeps its own baseline.
	if cp.Step.Recommendations[0].Figures[0].Refs[0].Insight != "stored-1" {
		t.Error("mutated the loaded checkpoint's figure refs")
	}
}

// TestRecommendationStepInStoredIDs_NormalizesFigureRefsToo — the other
// direction: what gets written has to be in one id space throughout, or the
// next resume refuses it.
func TestRecommendationStepInStoredIDs_NormalizesFigureRefsToo(t *testing.T) {
	step := recStepWith(rec("r1", "live-1"))
	step.Recommendations[0].Figures = []commonmodels.Figure{
		{ID: "f1", Refs: []commonmodels.FigureRef{{Insight: "live-1", Figure: "f2"}}},
	}
	got := recommendationStepInStoredIDs(step, map[string]string{"live-1": "attempt1-id-1"})

	if ref := got.Recommendations[0].Figures[0].Refs[0].Insight; ref != "attempt1-id-1" {
		t.Errorf("stored figure ref = %q, want the checkpoint's id", ref)
	}
	if step.Recommendations[0].Figures[0].Refs[0].Insight != "live-1" {
		t.Error("rewrote the live recommendation's figure refs; the result would cite ids it does not contain")
	}
}

// TestCheckpointRecommendations_ReportsWhetherTheRowLanded — the bool gates
// the per-recommendation marker writes, for the reason the draft's does: a
// marker with no row of its own can land on an earlier attempt's, where a
// reused model-supplied id would overwrite a marker describing a different
// recommendation.
func TestCheckpointRecommendations_ReportsWhetherTheRowLanded(t *testing.T) {
	ctx := context.Background()
	step := recStepWith(rec("r1", "i1"))

	t.Run("a landed write reports true", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			recommendationCheckpointRepo: &fakeRecommendationCheckpointStore{},
			explorationSummaryAttempt:    1,
			statusReporter:               reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointRecommendations(ctx, &step, []string{"i1"}, nil)
		if err != nil {
			t.Fatalf("checkpointRecommendations: %v", err)
		}
		if !got {
			t.Error("reported no row for a write that succeeded, so Phase 5.5's markers would never be written")
		}
	})

	t.Run("a swallowed failure reports false", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			recommendationCheckpointRepo: &fakeRecommendationCheckpointStore{saveErr: errors.New("mongo down")},
			explorationSummaryAttempt:    1,
			statusReporter:               reporterFor(newFakeRunDoc(), 1),
		}
		got, err := o.checkpointRecommendations(ctx, &step, []string{"i1"}, nil)
		if err != nil {
			t.Fatalf("a failed checkpoint must not abort the run, got %v", err)
		}
		if got {
			t.Error("reported a row that is not on disk; its markers could land on an earlier attempt's")
		}
	})

	t.Run("a phase that banked nothing reports false", func(t *testing.T) {
		o := &Orchestrator{
			projectID: "p", runID: "r",
			recommendationCheckpointRepo: &fakeRecommendationCheckpointStore{},
			explorationSummaryAttempt:    1,
			statusReporter:               reporterFor(newFakeRunDoc(), 1),
		}
		failed := models.RecommendationStep{Error: "llm provider 503"}
		if got, err := o.checkpointRecommendations(ctx, &failed, nil, nil); err != nil || got {
			t.Errorf("got (%v, %v), want (false, nil)", got, err)
		}
	})
}

// TestRestorableRecommendations_RefusesADifferentAnalysisGeneration is the
// regression the fingerprint exists for, in the shape that defeated every
// id-based check: a re-analysis replaces the area's checkpoint, so a LATER
// attempt sees a checkpoint-backed id matching a recommendation set reasoned
// over the content that re-analysis replaced.
func TestRestorableRecommendations_RefusesADifferentAnalysisGeneration(t *testing.T) {
	cp := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("r1", "stored-1")),
		EligibleInsightIDs: []string{"stored-1"},
		AnalysisGeneration: []string{"churn@1000"},
	}
	eligible := []models.Insight{{ID: "live-1", AnalysisArea: "churn"}}
	liveByStored := map[string]string{"stored-1": "live-1"}

	// Every id lines up — and the analysis behind them does not.
	if _, ok := restorableRecommendations(cp, eligible, liveByStored, []string{"churn@2000"}); ok {
		t.Error("restored recommendations reasoned over a different analysis of the same area")
	}
	// The same generation restores.
	if _, ok := restorableRecommendations(cp, eligible, liveByStored, []string{"churn@1000"}); !ok {
		t.Error("refused a checkpoint whose analysis generation matches")
	}
	// And a row written before the fingerprint existed carries none, so it is
	// refused rather than trusted.
	legacy := &models.RecommendationCheckpoint{
		Step:               recStepWith(rec("r1", "stored-1")),
		EligibleInsightIDs: []string{"stored-1"},
	}
	if _, ok := restorableRecommendations(legacy, eligible, liveByStored, []string{"churn@1000"}); ok {
		t.Error("restored a checkpoint that cannot say which analysis it came from")
	}
}
