package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// feed drives a rule through a sequence of observations, written as the
// engine sees them: 'r' a step that repeated earlier work, 'n' a novel step,
// '?' a step novelty could not be judged on.
func feed(s *stopRule, seq string) {
	for _, c := range seq {
		switch c {
		case 'r':
			s.observe(true, true)
		case 'n':
			s.observe(false, true)
		case '?':
			s.observe(false, false)
		default:
			panic("unknown observation " + string(c))
		}
	}
}

// TestStopRule_FloorRunIsUnchanged is the regression guard for every SQL
// deployment: a run that does not use the novelty rule must behave exactly as
// the step floor always has, whatever it observed along the way.
func TestStopRule_FloorRunIsUnchanged(t *testing.T) {
	for _, seq := range []string{"", "rrrrrr", "nnn", "??????"} {
		s := &stopRule{minSteps: 5}
		feed(s, seq)
		if ok, reason := s.acceptDone(4); ok || reason != "floor" {
			t.Errorf("seq %q: done at step 4 under a floor of 5 = (%v, %q), want refused by the floor", seq, ok, reason)
		}
		if ok, _ := s.acceptDone(5); !ok {
			t.Errorf("seq %q: done at step 5 under a floor of 5 was refused", seq)
		}
	}
}

// TestStopRule_NoFloorAcceptsImmediately pins today's default. With no floor
// configured and no novelty rule, the engine has always taken the model at
// its word — this is the behaviour the cube rule must not inherit.
func TestStopRule_NoFloorAcceptsImmediately(t *testing.T) {
	s := &stopRule{}
	if ok, _ := s.acceptDone(1); !ok {
		t.Error("a run with no floor and no novelty rule should accept a completion at step 1")
	}
}

// TestStopRule_NoveltyRefusesAnEarlyCompletionWithNoFloor is the hole the
// rule exists to close. min-steps defaults to zero, so a cube run that
// deferred to the floor when it had not yet judged enough steps would accept
// a completion signalled on step one — reintroducing exactly the early
// termination the floor was added to prevent.
func TestStopRule_NoveltyRefusesAnEarlyCompletionWithNoFloor(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "?") // the first step has nothing to be similar to
	ok, reason := s.acceptDone(1)
	if ok {
		t.Fatal("a cube run accepted a completion on step 1")
	}
	if reason != "unproven" {
		t.Errorf("refusal reason = %q, want the novelty rule to own the decision", reason)
	}
}

// TestStopRule_NoveltyAcceptsOnceTheRunRepeatsItself is the rule's whole
// purpose: the exploration stops because it stopped finding anything, at
// whatever step that happens to be.
func TestStopRule_NoveltyAcceptsOnceTheRunRepeatsItself(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 40}
	feed(s, "?nnrrr")
	if ok, reason := s.acceptDone(6); !ok {
		t.Errorf("three repeats in a row did not end the run: (%v, %q)", ok, reason)
	}
}

// TestStopRule_NoveltyIgnoresTheFloorInBothDirections is what "replace the
// floor" means. A cube run neither stops because it hit a number nor keeps
// going because it has not.
func TestStopRule_NoveltyIgnoresTheFloorInBothDirections(t *testing.T) {
	// Well past a floor of 3, but still finding new things: keep going.
	still := &stopRule{byNovelty: true, minSteps: 3}
	feed(still, "nnnnnnnnnn")
	if ok, _ := still.acceptDone(10); ok {
		t.Error("a run still turning up new ground was allowed to stop because it passed the floor")
	}
	// Well short of a floor of 40, but repeating itself: stop.
	spent := &stopRule{byNovelty: true, minSteps: 40}
	feed(spent, "nrrr")
	if ok, _ := spent.acceptDone(4); !ok {
		t.Error("a run that had run dry was forced onward because it had not reached the floor")
	}
}

// TestStopRule_ANovelStepBreaksTheStreak covers the case that decides whether
// this is a marginal-value rule or a counter: two repeats then a discovery
// means the run is still productive, and the count starts again.
func TestStopRule_ANovelStepBreaksTheStreak(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "nrrnr")
	if ok, _ := s.acceptDone(5); ok {
		t.Error("a novel step between repeats did not reset the streak")
	}
}

