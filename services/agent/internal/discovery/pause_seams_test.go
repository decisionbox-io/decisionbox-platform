package discovery

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// One rule, enforced structurally: a pause stops wherever a crash would have
// left a resumable run, which means EVERY durable checkpoint write is a pause
// seam — the uniform pause rule.
//
// This is the test the issue asks for by name — "a new checkpoint write added
// later without a pause check fails a test, so the rule cannot silently rot
// back to a seam inventory". It parses the package rather than listing the
// seams, so a seam added next year is covered without anyone remembering to
// come back here.
func TestEveryCheckpointSeamIsAPauseSeam(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	// A seam is exempt only for a reason that is written down here. Both
	// exemptions are the same reason: the function does not decide anything
	// about standing, it is the shared machinery the seams call INTO.
	exempt := map[string]string{}

	type seam struct{ name, body string }
	var seams []seam
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			// Methods on *Orchestrator whose name begins with "checkpoint".
			if !strings.HasPrefix(fn.Name.Name, "checkpoint") {
				continue
			}
			recvOK := false
			for _, fld := range fn.Recv.List {
				if star, ok := fld.Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok && id.Name == "Orchestrator" {
						recvOK = true
					}
				}
			}
			if !recvOK {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			seams = append(seams, seam{name: fn.Name.Name, body: body})
		}
	}

	// Guard the guard: if the AST walk stops finding seams (a rename, a move
	// to another package) this test must fail loudly rather than vacuously
	// pass over an empty list.
	if len(seams) < 7 {
		t.Fatalf("found only %d checkpoint seams; the walk is broken or seams moved out of this package — a vacuous pass here would hide every missing pause check", len(seams))
	}

	for _, s := range seams {
		if why, ok := exempt[s.name]; ok {
			t.Logf("%s exempt: %s", s.name, why)
			continue
		}
		// Found BEFORE the write: the seam must read standing, not ownership
		// alone. OwnsRun answers half the question and cannot see a pause.
		if !strings.Contains(s.body, "Standing(ctx)") {
			t.Errorf("%s writes a durable checkpoint but never reads Standing(ctx), so a pause requested before it is not honoured after it — that is the uniform pause rule. Add the find-before-write/stop-after-write pattern, or add it to `exempt` with a reason.", s.name)
		}
		// Taken AFTER the write, and only through the shared guard, which is
		// what refuses to strand a run whose exploration prefix has a gap.
		if !strings.Contains(s.body, "pauseSignalIfResumable") {
			t.Errorf("%s reads standing but never signals through pauseSignalIfResumable, so it either ignores a pause or takes one without checking there is anything to resume from — that is the uniform pause rule.", s.name)
		}
	}
}

// The per-item seam is the one the issue was filed over: a pause arriving
// between two verdicts used to wait for the whole area.
func TestCheckpointInsightVerdict_PauseSeam(t *testing.T) {
	ctx := t.Context()

	t.Run("a landed verdict with a pause standing signals the stop", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}
		o.contiguousCheckpointThrough = 9

		err := o.checkpointInsightVerdict(ctx, "churn", models.ValidationResult{InsightID: "i1"})
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointInsightVerdict() = %v, want ErrPauseRequested", err)
		}
		// The row lands FIRST. A pause that skipped the write would make the
		// resumed attempt pay for this verdict again, which is the opposite of
		// the point.
		if len(store.verdicts) != 1 || store.verdicts[0].InsightID != "i1" {
			t.Fatalf("verdicts = %+v, want one row for i1 before the stop", store.verdicts)
		}
	})

	t.Run("a failed write does not pause, so the verdict is not lost", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisDraftStore{verdictErr: errors.New("mongo is down")}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}
		o.contiguousCheckpointThrough = 9

		if err := o.checkpointInsightVerdict(ctx, "churn", models.ValidationResult{InsightID: "i1"}); err != nil {
			t.Fatalf("checkpointInsightVerdict() = %v, want nil — with no marker on disk a pause would re-charge this verdict", err)
		}
	})

	t.Run("no pause requested, no signal", func(t *testing.T) {
		doc := newFakeRunDoc()
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
		}
		if err := o.checkpointInsightVerdict(ctx, "churn", models.ValidationResult{InsightID: "i1"}); err != nil {
			t.Fatalf("checkpointInsightVerdict() = %v, want nil", err)
		}
	})
}