// TestStopRule_UnjudgeableStepsAreNotRepeats is the failure this rule could
// most easily have had. Three steps novelty could not be measured on are not
// three repetitions, and reading them as such would end runs on a silent
// measurement failure — with a full set of insights that were never sought.
func TestStopRule_UnjudgeableStepsAreNotRepeats(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "nnn???")
	if ok, _ := s.acceptDone(6); ok {
		t.Error("steps that could not be judged were counted as repetitions")
	}
}

// TestStopRule_AnUnmeasurableRunFallsBackToTheFloor is the other side of that
// coin. If novelty is never measurable — a vector store that is down — the
// rule must stand down rather than refuse every completion until the runaway
// cap, which would turn a degraded index into a maximum-length run against a
// source metered per request.
func TestStopRule_AnUnmeasurableRunFallsBackToTheFloor(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 5}
	feed(s, "???")
	if ok, reason := s.acceptDone(4); ok || reason != "floor" {
		t.Errorf("with novelty unmeasurable, step 4 under a floor of 5 = (%v, %q), want the floor to decide", ok, reason)
	}
	if ok, _ := s.acceptDone(5); !ok {
		t.Error("with novelty unmeasurable, the floor should still let the run finish")
	}
}

// TestStopRule_OneJudgedStepKeepsTheRuleArmed pins the boundary between the
// two cases above: the machinery having worked once proves it is there, so
// later unmeasurable steps must not be read as it being broken.
func TestStopRule_OneJudgedStepKeepsTheRuleArmed(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 1}
	feed(s, "n????????")
	if ok, reason := s.acceptDone(9); ok || reason != "unproven" {
		t.Errorf("after a successful judgement the rule handed back to the floor: (%v, %q)", ok, reason)
	}
}

// countingIndexer scripts Nearest so the engine's novelty plumbing can be
// exercised without a vector store.
type countingIndexer struct {
	scores []float64
	found  []bool
	err    error
	calls  int
	upsert int
}

func (c *countingIndexer) Upsert(context.Context, models.ExplorationStep) error {
	c.upsert++
	return nil
}

func (c *countingIndexer) Nearest(context.Context, models.ExplorationStep) (float64, bool, error) {
	i := c.calls
	c.calls++
	if c.err != nil {
		return 0, false, c.err
	}
	if i >= len(c.scores) {
		return 0, false, nil
	}
	return c.scores[i], c.found[i], nil
}

// TestRepeatsEarlierWork_ThresholdAndFailureModes covers the engine's reading
// of one novelty measurement. The failure modes matter more than the happy
// path: every one of them must come back unjudgeable, because "could not
// tell" read as "not similar" keeps a spent run going and read as "similar"
// ends a productive one.
func TestRepeatsEarlierWork_ThresholdAndFailureModes(t *testing.T) {
	step := models.ExplorationStep{Step: 4, Query: "SELECT 1", QueryPurpose: "count"}

	cases := map[string]struct {
		engine              *ExplorationEngine
		repeated, judgeable bool
	}{
		"clearly the same request": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0.97}, found: []bool{true}}},
			repeated: true, judgeable: true,
		},
		"a different question": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0.40}, found: []bool{true}}},
			repeated: false, judgeable: true,
		},
		"exactly at the threshold counts as a repeat": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{repeatSimilarityThreshold}, found: []bool{true}}},
			repeated: true, judgeable: true,
		},
		"nothing indexed yet": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0}, found: []bool{false}}},
			repeated: false, judgeable: false,
		},
		"the index failed": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{err: errors.New("qdrant unreachable")}},
			repeated: false, judgeable: false,
		},
		"no index configured": {
			engine:   &ExplorationEngine{},
			repeated: false, judgeable: false,
		},
	}
	for name, tc := range cases {
		repeated, judgeable := tc.engine.repeatsEarlierWork(context.Background(), step)
		if repeated != tc.repeated || judgeable != tc.judgeable {
			t.Errorf("%s: got (repeated=%v, judgeable=%v), want (%v, %v)", name, repeated, judgeable, tc.repeated, tc.judgeable)
		}
	}
}

// TestRejectionFor_NeverClaimsARunIsRepeatingItself covers a refusal that
// used to lie. A run that repeats itself is ACCEPTED, so a refusal can never
// truthfully say "you have been re-asking answered questions" — yet that was
// the wording on the main productive path, alongside a recorded reason that
// read "last 0 steps repeated earlier work".
func TestRejectionFor_NeverClaimsARunIsRepeatingItself(t *testing.T) {
	e := &ExplorationEngine{minSteps: 12, stop: stopRule{judged: 5, consecutiveRepeats: 1}}

	for _, reason := range []string{"productive", "unproven"} {
		nudge, recorded := e.rejectionFor(reason, 4)
		if strings.Contains(nudge, "already answered") || strings.Contains(nudge, "repeat") {
			t.Errorf("%s nudge tells the model it is repeating itself: %s", reason, nudge)
		}
		if !strings.Contains(nudge, "has not looked at") && !strings.Contains(nudge, "no earlier step") {
			t.Errorf("%s nudge does not ask for new ground: %s", reason, nudge)
		}
		if strings.Contains(nudge, "minimum") {
			t.Errorf("%s nudge asks for a step count, which this run does not have: %s", reason, nudge)
		}
		if recorded == "" {
			t.Errorf("%s refusal recorded no reason for an operator to read", reason)
		}
	}
}

// TestRejectionFor_DoesNotTeachTheModelHowToStopTheRun is a prompt-design
// constraint, not a wording preference. Spelling out "three repeated steps
// end the exploration" hands the model a cheaper way to satisfy the rule than
// exploring: ask one question three times and go home.
func TestRejectionFor_DoesNotTeachTheModelHowToStopTheRun(t *testing.T) {
	e := &ExplorationEngine{stop: stopRule{judged: 5}}
	nudge, _ := e.rejectionFor("productive", 4)
	for _, leak := range []string{"three", "3 ", "consecutive", "repeat"} {
		if strings.Contains(strings.ToLower(nudge), leak) {
			t.Errorf("the nudge describes the stopping rule (%q), which invites gaming it: %s", leak, nudge)
		}
	}
}

// TestRejectionFor_TheFloorStillNamesItsNumber keeps the other refusal
// unchanged: the floor HAS a target and the model can act on being told it.
func TestRejectionFor_TheFloorStillNamesItsNumber(t *testing.T) {
	e := &ExplorationEngine{minSteps: 12}
	nudge, recorded := e.rejectionFor("floor", 4)
	if !strings.Contains(nudge, "minimum 12") {
		t.Errorf("floor nudge does not name the floor: %s", nudge)
	}
	if !strings.Contains(recorded, "4 < 12") {
		t.Errorf("recorded floor reason %q does not show the shortfall", recorded)
	}
}

// TestRepeatsEarlierWork_AFailedQueryIsNotEvidence is the defect that mattered
// most in this pass. A rejected or failing query still carries its text, so it
// was scored like any other step — and the same broken request asked three
// times built a repeat streak and accepted the next completion, ending a run
// that had never successfully read anything.
func TestRepeatsEarlierWork_AFailedQueryIsNotEvidence(t *testing.T) {
	idx := &countingIndexer{scores: []float64{0.99}, found: []bool{true}}
	e := &ExplorationEngine{stepIndexer: idx}

	repeated, judgeable := e.repeatsEarlierWork(context.Background(), models.ExplorationStep{
		Step: 3, Query: "SELECT broken", QueryPurpose: "count", Error: "syntax error at or near broken",
	})
	if repeated || judgeable {
		t.Errorf("a failed query was judged: (repeated=%v, judgeable=%v)", repeated, judgeable)
	}
	if idx.calls != 0 {
		t.Errorf("a failed query was sent to the index anyway (%d calls)", idx.calls)
	}
}

// TestStopRule_ARunOfFailingQueriesFallsBackToTheFloor is the consequence of
// treating a failed query as unmeasurable rather than skipping it. A run whose
// every query fails can never accumulate a judgement, and must hand the
// decision back to the floor rather than refuse every completion until the
// runaway cap — which on a metered source is the expensive way to fail.
func TestStopRule_ARunOfFailingQueriesFallsBackToTheFloor(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 5}
	feed(s, "???") // three attempted queries, none judgeable
	if ok, reason := s.acceptDone(6); !ok || reason != "" {
		t.Errorf("a run of failing queries was not handed back to the floor: (%v, %q)", ok, reason)
	}
}