func TestCheckpointRecommendationVerdict_PauseSeam(t *testing.T) {
	ctx := t.Context()

	t.Run("a landed verdict with a pause standing signals the stop", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeRecommendationCheckpointStore{}
		o := &Orchestrator{
			runID:                        "run-1",
			statusReporter:               reporterFor(doc, 1),
			recommendationCheckpointRepo: store,
			explorationSummaryAttempt:    1,
			attemptStartedAt:             time.Now(),
		}
		o.contiguousCheckpointThrough = 9

		err := o.checkpointRecommendationVerdict(ctx, models.ValidationResult{InsightID: "r1"})
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointRecommendationVerdict() = %v, want ErrPauseRequested", err)
		}
		if len(store.verdicts) != 1 {
			t.Fatalf("verdicts = %+v, want one row before the stop", store.verdicts)
		}
	})

	t.Run("a failed write does not pause", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeRecommendationCheckpointStore{verdictErr: errors.New("mongo is down")}
		o := &Orchestrator{
			runID:                        "run-1",
			statusReporter:               reporterFor(doc, 1),
			recommendationCheckpointRepo: store,
			explorationSummaryAttempt:    1,
			attemptStartedAt:             time.Now(),
		}
		o.contiguousCheckpointThrough = 9
		if err := o.checkpointRecommendationVerdict(ctx, models.ValidationResult{InsightID: "r1"}); err != nil {
			t.Fatalf("checkpointRecommendationVerdict() = %v, want nil", err)
		}
	})
}

func TestCheckpointAreaDraft_PauseSeam(t *testing.T) {
	ctx := t.Context()
	step := models.AnalysisStep{AreaID: "churn", Insights: []models.Insight{{ID: "i1"}}}

	t.Run("the draft lands, then the stop is signalled", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisDraftStore{}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}
		o.contiguousCheckpointThrough = 12

		landed, err := o.checkpointAreaDraft(ctx, step, true)
		if !errors.Is(err, ai.ErrPauseRequested) {
			t.Fatalf("checkpointAreaDraft() err = %v, want ErrPauseRequested", err)
		}
		// True, because the row is on disk — the caller still discards nothing
		// and the resumed attempt re-enters at the first unvalidated insight.
		if !landed {
			t.Error("landed = false though the draft was written; the caller would not know it can skip validation")
		}
		if len(store.saved) != 1 {
			t.Fatalf("saved = %+v, want the draft written before the stop", store.saved)
		}
	})

	t.Run("a failed draft write refuses the pause too", func(t *testing.T) {
		doc := newFakeRunDoc()
		doc.pauseRequested = true
		store := &fakeAnalysisDraftStore{saveErr: errors.New("mongo is down")}
		o := &Orchestrator{
			runID:                     "run-1",
			statusReporter:            reporterFor(doc, 1),
			analysisDraftRepo:         store,
			explorationSummaryAttempt: 1,
			attemptStartedAt:          time.Now(),
		}
		o.contiguousCheckpointThrough = 12

		landed, err := o.checkpointAreaDraft(ctx, step, true)
		if err != nil {
			t.Fatalf("checkpointAreaDraft() err = %v, want nil — stopping with no draft discards the analysis call", err)
		}
		if landed {
			t.Error("landed = true after a failed write")
		}
	})
}

func TestCheckpointRecommendations_PauseSeam(t *testing.T) {
	ctx := t.Context()
	step := &models.RecommendationStep{}

	doc := newFakeRunDoc()
	doc.pauseRequested = true
	store := &fakeRecommendationCheckpointStore{}
	o := &Orchestrator{
		runID:                        "run-1",
		statusReporter:               reporterFor(doc, 1),
		recommendationCheckpointRepo: store,
		explorationSummaryAttempt:    1,
		attemptStartedAt:             time.Now(),
	}
	o.contiguousCheckpointThrough = 12

	landed, err := o.checkpointRecommendations(ctx, step, []string{"i1"}, nil)
	if !errors.Is(err, ai.ErrPauseRequested) {
		t.Fatalf("checkpointRecommendations() err = %v, want ErrPauseRequested", err)
	}
	if !landed {
		t.Error("landed = false though the checkpoint was written")
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved = %+v, want Phase 5 recorded before the stop", store.saved)
	}
}

// runBounded is where the per-item pause actually takes effect, and its
// contract has one sharp edge: `stop` is consulted before a dispatch and
// nowhere else, so a document either never starts or finishes and banks its
// verdict. A half-paid check is the one thing a cooperative stop must not
// create.
func TestRunBounded_StopIsConsultedOnlyBeforeDispatch(t *testing.T) {
	for _, concurrency := range []int{1, 4} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			p := &validationPhase{}
			p.caps.Concurrency = concurrency

			var ran counter
			const stopAfter = 3
			dispatched := p.runBounded(10, func(int) { ran.inc() }, func() bool { return ran.count() >= stopAfter })

			if dispatched >= 10 {
				t.Fatalf("dispatched = %d; the stop was never honoured", dispatched)
			}
			if dispatched < stopAfter {
				t.Fatalf("dispatched = %d, want at least %d before the stop could be seen", dispatched, stopAfter)
			}
			// Every dispatched call ran to completion: the count the workers
			// kept and the count runBounded reports must agree. If they did
			// not, a stop had abandoned work already in flight — which is the
			// half-paid verdict the contract forbids.
			if ran.count() != dispatched {
				t.Errorf("ran %d calls but runBounded reported %d dispatched; a stop dropped work mid-flight", ran.count(), dispatched)
			}
		})
	}
}

// counter is a mutex-guarded int. The workers are concurrent, so a plain one
// would make this test the race detector's problem rather than the scheduler's.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// The acceptance criterion in the issue's own words: "A pause taken
// mid-validation leaves the area's draft and its verdict markers on disk; the
// resumed attempt validates only the remaining M−J and re-analyses nothing."
//
// This is the half that could not be done by copying checkpointAnalysisArea's
// pattern. The verdict seam runs inside the bounded worker, so the pause is
// remembered on the shared checkpoint and read by the dispatch loop rather
// than returned up a stack that is not there.
func TestValidateInsights_HonoursAPauseBetweenTwoVerdicts(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 6 // the cap must not be what stops this

	// The seam reports a pause once two verdicts are durable.
	cp := &recordingCheckpoint{pauseAfter: 2}

	results, validated, pausedEarly := p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if !pausedEarly {
		t.Fatal("pausedEarly = false; the phase validated all 6 insights despite a pause standing after the 2nd")
	}
	// Stopped, and not on the area boundary: before the uniform rule this
	// used to run to the end of the area.
	if len(results) >= len(insights) {
		t.Errorf("got %d results for %d insights; the pause did not stop the dispatch", len(results), len(insights))
	}
	// The run's validation budget reflects what was actually spent, not what
	// was queued. runValidated is charged at queue time, so an early stop has
	// to give the unspent reservations back — otherwise the count claims
	// validations this attempt never made.
	if validated != len(results) {
		t.Errorf("budget says %d validations, but only %d results came back; the undispatched reservations were not returned", validated, len(results))
	}
	// Every verdict that was produced is on disk. "Produced but not banked"
	// is the state a pause must never create — the resumed attempt would pay
	// for it again.
	saved := cp.savedIDs()
	if len(saved) != len(results) {
		t.Errorf("%d results but %d verdicts persisted; a verdict was produced without being banked", len(results), len(saved))
	}
	// And nothing unchecked is passed off as checked: a slot whose verifier
	// never ran must not reach the caller carrying an empty verdict.
	for _, vr := range results {
		if vr.InsightID == "" {
			t.Error("a result with no insight id reached the caller; an unvalidated slot was returned as if it had been checked")
		}
		if vr.Combined == "" && vr.Status == "" {
			t.Errorf("result for %q carries no verdict at all", vr.InsightID)
		}
	}
	// The insights the pause skipped keep no Validation, which is exactly what
	// a resume looks for: "re-enter at the first insight without a verdict".
	withVerdict := 0
	for i := range insights {
		if insights[i].Validation != nil {
			withVerdict++
		}
	}
	if withVerdict == len(insights) {
		t.Error("every insight ended up with a verdict; the resumed attempt would have nothing left to do and the pause saved nothing")
	}
}

// Same seam on the recommendation side: "A pause taken between Phase 5 and 5.5
// leaves the recommendation checkpoint and its rec verdicts; the resumed
// attempt regenerates nothing."
func TestValidateRecommendations_HonoursAPauseBetweenTwoVerdicts(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxRecommendationsPerRun = 15

	recs := make([]models.Recommendation, 5)
	for i := range recs {
		recs[i] = models.Recommendation{
			ID:                fmt.Sprintf("r%d", i),
			Title:             fmt.Sprintf("do thing %d", i),
			RelatedInsightIDs: []string{insights[0].ID},
		}
	}
	cp := &recordingCheckpoint{pauseAfter: 2}

	results, pausedEarly := p.validateRecommendations(context.Background(), recs, insights, steps, cp.hook())

	if !pausedEarly {
		t.Fatal("pausedEarly = false; Phase 5.5 validated every recommendation despite a pause standing after the 2nd")
	}
	if len(results) >= len(recs) {
		t.Errorf("got %d results for %d recommendations; the dispatch did not stop", len(results), len(recs))
	}
	if saved := cp.savedIDs(); len(saved) != len(results) {
		t.Errorf("%d results but %d verdicts persisted", len(results), len(saved))
	}
}
